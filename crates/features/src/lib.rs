//! Peeper: the core and the list of its features (docs/feature-front.md,
//! section 7). This is the one place that names every feature: a phase
//! sees only `L`, and asks this list through `Language`, `Grammar` and
//! `Checked`. Each node is listed once (`nodes!`); a feature answers for
//! its own nodes, in its own file (`ExprNode`, `StmtNode`, `TypeNode`).

use arena::Id;
use arena::List;
use check::Checked;
use check::Checker;
use check::builder::ThirBuilder;
use model::DefId;
use model::semantic_types::TypeId;
use model::thir;
use parser::Grammar;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::ast::Language;
use syntax::ast::StringKind;
use syntax::ast::WrittenRange;
use syntax::operators::Precedence;

pub mod arrays;
pub mod bools;
pub mod casts;
pub mod chars;
pub mod enums;
pub mod interfaces;
pub mod optionals;
pub mod panics;
pub mod pipes;
pub mod printing;
pub mod ranges;
pub mod raw_pointers;
pub mod structs;
pub mod text;

#[cfg(test)]
mod testing;

/// The language this compiler compiles.
#[derive(Clone, Copy, Debug)]
pub enum Peeper {}

/// What a feature says of an expression of its own, in its own file.
pub(crate) trait ExprNode {
    /// The precedence the expression has where it is an operand.
    fn precedence(&self) -> Precedence;

    /// What kind of string literal it is, if it is one.
    fn string_kind(&self) -> Option<StringKind> {
        None
    }

    /// The range it writes, if it is one.
    fn written_range(&self) -> Option<WrittenRange<Peeper>> {
        None
    }

    /// The expression, written at `span`, checked; `expected` is the type
    /// its place asks for.
    fn check(
        &self,
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        span: Span,
        expected: Option<TypeId>,
    ) -> thir::Expr;
}

/// What a feature says of a statement of its own, in its own file.
pub(crate) trait StmtNode {
    /// The statement, written at `span`, checked; `None` after an error.
    fn check(&self, builder: &mut ThirBuilder<'_, '_, Peeper>, span: Span) -> Option<thir::Stmt>;
}

/// What a feature says of a written type of its own, in its own file.
pub(crate) trait TypeNode {
    /// Its fields, if it has fields.
    fn fields(&self) -> Option<List<ast::Field<Peeper>>> {
        None
    }

    /// Its methods, if it declares methods.
    fn methods(&self) -> Option<List<ast::Decl<Peeper>>> {
        None
    }

    /// The type it points to, if it is a pointer to one.
    fn pointee(&self) -> Option<Id<ast::Type<Peeper>>> {
        None
    }

    /// The type of its elements, if it has elements.
    fn element(&self) -> Option<Id<ast::Type<Peeper>>> {
        None
    }

    /// The type it names, written at `span`.
    fn resolve(&self, checker: &mut Checker<'_, Peeper>, span: Span) -> TypeId;

    /// The type that `definition`, written `written`, declares with it.
    fn declare(
        &self,
        _checker: &mut Checker<'_, Peeper>,
        _definition: DefId,
        _written: ast::TypeDecl<Peeper>,
    ) -> TypeId {
        unreachable!("only `struct`, `iface` and `enum` begin a type declaration")
    }
}

/// Declares `$name`, the nodes of one kind with a variant per listed node;
/// its `node`, which gives a node as what its feature says of it; and its
/// `rows`, the parse rules of every node.
macro_rules! nodes {
    ($(#[$doc:meta])* $name:ident: $says:ident { $($variant:ident($node:ty)),* $(,)? }) => {
        $(#[$doc])*
        #[derive(Clone, Copy, Debug)]
        pub enum $name {
            $($variant($node)),*
        }

        impl $name {
            /// The node, to ask what its feature says of it.
            fn node(&self) -> &dyn $says {
                match self {
                    $($name::$variant(node) => node),*
                }
            }

            /// Adds the rules that read each node to `rules`.
            const fn rows(rules: &mut Rules<Peeper>) {
                $(<$node>::rows(rules);)*
            }
        }
    };
}

nodes! {
    /// An expression of a feature's, by feature.
    FeatureExpr: ExprNode {
        ArrayLiteral(arrays::ArrayLiteral),
        Bool(bools::BoolLiteral),
        CaseTest(enums::CaseTest),
        Cast(casts::Cast),
        Char(chars::CharLiteral),
        OrElse(optionals::OrElse),
        Pipe(pipes::Pipe),
        Print(printing::Print),
        Range(ranges::Range),
        RawAddress(raw_pointers::RawAddress),
        String(text::StringLiteral),
        StructLiteral(structs::StructLiteral),
        VariantLiteral(enums::VariantLiteral),
    }
}

nodes! {
    /// A statement of a feature's, by feature.
    FeatureStmt: StmtNode {
        Match(enums::Match),
        Panic(panics::Panic),
    }
}

nodes! {
    /// A written type of a feature's, by feature.
    FeatureType: TypeNode {
        Array(arrays::ArrayType),
        Enum(enums::EnumType),
        Interface(interfaces::InterfaceType),
        Optional(optionals::OptionalType),
        Pointer(raw_pointers::PointerType),
        Struct(structs::StructType),
    }
}

/// Built once, when the compiler is: two rules for one token fail the build.
static RULES: Rules<Peeper> = {
    let mut rules = Rules::core();
    FeatureExpr::rows(&mut rules);
    FeatureStmt::rows(&mut rules);
    FeatureType::rows(&mut rules);
    rules
};

impl Language for Peeper {
    type Expr = FeatureExpr;
    type Type = FeatureType;
    type Stmt = FeatureStmt;

    fn precedence(expr: &FeatureExpr) -> Precedence {
        expr.node().precedence()
    }

    fn string_kind(expr: &FeatureExpr) -> Option<StringKind> {
        expr.node().string_kind()
    }

    fn written_range(expr: &FeatureExpr) -> Option<WrittenRange<Peeper>> {
        expr.node().written_range()
    }

    fn fields(ty: &FeatureType) -> Option<List<ast::Field<Peeper>>> {
        ty.node().fields()
    }

    fn methods(ty: &FeatureType) -> Option<List<ast::Decl<Peeper>>> {
        ty.node().methods()
    }

    fn pointee(ty: &FeatureType) -> Option<Id<ast::Type<Peeper>>> {
        ty.node().pointee()
    }

    fn element(ty: &FeatureType) -> Option<Id<ast::Type<Peeper>>> {
        ty.node().element()
    }
}

impl Grammar for Peeper {
    fn rules() -> &'static Rules<Peeper> {
        &RULES
    }
}

impl Checked for Peeper {
    fn check_expr(
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        expr: FeatureExpr,
        span: Span,
        expected: Option<TypeId>,
    ) -> thir::Expr {
        expr.node().check(builder, span, expected)
    }

    fn check_stmt(
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        stmt: FeatureStmt,
        span: Span,
    ) -> Option<thir::Stmt> {
        stmt.node().check(builder, span)
    }

    fn resolve_type(checker: &mut Checker<'_, Peeper>, ty: FeatureType, span: Span) -> TypeId {
        ty.node().resolve(checker, span)
    }

    fn declare_type(
        checker: &mut Checker<'_, Peeper>,
        definition: DefId,
        written: ast::TypeDecl<Peeper>,
        ty: FeatureType,
    ) -> TypeId {
        ty.node().declare(checker, definition, written)
    }
}
