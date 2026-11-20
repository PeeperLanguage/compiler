//! Lowering expressions: constants, places, operators, conversions, calls,
//! borrows, and the short-circuit `&&` and `||`.

use arena::Id;
use bigint::Limbs;
use model::ConstArguments;
use model::DeclarationCopy;
use model::constant;
use model::layout;
use model::mir;
use model::semantic_types;
use model::thir;
use runtime_abi::messages;
use source::Span;
use tree::operators;

use crate::Lowering;
use crate::Value;
use crate::arithmetic;
use crate::c_abi;
use crate::slot_of;

/// The length of what an index or a slice is taken of: an array's, from
/// its type, or a slice's, read when the program runs.
#[derive(Clone, Copy)]
pub(super) enum Length {
    Known(u64),
    Read(mir::Operand),
}

impl Lowering<'_> {
    /// The value of `expression`, computed in the block being filled.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub(super) fn lower_expr(&mut self, expression: Id<thir::Expr>) -> Option<Value> {
        // A long chain of operators, calls or parentheses is a deep recursion.
        source::grow_stack(|| {
            let span = self.thir.exprs.span(expression);
            let thir::Expr { ty, kind } = &self.thir.exprs[expression];
            let ty = self.types.real(*ty);
            Some(match kind {
                _ if ty == semantic_types::TypeId::INVALID => return None,
                thir::ExprKind::Invalid => {
                    unreachable!("a body with an invalid value has errors and is not lowered")
                }
                thir::ExprKind::Undecided => {
                    unreachable!("only a generic declaration's own check, never lowered, has it")
                }
                thir::ExprKind::Constant(constant::Value::Bytes(_))
                    if ty == semantic_types::TypeId::STR =>
                {
                    self.in_temporary(expression, ty, span)?
                }
                thir::ExprKind::Constant(value) => self.constant(value, ty, span),
                thir::ExprKind::Variable(variable) => {
                    self.read(mir::Place::whole(slot_of(*variable)), ty, span)
                }
                thir::ExprKind::Temporary { .. }
                | thir::ExprKind::Field { .. }
                | thir::ExprKind::Dereference(_)
                | thir::ExprKind::Index { .. } => {
                    let place = self.place_of(expression)?;
                    self.read(place, ty, span)
                }
                thir::ExprKind::Length(value) => self.lower_length(*value, span)?,
                thir::ExprKind::Binary {
                    op: op @ (operators::BinaryOp::LogicalAnd | operators::BinaryOp::LogicalOr),
                    left,
                    right,
                } => self.lower_short_circuit(*op, *left, *right, span)?,
                thir::ExprKind::Binary { op, left, right } => {
                    self.lower_binary(*op, *left, *right, span)?
                }
                thir::ExprKind::Unary { op, value } => self.lower_unary(*op, *value, ty, span)?,
                thir::ExprKind::Convert(value) => self.lower_convert(*value, ty, span)?,
                thir::ExprKind::Call {
                    callee,
                    signature,
                    arguments,
                } => self.lower_call(*callee, *signature, *arguments, ty, None, span)?,
                thir::ExprKind::Borrow(borrowed) if self.class(ty).is_some() => {
                    let Some(mutable) = self.types[ty].borrows() else {
                        unreachable!("a borrow's type is a reference, slice or interface reference")
                    };
                    let place = self.place_of(*borrowed)?;
                    let borrowed_type = self.types.real(self.thir.exprs[*borrowed].ty);
                    let (size, _) = layout::layout(self.types, borrowed_type, self.target);
                    Value::Register(self.borrow(
                        place,
                        Some(size),
                        mutable,
                        &mir::REFERENCE_WORDS,
                        span,
                    ))
                }
                thir::ExprKind::ToInterface { .. }
                | thir::ExprKind::StructLiteral(_)
                | thir::ExprKind::ArrayLiteral(_)
                | thir::ExprKind::Range { .. }
                | thir::ExprKind::Borrow(_)
                | thir::ExprKind::Slice { .. } => self.in_temporary(expression, ty, span)?,
                thir::ExprKind::Address(addressed) => {
                    let place = self.place_of(*addressed)?;
                    let addressed_type = self.types.real(self.thir.exprs[*addressed].ty);
                    let (size, _) = layout::layout(self.types, addressed_type, self.target);
                    Value::Register(self.emit(mir::InstructionKind::Address { place, size }, span))
                }
                thir::ExprKind::Block(block) => {
                    self.lower_block(*block, self.declared.len())?;
                    Value::Nothing
                }
            })
        })
    }

    /// The value of `expression`, of the type `ty`, built in a new
    /// temporary by `lower_into`, which builds each kind `lower_expr` sends
    /// here at a place (a kind it does not would come back here).
    fn in_temporary(
        &mut self,
        expression: Id<thir::Expr>,
        ty: semantic_types::TypeId,
        span: Span,
    ) -> Option<Value> {
        let slot = self.temporary(ty, span);
        self.lower_into(expression, mir::Place::whole(slot))?;
        Some(Value::Memory(mir::Place::whole(slot)))
    }

    /// The constant `value`, of the type `ty`, as a register; a `&str` is
    /// in memory (`lower_into`).
    fn constant(
        &mut self,
        value: &constant::Value,
        ty: semantic_types::TypeId,
        span: Span,
    ) -> Value {
        match value {
            constant::Value::Bool(value) => {
                Value::Register(mir::Operand::integer(mir::Class::I8, u64::from(*value)))
            }
            constant::Value::Integer(limbs) => {
                let semantic_types::Type::Integer(integer) = self.types[ty] else {
                    unreachable!("an integer constant has an integer type")
                };
                Value::Register(self.integer_operand(integer, limbs, span))
            }
            constant::Value::Float(bits) => Value::Register(match self.types[ty] {
                semantic_types::Type::Float(semantic_types::Float::F32) => {
                    let bits = (f64::from_bits(*bits) as f32).to_bits();
                    mir::Operand::integer(mir::Class::F32, u64::from(bits))
                }
                _ => mir::Operand::integer(mir::Class::F64, *bits),
            }),
            constant::Value::Null => Value::Register(mir::Operand::integer(mir::Class::Pointer, 0)),
            constant::Value::Bytes(bytes) => {
                // C reads a `&cstr` up to its first zero byte, so it ends in one.
                let mut text = bytes.to_vec();
                text.push(0);
                Value::Register(self.global(mir::Global::Bytes(text.into())))
            }
        }
    }

    /// The integer `value` of the type `integer` as an operand of its
    /// class: a type narrower than its class is extended to it, as every
    /// register of a narrow type holds its value (`narrow`).
    pub(super) fn integer_operand(
        &mut self,
        integer: semantic_types::Integer,
        value: &Limbs,
        span: Span,
    ) -> mir::Operand {
        let class = mir::Class::integer(integer.bits);
        let extended = bigint::extend(integer.bits, integer.signed, value, class.bits());
        self.class_constant(class, &extended, span)
    }

    /// The integer `value`, of `class`'s width, as an operand: a wide one is
    /// read-only data, loaded here, as a constant operand holds at most 128
    /// bits.
    pub(super) fn class_constant(
        &mut self,
        class: mir::Class,
        value: &Limbs,
        span: Span,
    ) -> mir::Operand {
        if let mir::Class::Wide(_) = class {
            let mut bytes = vec![0; class.bytes(self.target.pointer_bytes()) as usize];
            value.write_le_bytes(&mut bytes);
            let global = self.global(mir::Global::Bytes(bytes.into()));
            let place = mir::Place {
                root: mir::Root::At(global),
                steps: Box::default(),
            };
            return self.load(class, place, span);
        }
        let limbs = value.as_slice();
        mir::Operand::Constant {
            class,
            bits: [limbs[0], limbs.get(1).copied().unwrap_or(0)],
        }
    }

    /// The integer `value`, of one limb, of the type `integer` as an
    /// operand, cut to the type's width: `value` must fit it. A `usize` one
    /// is `Operand::integer(self.usize, value)`.
    pub(super) fn integer_constant(
        &mut self,
        integer: semantic_types::Integer,
        value: u64,
        span: Span,
    ) -> mir::Operand {
        self.integer_operand(integer, &Limbs::new(integer.bits, &[value]), span)
    }

    /// `value`, a register of the type `integer` after an operation that
    /// may carry into the bits above its width, cut back to its width: the
    /// bits above are made its sign bit's, or zero. Nothing for a type as
    /// wide as its class.
    pub(super) fn narrow(
        &mut self,
        value: mir::Operand,
        integer: semantic_types::Integer,
        span: Span,
    ) -> mir::Operand {
        let class = mir::Class::integer(integer.bits);
        let extra = class.bits() - integer.bits;
        if extra == 0 {
            return value;
        }
        if integer.signed {
            let count = mir::Operand::integer(class, u64::from(extra));
            let up = self.integer(mir::IntegerOp::ShiftLeft, class, value, count, span);
            self.integer(mir::IntegerOp::SignedShiftRight, class, up, count, span)
        } else {
            let ones = Limbs::new(integer.bits, &[u64::MAX, u64::MAX]);
            let mask = self.integer_operand(integer, &ones, span);
            self.integer(mir::IntegerOp::And, class, value, mask, span)
        }
    }

    /// `array.len`: a slice's length, read; an array's, its type's, after
    /// computing the array for the call in it.
    fn lower_length(&mut self, value: Id<thir::Expr>, span: Span) -> Option<Value> {
        let value_type = self.types.real(self.thir.exprs[value].ty);
        // The place, not the value: no element moves out, no large array is
        // copied, and a `&mut []` stays usable.
        let place = self.place_of(value)?;
        if !self.types[value_type].is_slice_shaped() {
            let semantic_types::Type::Array { length, .. } = &self.types[value_type] else {
                unreachable!("`.len` is the length of an array, slice or `&str`")
            };
            // A register, not a constant: the checker took this length as
            // known only when the program runs, after the call, so a
            // division, shift, step or count using it is checked then.
            let slot = mir::Place::whole(self.register_slot(self.usize, span));
            self.store(
                self.usize,
                slot.clone(),
                mir::Operand::integer(self.usize, length.laid_out_length()),
                span,
            );
            return Some(Value::Register(self.load(self.usize, slot, span)));
        }
        let length = self.second_word(place);
        Some(Value::Register(self.load(self.usize, length, span)))
    }

    /// The place `expression` names: a variable or a temporary, whose value
    /// is stored here, then each field, dereference and element from it
    /// (`s.start.x` is one place), each element's index computed here, in
    /// the order written; or, for a value not in a variable, the memory
    /// its value is computed into.
    pub(super) fn place_of(&mut self, expression: Id<thir::Expr>) -> Option<mir::Place> {
        // From the last step: each one, or the index expression of an element.
        let mut steps = Vec::new();
        let mut outer = expression;
        let root = loop {
            match self.thir.exprs[outer].kind {
                thir::ExprKind::Field { value, index } => {
                    let ty = self.thir.exprs[value].ty;
                    let (offset, _) =
                        model::layout::field_placed(self.types, ty, index as usize, self.target);
                    steps.push(Ok(mir::Step::Offset(offset)));
                    outer = value;
                }
                thir::ExprKind::Dereference(reference) => {
                    steps.push(Ok(mir::Step::Through));
                    outer = reference;
                }
                thir::ExprKind::Index { array, .. } => {
                    steps.push(Err(outer));
                    // A slice's elements are behind its first word.
                    let array_type = self.types.real(self.thir.exprs[array].ty);
                    if self.types[array_type].is_slice_shaped() {
                        steps.push(Ok(mir::Step::Through));
                    }
                    outer = array;
                }
                thir::ExprKind::Variable(variable) => break mir::Place::whole(slot_of(variable)),
                thir::ExprKind::Temporary {
                    variable,
                    value,
                    kept_by,
                } => {
                    let span = self.thir.exprs.span(outer);
                    let ty = self.types.real(self.thir.exprs[value].ty);
                    let value = self.lower_expr(value)?;
                    let temporary = slot_of(variable);
                    self.put(mir::Place::whole(temporary), value, ty, span);
                    // A kept one is in `declared` with its variable (`declare`).
                    if kept_by.is_none() {
                        self.declared.push(temporary); // until its statement ends
                    }
                    break mir::Place::whole(temporary);
                }
                // A value not in a variable: computed into memory, or a
                // reference in a register, gone through.
                _ => match self.lower_expr(outer)? {
                    Value::Memory(place) => break place,
                    Value::Moving(..) => unreachable!("a variable's value is read in place"),
                    Value::Register(address) => {
                        let Some(Ok(mir::Step::Through)) = steps.pop() else {
                            unreachable!("only a reference's value is a register here")
                        };
                        break mir::Place {
                            root: mir::Root::At(address),
                            steps: Box::default(),
                        };
                    }
                    Value::Nothing => unreachable!("a place has a value"),
                },
            }
        };
        let mut place = root;
        for step in steps.into_iter().rev() {
            let step = match step {
                Ok(step) => step,
                Err(indexing) => self.lower_index(indexing, &place)?,
            };
            place = place.then(step);
        }
        Some(place)
    }

    /// The element `indexing` (`thir::ExprKind::Index`) takes of the
    /// elements at `elements`: its index, computed here as a `usize`, after
    /// stopping the program when it is outside them. A constant index into
    /// an array is inside, as the checker made sure. A slice's length is
    /// read after its index is computed, as the element itself is.
    fn lower_index(
        &mut self,
        indexing: Id<thir::Expr>,
        elements: &mir::Place,
    ) -> Option<mir::Step> {
        let span = self.thir.exprs.span(indexing);
        let thir::ExprKind::Index { array, index } = self.thir.exprs[indexing].kind else {
            unreachable!("the caller found an index")
        };
        let viewed = self.types.real(self.thir.exprs[array].ty);
        let (element, checked, count) = match &self.types[viewed] {
            semantic_types::Type::Array {
                element, length, ..
            } if let Some(at) = self.known_position(index) => {
                let at = mir::Operand::integer(self.usize, at);
                (*element, at, Some(length.laid_out_length()))
            }
            semantic_types::Type::Array {
                element, length, ..
            } => {
                let (element, length) = (*element, length.laid_out_length());
                let value = self.lower_expr(index)?.register();
                let message = messages::array_index_message(length);
                let index_type = self.types.real(self.thir.exprs[index].ty);
                let checked = self.checked_usize(
                    value,
                    index_type,
                    mir::Comparison::UnsignedLessOrEqual,
                    Length::Known(length),
                    &message,
                    span,
                );
                (element, checked, Some(length))
            }
            &semantic_types::Type::Slice { element, .. } => {
                let value = self.lower_expr(index)?.register();
                // The slice's words are the place before its elements' step.
                let slice = mir::Place {
                    root: elements.root,
                    steps: elements.steps[..elements.steps.len() - 1].into(),
                };
                let length = self.second_word(slice);
                let read = self.load(self.usize, length, span);
                let index_type = self.types.real(self.thir.exprs[index].ty);
                let checked = self.checked_usize(
                    value,
                    index_type,
                    mir::Comparison::UnsignedLessOrEqual,
                    Length::Read(read),
                    messages::SLICE_INDEX_MESSAGE,
                    span,
                );
                (element, checked, None)
            }
            _ => unreachable!("the checker indexes only an array or a slice"),
        };
        let (size, _) = layout::layout(self.types, self.types.real(element), self.target);
        Some(mir::Step::Index {
            index: checked,
            size,
            count,
        })
    }

    /// The integer `position`, an index or a slice's end, when it is a
    /// constant: the checker took only those that fit a `usize`, and an
    /// index into an array only one inside it.
    pub(super) fn known_position(&self, position: Id<thir::Expr>) -> Option<u64> {
        let thir::ExprKind::Constant(constant::Value::Integer(value)) =
            &self.thir.exprs[position].kind
        else {
            return None;
        };
        let semantic_types::Type::Integer(integer) =
            self.types[self.types.real(self.thir.exprs[position].ty)]
        else {
            unreachable!("a position is an integer")
        };
        let known = bigint::to_u64(integer.bits, integer.signed, value);
        Some(known.expect("the checker took only constant positions that fit a `usize`"))
    }

    /// `value`, an integer of the type `ty`, as a `usize`, after stopping
    /// the program with `message` when it is negative or `length` compares
    /// with it as `outside` says (`UnsignedLessOrEqual` for an index,
    /// `UnsignedLess` for an end that may be the length), checked at
    /// `span`. One no wider than a `usize` is converted first, a negative
    /// one, extended, above any length; a wider one is compared in its own
    /// type before it is cut to one.
    pub(super) fn checked_usize(
        &mut self,
        value: mir::Operand,
        ty: semantic_types::TypeId,
        outside: mir::Comparison,
        length: Length,
        message: &str,
        span: Span,
    ) -> mir::Operand {
        let (&semantic_types::Type::Integer(integer), &semantic_types::Type::Integer(size)) =
            (&self.types[ty], &self.types[self.types.usize])
        else {
            unreachable!("a position and `usize` are integers")
        };
        let wide = integer.bits > size.bits;
        if wide && integer.signed {
            let zero = self.integer_constant(integer, 0, span);
            let class = mir::Class::integer(integer.bits);
            let failed = self.compare(mir::Comparison::SignedLess, class, value, zero, span);
            self.emit_check(failed, message, span);
        }
        let compared = if wide || ty == self.types.usize {
            value
        } else {
            self.convert_integer(value, integer, size, span)
        };
        let compared_type = if wide { integer } else { size };
        let compared_class = mir::Class::integer(compared_type.bits);
        let limit = match length {
            Length::Known(length) => self.integer_constant(compared_type, length, span),
            Length::Read(length) if wide => self.convert_integer(length, size, integer, span),
            Length::Read(length) => length,
        };
        // The comparison is unsigned: a negative value, extended, is above
        // any length, and a wide signed one was refused above.
        let failed = self.compare(outside, compared_class, limit, compared, span);
        self.emit_check(failed, message, span);
        if wide {
            self.convert_integer(value, integer, size, span)
        } else {
            compared
        }
    }

    pub(super) fn convert_integer(
        &mut self,
        value: mir::Operand,
        from: semantic_types::Integer,
        to: semantic_types::Integer,
        span: Span,
    ) -> mir::Operand {
        let (from_class, to_class) = (mir::Class::integer(from.bits), mir::Class::integer(to.bits));
        let converted = if from_class == to_class {
            value
        } else {
            let cast = from.cast_to(to);
            self.convert(cast, from_class, to_class, value, span)
        };
        // To a type narrower than its class, or one read with the other sign.
        if to.bits < from.bits || to.bits < to_class.bits() && from.signed != to.signed {
            self.narrow(converted, to, span)
        } else {
            converted
        }
    }

    fn lower_binary(
        &mut self,
        op: operators::BinaryOp,
        left: Id<thir::Expr>,
        right: Id<thir::Expr>,
        span: Span,
    ) -> Option<Value> {
        let left_type = self.types.real(self.thir.exprs[left].ty);
        let right_type = self.types.real(self.thir.exprs[right].ty);
        let left_value = self.lower_expr(left)?.register();
        let right_value = self.lower_expr(right)?.register();
        let right_constant = matches!(right_value, mir::Operand::Constant { .. });
        let number = &self.types[left_type];
        let value = match *number {
            semantic_types::Type::Integer(_) | semantic_types::Type::Float(_) => {
                let Some(class) = self.class(left_type) else {
                    unreachable!("a number is a register")
                };
                match arithmetic::binary_operation(op, number) {
                    arithmetic::BinaryOperation::Compare {
                        comparison,
                        swapped,
                    } => {
                        let (left, right) = if swapped {
                            (right_value, left_value)
                        } else {
                            (left_value, right_value)
                        };
                        self.compare(comparison, class, left, right, span)
                    }
                    arithmetic::BinaryOperation::Float(operation) => {
                        let kind = mir::InstructionKind::Float {
                            operation,
                            class,
                            left: left_value,
                            right: right_value,
                        };
                        self.emit(kind, span)
                    }
                    arithmetic::BinaryOperation::Integer(operation) => {
                        let semantic_types::Type::Integer(integer) = *number else {
                            unreachable!("an integer operation is on integers")
                        };
                        let right = match operation {
                            mir::IntegerOp::SignedDivide
                            | mir::IntegerOp::UnsignedDivide
                            | mir::IntegerOp::SignedRemainder
                            | mir::IntegerOp::UnsignedRemainder => {
                                if !right_constant {
                                    let zero = Limbs::new(class.bits(), &[]);
                                    let zero = self.class_constant(class, &zero, span);
                                    let failed = self.compare(
                                        mir::Comparison::Equal,
                                        class,
                                        right_value,
                                        zero,
                                        span,
                                    );
                                    self.emit_check(failed, messages::DIVISION_BY_ZERO, span);
                                }
                                right_value
                            }
                            mir::IntegerOp::ShiftLeft
                            | mir::IntegerOp::SignedShiftRight
                            | mir::IntegerOp::UnsignedShiftRight => {
                                let semantic_types::Type::Integer(count_type) =
                                    self.types[right_type]
                                else {
                                    unreachable!("a shift count is an integer")
                                };
                                self.shift_count(
                                    right_value,
                                    count_type,
                                    integer,
                                    right_constant,
                                    span,
                                )
                            }
                            _ => right_value,
                        };
                        self.arithmetic(operation, integer, left_value, right, span)
                    }
                }
            }
            semantic_types::Type::Bool => {
                let class = mir::Class::I8;
                match op {
                    operators::BinaryOp::EqualTo => {
                        self.compare(mir::Comparison::Equal, class, left_value, right_value, span)
                    }
                    operators::BinaryOp::NotEqualTo => self.compare(
                        mir::Comparison::NotEqual,
                        class,
                        left_value,
                        right_value,
                        span,
                    ),
                    _ => unreachable!("the checker allows no other `bool` operator"),
                }
            }
            semantic_types::Type::RawPointer { .. }
            | semantic_types::Type::CString
            | semantic_types::Type::Reference { .. } => match op {
                // `rawptr + usize`: the address that many bytes on.
                operators::BinaryOp::Add => self.integer(
                    mir::IntegerOp::Add,
                    mir::Class::Pointer,
                    left_value,
                    right_value,
                    span,
                ),
                operators::BinaryOp::EqualTo => self.compare(
                    mir::Comparison::Equal,
                    mir::Class::Pointer,
                    left_value,
                    right_value,
                    span,
                ),
                operators::BinaryOp::NotEqualTo => self.compare(
                    mir::Comparison::NotEqual,
                    mir::Class::Pointer,
                    left_value,
                    right_value,
                    span,
                ),
                _ => unreachable!("the checker allows no other pointer operator"),
            },
            _ => unreachable!("the checker allows operators on numbers, `bool`s and pointers"),
        };
        Some(Value::Register(value))
    }

    /// The count of a shift of a value of the type `shifted`, `count` of
    /// the type `count_type`, as the shifted type's class, after stopping
    /// the program when it is below 0 or not below the width: read
    /// unsigned, a negative count is above any width. A constant count the
    /// checker already refused when outside.
    fn shift_count(
        &mut self,
        count: mir::Operand,
        count_type: semantic_types::Integer,
        shifted: semantic_types::Integer,
        constant: bool,
        span: Span,
    ) -> mir::Operand {
        let count_class = mir::Class::integer(count_type.bits);
        if !constant {
            // The bits a count needs to reach the width, besides a sign bit.
            let width_bits = u32::BITS - shifted.bits.leading_zeros();
            let failed = if count_type.bits - u32::from(count_type.signed) >= width_bits {
                let width = self.integer_constant(count_type, u64::from(shifted.bits), span);
                let failed = mir::Comparison::UnsignedLessOrEqual;
                Some(self.compare(failed, count_class, width, count, span))
            } else if count_type.signed {
                // No count of its type reaches the width: only a negative
                // one is outside.
                let zero = self.integer_constant(count_type, 0, span);
                Some(self.compare(mir::Comparison::SignedLess, count_class, count, zero, span))
            } else {
                None // an unsigned count too narrow to reach the width
            };
            if let Some(failed) = failed {
                self.emit_check(failed, &messages::shift_count_message(shifted.bits), span);
            }
        }
        let shifted_class = mir::Class::integer(shifted.bits);
        if count_class == shifted_class {
            return count;
        }
        // Inside the width, so it fits any class.
        if let mir::Operand::Constant { bits, .. } = count {
            let count = Limbs::new(shifted.bits, &bits);
            return self.integer_operand(shifted, &count, span);
        }
        let cast = if count_class.bits() > shifted_class.bits() {
            mir::Cast::Truncate
        } else {
            mir::Cast::ZeroExtend
        };
        self.convert(cast, count_class, shifted_class, count, span)
    }

    fn lower_unary(
        &mut self,
        op: operators::UnaryOp,
        value: Id<thir::Expr>,
        ty: semantic_types::TypeId,
        span: Span,
    ) -> Option<Value> {
        let operand = self.lower_expr(value)?.register();
        let result = match (op, &self.types[ty]) {
            (operators::UnaryOp::Negate, &semantic_types::Type::Integer(integer)) => {
                let zero = self.integer_constant(integer, 0, span);
                self.arithmetic(mir::IntegerOp::Subtract, integer, zero, operand, span)
            }
            (operators::UnaryOp::Negate, &semantic_types::Type::Float(float)) => {
                let class = float.class();
                // From -0.0, so that -(0.0) is -0.0 and -(-0.0) is 0.0.
                let sign = if float == semantic_types::Float::F32 {
                    1 << 31
                } else {
                    1 << 63
                };
                let zero = mir::Operand::integer(class, sign);
                let kind = mir::InstructionKind::Float {
                    operation: model::mir::FloatOp::Subtract,
                    class,
                    left: zero,
                    right: operand,
                };
                self.emit(kind, span)
            }
            // All ones of the type, extended as its values are, so the bits
            // above the width flip to what they should be.
            (operators::UnaryOp::BitNot, &semantic_types::Type::Integer(integer)) => {
                let class = mir::Class::integer(integer.bits);
                let ones = vec![u64::MAX; integer.bits.div_ceil(64) as usize];
                let ones = Limbs::new(integer.bits, &ones);
                let ones = self.integer_operand(integer, &ones, span);
                self.integer(mir::IntegerOp::Xor, class, operand, ones, span)
            }
            (operators::UnaryOp::LogicalNot, semantic_types::Type::Bool) => {
                let zero = mir::Operand::integer(mir::Class::I8, 0);
                self.compare(mir::Comparison::Equal, mir::Class::I8, operand, zero, span)
            }
            _ => unreachable!("the checker allows no other unary operator"),
        };
        Some(Value::Register(result))
    }

    /// The number, `bool` or raw pointer `value` converted to the type `to`.
    fn lower_convert(
        &mut self,
        value: Id<thir::Expr>,
        to: semantic_types::TypeId,
        span: Span,
    ) -> Option<Value> {
        let from = self.types.real(self.thir.exprs[value].ty);
        let operand = self.lower_expr(value)?.register();
        let converted = match (&self.types[from], &self.types[to]) {
            (&semantic_types::Type::Integer(from), &semantic_types::Type::Integer(to)) => {
                self.convert_integer(operand, from, to, span)
            }
            (semantic_types::Type::Bool, &semantic_types::Type::Integer(to)) => {
                self.convert_integer(operand, semantic_types::Integer::BOOL, to, span)
            }
            _ => {
                let (semantic_types::Conversion::Implicit(cast)
                | semantic_types::Conversion::NeedsCast(cast)) = self.types.conversion(from, to)
                else {
                    unreachable!("check converts only a pair that converts")
                };
                let class = |ty| self.class(ty).expect("a converted value is a register");
                self.convert(cast, class(from), class(to), operand, span)
            }
        };
        Some(Value::Register(converted))
    }

    pub(super) fn convert(
        &mut self,
        cast: mir::Cast,
        from: mir::Class,
        to: mir::Class,
        value: mir::Operand,
        span: Span,
    ) -> mir::Operand {
        let kind = mir::InstructionKind::Convert {
            cast,
            from,
            to,
            value,
        };
        self.emit(kind, span)
    }

    /// Puts the value of `expression` at `place`, a place nothing the
    /// expression reads can see: a new variable, an argument's copy, the
    /// result, or a field of one of them. A literal, or a call giving its
    /// value in memory, is built right there, with no copy; an assignment's
    /// target may be read by its value, so it is not such a place.
    pub(super) fn lower_into(
        &mut self,
        expression: Id<thir::Expr>,
        place: mir::Place,
    ) -> Option<()> {
        // Literals nest as deep as the source.
        source::grow_stack(|| {
            let span = self.thir.exprs.span(expression);
            let thir::Expr { ty, kind } = &self.thir.exprs[expression];
            let ty = self.types.real(*ty);
            match kind {
                _ if ty == semantic_types::TypeId::INVALID => None,
                thir::ExprKind::StructLiteral(fields) => {
                    self.lower_struct_literal(*fields, ty, place)
                }
                // Its address and length.
                thir::ExprKind::Constant(constant::Value::Bytes(bytes))
                    if ty == semantic_types::TypeId::STR =>
                {
                    let address = self.global(mir::Global::Bytes(bytes.clone()));
                    let length = mir::Operand::integer(self.usize, bytes.len() as u64);
                    self.slice_words(address, length, place, span);
                    Some(())
                }
                thir::ExprKind::ToInterface {
                    reference,
                    implementation,
                } => {
                    let reference = self.lower_expr(*reference)?.register();
                    self.lower_to_interface(reference, *implementation, ty, place, span);
                    Some(())
                }
                thir::ExprKind::Borrow(borrowed) if self.class(ty).is_none() => {
                    self.lower_borrow(*borrowed, ty, place, span)
                }
                thir::ExprKind::Slice { .. } => self.lower_slice(expression, place),
                thir::ExprKind::Range { .. } => self.lower_range(expression, place),
                thir::ExprKind::ArrayLiteral(parts) => {
                    self.lower_array_literal(*parts, ty, place, span)
                }
                thir::ExprKind::Call {
                    callee,
                    signature,
                    arguments,
                } if matches!(self.result_passing(ty), Some(mir::Passing::Memory { .. })) => {
                    let destination = Some(place);
                    self.lower_call(*callee, *signature, *arguments, ty, destination, span)?;
                    Some(())
                }
                _ => {
                    let value = self.lower_expr(expression)?;
                    self.put(place, value, ty, span);
                    Some(())
                }
            }
        })
    }

    /// A call of `callee`, of the function type `signature`, with
    /// `arguments`, giving a value of the type `ty`. An argument kept in
    /// memory is passed as the address of a private copy, which the callee
    /// may change; a result kept in memory is written where a hidden first
    /// argument points: at `destination` when given (`lower_into`), else in
    /// a new temporary.
    fn lower_call(
        &mut self,
        callee: model::Callee,
        signature: semantic_types::TypeId,
        arguments: arena::List<thir::Expr>,
        ty: semantic_types::TypeId,
        destination: Option<mir::Place>,
        span: Span,
    ) -> Option<Value> {
        let semantic_types::Type::Function { origins, .. } =
            &self.types[self.types.real(signature)]
        else {
            unreachable!("a call's signature is a function type")
        };
        let mut passed = Vec::with_capacity(arguments.len());
        let mut parameters = Vec::with_capacity(arguments.len());
        let c_symbol = match callee {
            model::Callee::Function(function) => {
                model::program::c_symbol(self.modules, function.definition)
            }
            model::Callee::Method(_) => None,
        };
        let result = self.result_passing(ty);
        let result_place = match result {
            Some(mir::Passing::Memory { size, .. }) => {
                let place =
                    destination.unwrap_or_else(|| mir::Place::whole(self.temporary(ty, span)));
                Some((place, size))
            }
            _ => None,
        };
        // Each argument is passed as it is when lowered, before the next
        // one runs: a value in memory is copied then.
        let mut pending = self.thir.children.lists.items(arguments);
        let callee = match callee {
            model::Callee::Function(function) => mir::Callee::Function(function),
            // The receiver, the first argument, is an interface value: its
            // address is passed, and its table holds the method at `slot`.
            model::Callee::Method(slot) => {
                let receiver = pending.next().expect("a method call has its receiver");
                let receiver_type = self.types.real(self.thir.exprs[receiver].ty);
                // An owned interface taken by value moves once its words
                // are read.
                let (view, moved_at) = match self.lower_expr(receiver)? {
                    Value::Moving(place, read) => (place, Some(read)),
                    value => (value.memory(), None),
                };
                let address = self.load(mir::Class::Pointer, view.clone(), span);
                let table_place = self.second_word(view.clone());
                let table = self.load(mir::Class::Pointer, table_place, span);
                if let Some(read) = moved_at {
                    self.mark_moved(view, receiver_type, read);
                }
                let method_place = mir::Place {
                    root: mir::Root::At(table),
                    steps: Box::new([mir::Step::Offset(
                        u64::from(slot) * self.target.pointer_bytes(),
                    )]),
                };
                let method = self.load(mir::Class::Pointer, method_place, span);
                passed.push(address);
                parameters.push(mir::Passing::Register {
                    class: mir::Class::Pointer,
                    signed: false,
                });
                mir::Callee::Pointer(method)
            }
        };
        for argument in pending {
            let argument_type = self.types.real(self.thir.exprs[argument].ty);
            // An argument to a parameter whose type has an error, reported
            // where it was written, which may be another module.
            if argument_type == semantic_types::TypeId::INVALID {
                return None;
            }
            let passing = self.passing(argument_type);
            parameters.push(passing);
            match passing {
                mir::Passing::Register { .. } => passed.push(self.lower_expr(argument)?.register()),
                mir::Passing::Memory { size, .. } => {
                    let copy = self.temporary(argument_type, span);
                    self.lower_into(argument, mir::Place::whole(copy))?;
                    let kind = mir::InstructionKind::Address {
                        place: mir::Place::whole(copy),
                        size,
                    };
                    passed.push(self.emit(kind, span));
                }
            }
        }
        let c_function = c_symbol.is_some();
        let c = c_symbol.map(|symbol| {
            let argument_types = (self.thir.children.lists.items(arguments))
                .map(|argument| self.types.real(self.thir.exprs[argument].ty));
            c_abi::c_signature(self.types, self.target, symbol, ty, argument_types)
        });
        self.mir.signatures.push(mir::Signature {
            parameters: parameters.into(),
            result,
            c,
            origins: origins.clone(),
        });
        let kind = mir::InstructionKind::Call {
            callee,
            signature: (self.mir.signatures.len() - 1) as u32,
            arguments: passed.into(),
            result: result_place.clone(),
        };
        let mut called = self.emit(kind, span);
        // C leaves the bits of a register above a narrow integer unset.
        if let (true, &semantic_types::Type::Integer(integer)) =
            (c_function, &self.types[self.types.real(ty)])
        {
            called = self.narrow(called, integer, span);
        }
        Some(match (result, result_place) {
            (Some(mir::Passing::Register { .. }), _) => Value::Register(called),
            (Some(mir::Passing::Memory { .. }), Some((place, _))) => Value::Memory(place),
            _ => Value::Nothing,
        })
    }

    /// The interface value of the type `ty` viewing what `reference`
    /// points at, with the table of `implementation`: its two words at
    /// `place`.
    fn lower_to_interface(
        &mut self,
        reference: mir::Operand,
        implementation: semantic_types::ImplementationId,
        ty: semantic_types::TypeId,
        place: mir::Place,
        span: Span,
    ) {
        let methods = self.types.implementation(implementation);
        let interface = match self.types[ty] {
            semantic_types::Type::InterfaceReference { interface, .. } => interface,
            _ => ty, // an owned interface
        };
        let semantic_types::Type::Interface {
            methods: signatures,
            ..
        } = &self.types[interface]
        else {
            unreachable!("an interface reference's interface is checked")
        };
        // In the interface's method types `self` is `&Shape` or an owned
        // `Shape`; both pass an address, as a call through the table does.
        let table = (methods.iter().zip(signatures))
            .map(|(&definition, &signature)| {
                let copy = DeclarationCopy {
                    definition,
                    arguments: ConstArguments::NONE,
                };
                (copy, self.signature(signature))
            })
            .collect();
        let table = self.global(mir::Global::Functions(table));
        self.interface_words(reference, table, place, span);
    }

    /// `&borrowed` or `&mut borrowed` as the slice or interface reference
    /// type `ty`, at `to`: a loan of the place, and for a slice its two
    /// words, for an interface reference the struct's address and table. A
    /// reference is one register (`lower_expr`).
    fn lower_borrow(
        &mut self,
        borrowed: Id<thir::Expr>,
        ty: semantic_types::TypeId,
        to: mir::Place,
        span: Span,
    ) -> Option<()> {
        let Some(mutable) = self.types[ty].borrows() else {
            unreachable!("a borrow's type is a reference, slice or interface reference")
        };
        let place = self.place_of(borrowed)?;
        let borrowed_type = self.types.real(self.thir.exprs[borrowed].ty);
        match (&self.types[borrowed_type], &self.types[ty]) {
            // A slice of an array's elements, or a slice's elements lent again.
            (_, semantic_types::Type::Slice { .. }) => {
                let (elements, size, length) = if let semantic_types::Type::Array {
                    length, ..
                } = &self.types[borrowed_type]
                {
                    let (size, _) = layout::layout(self.types, borrowed_type, self.target);
                    let length = mir::Operand::integer(self.usize, length.laid_out_length());
                    (place, Some(size), length)
                } else {
                    let length = self.second_word(place.clone());
                    let length = self.load(self.usize, length, span);
                    (place.then(mir::Step::Through), None, length)
                };
                let address = self.borrow(elements, size, mutable, &mir::SLICE_WORDS, span);
                self.slice_words(address, length, to, span);
            }
            // An interface's struct lent again as an interface reference, under a
            // new loan: what an interface reference views (the place ends by
            // going through it, so its two words are before that), or what an
            // owned interface took (behind its own two words).
            (
                semantic_types::Type::Interface { .. },
                semantic_types::Type::InterfaceReference { .. },
            ) => {
                let (words, struct_place) = match place.steps.last() {
                    Some(mir::Step::Through) => (
                        mir::Place {
                            root: place.root,
                            steps: place.steps[..place.steps.len() - 1].into(),
                        },
                        place,
                    ),
                    _ => (place.clone(), place.then(mir::Step::Through)),
                };
                // Only the program knows which struct it views.
                let address = self.borrow(struct_place, None, mutable, &mir::REFERENCE_WORDS, span);
                let table_place = self.second_word(words);
                let table = self.load(mir::Class::Pointer, table_place, span);
                self.interface_words(address, table, to, span);
            }
            _ => unreachable!("a reference is one register"),
        }
        Some(())
    }

    /// A loan of `size` bytes from `place` (`None`: all there are),
    /// named by `words` in messages: its address.
    pub(super) fn borrow(
        &mut self,
        place: mir::Place,
        size: Option<u64>,
        mutable: bool,
        words: &'static mir::LoanWords,
        span: Span,
    ) -> mir::Operand {
        let kind = mir::InstructionKind::Borrow {
            place,
            size,
            mutable,
            words,
        };
        self.emit(kind, span)
    }

    /// An interface reference or owned interface at `place`: `address` and
    /// its methods' `table` as its two words.
    fn interface_words(
        &mut self,
        address: mir::Operand,
        table: mir::Operand,
        place: mir::Place,
        span: Span,
    ) {
        self.store(mir::Class::Pointer, place.clone(), address, span);
        let table_place = self.second_word(place);
        self.store(mir::Class::Pointer, table_place, table, span);
    }

    /// A slice value at `place`: `address` and `length` as its two words.
    pub(super) fn slice_words(
        &mut self,
        address: mir::Operand,
        length: mir::Operand,
        place: mir::Place,
        span: Span,
    ) {
        self.store(mir::Class::Pointer, place.clone(), address, span);
        let length_place = self.second_word(place);
        self.store(self.usize, length_place, length, span);
    }

    /// `left && right` or `left || right`: `right` runs only when `left`
    /// does not decide the result, and a hidden byte holds the result where
    /// both meet.
    fn lower_short_circuit(
        &mut self,
        op: operators::BinaryOp,
        left: Id<thir::Expr>,
        right: Id<thir::Expr>,
        span: Span,
    ) -> Option<Value> {
        let (left_span, right_span) = (self.thir.exprs.span(left), self.thir.exprs.span(right));
        let result = self.register_slot(mir::Class::I8, span);
        // Each side's temporaries end with it: a `bool` holds no reference,
        // and `right` may not run.
        let temporaries = self.declared.len();
        let left = self.lower_expr(left)?.register();
        self.close_scopes(temporaries, left_span);
        // When `left` decides, `&&` is false and `||` is true.
        let decided = mir::Operand::integer(
            mir::Class::I8,
            u64::from(op == operators::BinaryOp::LogicalOr),
        );
        self.store(mir::Class::I8, mir::Place::whole(result), decided, span);
        let right_block = self.new_block(right_span);
        let join = self.new_block(span);
        let (then, otherwise) = match op {
            operators::BinaryOp::LogicalAnd => (right_block, join),
            _ => (join, right_block),
        };
        self.finish(model::mir::Terminator::Branch {
            condition: left,
            span: left_span,
            then,
            otherwise,
        });
        self.block = right_block;
        let right = self.lower_expr(right)?.register();
        self.store(mir::Class::I8, mir::Place::whole(result), right, span);
        self.close_scopes(temporaries, right_span);
        self.finish(model::mir::Terminator::Jump(join));
        self.block = join;
        Some(Value::Register(self.load(
            mir::Class::I8,
            mir::Place::whole(result),
            span,
        )))
    }
}
