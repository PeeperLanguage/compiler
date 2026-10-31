//! The syntax tree of one file, in typed arenas (see the `arena` crate).
//!
//! A name is an `Ident`, the span of its text, kept in the node it belongs to.
//! A name or child that is missing after a syntax error is an empty span or an
//! `Invalid` node.

use std::sync::Arc;

use arena::Arena;
use arena::Id;
use arena::List;
use arena::Lists;
use arena::Node;
use arena::nodes;
use source::SourceFile;
use source::Span;

use crate::operators::BinaryOp;
use crate::operators::Precedence;
use crate::operators::UnaryOp;

nodes! {
    Tree;
    Decl => decls,
    Type => types,
    Stmt => stmts,
    Block => blocks,
    Expr => exprs,
    Pattern => patterns,
    Name => children.names,
    Attribute => children.attributes,
    Param => children.params,
    Field => children.fields,
    Variant => children.variants,
    FieldValue => children.field_values,
    MatchArm => children.match_arms,
}

/// The syntax tree of one file. It never changes once the parser returns it.
pub struct Tree {
    pub file: Arc<SourceFile>,
    pub imports: Vec<Import>,
    /// The module's own declarations, in source order: what a `DefId`
    /// counts. A body's `const` is in `decls` but not here.
    pub top_level: List<Decl>,

    pub decls: Arena<Decl>, // every declaration, top-level or in a body
    pub types: Arena<Type>,
    pub stmts: Arena<Stmt>,
    pub blocks: Arena<Block>,
    pub exprs: Arena<Expr>,
    pub patterns: Arena<Pattern>,
    pub children: Children,

    /// Whether every token of the text is in the tree: false after a lexical
    /// error, after a syntax error the parser skipped text for, and for text
    /// that was never parsed (a file that could not be read).
    pub complete: bool,
}

/// How a node holds several others: storage reached only through the node
/// that holds it, never by an id from elsewhere.
#[derive(Default)]
pub struct Children {
    pub names: Arena<Name>,
    pub attributes: Arena<Attribute>,
    pub params: Arena<Param>,
    pub fields: Arena<Field>,
    pub variants: Arena<Variant>,
    pub field_values: Arena<FieldValue>,
    pub match_arms: Arena<MatchArm>,
    pub lists: Lists,
}

impl Tree {
    pub fn new(file: Arc<SourceFile>) -> Tree {
        Tree {
            file,
            imports: Vec::new(),
            top_level: List::EMPTY,
            decls: Arena::default(),
            types: Arena::default(),
            stmts: Arena::default(),
            blocks: Arena::default(),
            exprs: Arena::default(),
            patterns: Arena::default(),
            children: Children::default(),
            complete: false,
        }
    }

    /// The top-level declaration at `index` of `top_level`, the place a
    /// `DefId` names.
    pub fn declaration(&self, index: u32) -> Id<Decl> {
        self.children.lists.get(self.top_level, index as usize)
    }

    /// The module's own declarations, in source order (`top_level`).
    pub fn declarations(&self) -> impl ExactSizeIterator<Item = Id<Decl>> + '_ {
        self.children.lists.items(self.top_level)
    }

    /// `expr` without the parentheses written around it, which only group.
    pub fn unparenthesized(&self, mut expr: Id<Expr>) -> Id<Expr> {
        while let Expr::Parentheses(inner) = self.exprs[expr] {
            expr = inner;
        }
        expr
    }

    /// Adds `node` with `span` to its arena.
    pub fn add<T: Node<Container = Tree>>(&mut self, node: T, span: Span) -> Id<T> {
        T::arena(self).push(node, span)
    }
}

/// `import "pkg:path" as alias;`
#[derive(Clone, Copy, Debug)]
pub struct Import {
    pub span: Span,
    pub path: Span, // the string literal
    pub alias: Option<Ident>,
}

/// The doc comment and attributes before a declaration.
#[derive(Clone, Copy, Debug)]
pub struct Metadata {
    pub doc_comment: Option<Span>, // the `///` lines
    pub attributes: List<Attribute>,
}

impl Metadata {
    pub const NONE: Metadata = Metadata {
        doc_comment: None,
        attributes: List::EMPTY,
    };
}

/// `#[name(arguments)]`
#[derive(Clone, Copy, Debug)]
pub struct Attribute {
    pub name: Ident,
    pub arguments: List<Expr>,
}

