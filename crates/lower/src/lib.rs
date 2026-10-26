//! Lowering: a function's THIR into MIR, in one walk (docs/ir-design.md).
//! The checks on the MIR are in the `flow` crate.

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
    loops: Vec<Loop>,                 // the loops being lowered, innermost last
    /// The variables of the enclosing blocks declared so far, in order. A
    /// block comes before the blocks inside it, so those of the blocks a
    /// scope end leaves are at the end.
    declared: Vec<Id<mir::Variable>>,
}

/// Where a loop's `continue` and `break` go, and the block they leave.
struct Loop {
    next: Id<mir::Block>, // the start of the next pass
    exit: Id<mir::Block>,
    body: Id<thir::Block>,
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
    fn end_scopes(&mut self, outermost: Id<thir::Block>, span: Span) -> usize {
        let mut ended = self.declared.len();
        while let Some(&variable) = self.declared[..ended].last()
            && self.mir.variables[variable].debug_scope.index() >= outermost.index()
        {
            self.emit(mir::InstructionKind::ScopeEnd(variable), TypeId::VOID, span);
            ended -= 1;
        }
        ended
    }

    /// Lowers the `thir::Stmt::ForRange` `statement`. On entry, once: whether
    /// the range counts up (`start <= end`, known now when both are
    /// constants), `left`, how far `end` is, as the step's unsigned type, and
    /// `delta`, what a pass adds to the counter: `step` up, `0 - step` down,
    /// both wrapping. The counter, `left`, the passes and whether a pass runs
    /// are `phi`s. A next pass runs while `left` is more than `step` (at least
    /// `step` for `..=`), so the counter steps only to a value in the range
    /// and never overflows.
    fn lower_for_range(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
        let thir::Stmt::ForRange {
            index,
            value,
            start,
            end,
            step,
            inclusive,
            body,
        } = self.thir.stmts[statement]
        else {
            unreachable!("only a range loop is lowered here")
        };
        let span = self.thir.stmts.span(statement);
        let range_span = Span::new(
            self.thir.exprs.span(start).start as usize,
            self.thir.exprs.span(end).end as usize,
        );
        let step_span = self.thir.exprs.span(step);
        let (ty, size) = (
            self.types.real(self.thir.exprs[start].ty),
            self.thir.exprs[step].ty,
        );
        let start = self.lower_expr(start)?;
        let end = self.lower_expr(end)?;
        let step = self.lower_expr(step)?;
        let number = |lowering: &mut Self, ty: TypeId, number: u64| {
            let Type::Integer(integer) = lowering.types[ty] else {
                unreachable!("the checker allows only an integer range")
            };
            lowering.add_constant(Constant {
                ty,
                value: Value::Integer(Limbs::new(integer.bits, &[number])),
            })
        };
        if !matches!(step, mir::Operand::Constant(_)) {
            let zero = number(self, size, 0);
            let failed = self.binary(BinaryOp::EqualTo, step, zero, TypeId::BOOL, step_span);
            let message = "range step is zero";
            let check = mir::InstructionKind::Check { failed, message };
            self.emit(check, TypeId::VOID, step_span);
        }
        // `left` and `delta` counting up, or down; between the step's type
        // and the counter's, the same bits read with the other sign.
        let way = |lowering: &mut Self, up: bool| {
            let convert = |lowering: &mut Self, value, to| match size == ty {
                true => value, // an unsigned range
                false => lowering.emit(mir::InstructionKind::Convert(value), to, range_span),
            };
            let (near, far) = if up { (start, end) } else { (end, start) };
            let distance = lowering.binary(BinaryOp::Subtract, far, near, ty, range_span);
            let left = convert(lowering, distance, size);
            let delta = convert(lowering, step, ty);
            if up {
                return (left, delta);
            }
            let zero = number(lowering, ty, 0);
            let back = lowering.binary(BinaryOp::Subtract, zero, delta, ty, range_span);
            (left, back)
        };
        let known_up = match (start, end) {
            (mir::Operand::Constant(start), mir::Operand::Constant(end)) => {
                let (&Type::Integer(integer), Value::Integer(start), Value::Integer(end)) = (
                    &self.types[ty],
                    &self.mir.constants[start.index()].value,
                    &self.mir.constants[end.index()].value,
                ) else {
                    unreachable!("the checker allows only an integer range")
                };
                Some(bigint::compare(integer.bits, integer.signed, start, end).is_le())
            }
            _ => None,
        };
        let (left_first, delta) = match known_up {
            Some(up) => way(self, up),
            None => {
                let up = self.binary(BinaryOp::LessOrEqual, start, end, TypeId::BOOL, range_span);
                let [upward, downward, counted] = [(); 3].map(|()| self.new_block(range_span));
                self.finish(mir::Terminator::Branch {
                    condition: up,
                    span: range_span,
                    then: upward,
                    otherwise: downward,
                });
                self.block = upward;
                let (left_up, delta_up) = way(self, true);
                self.finish(mir::Terminator::Goto(counted));
                self.block = downward;
                let (left_down, delta_down) = way(self, false);
                self.finish(mir::Terminator::Goto(counted));
                self.block = counted;
                let left =
                    mir::InstructionKind::Phi(Box::new([(upward, left_up), (downward, left_down)]));
                let delta = mir::InstructionKind::Phi(Box::new([
                    (upward, delta_up),
                    (downward, delta_down),
                ]));
                (
                    self.emit(left, size, range_span),
                    self.emit(delta, ty, range_span),
                )
            }
        };
        let first = if inclusive {
            self.add_constant(Constant {
                ty: TypeId::BOOL,
                value: Value::Bool(true),
            })
        } else {
            let zero = number(self, size, 0);
            self.binary(
                BinaryOp::NotEqualTo,
                left_first,
                zero,
                TypeId::BOOL,
                range_span,
            )
        };
        let entry = self.block;
        let test = self.new_block(span);
        self.finish(mir::Terminator::Goto(test));
        self.block = test;
        let empty = || mir::InstructionKind::Phi(Box::new([]));
        let counter = self.emit(empty(), ty, range_span);
        let left = self.emit(empty(), size, range_span);
        let index = index.map(|index| Id::from_index(index.index()));
        let passes =
            index.map(|index| self.emit(empty(), self.mir.variables[index].ty, range_span));
        let going = self.emit(empty(), TypeId::BOOL, range_span);
        let pass = self.new_block(self.thir.blocks.span(body));
        let exit = self.new_block(span);
        self.finish(mir::Terminator::Branch {
            condition: going,
            span: range_span,
            then: pass,
            otherwise: exit,
        });
        self.block = pass;
        let value = value.map(|value| Id::from_index(value.index()));
        for (variable, value) in [index.zip(passes), value.map(|value| (value, counter))]
            .into_iter()
            .flatten()
        {
            let place = mir::Place::whole(variable);
            let store = mir::InstructionKind::Store { place, value };
            self.emit(store, TypeId::VOID, self.mir.variables.span(variable));
            self.declared.push(variable);
        }
        let op = if inclusive {
            BinaryOp::GreaterOrEqual
        } else {
            BinaryOp::GreaterThan
        };
        let more = self.binary(op, left, step, TypeId::BOOL, range_span);
        let next = self.binary(BinaryOp::Add, counter, delta, ty, range_span);
        let left_next = self.binary(BinaryOp::Subtract, left, step, size, range_span);
        let passes = index.zip(passes).map(|(index, passes)| {
            let passes_type = self.mir.variables[index].ty;
            let one = number(self, passes_type, 1);
            let next = self.binary(BinaryOp::Add, passes, one, passes_type, range_span);
            (passes, number(self, passes_type, 0), next)
        });
        // Every pass ends here, so each `phi` has one entry for them.
        let step = self.new_block(span);
        let targets = Loop {
            next: step,
            exit,
            body,
        };
        self.lower_loop_body(body, targets)?;
        self.block = step;
        self.finish(mir::Terminator::Goto(test));
        let phis = [
            (counter, start, next),
            (left, left_first, left_next),
            (going, first, more),
        ];
        for (phi, first, next) in phis.into_iter().chain(passes) {
            let mir::Operand::Value(phi) = phi else {
                unreachable!("`emit` gives an instruction's value")
            };
            let incoming = [(entry, first), (step, next)];
            self.mir.instructions[phi].kind = mir::InstructionKind::Phi(incoming.into());
        }
        self.block = exit;
        Some(())
    }

