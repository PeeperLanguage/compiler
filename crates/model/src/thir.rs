//! THIR: a function body after checking. Every expression has its type, every
//! name is resolved to the variable or constant it means, implicit conversions
//! are explicit nodes, and constant expressions are folded to their values.
//! Its nodes live in typed arenas, as the syntax tree's do.

use std::fmt::Write;

use crate::DefId;
use crate::ModuleId;
use crate::constant::Constant;
use crate::constant::Value;
use crate::constant::value_text;
use crate::operators::BinaryOp;
use crate::operators::UnaryOp;
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
    Variable => variables,
    FieldValue => children.field_values,
}

/// One function's checked body. The function's parameters are its first
/// variables, in order.
#[derive(Default)]
pub struct Body {
    pub exprs: Arena<Expr>,
    pub stmts: Arena<Stmt>,
    /// Each block, with its span, braces included: the scope of the names it
    /// declares. The first is the function's, which also holds the parameters;
    /// a block comes before the blocks inside it.
    pub blocks: Arena<Block>,
    pub variables: Arena<Variable>, // with the span of each name
    /// Each `const` of the body, by its type and value, with the span of its
    /// name; `None` when its value had an error. Uses of it are folded.
    pub constants: Arena<Option<Constant>>,
    pub children: Children,
    /// Checking it reported an error: later phases would only find what
    /// follows from that, so it is not lowered.
    pub has_errors: bool,
}

/// How a node holds several others: storage reached only through the node
/// that holds it, never by an id from elsewhere.
#[derive(Default)]
pub struct Children {
    pub field_values: Arena<FieldValue>, // with the span of each field's name
    pub lists: Lists,
}

/// What a name stands for: first where it is declared, then its kind. A
/// module's scope holds the declared ones but `Local`; a body adds its own.
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
    /// A built-in type, such as `i32`, which no declaration hides.
    Builtin(TypeId),
}

/// A name declared in a body, by its kind: checked in place with the
/// body, never asked for on its own.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Local {
    /// A parameter or `let`: it holds a value at run time.
    Variable(Id<Variable>),
    /// A `const`, folded where it is used.
    Constant(Id<Option<Constant>>),
}

/// What the checker found about the text at `span` of a module. Recorded
/// only for a caller that asks (`ModuleChecks::facts`), such as an editor;
/// neither the body nor the module keeps facts.
#[derive(Clone, Copy, Debug)]
pub struct Fact {
    pub span: Span,
    pub kind: FactKind,
}

#[derive(Clone, Copy, Debug)]
pub enum FactKind {
    /// A name, where it is declared or used: what it stands for.
    Name(Symbol),
    /// A number or string literal: the type it got.
    Literal(TypeId),
}

/// A parameter or a `let` binding, or one the checker makes.
#[derive(Clone, Copy, Debug)]
pub struct Variable {
    pub ty: TypeId,
    pub mutable: bool,
    /// Set where declared; false for `let x: T;`, whose first assignment
    /// sets it, even without `mut`.
    pub initialized: bool,
    /// The block it is declared in, whose end ends its name's scope; the
    /// function's block for a parameter.
    pub scope: Id<Block>,
    /// Made by the checker, with no name: its span is the code it was made
    /// for (a `for` loop's array or counter), not a name.
    pub hidden: bool,
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
    Variable(Id<Variable>),
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
    /// The number or `bool` converted to the type `ty`, without `as` or with
    /// it (`Types::conversion`).
    Convert(Id<Expr>),
    /// A value of the struct type `ty`, each field set once, in the order
    /// written, which is the order the values are computed in.
    StructLiteral(List<FieldValue>),
    /// A value of the array type `ty` from its parts, in order, which give
    /// exactly its length of elements: a part of the element type is one
    /// element, and any other a spread, an array of elements or a range of
    /// them, giving each element or value, as `Stmt::ForRange` counts a
    /// range's.
    ArrayLiteral(List<Expr>),
    /// The field at `index`, in declaration order, of a struct value.
    Field {
        value: Id<Expr>,
        index: u32,
    },
    /// `array.len`, a `usize`. For a slice, its length, read when the
    /// program runs. For an array, or a reference or raw pointer to one, only
    /// where computing `array` calls a function: the array is computed, never
    /// read, and the value is its type's length. Without a call an array's
    /// `.len` is a `Constant` and the array is not computed, as Go's `len`
    /// (`Body::calls`).
    Length(Id<Expr>),
    /// The element of the array, a place, or of the slice at the index, an
    /// integer; outside the array or slice, the program fails. A slice's
    /// elements are behind it, as a reference's target: lowering adds that
    /// step, which THIR does not write.
    Index {
        array: Id<Expr>,
        index: Id<Expr>,
    },
    /// What the reference, a place, refers to: `r.*`, and the implicit step
    /// of `r.x` and `r[i]`.
    Dereference(Id<Expr>),
    /// `&place` or `&mut place`, as its reference or slice type says, and a
    /// `&mut` or `[&mut]` variable passed to a call (lent again, not moved);
    /// the place is a `Variable`, a `Dereference`, or a `Field` or `Index` of
    /// a place. As a slice, the place is an array, whose elements it views,
    /// or a slice, whose elements it lends again.
    Borrow(Id<Expr>),
    /// `@place`: the place's address as a raw pointer, which no loan
    /// follows; the place is as a `Borrow`'s.
    Address(Id<Expr>),
    /// `start..end`, or `..=` when `inclusive`, a value of the type
    /// `range[T]` (`ty`): integers from `start` toward `end`, `step` apart,
    /// up or down as `start <= end`. The ends are `T`s, the step is of the
    /// range type's step type (1 when not written); a step of zero stops
    /// the program when the range is made, so a range value's step is
    /// never zero.
    Range {
        start: Id<Expr>,
        end: Id<Expr>,
        step: Id<Expr>,
        inclusive: bool,
    },
    /// `print(values)`, or `println(values)` when `newline`: each value a
    /// number, `bool`, `cstr`, raw pointer, a struct or array of such values,
    /// or a reference to one; a value that moves, in a place, is lent as `&`.
    Print {
        newline: bool,
        values: List<Expr>,
    },
    Invalid,
}

