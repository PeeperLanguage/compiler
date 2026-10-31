use model::DefId;
use model::ast;
use model::thir::Body;
use source::SourceFile;

use crate::testing::Reported;
use crate::testing::reported;
use crate::testing::with_checker;

/// The THIR of every function body of `text`, as text, and the
/// diagnostics.
fn bodies(text: &str) -> (Vec<String>, Vec<Reported>) {
    let ((bodies, declared), types, reported) = with_checker(text, |checker, main| {
        let tree = &checker.modules[main.index()].ast;
        let declarations: Vec<_> = tree.declarations().collect();
        let declared = declarations.len();
        let mut bodies = Vec::new();
        for (index, declaration) in declarations.into_iter().enumerate() {
            let ast::Decl::Function(function) = tree.decls[declaration] else {
                continue;
            };
            let definition = DefId {
                module: main,
                index: index as u32,
            };
            let mut body = Body::default();
            let checked = checker.check_body(definition, function.name.span, &mut body, false);
            if checked && !body.has_errors {
                bodies.push(body);
            }
        }
        (bodies, declared)
    });
    let file = SourceFile::new("main.peep", text);
    // A called function's name, by its place among the declarations.
    let names: Vec<String> = (0..declared).map(|index| format!("f{index}")).collect();
    let name = |function: DefId| names[function.index as usize].as_str();
    let shown = bodies
        .iter()
        .map(|body| body.text(&types, &file, name))
        .collect();
    (shown, reported)
}

#[test]
fn statements_and_operators() {
    let (bodies, reported) = bodies(
        "fn f(a: i32, mut b: i64) -> i64 {
                let c = a + 1;
                b = b + c;
                const K: i64 = 2 * 3;
                if a < 0 { return K; } else if !(a == 1) { b = -b; } else { let c = true; }
                let d: u8;
                return b << 2u8;
            }",
    );
    assert!(reported.is_empty(), "{reported:?}");
    assert_eq!(
        bodies,
        ["    let c.2 = (a.0 Add 1:i32)
    b.1 = (b.1 Add convert(c.2):i64)
    if (a.0 LessThan 0:i32) {
        return 6:i64
    } else if (LogicalNot (a.0 EqualTo 1:i32)) {
        b.1 = (Negate b.1)
    } else {
        let c.3 = true
    }
    let d.4
    return (b.1 ShiftLeft 2:u8)
"]
    );
}

/// A range's literal start takes the end's type; its variables are
/// declared in the body. Its step is the ends' type made unsigned, 1
/// when not written.
#[test]
fn loops() {
    let (bodies, reported) = bodies(
        "fn f(n: i64) {
                for { break; }
                for n > 0 { continue; }
                for i in 0..n { let j = i; }
                for 0..3 { }
                for k in 1..=n { }
                for i, m in n..0:2 { }
            }",
    );
    assert!(reported.is_empty(), "{reported:?}");
    assert_eq!(
        bodies,
        ["    for {
        break
    }
    for (n.0 GreaterThan 0:i64) {
        continue
    }
    for i.1 in 0:i64..n.0 by 1:u64 {
        let j.2 = i.1
    }
    for 0:i32..3:i32 by 1:u32 {
    }
    for k.3 in 1:i64..=n.0 by 1:u64 {
    }
    for i.4, m.5 in n.0..0:i64 by 2:u64 {
    }
"]
    );
}

#[test]
fn names_shadow_until_their_block_ends() {
    let (bodies, reported) = bodies(
        "fn f(x: i32) {
                { let x = x + 1; let y = x; }
                { let x = true; let z = x; }
                let w = x;
                { const x = 2u8; let v = x; }
            }",
    );
    assert!(reported.is_empty(), "{reported:?}");
    assert_eq!(
        bodies,
        ["    {
        let x.1 = (x.0 Add 1:i32)
        let y.2 = x.1
    }
    {
        let x.3 = true
        let z.4 = x.3
    }
    let w.5 = x.0
    {
        let v.6 = 2:u8
    }
"]
    );
}

/// Types and values share names, and a value can't take a type's name,
/// declared or built-in: the name stays the type's.
#[test]
fn types_and_values_share_names() {
    let (_, found) = bodies(
        "type Meters = i64;
            fn f() {
                let Meters = 1;
                let far: Meters = 2;
                let i32 = 3;
                let near: i32 = 4;
                let n = i32;
            }",
    );
    let taken = "is a type; a value can't take its name";
    let expected = reported(&[
        ("T0003", &format!("`Meters` {taken}"), "Meters"),
        ("T0003", &format!("`i32` {taken}"), "i32"),
        ("T0004", "`i32` is a type, not a value", "i32"),
    ]);
    assert_eq!(found, expected);
}

/// A block declares each name once; the parameters are in the body's
/// outermost block. The first declaration keeps the name.
#[test]
fn a_block_declares_a_name_once() {
    let (shown, found) = bodies(
        "fn f(a: i32, a: i64) {
                let b = 1;
                let b = true;
                const C = 1;
                let C = 2;
                let a = 3;
                let c: i32 = b;
            }",
    );
    let again = "is already declared in this block";
    let expected = reported(&[
        ("T0003", &format!("`a` {again}"), "a"),
        ("T0003", &format!("`b` {again}"), "b"),
        ("T0003", &format!("`C` {again}"), "C"),
        ("T0003", &format!("`a` {again}"), "a"),
    ]);
    assert_eq!(found, expected);
    assert!(shown.is_empty(), "{shown:?}");
}

