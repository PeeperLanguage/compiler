//! Lowering: a function's THIR into MIR, in one walk (docs/ir-design.md).
//! The checks on the MIR are in the `flow` crate.

use std::num::NonZeroU32;

use arena::Arena;
use arena::Id;
use arena::Lists;
use model::constant::Constant;
use model::constant::Value;
use model::mir;
use model::operators::BinaryOp;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir;
use source::Span;

/// The MIR of a function's checked body, which has `parameters` parameters
/// and gives `result`.
/// `None` when checking it reported an error (`has_errors`), or an error left
/// it incomplete: what was reported is all there is to say about it. Its
/// types, from `types`, are each the type itself: THIR's aliases are looked
/// through (`Types::real`).
pub fn lower(
    body: &thir::Body,
    parameters: usize,
    result: TypeId,
    types: &Types,
) -> Option<mir::Body> {
    if body.has_errors {
        return None;
    }
    let mut lowering = Lowering {
        thir: body,
        types,
        mir: mir::Body {
            blocks: Arena::default(),
            instructions: Arena::default(),
            variables: Arena::default(),
            parameters,
            result: types.real(result),
            debug_scopes: Arena::default(),
            constants: Vec::new(),
            lists: Lists::default(),
        },
        block: Id::from_index(0),
        instructions: Vec::new(),
        debug_scope: Id::from_index(0),
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
    }
    lowering.lower_block(function)?;
    let end = Span::new(span.end as usize - 1, span.end as usize); // the `}`
    lowering.finish(mir::Terminator::FallOff(end));
    Some(lowering.mir)
}

