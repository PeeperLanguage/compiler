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
    /// scope has the same id and whose variables start at `start` in
    /// `declared`.
    pub(super) fn lower_block(&mut self, block: Id<thir::Block>, start: usize) -> Option<()> {
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
        self.close_scopes(start, self.thir.blocks.span(block)); // at the `}`
        self.debug_scope = outer;
        Some(())
    }

    /// Writes the `ScopeEnd`s of the variables from `start` on in
    /// `declared`, newest first, at `span`.
    pub(super) fn end_scopes(&mut self, start: usize, span: Span) {
        for index in (start..self.declared.len()).rev() {
            let variable = self.declared[index];
            self.emit(mir::InstructionKind::ScopeEnd(variable), TypeId::VOID, span);
        }
    }

    /// Ends the variables from `start` on in `declared` at the last
    /// character of `ending`, the code they end with (a block's `}`, a
    /// statement's `;`), and forgets them: the code after does not have them.
    pub(super) fn close_scopes(&mut self, start: usize, ending: Span) {
        let end = ending.end as usize;
        self.end_scopes(start, Span::new(end - 1, end));
        self.declared.truncate(start);
    }

    /// Lowers the statements of `body`, one pass of the loop `targets`,
    /// then goes to its next pass.
    pub(super) fn lower_loop_body(&mut self, body: Id<thir::Block>, targets: Loop) -> Option<()> {
        let Loop { next, declared, .. } = targets;
        self.loops.push(targets);
        self.lower_block(body, declared)?;
        self.loops.pop();
        self.finish(mir::Terminator::Goto(next));
        Some(())
    }

    pub(super) fn lower_statement(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
        let span = self.thir.stmts.span(statement);
        // The temporaries the statement makes come after this in `declared`;
        // they end with it, as in Rust.
        let temporaries = self.declared.len();
        match self.thir.stmts[statement] {
            thir::Stmt::Let { variable, value } => {
                let variable = Id::from_index(variable.index());
                if let Some(value) = value {
                    let value = self.lower_expr(value)?;
                    let place = mir::Place::whole(variable);
                    let store = mir::InstructionKind::Store { place, value };
                    self.emit(store, TypeId::VOID, span);
                }
                // A hidden `let` holds a `for` loop's array: every temporary
                // of that code lasts through the loop, as in Rust, and ends
                // with the block around it.
                if !self.mir.variables[variable].hidden {
                    self.close_scopes(temporaries, span);
                }
                self.declared.extend(self.kept.take());
                self.declared.push(variable);
                return Some(());
            }
            thir::Stmt::Assign { target, value } => {
                let value = self.lower_expr(value)?;
                let Ok(place) = self.place(target)? else {
                    unreachable!("the checker assigns only a place")
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
                self.end_scopes(0, span); // out of the function
                self.finish(mir::Terminator::Return { value, span });
                // What follows a `return` is unreachable; it still gets a block.
                self.block = self.new_block(span);
                self.declared.truncate(temporaries); // ended above
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
                // A `bool` holds no reference, so the condition's temporaries
                // end before either branch, as in Rust.
                self.close_scopes(temporaries, condition_span);
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
                self.lower_block(then, self.declared.len())?;
                self.finish(mir::Terminator::Goto(join));
                if let (Some(else_block), Some(otherwise)) = (else_block, otherwise) {
                    self.block = else_block;
                    self.lower_statement(otherwise)?;
                    self.finish(mir::Terminator::Goto(join));
                }
                self.block = join;
            }
            thir::Stmt::Block(block) => self.lower_block(block, self.declared.len())?,
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
                    // As an `if` condition's, in each pass.
                    self.close_scopes(temporaries, condition_span);
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
                    declared: self.declared.len(),
                };
                self.lower_loop_body(body, targets)?;
                self.block = exit;
            }
            thir::Stmt::ForRange { .. } => self.lower_for_range(statement)?,
            thir::Stmt::Break | thir::Stmt::Continue => {
                let Some(&Loop {
                    next,
                    exit,
                    declared,
                }) = self.loops.last()
                else {
                    unreachable!("the checker allows a jump only in a loop")
                };
                let target = match self.thir.stmts[statement] {
                    thir::Stmt::Break => exit,
                    _ => next,
                };
                self.end_scopes(declared, span);
                self.finish(mir::Terminator::Goto(target));
                // What follows a jump is unreachable; it still gets a block.
                self.block = self.new_block(span);
            }
        }
        self.close_scopes(temporaries, span);
        Some(())
    }
}