#[test]
fn constants_fold() {
    let (bodies, reported) = bodies(
        "fn g() {
                let small: i8 = -128;
                let wrapped = 127i8 + 1;
                const N: u8 = 200;
                let wide: i16 = N;
                let precise: f32 = 0.1 + 0.2;
                let whole: f64 = 1;
                let big = 1u256 << 100u8;
                let no = !(1 < 2);
                let parenthesized = (2 + 3) * -(4);
            }",
    );
    assert!(reported.is_empty(), "{reported:?}");
    // Folded in `f32`: in `f64` it would not be an `f32` and would show
    // as 0.30000000000000004.
    let precise = 0.1f32 + 0.2f32;
    assert_eq!(
        bodies,
        [format!(
            "    let small.0 = -128:i8
    let wrapped.1 = -128:i8
    let wide.2 = 200:i16
    let precise.3 = {precise}:f32
    let whole.4 = 1:f64
    let big.5 = {}:u256
    let no.6 = false
    let parenthesized.7 = -20:i32
",
            1u128 << 100
        )]
    );
}

#[test]
fn errors_are_located() {
    let (_, found) = bodies(
        "fn f(p: i32) -> i32 {
                let a: u8 = 300;
                let b: u8 = -1;
                let c: i8 = -(128);
                let d = 1i32 + 2i64;
                let e = true + false;
                let g = 1 / 0;
                let h = 1u8 << 8;
                let i = 1 << 1.5;
                let u = p / 0;
                let v = p << 40u8;
                let j = missing;
                let k = utils;
                let l = helper;
                let m = 1;
                m = 3;
                let w = m(1);
                let y = utils::Nope;
                let z = nothing(missing);
                const C = 1; C = 2;
                const D: i32 = p;
                let n;
                if 1 {}
                let s = \"a\";
                break;
                let q = 5f16;
                let r: f64 = 0xff;
                let t = -true;
                { let m = true; }
                alloc(1);
                let .{ first, second as renamed } = 1;
                let after = first + renamed;
                return;
            }
            fn helper() { return 1; }
            fn calls_back(callback: fn(i32)) { callback(1); }",
    );
    let expected = reported(&[
        ("L0003", "literal `300` does not fit `u8`", "300"),
        ("L0003", "literal `-1` does not fit `u8`", "-1"),
        ("L0003", "literal `128` does not fit `i8`", "128"),
        (
            "T0001",
            "mismatched types: expected `i32`, found `i64`",
            "2i64",
        ),
        (
            "T0004",
            "operator `+` cannot be applied to `bool`",
            "true + false",
        ),
        ("T0004", "division by zero", "0"),
        ("T0004", "a shift count must be from 0 to 7", "8"),
        (
            "T0004",
            "a shift count must be an integer, found `f64`",
            "1.5",
        ),
        ("T0004", "division by zero", "0"),
        ("T0004", "a shift count must be from 0 to 31", "40u8"),
        ("T0002", "unknown name `missing`", "missing"),
        ("T0004", "`utils` is a module, not a value", "utils"),
        (
            "T0043",
            "functions as values are not supported yet",
            "helper",
        ),
        ("T0007", "cannot assign to immutable `m`", "m"),
        ("T0005", "`m` is not a function", "m"),
        ("T0002", "unknown name `Nope` in module `utils`", "Nope"),
        ("T0002", "unknown name `nothing`", "nothing"),
        ("T0002", "unknown name `missing`", "missing"),
        ("T0018", "cannot assign to constant `C`", "C"),
        (
            "T0004",
            "the value of a constant must be known at compile time",
            "p",
        ),
        ("P0007", "`n` needs a type or a value", "n"),
        (
            "T0001",
            "mismatched types: expected `bool`, found `i32`",
            "1",
        ),
        ("T0043", "string literals are not supported yet", "\"a\""),
        ("P0004", "`break` outside a loop", "break;"),
        ("S0007", "unsupported numeric literal suffix `f16`", "5f16"),
        (
            "T0001",
            "mismatched types: expected `f64`, found `i32`",
            "0xff",
        ),
        ("T0004", "operator `-` cannot be applied to `bool`", "-true"),
        ("T0043", "built-in functions are not supported yet", "alloc"),
        (
            "T0043",
            "destructuring `let`s are not supported yet",
            "let .{ first, second as renamed } = 1;",
        ),
        ("T0016", "`return` needs a value of type `i32`", "return;"),
        (
            "T0016",
            "this function has no result, so `return` takes no value",
            "1",
        ),
        ("T0043", "function types are not supported yet", "fn(i32)"),
    ]);
    assert_eq!(found, expected);
}