/// An identifier written in the source, by where it is written: its text is
/// the source there.
#[derive(Clone, Copy, Debug, Default)]
pub struct Ident {
    pub span: Span,
}

impl Ident {
    /// The identifier as `file` writes it.
    pub fn text(self, file: &SourceFile) -> &str {
        file.slice(self.span)
    }
}

/// A name in a list of names: a type parameter, or an origin after `in`. Its
/// text is its span.
#[derive(Clone, Copy, Debug)]
pub struct Name;

#[derive(Clone, Copy, Debug)]
pub enum Decl {
    Function(Function),
    /// `const name: Type = value;`, also as a statement
    Const {
        metadata: Metadata,
        name: Ident,
        ty: Option<Id<Type>>,
        value: Option<Id<Expr>>,
    },
    /// `struct Name<T> { fields }`, `enum Name<T> { variants }` or
    /// `iface Name<T> { methods }`; its type, a `Type::Struct`, `Type::Enum`
    /// or `Type::Interface`, says which
    Type(TypeDecl),
    /// `type Name<T> = Type;`
    Alias(TypeDecl),
}

impl Decl {
    /// The declaration's name.
    pub fn name(&self) -> Ident {
        match *self {
            Decl::Function(function) => function.name,
            Decl::Const { name, .. } => name,
            Decl::Type(declaration) | Decl::Alias(declaration) => declaration.name,
        }
    }

    /// The doc comment and attributes before the declaration.
    pub fn metadata(&self) -> Metadata {
        match *self {
            Decl::Function(function) => function.metadata,
            Decl::Const { metadata, .. } => metadata,
            Decl::Type(declaration) | Decl::Alias(declaration) => declaration.metadata,
        }
    }
}

/// `fn (receiver) name<T>(parameters) -> Result in origins { body }`, or
/// with `;` for a body defined elsewhere; an interface method has no body.
/// Everything but the name and body is its signature.
#[derive(Clone, Copy, Debug)]
pub struct Function {
    pub metadata: Metadata,
    pub name: Ident,
    pub signature: Signature,
    pub body: Option<Id<Block>>,
}

/// A function's type: its receiver, type parameters, parameters, result and
/// the origins of a returned reference. A function type `fn(T) -> R in a`
/// has no receiver or type parameters, and its parameters only a name and a
/// type.
#[derive(Clone, Copy, Debug)]
pub struct Signature {
    pub receiver: Option<Id<Param>>,
    pub type_parameters: List<Name>,
    pub parameters: List<Param>,
    pub result: Option<Id<Type>>,
    pub origins: List<Name>, // what a returned reference may point into
}

#[derive(Clone, Copy, Debug)]
pub struct TypeDecl {
    pub metadata: Metadata,
    pub name: Ident,
    pub type_parameters: List<Name>,
    pub ty: Id<Type>,
}

/// `mut name: Type = default`; in a function type, only the name and type. A
/// parameter written `_: Type` has no name.
#[derive(Clone, Copy, Debug)]
pub struct Param {
    pub mutable: bool,
    pub name: Option<Ident>,
    pub ty: Id<Type>,
    /// `name: ...T` or `name: ...mut T`: `ty` is the slice `[&]T` or `[&mut]T`
    /// that holds the call's last arguments.
    pub variadic: bool,
    pub default: Option<Id<Expr>>,
}

/// `name: Type` in a struct.
#[derive(Clone, Copy, Debug)]
pub struct Field {
    pub name: Ident,
    pub ty: Id<Type>,
}

/// `Name` or `Name: Payload` in an enum.
#[derive(Clone, Copy, Debug)]
pub struct Variant {
    pub name: Ident,
    pub payload: Option<Id<Type>>,
}

#[derive(Clone, Copy, Debug)]
pub enum Type {
    /// a type written as a name; its text is its span
    Name,
    /// `a::B<T>`; the segments are `Name`s and `Generic`s
    Path(List<Type>),
    /// `Box<T>`
    Generic { name: Ident, arguments: List<Type> },
    /// `range[T]`: a built-in type's name with a type in brackets after it,
    /// part of how that type is written, not type arguments; the checker
    /// says which names take one
    Bracketed { name: Ident, inner: Id<Type> },
    /// `&T` or `&mut T`
    Reference { mutable: bool, target: Id<Type> },
    /// `*T`, a raw pointer to a `T`
    Pointer(Id<Type>),
    /// `?T`
    Optional(Id<Type>),
    /// `rawptr`
    RawPointer,
    /// `[4]T`, or `[_]T` in an array literal
    FixedArray { length: Length, element: Id<Type> },
    /// `[&]T`, or `[&mut]T` when `mutable`
    Slice { mutable: bool, element: Id<Type> },
    /// `fn(T) -> R in a`
    Function(Signature),
    /// `struct { fields }`
    Struct(List<Field>),
    /// `enum { variants }`
    Enum(List<Variant>),
    /// `iface { methods }`
    Interface(List<Decl>), // `Decl::Function`s without bodies
    /// a type that could not be parsed
    Invalid,
}

