//! Peeper's features, one file each: a feature reads its own nodes (the
//! tree's `FeatureExpr`, `FeatureStmt` and `FeatureType`) and checks them.
//! This is the one place that names every feature: its parse rules
//! (`RULES`) and how the checker checks its nodes (`CHECK_HOOKS`), which
//! the driver hands to the phases (docs/structure-mapping.md, step 7).

use check::CheckHooks;
use check::Checker;
use check::builder;
use model::DefId;
use model::semantic_types;
use model::thir;
use parser::Rules;
use source::Span;
use syntax::ast;

mod arrays;
mod chars;
mod enums;
mod interfaces;
mod optionals;
mod pipes;
mod printing;
mod ranges;
mod raw_pointers;
mod structs;

#[cfg(test)]
mod testing;

/// Built once, when the compiler is: two rules for one token fail the build.
pub static RULES: Rules = {
    let mut rules = Rules::core();
    arrays::rows(&mut rules);
    chars::rows(&mut rules);
    enums::rows(&mut rules);
    interfaces::rows(&mut rules);
    optionals::rows(&mut rules);
    pipes::rows(&mut rules);
    printing::rows(&mut rules);
    ranges::rows(&mut rules);
    raw_pointers::rows(&mut rules);
    structs::rows(&mut rules);
    rules
};

/// How the checker checks each feature's nodes: by its feature's file.
pub static CHECK_HOOKS: CheckHooks = CheckHooks {
    check_expr,
    check_stmt,
    resolve_type,
    declare_type,
};

/// The feature expression `expr`, written at `span`, where a value of the
/// type `expected` is wanted, if one is.
fn check_expr(
    builder: &mut builder::ThirBuilder<'_, '_>,
    expr: ast::FeatureExpr,
    span: Span,
    expected: Option<semantic_types::TypeId>,
) -> thir::Expr {
    match expr {
        ast::FeatureExpr::ArrayLiteral(literal) => {
            arrays::check_array_literal(builder, literal, span)
        }
        ast::FeatureExpr::CaseTest(test) => enums::check_case_test(builder, test, span),
        ast::FeatureExpr::Char(literal) => chars::check_char_literal(builder, literal, span),
        ast::FeatureExpr::OrElse(or_else) => optionals::check_or_else(builder, or_else, span),
        ast::FeatureExpr::Pipe(pipe) => pipes::check_pipe(builder, pipe, span),
        ast::FeatureExpr::Print(print) => printing::check_print(builder, print, span),
        ast::FeatureExpr::Range(range) => ranges::check_range(builder, range, span, expected),
        ast::FeatureExpr::RawAddress(address) => {
            raw_pointers::check_raw_address(builder, address, span)
        }
        ast::FeatureExpr::StructLiteral(literal) => {
            structs::check_struct_literal(builder, literal, span, expected)
        }
        ast::FeatureExpr::VariantLiteral(literal) => {
            enums::check_variant_literal(builder, literal, span)
        }
    }
}

/// The feature statement `stmt`, written at `span`; `None` when it leaves
/// no statement, after an error.
fn check_stmt(
    builder: &mut builder::ThirBuilder<'_, '_>,
    stmt: ast::FeatureStmt,
    span: Span,
) -> Option<thir::Stmt> {
    match stmt {
        ast::FeatureStmt::Match(statement) => enums::check_match(builder, statement, span),
    }
}

/// The type the feature type `ty`, written at `span`, names.
fn resolve_type(
    checker: &mut Checker<'_>,
    ty: ast::FeatureType,
    span: Span,
) -> semantic_types::TypeId {
    match ty {
        ast::FeatureType::Array(ty) => arrays::resolve_array_type(checker, ty, span),
        ast::FeatureType::Enum(ty) => enums::resolve_enum_type(checker, ty, span),
        ast::FeatureType::Interface(ty) => interfaces::resolve_interface_type(checker, ty, span),
        ast::FeatureType::Optional(ty) => optionals::resolve_optional_type(checker, ty, span),
        ast::FeatureType::Pointer(ty) => raw_pointers::resolve_pointer_type(checker, ty, span),
        ast::FeatureType::Struct(ty) => structs::resolve_struct_type(checker, ty, span),
    }
}

/// The type the module's type declaration `definition`, written `written`,
/// declares with the feature type `ty`.
fn declare_type(
    checker: &mut Checker<'_>,
    definition: DefId,
    written: ast::TypeDecl,
    ty: ast::FeatureType,
) -> semantic_types::TypeId {
    match ty {
        ast::FeatureType::Enum(ty) => enums::declare_enum_type(checker, ty, written),
        ast::FeatureType::Interface(ty) => {
            interfaces::declare_interface_type(checker, ty, definition)
        }
        ast::FeatureType::Struct(ty) => {
            structs::declare_struct_type(checker, ty, definition, written)
        }
        ast::FeatureType::Array(_)
        | ast::FeatureType::Optional(_)
        | ast::FeatureType::Pointer(_) => {
            unreachable!("only `struct`, `iface` and `enum` begin a type declaration")
        }
    }
}
