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

    /// How tightly the operator binds.
    pub const fn precedence(self) -> Precedence {
        match self {
            BinaryOp::Add | BinaryOp::Subtract => Precedence::Sum,
            BinaryOp::Multiply | BinaryOp::Divide | BinaryOp::Remainder => Precedence::Product,
            BinaryOp::ShiftLeft | BinaryOp::ShiftRight => Precedence::Shift,
            BinaryOp::BitAnd => Precedence::BitAnd,
            BinaryOp::BitOr => Precedence::BitOr,
            BinaryOp::BitXor => Precedence::BitXor,
            BinaryOp::EqualTo | BinaryOp::NotEqualTo => Precedence::Equality,
            BinaryOp::LessThan
            | BinaryOp::LessOrEqual
            | BinaryOp::GreaterThan
            | BinaryOp::GreaterOrEqual => Precedence::Compare,
            BinaryOp::LogicalAnd => Precedence::LogicalAnd,
            BinaryOp::LogicalOr => Precedence::LogicalOr,
        }
    }
}

/// How tightly an operator binds, from loosest to tightest.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Precedence {
    Lowest,
    OrElse,
    Pipe,
    LogicalOr,
    LogicalAnd,
    BitOr,
    BitXor,
    BitAnd,
    Equality,
    Compare,
    Shift,
    Sum,
    Product,
    Cast,
    Prefix,
    Call,
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
