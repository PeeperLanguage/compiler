//! The syntax tree of one file. Nodes of one category (expressions, types,
//! statements and so on) are values of one enum, stored in that category's
//! arena and named by a typed `Id`, so a tree takes a few allocations and an id
//! of one category cannot stand where another is expected. Later phases keep
//! their results in vectors indexed by id.
//!
//! A name is the span of its text, kept in the node it belongs to. A name or
//! child that is missing after a syntax error is an empty span or an `Invalid`
//! node.

use std::marker::PhantomData;
use std::num::NonZeroU32;
use std::ops::Index;
use std::sync::Arc;

use source::{SourceFile, Span};
use token::TokenKind;

/// A node in the arena of `T`. `Option<Id<T>>` takes no more room than an id.
pub struct Id<T> {
    raw: NonZeroU32, // the node's position in its arena, plus one
    category: PhantomData<fn() -> T>,
}

impl<T> Id<T> {
    /// The node's position in its arena.
    pub fn index(self) -> usize {
        self.raw.get() as usize - 1
    }
}

// Derived impls would require T itself to be Clone, Copy and so on.
impl<T> Clone for Id<T> {
    fn clone(&self) -> Id<T> {
        *self
    }
}
impl<T> Copy for Id<T> {}
impl<T> PartialEq for Id<T> {
    fn eq(&self, other: &Id<T>) -> bool {
        self.raw == other.raw
    }
}
impl<T> Eq for Id<T> {}
impl<T> std::fmt::Debug for Id<T> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "#{}", self.index())
    }
}

/// A list of nodes of `T`: a range of the tree's list items.
pub struct List<T> {
    start: u32,
    len: u32,
    category: PhantomData<fn() -> T>,
}

impl<T> List<T> {
    pub const EMPTY: List<T> = List {
        start: 0,
        len: 0,
        category: PhantomData,
    };

    pub fn len(self) -> usize {
        self.len as usize
    }

    pub fn is_empty(self) -> bool {
        self.len == 0
    }
}

impl<T> Clone for List<T> {
    fn clone(&self) -> List<T> {
        *self
    }
}
impl<T> Copy for List<T> {}
impl<T> std::fmt::Debug for List<T> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "[{}; {}]", self.start, self.len)
    }
}

/// The nodes of one category and their spans.
pub struct Arena<T> {
    nodes: Vec<T>,
    spans: Vec<Span>,
}

impl<T> Arena<T> {
    pub fn span(&self, id: Id<T>) -> Span {
        self.spans[id.index()]
    }
}

impl<T> Default for Arena<T> {
    fn default() -> Arena<T> {
        Arena {
            nodes: Vec::new(),
            spans: Vec::new(),
        }
    }
}

impl<T> Index<Id<T>> for Arena<T> {
    type Output = T;

    fn index(&self, id: Id<T>) -> &T {
        &self.nodes[id.index()]
    }
}

/// A node type: it knows its arena in the tree, so `Tree::add` takes any node.
pub trait Node: Sized {
    fn arena(tree: &mut Tree) -> &mut Arena<Self>;
}

macro_rules! nodes {
    ($($node:ty => $arena:ident),* $(,)?) => {
        $(impl Node for $node {
            fn arena(tree: &mut Tree) -> &mut Arena<$node> {
                &mut tree.$arena
            }
        })*
    };
}

