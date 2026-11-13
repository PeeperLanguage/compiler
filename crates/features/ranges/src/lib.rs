//! Ranges: `a..b`, `a..=b`, `a..b:step`, and `..b` or `a..` before a `]`.
//! The written parts are the core's `WrittenRange`, as slicing reads them
//! (`Language::written_range`); this crate reads them and checks a range
//! value into the core's `range[T]`, so the phases after it know nothing of
//! this crate.

pub mod check;
pub mod parse;

#[cfg(test)]
mod tests;