struct Lowering<'t> {
    thir: &'t thir::Body,
    types: &'t Types, // to look through aliases
    mir: mir::Body,
    block: Id<mir::Block>,            // the block being filled
    instructions: Vec<NonZeroU32>,    // that block's instructions so far
    debug_scope: Id<mir::DebugScope>, // the one new instructions are written in
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

    /// `constant` as an operand: the body's next constant.
    fn add_constant(&mut self, constant: Constant) -> mir::Operand {
        let id = Id::from_index(self.mir.constants.len());
        let ty = self.types.real(constant.ty);
        self.mir.constants.push(Constant { ty, ..constant });
        mir::Operand::Constant(id)
    }

    /// Lowers the statements of the source block `block`, whose MIR debug
    /// scope has the same id.
    fn lower_block(&mut self, block: Id<thir::Block>) -> Option<()> {
        let outer = std::mem::replace(&mut self.debug_scope, Id::from_index(block.index()));
        for statement in self.thir.lists.items(self.thir.blocks[block].statements) {
            self.lower_statement(statement)?;
        }
        self.debug_scope = outer;
        Some(())
    }

    fn lower_statement(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
        let span = self.thir.stmts.span(statement);
        match self.thir.stmts[statement] {
            thir::Stmt::Let { value: None, .. } => {}
            thir::Stmt::Let {
                variable,
                value: Some(value),
            } => {
                let value = self.lower_expr(value)?;
                let place = mir::Place::whole(Id::from_index(variable.index()));
                let store = mir::InstructionKind::Store { place, value };
                self.emit(store, TypeId::VOID, span);
            }
            thir::Stmt::Assign { target, value } => {
                let value = self.lower_expr(value)?;
                let Ok(place) = self.place(target) else {
                    unreachable!("the checker assigns only a place in a variable")
                };
                let store = mir::InstructionKind::Store { place, value };
                self.emit(store, TypeId::VOID, span);
            }
            thir::Stmt::Expression(value) => {
                self.lower_expr(value)?;
            }
            thir::Stmt::Return(value) => {
                let value = match value {
                    Some(value) => Some(self.lower_expr(value)?),
                    None => None,
                };
                self.finish(mir::Terminator::Return { value, span });
                // What follows a `return` is unreachable; it still gets a block.
                self.block = self.new_block(span);
            }
            thir::Stmt::If {
                condition,
                then,
                otherwise,
            } => {
                let condition_span = self.thir.exprs.span(condition);
                let condition = self.lower_expr(condition)?;
                let then_block = self.new_block(self.thir.blocks.span(then));
                let else_block =
                    otherwise.map(|otherwise| self.new_block(self.thir.stmts.span(otherwise)));
                let join = self.new_block(span);
                self.finish(mir::Terminator::Branch {
                    condition,
                    span: condition_span,
                    then: then_block,
                    otherwise: else_block.unwrap_or(join),
                });
                self.block = then_block;
                self.lower_block(then)?;
                self.finish(mir::Terminator::Goto(join));
                if let (Some(else_block), Some(otherwise)) = (else_block, otherwise) {
                    self.block = else_block;
                    self.lower_statement(otherwise)?;
                    self.finish(mir::Terminator::Goto(join));
                }
                self.block = join;
            }
            thir::Stmt::Block(block) => self.lower_block(block)?,
        }
        Some(())
    }

    /// The value of `expression`, computed in the block being filled.
    fn lower_expr(&mut self, expression: Id<thir::Expr>) -> Option<mir::Operand> {
        let span = self.thir.exprs.span(expression);
        let thir::Expr { ty, ref kind } = self.thir.exprs[expression];
        let kind = match *kind {
            _ if ty == TypeId::INVALID => return None,
            thir::ExprKind::Invalid => return None,
            thir::ExprKind::Constant(ref value) => {
                let value = value.clone();
                return Some(self.add_constant(Constant { ty, value }));
            }
            thir::ExprKind::Binary {
                op: op @ (BinaryOp::LogicalAnd | BinaryOp::LogicalOr),
                left,
                right,
            } => return self.lower_short_circuit(op, left, right, span),
            thir::ExprKind::Binary { op, left, right } => {
                let left = self.lower_expr(left)?;
                let right = self.lower_expr(right)?;
                mir::InstructionKind::Binary { op, left, right }
            }
            thir::ExprKind::Unary { op, value } => mir::InstructionKind::Unary {
                op,
                value: self.lower_expr(value)?,
            },
            thir::ExprKind::Widen(value) => mir::InstructionKind::Widen(self.lower_expr(value)?),
            thir::ExprKind::Call {
                function,
                arguments,
            } => {
                let arguments = self
                    .thir
                    .lists
                    .items(arguments)
                    .map(|argument| self.lower_expr(argument))
                    .collect::<Option<_>>()?;
                mir::InstructionKind::Call {
                    function,
                    arguments,
                }
            }
            thir::ExprKind::StructLiteral(fields) => {
                // Computed in the order written, given in declaration order.
                let mut values = vec![None; fields.len()];
                for field in self.thir.lists.items(fields) {
                    let thir::FieldValue { index, value } = self.thir.field_values[field];
                    values[index as usize] = Some(self.lower_expr(value)?);
                }
                let values = values
                    .into_iter()
                    .map(|value| value.expect("check sets each field"));
                mir::InstructionKind::Aggregate(values.collect())
            }
            thir::ExprKind::Variable(_)
            | thir::ExprKind::Field { .. }
            | thir::ExprKind::Dereference(_) => {
                // A variable, the most read place, skips the walk, which
                // cost a compile 10 M instructions (rust-style.md).
                let place = match *kind {
                    thir::ExprKind::Variable(variable) => {
                        Ok(mir::Place::whole(Id::from_index(variable.index())))
                    }
                    _ => self.place(expression),
                };
                match place {
                    Ok(place) if self.types[ty].is_copy() => mir::InstructionKind::Load(place),
                    Ok(place) => mir::InstructionKind::Move(place),
                    Err((value, fields)) => mir::InstructionKind::Field {
                        value: self.lower_expr(value)?,
                        fields,
                    },
                }
            }
            thir::ExprKind::Borrow { place, mutable } => {
                let Ok(place) = self.place(place) else {
                    unreachable!("the checker borrows only a place in a variable")
                };
                mir::InstructionKind::Borrow { place, mutable }
            }
        };
        Some(self.emit(kind, ty, span))
    }

    /// The place `expression` names: a variable, then each field and
    /// dereference from it (`s.start.x` is one place, so one load or store).
    /// For fields of a value not in a variable, that value and the field
    /// path.
    fn place(
        &self,
        expression: Id<thir::Expr>,
    ) -> Result<mir::Place, (Id<thir::Expr>, Box<[u32]>)> {
        let mut projections = Vec::new();
        let mut outer = expression;
        let variable = loop {
            match self.thir.exprs[outer].kind {
                thir::ExprKind::Field { value, index } => {
                    projections.push(mir::Projection::Field(index));
                    outer = value;
                }
                thir::ExprKind::Dereference(reference) => {
                    projections.push(mir::Projection::Dereference);
                    outer = reference;
                }
                thir::ExprKind::Variable(variable) => break variable,
                _ => {
                    let fields = projections.iter().rev().map(|&step| match step {
                        mir::Projection::Field(index) => index,
                        mir::Projection::Dereference => {
                            unreachable!("the checker dereferences only a place")
                        }
                    });
                    return Err((outer, fields.collect()));
                }
            }
        };
        projections.reverse();
        Ok(mir::Place {
            variable: Id::from_index(variable.index()),
            projections: projections.into(),
        })
    }

    /// `left && right` or `left || right`: `right` runs only when `left` does
    /// not decide the result, and a `phi` picks the result where both meet.
    fn lower_short_circuit(
        &mut self,
        op: BinaryOp,
        left: Id<thir::Expr>,
        right: Id<thir::Expr>,
        span: Span,
    ) -> Option<mir::Operand> {
        let left_span = self.thir.exprs.span(left);
        let left = self.lower_expr(left)?;
        let left_end = self.block;
        let right_block = self.new_block(self.thir.exprs.span(right));
        let join = self.new_block(span);
        let (then, otherwise) = match op {
            BinaryOp::LogicalAnd => (right_block, join),
            _ => (join, right_block),
        };
        self.finish(mir::Terminator::Branch {
            condition: left,
            span: left_span,
            then,
            otherwise,
        });
        self.block = right_block;
        let right = self.lower_expr(right)?;
        let right_end = self.block;
        self.finish(mir::Terminator::Goto(join));
        self.block = join;
        // When `left` decides, `&&` is false and `||` is true.
        let decided = self.add_constant(Constant {
            ty: TypeId::BOOL,
            value: Value::Bool(op == BinaryOp::LogicalOr),
        });
        let phi = mir::InstructionKind::Phi([(left_end, decided), (right_end, right)].into());
        Some(self.emit(phi, TypeId::BOOL, span))
    }
}
