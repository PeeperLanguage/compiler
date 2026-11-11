//! Lowering expressions: constants, places, operators, conversions, calls,
//! borrows, and the short-circuit `&&` and `||`.

use arena::Id;
use bigint::Limbs;
use model::Callee;
use model::FunctionCopy;
use model::constant::Value as Constant;
use model::layout::layout;
use model::mir::Callee as MirCallee;
use model::mir::Class;
use model::mir::Comparison;
use model::mir::Conversion;
use model::mir::Global;
use model::mir::InstructionKind;
use model::mir::IntegerOp;
use model::mir::LoanWords;
use model::mir::Operand;
use model::mir::Passing;
use model::mir::Place;
use model::mir::REFERENCE_WORDS;
use model::mir::Root;
use model::mir::SLICE_WORDS;
use model::mir::Signature;
use model::mir::Step;
use model::operators::BinaryOp;
use model::operators::UnaryOp;
use model::semantic_types::ConstArguments;
use model::semantic_types::Float;
use model::semantic_types::ImplementationId;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use runtime_abi::messages::DIVISION_BY_ZERO;
use runtime_abi::messages::SLICE_INDEX_MESSAGE;
use runtime_abi::messages::array_index_message;
use runtime_abi::messages::shift_count_message;
use source::Span;

use crate::Lowering;
use crate::Value;
use crate::binary::BinaryOperation;
use crate::binary::binary_operation;
use crate::c_abi::c_signature;
use crate::slot_of;

/// The length of what an index or a slice is taken of: an array's, from
/// its type, or a slice's, read when the program runs.
#[derive(Clone, Copy)]
pub(super) enum Length {
    Known(u64),
    Read(Operand),
}

