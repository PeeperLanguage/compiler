//! Lowering blocks and statements: scope ends, loops, `break` and
//! `continue`, and each statement kind.

use arena::Id;
use model::mir::Global;
use model::mir::InstructionKind;
use model::mir::Passing;
use model::mir::Place;
use model::mir::Root;
use model::mir::Slot;
use model::mir::Terminator;
use model::thir;
use source::Span;

use crate::Loop;
use crate::Lowering;
use crate::Value;
use crate::slot_of;

impl Lowering<'_> {
    /// Lowers the statements of the source block `block`, whose debug
    /// scope has the same id and whose slots start at `start` in
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
        // A slot is borrowed only inside its block, so its borrows are all
        // lowered by now.
        self.close_scopes(start, self.thir.blocks.span(block)); // at the `}`
        self.debug_scope = outer;
        Some(())
    }

    /// Writes the `Dead`s of the slots from `start` on in `declared`,
    /// newest first, at `span`.
    pub(super) fn emit_scope_ends(&mut self, start: usize, span: Span) {
        for index in (start..self.declared.len()).rev() {
            let slot = self.declared[index];
            self.emit(InstructionKind::Dead(slot), span);
        }
    }

    /// Puts `slot` in scope after the temporaries it keeps (`kept`), so
    /// they end just after it.
    pub(super) fn declare(&mut self, slot: Id<Slot>) {
        let kept = std::mem::take(&mut self.kept[slot.index()]);
        self.declared.extend(kept);
        self.declared.push(slot);
    }

    /// Ends the slots from `start` on in `declared` at the last character
    /// of `ending`, the code they end with (a block's `}`, a statement's
    /// `;`), and forgets them: the code after does not have them.
    pub(super) fn close_scopes(&mut self, start: usize, ending: Span) {
        let end = ending.end as usize;
        self.emit_scope_ends(start, Span::new(end - 1, end));
        self.declared.truncate(start);
    }

    /// Lowers the statements of `body`, one pass of the loop `targets`,
    /// then goes to its next pass.
    pub(super) fn lower_loop_body(&mut self, body: Id<thir::Block>, targets: Loop) -> Option<()> {
        let Loop { next, declared, .. } = targets;
        self.loops.push(targets);
        self.lower_block(body, declared)?;
        self.loops.pop();
        self.finish(Terminator::Jump(next));
        Some(())
    }

    pub(super) fn lower_statement(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
        // Blocks in blocks and long `else if` chains are a deep recursion.
        source::grow_stack(|| {
            let span = self.thir.stmts.span(statement);
            // The temporaries the statement makes come after this in `declared`;
            // they end with it.
            let temporaries = self.declared.len();
            match self.thir.stmts[statement] {
                thir::Stmt::Let { variable, value } => {
                    let declared = slot_of(variable);
                    match value {
                        // One store with the statement's span, as an
                        // assignment's: a warning underlines the whole `let`.
                        Some(value)
                            if let Some(class) =
                                self.class(self.types.real(self.thir.exprs[value].ty)) =>
                        {
                            let value = self.lower_expr(value)?.register();
                            self.store(class, Place::whole(declared), value, span);
                        }
                        Some(value) => self.lower_into(value, Place::whole(declared))?,
                        None => {}
                    }
                    self.close_scopes(temporaries, span);
                    self.declare(declared);
                    return Some(());
                }
                thir::Stmt::Assign { target, value } => {
                    let ty = self.types.real(self.thir.exprs[value].ty);
                    let value = self.lower_expr(value)?;
                    let place = self.place_of(target)?;
                    self.put(place, value, ty, span);
                }
                thir::Stmt::Expression(value) => {
                    // A variable's value used by itself still moves out.
                    if let Value::Moving(place, read) = self.lower_expr(value)? {
                        let ty = self.types.real(self.thir.exprs[value].ty);
                        self.mark_moved(place, ty, read);
                    }
                }
                thir::Stmt::Return(value) => {
                    let value = match value {
                        Some(value)
                            if matches!(
                                self.mir.signature.result,
                                Some(Passing::Memory { .. })
                            ) =>
                        {
                            let result = Place {
                                root: Root::Result,
                                steps: Box::default(),
                            };
                            self.lower_into(value, result)?;
                            None
                        }
                        Some(value) => match self.lower_expr(value)? {
                            Value::Register(operand) => Some(operand),
                            Value::Nothing => None,
                            Value::Memory(_) | Value::Moving(..) => {
                                unreachable!("a value kept in memory is given there")
                            }
                        },
                        None => None,
                    };
                    self.emit_scope_ends(0, span); // out of the function
                    self.finish(Terminator::Return { value, span });
                    // What follows a `return` is unreachable; it still gets a block.
                    self.block = self.new_block(span);
                    self.declared.truncate(temporaries); // ended above
                }
                thir::Stmt::Panic(message) => {
                    let thir::ExprKind::Constant(model::constant::Value::Bytes(text)) =
                        &self.thir.exprs[message].kind
                    else {
                        unreachable!("the checker gives a panic a constant message")
                    };
                    let message = self.global(Global::Bytes(text.clone()));
                    let model::mir::Operand::Global(message) = message else {
                        unreachable!("`global` gives a global")
                    };
                    // No `Dead`s: a panic ends the program without unwinding.
                    self.finish(Terminator::Panic { message, span });
                    self.block = self.new_block(span); // unreachable, as after a `return`
                }
                thir::Stmt::If {
                    condition,
                    then,
                    otherwise,
                } => {
                    let condition_span = self.thir.exprs.span(condition);
                    let condition = self.lower_expr(condition)?.register();
                    // A `bool` holds no reference, so the condition's temporaries
                    // end before either branch.
                    self.close_scopes(temporaries, condition_span);
                    let then_block = self.new_block(self.thir.blocks.span(then));
                    let else_block =
                        otherwise.map(|otherwise| self.new_block(self.thir.stmts.span(otherwise)));
                    let join = self.new_block(span);
                    self.finish(Terminator::Branch {
                        condition,
                        span: condition_span,
                        then: then_block,
                        otherwise: else_block.unwrap_or(join),
                    });
                    self.block = then_block;
                    self.lower_block(then, self.declared.len())?;
                    self.finish(Terminator::Jump(join));
                    if let (Some(else_block), Some(otherwise)) = (else_block, otherwise) {
                        self.block = else_block;
                        self.lower_statement(otherwise)?;
                        self.finish(Terminator::Jump(join));
                    }
                    self.block = join;
                }
                thir::Stmt::Block(block) => self.lower_block(block, self.declared.len())?,
                thir::Stmt::Loop { condition, body } => {
                    // `for {}` starts each pass at its body; `for condition {}`
                    // tests the condition first.
                    let start = self.new_block(span);
                    self.finish(Terminator::Jump(start));
                    self.block = start;
                    let exit = self.new_block(span);
                    if let Some(condition) = condition {
                        let condition_span = self.thir.exprs.span(condition);
                        let condition = self.lower_expr(condition)?.register();
                        // As an `if` condition's, in each pass.
                        self.close_scopes(temporaries, condition_span);
                        let then = self.new_block(self.thir.blocks.span(body));
                        self.finish(Terminator::Branch {
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
                    self.emit_scope_ends(declared, span);
                    self.finish(Terminator::Jump(target));
                    // What follows a jump is unreachable; it still gets a block.
                    self.block = self.new_block(span);
                }
            }
            self.close_scopes(temporaries, span);
            Some(())
        })
    }
}
