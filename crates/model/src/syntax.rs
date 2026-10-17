//! The syntax tree of one file, in typed arenas (see the `arena` crate).
//!
//! A name is the span of its text, kept in the node it belongs to. A name or
//! child that is missing after a syntax error is an empty span or an `Invalid`
//! node.

use std::sync::Arc;

use arena::{Arena, Id, List, Lists, Node, nodes};
use source::{SourceFile, Span};
use token::TokenKind;

nodes! {
    Tree;
    Decl => decls,
    Type => types,
    Stmt => stmts,
    Block => blocks,
    Expr => exprs,
    Pattern => patterns,
    Name => names,
    Attribute => attributes,
    Param => params,
    Field => fields,
    Variant => variants,
    FieldValue => field_values,
    MatchArm => match_arms,
}

/// The syntax tree of one file. It never changes once the parser returns it.
pub struct Tree {
    pub file: Arc<SourceFile>,
    pub span: Span, // the module: from the start of the file to the last token read
    pub imports: Vec<Import>,
    pub declarations: List<Decl>,

    pub decls: Arena<Decl>,
    pub types: Arena<Type>,
    pub stmts: Arena<Stmt>,
    pub blocks: Arena<Block>,
    pub exprs: Arena<Expr>,
    pub patterns: Arena<Pattern>,
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
            span: Span::default(),
            imports: Vec::new(),
            declarations: List::EMPTY,
            decls: Arena::default(),
            types: Arena::default(),
            stmts: Arena::default(),
            blocks: Arena::default(),
            exprs: Arena::default(),
            patterns: Arena::default(),
            names: Arena::default(),
            attributes: Arena::default(),
            params: Arena::default(),
            fields: Arena::default(),
            variants: Arena::default(),
            field_values: Arena::default(),
            match_arms: Arena::default(),
            lists: Lists::default(),
        }
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

/// A name in a list of names: a type parameter, or an origin after `from`. Its
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
    /// `struct Name<T> { fields }`; the type is a `Type::Struct`
    Struct(TypeDecl),
    /// `enum Name<T> { variants }`; the type is a `Type::Enum`
    Enum(TypeDecl),
    /// `iface Name<T> { methods }`; the type is a `Type::Interface`
    Interface(TypeDecl),
    /// `type Name<T> = Type;`
    Alias(TypeDecl),
}

impl Decl {
    /// The declaration's name.
    pub fn name(&self) -> Ident {
        match *self {
            Decl::Function(function) => function.name,
            Decl::Const { name, .. } => name,
            Decl::Struct(declaration)
            | Decl::Enum(declaration)
            | Decl::Interface(declaration)
            | Decl::Alias(declaration) => declaration.name,
        }
    }
}

/// `fn (receiver) name<T>(parameters) -> Result from origins { body }`; also an
/// interface method, without metadata and body.
#[derive(Clone, Copy, Debug)]
pub struct Function {
    pub metadata: Metadata,
    pub receiver: Option<Id<Param>>,
    pub name: Ident,
    pub type_parameters: List<Name>,
    pub parameters: List<Param>,
    pub result: Option<Id<Type>>,
    pub origins: List<Name>, // what a returned reference may point into
    pub body: Option<Id<Block>>,
}

#[derive(Clone, Copy, Debug)]
pub struct TypeDecl {
    pub metadata: Metadata,
    pub name: Ident,
    pub type_parameters: List<Name>,
    pub ty: Id<Type>,
}

/// `mut name: Type = default`; in a function type, only the name and type.
#[derive(Clone, Copy, Debug)]
pub struct Param {
    pub mutable: bool,
    pub name: Option<Ident>,
    pub ty: Id<Type>,
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
    /// `&T` or `&mut T`
    Reference { mutable: bool, target: Id<Type> },
    /// `*T`
    OwnedPointer(Id<Type>),
    /// `?T`
    Optional(Id<Type>),
    /// `rawptr`
    RawPointer,
    /// `[4]T`, or `[_]T` in an array literal
    FixedArray { length: Length, element: Id<Type> },
    /// `[]T`
    DynamicArray(Id<Type>),
    /// `[..]T`
    Slice(Id<Type>),
    /// `fn(T) -> R from a`
    Function {
        parameters: List<Param>,
        result: Option<Id<Type>>,
        origins: List<Name>,
    },
    /// `struct { fields }`
    Struct(List<Field>),
    /// `enum { variants }`
    Enum(List<Variant>),
    /// `iface { methods }`: `Decl::Function`s without bodies
    Interface(List<Decl>),
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
    /// `for { }` or `for condition { }`
    Loop {
        condition: Option<Id<Expr>>,
        body: Option<Id<Block>>,
    },
    /// `for value in items { }` or `for index, value in items { }`; the iterable
    /// may be an `Expr::Range`
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
    /// `_`
    Wildcard,
    /// `none`
    None,
    /// `Enum::Case` or `Enum::Case with pattern`; the inner pattern is a `None`,
    /// a `Binding`, a `Struct` or a `Case`
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
    /// `true` or `false`, its text
    Bool,
    /// `none`
    None,
    /// `left op right`
    Binary {
        op: TokenKind,
        left: Id<Expr>,
        right: Id<Expr>,
    },
    /// `+x`, `-x`, `!x` or `~x`
    Unary { op: TokenKind, value: Id<Expr> },
    /// `&x` or `&mut x`
    Reference { mutable: bool, value: Id<Expr> },
    /// `@x`
    RawAddress(Id<Expr>),
    /// `free(x)`; the value is missing when the `(` is
    Free(Option<Id<Expr>>),
    /// `print(x)`, or `println(x)` when `newline`
    Print {
        newline: bool,
        value: Option<Id<Expr>>,
    },
    /// `f(arguments)`
    Call {
        callee: Id<Expr>,
        arguments: List<Expr>,
    },
    /// `x.name`
    Selector { value: Id<Expr>, field: Ident },
    /// `x[index]`; the index may be a `Range`
    Index { value: Id<Expr>, index: Id<Expr> },
    /// `a..b`, or `a..=b` when `inclusive`, inside `[ ]` or after `in`
    Range {
        inclusive: bool,
        start: Option<Id<Expr>>,
        end: Option<Id<Expr>>,
    },
    /// `Type.{ name = value }`, or `.{ ... }` without a type
    StructLiteral {
        ty: Option<Id<Type>>,
        fields: List<FieldValue>,
    },
    /// `Enum::Variant with payload`; the case is a `Type::Path`
    VariantLiteral { case: Id<Type>, payload: Id<Expr> },
    /// `[N]T{ values }`; the type is a fixed array, dynamic array or slice type
    ArrayLiteral { ty: Id<Type>, values: List<Expr> },
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

/// `name = value` in a struct literal.
#[derive(Clone, Copy, Debug)]
pub struct FieldValue {
    pub name: Ident,
    pub value: Id<Expr>,
}
