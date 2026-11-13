//! Bools: `true` and `false`. The checker gives the core's bool constant,
//! so the phases after it know nothing of this crate. The `bool` type and
//! its operations (`!`, `&&`, `==`) are the core's: they read the checked
//! type.

pub mod check;
pub mod parse;

/// `true` or `false`
#[derive(Clone, Copy, Debug)]
pub struct BoolLiteral {
    pub value: bool,
}

#[cfg(test)]
mod tests;