    /// Lowers the statements of `body`, one pass of the loop `targets`,
    /// then goes to its next pass.
    fn lower_loop_body(&mut self, body: Id<thir::Block>, targets: Loop) -> Option<()> {
        let next = targets.next;
        self.loops.push(targets);
        self.lower_block(body)?;
        self.loops.pop();
        self.finish(mir::Terminator::Goto(next));
        Some(())
    }

    fn lower_statement(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
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
            // The array is computed for the call in it and never read, so
            // no element moves out and no large array is copied.
            thir::ExprKind::Length(array) => {
                _ = self.place(array)?;
                let array = self.thir.exprs[array].ty;
                let array = self.types[array].pointee().unwrap_or(array);
                let (Type::Array { length, .. }, Type::Integer(usize)) =
                    (&self.types[array], &self.types[ty])
                else {
                    unreachable!("`.len` is the `usize` length of an array")
                };
                let value = Value::Integer(Limbs::new(usize.bits, &[*length]));
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
            thir::ExprKind::Convert(value) => {
                mir::InstructionKind::Convert(self.lower_expr(value)?)
            }
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
            thir::ExprKind::Print { newline, values } => mir::InstructionKind::Print {
                newline,
                values: self
                    .thir
                    .lists
                    .items(values)
                    .map(|value| self.lower_expr(value))
                    .collect::<Option<_>>()?,
            },
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
            thir::ExprKind::ArrayLiteral(values) => mir::InstructionKind::Aggregate(
                self.thir
                    .lists
                    .items(values)
                    .map(|value| self.lower_expr(value))
                    .collect::<Option<_>>()?,
            ),
            thir::ExprKind::Variable(_)
            | thir::ExprKind::Field { .. }
            | thir::ExprKind::Dereference(_)
            | thir::ExprKind::Index { .. } => {
                // A variable, the most read place, skips the walk, which
                // cost a compile 10 M instructions (rust-style.md).
                let place = match *kind {
                    thir::ExprKind::Variable(variable) => {
                        Ok(mir::Place::whole(Id::from_index(variable.index())))
                    }
                    _ => self.place(expression)?,
                };
                match place {
                    Ok(place) if self.types.is_copy(ty) => mir::InstructionKind::Load(place),
                    Ok(place) => mir::InstructionKind::Move(place),
                    Err(field) => field,
                }
            }
            thir::ExprKind::Borrow(place) => {
                let Type::Reference { mutable, .. } = self.types[ty] else {
                    unreachable!("a borrow's type is a reference")
                };
                let Ok(place) = self.place(place)? else {
                    unreachable!("the checker borrows only a place in a variable")
                };
                mir::InstructionKind::Borrow { place, mutable }
            }
            thir::ExprKind::Address(place) => {
                let Ok(place) = self.place(place)? else {
                    unreachable!("the checker takes only the address of a place in a variable")
                };
                mir::InstructionKind::Address(place)
            }
        };
        Some(self.emit(kind, ty, span))
    }

