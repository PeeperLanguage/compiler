//! The syntax tree of one file, in typed arenas (see the `arena` crate).
//!
//! A name is an `Ident`, the span of its text, kept in the node it belongs to.
//! A name or child that is missing after a syntax error is an empty span or an
//! `Invalid` node.

use std::fmt::Debug;
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

/// The language a tree is written in: the core's nodes, plus the nodes of
/// each feature, which the top crate lists (docs/feature-front.md, section
/// 7). A feature's node sits in the core's arena as a `Feature` kind, so it
/// keeps the core's ids and spans.
pub trait Language: Copy + Debug + 'static {
    /// A feature's expression: one kind per feature, each the feature's own.
    type Expr: Copy + Debug + Send + Sync;
    /// A feature's written type.
    type Type: Copy + Debug + Send + Sync;

    /// The precedence the feature expression `expr`'s last part was read at
    /// (`Expr::precedence`).
    fn precedence(expr: &Self::Expr) -> Precedence;

    /// The fields of the feature type `ty`, if it has fields.
    fn fields(ty: &Self::Type) -> Option<List<Field<Self>>>;

    /// The methods of the feature type `ty`, if it is an interface:
    /// `Decl::Function`s without bodies.
    fn methods(ty: &Self::Type) -> Option<List<Decl<Self>>>;

    /// The written type the feature type `ty` points to, if it is a typed
    /// pointer (`*T`).
    fn pointee(ty: &Self::Type) -> Option<Id<Type<Self>>>;

    /// The kind of string literal the feature expression `expr` is, if it
    /// is one.
    fn string_kind(expr: &Self::Expr) -> Option<StringKind>;

    /// The parts of the feature expression `expr`, if it is a written range.
    fn written_range(expr: &Self::Expr) -> Option<WrittenRange<Self>>;
}

/// A range as written: `start..end`, or `start..=end` when `inclusive`,
/// stepping by `start..end:step`, inside `[ ]`, after `in` or as an array
/// literal's value; an end may be left out before a `]`. Slicing reads it,
/// so it is the core's.
#[derive(Clone, Copy, Debug)]
pub struct WrittenRange<L: Language> {
    pub inclusive: bool,
    pub start: Option<Id<Expr<L>>>,
    pub end: Option<Id<Expr<L>>>,
    pub step: Option<Id<Expr<L>>>,
}

/// A string literal's kind: `"..."` is a `str`, `c"..."` a `cstr`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum StringKind {
    Plain,
    C,
}

nodes! {
    <L: Language> Tree<L>;
    Decl<L> => decls,
    Type<L> => types,
    Stmt<L> => stmts,
    Block<L> => blocks,
    Expr<L> => exprs,
    Pattern<L> => patterns,
    Attribute<L> => children.attributes,
    Param<L> => children.params,
    Field<L> => children.fields,
    Variant<L> => children.variants,
    FieldValue<L> => children.field_values,
    MatchArm<L> => children.match_arms,
}

/// The syntax tree of one file. It never changes once the parser returns it.
pub struct Tree<L: Language> {
    pub file: Arc<SourceFile>,
    pub imports: Vec<Import>,
    /// The module's own declarations, in source order: what a `DefId`
    /// counts. A body's `const` is in `decls` but not here.
    pub top_level: List<Decl<L>>,

    pub decls: Arena<Decl<L>>, // every declaration, top-level or in a body
    pub types: Arena<Type<L>>,
    pub stmts: Arena<Stmt<L>>,
    pub blocks: Arena<Block<L>>,
    pub exprs: Arena<Expr<L>>,
    pub patterns: Arena<Pattern<L>>,
    pub children: Children<L>,

    /// Whether every token of the text is in the tree: false after a lexical
    /// error, after a syntax error the parser skipped text for, and for text
    /// that was never parsed (a file that could not be read).
    pub complete: bool,
}

/// How a node holds several others: storage reached only through the node
/// that holds it, never by an id from elsewhere.
pub struct Children<L: Language> {
    pub names: Arena<Name>,
    pub attributes: Arena<Attribute<L>>,
    pub params: Arena<Param<L>>,
    pub fields: Arena<Field<L>>,
    pub variants: Arena<Variant<L>>,
    pub field_values: Arena<FieldValue<L>>,
    pub match_arms: Arena<MatchArm<L>>,
    pub lists: Lists,
}

