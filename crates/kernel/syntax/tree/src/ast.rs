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

/// A range as written: `start..end`, or `start..=end` when `inclusive`,
/// stepping by `start..end:step`, inside `[ ]`, after `in` or as an array
/// literal's value; an end may be left out before a `]`. Slicing reads it,
/// so it is the core's.
#[derive(Clone, Copy, Debug)]
pub struct WrittenRange {
    pub inclusive: bool,
    pub start: Option<Id<Expr>>,
    pub end: Option<Id<Expr>>,
    pub step: Option<Id<Expr>>,
}

/// A string literal's kind: `"..."` is a `str`, `c"..."` a `cstr`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum StringKind {
    Plain,
    C,
}

nodes! {
    Tree;
    Decl => decls,
    Type => types,
    Stmt => stmts,
    Block => blocks,
    Expr => exprs,
    Pattern => patterns,
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
    pub constraints: Arena<Constraint>,
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

    /// Whether `parameter` of a function is `self`, `&self` or `&mut self`:
    /// of the type the function belongs to.
    pub fn is_self(&self, parameter: Param) -> bool {
        let taken = match self.types[parameter.ty] {
            Type::Reference { target, .. } => target,
            _ => parameter.ty,
        };
        matches!(self.types[taken], Type::Owner)
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

    /// The parts of `expr`, if it is a written range.
    pub fn written_range(&self, expr: Id<Expr>) -> Option<WrittenRange> {
        match &self.exprs[expr] {
            Expr::Feature(FeatureExpr::Range(range)) => Some(range.written),
            _ => None,
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
    /// a feature's, such as `struct Name<T> { fields }` or
    /// `iface Name<T> { methods }`; its type, a `Type::Feature`, says which
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

/// `fn Owner::name<T>(parameters) -> Result in origins where T: Bound { body }`, or with
/// `;` for a body defined elsewhere; an interface method has no body.
/// Everything but the owner, the name and the body is its signature.
#[derive(Clone, Copy, Debug)]
pub struct Function {
    pub metadata: Metadata,
    /// `Point` in `fn Point::name`: the type the function belongs to.
    pub owner: Option<Ident>,
    pub name: Ident,
    pub signature: Signature,
    pub body: Option<Id<Block>>,
}

/// A function's type: its type parameters, parameters, result, the origins
/// of a returned reference and its constraints. A function type
/// `fn(T) -> R in a` has no type parameters and no constraints, and its
/// parameters only a name and a type. A method's first parameter is `self`,
/// of the type `Type::Owner`.
#[derive(Clone, Copy, Debug)]
pub struct Signature {
    pub type_parameters: List<Name>,
    pub parameters: List<Param>,
    pub result: Option<Id<Type>>,
    pub origins: List<Name>, // what a returned reference may point into
    /// After `where`: what the types and `const` values a call gives must
    /// satisfy.
    pub constraints: List<Constraint>,
}

/// `T: Shape` or `T: Sized(8)` after `where`: the `subject`, a type
/// parameter or a `const` value, must implement the interface `bound`, or
/// `bound` is a function that gives `true` when called with the subject
/// first, then the `arguments`.
#[derive(Clone, Copy, Debug)]
pub struct Constraint {
    pub subject: Id<Expr>,
    pub bound: Id<Expr>,
    pub arguments: List<Expr>,
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
    /// `const name: type`: the call gives a type, known when compiling.
    pub constant: bool,
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
    /// `range[T]`: a built-in type's name with a type in brackets after it,
    /// part of how that type is written, not type arguments; the checker
    /// says which names take one
    Bracketed { name: Ident, inner: Id<Type> },
    /// `&T` or `&mut T`
    Reference { mutable: bool, target: Id<Type> },
    /// `&[]T`, or `&mut []T` when `mutable`
    Slice { mutable: bool, element: Id<Type> },
    /// `...T`, or `...mut T` when `mutable`: a variadic parameter's type, the
    /// slice `&[]T` or `&mut []T` that holds the call's last arguments
    Variadic { mutable: bool, element: Id<Type> },
    /// `fn(T) -> R in a`
    Function(Signature),
    /// The type the function belongs to, `Point` in `fn Point::sum(&self)`;
    /// in an interface, the type that meets it. Only `self` has it.
    Owner,
    /// `type`, written as a parameter's type: the parameter takes a type
    Type,
    /// a feature's written type
    Feature(FeatureType),
    /// a type that could not be parsed
    Invalid,
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
    /// a feature's statement
    Feature(FeatureStmt),
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
    /// a path such as `a::B` or `Box::<T>::new`; the type is a `Type::Path` or a
    /// `Type::Generic`
    Path(Id<Type>),
    /// the number is its text
    Number,
    /// `none`
    None,
    /// `true` or `false`
    Bool(bool),
    /// `"..."` or `c"..."`; its text is its span, which `literal::value`
    /// decodes
    String(StringKind),
    /// `value as ty`
    Cast { value: Id<Expr>, ty: Id<Type> },
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
    /// `f(arguments)`, or `f::<T>(arguments)` with the types its type
    /// parameters are given
    Call {
        callee: Id<Expr>,
        type_arguments: List<Type>,
        arguments: List<Expr>,
    },
    /// `x.name`
    Selector { value: Id<Expr>, field: Ident },
    /// `x.*`: the value a reference refers to
    Dereference(Id<Expr>),
    /// `x[index]`; the index may be a `Range`
    Index { value: Id<Expr>, index: Id<Expr> },
    /// `values...`, an entry of an array literal or an argument of a call:
    /// each value of the array, slice or range `values` in order
    Spread(Id<Expr>),
    /// `( expression )`
    Parentheses(Id<Expr>),
    /// a type where a value could go, written so it can only be a type
    /// (`*T`, `rawptr`, `[N]T` with no `{` after it, `&[]T`): only an
    /// argument that takes a type accepts it
    Type(Id<Type>),
    /// a feature's expression
    Feature(FeatureExpr),
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
            Expr::Unary { .. } | Expr::Reference { .. } => Precedence::Prefix,
            Expr::Cast { .. } => Precedence::Cast,
            Expr::Name
            | Expr::Path(_)
            | Expr::Number
            | Expr::None
            | Expr::Bool(_)
            | Expr::String(_)
            | Expr::Call { .. }
            | Expr::Selector { .. }
            | Expr::Dereference(_)
            | Expr::Index { .. }
            | Expr::Spread(_)
            | Expr::Parentheses(_)
            | Expr::Type(_)
            | Expr::Invalid => Precedence::Call,
            Expr::Feature(expr) => match expr {
                FeatureExpr::ArrayLiteral(_)
                | FeatureExpr::Char(_)
                | FeatureExpr::Print(_)
                | FeatureExpr::StructLiteral(_) => Precedence::Call,
                FeatureExpr::CaseTest(_) => Precedence::Equality,
                FeatureExpr::OrElse(_) | FeatureExpr::VariantLiteral(_) => Precedence::Lowest,
                FeatureExpr::Pipe(_) | FeatureExpr::RawAddress(_) => Precedence::Prefix,
                FeatureExpr::Range(_) => Precedence::Range,
            },
        }
    }
}

/// `name = value` in a struct literal.
#[derive(Clone, Copy, Debug)]
pub struct FieldValue {
    pub name: Ident,
    pub value: Id<Expr>,
}

// The nodes of the features: each feature's file in the `features` crate
// parses and checks its own (docs/structure-mapping.md, step 7).

/// An expression of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureExpr {
    ArrayLiteral(ArrayLiteral),
    CaseTest(CaseTest),
    Char(CharLiteral),
    OrElse(OrElse),
    Pipe(Pipe),
    Print(Print),
    Range(Range),
    RawAddress(RawAddress),
    StructLiteral(StructLiteral),
    VariantLiteral(VariantLiteral),
}

/// A statement of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureStmt {
    Match(Match),
}