/// `name = value` in a struct literal: the field by its index in declaration
/// order.
#[derive(Clone, Copy, Debug)]
pub struct FieldValue {
    pub index: u32,
    pub value: Id<Expr>,
}

#[derive(Clone, Copy, Debug)]
pub enum Stmt {
    /// `let`; without a value, the variable is set later
    Let {
        variable: Id<Variable>,
        value: Option<Id<Expr>>,
    },
    /// `target = value`; the target is a place: a `Variable`, a
    /// `Dereference`, or a `Field` or `Index` of a place.
    Assign {
        target: Id<Expr>,
        value: Id<Expr>,
    },
    Expression(Id<Expr>),
    Return(Option<Id<Expr>>),
    /// `panic message`: the message is a constant `Value::Bytes`
    Panic(Id<Expr>),
    /// the else is a `Block` or an `If`
    If {
        condition: Id<Expr>,
        then: Id<Block>,
        otherwise: Option<Id<Stmt>>,
    },
    Block(Id<Block>),
    /// `for { }`, or `for condition { }`, which ends when the condition is
    /// false before a pass.
    Loop {
        condition: Option<Id<Expr>>,
        body: Id<Block>,
    },
    /// `for index, value in range { }`: `value`, declared in `body`, is
    /// each integer of the range value `range` (`ExprKind::Range`), with
    /// `end` when the range includes it; `index` counts the passes from 0.
    /// The range is computed once, before the first pass. `for start..end
    /// { }` has no `value`.
    ForRange {
        index: Option<Id<Variable>>,
        value: Option<Id<Variable>>,
        range: Id<Expr>,
        body: Id<Block>,
    },
    /// Leaves the innermost loop.
    Break,
    /// Starts the innermost loop's next pass.
    Continue,
}

#[derive(Clone, Copy, Debug)]
pub struct Block {
    pub parent: Option<Id<Block>>, // the block it is in; `None` for the function's
    pub statements: List<Stmt>,
}

impl Body {
    /// Empties it for another function's body, keeping the memory.
    pub fn clear(&mut self) {
        // Named in full, so a new field cannot be missed.
        let Body {
            exprs,
            stmts,
            blocks,
            variables,
            constants,
            children: Children {
                field_values,
                lists,
            },
            has_errors,
        } = self;
        exprs.clear();
        stmts.clear();
        blocks.clear();
        variables.clear();
        field_values.clear();
        constants.clear();
        lists.clear();
        *has_errors = false;
    }

    /// Adds `node` with `span` to its arena.
    pub fn add<T: Node<Container = Body>>(&mut self, node: T, span: Span) -> Id<T> {
        T::arena(self).push(node, span)
    }

