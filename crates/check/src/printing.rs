//! Printing: `print(values)` and `println(values)` checked into calls of the
//! prelude's print functions, so no later phase knows of them.

use crate::builder;
use model::constant;
use model::semantic_types;
use model::thir;
use source::Span;
use std::num;
use tree::ast;

use arena::Id;
use arena::List;
use bigint::Limbs;
use model::Callee;
use model::ConstArguments;
use model::DeclarationCopy;

/// `print(values)`, or `println(values)` when `newline`: each value of a
/// type `printable` takes, with a space between each. A plain string
/// literal is printed as a `&cstr`, though it is not one anywhere else.
// shortcut: a built-in until a library function takes any number of
// values of any printable type; then delete it.
/// The calls that print `values`, a space between each, then a newline
/// when `newline`, then the one that writes out what they kept back, added
/// to `statements`. `None` at the first that cannot be made, reported.
fn print_values(
    builder: &mut builder::ThirBuilder<'_, '_>,
    statements: &mut Vec<Id<thir::Stmt>>,
    values: &[Id<thir::Expr>],
    newline: bool,
    span: Span,
) -> Option<()> {
    for (index, &value) in values.iter().enumerate() {
        if index > 0 {
            statements.push(print_text(builder, b" ", span)?);
        }
        print_value(builder, statements, value, span)?;
    }
    if newline {
        statements.push(print_text(builder, b"\n", span)?);
    }
    statements.push(library_call(builder, "Flush", Vec::new(), span)?);
    Some(())
}

/// The calls that print `value`, added to `statements`; `None` when one
/// cannot be made, reported.
fn print_value(
    builder: &mut builder::ThirBuilder<'_, '_>,
    statements: &mut Vec<Id<thir::Stmt>>,
    mut value: Id<thir::Expr>,
    span: Span,
) -> Option<()> {
    // A reference prints what it refers to. A loop, not recursion:
    // references nest as deep as the source writes.
    while let semantic_types::Type::Reference { .. } =
        builder.checker.types[builder.body.exprs[value].ty]
    {
        value = builder.through_pointer(value, span);
    }
    let ty = builder.body.exprs[value].ty;
    let i32_constant = |value: u32| thir::Expr {
        ty: semantic_types::TypeId::I32,
        kind: thir::ExprKind::Constant(constant::Value::Integer(Limbs::new(
            32,
            &[u64::from(value)],
        ))),
    };
    let read = |builder: &builder::ThirBuilder<'_, '_>| builder.body.exprs[value].clone();
    match builder.checker.types[ty] {
        // The runtime reads an integer from memory (`widened`): where it
        // is kept or referred to, else a temporary.
        semantic_types::Type::Integer(integer) => {
            let limbs = widened(builder, value, integer);
            builder.make_place(limbs);
            let address = thir::Expr {
                ty: semantic_types::TypeId::RAWPTR,
                kind: thir::ExprKind::Address(limbs),
            };
            let arguments = vec![
                i32_constant(u32::from(integer.signed)),
                i32_constant(integer.bits.div_ceil(64)),
                address,
            ];
            statements.push(library_call(builder, "PrintInt", arguments, span)?);
        }
        semantic_types::Type::Float(float) => {
            let (double, bits) = match float {
                semantic_types::Float::F32 => {
                    let own = builder.body.exprs.span(value);
                    let conversion = (builder.checker.types)
                        .conversion(semantic_types::TypeId::F32, semantic_types::TypeId::F64);
                    let widened = builder.convert(
                        read(builder),
                        own,
                        semantic_types::TypeId::F64,
                        conversion,
                    );
                    (widened.expect("an `f32` converts to an `f64`"), 32)
                }
                semantic_types::Float::F64 => (read(builder), 64),
            };
            let arguments = vec![double, i32_constant(bits)];
            statements.push(library_call(builder, "PrintFloat", arguments, span)?);
        }
        semantic_types::Type::Bool => {
            let (print_true, print_false) = (
                print_text(builder, b"true", span)?,
                print_text(builder, b"false", span)?,
            );
            let then = builder.add_block(span, |_| vec![print_true]);
            let else_block = builder.add_block(span, |_| vec![print_false]);
            let otherwise = builder.body.add(thir::Stmt::Block(else_block), span);
            let choice = thir::Stmt::If {
                condition: value,
                then,
                otherwise: Some(otherwise),
            };
            statements.push(builder.body.add(choice, span));
        }
        semantic_types::Type::CString => {
            let arguments = vec![read(builder)];
            statements.push(library_call(builder, "PrintText", arguments, span)?);
        }
        // Its bytes' address and its length, read from one variable: a
        // temporary is worked out by each expression that holds it.
        semantic_types::Type::Str => {
            let text = match builder.body.exprs[value].kind {
                thir::ExprKind::Variable(_) => value,
                _ => kept(builder, statements, value, span),
            };
            let part = |index, ty| thir::Expr {
                ty,
                kind: thir::ExprKind::Field { value: text, index },
            };
            let arguments = vec![
                part(0, semantic_types::TypeId::RAWPTR),
                part(1, builder.checker.types.usize),
            ];
            statements.push(library_call(builder, "PrintBytes", arguments, span)?);
        }
        semantic_types::Type::RawPointer { .. } => {
            let arguments = vec![read(builder)];
            statements.push(library_call(builder, "PrintPointer", arguments, span)?);
        }
        _ => unreachable!("`printable` takes only numbers, `bool`, `&cstr`, `&str` and pointers"),
    }
    Some(())
}