/// A written type of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureType {
    Array(ArrayType),
    Enum(EnumType),
    Interface(InterfaceType),
    Optional(OptionalType),
    Pointer(PointerType),
    Struct(StructType),
}

impl FeatureType {
    /// Its fields, if it has fields.
    pub fn fields(&self) -> Option<List<Field>> {
        match self {
            FeatureType::Struct(ty) => Some(ty.fields),
            _ => None,
        }
    }

    /// Its methods, if it is an interface: `Decl::Function`s without bodies.
    pub fn methods(&self) -> Option<List<Decl>> {
        match self {
            FeatureType::Interface(ty) => Some(ty.methods),
            _ => None,
        }
    }

    /// The written type it points to, if it is a typed pointer (`*T`).
    pub fn pointee(&self) -> Option<Id<Type>> {
        match self {
            FeatureType::Pointer(ty) => ty.target,
            _ => None,
        }
    }

    /// The written element type, if it is an array type (`[N]T`).
    pub fn element(&self) -> Option<Id<Type>> {
        match self {
            FeatureType::Array(ty) => Some(ty.element),
            _ => None,
        }
    }
}

/// `[length]element`
#[derive(Clone, Copy, Debug)]
pub struct ArrayType {
    /// the written number
    pub length: Span,
    pub element: Id<Type>,
}

