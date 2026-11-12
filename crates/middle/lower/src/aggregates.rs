//! Lowering values kept in memory: struct and array literals, arrays
//! filled from spread parts, ranges and the `for` over one, and slices.

use arena::Id;
use arena::List;
use bigint::Limbs;
use model::layout::layout;
use model::mir::Class;
use model::mir::Comparison;
use model::mir::Conversion;
use model::mir::InstructionKind;
use model::mir::IntegerOp;
use model::mir::Operand;
use model::mir::Place;
use model::mir::SLICE_WORDS;
use model::mir::Slot;
use model::mir::Step;
use model::mir::Terminator;
use model::operators::BinaryOp;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::range_field;
use model::thir;
use runtime_abi::messages::RANGE_STEP_MESSAGE;
use runtime_abi::messages::SLICE_BOUNDS_MESSAGE;
use runtime_abi::messages::SLICE_ORDER_MESSAGE;
use runtime_abi::messages::array_parts_message;
use source::Span;

use crate::Loop;
use crate::Lowering;
use crate::Value;
use crate::expression::Length;
use crate::slot_of;

/// A range's walk: its five fields in the order of `range_fields`, each by
/// its `range_field` index. Its values are `start`, then each `delta` more
/// than the one before, wrapping, while `left`, how far its end is, lasts
/// by `step`, the end counted when `inclusive`, an `I8`. Each is a
/// constant when known while lowering, else a register.
type RangeWalk = [Operand; 5];

/// A part of an array literal, lowered: its value, or the walk of a range
/// it spreads, whose count is then a constant when its ends are.
enum Part {
    Value(Value),
    Range(RangeWalk),
}

