//! The built-in types, one table: each with its name and doc, and the
//! editor docs worked out from it.

use super::Float;
use super::Integer;
use super::Type;
use super::TypeId;
use super::Types;
use super::integers;

/// Declares the built-in types, one row each. First the types with fixed
/// ids: `FIXED`, the types in id order (they need no lookup); a `TypeId`
/// constant for each, numbered in the order written; `NAMED`, the name a
/// program writes for each type given one; and `fixed_doc`, the doc comment
/// written on a type's row. Then, after `;`, the names written with their
/// values' type in brackets (`range[i32]`), whose types `Types` makes per
/// element type, as it makes arrays: a constant for each name, and
/// `bracketed_doc`, the doc comment on its row. Other integers are named by
/// rule (`Types::builtin`).
macro_rules! builtin_types {
    (
        $(
            $(#[doc = $doc:literal] $(#[doc = $doc_more:literal])*)?
            $name:ident $($text:literal)? => $ty:expr,
        )*
        ;
        $(
            #[doc = $bracketed_doc:literal] $(#[doc = $bracketed_doc_more:literal])*
            $bracketed:ident $bracketed_text:literal,
        )*
    ) => {
        pub(super) const FIXED: &[Type] = &[$($ty,)*];

        /// The position of each fixed type in `FIXED`, counted by the compiler.
        #[allow(non_camel_case_types, clippy::upper_case_acronyms)]
        enum Position {
            $($name,)*
        }

        impl TypeId {
            $(
                $(#[doc = $doc] $(#[doc = $doc_more])*)?
                pub const $name: TypeId = TypeId(Position::$name as u32);
            )*
        }

        pub(super) const NAMED: &[(&str, TypeId)] = &[$($(($text, TypeId::$name),)?)*];

        /// The doc comment on the row of the fixed type `id`, as Markdown,
        /// each line with the space after its `///`, which Markdown drops;
        /// `None` when its row has none.
        fn fixed_doc(id: TypeId) -> Option<&'static str> {
            match id {
                $($(TypeId::$name => Some(concat!($doc $(, "\n", $doc_more)*)),)?)*
                _ => None,
            }
        }

        $(
            #[doc = $bracketed_doc] $(#[doc = $bracketed_doc_more])*
            pub const $bracketed: &str = $bracketed_text;
        )*

        /// The doc comment on the row of the bracketed name `name`, as
        /// `fixed_doc` gives a fixed type's; `None` for another name.
        fn bracketed_doc(name: &str) -> Option<&'static str> {
            match name {
                $($bracketed_text => Some(concat!($bracketed_doc $(, "\n", $bracketed_doc_more)*)),)*
                _ => None,
            }
        }
    };
}

// A named type that is not an integer has a doc; an integer's is worked out
// (`builtin_doc`).
builtin_types! {
    INVALID => Type::Invalid,
    VOID => Type::Void,
    UNDECIDED => Type::Undecided,
    /// A truth value: `true` or `false`.
    BOOL "bool" => Type::Bool,
    I8 "i8" => integers::integer(true, 8),
    I16 "i16" => integers::integer(true, 16),
    I32 "i32" => integers::integer(true, 32),
    I64 "i64" => integers::integer(true, 64),
    I128 "i128" => integers::integer(true, 128),
    U8 "u8" => integers::integer(false, 8),
    U16 "u16" => integers::integer(false, 16),
    U32 "u32" => integers::integer(false, 32),
    U64 "u64" => integers::integer(false, 64),
    U128 "u128" => integers::integer(false, 128),
    /// A 32-bit floating-point number (IEEE 754 single precision).
    F32 "f32" => Type::Float(Float::F32),
    /// A 64-bit floating-point number (IEEE 754 double precision).
    F64 "f64" => Type::Float(Float::F64),
    /// C text: bytes ending in a zero byte, with none before it. It has any
    /// length, so no fixed size, and is used only through a reference,
    /// `&cstr`: the bytes' address, as C functions take it.
    CSTR "cstr" => Type::CString,
    /// Text: bytes of any length, such as a string literal's. It has no
    /// fixed size, so it is used only through a reference, `&str`: the
    /// bytes' address and how many there are. A `&str` copies; `.len` is
    /// its length in bytes.
    STR "str" => Type::Str,
    // No name: `rawptr` is a keyword, not a type name.
    RAWPTR => Type::RawPointer { target: None },
    ;
    /// The integers from a start toward an end, a step apart: the type of
    /// `a..b`, `a..=b` and `a..b:step`.
    ///
    /// `range[T]` holds integers of the type `T`. It counts up or down, as its
    /// ends say, includes its end when written with `..=`, and copies.
    /// `for v in r` counts each of its values, and `[N]T{r...}` spreads them
    /// into an array.
    ///
    /// ```peeper
    /// fn evens(n: i32) -> range[i32] {
    ///     return 0..n:2;
    /// }
    /// ```
    RANGE "range",
}