/// `[N]T{ entries }` or `[_]T{ entries }`; each entry is a value or a
/// `values...` spread.
#[derive(Clone, Copy, Debug)]
pub struct ArrayLiteral {
    pub ty: ArrayLiteralType,
    pub entries: List<Expr>,
}

/// The type an array literal is written with.
#[derive(Clone, Copy, Debug)]
pub enum ArrayLiteralType {
    /// `[N]T`
    Written(Id<Type>),
    /// `[_]T`: its length is the number of values its entries give
    Counted { element: Id<Type> },
}

/// `'x'`, or `b'x'` when `byte`
#[derive(Clone, Copy, Debug)]
pub struct CharLiteral {
    pub byte: bool,
}

/// `enum { variants }`
#[derive(Clone, Copy, Debug)]
pub struct EnumType {
    pub variants: List<Variant>,
}

/// `Enum::Variant with payload`; the case is a `Type::Path`
#[derive(Clone, Copy, Debug)]
pub struct VariantLiteral {
    pub case: Id<Type>,
    pub payload: Id<Expr>,
}

/// `value is Enum::Variant`
#[derive(Clone, Copy, Debug)]
pub struct CaseTest {
    pub value: Id<Expr>,
    pub case: Id<Type>,
}

/// `match subject { arms }`
#[derive(Clone, Copy, Debug)]
pub struct Match {
    pub subject: Id<Expr>,
    pub arms: List<MatchArm>,
}

/// `iface { methods }`: `Decl::Function`s without bodies.
#[derive(Clone, Copy, Debug)]
pub struct InterfaceType {
    pub methods: List<Decl>,
}

/// `?T`
#[derive(Clone, Copy, Debug)]
pub struct OptionalType {
    pub inner: Id<Type>,
}

/// `value orelse fallback`
#[derive(Clone, Copy, Debug)]
pub struct OrElse {
    pub value: Id<Expr>,
    pub fallback: OrElseFallback,
}

/// What `orelse` gives when its value is missing.
#[derive(Clone, Copy, Debug)]
pub enum OrElseFallback {
    Value(Id<Expr>),
    Block(Id<Block>),
}

/// `value |> target`, as written
#[derive(Clone, Copy, Debug)]
pub struct Pipe {
    pub value: Id<Expr>,
    pub target: Id<Expr>,
}

/// `print(values)`, or `println(values)` when `newline`
#[derive(Clone, Copy, Debug)]
pub struct Print {
    pub newline: bool,
    pub values: List<Expr>,
}

/// A range, as written: its parts are the core's, which slicing and loops
/// read (`Tree::written_range`).
#[derive(Clone, Copy, Debug)]
pub struct Range {
    pub written: WrittenRange,
}

/// `*T`, or `rawptr` without a `target`
#[derive(Clone, Copy, Debug)]
pub struct PointerType {
    pub target: Option<Id<Type>>,
}

/// `@place`
#[derive(Clone, Copy, Debug)]
pub struct RawAddress {
    pub place: Id<Expr>,
}

/// `Type.{ name = value }`, or `.{ ... }` without a type
#[derive(Clone, Copy, Debug)]
pub struct StructLiteral {
    pub ty: Option<Id<Type>>,
    pub fields: List<FieldValue>,
}

/// `struct { fields }`
#[derive(Clone, Copy, Debug)]
pub struct StructType {
    pub fields: List<Field>,
}
