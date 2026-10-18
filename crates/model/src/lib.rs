//! The program's data, in one place: the syntax tree, THIR, MIR, the type
//! model, definition ids, constant values with their arithmetic, and operator
//! kinds. Each phase keeps its own node types (`ast`, `thir`, `mir`); the
//! crates that build them hold only logic. See docs/ir-design.md.

pub mod ast;
pub mod constant;
pub mod mir;
pub mod semantic_types;
pub mod thir;

/// A declaration of the program: the module's place in the program's order and
/// the declaration's index in its syntax tree. Tables of per-declaration results
/// are vectors indexed by it.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct DefId {
    pub module: u32,
    pub index: u32,
}

/// What a module-level name stands for, by the kind of its declaration.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Binding {
    Function(DefId),
    Constant(DefId),
    /// a struct, enum, interface or type alias
    Type(DefId),
    /// An imported module, by its place in the program's order.
    Module(u32),
}

/// A binary operator, named after what it does; the comment shows its token.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BinaryOp {
    Add,            // +
    Subtract,       // -
    Multiply,       // *
    Divide,         // /
    Remainder,      // %
    ShiftLeft,      // <<
    ShiftRight,     // >>
    BitAnd,         // &
    BitOr,          // |
    BitXor,         // ^
    EqualTo,        // ==
    NotEqualTo,     // !=
    LessThan,       // <
    LessOrEqual,    // <=
    GreaterThan,    // >
    GreaterOrEqual, // >=
    LogicalAnd,     // &&
    LogicalOr,      // ||
}

/// A unary operator. `+x` changes nothing, so check keeps no node for it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum UnaryOp {
    Negate,     // -
    LogicalNot, // !
    BitNot,     // ~
}
