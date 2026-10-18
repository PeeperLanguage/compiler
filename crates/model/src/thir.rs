//! THIR: a function body after checking. Every expression has its type, every
//! name is resolved to the local or constant it means, implicit conversions
//! are explicit nodes, and constant expressions are folded to their values.
//! Its nodes live in typed arenas, as the syntax tree's do.

use std::fmt::Write;

use crate::BinaryOp;
use crate::Binding;
use crate::DefId;
use crate::UnaryOp;
use crate::constant::Constant;
use crate::constant::Value;
use crate::semantic_types::Type;
use crate::semantic_types::TypeId;
use crate::semantic_types::Types;
use arena::Arena;
use arena::Id;
use arena::List;
use arena::Lists;
use arena::Node;
use arena::nodes;
use source::SourceFile;
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
    pub span: Span,             // the function's block, braces included
    pub statements: List<Stmt>, // the function's block
    pub exprs: Arena<Expr>,
    pub stmts: Arena<Stmt>,
    pub blocks: Arena<Block>,
    pub locals: Arena<Local>, // with the span of each name
    /// Each `const` of the body, by its type and value, with the span of its
    /// name; `None` when its value had an error. Uses of it are folded.
    pub consts: Arena<Option<Constant>>,
    pub lists: Lists,
    /// Checking it reported an error: later phases would only find what
    /// follows from that, so it is not lowered.
    pub has_errors: bool,
}

/// A name written in a body, where it is declared or used, and what it
/// stands for. Recorded only for a caller that asks (`Checker::names`), such
/// as an editor; the body itself does not keep names.
#[derive(Clone, Copy, Debug)]
pub struct Name {
    pub span: Span,
    pub target: Target,
}

/// What a name written in a body stands for.
#[derive(Clone, Copy, Debug)]
pub enum Target {
    Body(Symbol),
    /// A name of the module or the prelude.
    Global(Binding),
}

/// A name declared in a body: a parameter or `let` (a local, which holds a
/// value at run time), or a `const` (folded where it is used).
#[derive(Clone, Copy, Debug)]
pub enum Symbol {
    Local(Id<Local>),
    Constant(Id<Option<Constant>>),
}

/// A parameter or a `let` binding.
#[derive(Clone, Copy, Debug)]
pub struct Local {
    pub ty: TypeId,
    pub mutable: bool,
    /// Set where declared; false for `let x: T;`, whose first assignment
    /// sets it, even without `mut`.
    pub initialized: bool,
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
    /// A call of a module's function; calls are never folded.
    Call {
        function: DefId,
        arguments: List<Expr>,
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

impl Default for Body {
    fn default() -> Body {
        Body {
            span: Span::default(),
            statements: List::EMPTY,
            exprs: Arena::default(),
            stmts: Arena::default(),
            blocks: Arena::default(),
            locals: Arena::default(),
            consts: Arena::default(),
            lists: Lists::default(),
            has_errors: false,
        }
    }
}

impl Body {
    /// Adds `node` with `span` to its arena.
    pub fn add<T: Node<Container = Body>>(&mut self, node: T, span: Span) -> Id<T> {
        T::arena(self).push(node, span)
    }

    /// Where the body declares `symbol`: its name.
    pub fn declared(&self, symbol: Symbol) -> Span {
        match symbol {
            Symbol::Local(local) => self.locals.span(local),
            Symbol::Constant(constant) => self.consts.span(constant),
        }
    }
}

impl Body {
    /// The body as text, one statement per line, for `peeper -dump`. `file` is
    /// where the body is written; `function_name` names a called function. A
    /// local shows as its name and index (`x.2`), a constant other than a
    /// `bool` with its type (`1:i32`), and an operation in parentheses.
    pub fn text(
        &self,
        types: &Types,
        file: &SourceFile,
        function_name: impl Fn(DefId) -> String,
    ) -> String {
        let mut printer = Printer {
            body: self,
            types,
            file,
            function_name,
            out: String::new(),
        };
        printer.statements(self.statements, 1);
        printer.out
    }
}

/// What `Body::text` writes with.
struct Printer<'b, F> {
    body: &'b Body,
    types: &'b Types,
    file: &'b SourceFile,
    function_name: F,
    out: String,
}

impl<F: Fn(DefId) -> String> Printer<'_, F> {
    /// Each of `statements` on its own line, `depth` levels in.
    fn statements(&mut self, statements: List<Stmt>, depth: usize) {
        for statement in self.body.lists.items(statements) {
            for _ in 0..depth {
                self.out.push_str("    ");
            }
            self.statement(statement, depth);
            self.out.push('\n');
        }
    }

