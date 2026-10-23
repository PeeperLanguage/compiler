//! Operator kinds, shared by the syntax tree, THIR, MIR and constant folding.

/// A binary operator, named after what it does.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BinaryOp {
    Add,
    Subtract,
    Multiply,
    Divide,
    Remainder,
    ShiftLeft,
    ShiftRight,
    BitAnd,
    BitOr,
    BitXor,
    EqualTo,
    NotEqualTo,
    LessThan,
    LessOrEqual,
    GreaterThan,
    GreaterOrEqual,
    LogicalAnd,
    LogicalOr,
}

impl BinaryOp {
    /// How the operator is written, for messages and dumps.
    pub const fn symbol(self) -> &'static str {
        match self {
            BinaryOp::Add => "+",
            BinaryOp::Subtract => "-",
            BinaryOp::Multiply => "*",
            BinaryOp::Divide => "/",
            BinaryOp::Remainder => "%",
            BinaryOp::ShiftLeft => "<<",
            BinaryOp::ShiftRight => ">>",
            BinaryOp::BitAnd => "&",
            BinaryOp::BitOr => "|",
            BinaryOp::BitXor => "^",
            BinaryOp::EqualTo => "==",
            BinaryOp::NotEqualTo => "!=",
            BinaryOp::LessThan => "<",
            BinaryOp::LessOrEqual => "<=",
            BinaryOp::GreaterThan => ">",
            BinaryOp::GreaterOrEqual => ">=",
            BinaryOp::LogicalAnd => "&&",
            BinaryOp::LogicalOr => "||",
        }
    }
}

/// A unary operator. There is no `+x`: it would change nothing, so the
/// parser reports it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum UnaryOp {
    Negate,
    LogicalNot,
    BitNot,
}

impl UnaryOp {
    /// How the operator is written, for messages and dumps.
    pub const fn symbol(self) -> &'static str {
        match self {
            UnaryOp::Negate => "-",
            UnaryOp::LogicalNot => "!",
            UnaryOp::BitNot => "~",
        }
    }
}
