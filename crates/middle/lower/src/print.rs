//! Lowering `print` and `println`: the text between the values is
//! gathered and written in one runtime call; each value is written by the
//! runtime function for its kind.

use arena::List;
use model::mir::Callee;
use model::mir::Class;
use model::mir::Conversion;
use model::mir::Global;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Place;
use model::mir::Root;
use model::mir::Signature;
use model::mir::Terminator;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use runtime_abi::symbols::Runtime;
use source::Span;
use syntax::ast::Language;

use crate::Lowering;
use crate::Value;

impl<L: Language> Lowering<'_, L> {
    /// `print(values)`, or `println(values)` when `newline`: the values
    /// with a space between each, then what was kept back is written out.
    pub(super) fn lower_print(
        &mut self,
        newline: bool,
        values: List<thir::Expr>,
        span: Span,
    ) -> Option<()> {
        // Every value is worked out before any is written, as a value's
        // code may print too.
        let values: Vec<_> = self.thir.children.lists.items(values).collect();
        let mut lowered = Vec::with_capacity(values.len());
        let last_call = values.iter().rposition(|&value| self.thir.has_call(value));
        for (index, &value) in values.iter().enumerate() {
            let ty = self.types.real(self.thir.exprs[value].ty);
            let called_later = last_call.is_some_and(|last| index < last);
            let value = self.lower_expr(value)?;
            lowered.push((self.settled(value, ty, called_later, span), ty));
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
        self.runtime_call(Runtime::Flush, &[], span);
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
                    self.convert(conversion, class, wide, value, span)
                };
                let limbs = self.register_slot(wide, span);
                self.store(wide, Place::whole(limbs), value, span);
                let kind = InstructionKind::Address {
                    place: Place::whole(limbs),
                    size: wide.bytes(self.target.pointer_bytes()),
                };
                let address = self.emit(kind, span);
                self.print_text(text, span);
                let signed = Operand::integer(Class::I32, u64::from(integer.signed));
                let count = Operand::integer(Class::I32, u64::from(count));
                self.runtime_call(Runtime::PrintInt, &[signed, count, address], span);
            }
            Type::Float(float) => {
                let value = value.register();
                let (value, bits) = match float {
                    Float::F32 => (
                        self.convert(Conversion::FloatWiden, Class::F32, Class::F64, value, span),
                        32,
                    ),
                    Float::F64 => (value, 64),
                };
                self.print_text(text, span);
                let bits = Operand::integer(Class::I32, bits);
                self.runtime_call(Runtime::PrintFloat, &[value, bits], span);
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
                self.runtime_call(Runtime::PrintText, &[value.register()], span);
            }
            Type::Str => {
                let place = value.memory();
                let address = self.load(Class::Pointer, place.clone(), span);
                let length = self.second_word(place);
                let length = self.load(self.usize, length, span);
                self.print_text(text, span);
                self.runtime_call(Runtime::PrintBytes, &[address, length], span);
            }
            Type::RawPointer { .. } => {
                self.print_text(text, span);
                self.runtime_call(Runtime::PrintPointer, &[value.register()], span);
            }
            // What a reference points at: its address is a value kept in
            // memory, or holds a register.
            Type::Reference { target, .. } => {
                let target = self.types.real(target);
                let at = Place {
                    root: Root::At(value.register()),
                    steps: Box::default(),
                };
                // A `&str` is printed from its place, kept in memory.
                let referred = match self.class(target) {
                    Some(class) => Value::Register(self.load(class, at, span)),
                    None => Value::Memory(at),
                };
                self.print_value(text, referred, target, span);
            }
            _ => unreachable!("check prints only numbers, `bool`, `&cstr`, `&str` and pointers"),
        }
    }

    /// Writes `text`, if any, in one runtime call, and empties it.
    fn print_text(&mut self, text: &mut Vec<u8>, span: Span) {
        if text.is_empty() {
            return;
        }
        let length = Operand::integer(self.usize, text.len() as u64);
        let bytes = self.global(Global::Bytes(std::mem::take(text).into()));
        self.runtime_call(Runtime::PrintBytes, &[bytes, length], span);
    }

    /// A call of the runtime function `runtime` with `arguments`, one per
    /// parameter.
    fn runtime_call(&mut self, runtime: Runtime, arguments: &[Operand], span: Span) {
        let signature = Signature::of_runtime(runtime, self.usize);
        self.mir.signatures.push(signature);
        let kind = InstructionKind::Call {
            callee: Callee::Runtime(runtime),
            signature: (self.mir.signatures.len() - 1) as u32,
            arguments: arguments.into(),
            result: None, // the runtime gives no result in memory
        };
        self.emit(kind, span);
    }
}
