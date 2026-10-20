//! The program's data, in one place: the syntax tree, THIR, MIR, the type
//! model, definition ids, constant values with their arithmetic, and operator
//! kinds. Each phase keeps its own node types (`ast`, `thir`, `mir`); the
//! crates that build them hold only logic. See docs/ir-design.md.

pub mod ast;
pub mod constant;
pub mod mir;
pub mod semantic_types;
pub mod thir;

use constant::Constant;
use semantic_types::TypeId;

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

/// A declaration of the program: its module and the declaration's index in
/// that module's syntax tree. Tables of per-declaration results are vectors
/// indexed by it.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct DefId {
    pub module: ModuleId,
    pub index: u32,
}

/// What a module-level name stands for, by the kind of its declaration.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Binding {
    Function(DefId),
    Constant(DefId),
    /// a struct, enum, interface or type alias
    Type(DefId),
    /// An imported module.
    Module(ModuleId),
}

/// What the query of a declaration found, by the declaration's kind.
#[derive(Clone, Debug)]
pub enum Declared {
    /// What `Checker::signature` gives: a function's type, else `INVALID`.
    Signature(TypeId),
    /// A constant's type and value; `None` when it had an error.
    Constant(Option<Constant>),
}

impl Declared {
    /// The signature of a function declaration.
    pub fn signature(self) -> TypeId {
        match self {
            Declared::Signature(ty) => ty,
            Declared::Constant(_) => unreachable!("a constant has a value, not a signature"),
        }
    }

    /// The value of a constant declaration; `None` when it had an error.
    pub fn constant(self) -> Option<Constant> {
        match self {
            Declared::Constant(value) => value,
            Declared::Signature(_) => unreachable!("a function has a signature, not a value"),
        }
    }
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
