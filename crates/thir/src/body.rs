//! THIR: a function body after checking. Every expression has its type, every
//! name is resolved to the local or constant it means, implicit conversions
//! are explicit nodes, and constant expressions are folded to their values.
//! Its nodes live in typed arenas, as the syntax tree's do.

use arena::Arena;
use arena::Id;
use arena::List;
use arena::Lists;
use arena::Node;
use arena::nodes;
use ir::BinaryOp;
use ir::UnaryOp;
use ir::types::TypeId;
use ir::value::Value;
use source::Span;

nodes! {
    Body;
    Expr => exprs,
    Stmt => stmts,
    Block => blocks,
    Local => locals,
}

/// One function's checked body. The function's parameters are its first
/// locals, in order.
pub struct Body {
    pub statements: List<Stmt>, // the function's block
    pub exprs: Arena<Expr>,
    pub stmts: Arena<Stmt>,
    pub blocks: Arena<Block>,
    pub locals: Arena<Local>, // with the span of each name
    pub lists: Lists,
}

/// A parameter or a `let` binding.
#[derive(Clone, Copy, Debug)]
pub struct Local {
    pub ty: TypeId,
    pub mutable: bool,
}

#[derive(Clone, Debug)]
pub struct Expr {
    pub ty: TypeId,
    pub kind: ExprKind,
}

impl Expr {
    /// An expression whose error was already reported.
    pub const INVALID: Expr = Expr {
        ty: TypeId::INVALID,
        kind: ExprKind::Invalid,
    };
}

#[derive(Clone, Debug)]
pub enum ExprKind {
    /// A value known at compile time: a literal, a constant, or an operation
    /// on them, folded.
    Constant(Value),
    Local(Id<Local>),
    Binary {
        op: BinaryOp,
        left: Id<Expr>,
        right: Id<Expr>,
    },
    Unary {
        op: UnaryOp,
        value: Id<Expr>,
    },
    /// The value converted to the wider type `ty` without `as`
    /// (`Type::widens_to`).
    Widen(Id<Expr>),
    Invalid,
}

#[derive(Clone, Copy, Debug)]
pub enum Stmt {
    /// `let`; without a value, the local is set later
    Let {
        local: Id<Local>,
        value: Option<Id<Expr>>,
    },
    Assign {
        local: Id<Local>,
        value: Id<Expr>,
    },
    Expression(Id<Expr>),
    Return(Option<Id<Expr>>),
    /// the else is a `Block` or an `If`
    If {
        condition: Id<Expr>,
        then: Id<Block>,
        otherwise: Option<Id<Stmt>>,
    },
    Block(Id<Block>),
}

#[derive(Clone, Copy, Debug)]
pub struct Block {
    pub statements: List<Stmt>,
}

impl Body {
    pub(crate) fn new() -> Body {
        Body {
            statements: List::EMPTY,
            exprs: Arena::default(),
            stmts: Arena::default(),
            blocks: Arena::default(),
            locals: Arena::default(),
            lists: Lists::default(),
        }
    }

    /// Adds `node` with `span` to its arena.
    pub fn add<T: Node<Container = Body>>(&mut self, node: T, span: Span) -> Id<T> {
        T::arena(self).push(node, span)
    }
}
