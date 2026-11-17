//! The `#[builtin]` functions the checker works out itself.

use bigint::Limbs;
use model::DeclarationCopy;
use model::constant;
use model::layout;
use model::semantic_types;
use source::Span;

use crate::Checked;
use crate::Checker;

/// What a `#[builtin]` function gives of the type it takes, on the target
/// being built.
#[derive(Clone, Copy)]
pub(crate) enum TypeFact {
    /// How many bytes a value of the type takes, its padding included.
    Size,
    /// The multiple of bytes a value of the type starts at in memory.
    Align,
    /// How many bits an integer or a float type has.
    Bits,
    /// Whether an integer type has negative values.
    Signed,
    /// Whether the type is an integer type, of any width.
    IsInteger,
    /// Whether the type is a float type.
    IsFloat,
    /// Whether the type is `&str`, text.
    IsText,
    /// Whether the type is `bool`.
    IsBool,
    /// Whether the type is a raw pointer: `*T` or `rawptr`.
    IsPointer,
}

/// What the `#[builtin]` function named `name` gives, if the compiler has
/// one of that name.
pub(crate) fn type_fact(name: &str) -> Option<TypeFact> {
    match name {
        "size_of" => Some(TypeFact::Size),
        "align_of" => Some(TypeFact::Align),
        "bits_of" => Some(TypeFact::Bits),
        "is_signed" => Some(TypeFact::Signed),
        "is_int" => Some(TypeFact::IsInteger),
        "is_float" => Some(TypeFact::IsFloat),
        "is_text" => Some(TypeFact::IsText),
        "is_bool" => Some(TypeFact::IsBool),
        "is_pointer" => Some(TypeFact::IsPointer),
        _ => None,
    }
}

impl<L: Checked> Checker<'_, L> {
    /// What the `#[builtin]` function `function`, a copy for the type it
    /// is given, answers when its call at `span` runs (`evaluate_call`): the
    /// fact of that type its name asks for. `None` for a type the fact is
    /// not of, reported.
    pub(crate) fn builtin_value(
        &mut self,
        function: DeclarationCopy,
        span: Span,
    ) -> Option<constant::Value> {
        let &[semantic_types::ConstArgument::Type(ty)] = self.types.arguments(function.arguments)
        else {
            unreachable!("a `#[builtin]` function takes one type")
        };
        let (name, file) = model::program::declared_name(self.modules, function.definition);
        let name = name.text(file);
        let fact = type_fact(name).expect("a `#[builtin]` function is one the compiler has");
        let (size, align) = layout::layout(&self.types, ty, self.target);
        let number =
            |value: u64| constant::Value::Integer(Limbs::new(self.target.pointer_bits, &[value]));
        Some(match (fact, &self.types[ty]) {
            (TypeFact::Size, _) => number(size),
            (TypeFact::Align, _) => number(align),
            (TypeFact::Bits, semantic_types::Type::Integer(integer)) => {
                number(u64::from(integer.bits))
            }
            (TypeFact::Bits, semantic_types::Type::Float(semantic_types::Float::F32)) => number(32),
            (TypeFact::Bits, semantic_types::Type::Float(semantic_types::Float::F64)) => number(64),
            (TypeFact::Signed, semantic_types::Type::Integer(integer)) => {
                constant::Value::Bool(integer.signed)
            }
            (TypeFact::IsInteger, kind) => {
                constant::Value::Bool(matches!(kind, semantic_types::Type::Integer(_)))
            }
            (TypeFact::IsFloat, kind) => {
                constant::Value::Bool(matches!(kind, semantic_types::Type::Float(_)))
            }
            (TypeFact::IsText, kind) => {
                constant::Value::Bool(matches!(kind, semantic_types::Type::Str))
            }
            (TypeFact::IsBool, kind) => {
                constant::Value::Bool(matches!(kind, semantic_types::Type::Bool))
            }
            (TypeFact::IsPointer, kind) => {
                constant::Value::Bool(matches!(kind, semantic_types::Type::RawPointer { .. }))
            }
            (TypeFact::Bits | TypeFact::Signed, _) => {
                let kinds = match fact {
                    TypeFact::Bits => "an integer or a float",
                    _ => "an integer",
                };
                let found = self.type_text(ty);
                let message = format!("`{name}` takes {kinds} type, found `{found}`");
                self.error(diagnostics::INVALID_OPERATION, message, span);
                return None;
            }
        })
    }
}