    /// Whether computing `expression` calls a function, `print` too: then
    /// it runs even where only its type is needed (`ExprKind::Length`).
    pub fn calls(&self, expression: Id<Expr>) -> bool {
        match self.exprs[expression].kind {
            ExprKind::Call { .. } | ExprKind::Print { .. } => true,
            ExprKind::Constant(_) | ExprKind::Variable(_) | ExprKind::Invalid => false,
            ExprKind::Binary { left, right, .. }
            | ExprKind::Index {
                array: left,
                index: right,
            } => self.calls(left) || self.calls(right),
            ExprKind::Unary { value, .. }
            | ExprKind::Convert(value)
            | ExprKind::Field { value, .. }
            | ExprKind::Length(value)
            | ExprKind::Dereference(value)
            | ExprKind::Borrow(value)
            | ExprKind::Address(value) => self.calls(value),
            ExprKind::Range {
                start, end, step, ..
            } => self.calls(start) || self.calls(end) || self.calls(step),
            ExprKind::StructLiteral(fields) => (self.children.lists.items(fields))
                .any(|field| self.calls(self.children.field_values[field].value)),
            ExprKind::ArrayLiteral(parts) => {
                (self.children.lists.items(parts)).any(|part| self.calls(part))
            }
        }
    }

    /// Where the body declares `local`: its name.
    pub fn declared(&self, local: Local) -> Span {
        match local {
            Local::Variable(variable) => self.variables.span(variable),
            Local::Constant(constant) => self.constants.span(constant),
        }
    }

    /// The body as text, one statement per line, for `peeper -dump`. `file` is
    /// where the body is written; `name_of` gives a declaration's name. A
    /// variable shows as its name and index (`x.2`), a constant other than a
    /// `bool` with its type (`1:i32`), an operation in parentheses, and a
    /// field by its index (`p.0[1]`, `Point.{ [1] = 2:i32 }`).
    pub fn text<'n>(
        &self,
        types: &Types,
        file: &SourceFile,
        name_of: impl Fn(DefId) -> &'n str + Copy,
    ) -> String {
        let mut printer = Printer {
            body: self,
            types,
            file,
            name_of,
            out: String::new(),
        };
        printer.write_statements(self.blocks[Id::from_index(0)].statements, 1);
        printer.out
    }
}

/// What `Body::text` writes with.
struct Printer<'b, F> {
    body: &'b Body,
    types: &'b Types,
    file: &'b SourceFile,
    name_of: F,
    out: String,
}

