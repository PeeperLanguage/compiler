//! The `#[builtin]` functions the checker works out itself, and the built-in
//! types it does not handle yet. The types it does handle are named in
//! `model::semantic_types` (`Types::builtin`).

/// What to call the built-in type `name` in a "not supported yet" report, if
/// it is one the checker does not handle yet.
pub(crate) fn unsupported_type(name: &str) -> Option<&'static str> {
    match name {
        "string" => Some("string types"),
        "byte" | "char" => Some("byte and char types"),
        _ => None,
    }
}

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
