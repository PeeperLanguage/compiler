//! Arrays: the checker's file for `[N]T` and the array literals `[N]T{ ... }`
//! and `[_]T{ ... }`, as the parser reads them. The checker gives a
//! `Type::Array` and an `ExprKind::ArrayLiteral`, so the phases after it know
//! nothing of this file. A literal's entries and their `...` spreads are
//! checked in `expression/literals.rs`, as a call to a `...` parameter checks
//! them too; slices (read after `&`) and indexing in `expression/places.rs`.

use crate::Checker;
use crate::builder;
use arena::Id;
use arena::List;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;

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
    checker.array_type(element, value[0], span)
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
                ty => match builder.checker.types[ty] {
                    semantic_types::Type::Array {
                        element, length, ..
                    } => (element, Some((ty, length)), span),
                    _ => unreachable!("an array literal's written type is an array type"),
                },
            }
        }
    };
    let entries = tree.children.lists.items(entries);
    let (parts, known, later) = builder.check_entries(entries, element, None);
    let Some(known) = known.filter(|_| element != semantic_types::TypeId::INVALID) else {
        return thir::Expr::INVALID;
    };
    let Some(ty) = builder.array_literal_type(written, element, length, (known, later), span)
    else {
        return thir::Expr::INVALID;
    };
    thir::Expr {
        ty,
        kind: thir::ExprKind::ArrayLiteral(builder.body.children.lists.add(&parts)),
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

    /// A `const` value's type is read again at each call, which cannot know a
    /// type parameter yet, so one naming it through an array is refused.
    #[test]
    fn const_array_of_a_type_parameter() {
        let text = "fn Typed(const T: type, const a: [2]T) {}";
        let ((), reported) = testing::with_functions(text, |checker, functions| {
            let (definition, name) = functions[0];
            checker.function_type(definition, name);
        });
        assert_eq!(
            reported,
            [(
                "T0043",
                "`const` values whose type names a type parameter are not supported yet"
                    .to_string(),
                "[2]T".to_string()
            )]
        );
    }
}
