//! The built-in functions the checker works out itself, and the built-in
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

/// What a built-in function that takes a type gives of its layout
/// (`model::layout::layout`) on the target being built.
#[derive(Clone, Copy)]
pub(crate) enum LayoutFact {
    Size,
    Align,
}

/// The built-in function that takes a type named `name`, if it is one. Each
/// gives a `usize` the checker works out, so it is a constant.
pub(crate) fn layout_function(name: &str) -> Option<LayoutFact> {
    match name {
        // How many bytes a value of the type takes, its padding included.
        "size_of" => Some(LayoutFact::Size),
        // The multiple of bytes a value of the type starts at in memory.
        "align_of" => Some(LayoutFact::Align),
        _ => None,
    }
}