/// The length of a fixed array type.
#[derive(Clone, Copy, Debug)]
pub enum Length {
    Number(Span),
    /// `_`: the number of values in the array literal
    Inferred(Span),
}

/// `{ statements }`
#[derive(Clone, Copy, Debug)]
pub struct Block {
    pub statements: List<Stmt>,
}

#[derive(Clone, Copy, Debug)]
pub enum Stmt {
    Block(Id<Block>),
    /// `let mut name: Type = value;`
    Let {
        mutable: bool,
        name: Ident,
        ty: Option<Id<Type>>,
        value: Option<Id<Expr>>,
    },
    /// a `Decl::Const`
    Const(Id<Decl>),
    Return(Option<Id<Expr>>),
    /// `panic message;`
    Panic(Id<Expr>),
    /// an expression followed by `;`
    Expression(Id<Expr>),
    /// `target = value;`
    Assign {
        target: Id<Expr>,
        value: Id<Expr>,
    },
    /// `let .{ a, b as c } = value;`; the pattern is a `Pattern::Struct`
    Destructure {
        pattern: Id<Pattern>,
        value: Id<Expr>,
    },
    /// `if condition { } else { }`; the else is a `Stmt::Block` or a `Stmt::If`
    If {
        condition: Id<Expr>,
        then: Option<Id<Block>>,
        otherwise: Option<Id<Stmt>>,
    },
    /// `for { }` or `for control { }`, where `control` is a `bool`
    /// condition or a range to count through (`for 0..n { }`, `for r { }`),
    /// as its type says.
    Loop {
        control: Option<Id<Expr>>,
        body: Option<Id<Block>>,
    },
    /// `for value in items { }` or `for index, value in items { }`, with no
    /// index for `for _, value in`; the iterable may be a range.
    ForIn {
        index: Option<Ident>,
        value: Ident,
        iterable: Id<Expr>,
        body: Option<Id<Block>>,
    },
    Break,
    Continue,
    /// `match subject { arms }`
    Match {
        subject: Id<Expr>,
        arms: List<MatchArm>,
    },
}

/// `pattern => { }`; the pattern is a `Pattern::Wildcard` or a `Pattern::Case`.
#[derive(Clone, Copy, Debug)]
pub struct MatchArm {
    pub pattern: Id<Pattern>,
    pub body: Id<Block>,
}

#[derive(Clone, Copy, Debug)]
pub enum Pattern {
    /// `_`: a match arm's or a payload's pattern that binds nothing
    Wildcard,
    /// `none`
    None,
    /// `Enum::Case` or `Enum::Case with pattern`; the inner pattern is a `None`,
    /// a `Wildcard`, a `Binding`, a `Struct` or a `Case`
    Case {
        case: Id<Type>,
        inner: Option<Id<Pattern>>,
    },
    /// a name to bind; its text is its span
    Binding,
    /// `.{ a, b as c, _ }`: `Field`s, then perhaps a `Rest`
    Struct(List<Pattern>),
    /// `a`, or `a as b`; without a binding, the field's own name is bound
    Field {
        field: Ident,
        binding: Option<Ident>,
    },
    /// the `_` that ends a struct pattern
    Rest,
    /// a pattern that could not be parsed
    Invalid,
}

