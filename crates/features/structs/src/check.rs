//! Checking struct declarations and struct literals into the core's struct
//! type (`Type::Struct`) and struct value (`ExprKind::StructLiteral`).

use std::num::NonZeroU32;

use arena::Id;
use check::Checked;
use check::Checker;
use check::builder::ThirBuilder;
use check::scope::report_redeclared;
use diagnostics::Diagnostic;
use model::DefId;
use model::layout::struct_layout;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::FieldValue;
use source::Span;
use syntax::ast;

use crate::StructLiteral;
use crate::StructType;

/// The type a struct type written at `span` names: none yet, as only a
/// declared struct is checked.
pub fn resolve_type<L: Checked>(checker: &mut Checker<'_, L>, span: Span) -> TypeId {
    checker.not_supported(span, "struct types")
}

/// The type the struct declaration `definition`, written `written` with the
/// fields of `ty`, declares, its fields laid out for the target.
pub fn declare<L: Checked>(
    checker: &mut Checker<'_, L>,
    definition: DefId,
    written: ast::TypeDecl<L>,
    ty: StructType<L>,
) -> TypeId {
    let tree = &checker.module().ast;
    let fields = ty.fields;
    let mut field_types = Vec::with_capacity(fields.len());
    let name_of = |field: Id<ast::Field<L>>| tree.children.fields[field].name;
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
pub fn check_literal<L: Checked>(
    builder: &mut ThirBuilder<'_, '_, L>,
    literal: StructLiteral<L>,
    span: Span,
    expected: Option<TypeId>,
) -> Expr {
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
        return Expr::INVALID;
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
                .add(FieldValue { index, value }, name.span)
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
    Expr {
        ty,
        kind: ExprKind::StructLiteral(builder.body.children.lists.add(&checked)),
    }
}