    /// `statement` without its indent and line end; a block's lines are
    /// `depth + 1` levels in.
    fn statement(&mut self, statement: Id<Stmt>, depth: usize) {
        match self.body.stmts[statement] {
            Stmt::Let { local, value } => {
                let mutable = self.body.locals[local].mutable;
                self.out.push_str(if mutable { "let mut " } else { "let " });
                self.local(local);
                if let Some(value) = value {
                    self.out.push_str(" = ");
                    self.expr(value);
                }
            }
            Stmt::Assign { local, value } => {
                self.local(local);
                self.out.push_str(" = ");
                self.expr(value);
            }
            Stmt::Expression(value) => self.expr(value),
            Stmt::Return(value) => {
                self.out.push_str("return");
                if let Some(value) = value {
                    self.out.push(' ');
                    self.expr(value);
                }
            }
            Stmt::If {
                condition,
                then,
                otherwise,
            } => {
                self.out.push_str("if ");
                self.expr(condition);
                self.out.push(' ');
                self.block(self.body.blocks[then].statements, depth);
                if let Some(otherwise) = otherwise {
                    self.out.push_str(" else ");
                    self.statement(otherwise, depth);
                }
            }
            Stmt::Block(block) => self.block(self.body.blocks[block].statements, depth),
        }
    }

    /// `{`, the statements one level in, and `}` at `depth`.
    fn block(&mut self, statements: List<Stmt>, depth: usize) {
        self.out.push_str("{\n");
        self.statements(statements, depth + 1);
        for _ in 0..depth {
            self.out.push_str("    ");
        }
        self.out.push('}');
    }

    fn local(&mut self, local: Id<Local>) {
        let name = self.file.slice(self.body.locals.span(local));
        _ = write!(self.out, "{name}.{}", local.index());
    }

    fn expr(&mut self, expr: Id<Expr>) {
        let Expr { ty, ref kind } = self.body.exprs[expr];
        match *kind {
            ExprKind::Constant(Value::Bool(value)) => _ = write!(self.out, "{value}"),
            ExprKind::Constant(Value::Float(bits)) => {
                _ = write!(self.out, "{}:{}", f64::from_bits(bits), self.types.text(ty));
            }
            ExprKind::Constant(Value::Integer(ref limbs)) => {
                let Type::Integer(integer) = self.types[ty] else {
                    unreachable!("an integer constant has an integer type")
                };
                // Shown by its low 128 bits, read through its type.
                let value = match integer.bits {
                    ..=128 => bigint::extend(integer.bits, integer.signed, limbs, 128),
                    _ => limbs.clone(),
                };
                let low = value.as_slice()[0] as u128 | (value.as_slice()[1] as u128) << 64;
                _ = if integer.signed {
                    write!(self.out, "{}", low as i128)
                } else {
                    write!(self.out, "{low}")
                };
                _ = write!(self.out, ":{}", self.types.text(ty));
            }
            ExprKind::Local(local) => self.local(local),
            ExprKind::Binary { op, left, right } => {
                self.out.push('(');
                self.expr(left);
                _ = write!(self.out, " {op:?} ");
                self.expr(right);
                self.out.push(')');
            }
            ExprKind::Unary { op, value } => {
                _ = write!(self.out, "({op:?} ");
                self.expr(value);
                self.out.push(')');
            }
            ExprKind::Widen(value) => {
                self.out.push_str("widen(");
                self.expr(value);
                _ = write!(self.out, "):{}", self.types.text(ty));
            }
            ExprKind::Call {
                function,
                arguments,
            } => {
                _ = write!(self.out, "call {}(", (self.function_name)(function));
                for (index, argument) in self.body.lists.items(arguments).enumerate() {
                    if index > 0 {
                        self.out.push_str(", ");
                    }
                    self.expr(argument);
                }
                _ = write!(self.out, "):{}", self.types.text(ty));
            }
            ExprKind::Invalid => self.out.push_str("invalid"),
        }
    }
}
