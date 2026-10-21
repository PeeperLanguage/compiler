//! The program's data, in one place: the syntax tree, THIR, MIR, the type
//! model, definition ids, constant values with their arithmetic, and operator
//! kinds. Each phase keeps its own node types (`ast`, `thir`, `mir`); the
//! crates that build them hold only logic. See docs/ir-design.md.

pub mod ast;
pub mod constant;
pub mod mir;
pub mod semantic_types;
pub mod thir;

use arena::Id;

/// A module of the program, by its place in the program's order. Tables of
/// per-module results are vectors indexed by it.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ModuleId(u32);

impl ModuleId {
    /// The module's place in the program's order.
    pub fn index(self) -> usize {
        self.0 as usize
    }

    /// The module at place `index` in the program's order.
    pub fn from_index(index: usize) -> ModuleId {
        ModuleId(u32::try_from(index).expect("a program has fewer than 2^32 modules"))
    }
}

/// A declaration the whole program can name: its module and its place among
/// that module's top-level declarations (`Tree::declaration`). Tables of
/// per-declaration results are vectors indexed by it. A body edit leaves it
/// as it was. A name declared inside a body is not one: it is a `Local`, an
/// `Id` into that body's own arena.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct DefId {
    pub module: ModuleId,
    pub index: u32,
}

/// What a name stands for: first where it is declared, then its kind. A
/// module's scope holds all but `Local`; a body adds its own.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Symbol {
    /// An imported module.
    Module(ModuleId),
    /// A function or method.
    Function(DefId),
    /// A `const` declared at module scope. One declared in a body is
    /// `Local::Constant`.
    Constant(DefId),
    /// a struct, enum, interface or type alias
    Type(DefId),
    /// A name declared in a body.
    Local(Local),
}

/// A name declared in a body, by its kind: checked in place with the
/// body, never asked for as a query.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Local {
    /// A parameter or `let`: it holds a value at run time.
    Variable(Id<thir::Variable>),
    /// A `const`, folded where it is used.
    Constant(Id<Option<constant::Constant>>),
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
