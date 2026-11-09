//! Lowering, which is core: it builds each function's MIR from its THIR and
//! gives features the few moves they combine. Whether reading a place
//! copies or moves comes from its type's facts, never from its feature.

use model::mir::Block;
use model::mir::BlockId;
use model::mir::Body;
use model::mir::Instruction;
use model::mir::InstructionId;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Place;
use model::mir::Program;
use model::mir::Terminator;
use model::mir::Variable;
use model::mir::VariableId;
use model::span::Span;
use model::types::TypeId;

use crate::check::Checker;
use crate::check::FunctionBody;
use crate::thir::ThirId;

/// A loop being lowered: where `continue` and `break` go, and how many
/// scopes were open outside it, which a jump out of it ends.
pub(crate) struct Loop {
    pub(crate) head: BlockId,
    pub(crate) exit: BlockId,
    pub(crate) scopes: usize,
}

pub struct Lowering<'a> {
    pub checker: &'a Checker<'a>,
    variables: Vec<Variable>,
    instructions: Vec<Instruction>,
    /// `None` until the block is ended.
    blocks: Vec<(Vec<InstructionId>, Option<Terminator>)>,
    block: BlockId,
    /// Where the node being lowered is written, for what it emits.
    span: Span,
    /// The variables of each open scope, in the order declared.
    scopes: Vec<Vec<VariableId>>,
    pub(crate) loops: Vec<Loop>,
}

/// Lowers a checked program with no refusals.
pub fn lower(checker: &Checker<'_>) -> Program {
    Program {
        functions: (checker.bodies.iter())
            .map(|body| lower_function(checker, body.as_ref().expect("every function is checked")))
            .collect(),
        facts: checker.types.all_facts(),
        pointer_bytes: checker.target.pointer_bytes,
    }
}

/// Each parameter is stored into its variable, then the body runs to its
/// closing brace.
fn lower_function(checker: &Checker<'_>, body: &FunctionBody) -> Body {
    let mut lw = Lowering {
        checker,
        variables: body.variables.clone(),
        instructions: Vec::new(),
        blocks: vec![(Vec::new(), None)],
        block: BlockId(0),
        span: body.name,
        scopes: vec![Vec::new()],
        loops: Vec::new(),
    };
    for index in 0..body.parameters {
        let variable = VariableId(index as u32);
        lw.declare(variable);
        let store = InstructionKind::Store {
            place: Place::whole(variable),
            value: Operand::Argument(index as u32),
            ty: lw.variables[index].ty,
        };
        lw.emit(store, TypeId::VOID);
    }
    lw.value(body.block);
    let end = closing_brace(checker.thir().node(body.block).span);
    lw.leave_scope(end);
    lw.terminate(Terminator::FallOff(end));
    let blocks = (lw.blocks.into_iter())
        .map(|(instructions, terminator)| Block {
            instructions,
            terminator: terminator.expect("every block is ended"),
        })
        .collect();
    Body {
        name: body.name,
        variables: lw.variables,
        parameters: body.parameters,
        instructions: lw.instructions,
        blocks,
        result: body.result,
    }
}

/// The `}` that ends the braces written at `span`.
pub(crate) fn closing_brace(span: Span) -> Span {
    Span {
        start: span.end - 1,
        end: span.end,
    }
}

impl Lowering<'_> {
    /// The value of `id`, by its own kind.
    pub fn value(&mut self, id: ThirId) -> Operand {
        let checker = self.checker;
        let node = checker.thir().node(id);
        let outer = std::mem::replace(&mut self.span, node.span);
        let value = (node.kind.lower)(self, id);
        self.span = outer;
        value
    }

    /// The storage `id` names; for a value that is not a place, a hidden
    /// variable it is stored in.
    pub fn place(&mut self, id: ThirId) -> Place {
        let checker = self.checker;
        let node = checker.thir().node(id);
        let outer = std::mem::replace(&mut self.span, node.span);
        let place = match &node.kind.place {
            Some(place) => (place.lower)(self, id),
            None => {
                let value = self.value(id);
                let variable = VariableId(self.variables.len() as u32);
                self.variables.push(Variable {
                    ty: node.ty,
                    mutable: false,
                    initialized: true,
                    span: node.span,
                    hidden: true,
                });
                self.declare(variable);
                let place = Place::whole(variable);
                let store = InstructionKind::Store {
                    place: place.clone(),
                    value,
                    ty: node.ty,
                };
                self.emit(store, TypeId::VOID);
                place
            }
        };
        self.span = outer;
        place
    }

    /// The value at `place`, of `ty`: a copy when the type copies, else a
    /// move.
    pub fn read(&mut self, place: Place, ty: TypeId) -> Operand {
        let kind = match self.checker.known_facts(ty).copies {
            true => InstructionKind::Load(place),
            false => InstructionKind::Move(place),
        };
        self.emit(kind, ty)
    }

    /// Adds an instruction giving a value of `ty` to the current block.
    pub fn emit(&mut self, kind: InstructionKind, ty: TypeId) -> Operand {
        let id = InstructionId(self.instructions.len() as u32);
        self.instructions.push(Instruction {
            ty,
            kind,
            span: self.span,
        });
        self.blocks[self.block.0 as usize].0.push(id);
        Operand::Value(id)
    }

    /// Opens `variable`'s scope in the innermost one: it ends there.
    pub fn declare(&mut self, variable: VariableId) {
        (self.scopes.last_mut())
            .expect("a variable is declared in a scope")
            .push(variable);
    }

    pub fn enter_scope(&mut self) {
        self.scopes.push(Vec::new());
    }

    /// Leaves the innermost scope at the `}` written at `at`.
    pub fn leave_scope(&mut self, at: Span) {
        let outer = std::mem::replace(&mut self.span, at);
        self.end_scopes(self.scopes.len() - 1);
        self.span = outer;
        self.scopes.pop();
    }

    /// Ends the variables of the scopes from `first` on, innermost and
    /// latest first, for leaving them: at a `}` or by a jump.
    pub(crate) fn end_scopes(&mut self, first: usize) {
        let ending: Vec<VariableId> = (self.scopes[first..].iter().rev())
            .flat_map(|scope| scope.iter().rev().copied())
            .collect();
        for variable in ending {
            self.emit(InstructionKind::ScopeEnd(variable), TypeId::VOID);
        }
    }

    pub(crate) fn open_scopes(&self) -> usize {
        self.scopes.len()
    }

    pub fn new_block(&mut self) -> BlockId {
        self.blocks.push((Vec::new(), None));
        BlockId(self.blocks.len() as u32 - 1)
    }

    pub fn switch_to(&mut self, block: BlockId) {
        self.block = block;
    }

    /// Ends the current block. Lowering goes on only after `switch_to`.
    pub fn terminate(&mut self, terminator: Terminator) {
        let end = &mut self.blocks[self.block.0 as usize].1;
        debug_assert!(end.is_none(), "a block is ended once");
        *end = Some(terminator);
    }
}