impl<'n, F: Fn(DefId) -> &'n str + Copy> Printer<'_, F> {
    /// Each of `statements` on its own line, `depth` levels in.
    fn write_statements(&mut self, statements: List<Stmt>, depth: usize) {
        for statement in self.body.children.lists.items(statements) {
            for _ in 0..depth {
                self.out.push_str("    ");
            }
            self.write_statement(statement, depth);
            self.out.push('\n');
        }
    }

    /// `statement` without its indent and line end; a block's lines are
    /// `depth + 1` levels in.
    fn write_statement(&mut self, statement: Id<Stmt>, depth: usize) {
        match self.body.stmts[statement] {
            Stmt::Let { variable, value } => {
                let mutable = self.body.variables[variable].mutable;
                self.out.push_str(if mutable { "let mut " } else { "let " });
                self.write_variable(variable);
                if let Some(value) = value {
                    self.out.push_str(" = ");
                    self.write_expr(value);
                }
            }
            Stmt::Assign { target, value } => {
                self.write_expr(target);
                self.out.push_str(" = ");
                self.write_expr(value);
            }
            Stmt::Expression(value) => self.write_expr(value),
            Stmt::Return(value) => {
                self.out.push_str("return");
                if let Some(value) = value {
                    self.out.push(' ');
                    self.write_expr(value);
                }
            }
            Stmt::Panic(message) => {
                self.out.push_str("panic ");
                self.write_expr(message);
            }
            Stmt::If {
                condition,
                then,
                otherwise,
            } => {
                self.out.push_str("if ");
                self.write_expr(condition);
                self.out.push(' ');
                self.write_block(self.body.blocks[then].statements, depth);
                if let Some(otherwise) = otherwise {
                    self.out.push_str(" else ");
                    self.write_statement(otherwise, depth);
                }
            }
            Stmt::Block(block) => self.write_block(self.body.blocks[block].statements, depth),
            Stmt::Loop { condition, body } => {
                self.out.push_str("for ");
                if let Some(condition) = condition {
                    self.write_expr(condition);
                    self.out.push(' ');
                }
                self.write_block(self.body.blocks[body].statements, depth);
            }
            Stmt::ForRange {
                index,
                value,
                range,
                body,
            } => {
                self.out.push_str("for ");
                if let Some(index) = index {
                    self.write_variable(index);
                    self.out.push_str(", ");
                }
                if let Some(value) = value {
                    self.write_variable(value);
                    self.out.push_str(" in ");
                }
                self.write_expr(range);
                self.out.push(' ');
                self.write_block(self.body.blocks[body].statements, depth);
            }
            Stmt::Break => self.out.push_str("break"),
            Stmt::Continue => self.out.push_str("continue"),
        }
    }

    /// `{`, the statements one level in, and `}` at `depth`.
    fn write_block(&mut self, statements: List<Stmt>, depth: usize) {
        self.out.push_str("{\n");
        self.write_statements(statements, depth + 1);
        for _ in 0..depth {
            self.out.push_str("    ");
        }
        self.out.push('}');
    }

    fn write_variable(&mut self, variable: Id<Variable>) {
        let name = if self.body.variables[variable].hidden {
            "_"
        } else {
            self.file.slice(self.body.variables.span(variable))
        };
        _ = write!(self.out, "{name}.{}", variable.index());
    }

    /// `exprs`, separated by commas.
    fn write_exprs(&mut self, exprs: List<Expr>) {
        for (index, expr) in self.body.children.lists.items(exprs).enumerate() {
            if index > 0 {
                self.out.push_str(", ");
            }
            self.write_expr(expr);
        }
    }

    fn write_expr(&mut self, expr: Id<Expr>) {
        let Expr { ty, ref kind } = self.body.exprs[expr];
        match *kind {
            ExprKind::Constant(ref value) => {
                _ = write!(self.out, "{}", value_text(value, &self.types[ty]));
                if !matches!(value, Value::Bool(_) | Value::Bytes(_)) {
                    _ = write!(self.out, ":{}", self.types.text(ty, self.name_of));
                }
            }
            ExprKind::Variable(variable) => self.write_variable(variable),
            ExprKind::Binary { op, left, right } => {
                self.out.push('(');
                self.write_expr(left);
                _ = write!(self.out, " {op:?} ");
                self.write_expr(right);
                self.out.push(')');
            }
            ExprKind::Unary { op, value } => {
                _ = write!(self.out, "({op:?} ");
                self.write_expr(value);
                self.out.push(')');
            }
            ExprKind::Convert(value) => {
                self.out.push_str("convert(");
                self.write_expr(value);
                _ = write!(self.out, "):{}", self.types.text(ty, self.name_of));
            }
            ExprKind::Call {
                function,
                arguments,
            } => {
                _ = write!(self.out, "call {}(", (self.name_of)(function));
                self.write_exprs(arguments);
                _ = write!(self.out, "):{}", self.types.text(ty, self.name_of));
            }
            ExprKind::StructLiteral(fields) => {
                _ = write!(self.out, "{}.{{", self.types.text(ty, self.name_of));
                for (position, field) in self.body.children.lists.items(fields).enumerate() {
                    let FieldValue { index, value } = self.body.children.field_values[field];
                    let separator = if position > 0 { ", " } else { " " };
                    _ = write!(self.out, "{separator}[{index}] = ");
                    self.write_expr(value);
                }
                self.out.push_str(" }");
            }
            ExprKind::ArrayLiteral(parts) => {
                _ = write!(self.out, "{}{{", self.types.text(ty, self.name_of));
                let Type::Array { element, .. } = self.types[ty] else {
                    unreachable!("an array literal makes an array")
                };
                for (index, part) in self.body.children.lists.items(parts).enumerate() {
                    self.out.push_str(if index > 0 { ", " } else { " " });
                    if self.types.same(self.body.exprs[part].ty, element) {
                        self.write_expr(part);
                    } else {
                        self.out.push('(');
                        self.write_expr(part);
                        self.out.push_str(")...");
                    }
                }
                self.out.push_str(" }");
            }
            ExprKind::Field { value, index } => {
                self.write_expr(value);
                _ = write!(self.out, "[{index}]");
            }
            ExprKind::Length(array) => {
                self.write_expr(array);
                self.out.push_str(".len");
            }
            ExprKind::Index { array, index } => {
                self.write_expr(array);
                self.out.push_str("[at ");
                self.write_expr(index);
                self.out.push(']');
            }
            ExprKind::Dereference(reference) => {
                self.write_expr(reference);
                self.out.push_str(".*");
            }
            ExprKind::Borrow(place) => {
                let mutable = self.types[ty].borrows() == Some(true);
                self.out.push_str(if mutable { "&mut " } else { "&" });
                self.write_expr(place);
            }
            ExprKind::Address(place) => {
                self.out.push('@');
                self.write_expr(place);
            }
            ExprKind::Range {
                start,
                end,
                step,
                inclusive,
            } => {
                self.write_expr(start);
                self.out.push_str(if inclusive { "..=" } else { ".." });
                self.write_expr(end);
                self.out.push_str(" by ");
                self.write_expr(step);
            }
            ExprKind::Print { newline, values } => {
                self.out
                    .push_str(if newline { "println(" } else { "print(" });
                self.write_exprs(values);
                self.out.push(')');
            }
            ExprKind::Invalid => self.out.push_str("invalid"),
        }
    }
}