nodes! {
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

    list_items: Vec<NonZeroU32>, // the items of every list, each list's items together
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
            list_items: Vec::new(),
        }
    }

    /// Adds `node` with `span` to its arena.
    pub fn add<T: Node>(&mut self, node: T, span: Span) -> Id<T> {
        let arena = T::arena(self);
        arena.nodes.push(node);
        arena.spans.push(span);
        let raw = u32::try_from(arena.nodes.len())
            .ok()
            .and_then(NonZeroU32::new)
            .expect("an arena holds fewer than 2^32 nodes");
        Id {
            raw,
            category: PhantomData,
        }
    }

    /// The items of `list`.
    pub fn list<T>(&self, list: List<T>) -> impl ExactSizeIterator<Item = Id<T>> + '_ {
        self.list_items[list.start as usize..][..list.len()]
            .iter()
            .map(|&raw| Id {
                raw,
                category: PhantomData,
            })
    }

    /// Stores the items as a list; `items` hold raw ids of `T` (see
    /// `From<Id<T>> for NonZeroU32`).
    pub fn add_list<T>(&mut self, items: &[NonZeroU32]) -> List<T> {
        if items.is_empty() {
            return List::EMPTY;
        }
        let start = self.list_items.len() as u32;
        self.list_items.extend_from_slice(items);
        List {
            start,
            len: items.len() as u32,
            category: PhantomData,
        }
    }
}

/// Lets the parser keep the ids of the lists being read in one shared buffer.
impl<T> From<Id<T>> for NonZeroU32 {
    fn from(id: Id<T>) -> NonZeroU32 {
        id.raw
    }
}

/// `import "pkg:path" as alias;`
#[derive(Clone, Copy, Debug)]
pub struct Import {
    pub span: Span,
    pub path: Span, // the string literal
    pub alias: Option<Span>,
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
    pub name: Span,
    pub arguments: List<Expr>,
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
        name: Span,
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

/// `fn (receiver) name<T>(parameters) -> Result from origins { body }`; also an
/// interface method, without metadata and body.
#[derive(Clone, Copy, Debug)]
pub struct Function {
    pub metadata: Metadata,
    pub receiver: Option<Id<Param>>,
    pub name: Span,
    pub type_parameters: List<Name>,
    pub parameters: List<Param>,
    pub result: Option<Id<Type>>,
    pub origins: List<Name>, // what a returned reference may point into
    pub body: Option<Id<Block>>,
}

#[derive(Clone, Copy, Debug)]
pub struct TypeDecl {
    pub metadata: Metadata,
    pub name: Span,
    pub type_parameters: List<Name>,
    pub ty: Id<Type>,
}

/// `mut name: Type = default`; in a function type, only the name and type.
#[derive(Clone, Copy, Debug)]
pub struct Param {
    pub mutable: bool,
    pub name: Option<Span>,
    pub ty: Id<Type>,
    pub default: Option<Id<Expr>>,
}

/// `name: Type` in a struct.
#[derive(Clone, Copy, Debug)]
pub struct Field {
    pub name: Span,
    pub ty: Id<Type>,
}

/// `Name` or `Name: Payload` in an enum.
#[derive(Clone, Copy, Debug)]
pub struct Variant {
    pub name: Span,
    pub payload: Option<Id<Type>>,
}

#[derive(Clone, Copy, Debug)]
pub enum Type {
    /// a type written as a name; its text is its span
    Name,
    /// `a::B<T>`; the segments are `Name`s and `Generic`s
    Path(List<Type>),
    /// `Box<T>`
    Generic { name: Span, arguments: List<Type> },
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
        name: Span,
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
        index: Option<Span>,
        value: Span,
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
    Field { field: Span, binding: Option<Span> },
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
    Selector { value: Id<Expr>, field: Span },
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
    pub name: Span,
    pub value: Id<Expr>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ids_and_lists_round_trip() {
        let mut tree = Tree::new(Arc::new(SourceFile::new("a.peep", "a b")));
        let a = tree.add(Expr::Name, Span::new(0, 1));
        let b = tree.add(Expr::Name, Span::new(2, 3));
        assert_eq!(tree.exprs.span(b), Span::new(2, 3));
        let list: List<Expr> = tree.add_list(&[a.into(), b.into()]);
        assert_eq!(tree.list(list).collect::<Vec<_>>(), [a, b]);
        assert!(tree.add_list::<Expr>(&[]).is_empty());
        assert_eq!(size_of::<Option<Id<Expr>>>(), 4);
    }
}
