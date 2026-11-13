//! Peeper: the core and the list of its features (docs/feature-front.md,
//! section 7). This is the one place that names every feature; the core
//! sees only `L`, and a feature only the core.

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
use structs::StructLiteral;
use structs::StructType;
use syntax::ast;
use syntax::ast::Language;
use syntax::operators::Precedence;

/// The language this compiler compiles.
#[derive(Clone, Copy, Debug)]
pub enum Peeper {}

// shortcut: structs are the one feature, so a feature's expression and type
// are its own; an enum over the features replaces each with the second.
impl Language for Peeper {
    type Expr = StructLiteral<Peeper>;
    type Type = StructType<Peeper>;

    /// A struct literal ends with its `}`.
    fn precedence(_: &StructLiteral<Peeper>) -> Precedence {
        Precedence::Call
    }

    fn fields(ty: &StructType<Peeper>) -> Option<List<ast::Field<Peeper>>> {
        Some(ty.fields)
    }
}

/// Built once, when the compiler is: two rules for one token fail the build.
static RULES: Rules<Peeper> = {
    let mut rules = Rules::core();
    structs::parse::rows(&mut rules);
    rules
};

impl Grammar for Peeper {
    fn rules() -> &'static Rules<Peeper> {
        &RULES
    }
}

impl Checked for Peeper {
    fn check_expr(
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        expr: StructLiteral<Peeper>,
        span: Span,
        expected: Option<TypeId>,
    ) -> thir::Expr {
        structs::check::check_literal(builder, expr, span, expected)
    }

    fn resolve_type(
        checker: &mut Checker<'_, Peeper>,
        _: StructType<Peeper>,
        span: Span,
    ) -> TypeId {
        structs::check::resolve_type(checker, span)
    }

    fn declare_type(
        checker: &mut Checker<'_, Peeper>,
        definition: DefId,
        written: ast::TypeDecl<Peeper>,
        ty: StructType<Peeper>,
    ) -> TypeId {
        structs::check::declare(checker, definition, written, ty)
    }
}
