//! Lowering: a function's THIR into MIR, in one walk (docs/ir-design.md).
//! The checks on the MIR are in the `flow` crate.

use std::num::NonZeroU32;

use arena::Arena;
use arena::Id;
use arena::List;
use arena::Lists;
use bigint::Limbs;
use model::constant::Constant;
use model::constant::Value;
use model::mir;
use model::operators::BinaryOp;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::semantic_types::range_fields;
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

    /// Stops the program when a range's `step`, of the unsigned type `size`
    /// and written at `span`, is zero; a constant one the checker rejected.
    fn check_step(&mut self, step: mir::Operand, size: TypeId, span: Span) {
        if matches!(step, mir::Operand::Constant(_)) {
            return;
        }
        let zero = self.integer_constant(size, 0);
        let failed = self.binary(BinaryOp::EqualTo, step, zero, TypeId::BOOL, span);
        let message = "range step is zero";
        let check = mir::InstructionKind::Check { failed, message };
        self.emit(check, TypeId::VOID, span);
    }

    /// The parts of the range `range` written as `start..end:step`
    /// (`thir::ExprKind::Range`), in the order of `range_fields`, after
    /// stopping the program when its step is zero (`check_step`).
    fn lower_range_parts(&mut self, range: Id<thir::Expr>) -> Option<[mir::Operand; 4]> {
        let thir::ExprKind::Range {
            start,
            end,
            step,
            inclusive,
        } = self.thir.exprs[range].kind
        else {
            unreachable!("only a written range has parts to lower")
        };
        let (size, step_span) = (self.thir.exprs[step].ty, self.thir.exprs.span(step));
        let start = self.lower_expr(start)?;
        let end = self.lower_expr(end)?;
        let step = self.lower_expr(step)?;
        self.check_step(step, size, step_span);
        let inclusive = self.add_constant(Constant {
            ty: TypeId::BOOL,
            value: Value::Bool(inclusive),
        });
        Some([start, end, step, inclusive])
    }

    /// Starts walking the range value `range`, of the type `range[T]` with
    /// the step type `size`: its parts, then whether it counts up (`start
    /// <= end`, known now when both are constants), `left`, how far `end`
    /// is, as `size`, and `delta`, what each next value adds: `step` up,
    /// `0 - step` down, both wrapping, each a constant when what it is
    /// computed from is. A range written here gives its parts as written
    /// (`lower_range_parts`), so constants fold; any other range value is
    /// read field by field, its step checked when it was made. `for` and an
    /// array's ranges both start here, so they count alike.
    fn walk_range(&mut self, range: Id<thir::Expr>) -> Option<mir::RangeWalk> {
        let span = self.thir.exprs.span(range);
        let Type::Range {
            element: ty,
            step: size,
        } = self.types[self.thir.exprs[range].ty]
        else {
            unreachable!("only a range is walked")
        };
        let Type::Integer(integer) = self.types[ty] else {
            unreachable!("a range is of integers")
        };
        let [start, end, step, inclusive] =
            if let thir::ExprKind::Range { .. } = self.thir.exprs[range].kind {
                self.lower_range_parts(range)?
            } else {
                let value = self.lower_expr(range)?;
                let types = range_fields(ty, size);
                [0, 1, 2, 3].map(|index| {
                    let fields = Box::new([index as u32]);
                    let field = mir::InstructionKind::Field { value, fields };
                    self.emit(field, types[index], span)
                })
            };
        let known = |lowering: &Self, operand| match operand {
            mir::Operand::Constant(id) => match lowering.mir.constants[id.index()].value {
                Value::Integer(ref value) => Some(value.clone()),
                _ => unreachable!("the checker allows only an integer range"),
            },
            _ => None,
        };
        // Between the step's type and the counter's, the same bits read
        // with the other sign.
        let convert = |lowering: &mut Self, value, to| {
            if size == ty {
                value
            } else {
                lowering.emit(mir::InstructionKind::Convert(value), to, span)
            }
        };
        let delta = |lowering: &mut Self, up: bool| match known(lowering, step) {
            Some(step) => lowering.add_constant(Constant {
                ty,
                value: Value::Integer(if up {
                    step
                } else {
                    bigint::negate(integer.bits, &step)
                }),
            }),
            None if up => convert(lowering, step, ty),
            None => {
                let delta = convert(lowering, step, ty);
                let zero = lowering.integer_constant(ty, 0);
                lowering.binary(BinaryOp::Subtract, zero, delta, ty, span)
            }
        };
        if let (Some(first), Some(last)) = (known(self, start), known(self, end)) {
            let (up, distance) = integer.range_distance(&first, &last);
            let left = self.add_constant(Constant {
                ty: size,
                value: Value::Integer(distance),
            });
            let delta = delta(self, up);
            return Some(mir::RangeWalk {
                start,
                left,
                step,
                delta,
                inclusive,
            });
        }
        let way = |lowering: &mut Self, up: bool| {
            let (near, far) = if up { (start, end) } else { (end, start) };
            let distance = lowering.binary(BinaryOp::Subtract, far, near, ty, span);
            (convert(lowering, distance, size), delta(lowering, up))
        };
        let up = self.binary(BinaryOp::LessOrEqual, start, end, TypeId::BOOL, span);
        let [upward, downward, counted] = [(); 3].map(|()| self.new_block(span));
        self.finish(mir::Terminator::Branch {
            condition: up,
            span,
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
        let left = mir::InstructionKind::Phi(Box::new([(upward, left_up), (downward, left_down)]));
        let delta =
            mir::InstructionKind::Phi(Box::new([(upward, delta_up), (downward, delta_down)]));
        let (left, delta) = (self.emit(left, size, span), self.emit(delta, ty, span));
        Some(mir::RangeWalk {
            start,
            left,
            step,
            delta,
            inclusive,
        })
    }

    /// Lowers the `thir::Stmt::ForRange` `statement`. On entry, once:
    /// `left` and `delta` (`walk_range`). The counter, `left`, the passes
    /// and whether a pass runs are `phi`s. A next pass runs while `left` is
    /// more than `step` (at least `step` for `..=`), so the counter steps
    /// only to a value in the range and never overflows.
    fn lower_for_range(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
        let thir::Stmt::ForRange {
            index,
            value,
            range,
            body,
        } = self.thir.stmts[statement]
        else {
            unreachable!("only a range loop is lowered here")
        };
        let span = self.thir.stmts.span(statement);
        let range_span = self.thir.exprs.span(range);
        let Type::Range {
            element: ty,
            step: size,
        } = self.types[self.thir.exprs[range].ty]
        else {
            unreachable!("a range loop walks a range")
        };
        let walk = self.walk_range(range)?;
        let included = self.mir.included(&walk);
        let mir::RangeWalk {
            start,
            left: left_first,
            step,
            delta,
            inclusive,
        } = walk;
        let first = if included == Some(true) {
            inclusive
        } else {
            let zero = self.integer_constant(size, 0);
            let bool = TypeId::BOOL;
            let away = self.binary(BinaryOp::NotEqualTo, left_first, zero, bool, range_span);
            match included {
                Some(_) => away,
                None => self.binary(BinaryOp::BitOr, inclusive, away, bool, range_span),
            }
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
        let bool = TypeId::BOOL;
        let more = match included {
            Some(true) => self.binary(BinaryOp::GreaterOrEqual, left, step, bool, range_span),
            Some(false) => self.binary(BinaryOp::GreaterThan, left, step, bool, range_span),
            None => {
                let beyond = self.binary(BinaryOp::GreaterThan, left, step, bool, range_span);
                let at = self.binary(BinaryOp::EqualTo, left, step, bool, range_span);
                let last = self.binary(BinaryOp::BitAnd, inclusive, at, bool, range_span);
                self.binary(BinaryOp::BitOr, beyond, last, bool, range_span)
            }
        };
        let next = self.binary(BinaryOp::Add, counter, delta, ty, range_span);
        let left_next = self.binary(BinaryOp::Subtract, left, step, size, range_span);
        let passes = index.zip(passes).map(|(index, passes)| {
            let passes_type = self.mir.variables[index].ty;
            let one = self.integer_constant(passes_type, 1);
            let next = self.binary(BinaryOp::Add, passes, one, passes_type, range_span);
            (passes, self.integer_constant(passes_type, 0), next)
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

    /// The instruction that makes the array literal of `parts`: an
    /// `Aggregate` when each part is one value, as a struct literal; else
    /// `ArrayParts`, with each range's step checked and its walk started
    /// (`walk_range`) before the array is made.
    fn lower_array_literal(
        &mut self,
        parts: List<thir::ArrayPart>,
    ) -> Option<mir::InstructionKind> {
        let mut lowered = Vec::with_capacity(parts.len());
        for part in self.thir.lists.items(parts) {
            let part = match self.thir.array_parts[part] {
                thir::ArrayPart::Value(value) => mir::ArrayPart::Value(self.lower_expr(value)?),
                thir::ArrayPart::Array(array) => mir::ArrayPart::Array(self.lower_expr(array)?),
                thir::ArrayPart::Range(range) => mir::ArrayPart::Range(self.walk_range(range)?),
            };
            lowered.push(part);
        }
        // Without a spread, as a struct literal.
        let values = (lowered.iter()).map(|part| match *part {
            mir::ArrayPart::Value(value) => Some(value),
            _ => None,
        });
        Some(match values.collect::<Option<Box<[_]>>>() {
            Some(values) => mir::InstructionKind::Aggregate(values),
            None => mir::InstructionKind::ArrayParts(lowered.into()),
        })
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
                let Type::Array { length, .. } = self.types[array] else {
                    unreachable!("`.len` is the length of an array")
                };
                return Some(self.integer_constant(ty, length));
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
            thir::ExprKind::ArrayLiteral(parts) => self.lower_array_literal(parts)?,
            thir::ExprKind::Range { .. } => {
                mir::InstructionKind::Aggregate(self.lower_range_parts(expression)?.into())
            }
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