/// The diagnostics of the body of `text`'s first function, as each
/// message with its first note.
fn notes(text: &str) -> Vec<(String, Option<String>)> {
    let (notes, _, _) = with_checker(text, |checker, main| {
        let tree = &checker.modules[main.index()].ast;
        let declaration = tree.declarations().next().unwrap();
        let ast::Decl::Function(function) = tree.decls[declaration] else {
            unreachable!("the first declaration is a function")
        };
        let definition = DefId {
            module: main,
            index: 0,
        };
        checker.check_body(definition, function.name.span, &mut Body::default(), false);
        let note = |diagnostic: &diagnostics::Diagnostic| {
            let first = diagnostic.notes.first();
            (
                diagnostic.message.clone(),
                first.map(|note| note.text.clone()),
            )
        };
        checker.module_checks[main.index()]
            .diagnostics
            .iter()
            .map(note)
            .collect()
    });
    notes
}

#[test]
fn a_left_literal_gets_help_if_it_can_have_the_type() {
    let mismatch = |found: &str| format!("mismatched types: expected `i32`, found `{found}`");
    let help = |literal: &str| {
        Some(format!(
            "write `{literal}` to give the left literal that type"
        ))
    };
    // Without a literal to fix, the right operand is converted with `as`.
    let cast = |ty: &str| {
        Some(format!(
            "add `as {ty}` to convert, which can change the value"
        ))
    };
    assert_eq!(
        notes(
            "fn f(x: i64, y: f64) { let a = 1 + x; let b = x + 1; let c = 1 + y; let d = 0xff + y; }"
        ),
        [
            (mismatch("i64"), help("1i64")),
            (mismatch("f64"), help("1f64")),
            (mismatch("f64"), cast("i32")),
        ]
    );
    assert_eq!(
        notes("fn f(x: i64) { let a = 1 + true; let b = 1.5 + x; }"),
        [
            (mismatch("bool"), cast("i32")),
            (
                "mismatched types: expected `f64`, found `i64`".to_string(),
                cast("f64")
            ),
        ]
    );
}

/// A name of another module is guessed only among those it exports.
#[test]
fn unknown_members_get_exported_suggestions() {
    assert_eq!(
        notes("fn f() { utils::Shwn(); utils::hiddn(); }"),
        [
            (
                "unknown name `Shwn` in module `utils`".to_string(),
                Some("did you mean `Shown`?".to_string())
            ),
            ("unknown name `hiddn` in module `utils`".to_string(), None),
        ]
    );
}

#[test]
fn unknown_names_get_suggestions() {
    assert_eq!(
        notes("fn f(count: i32) { let total = cuont; }"),
        [(
            "unknown name `cuont`".to_string(),
            Some("did you mean `count`?".to_string())
        )]
    );
}

#[test]
fn temporaries() {
    let (bodies, reported) = bodies(
        "fn make() -> [2]i32 { return [2]i32{1, 2}; }
            fn id(r: &mut i32) -> &mut i32 in r { return r; }
            fn sum(values: [&]i32) -> i32 { return values[0]; }
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
    // A temporary is stored where it is computed, and the one a `let` lends
    // is marked to last.
    assert_eq!(
        bodies[3],
        "    let total.1 = call f2(&(_.0 = call f0():[2]i32)):i32
    let kept.3 = &(kept _.2 = call f0():[2]i32)
    let first.5 = (_.4 = call f0():[2]i32)[at 0:u64]
    let mut x.6 = 1:i32
    (_.7 = call f1(&mut x.6):&mut i32).* = 2:i32
    {
        let _.9 = &(_.8 = call f0():[2]i32)
        for _.10 in 0:u64..2:u64 by 1:u64 {
            let v.11 = &_.9.*[at _.10]
        }
    }
"
    );
}

#[test]
fn temporaries_cannot_be_written_or_addressed() {
    let (_, found) = bodies(
        "fn make() -> [2]i32 { return [2]i32{1, 2}; }
            fn f() {
                make()[0] = 5;
                let p = @make()[0];
                let q = @5;
            }",
    );
    let expected = reported(&[
        ("T0007", "cannot assign to a temporary value", "make()[0]"),
        (
            "T0004",
            "cannot take address of a temporary value",
            "@make()[0]",
        ),
        ("T0004", "cannot take address of a temporary value", "@5"),
    ]);
    assert_eq!(found, expected);
}

#[test]
fn a_constant_cannot_be_lent_as_writable() {
    let (_, found) = bodies(
        "const LIMIT: i32 = 5;
            fn f() {
                const LOCAL: i32 = 9;
                let a = &mut LIMIT;
                let b = &mut LOCAL;
                let c = &mut (LIMIT);
                let shared = &LIMIT;
                let literal = &mut 5;
                let sum = &mut (LIMIT + 1);
            }",
    );
    let expected = reported(&[
        (
            "T0018",
            "cannot mutably borrow constant `LIMIT`",
            "&mut LIMIT",
        ),
        (
            "T0018",
            "cannot mutably borrow constant `LOCAL`",
            "&mut LOCAL",
        ),
        (
            "T0018",
            "cannot mutably borrow constant `LIMIT`",
            "&mut (LIMIT)",
        ),
    ]);
    assert_eq!(found, expected);
}
