//! Peeper: the core and the list of its features (docs/feature-front.md,
//! section 7). This is the one place that names every feature: a phase
//! sees only `L`, and asks this list through `Language`, `Grammar` and
//! `Checked`.

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
mod ranges;
pub mod raw_pointers;
pub mod structs;
pub mod text;

#[cfg(test)]
mod testing;

/// The language this compiler compiles.
#[derive(Clone, Copy, Debug)]
pub enum Peeper {}

/// An expression of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureExpr {
    Bool(bools::BoolLiteral),
    CaseTest(enums::CaseTest),
    Cast(casts::Cast),
    Char(chars::CharLiteral),
    OrElse(optionals::OrElse),
    Pipe(pipes::Pipe),
    Print(printing::Print),
    StructLiteral(structs::StructLiteral),
    RawAddress(raw_pointers::RawAddress),
    String(text::StringLiteral),
    VariantLiteral(enums::VariantLiteral),
    Range(WrittenRange<Peeper>),
    ArrayLiteral(arrays::ArrayLiteral),
}

/// A statement of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureStmt {
    Match(enums::Match),
    Panic(panics::Panic),
}

/// A written type of a feature's, by feature.
#[derive(Clone, Copy, Debug)]
pub enum FeatureType {
    Struct(structs::StructType),
    Interface(interfaces::InterfaceType),
    Pointer(raw_pointers::PointerType),
    Array(arrays::ArrayType),
    Optional(optionals::OptionalType),
    Enum(enums::EnumType),
}

impl Language for Peeper {
    type Expr = FeatureExpr;
    type Type = FeatureType;
    type Stmt = FeatureStmt;

    fn precedence(expr: &FeatureExpr) -> Precedence {
        match expr {
            // A struct literal ends with its `}`.
            FeatureExpr::Bool(_)
            | FeatureExpr::Char(_)
            | FeatureExpr::String(_)
            | FeatureExpr::Print(_)
            | FeatureExpr::StructLiteral(_)
            | FeatureExpr::ArrayLiteral(_) => Precedence::Call,
            FeatureExpr::Cast(_) => Precedence::Cast,
            FeatureExpr::CaseTest(_) => Precedence::Equality,
            // Ends in an expression read at the lowest precedence.
            FeatureExpr::VariantLiteral(_) => Precedence::Lowest,
            FeatureExpr::Range(_) => Precedence::Range,
            // `orelse` ends in an expression read at the lowest precedence.
            FeatureExpr::OrElse(_) => Precedence::Lowest,
            // A pipe reads its target at prefix precedence.
            FeatureExpr::Pipe(_) => Precedence::Prefix,
            // `@` reads its place at prefix precedence.
            FeatureExpr::RawAddress(_) => Precedence::Prefix,
        }
    }

    fn fields(ty: &FeatureType) -> Option<List<ast::Field<Peeper>>> {
        match ty {
            FeatureType::Struct(ty) => Some(ty.fields),
            FeatureType::Interface(_)
            | FeatureType::Pointer(_)
            | FeatureType::Array(_)
            | FeatureType::Optional(_)
            | FeatureType::Enum(_) => None,
        }
    }

    fn methods(ty: &FeatureType) -> Option<List<ast::Decl<Peeper>>> {
        match ty {
            FeatureType::Interface(ty) => Some(ty.methods),
            FeatureType::Struct(_)
            | FeatureType::Pointer(_)
            | FeatureType::Array(_)
            | FeatureType::Optional(_)
            | FeatureType::Enum(_) => None,
        }
    }

    fn pointee(ty: &FeatureType) -> Option<Id<ast::Type<Peeper>>> {
        match ty {
            FeatureType::Pointer(ty) => ty.target,
            FeatureType::Struct(_)
            | FeatureType::Interface(_)
            | FeatureType::Array(_)
            | FeatureType::Optional(_)
            | FeatureType::Enum(_) => None,
        }
    }

    fn element(ty: &FeatureType) -> Option<Id<ast::Type<Peeper>>> {
        match ty {
            FeatureType::Array(ty) => Some(ty.element),
            FeatureType::Struct(_)
            | FeatureType::Interface(_)
            | FeatureType::Pointer(_)
            | FeatureType::Optional(_)
            | FeatureType::Enum(_) => None,
        }
    }

