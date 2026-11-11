//! Lowering `print` and `println`: the text between the values is
//! gathered and written in one runtime call; each value is written by the
//! runtime function for its kind, a struct field by field and an array
//! element by element.
//!
//! shortcut: structs and arrays print built in until a trait or interface
//! lets each type say how it prints (`check` `printable`).

use arena::List;
use model::core_mir::Callee;
use model::core_mir::Class;
use model::core_mir::Comparison;
use model::core_mir::Conversion;
use model::core_mir::Global;
use model::core_mir::InstructionKind;
use model::core_mir::IntegerOp;
use model::core_mir::Operand;
use model::core_mir::Passing;
use model::core_mir::Place;
use model::core_mir::Root;
use model::core_mir::Signature;
use model::core_mir::Step;
use model::core_mir::Terminator;
use model::layout::in_memory;
use model::layout::layout;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use runtime_abi::symbols::Carrier;
use runtime_abi::symbols::FLUSH;
use runtime_abi::symbols::PRINT_BYTES;
use runtime_abi::symbols::PRINT_FLOAT;
use runtime_abi::symbols::PRINT_INT;
use runtime_abi::symbols::PRINT_POINTER;
use runtime_abi::symbols::PRINT_TEXT;
use source::Span;

use crate::core::Lowering;
use crate::core::Value;