/// `value`, an integer of the type `integer`, as the runtime reads one of
/// any width: 64-bit limbs, least significant first, as it is in memory on
/// every target, all little-endian. So it is widened to a multiple of 64
/// bits.
fn widened(
    builder: &mut builder::ThirBuilder<'_, '_>,
    value: Id<thir::Expr>,
    integer: semantic_types::Integer,
) -> Id<thir::Expr> {
    let bits = integer.bits.next_multiple_of(64);
    if bits == integer.bits {
        return value;
    }
    let wide =
        (builder.checker.types).intern(semantic_types::Type::Integer(semantic_types::Integer {
            bits,
            ..integer
        }));
    // At its own span, where an error in working it out is reported.
    let (narrow, span) = (
        builder.body.exprs[value].clone(),
        builder.body.exprs.span(value),
    );
    let conversion = builder.checker.types.conversion(narrow.ty, wide);
    let widened = builder.convert(narrow, span, wide, conversion);
    builder
        .body
        .add(widened.expect("an integer converts to a wider one"), span)
}

/// A hidden variable set to `value` by a statement added to `statements`.
fn kept(
    builder: &mut builder::ThirBuilder<'_, '_>,
    statements: &mut Vec<Id<thir::Stmt>>,
    value: Id<thir::Expr>,
    span: Span,
) -> Id<thir::Expr> {
    let ty = builder.body.exprs[value].ty;
    let variable = builder.add_hidden_variable(ty, false, span);
    let set = thir::Stmt::Let {
        variable,
        value: Some(value),
    };
    statements.push(builder.body.add(set, span));
    let read = thir::Expr {
        ty,
        kind: thir::ExprKind::Variable(variable),
    };
    builder.body.add(read, span)
}

/// The statement that prints `text` (`library_call`).
fn print_text(
    builder: &mut builder::ThirBuilder<'_, '_>,
    text: &[u8],
    span: Span,
) -> Option<Id<thir::Stmt>> {
    let text = thir::Expr {
        ty: semantic_types::TypeId::CSTR,
        kind: thir::ExprKind::Constant(constant::Value::Bytes(text.into())),
    };
    library_call(builder, "PrintText", vec![text], span)
}

/// The statement that calls the function `name` of the prelude with
/// `arguments`. `None` after reporting that the prelude does not declare
/// it with a parameter of each argument's type and no result, or when its
/// declaration has an error, reported there.
fn library_call(
    builder: &mut builder::ThirBuilder<'_, '_>,
    name: &str,
    arguments: Vec<thir::Expr>,
    span: Span,
) -> Option<Id<thir::Stmt>> {
    // The prelude's own, not a function the program names the same; the
    // prelude itself is checked with no other.
    let checker = &mut *builder.checker;
    let prelude = checker.prelude().unwrap_or(checker.scope());
    let declared = match prelude.get(None, name) {
        Some(thir::Symbol::Function(definition)) => {
            Some((definition, checker.function_type(definition, span)))
        }
        _ => None,
    };
    let types = &checker.types;
    let as_called = declared.filter(|&(_, signature)| match &types[signature] {
        semantic_types::Type::Function {
            parameters, result, ..
        } => {
            types.same(*result, semantic_types::TypeId::VOID)
                && parameters.len() == arguments.len()
                && (parameters.iter().zip(&arguments)).all(|(&parameter, argument)| {
                    types.conversion(argument.ty, parameter) == semantic_types::Conversion::Same
                })
        }
        _ => false,
    });
    let Some((definition, signature)) = as_called else {
        if declared.is_none_or(|(_, signature)| signature != semantic_types::TypeId::INVALID) {
            let message =
                format!("the `core` library does not declare `{name}` as `print` calls it");
            checker.error(diagnostics::UNDEFINED_NAME, message, span);
        }
        return None;
    };
    let function = DeclarationCopy {
        definition,
        arguments: ConstArguments::NONE,
    };
    let arguments: Vec<num::NonZeroU32> = (arguments.into_iter())
        .map(|argument| builder.body.add(argument, span).into())
        .collect();
    let call = thir::Expr {
        ty: semantic_types::TypeId::VOID,
        kind: thir::ExprKind::Call {
            callee: Callee::Function(function),
            signature,
            arguments: builder.body.children.lists.add(&arguments),
        },
    };
    let call = builder.body.add(call, span);
    Some(builder.body.add(thir::Stmt::Expression(call), span))
}

