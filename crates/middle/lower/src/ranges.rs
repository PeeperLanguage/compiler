//! Lowering ranges: a range's walk from its ends and step, the check that
//! the step is not 0, and `for` over a range.

use arena::Id;
use model::constant::Constant;
use model::constant::Value;
use model::mir;
use model::operators::BinaryOp;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::range_field;
use model::semantic_types::range_fields;
use model::thir;
use source::Span;

use crate::Loop;
use crate::Lowering;

/// A range's walk, the fields of a range value (`range_fields`): its values
/// are `start`, then each `delta` more than the one before, wrapping, while
/// `left`, how far its end is, lasts by `step`, the end counted when
/// `inclusive`, a `bool`.
pub(crate) struct RangeWalk {
    pub(crate) start: mir::Operand,
    pub(crate) left: mir::Operand,
    pub(crate) step: mir::Operand,
    pub(crate) delta: mir::Operand,
    pub(crate) inclusive: mir::Operand,
}

impl Lowering<'_> {
    /// Stops the program when a range's `step`, of the unsigned type `size`
    /// and written at `span`, is zero; a constant one the checker rejected.
    pub(super) fn check_step(&mut self, step: mir::Operand, size: TypeId, span: Span) {
        if matches!(step, mir::Operand::Constant(_)) {
            return;
        }
        let zero = self.integer_constant(size, 0);
        let failed = self.binary(BinaryOp::EqualTo, step, zero, TypeId::BOOL, span);
        self.check(failed, "range step is zero", span);
    }

    /// The parts of the range `range` written as `start..end:step`
    /// (`thir::ExprKind::Range`), as written: `start`, `end`, `step` and
    /// `inclusive`, after stopping the program when its step is zero
    /// (`check_step`).
    pub(super) fn lower_range_parts(&mut self, range: Id<thir::Expr>) -> Option<[mir::Operand; 4]> {
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

    /// The walk of the range `range`, of the type `range[T]` with the step
    /// type `size`. A range value holds its walk, so one made elsewhere is
    /// read field by field. A range written here (`thir::ExprKind::Range`)
    /// finds it from its parts (`lower_range_parts`): whether it counts up
    /// (`start <= end`, known now when both are constants), `left`, how far
    /// `end` is, as `size`, and `delta`, what each next value adds: `step`
    /// up, `0 - step` down, both wrapping, each a constant when what it is
    /// computed from is. Every range is walked here, so all count alike.
    pub(super) fn walk_range(&mut self, range: Id<thir::Expr>) -> Option<RangeWalk> {
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
        if !matches!(self.thir.exprs[range].kind, thir::ExprKind::Range { .. }) {
            let value = self.lower_expr(range)?;
            let types = range_fields(ty, size);
            let mut field = |index: usize| {
                let fields = Box::new([index as u32]);
                let field = mir::InstructionKind::Field { value, fields };
                self.emit(field, types[index], span)
            };
            return Some(RangeWalk {
                start: field(range_field::START),
                left: field(range_field::LEFT),
                step: field(range_field::STEP),
                delta: field(range_field::DELTA),
                inclusive: field(range_field::INCLUSIVE),
            });
        }
        let [start, end, step, inclusive] = self.lower_range_parts(range)?;
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
            return Some(RangeWalk {
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
        Some(RangeWalk {
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
    pub(super) fn lower_for_range(&mut self, statement: Id<thir::Stmt>) -> Option<()> {
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
        let RangeWalk {
            start,
            left: left_first,
            step,
            delta,
            inclusive,
        } = self.walk_range(range)?;
        // Whether the range includes its end, when that is known now: a
        // range written here; `None` for a range value.
        let included = match inclusive {
            mir::Operand::Constant(constant) => match self.mir.constants[constant.index()].value {
                Value::Bool(included) => Some(included),
                _ => unreachable!("`inclusive` is a `bool`"),
            },
            _ => None,
        };
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
}
