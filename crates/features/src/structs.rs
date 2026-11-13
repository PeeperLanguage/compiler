//! Structs: `struct { name: Type, ... }` and `Type.{ name = value, ... }`.
//! The checker gives the core's struct type and struct value, so the phases
//! after it know nothing of them. A field and a field's value are the
//! core's (`ast::Field`, `ast::FieldValue`): the core reads fields too, as
//! in `point.x`.

use arena::Id;
use arena::List;
use check::Checker;
use check::builder::ThirBuilder;
use check::scope::report_redeclared;
use diagnostics::Diagnostic;
use model::DefId;
use model::layout::struct_layout;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use parser::decl::type_decl;
use parser::is_name;
use source::Span;
use std::num::NonZeroU32;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::FeatureExpr;
use crate::FeatureType;
use crate::Peeper;

/// `Type.{ name = value }`, or `.{ ... }` without a type
#[derive(Clone, Copy, Debug)]
pub struct StructLiteral {
    pub ty: Option<Id<ast::Type<Peeper>>>,
    pub fields: List<ast::FieldValue<Peeper>>,
}

/// `struct { fields }`
#[derive(Clone, Copy, Debug)]
pub struct StructType {
    pub fields: List<ast::Field<Peeper>>,
}

/// Adds the rules that read structs to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.declaration(TokenKind::Struct, type_decl);
    rules.type_start(TokenKind::Struct, struct_type);
    rules.prefix(TokenKind::Dot, anonymous_literal);
    rules.after_path(TokenKind::Dot, TokenKind::LeftBrace, typed_literal);
}

/// Reads `{ name: Type, ... }`, the fields of a struct type that starts at
/// `start`.
fn struct_type(p: &mut Parser<Peeper>, start: u32) -> Id<ast::Type<Peeper>> {
    let fields = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        true,
        is_name,
        |p| {
            let start = p.tok().span.start;
            let name = p.name();
            p.expect(TokenKind::Colon);
            let ty = p.type_expr();
            p.add(start, ast::Field { name, ty })
        },
    );
    p.add(
        start,
        ast::Type::Feature(FeatureType::Struct(StructType { fields })),
    )
}

/// Reads `.{ name = value, ... }`, a struct literal without a type.
fn anonymous_literal(p: &mut Parser<Peeper>) -> Id<ast::Expr<Peeper>> {
    let start = p.tok().span.start;
    literal(p, None, start)
}

/// Reads `.{ name = value, ... }` after the type `path`.
fn typed_literal(p: &mut Parser<Peeper>, path: Id<ast::Type<Peeper>>) -> Id<ast::Expr<Peeper>> {
    let start = p.tree.types.span(path).start;
    literal(p, Some(path), start)
}

/// Reads `.{ name = value, ... }` after `ty` (none when there is no type), a
/// literal that starts at `start`. A `name: value` field, the colon of a
/// declaration where a field is set, is read as if it were `=`, and all such
/// colons are reported in one error.
fn literal(
    p: &mut Parser<Peeper>,
    ty: Option<Id<ast::Type<Peeper>>>,
    start: u32,
) -> Id<ast::Expr<Peeper>> {
    p.next(); // `.`
    let mut colons = Vec::new();
    let fields = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        false,
        is_name,
        |p| {
            let field_start = p.tok().span.start;
            let name = p.name();
            let t = p.tok();
            if t.kind == TokenKind::Colon {
                p.next();
                colons.push(p.fix(t.span, " ="));
            } else {
                p.expect(TokenKind::Assign);
            }
            let value = p.expr(Precedence::Lowest);
            p.add(field_start, ast::FieldValue { name, value })
        },
    );
    if !colons.is_empty() {
        let d = Diagnostic::new(
            diagnostics::EXPECTED_TOKEN,
            "expected `=` after struct literal field name",
        )
        .label(&p.tree.file, colons[0].span, "");
        p.repair(d.help("set a field with `=`", colons));
    }
    p.add(
        start,
        ast::Expr::Feature(FeatureExpr::StructLiteral(StructLiteral { ty, fields })),
    )
}

/// The type a struct type written at `span` names: none yet, as only a
/// declared struct is checked.
pub(crate) fn resolve_type(checker: &mut Checker<'_, Peeper>, span: Span) -> TypeId {
    checker.not_supported(span, "struct types")
}