/// Whether `print` writes a value of the type `ty`: a number, `bool`,
/// `&cstr`, `&str` or raw pointer, or a reference to one; `None` when each
/// copy decides, for a type parameter or a reference to one. A struct or
/// array prints only once a trait or interface lets its type say how.
fn printable(types: &semantic_types::Types, mut ty: semantic_types::TypeId) -> Option<bool> {
    // A loop, not recursion: references nest as deep as the source writes.
    loop {
        match &types[ty] {
            semantic_types::Type::Integer(_)
            | semantic_types::Type::Float(_)
            | semantic_types::Type::Bool
            | semantic_types::Type::CString
            | semantic_types::Type::Str
            | semantic_types::Type::RawPointer { .. } => return Some(true),
            semantic_types::Type::Reference { target, .. } => ty = *target,
            _ if types[ty].is_decided_per_copy() => return None,
            _ => return Some(false),
        }
    }
}

/// A block of calls of the prelude's print functions, one for each
/// value by its type, then one that writes out what they kept back.
pub(crate) fn check_print(
    builder: &mut builder::ThirBuilder<'_, '_>,
    newline: bool,
    values: List<ast::Expr>,
    span: Span,
) -> thir::Expr {
    let tree = &builder.checker.module().ast;
    let mut checked = Vec::with_capacity(values.len());
    let mut failed = false;
    for value in tree.children.lists.items(values) {
        let span = tree.exprs.span(value);
        let expr = builder.check_expr(value, None);
        let ty = expr.ty;
        let types = &builder.checker.types;
        // Each copy prints a value of a type it decides its own way; a
        // generic body's own check, which never runs, leaves it out.
        let Some(printable) = printable(types, ty) else {
            continue;
        };
        // On an error the others are still checked, for their own.
        if !printable {
            if ty != semantic_types::TypeId::INVALID {
                let message = format!("`{}` cannot be printed", builder.checker.type_text(ty));
                (builder.checker).error(diagnostics::INVALID_OPERATION, message, span);
            }
            failed = true;
            continue;
        }
        let mut printed = builder.body.add(expr, span);
        // Printing reads a value, never takes it: a `&mut` reference in
        // a place, the one printable value that moves, is lent as `&`.
        if let semantic_types::Type::Reference {
            mutable: true,
            target,
        } = types[ty]
            && builder
                .start_variable(builder.place_root(printed).0)
                .is_some()
        {
            let reference = builder.body.exprs[printed].clone();
            let shared = builder.checker.reference_type(false, target);
            let lent = builder.reborrow(reference, span, shared);
            printed = builder.body.add(lent, span);
        }
        checked.push(printed);
    }
    if failed {
        return thir::Expr::INVALID;
    }
    let mut written = None;
    let block = builder.add_block(span, |builder| {
        let mut statements = Vec::new();
        // Every value is worked out, in order, before any is written:
        // a value's code may print too, and a reference among them is
        // in use until it is written. So each is kept in a hidden
        // variable; one value alone, or a constant, is as good as it is.
        let alone = checked.len() == 1;
        for value in &mut checked {
            let thir::Expr { ty, kind } = &builder.body.exprs[*value];
            if matches!(kind, thir::ExprKind::Constant(_)) {
                continue;
            }
            // An integer is kept even alone, as the runtime reads it:
            // from memory, which must not be the program's own variable.
            if let semantic_types::Type::Integer(integer) = builder.checker.types[*ty] {
                *value = widened(builder, *value, integer);
            } else if alone {
                continue;
            }
            *value = kept(builder, &mut statements, *value, span);
        }
        written = print_values(builder, &mut statements, &checked, newline, span);
        statements
    });
    if written.is_none() {
        return thir::Expr::INVALID; // reported
    }
    thir::Expr {
        ty: semantic_types::TypeId::VOID,
        kind: thir::ExprKind::Block(block),
    }
}