    fn string_kind(expr: &FeatureExpr) -> Option<StringKind> {
        match expr {
            FeatureExpr::String(literal) => Some(literal.kind),
            FeatureExpr::Bool(_)
            | FeatureExpr::CaseTest(_)
            | FeatureExpr::VariantLiteral(_)
            | FeatureExpr::Cast(_)
            | FeatureExpr::Char(_)
            | FeatureExpr::OrElse(_)
            | FeatureExpr::Pipe(_)
            | FeatureExpr::Print(_)
            | FeatureExpr::Range(_)
            | FeatureExpr::StructLiteral(_)
            | FeatureExpr::ArrayLiteral(_)
            | FeatureExpr::RawAddress(_) => None,
        }
    }

    fn written_range(expr: &FeatureExpr) -> Option<WrittenRange<Peeper>> {
        match expr {
            FeatureExpr::Range(range) => Some(*range),
            FeatureExpr::Bool(_)
            | FeatureExpr::CaseTest(_)
            | FeatureExpr::VariantLiteral(_)
            | FeatureExpr::Cast(_)
            | FeatureExpr::Char(_)
            | FeatureExpr::OrElse(_)
            | FeatureExpr::Pipe(_)
            | FeatureExpr::Print(_)
            | FeatureExpr::String(_)
            | FeatureExpr::StructLiteral(_)
            | FeatureExpr::ArrayLiteral(_)
            | FeatureExpr::RawAddress(_) => None,
        }
    }
}

/// Built once, when the compiler is: two rules for one token fail the build.
static RULES: Rules<Peeper> = {
    let mut rules = Rules::core();
    bools::rows(&mut rules);
    casts::rows(&mut rules);
    enums::rows(&mut rules);
    chars::rows(&mut rules);
    structs::rows(&mut rules);
    interfaces::rows(&mut rules);
    ranges::rows(&mut rules);
    arrays::rows(&mut rules);
    optionals::rows(&mut rules);
    panics::rows(&mut rules);
    pipes::rows(&mut rules);
    printing::rows(&mut rules);
    raw_pointers::rows(&mut rules);
    text::rows(&mut rules);
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
            FeatureExpr::Bool(literal) => bools::check_literal(literal),
            FeatureExpr::Cast(cast) => casts::check_cast(builder, cast, span),
            FeatureExpr::CaseTest(_) => enums::check_expr(builder, span, "`is` tests"),
            FeatureExpr::VariantLiteral(_) => enums::check_expr(builder, span, "variant literals"),
            FeatureExpr::Char(_) => chars::check_literal(builder, span),
            FeatureExpr::OrElse(_) => optionals::check_or_else(builder, span),
            FeatureExpr::Pipe(pipe) => pipes::check_pipe(builder, pipe, span),
            FeatureExpr::Print(print) => printing::check_print(builder, print),
            FeatureExpr::StructLiteral(literal) => {
                structs::check_literal(builder, literal, span, expected)
            }
            FeatureExpr::Range(range) => ranges::check_range(builder, range, span, expected),
            FeatureExpr::ArrayLiteral(literal) => arrays::check_literal(builder, literal, span),
            FeatureExpr::String(literal) => text::check_literal(builder, literal, span),
            FeatureExpr::RawAddress(address) => raw_pointers::check_address(builder, address, span),
        }
    }

    fn check_stmt(
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        stmt: FeatureStmt,
        span: Span,
    ) -> Option<thir::Stmt> {
        match stmt {
            FeatureStmt::Match(_) => enums::check_match(builder, span),
            FeatureStmt::Panic(panic) => panics::check_panic(builder, panic),
        }
    }

    fn resolve_type(checker: &mut Checker<'_, Peeper>, ty: FeatureType, span: Span) -> TypeId {
        match ty {
            FeatureType::Struct(_) => structs::resolve_type(checker, span),
            FeatureType::Interface(_) => interfaces::resolve_type(checker, span),
            FeatureType::Pointer(ty) => raw_pointers::resolve_type(checker, ty, span),
            FeatureType::Array(ty) => arrays::resolve_type(checker, ty, span),
            FeatureType::Optional(_) => optionals::resolve_type(checker, span),
            FeatureType::Enum(_) => enums::resolve_type(checker, span),
        }
    }

    fn declare_type(
        checker: &mut Checker<'_, Peeper>,
        definition: DefId,
        written: ast::TypeDecl<Peeper>,
        ty: FeatureType,
    ) -> TypeId {
        match ty {
            FeatureType::Struct(ty) => structs::declare(checker, definition, written, ty),
            FeatureType::Interface(_) => interfaces::declare(checker, definition),
            FeatureType::Enum(_) => enums::declare(checker, written),
            FeatureType::Pointer(_) | FeatureType::Array(_) | FeatureType::Optional(_) => {
                unreachable!("only `struct`, `iface` and `enum` begin a type declaration")
            }
        }
    }
}