    /// The place `expression` names: a variable, then each field,
    /// dereference and element from it (`s.start.x` is one place, so one
    /// load or store), each element's index computed here, in the order
    /// written. For fields of a value not in a variable, the `Field`
    /// instruction reading them from that value, computed here.
    fn place(
        &mut self,
        expression: Id<thir::Expr>,
    ) -> Option<Result<mir::Place, mir::InstructionKind>> {
        // From the last step: each one, or the index expression of an element.
        let mut steps = Vec::new();
        let mut outer = expression;
        let variable = loop {
            match self.thir.exprs[outer].kind {
                thir::ExprKind::Field { value, index } => {
                    steps.push(Ok(mir::Projection::Field(index)));
                    outer = value;
                }
                thir::ExprKind::Dereference(reference) => {
                    steps.push(Ok(mir::Projection::Dereference));
                    outer = reference;
                }
                thir::ExprKind::Index { array, index } => {
                    steps.push(Err(index));
                    outer = array;
                }
                thir::ExprKind::Variable(variable) => break variable,
                _ => {
                    let fields = steps.iter().rev().map(|&step| match step {
                        Ok(mir::Projection::Field(index)) => index,
                        _ => unreachable!("the checker dereferences and indexes only a place"),
                    });
                    let fields = fields.collect();
                    let value = self.lower_expr(outer)?;
                    return Some(Err(mir::InstructionKind::Field { value, fields }));
                }
            }
        };
        let projections = steps.into_iter().rev().map(|step| match step {
            Ok(step) => Some(step),
            Err(index) => Some(mir::Projection::Index(self.lower_expr(index)?)),
        });
        Some(Ok(mir::Place {
            variable: Id::from_index(variable.index()),
            projections: projections.collect::<Option<_>>()?,
        }))
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