/// The type the struct declaration `definition`, written `written` with the
/// fields of `ty`, declares, its fields laid out for the target.
pub(crate) fn declare(
    checker: &mut Checker<'_, Peeper>,
    definition: DefId,
    written: ast::TypeDecl<Peeper>,
    ty: StructType,
) -> TypeId {
    let tree = &checker.module().ast;
    let fields = ty.fields;
    let mut field_types = Vec::with_capacity(fields.len());
    let name_of = |field: Id<ast::Field<Peeper>>| tree.children.fields[field].name;
    for (index, field) in tree.children.lists.items(fields).enumerate() {
        let ast::Field { name, ty } = tree.children.fields[field];
        let text = name.text(&tree.file);
        // A struct has few fields, so the ones before are searched.
        let mut before = tree.children.lists.items(fields).take(index).map(name_of);
        if let Some(first) = before.find(|first| first.text(&tree.file) == text) {
            let diagnostic = report_redeclared(&tree.file, text, "struct", name.span, first.span);
            checker.report(diagnostic);
        }
        // A struct held by value is checked here: one that holds
        // itself, however deep, is a cycle, as is one a reference
        // field reaches: a reference's target is a complete type.
        // A raw pointer's need not be (`resolve_type`).
        field_types.push(checker.resolve_value_type(ty));
    }
    // Invalid when a field is, as an array is when its element
    // is, so no later phase lays out a field it cannot.
    if field_types.contains(&TypeId::INVALID) {
        return TypeId::INVALID;
    }
    let (offsets, size, align) = struct_layout(&checker.types, &field_types, checker.target);
    let named = checker.types.intern(Type::Named(definition));
    let mut ty = checker.fits_target(named, size, written.name.span);
    let holders = checker.types.holders_in_fields(&field_types);
    if checker.reports_too_many_holders(named, holders, written.name.span) {
        ty = TypeId::INVALID;
    }
    if ty != TypeId::INVALID {
        checker
            .types
            .define_struct(named, field_types.into(), (offsets, size, align));
    }
    ty
}

/// `Type.{ name = value, ... }`, written at `span`, or `.{ ... }` taking
/// the struct type `expected`: a struct value with each field set once.
/// A field that is unknown, set twice or missing is reported.
pub(crate) fn check_literal(
    builder: &mut ThirBuilder<'_, '_, Peeper>,
    literal: StructLiteral,
    span: Span,
    expected: Option<TypeId>,
) -> thir::Expr {
    let StructLiteral {
        ty: written,
        fields,
    } = literal;
    let tree = &builder.checker.module().ast;
    let ty = match (written, expected) {
        (Some(written), _) => builder.checker.resolve_value_type(written),
        (None, Some(expected))
            if matches!(builder.checker.types[expected], Type::Struct { .. }) =>
        {
            expected
        }
        (None, _) => {
            let message = "this struct literal needs its type: write it before `.{`";
            builder
                .checker
                .error(diagnostics::MISSING_TYPE, message, span)
        }
    };
    let Type::Struct {
        definition,
        fields: field_types,
        ..
    } = &builder.checker.types[ty]
    else {
        if ty != TypeId::INVALID {
            let message = format!(
                "expected a struct type, found `{}`",
                builder.checker.type_text(ty)
            );
            let span = written.map_or(span, |written| tree.types.span(written));
            builder
                .checker
                .error(diagnostics::TYPE_MISMATCH, message, span);
        }
        for field in tree.children.lists.items(fields) {
            builder.add_expr(tree.children.field_values[field].value, None); // for its own errors
        }
        return thir::Expr::INVALID;
    };
    let definition = *definition;
    // Where each field is set, by its index.
    let mut set: Vec<Option<Span>> = vec![None; field_types.len()];
    let mut checked: Vec<NonZeroU32> = Vec::with_capacity(fields.len());
    for field in tree.children.lists.items(fields) {
        let ast::FieldValue { name, value } = tree.children.field_values[field];
        let Some((index, field_type)) = builder.field_index(ty, name) else {
            builder.add_expr(value, None); // for its own errors
            continue;
        };
        if let Some(first) = set[index] {
            let text = name.text(&tree.file);
            builder.checker.report(
                Diagnostic::new(
                    diagnostics::DUPLICATE_FIELD,
                    format!("field `{text}` is set twice"),
                )
                .label(&tree.file, name.span, "")
                .label(&tree.file, first, "first set here"),
            );
            builder.add_expr(value, None);
            continue;
        }
        set[index] = Some(name.span);
        let value = builder.add_required(value, field_type);
        let index = index as u32;
        checked.push(
            builder
                .body
                .add(thir::FieldValue { index, value }, name.span)
                .into(),
        );
    }
    let missing: Vec<String> = model::program::field_names(builder.checker.modules, definition)
        .zip(&set)
        .filter(|(_, set)| set.is_none())
        .map(|(name, _)| format!("`{name}`"))
        .collect();
    if !missing.is_empty() {
        let fields = if missing.len() == 1 {
            "field"
        } else {
            "fields"
        };
        let message = format!(
            "missing {fields} {} in `{}`",
            missing.join(", "),
            builder.checker.type_text(ty)
        );
        builder
            .checker
            .error(diagnostics::MISSING_FIELD, message, span);
    }
    thir::Expr {
        ty,
        kind: thir::ExprKind::StructLiteral(builder.body.children.lists.add(&checked)),
    }
}

#[cfg(test)]
mod tests;