impl Lowering<'_> {
    /// The value of `expression`, computed in the block being filled.
    pub(super) fn lower_expr(&mut self, expression: Id<thir::Expr>) -> Option<Value> {
        // A long chain of operators, calls or parentheses is a deep recursion.
        source::grow_stack(|| {
            let span = self.thir.exprs.span(expression);
            let thir::Expr { ty, kind } = &self.thir.exprs[expression];
            let ty = self.types.real(*ty);
            Some(match kind {
                _ if ty == TypeId::INVALID => return None,
                thir::ExprKind::Invalid => return None,
                thir::ExprKind::Constant(Constant::Bytes(bytes)) if ty == TypeId::STR => self
                    .in_temporary(ty, span, |this, place| {
                        this.str_constant(bytes, place, span);
                        Some(())
                    })?,
                thir::ExprKind::Constant(value) => self.constant(value, ty, span),
                thir::ExprKind::Variable(variable) => {
                    self.read(Place::whole(slot_of(*variable)), ty, span)
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
                    op: op @ (BinaryOp::LogicalAnd | BinaryOp::LogicalOr),
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
                thir::ExprKind::ToInterface {
                    reference,
                    implementation,
                } => {
                    let reference = self.lower_expr(*reference)?.register();
                    self.in_temporary(ty, span, |this, place| {
                        this.lower_to_interface(reference, *implementation, ty, place, span);
                        Some(())
                    })?
                }
                thir::ExprKind::StructLiteral(fields) => {
                    self.in_temporary(ty, span, |this, place| {
                        this.lower_struct_literal(*fields, ty, place)
                    })?
                }
                thir::ExprKind::ArrayLiteral(parts) => {
                    self.in_temporary(ty, span, |this, place| {
                        this.lower_array_literal(*parts, ty, place, span)
                    })?
                }
                thir::ExprKind::Range { .. } => {
                    self.in_temporary(ty, span, |this, place| this.lower_range(expression, place))?
                }
                thir::ExprKind::Borrow(borrowed) if self.class(ty).is_none() => {
                    self.in_temporary(ty, span, |this, place| {
                        this.lower_borrow(*borrowed, ty, place, span)
                    })?
                }
                thir::ExprKind::Borrow(borrowed) => {
                    let Some(mutable) = self.types[ty].borrows() else {
                        unreachable!("a borrow's type is a reference, slice or interface reference")
                    };
                    let place = self.place_of(*borrowed)?;
                    let borrowed_type = self.types.real(self.thir.exprs[*borrowed].ty);
                    let (size, _) = layout(self.types, borrowed_type, self.target);
                    Value::Register(self.borrow(place, Some(size), mutable, &REFERENCE_WORDS, span))
                }
                thir::ExprKind::Slice { .. } => {
                    self.in_temporary(ty, span, |this, place| this.lower_slice(expression, place))?
                }
                thir::ExprKind::Address(addressed) => {
                    let place = self.place_of(*addressed)?;
                    let addressed_type = self.types.real(self.thir.exprs[*addressed].ty);
                    let (size, _) = layout(self.types, addressed_type, self.target);
                    Value::Register(self.emit(InstructionKind::Address { place, size }, span))
                }
                thir::ExprKind::Print { newline, values } => {
                    self.lower_print(*newline, *values, span)?;
                    Value::Nothing
                }
            })
        })
    }

    /// A value of the type `ty` that `build` writes at the place it is
    /// given, built in a new temporary.
    fn in_temporary(
        &mut self,
        ty: TypeId,
        span: Span,
        build: impl FnOnce(&mut Self, Place) -> Option<()>,
    ) -> Option<Value> {
        let slot = self.temporary(ty, span);
        build(self, Place::whole(slot))?;
        Some(Value::Memory(Place::whole(slot)))
    }

    /// The `&str` constant `bytes` at `place`: their address and length.
    fn str_constant(&mut self, bytes: &[u8], place: Place, span: Span) {
        let address = self.global(Global::Bytes(bytes.into()));
        let length = Operand::integer(self.usize, bytes.len() as u64);
        self.slice_words(address, length, place, span);
    }

    /// The constant `value`, of the type `ty`, as a register; a `&str` is
    /// `str_constant`'s.
    fn constant(&mut self, value: &Constant, ty: TypeId, span: Span) -> Value {
        match value {
            Constant::Bool(value) => {
                Value::Register(Operand::integer(Class::I8, u64::from(*value)))
            }
            Constant::Integer(limbs) => {
                let Type::Integer(integer) = self.types[ty] else {
                    unreachable!("an integer constant has an integer type")
                };
                Value::Register(self.integer_operand(integer, limbs, span))
            }
            Constant::Float(bits) => Value::Register(match self.types[ty] {
                Type::Float(Float::F32) => {
                    let bits = (f64::from_bits(*bits) as f32).to_bits();
                    Operand::integer(Class::F32, u64::from(bits))
                }
                _ => Operand::integer(Class::F64, *bits),
            }),
            Constant::Null => Value::Register(Operand::integer(Class::Pointer, 0)),
            Constant::Bytes(bytes) => {
                // C reads a `&cstr` up to its first zero byte, so it ends in one.
                let mut text = bytes.to_vec();
                text.push(0);
                Value::Register(self.global(Global::Bytes(text.into())))
            }
        }
    }

    /// The integer `value` of the type `integer` as an operand of its
    /// class: a type narrower than its class is extended to it, as every
    /// register of a narrow type holds its value (`narrow`).
    pub(super) fn integer_operand(
        &mut self,
        integer: Integer,
        value: &Limbs,
        span: Span,
    ) -> Operand {
        let class = Class::integer(integer.bits);
        let extended = bigint::extend(integer.bits, integer.signed, value, class.bits());
        self.class_constant(class, &extended, span)
    }

    /// The integer `value`, of `class`'s width, as an operand: a wide one is
    /// read-only data, loaded here, as a constant operand holds at most 128
    /// bits.
    pub(super) fn class_constant(&mut self, class: Class, value: &Limbs, span: Span) -> Operand {
        if let Class::Wide(_) = class {
            let mut bytes = vec![0; class.bytes(self.pointer_bytes) as usize];
            value.write_le_bytes(&mut bytes);
            let global = self.global(Global::Bytes(bytes.into()));
            let place = Place {
                root: Root::At(global),
                steps: Box::default(),
            };
            return self.load(class, place, span);
        }
        let limbs = value.as_slice();
        Operand::Constant {
            class,
            bits: [limbs[0], limbs.get(1).copied().unwrap_or(0)],
        }
    }

    /// The integer `value` of the type `ty` as a constant.
    pub(super) fn integer_constant(&mut self, ty: TypeId, value: u64, span: Span) -> Operand {
        let Type::Integer(integer) = self.types[ty] else {
            unreachable!("an integer constant has an integer type")
        };
        self.integer_operand(integer, &Limbs::new(integer.bits, &[value]), span)
    }

    /// `value`, a register of the type `integer` after an operation that
    /// may carry into the bits above its width, cut back to its width: the
    /// bits above are made its sign bit's, or zero. Nothing for a type as
    /// wide as its class.
    pub(super) fn narrow(&mut self, value: Operand, integer: Integer, span: Span) -> Operand {
        let class = Class::integer(integer.bits);
        let extra = class.bits() - integer.bits;
        if extra == 0 {
            return value;
        }
        if integer.signed {
            let count = Operand::integer(class, u64::from(extra));
            let up = self.integer(IntegerOp::ShiftLeft, class, value, count, span);
            self.integer(IntegerOp::SignedShiftRight, class, up, count, span)
        } else {
            let ones = Limbs::new(integer.bits, &[u64::MAX, u64::MAX]);
            let mask = self.integer_operand(integer, &ones, span);
            self.integer(IntegerOp::And, class, value, mask, span)
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
            let array = self.types[value_type].pointee().unwrap_or(value_type);
            let Type::Array { length, .. } = self.types[self.types.real(array)] else {
                unreachable!("`.len` is the length of an array, slice or `&str`")
            };
            return Some(Value::Register(self.integer_constant(
                self.types.usize,
                length,
                span,
            )));
        }
        let length = self.second_word(place);
        Some(Value::Register(self.load(self.usize, length, span)))
    }

    /// The place `expression` names: a variable or a temporary, whose value
    /// is stored here, then each field, dereference and element from it
    /// (`s.start.x` is one place), each element's index computed here, in
    /// the order written; or, for a value not in a variable, the memory
    /// its value is computed into.
    pub(super) fn place_of(&mut self, expression: Id<thir::Expr>) -> Option<Place> {
        // From the last step: each one, or the index expression of an element.
        let mut steps = Vec::new();
        let mut outer = expression;
        let root = loop {
            match self.thir.exprs[outer].kind {
                thir::ExprKind::Field { value, index } => {
                    steps.push(Ok(Step::Offset(self.field_offset(value, index))));
                    outer = value;
                }
                thir::ExprKind::Dereference(reference) => {
                    steps.push(Ok(Step::Through));
                    outer = reference;
                }
                thir::ExprKind::Index { array, .. } => {
                    steps.push(Err(outer));
                    // A slice's elements are behind its first word.
                    let array_type = self.types.real(self.thir.exprs[array].ty);
                    if self.types[array_type].is_slice_shaped() {
                        steps.push(Ok(Step::Through));
                    }
                    outer = array;
                }
                thir::ExprKind::Variable(variable) => break Place::whole(slot_of(variable)),
                thir::ExprKind::Temporary {
                    variable,
                    value,
                    kept_by,
                } => {
                    let span = self.thir.exprs.span(outer);
                    let ty = self.types.real(self.thir.exprs[value].ty);
                    let value = self.lower_expr(value)?;
                    let temporary = slot_of(variable);
                    self.put(Place::whole(temporary), value, ty, span);
                    // A kept one is in `declared` with its variable (`declare`).
                    if kept_by.is_none() {
                        self.declared.push(temporary); // until its statement ends
                    }
                    break Place::whole(temporary);
                }
                // A value not in a variable: computed into memory, or a
                // reference in a register, gone through.
                _ => match self.lower_expr(outer)? {
                    Value::Memory(place) => break place,
                    Value::Moving(..) => unreachable!("a variable's value is read in place"),
                    Value::Register(address) => {
                        let Some(Ok(Step::Through)) = steps.pop() else {
                            unreachable!("only a reference's value is a register here")
                        };
                        break Place {
                            root: Root::At(address),
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

    /// Where field `index` of the struct or range value `value` is from
    /// its start.
    fn field_offset(&self, value: Id<thir::Expr>, index: u32) -> u64 {
        let ty = self.types.real(self.thir.exprs[value].ty);
        let (offset, _) = model::layout::field_placed(self.types, ty, index as usize, self.target);
        offset
    }

    /// The element `indexing` (`thir::ExprKind::Index`) takes of the
    /// elements at `elements`: its index, computed here as a `usize`, after
    /// stopping the program when it is outside them. A constant index into
    /// an array is inside, as the checker made sure. A slice's length is
    /// read after its index is computed, as the element itself is.
    fn lower_index(&mut self, indexing: Id<thir::Expr>, elements: &Place) -> Option<Step> {
        let span = self.thir.exprs.span(indexing);
        let thir::ExprKind::Index { array, index } = self.thir.exprs[indexing].kind else {
            unreachable!("the caller found an index")
        };
        let viewed = self.types.real(self.thir.exprs[array].ty);
        let (element, checked, count) = match self.types[viewed] {
            Type::Array {
                element, length, ..
            } if let Some(at) = self.known_position(index) => {
                let at = self.integer_constant(self.types.usize, at, span);
                (element, at, Some(length))
            }
            Type::Array {
                element, length, ..
            } => {
                let value = self.lower_expr(index)?.register();
                let message = array_index_message(length);
                let op = BinaryOp::GreaterOrEqual;
                let index_type = self.types.real(self.thir.exprs[index].ty);
                let checked = self.checked_usize(
                    value,
                    index_type,
                    op,
                    Length::Known(length),
                    &message,
                    span,
                );
                (element, checked, Some(length))
            }
            Type::Slice { element, .. } => {
                let value = self.lower_expr(index)?.register();
                // The slice's words are the place before its elements' step.
                let slice = Place {
                    root: elements.root,
                    steps: elements.steps[..elements.steps.len() - 1].into(),
                };
                let length = self.second_word(slice);
                let read = self.load(self.usize, length, span);
                let op = BinaryOp::GreaterOrEqual;
                let index_type = self.types.real(self.thir.exprs[index].ty);
                let checked = self.checked_usize(
                    value,
                    index_type,
                    op,
                    Length::Read(read),
                    SLICE_INDEX_MESSAGE,
                    span,
                );
                (element, checked, None)
            }
            _ => unreachable!("the checker indexes only an array or a slice"),
        };
        let (size, _) = layout(self.types, self.types.real(element), self.target);
        Some(Step::Index {
            index: checked,
            size,
            count,
        })
    }

    /// The integer `position`, an index or a slice's end, when it is a
    /// constant: the checker took only those that fit a `usize`, and an
    /// index into an array only one inside it.
    pub(super) fn known_position(&self, position: Id<thir::Expr>) -> Option<u64> {
        let thir::ExprKind::Constant(Constant::Integer(value)) = &self.thir.exprs[position].kind
        else {
            return None;
        };
        let Type::Integer(integer) = self.types[self.types.real(self.thir.exprs[position].ty)]
        else {
            unreachable!("a position is an integer")
        };
        let known = bigint::to_u64(integer.bits, integer.signed, value);
        Some(known.expect("the checker took only constant positions that fit a `usize`"))
    }

    /// `value`, an integer of the type `ty`, as a `usize`, after stopping
    /// the program with `message` when it is negative or `op` `length`,
    /// checked at `span`. One no wider than a `usize` is converted first, a
    /// negative one, extended, above any length; a wider one is compared in
    /// its own type before it is cut to one.
    pub(super) fn checked_usize(
        &mut self,
        value: Operand,
        ty: TypeId,
        op: BinaryOp,
        length: Length,
        message: &str,
        span: Span,
    ) -> Operand {
        let (&Type::Integer(integer), &Type::Integer(size)) =
            (&self.types[ty], &self.types[self.types.usize])
        else {
            unreachable!("a position and `usize` are integers")
        };
        let wide = integer.bits > size.bits;
        if wide && integer.signed {
            let zero = self.integer_constant(ty, 0, span);
            let class = Class::integer(integer.bits);
            let failed = self.compare(Comparison::SignedLess, class, value, zero, span);
            self.emit_check(failed, message, span);
        }
        let compared = if wide || ty == self.types.usize {
            value
        } else {
            self.convert_integer(value, integer, size, span)
        };
        let compared_type = if wide { ty } else { self.types.usize };
        let compared_class = Class::integer(if wide { integer.bits } else { size.bits });
        let limit = match length {
            Length::Known(length) => self.integer_constant(compared_type, length, span),
            Length::Read(length) if wide => self.convert_integer(length, size, integer, span),
            Length::Read(length) => length,
        };
        // The comparison is unsigned: a negative value, extended, is above
        // any length, and a wide signed one was refused above.
        let failed = match op {
            BinaryOp::GreaterOrEqual => self.compare(
                Comparison::UnsignedLessOrEqual,
                compared_class,
                limit,
                compared,
                span,
            ),
            BinaryOp::GreaterThan => self.compare(
                Comparison::UnsignedLess,
                compared_class,
                limit,
                compared,
                span,
            ),
            _ => unreachable!("a position is checked against a length with `>=` or `>`"),
        };
        self.emit_check(failed, message, span);
        if wide {
            self.convert_integer(value, integer, size, span)
        } else {
            compared
        }
    }

    /// `value`, of the integer type `from`, as the integer type `to`.
    pub(super) fn convert_integer(
        &mut self,
        value: Operand,
        from: Integer,
        to: Integer,
        span: Span,
    ) -> Operand {
        let (from_class, to_class) = (Class::integer(from.bits), Class::integer(to.bits));
        let converted = if from_class == to_class {
            value
        } else {
            let conversion = if to.bits < from.bits {
                Conversion::Truncate
            } else if from.signed {
                Conversion::SignExtend
            } else {
                Conversion::ZeroExtend
            };
            self.emit(
                InstructionKind::Convert {
                    conversion,
                    from: from_class,
                    to: to_class,
                    value,
                },
                span,
            )
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
        op: BinaryOp,
        left: Id<thir::Expr>,
        right: Id<thir::Expr>,
        span: Span,
    ) -> Option<Value> {
        let left_type = self.types.real(self.thir.exprs[left].ty);
        let right_type = self.types.real(self.thir.exprs[right].ty);
        let left_value = self.lower_expr(left)?.register();
        let right_value = self.lower_expr(right)?.register();
        let right_constant = matches!(right_value, Operand::Constant { .. });
        let number = &self.types[left_type];
        let value = match *number {
            Type::Integer(_) | Type::Float(_) => {
                let class = match *number {
                    Type::Integer(integer) => Class::integer(integer.bits),
                    Type::Float(Float::F32) => Class::F32,
                    _ => Class::F64,
                };
                match binary_operation(op, number) {
                    BinaryOperation::Compare {
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
                    BinaryOperation::Float(operation) => {
                        let kind = InstructionKind::Float {
                            operation,
                            class,
                            left: left_value,
                            right: right_value,
                        };
                        self.emit(kind, span)
                    }
                    BinaryOperation::Integer(operation) => {
                        let Type::Integer(integer) = *number else {
                            unreachable!("an integer operation is on integers")
                        };
                        let right = match operation {
                            IntegerOp::SignedDivide
                            | IntegerOp::UnsignedDivide
                            | IntegerOp::SignedRemainder
                            | IntegerOp::UnsignedRemainder => {
                                if !right_constant {
                                    let zero = Limbs::new(class.bits(), &[]);
                                    let zero = self.class_constant(class, &zero, span);
                                    let failed = self.compare(
                                        Comparison::Equal,
                                        class,
                                        right_value,
                                        zero,
                                        span,
                                    );
                                    self.emit_check(failed, DIVISION_BY_ZERO, span);
                                }
                                // The lowest signed value over -1 wraps to
                                // itself, so the result fits the width.
                                right_value
                            }
                            IntegerOp::ShiftLeft
                            | IntegerOp::SignedShiftRight
                            | IntegerOp::UnsignedShiftRight => {
                                let Type::Integer(count_type) = self.types[right_type] else {
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
            Type::Bool => {
                let class = Class::I8;
                match op {
                    BinaryOp::BitAnd => {
                        self.integer(IntegerOp::And, class, left_value, right_value, span)
                    }
                    BinaryOp::BitOr => {
                        self.integer(IntegerOp::Or, class, left_value, right_value, span)
                    }
                    BinaryOp::BitXor => {
                        self.integer(IntegerOp::Xor, class, left_value, right_value, span)
                    }
                    BinaryOp::EqualTo => {
                        self.compare(Comparison::Equal, class, left_value, right_value, span)
                    }
                    BinaryOp::NotEqualTo => {
                        self.compare(Comparison::NotEqual, class, left_value, right_value, span)
                    }
                    _ => unreachable!("the checker allows no other `bool` operator"),
                }
            }
            Type::RawPointer { .. } | Type::CString | Type::Reference { .. } => match op {
                // `rawptr + usize`: the address that many bytes on.
                BinaryOp::Add => self.integer(
                    IntegerOp::Add,
                    Class::Pointer,
                    left_value,
                    right_value,
                    span,
                ),
                BinaryOp::EqualTo => self.compare(
                    Comparison::Equal,
                    Class::Pointer,
                    left_value,
                    right_value,
                    span,
                ),
                BinaryOp::NotEqualTo => self.compare(
                    Comparison::NotEqual,
                    Class::Pointer,
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
        count: Operand,
        count_type: Integer,
        shifted: Integer,
        constant: bool,
        span: Span,
    ) -> Operand {
        let count_class = Class::integer(count_type.bits);
        if !constant {
            let width = Limbs::new(count_type.bits, &[u64::from(shifted.bits)]);
            let width = self.integer_operand(count_type, &width, span);
            // An unsigned count type too narrow to reach the width holds no
            // count outside it; a signed one holds negative counts.
            if count_type.signed || count_type.bits > shifted.bits.ilog2() {
                let failed = self.compare(
                    Comparison::UnsignedLessOrEqual,
                    count_class,
                    width,
                    count,
                    span,
                );
                self.emit_check(failed, &shift_count_message(shifted.bits), span);
            }
        }
        let shifted_class = Class::integer(shifted.bits);
        if count_class == shifted_class {
            return count;
        }
        // Inside the width, so it fits any class.
        if let Operand::Constant { bits, .. } = count {
            let count = Limbs::new(shifted.bits, &bits);
            return self.integer_operand(shifted, &count, span);
        }
        let conversion = if count_class.bits() > shifted_class.bits() {
            Conversion::Truncate
        } else {
            Conversion::ZeroExtend
        };
        self.emit(
            InstructionKind::Convert {
                conversion,
                from: count_class,
                to: shifted_class,
                value: count,
            },
            span,
        )
    }

    fn lower_unary(
        &mut self,
        op: UnaryOp,
        value: Id<thir::Expr>,
        ty: TypeId,
        span: Span,
    ) -> Option<Value> {
        let operand = self.lower_expr(value)?.register();
        let result = match (op, &self.types[ty]) {
            (UnaryOp::Negate, &Type::Integer(integer)) => {
                let zero = self.integer_operand(integer, &Limbs::new(integer.bits, &[]), span);
                self.arithmetic(IntegerOp::Subtract, integer, zero, operand, span)
            }
            (UnaryOp::Negate, &Type::Float(float)) => {
                let class = if float == Float::F32 {
                    Class::F32
                } else {
                    Class::F64
                };
                // From -0.0, so that -(0.0) is -0.0 and -(-0.0) is 0.0.
                let sign = if float == Float::F32 {
                    1 << 31
                } else {
                    1 << 63
                };
                let zero = Operand::integer(class, sign);
                let kind = InstructionKind::Float {
                    operation: model::mir::FloatOp::Subtract,
                    class,
                    left: zero,
                    right: operand,
                };
                self.emit(kind, span)
            }
            // All ones of the type, extended as its values are, so the bits
            // above the width flip to what they should be.
            (UnaryOp::BitNot, &Type::Integer(integer)) => {
                let class = Class::integer(integer.bits);
                let ones = vec![u64::MAX; integer.bits.div_ceil(64) as usize];
                let ones = Limbs::new(integer.bits, &ones);
                let ones = self.integer_operand(integer, &ones, span);
                self.integer(IntegerOp::Xor, class, operand, ones, span)
            }
            (UnaryOp::LogicalNot, Type::Bool) => {
                let zero = Operand::integer(Class::I8, 0);
                self.compare(Comparison::Equal, Class::I8, operand, zero, span)
            }
            _ => unreachable!("the checker allows no other unary operator"),
        };
        Some(Value::Register(result))
    }

    /// The number or `bool` `value` converted to the type `to`.
    fn lower_convert(&mut self, value: Id<thir::Expr>, to: TypeId, span: Span) -> Option<Value> {
        let from = self.types.real(self.thir.exprs[value].ty);
        let operand = self.lower_expr(value)?.register();
        let converted = match (&self.types[from], &self.types[to]) {
            (&Type::Integer(from), &Type::Integer(to)) => {
                self.convert_integer(operand, from, to, span)
            }
            (Type::Bool, &Type::Integer(to)) => {
                let bit = Integer {
                    signed: false,
                    bits: 1,
                };
                self.convert_integer(operand, bit, to, span)
            }
            (&Type::Integer(from), &Type::Float(to)) => {
                let conversion = if from.signed {
                    Conversion::SignedToFloat
                } else {
                    Conversion::UnsignedToFloat
                };
                self.convert(
                    conversion,
                    Class::integer(from.bits),
                    float_class(to),
                    operand,
                    span,
                )
            }
            (&Type::Float(from), &Type::Integer(to)) => {
                let conversion = if to.signed {
                    Conversion::FloatToSigned { bits: to.bits }
                } else {
                    Conversion::FloatToUnsigned { bits: to.bits }
                };
                let class = Class::integer(to.bits);
                self.convert(conversion, float_class(from), class, operand, span)
            }
            (Type::Float(Float::F32), Type::Float(Float::F64)) => self.convert(
                Conversion::FloatWiden,
                Class::F32,
                Class::F64,
                operand,
                span,
            ),
            (Type::Float(Float::F64), Type::Float(Float::F32)) => self.convert(
                Conversion::FloatNarrow,
                Class::F64,
                Class::F32,
                operand,
                span,
            ),
            (Type::RawPointer { .. }, Type::RawPointer { .. }) => self.convert(
                Conversion::PointerCast,
                Class::Pointer,
                Class::Pointer,
                operand,
                span,
            ),
            _ => unreachable!("the checker converts numbers, `bool`s and raw pointers only"),
        };
        Some(Value::Register(converted))
    }

    fn convert(
        &mut self,
        conversion: Conversion,
        from: Class,
        to: Class,
        value: Operand,
        span: Span,
    ) -> Operand {
        let kind = InstructionKind::Convert {
            conversion,
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
    pub(super) fn lower_into(&mut self, expression: Id<thir::Expr>, place: Place) -> Option<()> {
        // Literals nest as deep as the source.
        source::grow_stack(|| {
            let span = self.thir.exprs.span(expression);
            let thir::Expr { ty, kind } = &self.thir.exprs[expression];
            let ty = self.types.real(*ty);
            match kind {
                _ if ty == TypeId::INVALID => None,
                thir::ExprKind::StructLiteral(fields) => {
                    self.lower_struct_literal(*fields, ty, place)
                }
                thir::ExprKind::Constant(Constant::Bytes(bytes)) if ty == TypeId::STR => {
                    self.str_constant(bytes, place, span);
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
                } if matches!(self.result_passing(ty), Some(Passing::Memory { .. })) => {
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
        callee: Callee,
        signature: TypeId,
        arguments: arena::List<thir::Expr>,
        ty: TypeId,
        destination: Option<Place>,
        span: Span,
    ) -> Option<Value> {
        let Type::Function {
            variadic, origins, ..
        } = &self.types[self.types.real(signature)]
        else {
            unreachable!("a call's signature is a function type")
        };
        let mut passed = Vec::with_capacity(arguments.len() + 1);
        let mut parameters = Vec::with_capacity(arguments.len());
        let c_function = match callee {
            Callee::Function(function) => {
                model::program::c_symbol(self.modules, function.definition).is_some()
            }
            Callee::Method(_) => false,
        };
        let mut argument_types = Vec::new();
        let result = self.result_passing(ty);
        let result_place = match result {
            Some(Passing::Memory { size, .. }) => {
                let place = destination.unwrap_or_else(|| Place::whole(self.temporary(ty, span)));
                let kind = InstructionKind::Address {
                    place: place.clone(),
                    size,
                };
                let address = self.emit(kind, span);
                passed.push(address);
                Some(place)
            }
            _ => None,
        };
        // Each argument is passed as it is when lowered, before the next
        // one runs: a value in memory is copied then.
        let mut target = match callee {
            Callee::Function(function) => Some(MirCallee::Function(function)),
            Callee::Method(_) => None, // read from the receiver, the first argument
        };
        for argument in self.thir.children.lists.items(arguments) {
            let argument_type = self.types.real(self.thir.exprs[argument].ty);
            // The interface value's address is the receiver, and its table
            // holds the method at `slot`.
            if let (None, Callee::Method(slot)) = (target, callee) {
                // An owned interface taken by value moves once its words
                // are read.
                let (view, moved_at) = match self.lower_expr(argument)? {
                    Value::Moving(place, read) => (place, Some(read)),
                    value => (value.memory(), None),
                };
                let address = self.load(Class::Pointer, view.clone(), span);
                let table_place = view.clone().then(Step::Offset(self.pointer_bytes));
                let table = self.load(Class::Pointer, table_place, span);
                if let Some(read) = moved_at {
                    self.mark_moved(view, argument_type, read);
                }
                let method_place = Place {
                    root: Root::At(table),
                    steps: Box::new([Step::Offset(u64::from(slot) * self.pointer_bytes)]),
                };
                target = Some(MirCallee::Pointer(self.load(
                    Class::Pointer,
                    method_place,
                    span,
                )));
                passed.push(address);
                parameters.push(Passing::Register {
                    class: Class::Pointer,
                    signed: false,
                });
                continue;
            }
            // An argument to a parameter whose type has an error, reported
            // where it was written, which may be another module.
            if argument_type == TypeId::INVALID {
                return None;
            }
            let passing = self.passing(argument_type);
            parameters.push(passing);
            argument_types.push(argument_type);
            match passing {
                Passing::Register { .. } => passed.push(self.lower_expr(argument)?.register()),
                Passing::Memory { size, .. } => {
                    let copy = self.temporary(argument_type, span);
                    self.lower_into(argument, Place::whole(copy))?;
                    let kind = InstructionKind::Address {
                        place: Place::whole(copy),
                        size,
                    };
                    passed.push(self.emit(kind, span));
                }
            }
        }
        let callee = target.expect("a method call has its receiver");
        let c = c_function
            .then(|| c_signature(self.types, self.target, ty, argument_types.into_iter()));
        self.mir.signatures.push(Signature {
            parameters: parameters.into(),
            result,
            variadic: *variadic,
            c,
            origins: origins.clone(),
        });
        let kind = InstructionKind::Call {
            callee,
            signature: (self.mir.signatures.len() - 1) as u32,
            arguments: passed.into(),
        };
        let mut called = self.emit(kind, span);
        // C leaves the bits of a register above a narrow integer unset.
        if let (true, &Type::Integer(integer)) = (c_function, &self.types[self.types.real(ty)]) {
            called = self.narrow(called, integer, span);
        }
        Some(match (result, result_place) {
            (Some(Passing::Register { .. }), _) => Value::Register(called),
            (Some(Passing::Memory { .. }), Some(place)) => Value::Memory(place),
            _ => Value::Nothing,
        })
    }

    /// The interface value of the type `ty` viewing what `reference`
    /// points at, with the table of `implementation`: its two words at
    /// `place`.
    fn lower_to_interface(
        &mut self,
        reference: Operand,
        implementation: ImplementationId,
        ty: TypeId,
        place: Place,
        span: Span,
    ) {
        let methods = self.types.implementation(implementation);
        let interface = match self.types[ty] {
            Type::InterfaceReference { interface, .. } => interface,
            _ => ty, // an owned interface
        };
        let Type::Interface {
            methods: signatures,
            ..
        } = &self.types[interface]
        else {
            unreachable!("an interface reference's interface is checked")
        };
        // As a call through the table passes them: `self` as an address.
        let table = (methods.iter().zip(signatures))
            .map(|(&definition, &signature)| {
                let Type::Function {
                    parameters,
                    result,
                    origins,
                    ..
                } = &self.types[signature]
                else {
                    unreachable!("a method's signature is a function type")
                };
                let receiver = Passing::Register {
                    class: Class::Pointer,
                    signed: false,
                };
                let parameters = std::iter::once(receiver)
                    .chain(parameters[1..].iter().map(|&ty| self.passing(ty)));
                let copy = FunctionCopy {
                    definition,
                    arguments: ConstArguments::NONE,
                };
                let signature = Signature {
                    parameters: parameters.collect(),
                    result: self.result_passing(*result),
                    variadic: false,
                    c: None,
                    origins: origins.clone(),
                };
                (copy, signature)
            })
            .collect();
        let table = self.global(Global::Functions(table));
        self.store(Class::Pointer, place.clone(), reference, span);
        let table_place = place.then(Step::Offset(self.pointer_bytes));
        self.store(Class::Pointer, table_place, table, span);
    }

    /// `&borrowed` or `&mut borrowed` as the slice or interface reference
    /// type `ty`, at `to`: a loan of the place, and for a slice its two
    /// words, for an interface reference the struct's address and table. A
    /// reference is one register (`lower_expr`).
    fn lower_borrow(
        &mut self,
        borrowed: Id<thir::Expr>,
        ty: TypeId,
        to: Place,
        span: Span,
    ) -> Option<()> {
        let Some(mutable) = self.types[ty].borrows() else {
            unreachable!("a borrow's type is a reference, slice or interface reference")
        };
        let place = self.place_of(borrowed)?;
        let borrowed_type = self.types.real(self.thir.exprs[borrowed].ty);
        match (&self.types[borrowed_type], &self.types[ty]) {
            // A slice of an array's elements, or a slice's elements lent again.
            (_, Type::Slice { .. } | Type::Str) => {
                let (elements, size, length) =
                    if let Type::Array { length, .. } = self.types[borrowed_type] {
                        let (size, _) = layout(self.types, borrowed_type, self.target);
                        let length = self.integer_constant(self.types.usize, length, span);
                        (place, Some(size), length)
                    } else {
                        let length = self.second_word(place.clone());
                        let length = self.load(self.usize, length, span);
                        (Self::through_slice(place), None, length)
                    };
                let address = self.borrow(elements, size, mutable, &SLICE_WORDS, span);
                self.slice_words(address, length, to, span);
            }
            // An interface's struct lent again as an interface reference, under a
            // new loan: what an interface reference views (the place ends by
            // going through it, so its two words are before that), or what an
            // owned interface took (behind its own two words).
            (Type::Interface { .. }, Type::InterfaceReference { .. }) => {
                let (words, struct_place) = match place.steps.last() {
                    Some(Step::Through) => (
                        Place {
                            root: place.root,
                            steps: place.steps[..place.steps.len() - 1].into(),
                        },
                        place,
                    ),
                    _ => (place.clone(), Self::through_slice(place)),
                };
                // Only the program knows which struct it views.
                let address = self.borrow(struct_place, None, mutable, &REFERENCE_WORDS, span);
                let table_place = words.then(Step::Offset(self.pointer_bytes));
                let table = self.load(Class::Pointer, table_place, span);
                self.store(Class::Pointer, to.clone(), address, span);
                let table_at = to.then(Step::Offset(self.pointer_bytes));
                self.store(Class::Pointer, table_at, table, span);
            }
            _ => unreachable!("a reference is one register"),
        }
        Some(())
    }

    /// A loan of `size` bytes from `place` (`None`: all there are),
    /// named by `words` in messages: its address.
    pub(super) fn borrow(
        &mut self,
        place: Place,
        size: Option<u64>,
        mutable: bool,
        words: &'static LoanWords,
        span: Span,
    ) -> Operand {
        let kind = InstructionKind::Borrow {
            place,
            size,
            mutable,
            words,
        };
        self.emit(kind, span)
    }

    /// A slice value at `place`: `address` and `length` as its two words.
    pub(super) fn slice_words(
        &mut self,
        address: Operand,
        length: Operand,
        place: Place,
        span: Span,
    ) {
        self.store(Class::Pointer, place.clone(), address, span);
        let length_place = self.second_word(place);
        self.store(self.usize, length_place, length, span);
    }

    /// `left && right` or `left || right`: `right` runs only when `left`
    /// does not decide the result, and a hidden byte holds the result where
    /// both meet.
    fn lower_short_circuit(
        &mut self,
        op: BinaryOp,
        left: Id<thir::Expr>,
        right: Id<thir::Expr>,
        span: Span,
    ) -> Option<Value> {
        let (left_span, right_span) = (self.thir.exprs.span(left), self.thir.exprs.span(right));
        let result = self.register_slot(Class::I8, span);
        // Each side's temporaries end with it: a `bool` holds no reference,
        // and `right` may not run.
        let temporaries = self.declared.len();
        let left = self.lower_expr(left)?.register();
        self.close_scopes(temporaries, left_span);
        // When `left` decides, `&&` is false and `||` is true.
        let decided = Operand::integer(Class::I8, u64::from(op == BinaryOp::LogicalOr));
        self.store(Class::I8, Place::whole(result), decided, span);
        let right_block = self.new_block(right_span);
        let join = self.new_block(span);
        let (then, otherwise) = match op {
            BinaryOp::LogicalAnd => (right_block, join),
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
        self.store(Class::I8, Place::whole(result), right, span);
        self.close_scopes(temporaries, right_span);
        self.finish(model::mir::Terminator::Jump(join));
        self.block = join;
        Some(Value::Register(self.load(
            Class::I8,
            Place::whole(result),
            span,
        )))
    }
}

/// The class of a float of the type `float`.
pub(super) fn float_class(float: Float) -> Class {
    match float {
        Float::F32 => Class::F32,
        Float::F64 => Class::F64,
    }
}
