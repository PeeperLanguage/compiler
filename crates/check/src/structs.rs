//! Structs: checking `struct { name: Type, ... }` declarations and
//! `Type.{ name = value, ... }` literals, and reading a field, as in
//! `point.x`. The checker gives a `Type::Struct`, an `ExprKind::StructLiteral`
//! and an `ExprKind::Field`, so the phases after it know nothing of this
//! file. Which values have fields, the dispatch decides
//! (`expression/places.rs`).

use crate::builder;
use crate::scope;
use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use diagnostics::suggest::nearest_name;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::layout;
use model::semantic_types;
use model::thir;
use source::Span;
use std::num;
use tree::ast;
use tree::ast::Ident;

use crate::Checker;
use crate::Checking;

/// The type a struct type written at `span` names: none yet, as only a
/// declared struct is checked.
pub(crate) fn resolve_struct_type(checker: &mut Checker<'_>, span: Span) -> semantic_types::TypeId {
    checker.not_supported(span, "struct types")
}

/// The type the struct declaration `definition`, written `written` with
/// `fields`, declares, its fields laid out for the target. Given an open
/// type, as in its own check, it stays a name: each copy is laid out.
pub(crate) fn declare_struct_type(
    checker: &mut Checker<'_>,
    fields: List<ast::Field>,
    definition: DefId,
    written: ast::TypeDecl,
) -> semantic_types::TypeId {
    let tree = &checker.module().ast;
    let mut field_types = Vec::with_capacity(fields.len());
    let name_of = |field: Id<ast::Field>| tree.children.fields[field].name;
    for (index, field) in tree.children.lists.items(fields).enumerate() {
        let ast::Field { name, ty } = tree.children.fields[field];
        let text = name.text(&tree.file);
        // A struct has few fields, so the ones before are searched.
        let mut before = tree.children.lists.items(fields).take(index).map(name_of);
        if let Some(first) = before.find(|first| first.text(&tree.file) == text) {
            let diagnostic =
                scope::report_redeclared(&tree.file, text, "struct", name.span, first.span);
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
    if field_types.contains(&semantic_types::TypeId::INVALID) {
        return semantic_types::TypeId::INVALID;
    }
    let named = checker.declared_name(definition);
    if checker.types.is_open(named) {
        return named;
    }
    let (offsets, size, align) =
        layout::struct_layout(&checker.types, &field_types, checker.target);
    let mut ty = checker.fits_target(named, size, written.name.span);
    let holders = checker.types.holders_in_fields(&field_types);
    if checker.reports_too_many_holders(named, holders, written.name.span) {
        ty = semantic_types::TypeId::INVALID;
    }
    if ty != semantic_types::TypeId::INVALID {
        checker
            .types
            .define_struct(named, field_types.into(), (offsets, size, align));
    }
    ty
}

/// `Type.{ name = value, ... }`, written at `span`, or `.{ ... }` taking
/// the struct type `expected`: a struct value with each field set once.
/// A field that is unknown, set twice or missing is reported.
pub(crate) fn check_struct_literal(
    builder: &mut builder::ThirBuilder<'_, '_>,
    written: Option<Id<ast::Type>>,
    fields: List<ast::FieldValue>,
    span: Span,
    expected: Option<semantic_types::TypeId>,
) -> thir::Expr {
    let tree = &builder.checker.module().ast;
    let ty = match (written, expected) {
        (Some(written), _) => builder.checker.resolve_value_type(written),
        // A struct, or one given an open type (`open_struct`).
        (None, Some(expected))
            if matches!(
                builder.checker.types[expected],
                semantic_types::Type::Struct { .. } | semantic_types::Type::Named { .. }
            ) =>
        {
            expected
        }
        // Reported where it was written, or one each copy decides.
        (None, Some(expected))
            if expected == semantic_types::TypeId::INVALID
                || builder.checker.types.is_open(expected) =>
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
    let (semantic_types::Type::Struct { definition, .. }
    | semantic_types::Type::Named { definition, .. }) = builder.checker.types[ty]
    else {
        let open = builder.checker.types.is_open(ty);
        if ty != semantic_types::TypeId::INVALID && !open {
            let message = format!(
                "expected a struct type, found `{}`",
                builder.checker.type_text(ty)
            );
            let span = written.map_or(span, |written| tree.types.span(written));
            builder
                .checker
                .error(diagnostics::TYPE_MISMATCH, message, span);
        }
        // For their own errors; what each field's type is, is not known.
        for field in tree.children.lists.items(fields) {
            let value = tree.children.field_values[field].value;
            builder.add_expr(value, Some(semantic_types::TypeId::INVALID));
        }
        if open {
            return thir::Expr::UNDECIDED;
        }
        return thir::Expr::INVALID;
    };
    // Where each field is set, by its index.
    let count = model::program::field_names(builder.checker.modules, definition).count();
    let mut set: Vec<Option<Span>> = vec![None; count];
    let mut checked: Vec<num::NonZeroU32> = Vec::with_capacity(fields.len());
    for field in tree.children.lists.items(fields) {
        let ast::FieldValue { name, value } = tree.children.field_values[field];
        let Some((index, field_type)) = field_index(builder, ty, name) else {
            builder.add_expr(value, Some(semantic_types::TypeId::INVALID)); // for its own errors
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

/// `place.name`: the field `name` of `place`, a checked struct in a place.
pub(crate) fn check_field(
    builder: &mut builder::ThirBuilder<'_, '_>,
    place: Id<thir::Expr>,
    name: Ident,
) -> thir::Expr {
    let ty = builder.body.exprs[place].ty;
    let Some((index, field_type)) = field_index(builder, ty, name) else {
        return thir::Expr::INVALID;
    };
    thir::Expr {
        ty: field_type,
        kind: thir::ExprKind::Field {
            value: place,
            index: index as u32,
        },
    }
}

/// The index of the field `name` of the struct type `ty`, in declaration
/// order, and its type; `None` after reporting that the type has no such
/// field, with its nearest field name. A struct given an open type has no
/// copy holding its fields' types: they are read with what it is given
/// (`field_types_given`).
fn field_index(
    builder: &mut builder::ThirBuilder<'_, '_>,
    ty: semantic_types::TypeId,
    name: Ident,
) -> Option<(usize, semantic_types::TypeId)> {
    let (semantic_types::Type::Struct { definition, .. }
    | semantic_types::Type::Named { definition, .. }) = builder.checker.types[ty]
    else {
        unreachable!("only a struct has fields")
    };
    let file = &builder.checker.module().ast.file;
    let text = name.text(file);
    let modules = builder.checker.modules;
    let Some(index) =
        model::program::field_names(modules, definition).position(|field| field == text)
    else {
        let message = format!("`{}` has no field `{text}`", builder.checker.type_text(ty));
        let fields = model::program::field_names(modules, definition).map(|field| (field, 0));
        let meant = nearest_name(text, fields).map(str::to_string);
        builder
            .checker
            .report_unknown(diagnostics::UNKNOWN_FIELD, name.span, message, meant);
        return None;
    };
    let field_type = match builder.checker.types[ty] {
        semantic_types::Type::Named { arguments, .. } => {
            field_types_given(builder.checker, definition, arguments, name.span)[index]
        }
        _ => {
            builder.checker.types[ty]
                .members()
                .expect("a struct has members")
                .fields[index]
        }
    };
    Some((index, field_type))
}

/// The types of the fields of the struct `definition`, in order, given
/// `arguments`, asked for at `span`: each as its declaration writes it,
/// read again with them (`resolve_value_type`), recording nothing, once
/// for each `arguments`. For a struct given an open type, which has no
/// copy to hold them.
pub(crate) fn field_types_given<'c>(
    checker: &'c mut Checker<'_>,
    definition: DefId,
    arguments: ConstArguments,
    span: Span,
) -> &'c [semantic_types::TypeId] {
    let given = DeclarationCopy {
        definition,
        arguments,
    };
    if checker.internals.open_struct_fields.contains_key(&given) {
        return &checker.internals.open_struct_fields[&given];
    }
    let tree = &checker.modules[definition.module.index()].ast;
    let ast::Decl::Type(written) = tree.decls[tree.declaration(definition.index)] else {
        unreachable!("only a struct declaration is given type arguments");
    };
    let ast::Type::Struct { fields } = tree.types[written.ty] else {
        unreachable!("only a struct declaration is given type arguments");
    };
    let written: Vec<Id<ast::Type>> = (tree.children.lists.items(fields))
        .map(|field| tree.children.fields[field].ty)
        .collect();
    let checking = Checking {
        definition,
        arguments,
        used_at: span,
        records_facts: false,
    };
    let fields = checker.checking_in(checking, |checker| {
        (written.into_iter())
            .map(|ty| checker.resolve_value_type(ty))
            .collect()
    });
    checker
        .internals
        .open_struct_fields
        .entry(given)
        .or_insert(fields)
}