impl Lowering<'_> {
    /// `print(values)`, or `println(values)` when `newline`: the values
    /// with a space between each, then what was kept back is written out.
    pub(super) fn lower_print(
        &mut self,
        newline: bool,
        values: List<thir::Expr>,
        span: Span,
    ) -> Option<()> {
        // Every value is worked out before any is written, as a value's
        // code may print too; one in memory is copied when code follows
        // that could change it.
        let values: Vec<_> = self.thir.children.lists.items(values).collect();
        let mut lowered = Vec::with_capacity(values.len());
        for (index, &value) in values.iter().enumerate() {
            let ty = self.types.real(self.thir.exprs[value].ty);
            let value = match self.lower_expr(value)? {
                Value::Memory(place) if index + 1 < values.len() => {
                    let copy = self.temporary(ty, span);
                    self.put(Place::whole(copy), Value::Memory(place), ty, span);
                    Value::Memory(Place::whole(copy))
                }
                value => value,
            };
            lowered.push((value, ty));
        }
        let mut text = Vec::new();
        for (index, (value, ty)) in lowered.into_iter().enumerate() {
            if index > 0 {
                text.push(b' ');
            }
            self.print_value(&mut text, value, ty, span);
        }
        if newline {
            text.push(b'\n');
        }
        self.print_text(&mut text, span);
        self.runtime_call(&FLUSH, &[], span);
        Some(())
    }

    /// Writes `value`, of the type `ty`, after `text`.
    fn print_value(&mut self, text: &mut Vec<u8>, value: Value, ty: TypeId, span: Span) {
        match self.types[ty] {
            Type::Integer(integer) => {
                // In memory as 64-bit limbs, least significant first: every
                // target is little-endian.
                let count = integer.bits.div_ceil(64);
                let class = Class::integer(integer.bits);
                let wide = Class::integer(count * 64);
                let value = value.register();
                let value = if class == wide {
                    value
                } else {
                    let conversion = if integer.signed {
                        Conversion::SignExtend
                    } else {
                        Conversion::ZeroExtend
                    };
                    let kind = InstructionKind::Convert {
                        conversion,
                        from: class,
                        to: wide,
                        value,
                    };
                    self.emit(kind, span)
                };
                let limbs = self.register_slot(wide, span);
                self.store(wide, Place::whole(limbs), value, span);
                let kind = InstructionKind::Address {
                    place: Place::whole(limbs),
                    size: wide.bytes(self.pointer_bytes),
                };
                let address = self.emit(kind, span);
                self.print_text(text, span);
                let signed = Operand::integer(Class::I32, u64::from(integer.signed));
                let count = Operand::integer(Class::I32, u64::from(count));
                self.runtime_call(&PRINT_INT, &[signed, count, address], span);
            }
            Type::Float(float) => {
                let value = value.register();
                let (value, bits) = match float {
                    Float::F32 => {
                        let kind = InstructionKind::Convert {
                            conversion: Conversion::FloatWiden,
                            from: Class::F32,
                            to: Class::F64,
                            value,
                        };
                        (self.emit(kind, span), 32)
                    }
                    Float::F64 => (value, 64),
                };
                self.print_text(text, span);
                let bits = Operand::integer(Class::I32, bits);
                self.runtime_call(&PRINT_FLOAT, &[value, bits], span);
            }
            Type::Bool => {
                self.print_text(text, span);
                let value = value.register();
                let (yes, no, join) = (
                    self.new_block(span),
                    self.new_block(span),
                    self.new_block(span),
                );
                self.finish(Terminator::Branch {
                    condition: value,
                    span,
                    then: yes,
                    otherwise: no,
                });
                for (block, word) in [(yes, &b"true"[..]), (no, &b"false"[..])] {
                    self.block = block;
                    let mut word = word.to_vec();
                    self.print_text(&mut word, span);
                    self.finish(Terminator::Jump(join));
                }
                self.block = join;
            }
            Type::CString => {
                self.print_text(text, span);
                self.runtime_call(&PRINT_TEXT, &[value.register()], span);
            }
            Type::Str => {
                let place = value.memory();
                let address = self.load(Class::Pointer, place.clone(), span);
                let length = self.second_word(place);
                let length = self.load(self.usize, length, span);
                self.print_text(text, span);
                self.runtime_call(&PRINT_BYTES, &[address, length], span);
            }
            Type::RawPointer { .. } => {
                self.print_text(text, span);
                self.runtime_call(&PRINT_POINTER, &[value.register()], span);
            }
            // What a reference points at: its address is a value kept in
            // memory, or holds a register.
            Type::Reference { target, .. } => {
                let target = self.types.real(target);
                let at = Place {
                    root: Root::At(value.register()),
                    steps: Box::default(),
                };
                let referred = self.read_for_print(at, target, span);
                self.print_value(text, referred, target, span);
            }
            Type::Array { length: 0, .. } => text.extend_from_slice(b"[]"),
            Type::Array {
                element, length, ..
            } => {
                let element = self.types.real(element);
                let (size, _) = layout(self.types, element, self.target);
                let place = value.memory();
                text.push(b'[');
                self.print_text(text, span);
                // A loop, so the code does not grow with the length.
                let index = self.register_slot(self.usize, span);
                let zero = Operand::integer(self.usize, 0);
                self.store(self.usize, Place::whole(index), zero, span);
                let (body, between, element_block, done) = (
                    self.new_block(span),
                    self.new_block(span),
                    self.new_block(span),
                    self.new_block(span),
                );
                self.finish(Terminator::Jump(body));
                self.block = body;
                let at = self.load(self.usize, Place::whole(index), span);
                let first = self.compare(Comparison::Equal, self.usize, at, zero, span);
                self.finish(Terminator::Branch {
                    condition: first,
                    span,
                    then: element_block,
                    otherwise: between,
                });
                self.block = between;
                let mut separator = b", ".to_vec();
                self.print_text(&mut separator, span);
                self.finish(Terminator::Jump(element_block));
                self.block = element_block;
                let count = Some(length);
                let at_element = place.then(Step::Index {
                    index: at,
                    size,
                    count,
                });
                let value = self.read_for_print(at_element, element, span);
                let mut piece = Vec::new();
                source::grow_stack(|| self.print_value(&mut piece, value, element, span));
                self.print_text(&mut piece, span);
                let one = Operand::integer(self.usize, 1);
                let next = self.integer(IntegerOp::Add, self.usize, at, one, span);
                self.store(self.usize, Place::whole(index), next, span);
                let limit = Operand::integer(self.usize, length);
                let more = self.compare(Comparison::UnsignedLess, self.usize, next, limit, span);
                self.finish(Terminator::Branch {
                    condition: more,
                    span,
                    then: body,
                    otherwise: done,
                });
                self.block = done;
                text.push(b']');
            }
            Type::Struct { definition, .. } => {
                let place = value.memory();
                text.extend_from_slice(
                    model::program::declared_text(self.modules, definition).as_bytes(),
                );
                let names: Vec<String> = model::program::field_names(self.modules, definition)
                    .map(str::to_string)
                    .collect();
                for (index, name) in names.iter().enumerate() {
                    text.extend_from_slice(if index == 0 { b" { " } else { b", " });
                    text.extend_from_slice(name.as_bytes());
                    text.extend_from_slice(b": ");
                    let (at, field) = self.field(place.clone(), ty, index);
                    let value = self.read_for_print(at, field, span);
                    source::grow_stack(|| self.print_value(text, value, field, span));
                }
                text.extend_from_slice(if names.is_empty() { b" {}" } else { b" }" });
            }
            _ => unreachable!(
                "check prints only numbers, `bool`, `&cstr`, `&str`, pointers, structs and arrays"
            ),
        }
    }

    /// The value of the type `ty` at `place`, to print: loaded, or the
    /// place itself for a value kept in memory. Nothing is moved.
    fn read_for_print(&mut self, place: Place, ty: TypeId, span: Span) -> Value {
        if let Some(class) = self.class(ty) {
            return Value::Register(self.load(class, place, span));
        }
        debug_assert!(in_memory(self.types, ty) || self.types[ty].is_slice_shaped());
        Value::Memory(place)
    }

    /// Writes `text`, if any, in one runtime call, and empties it.
    fn print_text(&mut self, text: &mut Vec<u8>, span: Span) {
        if text.is_empty() {
            return;
        }
        let length = Operand::integer(self.usize, text.len() as u64);
        let bytes = self.global(Global::Bytes(std::mem::take(text).into()));
        self.runtime_call(&PRINT_BYTES, &[bytes, length], span);
    }

    /// A call of the runtime function `signature` with `arguments`, one
    /// per parameter.
    fn runtime_call(
        &mut self,
        signature: &runtime_abi::symbols::Signature,
        arguments: &[Operand],
        span: Span,
    ) -> Operand {
        let passing = |carrier: &Carrier| match carrier {
            Carrier::I32 => Passing::Register {
                class: Class::I32,
                signed: true,
            },
            Carrier::Usize => Passing::Register {
                class: self.usize,
                signed: false,
            },
            Carrier::F64 => Passing::Register {
                class: Class::F64,
                signed: false,
            },
            Carrier::Pointer => Passing::Register {
                class: Class::Pointer,
                signed: false,
            },
        };
        self.mir.signatures.push(Signature {
            parameters: signature.parameters.iter().map(passing).collect(),
            result: signature.result.as_ref().map(passing),
            variadic: false,
            c: None,
            origins: Box::default(), // the runtime returns no pointer
        });
        let kind = InstructionKind::Call {
            callee: Callee::Runtime(signature.symbol),
            signature: (self.mir.signatures.len() - 1) as u32,
            arguments: arguments.into(),
        };
        self.emit(kind, span)
    }
}
