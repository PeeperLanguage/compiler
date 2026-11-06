//! The built-in types the checker does not handle yet. The ones it does
//! handle are named in `model::semantic_types` (`Types::builtin`).

/// What to call the built-in type `name` in a "not supported yet" report, if
/// it is one the checker does not handle yet.
pub(crate) fn unsupported_type(name: &str) -> Option<&'static str> {
    match name {
        "string" => Some("string types"),
        "byte" | "char" => Some("byte and char types"),
        "Allocator" => Some("allocator types"),
        _ => None,
    }
}