impl Lowering<'_> {
    /// A struct value of the type `ty` from `fields`, at `place`
    /// (`lower_into`): each computed in the order written, right at its
    /// offset.
    pub(super) fn lower_struct_literal(
        &mut self,
        fields: List<thir::FieldValue>,
        ty: TypeId,
        place: Place,
    ) -> Option<()> {
        for field in self.thir.children.lists.items(fields) {
            let thir::FieldValue { index, value } = self.thir.children.field_values[field];
            let (at, _) = self.field(place.clone(), ty, index as usize);
            self.lower_into(value, at)?;
        }
        Some(())
    }

    /// An array value of the array type `ty` from `parts`, at `place`
    /// (`lower_into`): each element at its own place; with a spread (an array, a slice or a range giving each
    /// of its elements or values), the parts are counted first and the
    /// program stops unless they give exactly the array's length.
    pub(super) fn lower_array_literal(
        &mut self,
        parts: List<thir::Expr>,
        ty: TypeId,
        place: Place,
        span: Span,
    ) -> Option<()> {
        let Type::Array {
            element, length, ..
        } = self.types[ty]
        else {
            unreachable!("an array literal makes an array")
        };
        let element = self.types.real(element);
        let (size, _) = layout(self.types, element, self.target);
        let parts: Vec<Id<thir::Expr>> = self.thir.children.lists.items(parts).collect();
        // Each part's value, and how many elements it gives: a number when
        // known now, else a register counted when the program runs.
        let mut lowered = Vec::with_capacity(parts.len());
        let mut counted_at_run_time = false;
        // Counted in an integer wider than a range's element and at least
        // as wide as a pointer, so a count never overflows.
        let wide = match self.types[element] {
            Type::Integer(integer) => {
                Class::integer((integer.bits + 1).max(self.target.pointer_bits))
            }
            _ => self.usize,
        };
        for &part in &parts {
            let part_type = self.types.real(self.thir.exprs[part].ty);
            // An element may itself be a slice or a range: one part, not a spread.
            let spread = !self.types.same(part_type, element);
            let value = match self.types[part_type] {
                Type::Range { .. } if spread => Part::Range(self.walk_range(part)?),
                _ => Part::Value(self.lower_expr(part)?),
            };
            let count = match self.types[part_type] {
                _ if !spread => Ok(1),
                Type::Array { length, .. } => Ok(length),
                _ => match self.part_count(&value, part_type, wide, span) {
                    Operand::Constant { bits, .. } => Ok(bits[0]),
                    counted => {
                        counted_at_run_time = true;
                        Err(counted)
                    }
                },
            };
            lowered.push((spread, part_type, value, count));
        }
        if counted_at_run_time {
            let fixed: u64 = lowered.iter().filter_map(|(.., count)| count.ok()).sum();
            let mut remaining =
                self.class_constant(wide, &Limbs::new(wide.bits(), &[length - fixed]), span);
            let mut failed = Operand::integer(Class::I8, 0);
            for (.., count) in &lowered {
                let Err(count) = count else { continue };
                let over = self.compare(Comparison::UnsignedLess, wide, remaining, *count, span);
                failed = self.integer(IntegerOp::Or, Class::I8, failed, over, span);
                remaining = self.integer(IntegerOp::Subtract, wide, remaining, *count, span);
            }
            let zero = self.class_constant(wide, &Limbs::new(wide.bits(), &[]), span);
            let short = self.compare(Comparison::NotEqual, wide, remaining, zero, span);
            let failed = self.integer(IntegerOp::Or, Class::I8, failed, short, span);
            self.emit_check(failed, &array_parts_message(length), span);
        }
        // Where the next part goes, in elements: a number until a part
        // counted when the program runs, then a register.
        let mut position: Result<u64, Operand> = Ok(0);
        for (&part, (spread, part_type, value, count)) in parts.iter().zip(lowered) {
            // A position counted when the program runs is already a `usize`.
            // Also how many elements there are from it to the end: at most
            // the length when only the program knows the position.
            let at = |position: &Result<u64, Operand>| match *position {
                Ok(index) => (
                    place.clone().then(Step::Offset(index * size)),
                    length - index,
                ),
                Err(index) => {
                    let count = Some(length);
                    let step = Step::Index { index, size, count };
                    (place.clone().then(step), length)
                }
            };
            match value {
                Part::Range(walk) => {
                    let (to, room) = at(&position);
                    let count = match count {
                        Ok(count) => Operand::integer(self.usize, count),
                        Err(count) => self.as_usize(count, wide, span),
                    };
                    self.fill_range(to, room, &walk, part_type, count, span);
                }
                Part::Value(value)
                    if spread && matches!(self.types[part_type], Type::Slice { .. }) =>
                {
                    let (to, room) = at(&position);
                    let count = count.expect_err("a slice is counted when the program runs");
                    let count = self.as_usize(count, wide, span);
                    let from = Self::through_slice(value.memory());
                    self.copy_elements(to, room, from, count, size, span);
                }
                // A value in memory is read by its copy, at the part's own
                // span; a register was read there already, and the literal
                // keeps it.
                Part::Value(value) => {
                    let (to, _) = at(&position);
                    let at = match value {
                        Value::Register(_) => span,
                        _ => self.thir.exprs.span(part),
                    };
                    self.put(to, value, part_type, at);
                }
            }
            position = match (position, count) {
                (Ok(index), Ok(count)) => Ok(index + count),
                (Ok(index), Err(count)) => {
                    let count = self.as_usize(count, wide, span);
                    let index = Operand::integer(self.usize, index);
                    Err(self.integer(IntegerOp::Add, self.usize, index, count, span))
                }
                (Err(index), Ok(count)) => {
                    let count = Operand::integer(self.usize, count);
                    Err(self.integer(IntegerOp::Add, self.usize, index, count, span))
                }
                (Err(index), Err(count)) => {
                    let count = self.as_usize(count, wide, span);
                    Err(self.integer(IntegerOp::Add, self.usize, index, count, span))
                }
            };
        }
        Some(())
    }

    /// `count`, a number of the class `wide`, as a `usize`: it was checked
    /// to fit the array.
    fn as_usize(&mut self, count: Operand, wide: Class, span: Span) -> Operand {
        if wide == self.usize {
            return count;
        }
        let kind = InstructionKind::Convert {
            conversion: Conversion::Truncate,
            from: wide,
            to: self.usize,
            value: count,
        };
        self.emit(kind, span)
    }

    /// How many elements the spread part `part`, of the type `part_type`,
    /// gives, as the class `wide`: a slice's length, or a range's count, its
    /// `left` over its `step`, one more when it includes its end or when
    /// that leaves a rest. A constant when the range's fields are.
    fn part_count(&mut self, part: &Part, part_type: TypeId, wide: Class, span: Span) -> Operand {
        match (part, &self.types[part_type]) {
            (Part::Value(value), Type::Slice { .. }) => {
                let length = self.second_word(value.clone().memory());
                let length = self.load(self.usize, length, span);
                self.widen_unsigned(length, self.usize, wide, span)
            }
            (Part::Range(walk), &Type::Range { step, .. }) => {
                let Type::Integer(size) = self.types[self.types.real(step)] else {
                    unreachable!("a range's step is an integer")
                };
                let (left, step) = (walk[range_field::LEFT], walk[range_field::STEP]);
                let inclusive = walk[range_field::INCLUSIVE];
                self.range_count(left, step, inclusive, size, wide, span)
            }
            _ => unreachable!("only a slice and a range are counted when the program runs"),
        }
    }

    /// How many values a range with `left`, `step` and `inclusive`, of the
    /// step type `size`, has, as the class `wide` (`Integer::count_over`).
    /// A constant when the three are.
    fn range_count(
        &mut self,
        left: Operand,
        step: Operand,
        inclusive: Operand,
        size: Integer,
        wide: Class,
        span: Span,
    ) -> Operand {
        if let (
            Operand::Constant { bits: left, .. },
            Operand::Constant { bits: step, .. },
            Operand::Constant {
                bits: inclusive, ..
            },
        ) = (left, step, inclusive)
        {
            let (left, step) = (Limbs::new(size.bits, &left), Limbs::new(size.bits, &step));
            let count = size.count_over(&left, &step, inclusive[0] != 0);
            let count = count.expect("the checker refused a zero step");
            let count = bigint::extend(size.bits + 1, false, &count, wide.bits());
            return self.class_constant(wide, &count, span);
        }
        let class = Class::integer(size.bits);
        let whole = self.integer(IntegerOp::UnsignedDivide, class, left, step, span);
        let whole = self.widen_unsigned(whole, class, wide, span);
        let rest = self.integer(IntegerOp::UnsignedRemainder, class, left, step, span);
        let zero = self.class_constant(class, &Limbs::new(class.bits(), &[]), span);
        let more = self.compare(Comparison::NotEqual, class, rest, zero, span);
        let last = self.integer(IntegerOp::Or, Class::I8, inclusive, more, span);
        let extra = self.widen_unsigned(last, Class::I8, wide, span);
        self.integer(IntegerOp::Add, wide, whole, extra, span)
    }

    /// `value` of the class `from` as the class `to`, no narrower, read
    /// unsigned.
    fn widen_unsigned(&mut self, value: Operand, from: Class, to: Class, span: Span) -> Operand {
        if from == to {
            return value;
        }
        let kind = InstructionKind::Convert {
            conversion: Conversion::ZeroExtend,
            from,
            to,
            value,
        };
        self.emit(kind, span)
    }

    /// Copies `count` elements of `size` bytes from `from`, a slice's
    /// elements, to `to`, which has `room` elements to its array's end: a
    /// loop, so the code does not grow with the count.
    fn copy_elements(
        &mut self,
        to: Place,
        room: u64,
        from: Place,
        count: Operand,
        size: u64,
        span: Span,
    ) {
        let index = self.counter_slot(span);
        let (test, body, done) = (
            self.new_block(span),
            self.new_block(span),
            self.new_block(span),
        );
        self.finish(Terminator::Jump(test));
        self.block = test;
        let at = self.load(self.usize, Place::whole(index), span);
        let more = self.compare(Comparison::UnsignedLess, self.usize, at, count, span);
        self.finish(Terminator::Branch {
            condition: more,
            span,
            then: body,
            otherwise: done,
        });
        self.block = body;
        let kind = InstructionKind::Copy {
            to: to.then(Step::Index {
                index: at,
                size,
                count: Some(room),
            }),
            from: from.then(Step::Index {
                index: at,
                size,
                count: None,
            }),
            size,
        };
        self.emit(kind, span);
        self.step_counter(index, at, span);
        self.finish(Terminator::Jump(test));
        self.block = done;
    }

    /// Writes `count` values of the range whose walk is `walk`, of the type
    /// `range_type`, as elements from `to` on, which has `room` elements to
    /// its array's end: its `start`, then each `delta` more, wrapping.
    fn fill_range(
        &mut self,
        to: Place,
        room: u64,
        walk: &RangeWalk,
        range_type: TypeId,
        count: Operand,
        span: Span,
    ) {
        let Type::Range { element, .. } = self.types[range_type] else {
            unreachable!("only a range fills elements")
        };
        let Type::Integer(integer) = self.types[self.types.real(element)] else {
            unreachable!("a range is of integers")
        };
        let class = Class::integer(integer.bits);
        let (size, _) = layout(self.types, self.types.real(element), self.target);
        let (start, delta) = (walk[range_field::START], walk[range_field::DELTA]);
        let value = self.register_slot(class, span);
        self.store(class, Place::whole(value), start, span);
        let index = self.counter_slot(span);
        let (test, body, done) = (
            self.new_block(span),
            self.new_block(span),
            self.new_block(span),
        );
        self.finish(Terminator::Jump(test));
        self.block = test;
        let at = self.load(self.usize, Place::whole(index), span);
        let more = self.compare(Comparison::UnsignedLess, self.usize, at, count, span);
        self.finish(Terminator::Branch {
            condition: more,
            span,
            then: body,
            otherwise: done,
        });
        self.block = body;
        let current = self.load(class, Place::whole(value), span);
        self.store(
            class,
            to.then(Step::Index {
                index: at,
                size,
                count: Some(room),
            }),
            current,
            span,
        );
        let next = self.arithmetic(IntegerOp::Add, integer, current, delta, span);
        self.store(class, Place::whole(value), next, span);
        self.step_counter(index, at, span);
        self.finish(Terminator::Jump(test));
        self.block = done;
    }

    /// A hidden `usize` slot counting from 0, in scope until the statement
    /// ends.
    fn counter_slot(&mut self, span: Span) -> Id<Slot> {
        let slot = self.register_slot(self.usize, span);
        let zero = Operand::integer(self.usize, 0);
        self.store(self.usize, Place::whole(slot), zero, span);
        slot
    }

    /// Stores `at` plus one into the counter `slot`.
    fn step_counter(&mut self, slot: Id<Slot>, at: Operand, span: Span) {
        let one = Operand::integer(self.usize, 1);
        let next = self.integer(IntegerOp::Add, self.usize, at, one, span);
        self.store(self.usize, Place::whole(slot), next, span);
    }

    /// A range value written as `start..end:step`, at `place`: its walk's
    /// five fields.
    pub(super) fn lower_range(&mut self, range: Id<thir::Expr>, place: Place) -> Option<()> {
        let span = self.thir.exprs.span(range);
        let ty = self.types.real(self.thir.exprs[range].ty);
        let walk = self.walk_range(range)?;
        for (index, value) in walk.into_iter().enumerate() {
            let (at, field_type) = self.field(place.clone(), ty, index);
            let class = self
                .class(field_type)
                .expect("a range's fields are registers");
            self.store(class, at, value, span);
        }
        Some(())
    }

    /// The parts of the range `range` written as `start..end:step`, as
    /// written: `start`, `end`, `step` and `inclusive`, after stopping the
    /// program when its step is zero; a constant zero step the checker
    /// rejected.
    fn lower_range_parts(&mut self, range: Id<thir::Expr>) -> Option<[Operand; 4]> {
        let thir::ExprKind::Range {
            start,
            end,
            step,
            inclusive,
        } = self.thir.exprs[range].kind
        else {
            unreachable!("only a written range has parts to lower")
        };
        let (size, step_span) = (
            self.types.real(self.thir.exprs[step].ty),
            self.thir.exprs.span(step),
        );
        let start = self.lower_expr(start)?.register();
        let end = self.lower_expr(end)?.register();
        let step = self.lower_expr(step)?.register();
        if !matches!(step, Operand::Constant { .. }) {
            let zero = self.integer_constant(size, 0, step_span);
            let class = self.class(size).expect("a step is an integer");
            let failed = self.compare(Comparison::Equal, class, step, zero, step_span);
            self.emit_check(failed, RANGE_STEP_MESSAGE, step_span);
        }
        let inclusive = Operand::integer(Class::I8, u64::from(inclusive));
        Some([start, end, step, inclusive])
    }

    /// The walk of the range `range`, of the type `range[T]` with the step
    /// type `size`. A range value holds its walk, so one made elsewhere is
    /// read field by field. A range written here finds it from its parts:
    /// whether it counts up (`start <= end`, known now when both are
    /// constants), `left`, how far `end` is, as `size`, and `delta`, what
    /// each next value adds: `step` up, `0 - step` down, both wrapping,
    /// each a constant when what it is computed from is.
    fn walk_range(&mut self, range: Id<thir::Expr>) -> Option<RangeWalk> {
        let span = self.thir.exprs.span(range);
        let range_type = self.types.real(self.thir.exprs[range].ty);
        let Type::Range { element, step } = self.types[range_type] else {
            unreachable!("only a range is walked")
        };
        let (ty, size) = (self.types.real(element), self.types.real(step));
        let (&Type::Integer(integer), &Type::Integer(step_integer)) =
            (&self.types[ty], &self.types[size])
        else {
            unreachable!("a range is of integers")
        };
        let (class, size_class) = (
            Class::integer(integer.bits),
            Class::integer(step_integer.bits),
        );
        if !matches!(self.thir.exprs[range].kind, thir::ExprKind::Range { .. }) {
            let place = self.lower_expr(range)?.memory();
            let field = |lowering: &mut Self, index: usize| {
                let (at, field_type) = lowering.field(place.clone(), range_type, index);
                let class = lowering
                    .class(field_type)
                    .expect("a range's fields are registers");
                lowering.load(class, at, span)
            };
            return Some([0, 1, 2, 3, 4].map(|index| field(self, index)));
        }
        let [start, end, step, inclusive] = self.lower_range_parts(range)?;
        let known = |operand: Operand, bits: u32| match operand {
            Operand::Constant { bits: value, .. } => Some(Limbs::new(bits, &value)),
            _ => None,
        };
        let delta = |lowering: &mut Self, up: bool| match known(step, step_integer.bits) {
            Some(step) => {
                let delta = if up {
                    bigint::extend(step_integer.bits, step_integer.signed, &step, integer.bits)
                } else {
                    let step =
                        bigint::extend(step_integer.bits, step_integer.signed, &step, integer.bits);
                    bigint::negate(integer.bits, &step)
                };
                lowering.integer_operand(integer, &Limbs::new(integer.bits, delta.as_slice()), span)
            }
            None if up => lowering.convert_integer(step, step_integer, integer, span),
            None => {
                let delta = lowering.convert_integer(step, step_integer, integer, span);
                let zero = lowering.integer_operand(integer, &Limbs::new(integer.bits, &[]), span);
                lowering.arithmetic(IntegerOp::Subtract, integer, zero, delta, span)
            }
        };
        if let (Some(first), Some(last)) = (known(start, integer.bits), known(end, integer.bits)) {
            let (up, distance) = integer.range_distance(&first, &last);
            let distance = Limbs::new(step_integer.bits, distance.as_slice());
            let left = self.integer_operand(step_integer, &distance, span);
            let delta = delta(self, up);
            return Some([start, left, step, delta, inclusive]);
        }
        // Which way it counts is known only when it runs: each way's
        // `left` and `delta` go into a hidden slot where the ways meet.
        let left_slot = self.register_slot(size_class, span);
        let delta_slot = self.register_slot(class, span);
        let way = |lowering: &mut Self, up: bool| {
            let (near, far) = if up { (start, end) } else { (end, start) };
            let distance = lowering.arithmetic(IntegerOp::Subtract, integer, far, near, span);
            let left = lowering.convert_integer(distance, integer, step_integer, span);
            lowering.store(size_class, Place::whole(left_slot), left, span);
            let delta = delta(lowering, up);
            lowering.store(class, Place::whole(delta_slot), delta, span);
        };
        let up = self.compare_integers_le(start, end, integer, span);
        let [upward, downward, counted] = [(); 3].map(|()| self.new_block(span));
        self.finish(Terminator::Branch {
            condition: up,
            span,
            then: upward,
            otherwise: downward,
        });
        self.block = upward;
        way(self, true);
        self.finish(Terminator::Jump(counted));
        self.block = downward;
        way(self, false);
        self.finish(Terminator::Jump(counted));
        self.block = counted;
        let left = self.load(size_class, Place::whole(left_slot), span);
        let delta = self.load(class, Place::whole(delta_slot), span);
        Some([start, left, step, delta, inclusive])
    }

    /// `start <= end` for two registers of the type `integer`.
    fn compare_integers_le(
        &mut self,
        start: Operand,
        end: Operand,
        integer: Integer,
        span: Span,
    ) -> Operand {
        let (_, less_or_equal) = Comparison::integer_less(integer.signed);
        self.compare(
            less_or_equal,
            Class::integer(integer.bits),
            start,
            end,
            span,
        )
    }

    /// Lowers the `thir::Stmt::ForRange` `statement`. On entry, once:
    /// `left` and `delta` (`walk_range`). The counter, `left`, the passes
    /// and whether a pass runs live in hidden slots, set on entry and
    /// before each pass's body. A next pass runs while `left` is more than
    /// `step` (at least `step` for `..=`), so the counter steps only to a
    /// value in the range and never overflows.
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
        let range_type = self.types.real(self.thir.exprs[range].ty);
        let Type::Range { element, step } = self.types[range_type] else {
            unreachable!("a range loop walks a range")
        };
        let (ty, size) = (self.types.real(element), self.types.real(step));
        let (&Type::Integer(integer), &Type::Integer(step_integer)) =
            (&self.types[ty], &self.types[size])
        else {
            unreachable!("a range is of integers")
        };
        let (class, size_class) = (
            Class::integer(integer.bits),
            Class::integer(step_integer.bits),
        );
        let walk = self.walk_range(range)?;
        let (start, left_first, step, delta, inclusive) = (
            walk[range_field::START],
            walk[range_field::LEFT],
            walk[range_field::STEP],
            walk[range_field::DELTA],
            walk[range_field::INCLUSIVE],
        );
        // Whether the range includes its end, when that is known now: a
        // range written here; `None` for a range value.
        let included = match inclusive {
            Operand::Constant { bits, .. } => Some(bits[0] != 0),
            _ => None,
        };
        let first = if included == Some(true) {
            inclusive
        } else {
            let zero =
                self.class_constant(size_class, &Limbs::new(size_class.bits(), &[]), range_span);
            let away = self.compare(
                Comparison::NotEqual,
                size_class,
                left_first,
                zero,
                range_span,
            );
            match included {
                Some(_) => away,
                None => self.integer(IntegerOp::Or, Class::I8, inclusive, away, range_span),
            }
        };
        let counter = self.register_slot(class, range_span);
        let left = self.register_slot(size_class, range_span);
        let going = self.register_slot(Class::I8, range_span);
        let index_slot = index.map(slot_of);
        let passes_class = index_slot.map(|slot| {
            let passes_type = self.thir.variables[Id::from_index(slot.index())].ty;
            self.class(self.types.real(passes_type))
                .expect("a count is a register")
        });
        let passes = passes_class.map(|class| self.register_slot(class, range_span));
        self.store(class, Place::whole(counter), start, range_span);
        self.store(size_class, Place::whole(left), left_first, range_span);
        self.store(Class::I8, Place::whole(going), first, range_span);
        if let (Some(passes), Some(passes_class)) = (passes, passes_class) {
            let zero = self.class_constant(
                passes_class,
                &Limbs::new(passes_class.bits(), &[]),
                range_span,
            );
            self.store(passes_class, Place::whole(passes), zero, range_span);
        }
        let test = self.new_block(span);
        self.finish(Terminator::Jump(test));
        self.block = test;
        let going_now = self.load(Class::I8, Place::whole(going), range_span);
        let pass = self.new_block(self.thir.blocks.span(body));
        let exit = self.new_block(span);
        self.finish(Terminator::Branch {
            condition: going_now,
            span: range_span,
            then: pass,
            otherwise: exit,
        });
        self.block = pass;
        let counter_now = self.load(class, Place::whole(counter), range_span);
        let left_now = self.load(size_class, Place::whole(left), range_span);
        let declared = self.declared.len(); // the pass's variables, then its body's
        if let Some(value) = value {
            let slot = slot_of(value);
            let value_span = self.mir.slots.span(slot);
            self.store(class, Place::whole(slot), counter_now, value_span);
            self.declare(slot);
        }
        if let (Some(index_slot), Some(passes), Some(passes_class)) =
            (index_slot, passes, passes_class)
        {
            let passes_now = self.load(passes_class, Place::whole(passes), range_span);
            let index_span = self.mir.slots.span(index_slot);
            self.store(
                passes_class,
                Place::whole(index_slot),
                passes_now,
                index_span,
            );
            self.declare(index_slot);
            let one = self.class_constant(
                passes_class,
                &Limbs::new(passes_class.bits(), &[1]),
                range_span,
            );
            let next = self.integer(IntegerOp::Add, passes_class, passes_now, one, range_span);
            self.store(passes_class, Place::whole(passes), next, range_span);
        }
        let (less, less_or_equal) = Comparison::integer_less(step_integer.signed);
        let more = match included {
            Some(true) => self.compare(less_or_equal, size_class, step, left_now, range_span),
            Some(false) => self.compare(less, size_class, step, left_now, range_span),
            None => {
                let beyond = self.compare(less, size_class, step, left_now, range_span);
                let at = self.compare(Comparison::Equal, size_class, left_now, step, range_span);
                let last = self.integer(IntegerOp::And, Class::I8, inclusive, at, range_span);
                self.integer(IntegerOp::Or, Class::I8, beyond, last, range_span)
            }
        };
        let next = self.arithmetic(IntegerOp::Add, integer, counter_now, delta, range_span);
        let left_next = self.arithmetic(
            IntegerOp::Subtract,
            step_integer,
            left_now,
            step,
            range_span,
        );
        self.store(class, Place::whole(counter), next, range_span);
        self.store(size_class, Place::whole(left), left_next, range_span);
        self.store(Class::I8, Place::whole(going), more, range_span);
        // Every pass ends here, then tests again.
        let step_block = self.new_block(span);
        let targets = Loop {
            next: step_block,
            exit,
            declared,
        };
        self.lower_loop_body(body, targets)?;
        self.block = step_block;
        self.finish(Terminator::Jump(test));
        self.block = exit;
        Some(())
    }

    /// The slice `&array[start..end]` (`thir::ExprKind::Slice`) at
    /// `expression`, of an array or a slice: the borrow of the elements
    /// from the start, after stopping the program when an end is outside
    /// them or the ends are out of order. The checker rejected such ends
    /// known at compile time for an array, so they get no check; a slice's
    /// length is known only when the program runs, so each of its ends but
    /// a start of 0 is checked. Its two words go at `to`.
    pub(super) fn lower_slice(&mut self, expression: Id<thir::Expr>, to: Place) -> Option<()> {
        let span = self.thir.exprs.span(expression);
        let thir::Expr {
            ty,
            kind:
                thir::ExprKind::Slice {
                    array,
                    start,
                    end,
                    inclusive,
                },
        } = self.thir.exprs[expression]
        else {
            unreachable!("the caller found a slice")
        };
        let ty = self.types.real(ty);
        let Some(mutable) = self.types[ty].borrows() else {
            unreachable!("a slice's type is a slice")
        };
        let mut place = self.place_of(array)?;
        let viewed = self.types.real(self.thir.exprs[array].ty);
        let (element, length) = match self.types[viewed] {
            Type::Array {
                element, length, ..
            } => (element, Length::Known(length)),
            // Its elements are behind its first word.
            Type::Slice { element, .. } => {
                let length = self.second_word(place.clone());
                let read = self.load(self.usize, length, span);
                place = Self::through_slice(place);
                (element, Length::Read(read))
            }
            _ => unreachable!("the checker slices only an array or a slice"),
        };
        let (size, _) = layout(self.types, self.types.real(element), self.target);
        let known_length = match length {
            Length::Known(length) => Some(length),
            Length::Read(_) => None,
        };
        // How many elements come before each end when known at compile
        // time: a missing start is 0, a missing end the length.
        let known_start = start.map_or(Some(0), |start| self.known_position(start));
        let known_end = end.map_or(known_length, |end| {
            (self.known_position(end)).map(|end| end + u64::from(inclusive))
        });
        // A written end is checked unless it is known inside the array; no
        // start of 0 is outside.
        let checked =
            |known: Option<u64>| known.is_none() || known_length.is_none() && known != Some(0);
        let start_operand = match start {
            Some(start) if checked(known_start) => {
                self.lower_slice_end(start, BinaryOp::GreaterThan, length)?
            }
            _ => self.integer_constant(
                self.types.usize,
                known_start.expect("an unchecked start is known"),
                span,
            ),
        };
        let end_operand = match (end, known_end) {
            (Some(end), known) if checked(known) => {
                // A `..=` end is below the length, so one past it fits.
                let op = if inclusive {
                    BinaryOp::GreaterOrEqual
                } else {
                    BinaryOp::GreaterThan
                };
                let end = self.lower_slice_end(end, op, length)?;
                if inclusive {
                    let one = Operand::integer(self.usize, 1);
                    self.integer(IntegerOp::Add, self.usize, end, one, span)
                } else {
                    end
                }
            }
            (_, Some(known)) => self.integer_constant(self.types.usize, known, span),
            // A slice's missing end is its length.
            (_, None) => match length {
                Length::Read(length) => length,
                Length::Known(_) => unreachable!("an array's missing end is known"),
            },
        };
        // Each end is inside the elements, so a start of 0 or an end at the
        // length is in order with any other.
        let at_length = end.is_none() || known_end.is_some() && known_end == known_length;
        let in_order =
            known_start == Some(0) || at_length || known_start.is_some() && known_end.is_some();
        if !in_order {
            let failed = self.compare(
                Comparison::UnsignedLess,
                self.usize,
                end_operand,
                start_operand,
                span,
            );
            self.emit_check(failed, SLICE_ORDER_MESSAGE, span);
        }
        let count = match (known_start, known_end) {
            (Some(start), Some(end)) => self.integer_constant(self.types.usize, end - start, span),
            (Some(0), None) => end_operand,
            _ => self.integer(
                IntegerOp::Subtract,
                self.usize,
                end_operand,
                start_operand,
                span,
            ),
        };
        // The loan runs from the start to the array's end, so a write
        // before the start may happen while the slice is in use.
        let elements = place.then(Step::Index {
            index: start_operand,
            size,
            count: known_length,
        });
        let covered = known_length.map(|length| (length - known_start.unwrap_or(0)) * size);
        let address = self.borrow(elements, covered, mutable, &SLICE_WORDS, span);
        self.slice_words(address, count, to, span);
        Some(())
    }

    /// The end `end` of a slice of `length` elements, an integer, as a
    /// `usize`, after stopping the program when it is outside them
    /// (`checked_usize`).
    fn lower_slice_end(
        &mut self,
        end: Id<thir::Expr>,
        op: BinaryOp,
        length: Length,
    ) -> Option<Operand> {
        let span = self.thir.exprs.span(end);
        let ty = self.types.real(self.thir.exprs[end].ty);
        let value = self.lower_expr(end)?.register();
        Some(self.checked_usize(value, ty, op, length, SLICE_BOUNDS_MESSAGE, span))
    }
}