#[derive(Clone, Copy, Debug)]
pub enum Expr {
    /// a use of a declared name; its text is its span
    Name,
    /// a path such as `a::B` or `Box<T>::new`; the type is a `Type::Path` or a
    /// `Type::Generic`
    Path(Id<Type>),
    /// the number is its text
    Number,
    /// `"..."`, or `c"..."` when `c_string`; `literal::value` decodes the text
    String { c_string: bool },
    /// `'x'`, or `b'x'` when `byte`; `literal::value` decodes the text
    Char { byte: bool },
    /// `true` or `false`
    Bool(bool),
    /// `none`
    None,
    /// `left op right`
    Binary {
        op: BinaryOp,
        left: Id<Expr>,
        right: Id<Expr>,
    },
    /// `-x`, `!x` or `~x`
    Unary { op: UnaryOp, value: Id<Expr> },
    /// `&x` or `&mut x`
    Reference { mutable: bool, value: Id<Expr> },
    /// `@x`
    RawAddress(Id<Expr>),
    /// `free(x)`; the value is missing when the `(` is
    Free(Option<Id<Expr>>),
    /// `print(values)`, or `println(values)` when `newline`
    Print { newline: bool, values: List<Expr> },
    /// `f(arguments)`
    Call {
        callee: Id<Expr>,
        arguments: List<Expr>,
    },
    /// `x.name`
    Selector { value: Id<Expr>, field: Ident },
    /// `x.*`: the value a reference refers to
    Dereference(Id<Expr>),
    /// `x[index]`; the index may be a `Range`
    Index { value: Id<Expr>, index: Id<Expr> },
    /// `a..b`, or `a..=b` when `inclusive`, inside `[ ]`, after `in` or as
    /// an array literal's value, with `a..b:step` stepping by more than one
    Range {
        inclusive: bool,
        start: Option<Id<Expr>>,
        end: Option<Id<Expr>>,
        step: Option<Id<Expr>>,
    },
    /// `Type.{ name = value }`, or `.{ ... }` without a type
    StructLiteral {
        ty: Option<Id<Type>>,
        fields: List<FieldValue>,
    },
    /// `Enum::Variant with payload`; the case is a `Type::Path`
    VariantLiteral { case: Id<Type>, payload: Id<Expr> },
    /// `[N]T{ entries }`; the type is a fixed array or slice type
    ArrayLiteral { ty: Id<Type>, entries: List<Expr> },
    /// `values...`, an entry of an array literal or an argument of a call:
    /// each value of the array, slice or range `values` in order
    Spread(Id<Expr>),
    /// `x as T`
    Cast { value: Id<Expr>, ty: Id<Type> },
    /// `x is Enum::Variant`
    Is { value: Id<Expr>, case: Id<Type> },
    /// `x orelse fallback`; the fallback may be a `Block`
    OrElse { value: Id<Expr>, fallback: Id<Expr> },
    /// a block as an `orelse` fallback
    Block(Id<Block>),
    /// `x |> f(arguments)`, kept as written; the call is a `Call`
    Pipe { value: Id<Expr>, call: Id<Expr> },
    /// `( expression )`
    Parentheses(Id<Expr>),
    /// an expression that could not be parsed
    Invalid,
}

impl Expr {
    /// The precedence the expression's last part was read at: an operator
    /// written after it that binds tighter joins that part, not the whole
    /// (`-a.*` reads `a`, `a + b as T` casts `b`).
    pub fn precedence(&self) -> Precedence {
        match self {
            Expr::Binary { op, .. } => op.precedence(),
            // A pipe reads its call at prefix precedence.
            Expr::Unary { .. }
            | Expr::Reference { .. }
            | Expr::RawAddress(_)
            | Expr::Pipe { .. } => Precedence::Prefix,
            Expr::Cast { .. } => Precedence::Cast,
            Expr::Is { .. } => Precedence::Equality,
            // These end in an expression read at the lowest precedence.
            Expr::OrElse { .. } | Expr::VariantLiteral { .. } => Precedence::Lowest,
            Expr::Range { .. } => Precedence::Range,
            Expr::Name
            | Expr::Path(_)
            | Expr::Number
            | Expr::String { .. }
            | Expr::Char { .. }
            | Expr::Bool(_)
            | Expr::None
            | Expr::Free(_)
            | Expr::Print { .. }
            | Expr::Call { .. }
            | Expr::Selector { .. }
            | Expr::Dereference(_)
            | Expr::Index { .. }
            | Expr::StructLiteral { .. }
            | Expr::ArrayLiteral { .. }
            | Expr::Spread(_)
            | Expr::Block(_)
            | Expr::Parentheses(_)
            | Expr::Invalid => Precedence::Call,
        }
    }
}

/// `name = value` in a struct literal.
#[derive(Clone, Copy, Debug)]
pub struct FieldValue {
    pub name: Ident,
    pub value: Id<Expr>,
}
