//! Lowering: a function's THIR into MIR, in one walk (docs/ir-design.md).
//! The checks on the MIR are in the `flow` crate.

mod expression;
mod ranges;
mod slices;
mod statement;

use std::num::NonZeroU32;

use arena::Arena;
use arena::Id;
use arena::Lists;
use bigint::Limbs;
use model::constant::Constant;
use model::constant::Value;
use model::mir;
use model::operators::BinaryOp;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir;
use source::Span;

/// The MIR of a function's checked body, which has `parameters` parameters
/// and gives `result`; `usize` is the target's pointer-sized unsigned type.
/// `None` when checking it reported an error (`has_errors`), or an error left
/// it incomplete: what was reported is all there is to say about it. Its
/// types, from `types`, are each the type itself: THIR's aliases are looked
/// through (`Types::real`).
pub fn lower(
    body: &thir::Body,
    parameters: usize,
    result: TypeId,
    usize: TypeId,
    types: &Types,
) -> Option<mir::Body> {
    if body.has_errors {
        return None;
    }
    let mut lowering = Lowering {
        thir: body,
        types,
        usize,
        mir: mir::Body {
            blocks: Arena::default(),
            instructions: Arena::default(),
            variables: Arena::default(),
            parameters,
            result: types.real(result),
            debug_scopes: Arena::default(),
            constants: Vec::new(),
            subslices: Vec::new(),
            lists: Lists::default(),
        },
        block: Id::from_index(0),
        instructions: Vec::new(),
        debug_scope: Id::from_index(0),
        loops: Vec::new(),
        declared: Vec::new(),
    };
    // One debug scope per block and one MIR variable per THIR variable, in
    // order: block `i` is debug scope `i`, and the ids of variables match.
    for block in body.blocks.ids() {
        let parent = body.blocks[block]
            .parent
            .map(|parent| Id::from_index(parent.index()));
        let span = body.blocks.span(block);
        lowering
            .mir
            .debug_scopes
            .push(mir::DebugScope { parent }, span);
    }
    for id in body.variables.ids() {
        let variable = body.variables[id];
        if variable.ty == TypeId::INVALID {
            return None;
        }
        lowering.mir.variables.push(
            mir::Variable {
                ty: types.real(variable.ty),
                mutable: variable.mutable,
                initialized: variable.initialized,
                debug_scope: Id::from_index(variable.scope.index()),
                hidden: variable.hidden,
            },
            body.variables.span(id),
        );
    }
    let function = Id::from_index(0); // the function's block
    let span = body.blocks.span(function);
    lowering.block = lowering.new_block(span);
    for index in 0..parameters {
        let variable = Id::from_index(index);
        let place = mir::Place::whole(variable);
        let value = mir::Operand::Argument(index as u32);
        let store = mir::InstructionKind::Store { place, value };
        let span = lowering.mir.variables.span(variable);
        lowering.emit(store, TypeId::VOID, span);
        lowering.declared.push(variable);
    }
    lowering.lower_block(function, 0)?; // the parameters are its variables too
    let end = Span::new(span.end as usize - 1, span.end as usize); // the `}`
    lowering.finish(mir::Terminator::FallOff(end));
    Some(lowering.mir)
}

struct Lowering<'t> {
    thir: &'t thir::Body,
    types: &'t Types, // to look through aliases
    usize: TypeId,    // what slice bounds become once checked
    mir: mir::Body,
    block: Id<mir::Block>,            // the block being filled
    instructions: Vec<NonZeroU32>,    // that block's instructions so far
    debug_scope: Id<mir::DebugScope>, // the one new instructions are written in
    loops: Vec<Loop>,                 // the loops being lowered, innermost last
    /// The variables in scope, in the order declared: those of a block,
    /// from where it starts (`lower_block`), are the last.
    declared: Vec<Id<mir::Variable>>,
}

/// Where a loop's `continue` and `break` go, and where the variables they
/// leave start in `declared`.
struct Loop {
    next: Id<mir::Block>, // the start of the next pass
    exit: Id<mir::Block>,
    declared: usize,
}

impl Lowering<'_> {
    /// A new block starting at `span`; `finish` gives it its instructions and
    /// terminator.
    fn new_block(&mut self, span: Span) -> Id<mir::Block> {
        let block = mir::Block {
            instructions: arena::List::EMPTY,
            terminator: mir::Terminator::FallOff(span), // replaced by `finish`
            debug_scope: self.debug_scope,
        };
        self.mir.blocks.push(block, span)
    }

    /// Ends the block being filled with `terminator`.
    fn finish(&mut self, terminator: mir::Terminator) {
        let instructions = self.mir.lists.add(&self.instructions);
        self.instructions.clear();
        self.mir.blocks[self.block] = mir::Block {
            instructions,
            terminator,
            debug_scope: self.debug_scope,
        };
    }

    /// Adds an instruction to the block being filled and gives its value.
    // Called for every instruction. The inliner dropped it when unrelated code
    // left this crate, costing a compile 10 M instructions; `#[inline]` did not
    // bring it back.
    #[inline(always)]
    fn emit(&mut self, kind: mir::InstructionKind, ty: TypeId, span: Span) -> mir::Operand {
        let instruction = self.mir.instructions.push(
            mir::Instruction {
                ty: self.types.real(ty),
                kind,
                debug_scope: self.debug_scope,
            },
            span,
        );
        self.instructions.push(instruction.into());
        mir::Operand::Value(instruction)
    }

    /// Adds `left op right`, of type `ty`, to the block being filled.
    fn binary(
        &mut self,
        op: BinaryOp,
        left: mir::Operand,
        right: mir::Operand,
        ty: TypeId,
        span: Span,
    ) -> mir::Operand {
        self.emit(mir::InstructionKind::Binary { op, left, right }, ty, span)
    }

    /// Stops the program with "panic: `message`" when `failed`, a `bool`
    /// written at `span`, is true.
    fn check(&mut self, failed: mir::Operand, message: &'static str, span: Span) {
        self.emit(
            mir::InstructionKind::Check { failed, message },
            TypeId::VOID,
            span,
        );
    }

    /// `constant` as an operand: the body's next constant.
    fn add_constant(&mut self, constant: Constant) -> mir::Operand {
        let id = Id::from_index(self.mir.constants.len());
        let ty = self.types.real(constant.ty);
        self.mir.constants.push(Constant { ty, ..constant });
        mir::Operand::Constant(id)
    }

    /// The integer `value`, of the integer type `ty`, as an operand.
    fn integer_constant(&mut self, ty: TypeId, value: u64) -> mir::Operand {
        let Type::Integer(integer) = self.types[ty] else {
            unreachable!("an integer constant has an integer type")
        };
        self.add_constant(Constant {
            ty,
            value: Value::Integer(Limbs::new(integer.bits, &[value])),
        })
    }
}
