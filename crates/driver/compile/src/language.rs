//! Peeper: the core and the list of its features (docs/feature-front.md,
//! section 7). This is the one place that names every feature; the core
//! sees only `L`, and a feature only the core.

use arena::Id;
use arena::List;
use bools::BoolLiteral;
use casts::Cast;
use check::Checked;
use check::Checker;
use check::builder::ThirBuilder;
use interfaces::InterfaceType;
use model::DefId;
use model::semantic_types::TypeId;
use model::thir;
use parser::Grammar;
use parser::Rules;
use raw_pointers::PointerType;
use raw_pointers::RawAddress;
use source::Span;
use structs::StructLiteral;
use structs::StructType;
use syntax::ast;
use syntax::ast::Language;
use syntax::ast::StringKind;
use syntax::ast::WrittenRange;
use syntax::operators::Precedence;
use text::StringLiteral;

/// The language this compiler compiles.
#[derive(Clone, Copy, Debug)]
pub enum Peeper {}

/// An expression of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureExpr {
    Bool(BoolLiteral),
    Cast(Cast<Peeper>),
    StructLiteral(StructLiteral<Peeper>),
    RawAddress(RawAddress<Peeper>),
    String(StringLiteral),
    Range(WrittenRange<Peeper>),
}

impl From<BoolLiteral> for FeatureExpr {
    fn from(expr: BoolLiteral) -> FeatureExpr {
        FeatureExpr::Bool(expr)
    }
}

impl From<Cast<Peeper>> for FeatureExpr {
    fn from(expr: Cast<Peeper>) -> FeatureExpr {
        FeatureExpr::Cast(expr)
    }
}

impl From<StructLiteral<Peeper>> for FeatureExpr {
    fn from(expr: StructLiteral<Peeper>) -> FeatureExpr {
        FeatureExpr::StructLiteral(expr)
    }
}

impl From<StringLiteral> for FeatureExpr {
    fn from(expr: StringLiteral) -> FeatureExpr {
        FeatureExpr::String(expr)
    }
}

impl From<WrittenRange<Peeper>> for FeatureExpr {
    fn from(expr: WrittenRange<Peeper>) -> FeatureExpr {
        FeatureExpr::Range(expr)
    }
}

impl From<RawAddress<Peeper>> for FeatureExpr {
    fn from(expr: RawAddress<Peeper>) -> FeatureExpr {
        FeatureExpr::RawAddress(expr)
    }
}

/// A written type of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureType {
    Struct(StructType<Peeper>),
    Interface(InterfaceType<Peeper>),
    Pointer(PointerType<Peeper>),
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

impl From<PointerType<Peeper>> for FeatureType {
    fn from(ty: PointerType<Peeper>) -> FeatureType {
        FeatureType::Pointer(ty)
    }
}

impl Language for Peeper {
    type Expr = FeatureExpr;
    type Type = FeatureType;

    fn precedence(expr: &FeatureExpr) -> Precedence {
        match expr {
            // A struct literal ends with its `}`.
            FeatureExpr::Bool(_) | FeatureExpr::String(_) | FeatureExpr::StructLiteral(_) => {
                Precedence::Call
            }
            FeatureExpr::Cast(_) => Precedence::Cast,
            FeatureExpr::Range(_) => Precedence::Range,
            // `@` reads its place at prefix precedence.
            FeatureExpr::RawAddress(_) => Precedence::Prefix,
        }
    }

    fn fields(ty: &FeatureType) -> Option<List<ast::Field<Peeper>>> {
        match ty {
            FeatureType::Struct(ty) => Some(ty.fields),
            FeatureType::Interface(_) | FeatureType::Pointer(_) => None,
        }
    }

    fn methods(ty: &FeatureType) -> Option<List<ast::Decl<Peeper>>> {
        match ty {
            FeatureType::Interface(ty) => Some(ty.methods),
            FeatureType::Struct(_) | FeatureType::Pointer(_) => None,
        }
    }

    fn pointee(ty: &FeatureType) -> Option<Id<ast::Type<Peeper>>> {
        match ty {
            FeatureType::Pointer(ty) => ty.target,
            FeatureType::Struct(_) | FeatureType::Interface(_) => None,
        }
    }

    fn string_kind(expr: &FeatureExpr) -> Option<StringKind> {
        match expr {
            FeatureExpr::String(literal) => Some(literal.kind),
            FeatureExpr::Bool(_)
            | FeatureExpr::Cast(_)
            | FeatureExpr::Range(_)
            | FeatureExpr::StructLiteral(_)
            | FeatureExpr::RawAddress(_) => None,
        }
    }

    fn written_range(expr: &FeatureExpr) -> Option<WrittenRange<Peeper>> {
        match expr {
            FeatureExpr::Range(range) => Some(*range),
            FeatureExpr::Bool(_)
            | FeatureExpr::Cast(_)
            | FeatureExpr::String(_)
            | FeatureExpr::StructLiteral(_)
            | FeatureExpr::RawAddress(_) => None,
        }
    }
}

/// Built once, when the compiler is: two rules for one token fail the build.
static RULES: Rules<Peeper> = {
    let mut rules = Rules::core();
    bools::parse::rows(&mut rules);
    casts::parse::rows(&mut rules);
    structs::parse::rows(&mut rules);
    interfaces::parse::rows(&mut rules);
    ranges::parse::rows(&mut rules);
    raw_pointers::parse::rows(&mut rules);
    text::parse::rows(&mut rules);
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
        expr: FeatureExpr,
        span: Span,
        expected: Option<TypeId>,
    ) -> thir::Expr {
        match expr {
            FeatureExpr::Bool(literal) => bools::check::check_literal(literal),
            FeatureExpr::Cast(cast) => casts::check::check_cast(builder, cast, span),
            FeatureExpr::StructLiteral(literal) => {
                structs::check::check_literal(builder, literal, span, expected)
            }
            FeatureExpr::Range(range) => ranges::check::check_range(builder, range, span, expected),
            FeatureExpr::String(literal) => text::check::check_literal(builder, literal, span),
            FeatureExpr::RawAddress(address) => {
                raw_pointers::check::check_address(builder, address, span)
            }
        }
    }

    fn resolve_type(checker: &mut Checker<'_, Peeper>, ty: FeatureType, span: Span) -> TypeId {
        match ty {
            FeatureType::Struct(_) => structs::check::resolve_type(checker, span),
            FeatureType::Interface(_) => interfaces::check::resolve_type(checker, span),
            FeatureType::Pointer(ty) => raw_pointers::check::resolve_type(checker, ty, span),
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
            FeatureType::Pointer(_) => {
                unreachable!("only `struct` and `iface` begin a feature's type declaration")
            }
        }
    }
}
