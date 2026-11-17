//! Reading and checking arrays. The parser's tree dump and the checker's
//! test program are those crates' own, so these build what they need.

use arena::Id;
use syntax::ast;

use crate::testing::Tree;
use crate::testing::bodies;
use crate::testing::messages;
use crate::testing::parse_text;
use crate::testing::with_checker;

/// The messages of the errors in `text`.
/// The written type `ty` with `[N]` for each array, `&[]` for each slice
/// and the innermost type's text.
fn shape(tree: &Tree, ty: Id<ast::Type>) -> String {
    match tree.types[ty] {
        ast::Type::Feature(ast::FeatureType::Array(ast::ArrayType { length, element })) => {
            format!("[{}]{}", tree.file.slice(length), shape(tree, element))
        }
        ast::Type::Slice { element, .. } => format!("&[]{}", shape(tree, element)),
        _ => tree.file.slice(tree.types.span(ty)).to_string(),
    }
}

/// `[N]T` is a type wherever a type goes, an array's element too, and
/// where a value could go when no `{` follows; `[N]T{ }` and `[_]T{ }` are
/// literals, whose entries may spread.
#[test]
fn parses_types_and_literals() {
    let text = "fn f(a: [4]u8, b: [2][3]i32, c: &[][2]u8) {
        g([4]i32);
        [2]i32{ 1, 2 };
        [_]u8{ 1, (b)..., a... };
    }";
    let (tree, errors) = parse_text(text);
    assert!(errors.is_empty(), "{errors:?}");
    let decl = tree.declarations().next().unwrap();
    let ast::Decl::Function(function) = tree.decls[decl] else {
        panic!("not a function")
    };
    let parameters: Vec<String> = (tree.children.lists.items(function.signature.parameters))
        .map(|parameter| shape(&tree, tree.children.params[parameter].ty))
        .collect();
    assert_eq!(parameters, ["[4]u8", "[2][3]i32", "&[][2]u8"]);
    let mut literals = Vec::new();
    for id in tree.exprs.ids() {
        match tree.exprs[id] {
            ast::Expr::Type(ty) => literals.push(format!("type {}", shape(&tree, ty))),
            ast::Expr::Feature(ast::FeatureExpr::ArrayLiteral(ast::ArrayLiteral {
                ty,
                entries,
            })) => {
                let ty = match ty {
                    ast::ArrayLiteralType::Written(ty) => shape(&tree, ty),
                    ast::ArrayLiteralType::Counted { element } => {
                        format!("[_]{}", shape(&tree, element))
                    }
                };
                let entries: Vec<&str> = (tree.children.lists.items(entries))
                    .map(|entry| match tree.exprs[entry] {
                        ast::Expr::Spread(_) => "spread",
                        _ => "value",
                    })
                    .collect();
                literals.push(format!("{ty} {}", entries.join(" ")));
            }
            _ => {}
        }
    }
    assert_eq!(
        literals,
        [
            "type [4]i32",
            "[2]i32 value value",
            "[_]u8 value spread spread"
        ]
    );
}

/// A `[]T` alone has no fixed size, and is read whole, so the parser stays
/// in step and reports the next one too; an element so reported is the
/// one mistake.
#[test]
fn type_errors() {
    for (text, want) in [
        ("fn f(a: [n]i32) {}", &["expected array length"][..]),
        (
            "fn f(a: []i32) {}",
            &["`[]i32` is any number of `i32`s, so it has no fixed size"],
        ),
        (
            "fn f(a: [][]i32) {}",
            &["`[]i32` is any number of `i32`s, so it has no fixed size"],
        ),
        (
            "fn f(a: []i32, b: []u8) {}",
            &[
                "`[]i32` is any number of `i32`s, so it has no fixed size",
                "`[]u8` is any number of `u8`s, so it has no fixed size",
            ],
        ),
        ("fn f() { [x]i32{}; }", &["expected array length"]),
        (
            "fn f() { []i32{}; }",
            &["`[]i32` is any number of `i32`s, so it has no fixed size"],
        ),
        ("fn f() { [_]i32; }", &["expected `{`, found `;`"]),
    ] {
        assert_eq!(messages(text), want, "{text}");
    }
}

/// A `;` for a `,` in a literal's `{ }` is repaired with one error per
/// list; a `;` after an unclosed literal ends the statement, so its `}` is
/// missing.
#[test]
fn separators() {
    let text = "fn f() { let v = [2]i32{1; 2}; }";
    let (_, errors) = parse_text(text);
    assert_eq!(errors.len(), 1, "{errors:?}");
    assert_eq!(errors[0].message, "expected `,` between items, found `;`");
    let mut fixed = text.to_string();
    for fix in errors[0].notes[0].fixes().iter().rev() {
        fixed.replace_range(fix.span.range(), &fix.new_text);
    }
    assert_eq!(fixed, "fn f() { let v = [2]i32{1, 2}; }");
    assert_eq!(
        messages("fn f() {\n    let v = [3]i32{4, 5, 6;\n    return;\n}"),
        ["expected `}`, found `;`"]
    );
}

/// `[_]T` takes its length from its entries, a spread's too; `[N]T` needs
/// that many.
#[test]
fn literals() {
    let (bodies, reported) = bodies(
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
    let (bodies, reported) = bodies(
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
    let ((), reported) = with_checker(text, |checker, functions| {
        let (definition, name) = functions[0];
        checker.function_type(definition, name);
    });
    assert_eq!(
        reported,
        [(
            "T0043",
            "`const` values whose type names a type parameter are not supported yet".to_string(),
            "[2]T".to_string()
        )]
    );
}
