//! The program's data after parsing: THIR, MIR, the type model, constant
//! values, and the ids every phase shares (`ModuleId`, `DefId`). The syntax
//! tree is the `syntax` crate's. Each phase keeps its own node types
//! (`thir`, `mir`); the crates that build them hold only logic. See
//! docs/ir-design.md.

pub mod constant;
pub mod layout;
pub mod machine;
pub mod mir;
pub mod program;
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

/// What a function's `const` parameters are given, in order, by their place
/// in `Types`'s list of them (`Types::const_arguments`).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ConstArguments(pub(crate) u32);

impl ConstArguments {
    /// None: the arguments of a function without `const` parameters.
    pub const NONE: ConstArguments = ConstArguments(0);
}

/// A function as a call names it: its declaration, and what its `const`
/// parameters are given, which makes this copy of it
/// (`ConstArguments::NONE` for a function without them).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct FunctionCopy {
    pub definition: DefId,
    pub arguments: ConstArguments,
}

/// What a call runs: a declared function, or the method at this position
/// in the table of the interface reference that is the call's first
/// argument, which the call gives as its `self` (THIR's and MIR's `Call`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Callee {
    Function(FunctionCopy),
    Method(u32),
}
