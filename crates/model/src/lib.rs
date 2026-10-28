//! The program's data, in one place: the syntax tree, THIR, MIR, the type
//! model, constant values with their arithmetic, operator kinds, and the ids
//! every phase shares (`ModuleId`, `DefId`). Each phase keeps its own node
//! types (`ast`, `thir`, `mir`); the crates that build them hold only logic.
//! See docs/ir-design.md.

pub mod ast;
pub mod constant;
pub mod machine;
pub mod mir;
pub mod operators;
pub mod semantic_types;
pub mod thir;

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