/// The built-in type names a program writes most, for "did you mean"
/// guesses: each in `NAMED`, `isize` and `usize`.
pub fn builtin_names() -> impl Iterator<Item = &'static str> {
    let named = NAMED.iter().map(|&(text, _)| text);
    named.chain(integers::POINTER_SIZED.iter().map(|&(text, _)| text))
}

/// What the built-in type `name` is (`Types::builtin`), or `range`, as
/// Markdown, as an editor shows it: an integer's range, worked out, and
/// whether it is as wide as a pointer, `pointer_bits`; another type's doc,
/// from its row (`fixed_doc`). `None` when `name` is not a built-in type.
pub fn builtin_doc(name: &str, pointer_bits: u32) -> Option<String> {
    if let Some(doc) = bracketed_doc(name) {
        return Some(doc.to_string());
    }
    let Integer { signed, bits } = match NAMED.iter().find(|&&(text, _)| text == name) {
        Some(&(_, id)) => match FIXED[id.0 as usize] {
            Type::Integer(integer) => integer,
            _ => return fixed_doc(id).map(str::to_string),
        },
        None => integers::integer_named(name, pointer_bits)?,
    };
    let (min, max) = match (signed, bits) {
        (true, ..=128) => (
            (i128::MIN >> (128 - bits)).to_string(),
            (i128::MAX >> (128 - bits)).to_string(),
        ),
        (false, ..=128) => ("0".to_string(), (u128::MAX >> (128 - bits)).to_string()),
        // Too many digits to read.
        (true, _) => (format!("-2^{}", bits - 1), format!("2^{} - 1", bits - 1)),
        (false, _) => ("0".to_string(), format!("2^{bits} - 1")),
    };
    let kind = if signed { "A signed" } else { "An unsigned" };
    let size = if integers::POINTER_SIZED
        .iter()
        .any(|&(text, _)| text == name)
    {
        format!("integer as wide as a pointer, {bits} bits on the target")
    } else {
        format!("{bits}-bit integer")
    };
    Some(format!("{kind} {size}: from `{min}` to `{max}`."))
}

impl Types {
    /// The built-in type `name` stands for: one in `NAMED`, `isize` and `usize`
    /// (as wide as a pointer: the `usize` this table was made with), or `iN` and `uN` for N from 1 to
    /// `machine::MAX_INTEGER_BITS`, written without leading zeros. A number literal's
    /// suffix (`5u24`) is one of these names too.
    pub fn builtin(&mut self, name: &str) -> Option<TypeId> {
        if let Some(&(_, id)) = NAMED.iter().find(|(text, _)| *text == name) {
            return Some(id);
        }
        let Type::Integer(pointer) = self[self.usize] else {
            unreachable!("`usize` is an integer")
        };
        Some(self.intern(Type::Integer(integers::integer_named(name, pointer.bits)?)))
    }
}
