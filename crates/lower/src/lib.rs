//! Lowering: a function's THIR into MIR, in one walk (docs/ir-design.md), and
//! the path checks on the MIR (`path_checks`).

pub mod path_checks;

use std::num::NonZeroU32;

use arena::Arena;
use arena::Id;
use arena::Lists;
use model::BinaryOp;
use model::constant::Value;
use model::mir;
use model::semantic_types::TypeId;
use model::thir;
use source::Span;

/// The MIR of a function's checked body, which has `parameters` parameters.
/// `None` when checking it reported an error (`has_errors`), or an error left
/// it incomplete: what was reported is all there is to say about it.
pub fn lower(body: &thir::Body, parameters: usize) -> Option<mir::Body> {
    if body.has_errors {
        return None;
    }
    let mut lowering = Lowering {
        thir: body,
        mir: mir::Body {
            blocks: Arena::default(),
            instructions: Arena::default(),
            variables: Arena::default(),
            constants: Vec::new(),
            lists: Lists::default(),
        },
        block: Id::from_index(0),
        instructions: Vec::new(),
    };
    // One variable per local, in order: local `i` is variable `i`.
    for index in 0..body.locals.len() {
        let id = Id::from_index(index);
        let local = body.locals[id];
        if local.ty == TypeId::INVALID {
            return None;
        }
        let variable = mir::Variable {
            ty: local.ty,
            mutable: local.mutable,
        };
        lowering.mir.variables.push(variable, body.locals.span(id));
    }
    lowering.block = lowering.new_block(body.span);
    for index in 0..parameters {
        let variable = Id::from_index(index);
        let store = mir::InstructionKind::Store {
            variable,
            value: mir::Operand::Argument(index as u32),
        };
        let span = lowering.mir.variables.span(variable);
        lowering.emit(store, TypeId::VOID, span);
    }
    lowering.statements(body.statements)?;
    let end = Span::new(body.span.end as usize - 1, body.span.end as usize); // the `}`
    lowering.finish(mir::Terminator::FallOff(end));
    Some(lowering.mir)
}

struct Lowering<'t> {
    thir: &'t thir::Body,
    mir: mir::Body,
    block: Id<mir::Block>,         // the block being filled
    instructions: Vec<NonZeroU32>, // that block's instructions so far
}

impl Lowering<'_> {
    /// A new block starting at `span`; `finish` gives it its instructions and
    /// terminator.
    fn new_block(&mut self, span: Span) -> Id<mir::Block> {
        let block = mir::Block {
            instructions: arena::List::EMPTY,
            terminator: mir::Terminator::FallOff(span), // replaced by `finish`
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
        };
    }

    /// Adds an instruction to the block being filled and gives its value.
    fn emit(&mut self, kind: mir::InstructionKind, ty: TypeId, span: Span) -> mir::Operand {
        let instruction = self
            .mir
            .instructions
            .push(mir::Instruction { ty, kind }, span);
        self.instructions.push(instruction.into());
        mir::Operand::Value(instruction)
    }

    /// `value` as an operand: the body's next constant.
    fn constant(&mut self, value: Value) -> mir::Operand {
        self.mir.constants.push(value);
        mir::Operand::Constant(self.mir.constants.len() as u32 - 1)
    }

    fn statements(&mut self, statements: arena::List<thir::Stmt>) -> Option<()> {
        for statement in self.thir.lists.items(statements) {
            self.statement(statement)?;
        }
        Some(())
    }

    fn statement(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
        let span = self.thir.stmts.span(statement);
        match self.thir.stmts[statement] {
            thir::Stmt::Let { value: None, .. } => {}
            thir::Stmt::Let {
                local,
                value: Some(value),
            }
            | thir::Stmt::Assign { local, value } => {
                let value = self.expr(value)?;
                let variable = Id::from_index(local.index());
                let store = mir::InstructionKind::Store { variable, value };
                self.emit(store, TypeId::VOID, span);
            }
            thir::Stmt::Expression(value) => {
                self.expr(value)?;
            }
            thir::Stmt::Return(value) => {
                let value = match value {
                    Some(value) => Some(self.expr(value)?),
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
                let condition = self.expr(condition)?;
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
                self.statements(self.thir.blocks[then].statements)?;
                self.finish(mir::Terminator::Goto(join));
                if let (Some(else_block), Some(otherwise)) = (else_block, otherwise) {
                    self.block = else_block;
                    self.statement(otherwise)?;
                    self.finish(mir::Terminator::Goto(join));
                }
                self.block = join;
            }
            thir::Stmt::Block(block) => self.statements(self.thir.blocks[block].statements)?,
        }
        Some(())
    }

    /// The value of `expression`, computed in the block being filled.
    fn expr(&mut self, expression: Id<thir::Expr>) -> Option<mir::Operand> {
        let span = self.thir.exprs.span(expression);
        let thir::Expr { ty, ref kind } = self.thir.exprs[expression];
        let kind = match *kind {
            _ if ty == TypeId::INVALID => return None,
            thir::ExprKind::Invalid => return None,
            thir::ExprKind::Constant(ref value) => {
                return Some(self.constant(value.clone()));
            }
            thir::ExprKind::Local(local) => {
                mir::InstructionKind::Load(Id::from_index(local.index()))
            }
            thir::ExprKind::Binary {
                op: op @ (BinaryOp::LogicalAnd | BinaryOp::LogicalOr),
                left,
                right,
            } => return self.short_circuit(op, left, right, span),
            thir::ExprKind::Binary { op, left, right } => {
                let left = self.expr(left)?;
                let right = self.expr(right)?;
                mir::InstructionKind::Binary { op, left, right }
            }
            thir::ExprKind::Unary { op, value } => mir::InstructionKind::Unary {
                op,
                value: self.expr(value)?,
            },
            thir::ExprKind::Widen(value) => mir::InstructionKind::Widen(self.expr(value)?),
            thir::ExprKind::Call {
                function,
                arguments,
            } => {
                let arguments = self
                    .thir
                    .lists
                    .items(arguments)
                    .map(|argument| self.expr(argument))
                    .collect::<Option<_>>()?;
                mir::InstructionKind::Call {
                    function,
                    arguments,
                }
            }
        };
        Some(self.emit(kind, ty, span))
    }

    /// `left && right` or `left || right`: `right` runs only when `left` does
    /// not decide the result, and a `phi` picks the result where both meet.
    fn short_circuit(
        &mut self,
        op: BinaryOp,
        left: Id<thir::Expr>,
        right: Id<thir::Expr>,
        span: Span,
    ) -> Option<mir::Operand> {
        let left_span = self.thir.exprs.span(left);
        let left = self.expr(left)?;
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
        let right = self.expr(right)?;
        let right_end = self.block;
        self.finish(mir::Terminator::Goto(join));
        self.block = join;
        // When `left` decides, `&&` is false and `||` is true.
        let decided = self.constant(Value::Bool(op == BinaryOp::LogicalOr));
        let phi = mir::InstructionKind::Phi([(left_end, decided), (right_end, right)].into());
        Some(self.emit(phi, TypeId::BOOL, span))
    }
}
