//! Arrays: the checker's file for `[N]T` and the array literals `[N]T{ ... }`
//! and `[_]T{ ... }`, as the parser reads them. The checker gives a
//! `Type::Array` and an `ExprKind::ArrayLiteral`, so the phases after it know
//! nothing of this file. A literal's entries and their `...` spreads are
//! checked in `expression/literals.rs`, as a call to a `...` parameter checks
//! them too; slices in `slices.rs`. Indexing an array or a slice and their
//! `.len` are here; which values take them, the dispatch decides
//! (`expression/places.rs`).

use crate::Checker;
use crate::builder;
use arena::Id;
use arena::List;
use diagnostics::suggest::nearest_name;
use model::constant::Value;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;
use tree::ast::Ident;

/// The type `[length]element`, written at `span`: the length is a whole
/// number without a suffix that an array can hold.
pub(crate) fn resolve_array_type(
    checker: &mut Checker<'_>,
    length: Span,
    element: Id<ast::Type>,
    span: Span,
) -> semantic_types::TypeId {
    let literal = literal::numeric::split(checker.module().ast.file.slice(length));
    let value = literal.integer_value();
    if !literal.suffix.is_empty() || literal.is_float() {
        let message = "an array length is a whole number without a suffix";
        return checker.error(diagnostics::INVALID_TYPE, message, length);
    }
    if !checker.length_fits(&value, length) {
        return semantic_types::TypeId::INVALID;
    }
    let element = checker.resolve_value_type(element);
    let length = checker.types.array_length(value[0]);
    checker.array_type(element, length, span)
}

/// `[N]T{ entries }`, written at `span`, or `[_]T{ entries }`, whose
/// length is the number of values its entries give: each entry a `T`, or
/// a spread of a range, an array or a slice (`count_spread`). They must
/// give exactly its length. A slice, or a range whose count is not known
/// now, is counted when the program runs, so its literal needs a written
/// length, which the entries known now must not already pass.
pub(crate) fn check_array_literal(
    builder: &mut builder::ThirBuilder<'_, '_>,
    ty: ast::ArrayLiteralType,
    entries: List<ast::Expr>,
    span: Span,
) -> thir::Expr {
    let tree = &builder.checker.module().ast;
    let (element, length, written) = match ty {
        ast::ArrayLiteralType::Counted { element } => {
            // `[_]T`, from its `[` to its element's end
            let written = Span {
                start: span.start,
                end: tree.types.span(element).end,
            };
            (builder.checker.resolve_value_type(element), None, written)
        }
        ast::ArrayLiteralType::Written(written) => {
            let span = tree.types.span(written);
            match builder.checker.resolve_value_type(written) {
                ty if ty == semantic_types::TypeId::INVALID => {
                    (semantic_types::TypeId::INVALID, None, span)
                }
                ty => match builder.checker.types[ty].elements() {
                    Some((element, Some(length))) => (element, Some((ty, length)), span),
                    _ => unreachable!(
                        "an array literal's written type is an array type with its length written"
                    ),
                },
            }
        }
    };
    let entries = tree.children.lists.items(entries);
    let (parts, count) = builder.check_entries(entries, element, None);
    let Some(count) = count.filter(|_| element != semantic_types::TypeId::INVALID) else {
        return thir::Expr::INVALID;
    };
    let Some(ty) = builder.array_literal_type(written, element, length, count, span) else {
        return thir::Expr::INVALID;
    };
    thir::Expr {
        ty,
        kind: thir::ExprKind::ArrayLiteral(builder.body.children.lists.add(&parts)),
    }
}

/// `value.len`, written `value.field`, where `viewed` is the type of `value`
/// or of what it points to: an array's, a slice's or a `&str`'s length, its
/// only field. An array's type gives it, so the array is not read where
/// computing it runs nothing.
pub(crate) fn check_length(
    builder: &mut builder::ThirBuilder<'_, '_>,
    value: Id<thir::Expr>,
    viewed: semantic_types::TypeId,
    field: Ident,
) -> thir::Expr {
    // shortcut: `len` as a field until the language can write it as a free
    // function over any array; then move it there.
    let text = field.text(&builder.checker.module().ast.file);
    if text != "len" {
        let viewed = builder.checker.type_text(viewed);
        let message = format!("`{viewed}` has no field `{text}`");
        let meant = nearest_name(text, [("len", 0)]).map(str::to_string);
        builder
            .checker
            .report_unknown(diagnostics::UNKNOWN_FIELD, field.span, message, meant);
        return thir::Expr::INVALID;
    }
    // An array's length is folded only where computing the array runs
    // nothing: a variable or constant, its fields and dereferences, and
    // elements at constant indexes, which are checked here. A call, a
    // run-time index or a literal's parts still run, and may fail.
    let mut part = value;
    let runs_nothing = loop {
        match builder.body.exprs[part].kind {
            thir::ExprKind::Variable(_) | thir::ExprKind::Constant(_) => break true,
            thir::ExprKind::Field { value, .. } | thir::ExprKind::Dereference(value) => {
                part = value;
            }
            thir::ExprKind::Index { array, index }
                if matches!(builder.body.exprs[index].kind, thir::ExprKind::Constant(_))
                    && !builder.checker.types[builder.body.exprs[array].ty].is_slice_shaped() =>
            {
                part = array;
            }
            _ => break false,
        }
    };
    // An array's length is its type's constant, each copy's when only its
    // type is known; a slice's or a `&str`'s is known only when the program
    // runs: read from the value, so through the reference to it, as an
    // array's that must still be computed.
    if runs_nothing
        && let semantic_types::Type::Array { length, .. } = &builder.checker.types[viewed]
    {
        return crate::constant_expr(Some(length.clone()));
    }
    let value = builder.through_pointer(value, builder.body.exprs.span(value));
    thir::Expr {
        ty: builder.checker.types.usize,
        kind: thir::ExprKind::Length(value),
    }
}