// Derived, it would require `L: Default`.
impl<L: Language> Default for Children<L> {
    fn default() -> Children<L> {
        Children {
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
}

impl<L: Language> Tree<L> {
    pub fn new(file: Arc<SourceFile>) -> Tree<L> {
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
    pub fn declaration(&self, index: u32) -> Id<Decl<L>> {
        self.children.lists.get(self.top_level, index as usize)
    }

    /// The module's own declarations, in source order (`top_level`).
    pub fn declarations(&self) -> impl ExactSizeIterator<Item = Id<Decl<L>>> + '_ {
        self.children.lists.items(self.top_level)
    }

    /// `expr` without the parentheses written around it, which only group.
    pub fn unparenthesized(&self, mut expr: Id<Expr<L>>) -> Id<Expr<L>> {
        while let Expr::Parentheses(inner) = self.exprs[expr] {
            expr = inner;
        }
        expr
    }

    /// The parts of `expr`, if it is a written range.
    pub fn written_range(&self, expr: Id<Expr<L>>) -> Option<WrittenRange<L>> {
        match &self.exprs[expr] {
            Expr::Feature(feature) => L::written_range(feature),
            _ => None,
        }
    }

    /// Adds `node` with `span` to its arena.
    pub fn add<T: Node<Container = Tree<L>>>(&mut self, node: T, span: Span) -> Id<T> {
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
pub struct Metadata<L: Language> {
    pub doc_comment: Option<Span>, // the `///` lines
    pub attributes: List<Attribute<L>>,
}

impl<L: Language> Metadata<L> {
    pub const NONE: Metadata<L> = Metadata {
        doc_comment: None,
        attributes: List::EMPTY,
    };
}

/// `#[name(arguments)]`
#[derive(Clone, Copy, Debug)]
pub struct Attribute<L: Language> {
    pub name: Ident,
    pub arguments: List<Expr<L>>,
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
pub enum Decl<L: Language> {
    Function(Function<L>),
    /// `const name: Type = value;`, also as a statement
    Const {
        metadata: Metadata<L>,
        name: Ident,
        ty: Option<Id<Type<L>>>,
        value: Option<Id<Expr<L>>>,
    },
    /// `enum Name<T> { variants }` or a feature's, such as
    /// `struct Name<T> { fields }` or `iface Name<T> { methods }`; its type,
    /// a `Type::Enum` or `Type::Feature`, says which
    Type(TypeDecl<L>),
    /// `type Name<T> = Type;`
    Alias(TypeDecl<L>),
}

impl<L: Language> Decl<L> {
    /// The declaration's name.
    pub fn name(&self) -> Ident {
        match *self {
            Decl::Function(function) => function.name,
            Decl::Const { name, .. } => name,
            Decl::Type(declaration) | Decl::Alias(declaration) => declaration.name,
        }
    }

    /// The doc comment and attributes before the declaration.
    pub fn metadata(&self) -> Metadata<L> {
        match *self {
            Decl::Function(function) => function.metadata,
            Decl::Const { metadata, .. } => metadata,
            Decl::Type(declaration) | Decl::Alias(declaration) => declaration.metadata,
        }
    }
}

/// `fn Owner::name<T>(parameters) -> Result in origins { body }`, or with
/// `;` for a body defined elsewhere; an interface method has no body.
/// Everything but the owner, the name and the body is its signature.
#[derive(Clone, Copy, Debug)]
pub struct Function<L: Language> {
    pub metadata: Metadata<L>,
    /// `Point` in `fn Point::name`: the type the function belongs to.
    pub owner: Option<Ident>,
    pub name: Ident,
    pub signature: Signature<L>,
    pub body: Option<Id<Block<L>>>,
}

/// A function's type: its type parameters, parameters, result and the
/// origins of a returned reference. A function type `fn(T) -> R in a` has no
/// type parameters, and its parameters only a name and a type. A method's
/// first parameter is `self`, of the type `Type::Owner`.
#[derive(Clone, Copy, Debug)]
pub struct Signature<L: Language> {
    pub type_parameters: List<Name>,
    pub parameters: List<Param<L>>,
    pub result: Option<Id<Type<L>>>,
    pub origins: List<Name>, // what a returned reference may point into
}

#[derive(Clone, Copy, Debug)]
pub struct TypeDecl<L: Language> {
    pub metadata: Metadata<L>,
    pub name: Ident,
    pub type_parameters: List<Name>,
    pub ty: Id<Type<L>>,
}

/// `mut name: Type = default`; in a function type, only the name and type. A
/// parameter written `_: Type` has no name.
#[derive(Clone, Copy, Debug)]
pub struct Param<L: Language> {
    /// `const name: type`: the call gives a type, known when compiling.
    pub constant: bool,
    pub mutable: bool,
    pub name: Option<Ident>,
    pub ty: Id<Type<L>>,
    /// `name: ...T` or `name: ...mut T`: `ty` is the slice `&[]T` or `&mut []T`
    /// that holds the call's last arguments.
    pub variadic: bool,
    pub default: Option<Id<Expr<L>>>,
}

/// `name: Type` in a struct.
#[derive(Clone, Copy, Debug)]
pub struct Field<L: Language> {
    pub name: Ident,
    pub ty: Id<Type<L>>,
}

/// `Name` or `Name: Payload` in an enum.
#[derive(Clone, Copy, Debug)]
pub struct Variant<L: Language> {
    pub name: Ident,
    pub payload: Option<Id<Type<L>>>,
}

#[derive(Clone, Copy, Debug)]
pub enum Type<L: Language> {
    /// a type written as a name; its text is its span
    Name,
    /// `a::B<T>`; the segments are `Name`s and `Generic`s
    Path(List<Type<L>>),
    /// `Box<T>`
    Generic {
        name: Ident,
        arguments: List<Type<L>>,
    },
    /// `range[T]`: a built-in type's name with a type in brackets after it,
    /// part of how that type is written, not type arguments; the checker
    /// says which names take one
    Bracketed { name: Ident, inner: Id<Type<L>> },
    /// `&T` or `&mut T`
    Reference { mutable: bool, target: Id<Type<L>> },
    /// `?T`
    Optional(Id<Type<L>>),
    /// `[4]T`, or `[_]T` in an array literal
    FixedArray {
        length: Length,
        element: Id<Type<L>>,
    },
    /// `&[]T`, or `&mut []T` when `mutable`
    Slice { mutable: bool, element: Id<Type<L>> },
    /// `fn(T) -> R in a`
    Function(Signature<L>),
    /// `enum { variants }`
    Enum(List<Variant<L>>),
    /// The type the function belongs to, `Point` in `fn Point::sum(&self)`;
    /// in an interface, the type that meets it. Only `self` has it.
    Owner,
    /// `type`, written as a parameter's type: the parameter takes a type
    Type,
    /// a feature's written type
    Feature(L::Type),
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
pub struct Block<L: Language> {
    pub statements: List<Stmt<L>>,
}

#[derive(Clone, Copy, Debug)]
pub enum Stmt<L: Language> {
    Block(Id<Block<L>>),
    /// `let mut name: Type = value;`
    Let {
        mutable: bool,
        name: Ident,
        ty: Option<Id<Type<L>>>,
        value: Option<Id<Expr<L>>>,
    },
    /// a `Decl::Const`
    Const(Id<Decl<L>>),
    Return(Option<Id<Expr<L>>>),
    /// `panic message;`
    Panic(Id<Expr<L>>),
    /// an expression followed by `;`
    Expression(Id<Expr<L>>),
    /// `target = value;`
    Assign {
        target: Id<Expr<L>>,
        value: Id<Expr<L>>,
    },
    /// `let .{ a, b as c } = value;`; the pattern is a `Pattern::Struct`
    Destructure {
        pattern: Id<Pattern<L>>,
        value: Id<Expr<L>>,
    },
    /// `if condition { } else { }`; the else is a `Stmt::Block` or a `Stmt::If`
    If {
        condition: Id<Expr<L>>,
        then: Option<Id<Block<L>>>,
        otherwise: Option<Id<Stmt<L>>>,
    },
    /// `for { }` or `for control { }`, where `control` is a `bool`
    /// condition or a range to count through (`for 0..n { }`, `for r { }`),
    /// as its type says.
    Loop {
        control: Option<Id<Expr<L>>>,
        body: Option<Id<Block<L>>>,
    },
    /// `for value in items { }` or `for index, value in items { }`, with no
    /// index for `for _, value in`; the iterable may be a range.
    ForIn {
        index: Option<Ident>,
        value: Ident,
        iterable: Id<Expr<L>>,
        body: Option<Id<Block<L>>>,
    },
    Break,
    Continue,
    /// `match subject { arms }`
    Match {
        subject: Id<Expr<L>>,
        arms: List<MatchArm<L>>,
    },
}

/// `pattern => { }`; the pattern is a `Pattern::Wildcard` or a `Pattern::Case`.
#[derive(Clone, Copy, Debug)]
pub struct MatchArm<L: Language> {
    pub pattern: Id<Pattern<L>>,
    pub body: Id<Block<L>>,
}

#[derive(Clone, Copy, Debug)]
pub enum Pattern<L: Language> {
    /// `_`: a match arm's or a payload's pattern that binds nothing
    Wildcard,
    /// `none`
    None,
    /// `Enum::Case` or `Enum::Case with pattern`; the inner pattern is a `None`,
    /// a `Wildcard`, a `Binding`, a `Struct` or a `Case`
    Case {
        case: Id<Type<L>>,
        inner: Option<Id<Pattern<L>>>,
    },
    /// a name to bind; its text is its span
    Binding,
    /// `.{ a, b as c, _ }`: `Field`s, then perhaps a `Rest`
    Struct(List<Pattern<L>>),
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
pub enum Expr<L: Language> {
    /// a use of a declared name; its text is its span
    Name,
    /// a path such as `a::B` or `Box<T>::new`; the type is a `Type::Path` or a
    /// `Type::Generic`
    Path(Id<Type<L>>),
    /// the number is its text
    Number,
    /// `'x'`, or `b'x'` when `byte`; `literal::value` decodes the text
    Char { byte: bool },
    /// `none`
    None,
    /// `left op right`
    Binary {
        op: BinaryOp,
        left: Id<Expr<L>>,
        right: Id<Expr<L>>,
    },
    /// `-x`, `!x` or `~x`
    Unary { op: UnaryOp, value: Id<Expr<L>> },
    /// `&x` or `&mut x`
    Reference { mutable: bool, value: Id<Expr<L>> },
    /// `print(values)`, or `println(values)` when `newline`
    Print {
        newline: bool,
        values: List<Expr<L>>,
    },
    /// `f(arguments)`
    Call {
        callee: Id<Expr<L>>,
        arguments: List<Expr<L>>,
    },
    /// `x.name`
    Selector { value: Id<Expr<L>>, field: Ident },
    /// `x.*`: the value a reference refers to
    Dereference(Id<Expr<L>>),
    /// `x[index]`; the index may be a `Range`
    Index {
        value: Id<Expr<L>>,
        index: Id<Expr<L>>,
    },
    /// `Enum::Variant with payload`; the case is a `Type::Path`
    VariantLiteral {
        case: Id<Type<L>>,
        payload: Id<Expr<L>>,
    },
    /// `[N]T{ entries }`; the type is a fixed array or slice type
    ArrayLiteral {
        ty: Id<Type<L>>,
        entries: List<Expr<L>>,
    },
    /// `values...`, an entry of an array literal or an argument of a call:
    /// each value of the array, slice or range `values` in order
    Spread(Id<Expr<L>>),
    /// `x is Enum::Variant`
    Is {
        value: Id<Expr<L>>,
        case: Id<Type<L>>,
    },
    /// `x orelse fallback`; the fallback may be a `Block`
    OrElse {
        value: Id<Expr<L>>,
        fallback: Id<Expr<L>>,
    },
    /// a block as an `orelse` fallback
    Block(Id<Block<L>>),
    /// `x |> f(arguments)`, kept as written; the call is a `Call`
    Pipe {
        value: Id<Expr<L>>,
        call: Id<Expr<L>>,
    },
    /// `( expression )`
    Parentheses(Id<Expr<L>>),
    /// a type where a value could go, written so it can only be a type
    /// (`*T`, `rawptr`, `[N]T` with no `{` after it, `&[]T`): only an
    /// argument that takes a type accepts it
    Type(Id<Type<L>>),
    /// a feature's expression
    Feature(L::Expr),
    /// an expression that could not be parsed
    Invalid,
}

impl<L: Language> Expr<L> {
    /// The precedence the expression's last part was read at: an operator
    /// written after it that binds tighter joins that part, not the whole
    /// (`-a.*` reads `a`, `a + b as T` casts `b`).
    pub fn precedence(&self) -> Precedence {
        match self {
            Expr::Binary { op, .. } => op.precedence(),
            // A pipe reads its call at prefix precedence.
            Expr::Unary { .. } | Expr::Reference { .. } | Expr::Pipe { .. } => Precedence::Prefix,
            Expr::Is { .. } => Precedence::Equality,
            // These end in an expression read at the lowest precedence.
            Expr::OrElse { .. } | Expr::VariantLiteral { .. } => Precedence::Lowest,
            Expr::Name
            | Expr::Path(_)
            | Expr::Number
            | Expr::Char { .. }
            | Expr::None
            | Expr::Print { .. }
            | Expr::Call { .. }
            | Expr::Selector { .. }
            | Expr::Dereference(_)
            | Expr::Index { .. }
            | Expr::ArrayLiteral { .. }
            | Expr::Spread(_)
            | Expr::Block(_)
            | Expr::Parentheses(_)
            | Expr::Type(_)
            | Expr::Invalid => Precedence::Call,
            Expr::Feature(expr) => L::precedence(expr),
        }
    }
}

/// `name = value` in a struct literal.
#[derive(Clone, Copy, Debug)]
pub struct FieldValue<L: Language> {
    pub name: Ident,
    pub value: Id<Expr<L>>,
}
