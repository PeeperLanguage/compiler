//! The built-ins the checker does not handle yet. The built-in types it does
//! handle are named in `model::semantic_types` (`Types::builtin`).

/// What to call the built-in type `name` in a "not supported yet" report, if
/// it is one the checker does not handle yet.
pub(crate) fn unsupported_type(name: &str) -> Option<&'static str> {
    match name {
        "str" | "string" => Some("string types"),
        "byte" | "char" => Some("byte and char types"),
        "Allocator" => Some("allocator types"),
        _ => None,
    }
}

/// The built-in functions, which the checker does not handle yet.
pub(crate) const UNSUPPORTED_FUNCTIONS: &[&str] = &[
    "alloc",
    "append",
    "reserve",
    "resize",
    "shrink",
    "len",
    "as_bytes",
    "as_chars",
    "from_bytes",
];