/// `array[index]`, written at `span`: the element at the checked integer
/// `index` of `array`, a checked array or slice in a place. A constant index
/// must be inside an array; a slice's must be at least 0 and fit a `u64`,
/// and is checked when the program runs.
pub(crate) fn check_element(
    builder: &mut builder::ThirBuilder<'_, '_>,
    array: Id<thir::Expr>,
    index: Id<thir::Expr>,
) -> thir::Expr {
    let ty = builder.body.exprs[array].ty;
    let (element, length) = builder.checker.types[ty]
        .elements()
        .expect("only an array or a slice is indexed");
    let Some(integer) = builder.integer_index(index) else {
        return thir::Expr::INVALID;
    };
    // A negative constant, or one past `u64`, is outside every slice.
    if let thir::ExprKind::Constant(Value::Integer(value)) = &builder.body.exprs[index].kind
        && bigint::to_u64(integer.bits, integer.signed, value)
            .is_none_or(|at| length.is_some_and(|length| at >= length))
    {
        let index_span = builder.body.exprs.span(index);
        let written = builder.checker.module().ast.file.slice(index_span);
        let viewed = builder.checker.type_text(ty);
        let message = match length {
            Some(length) => {
                format!("index `{written}` is outside `{viewed}`, whose length is {length}")
            }
            None => format!("index `{written}` is outside every `{viewed}`"),
        };
        builder
            .checker
            .error(diagnostics::INVALID_OPERATION, message, index_span);
        return thir::Expr::INVALID;
    }
    // An element is read through the array's address, so from a place.
    builder.make_place(array);
    thir::Expr {
        ty: element,
        kind: thir::ExprKind::Index { array, index },
    }
}

#[cfg(test)]
mod tests {
    //! Checking arrays: the literals' lengths and entries, temporaries, and
    //! `const` values whose type names a type parameter.

    use crate::testing;

    /// `[_]T` takes its length from its entries, a spread's too; `[N]T` needs
    /// that many.
    #[test]
    fn literals() {
        let (bodies, reported) = testing::bodies(
            "fn f(a: [2]i32) {
                let b = [_]i32{ 1, a..., 4 };
                let c = [3]i32{ 1, 2 };
            }",
        );
        assert_eq!(
            reported,
            [(
                "T0001",
                "`[3]i32` needs 3 values, found 2".to_string(),
                "[3]i32{ 1, 2 }".to_string()
            )]
        );
        assert_eq!(
            bodies,
            ["    let b.1 = [4]i32{ 1:i32, (a.0)..., 4:i32 }\n    let c.2 = invalid\n"]
        );
    }

    /// A temporary is stored where it is computed, and each one a `let`'s
    /// value makes is marked to last with it; one in a plain statement is not.
    /// An array's temporary is indexed and looped over as a variable is.
    #[test]
    fn temporaries() {
        let (bodies, reported) = testing::bodies(
            "fn make() -> [2]i32 { return [2]i32{1, 2}; }
            fn id(r: &mut i32) -> &mut i32 in r { return r; }
            fn sum(values: &[]i32) -> i32 { return values[0]; }
            fn f() {
                let total = sum(&make());
                let kept = &make();
                let first = make()[0];
                let mut x = 1;
                id(&mut x).* = 2;
                for v in &make() { }
            }",
        );
        assert!(reported.is_empty(), "{reported:?}");
        assert_eq!(
            bodies[3],
            "    let total.1 = call f2(&(kept _.0 = call f0():[2]i32)):i32
    let kept.3 = &(kept _.2 = call f0():[2]i32)
    let first.5 = (kept _.4 = call f0():[2]i32)[at 0:u64]
    let mut x.6 = 1:i32
    (_.7 = call f1(&mut x.6):&mut i32).* = 2:i32
    {
        let _.9 = &(kept _.8 = call f0():[2]i32)
        for _.10 in 0:u64..2:u64 by 1:u64 {
            let v.11 = &_.9.*[at _.10]
        }
    }
"
        );
    }

    /// A `const` value's type may name a type parameter through an array:
    /// each call gives it the type. Only one before the value, as a call
    /// gives those first.
    #[test]
    fn const_array_of_a_type_parameter() {
        let check = |text| {
            let ((), reported) = testing::with_functions(text, |checker, functions| {
                let (definition, name) = functions[0];
                checker.function_type(definition, name);
            });
            reported
        };
        assert_eq!(check("fn Typed(const T: type, const a: [2]T) {}"), []);
        assert_eq!(
            check("fn Typed(const a: [2]T, const T: type) {}"),
            [(
                "P0005",
                "a `const` value's type names only the type parameters before it".to_string(),
                "[2]T".to_string()
            )]
        );
    }
}
