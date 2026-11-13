//! Peeper: the core and the list of its features (docs/feature-front.md,
//! section 7). This is the one place that names every feature; the core
//! sees only `L`, and a feature only the core.

use arena::List;
use check::Checked;
use check::Checker;
use check::builder::ThirBuilder;
use interfaces::InterfaceType;
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

/// A written type of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureType {
    Struct(StructType<Peeper>),
    Interface(InterfaceType<Peeper>),
}

impl From<StructType<Peeper>> for FeatureType {
    fn from(ty: StructType<Peeper>) -> FeatureType {
        FeatureType::Struct(ty)
    }
}

impl From<InterfaceType<Peeper>> for FeatureType {
    fn from(ty: InterfaceType<Peeper>) -> FeatureType {
        FeatureType::Interface(ty)
    }
}

// shortcut: structs are the one feature with an expression, so a feature's
// expression is the struct literal; an enum over the features, as
// `FeatureType` is, replaces it with the second.
impl Language for Peeper {
    type Expr = StructLiteral<Peeper>;
    type Type = FeatureType;

    /// A struct literal ends with its `}`.
    fn precedence(_: &StructLiteral<Peeper>) -> Precedence {
        Precedence::Call
    }

    fn fields(ty: &FeatureType) -> Option<List<ast::Field<Peeper>>> {
        match ty {
            FeatureType::Struct(ty) => Some(ty.fields),
            FeatureType::Interface(_) => None,
        }
    }

    fn methods(ty: &FeatureType) -> Option<List<ast::Decl<Peeper>>> {
        match ty {
            FeatureType::Struct(_) => None,
            FeatureType::Interface(ty) => Some(ty.methods),
        }
    }
}

/// Built once, when the compiler is: two rules for one token fail the build.
static RULES: Rules<Peeper> = {
    let mut rules = Rules::core();
    structs::parse::rows(&mut rules);
    interfaces::parse::rows(&mut rules);
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

    fn resolve_type(checker: &mut Checker<'_, Peeper>, ty: FeatureType, span: Span) -> TypeId {
        match ty {
            FeatureType::Struct(_) => structs::check::resolve_type(checker, span),
            FeatureType::Interface(_) => interfaces::check::resolve_type(checker, span),
        }
    }

    fn declare_type(
        checker: &mut Checker<'_, Peeper>,
        definition: DefId,
        written: ast::TypeDecl<Peeper>,
        ty: FeatureType,
    ) -> TypeId {
        match ty {
            FeatureType::Struct(ty) => structs::check::declare(checker, definition, written, ty),
            FeatureType::Interface(_) => interfaces::check::declare(checker, definition),
        }
    }
}
