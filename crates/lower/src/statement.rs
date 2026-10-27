//! Lowering blocks and statements: scope ends, loops, `break` and
//! `continue`, and each statement kind.

use arena::Id;
use model::mir;
use model::semantic_types::TypeId;
use model::thir;
use source::Span;

use crate::Loop;
use crate::Lowering;

impl Lowering<'_> {
    /// Lowers the statements of the source block `block`, whose MIR debug
    /// scope has the same id.
    pub(super) fn lower_block(&mut self, block: Id<thir::Block>) -> Option<()> {
        let outer = std::mem::replace(&mut self.debug_scope, Id::from_index(block.index()));
        for statement in self
            .thir
            .children
            .lists
            .items(self.thir.blocks[block].statements)
        {
            self.lower_statement(statement)?;
        }
        // A variable is borrowed only inside its block, so its borrows are
        // all lowered by now.
        let end = self.thir.blocks.span(block).end as usize;
        let ended = self.end_scopes(block, Span::new(end - 1, end)); // at the `}`
        self.declared.truncate(ended);
        self.debug_scope = outer;
        Some(())
    }

    /// Writes the `ScopeEnd`s of the variables declared so far in each block
    /// from the current one out to `outermost`, newest first, at `span`.
    /// Gives where those variables start in `declared`.
    pub(super) fn end_scopes(&mut self, outermost: Id<thir::Block>, span: Span) -> usize {
        let mut ended = self.declared.len();
        while let Some(&variable) = self.declared[..ended].last()
            && self.mir.variables[variable].debug_scope.index() >= outermost.index()
        {
            self.emit(mir::InstructionKind::ScopeEnd(variable), TypeId::VOID, span);
            ended -= 1;
        }
        ended
    }

    /// Lowers the statements of `body`, one pass of the loop `targets`,
    /// then goes to its next pass.
    pub(super) fn lower_loop_body(&mut self, body: Id<thir::Block>, targets: Loop) -> Option<()> {
        let next = targets.next;
        self.loops.push(targets);
        self.lower_block(body)?;
        self.loops.pop();
        self.finish(mir::Terminator::Goto(next));
        Some(())
    }

    pub(super) fn lower_statement(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
        let span = self.thir.stmts.span(statement);
        match self.thir.stmts[statement] {
            thir::Stmt::Let { variable, value } => {
                let variable = Id::from_index(variable.index());
                if let Some(value) = value {
                    let value = self.lower_expr(value)?;
                    let place = mir::Place::whole(variable);
                    let store = mir::InstructionKind::Store { place, value };
                    self.emit(store, TypeId::VOID, span);
                }
                self.declared.push(variable);
            }
            thir::Stmt::Assign { target, value } => {
                let value = self.lower_expr(value)?;
                let Ok(place) = self.place(target)? else {
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
                self.end_scopes(Id::from_index(0), span); // out to the function's block
                self.finish(mir::Terminator::Return { value, span });
                // What follows a `return` is unreachable; it still gets a block.
                self.block = self.new_block(span);
            }
            thir::Stmt::Panic(message) => {
                let mir::Operand::Constant(message) = self.lower_expr(message)? else {
                    unreachable!("the checker gives a panic a constant message")
                };
                // No `ScopeEnd`s: a panic ends the program without unwinding.
                self.finish(mir::Terminator::Panic { message, span });
                self.block = self.new_block(span); // unreachable, as after a `return`
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
            thir::Stmt::Loop { condition, body } => {
                // `for {}` starts each pass at its body; `for condition {}`
                // tests the condition first.
                let start = self.new_block(span);
                self.finish(mir::Terminator::Goto(start));
                self.block = start;
                let exit = self.new_block(span);
                if let Some(condition) = condition {
                    let condition_span = self.thir.exprs.span(condition);
                    let condition = self.lower_expr(condition)?;
                    let then = self.new_block(self.thir.blocks.span(body));
                    self.finish(mir::Terminator::Branch {
                        condition,
                        span: condition_span,
                        then,
                        otherwise: exit,
                    });
                    self.block = then;
                }
                let targets = Loop {
                    next: start,
                    exit,
                    body,
                };
                self.lower_loop_body(body, targets)?;
                self.block = exit;
            }
            thir::Stmt::ForRange { .. } => self.lower_for_range(statement)?,
            thir::Stmt::Break | thir::Stmt::Continue => {
                let Some(&Loop { next, exit, body }) = self.loops.last() else {
                    unreachable!("the checker allows a jump only in a loop")
                };
                let target = match self.thir.stmts[statement] {
                    thir::Stmt::Break => exit,
                    _ => next,
                };
                self.end_scopes(body, span);
                self.finish(mir::Terminator::Goto(target));
                // What follows a jump is unreachable; it still gets a block.
                self.block = self.new_block(span);
            }
        }
        Some(())
    }
}
