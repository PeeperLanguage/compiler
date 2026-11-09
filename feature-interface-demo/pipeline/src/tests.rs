use model::facts::Target;

use super::mir;
use super::run;
use super::run_native;

const WIDE: Target = Target { pointer_bytes: 8 };
const NARROW: Target = Target { pointer_bytes: 4 };

/// What `main` returns, with no warning: the same on both targets, and the
/// same from the interpreter and from the program clang builds.
fn value(source: &str) -> u64 {
    let wide = run(source, WIDE).unwrap_or_else(|errors| panic!("{errors:#?}"));
    let narrow = run(source, NARROW).unwrap_or_else(|errors| panic!("{errors:#?}"));
    assert_eq!(wide.value, narrow.value, "the two targets disagree");
    assert_eq!(wide.warnings, Vec::<String>::new());
    for target in [WIDE, NARROW] {
        let native = run_native(source, target).unwrap_or_else(|errors| panic!("{errors:#?}"));
        let bytes = target.pointer_bytes;
        let native = native.value;
        assert_eq!(
            native, wide.value,
            "the native run disagrees ({bytes}-byte pointers)"
        );
    }
    wide.value
}

/// The errors and warnings, in source order, of a program refused.
fn errors(source: &str) -> Vec<String> {
    match run(source, WIDE) {
        Ok(output) => panic!("expected refusals, ran to {}", output.value),
        Err(errors) => errors,
    }
}

/// The MIR after cleanup.
fn mir_text(source: &str) -> String {
    mir(source, WIDE)
        .unwrap_or_else(|errors| panic!("{errors:#?}"))
        .value
}

const INTERFACES: &str = "
iface Shape { fn area(&self) -> i32 }
struct Square { side: i32 }
fn Square::area(&self) -> i32 { return self.side * self.side; }
struct Circle { radius: i32 }
fn Circle::area(&self) -> i32 { return 3 * self.radius * self.radius; }
struct Holder { name: i32, shape: Shape }
";

#[test]
fn integer_arithmetic() {
    let source = "fn main() -> i32 { let a = 2; let b = a * 3 + 1; return b - 0; }";
    assert_eq!(value(source), 7);
}

#[test]
#[ignore = "waits for the interfaces feature"]
fn struct_holding_an_interface() {
    let main = "fn main() -> i32 {
        let square = Holder.{ name = 1, shape = Square.{ side = 3 } };
        let circle = Holder.{ shape = Circle.{ radius = 2 }, name = 2 };
        return square.shape.area() + circle.shape.area() * 10
            + square.name * 1000 + circle.name * 10000;
    }";
    assert_eq!(
        value(&format!("{INTERFACES}{main}")),
        9 + 120 + 1000 + 20000
    );
}

#[test]
fn method_on_a_struct_value() {
    let source = "struct Square { side: i32 }
    fn Square::area(&self) -> i32 { return self.side * self.side; }
    fn main() -> i32 { let square = Square.{ side = 5 }; return square.area(); }";
    assert_eq!(value(source), 25);
}

#[test]
#[ignore = "waits for the arrays feature"]
fn for_over_an_array() {
    let source = "fn main() -> i32 {
        let all = [1, 2, 3, 4];
        let mut sum = 0;
        for x in all { sum = sum + x; }
        return sum;
    }";
    assert_eq!(value(source), 10);
}

// Two features at once.

#[test]
#[ignore = "waits for the slices and arrays features"]
fn array_reference_to_slice() {
    let source = "
    fn total(numbers: &[]i32) -> i32 {
        let mut sum = 0;
        for x in numbers { sum = sum + x; }
        return sum;
    }
    fn main() -> i32 { let all = [5, 6, 7]; return total(&all); }";
    assert_eq!(value(source), 18);
}

#[test]
#[ignore = "waits for the slices and arrays features"]
fn reference_to_a_reference() {
    // A reference walks its target, which may be a reference too.
    let total = "fn total(numbers: &[]i32) -> i32 {
        let mut sum = 0;
        for x in numbers { sum = sum + x; }
        return sum;
    }";
    let main = "fn main() -> i32 { let all = [5, 6, 7]; let r = &all; return total(&r); }";
    assert_eq!(value(&format!("{total}{main}")), 18);
    let main = "fn main() -> i32 {
        let all = [5, 6, 7];
        let r = &all;
        let mut sum = 0;
        for x in &r { sum = sum + x; }
        return sum;
    }";
    assert_eq!(value(main), 18);
    let main = "fn main() -> i32 {
        let numbers = 2..5;
        let mut sum = 0;
        for i in &numbers { sum = sum + i; }
        return sum;
    }";
    assert_eq!(value(main), 9);
}

/// A `main` giving nothing gives 0, and a function's name is a value.
#[test]
fn void_main_and_a_function_value() {
    assert_eq!(value("fn main() { let x = 1; }"), 0);
    let source = "fn one() -> i32 { return 1; } fn main() -> i32 { let f = one; return 2; }";
    assert_eq!(value(source), 2);
}

/// A struct with no fields holds nothing: a parameter of it is left out of
/// the native function and of each call, on both sides alike. The real
/// compiler gives 3.
#[test]
fn empty_struct_argument() {
    let source = "struct E { }
    fn f(a: i32, e: E, b: i32) -> i32 { return a - b; }
    fn main() -> i32 { let e = E.{ }; return f(5, e, 2); }";
    assert_eq!(value(source), 3);
}

/// Constants past a byte, and arithmetic that wraps, the same natively.
#[test]
fn wide_constants_and_wrapping() {
    let source =
        "fn main() -> i64 { let big: i64 = 5000000000; let small: i64 = 300; return small - big; }";
    assert_eq!(value(source), (300 - 5_000_000_000_i64) as u64);
    let source = "fn main() -> i32 { let a = 2147483647; return a + 1 + 1000; }";
    assert_eq!(
        value(source),
        u64::from((i32::MAX).wrapping_add(1001) as u32)
    );
}

#[test]
fn comparison_gives_bool() {
    assert_eq!(value("fn main() -> bool { return 2 < 3; }"), 1);
    assert_eq!(value("fn main() -> bool { return 3 < 2; }"), 0);
    let refused = errors("fn main() -> i32 { let n: i32 = 2 < 3; return n; }");
    assert_eq!(refused, ["error[E0002] at 1:33: expected i32, found bool"]);
}

#[test]
#[ignore = "waits for the slices and arrays features"]
fn for_over_an_array_reference() {
    let source = "fn main() -> i32 {
        let all = [1, 2, 3];
        let mut sum = 0;
        for x in &all { sum = sum + x; }
        return sum;
    }";
    assert_eq!(value(source), 6);
}

#[test]
#[ignore = "waits for the ranges feature"]
fn range_of_integers() {
    let source =
        "fn main() -> i32 { let mut sum = 0; for i in 0..5 { sum = sum + i; } return sum; }";
    assert_eq!(value(source), 10);
    // Wider than a word on the 4-byte target: the counter keeps all 8 bytes.
    let wide = "fn main() -> i64 {
        let start: i64 = 4294967296;
        let end: i64 = 4294967298;
        let mut last = start;
        for i in start..end { last = i; }
        return last - start;
    }";
    assert_eq!(value(wide), 1);
    let refused = errors("fn main() -> i32 { let yes = 1 < 2; for i in yes..yes { } return 0; }");
    assert_eq!(
        refused,
        ["error[E1000] at 1:46: a range's ends are integers, not bool"]
    );
}

#[test]
#[ignore = "waits for the interfaces feature"]
fn array_of_structs_holding_interfaces() {
    let main = "fn main() -> i32 {
        let all = [
            Holder.{ name = 1, shape = Square.{ side = 2 } },
            Holder.{ name = 2, shape = Circle.{ radius = 1 } },
        ];
        let mut total = 0;
        for holder in &all { total = total + holder.shape.area() * holder.name; }
        return total;
    }";
    assert_eq!(value(&format!("{INTERFACES}{main}")), 4 + 3 * 2);
}

// Each refusal comes from the feature it is about, in its own words.

#[test]
#[ignore = "waits for the for-in loops feature"]
fn integer_cannot_be_looped_over() {
    let refused = errors("fn main() -> i32 { for x in 5 { } return 0; }");
    assert_eq!(
        refused,
        ["error[E0900] at 1:29: a value of i32 cannot be looped over; loop over a range"]
    );
}

#[test]
#[ignore = "waits for the interfaces feature"]
fn interface_needs_every_method() {
    let main = "struct Point { x: i32 }
    fn main() -> i32 { let held = Holder.{ name = 1, shape = Point.{ x = 1 } }; return 0; }";
    let refused = errors(&format!("{INTERFACES}{main}"));
    assert_eq!(refused.len(), 1);
    assert!(
        refused[0].ends_with("Point has no method `fn area(&self) -> i32`, which Shape needs"),
        "{refused:?}"
    );
}

#[test]
#[ignore = "waits for the interfaces feature"]
fn interface_method_takes_only_self() {
    let main = "struct Point { x: i32 }
    fn Point::area(&self, by: i32) -> i32 { return by; }
    fn main() -> i32 { let held = Holder.{ name = 1, shape = Point.{ x = 1 } }; return 0; }";
    let refused = errors(&format!("{INTERFACES}{main}"));
    assert_eq!(refused.len(), 1);
    assert!(
        refused[0].ends_with("Point has no method `fn area(&self) -> i32`, which Shape needs"),
        "{refused:?}"
    );
}

#[test]
fn self_only_first_in_a_method() {
    let refused = errors("fn twice(&self) -> i32 { return 2; } fn main() -> i32 { return 0; }");
    assert_eq!(
        refused,
        ["error[E0503] at 1:11: `&self` is only for a method"]
    );
    // The receiver is written first, so anywhere else is a syntax error.
    let source = "struct P { x: i32 } fn P::f(n: i32, &self) -> i32 { return n; }";
    assert_eq!(errors(source), ["error[E0004] at 1:37: expected a name"]);
    let source = "struct P { x: i32 } fn P::get(&me) -> i32 { return me.x; }";
    assert_eq!(errors(source), ["error[E0004] at 1:32: expected `self`"]);
}

#[test]
fn method_without_self_is_not_a_method() {
    let source = "struct P { x: i32 } fn P::make() -> i32 { return 1; }
    fn main() -> i32 { let p = P.{ x = 1 }; return p.make(); }";
    assert_eq!(
        errors(source),
        [
            "error[E0504] at 1:27: a method takes `&self` first",
            "error[E0601] at 2:52: P has no method `make`",
        ]
    );
}

#[test]
fn every_field_given_once() {
    let source = "struct P { x: i32 }
    fn main() -> i32 { let p = P.{ x = 1, x = nope }; return p.x; }";
    assert_eq!(
        errors(source),
        [
            "error[E0703] at 2:43: field `x` is given twice",
            "error[E0001] at 2:47: nothing is named `nope` here",
        ]
    );
}

#[test]
#[ignore = "waits for the arrays feature"]
fn array_count_must_fit() {
    let refused = errors("fn main() -> i32 { let a: [99999999999]i32 = [1]; return 0; }");
    assert_eq!(
        refused,
        ["error[E0802] at 1:28: an array holds at most 4294967295 elements"]
    );
}

#[test]
fn later_binding_hides_earlier() {
    assert_eq!(
        value("fn main() -> i32 { let x = 1; let x = 2; return x; }"),
        2
    );
}

#[test]
fn struct_cannot_hold_itself() {
    let refused = errors("struct A { b: B } struct B { a: A } fn main() -> i32 { return 0; }");
    assert_eq!(
        refused,
        ["error[E0003] at 1:8: A holds itself, so it has no size"]
    );
}

#[test]
fn only_a_place_is_set() {
    let refused = errors("fn main() -> i32 { 1 = 2; return 0; }");
    assert_eq!(
        refused,
        ["error[E0300] at 1:20: this cannot be set; only a place can"]
    );
}

#[test]
fn keyword_is_not_a_name() {
    let refused = errors("fn main() -> i32 { let for = 1; return 0; }");
    assert_eq!(refused, ["error[E0004] at 1:24: expected a name"]);
}

#[test]
fn refused_value_is_refused_once() {
    // The error type answers every question, so what uses it adds nothing.
    for source in [
        "fn main() -> i32 { if nope { } return 0; }",
        "fn main() -> i32 { return nope.x; }",
        "fn main() -> i32 { return nope + 1; }",
    ] {
        let refused = errors(source);
        assert_eq!(refused.len(), 1, "{refused:?}");
        assert!(
            refused[0].ends_with("nothing is named `nope` here"),
            "{refused:?}"
        );
    }
}

// The core's phases: control flow, places, moves and the MIR they give.

#[test]
fn if_else_chooses_one_block() {
    let source = "fn pick(n: i32) -> i32 {
        if n < 0 { return 1; } else if n < 10 { return 2; } else { return 3; }
    }
    fn main() -> i32 { return pick(0 - 5) + pick(5) * 10 + pick(50) * 100; }";
    assert_eq!(value(source), 321);
    let source = "fn main() -> i32 { let mut n = 1; if n < 0 { n = 2; } return n; }";
    assert_eq!(value(source), 1);
    // Each branch runs on to the code after the `if`.
    let source = "fn main() -> i32 {
        let mut n = 1;
        if 0 < n { n = n + 10; } else { n = 0; }
        if n < 0 { n = 0; } else { n = n * 2; }
        return n;
    }";
    assert_eq!(value(source), 22);
}

#[test]
fn loops_with_break_and_continue() {
    let source = "fn main() -> i32 {
        let mut i = 0;
        let mut sum = 0;
        for i < 10 { i = i + 1; if i < 3 { continue; } sum = sum + i; }
        let mut n = 0;
        for { n = n + 1; if 4 < n { break; } }
        return sum * 10 + n;
    }";
    assert_eq!(value(source), 525);
}

#[test]
fn jump_and_condition_refusals() {
    let refused = errors("fn main() -> i32 { break; return 0; }");
    assert_eq!(
        refused,
        ["error[E0010] at 1:20: `break` is only inside a loop"]
    );
    let refused = errors("fn main() -> i32 { return; }");
    assert_eq!(
        refused,
        ["error[E0011] at 1:20: this function returns i32, so `return` needs a value"]
    );
    let refused = errors("fn main() -> i32 { if 1 { } return 0; }");
    assert_eq!(refused, ["error[E0002] at 1:23: expected bool, found i32"]);
}

#[test]
fn set_through_a_mutable_reference() {
    let source = "fn set(n: &mut i32) { n.* = 3; }
    fn main() -> i32 { let mut n = 0; set(&mut n); return n; }";
    assert_eq!(value(source), 3);
}

#[test]
fn mutable_only_through_mut() {
    let refused = errors(
        "fn set(n: &mut i32) { n.* = 3; }
    fn main() -> i32 { let n = 0; set(&mut n); return n; }",
    );
    assert_eq!(
        refused,
        ["error[E0005] at 2:44: this cannot be borrowed `&mut`; it is not `mut`"]
    );
    let refused = errors(
        "struct P { x: i32 }
    fn reset(p: &P) { p.x = 0; } fn main() -> i32 { return 0; }",
    );
    assert_eq!(
        refused,
        ["error[E0005] at 2:23: this cannot be set; it is not `mut`"]
    );
    let refused = errors("fn main() -> i32 { let n = 1; return n.*; }");
    assert_eq!(
        refused,
        ["error[E0401] at 1:38: a value of i32 is not a reference"]
    );
}

#[test]
fn let_without_a_value() {
    assert_eq!(
        value("fn main() -> i32 { let x: i32; x = 4; return x; }"),
        4
    );
    let refused = errors("fn main() -> i32 { let x; return 0; }");
    assert_eq!(
        refused,
        ["error[E0301] at 1:24: `x` needs a type or a value"]
    );
}

#[test]
fn struct_moves_into_a_call() {
    let source = "struct Point { x: i32, y: i32 }
    fn sum(p: Point) -> i32 { return p.x + p.y; }
    fn main() -> i32 { let p = Point.{ y = 4, x = 3 }; let y = p.y; return sum(p) * 10 + y; }";
    assert_eq!(value(source), 74);
}

/// Each program, and what the real compiler (`peeper check`) reports for
/// it: the same code, message, line and column.
#[test]
fn path_checks_match_the_real_compiler() {
    let cases = [
        (
            "fn main() -> i32 { }",
            "error[T0017] at 1:20: not all control paths return a value",
        ),
        (
            "fn main() -> i32 { let x: i32; return x; }",
            "error[T0039] at 1:39: `x` is used before it is set",
        ),
        (
            "fn main() -> i32 { let x: i32; if 1 < 2 { x = 1; } return x; }",
            "error[T0039] at 1:59: `x` is used before it is set",
        ),
        (
            "fn main() -> i32 { let x: i32; x = 1; x = 2; return x; }",
            "error[T0007] at 1:39: cannot assign twice to immutable `x`",
        ),
        (
            "struct P { x: i32, h: &mut i32 }
fn take(p: P) -> i32 { return p.x; }
fn main() -> i32 { let mut n = 1; let p = P.{ x = 1, h = &mut n }; let a = take(p); return take(p) + a; }",
            "error[T0034] at 3:97: `p` is used after it was moved",
        ),
        (
            "struct P { h: &mut i32 }
fn take(p: P) -> i32 { return 1; }
fn main() -> i32 { let mut n = 0; let p = P.{ h = &mut n }; let mut i = 0; for i < 2 { i = i + take(p); } return i; }",
            "error[T0034] at 3:101: `p` is used after it was moved",
        ),
        (
            "struct H { r: &mut i32 }
fn main() -> i32 { let mut n = 1; let h = H.{ r = &mut n }; let r = h.r; return r.*; }",
            "error[T0035] at 2:69: cannot move a field out of its struct",
        ),
        (
            "fn f(r: &&mut i32) -> i32 { let m = r.*; return m.*; }
fn main() -> i32 { let mut n = 1; let m = &mut n; return f(&m); }",
            "error[T0035] at 1:37: cannot move out of a reference or pointer",
        ),
        (
            "fn main() -> i32 { let mut x = 1; let r = &x; x = 2; return r.*; }",
            "error[T0037] at 1:47: `x` is changed while a reference to it is still in use",
        ),
        (
            "fn main() -> i32 { let mut x = 1; let r = &mut x; let y = x; r.* = 2; return y; }",
            "error[T0037] at 1:59: `x` is read while a `&mut` reference to it is still in use",
        ),
        (
            "fn main() -> i32 { let y = 0; let mut r = &y; if y < 1 { let x = 1; r = &x; } return r.*; }",
            "error[T0037] at 1:77: `x` is gone while a reference to it is still in use",
        ),
        (
            "fn main() -> i32 { let y = 0; let mut r = &y; for { let x = 1; r = &x; break; } return r.*; }",
            "error[T0037] at 1:72: `x` is gone while a reference to it is still in use",
        ),
        (
            "fn both(a: &mut i32, b: &i32) -> i32 { return b.*; }
fn main() -> i32 { let mut x = 1; return both(&mut x, &x); }",
            "error[T0037] at 2:47: `x` is borrowed as `&mut` while a reference to it is still in use",
        ),
        (
            "struct P { x: i32, y: i32 }
fn main() -> i32 { let mut p = P.{ x = 1, y = 2 }; let r = &p.x; p.y = 3; let a = r.*; p.x = 4; return a + r.*; }",
            "error[T0037] at 2:88: `p` is changed while a reference to it is still in use",
        ),
        (
            "struct Two { a: &i32, b: &i32 }
fn main() -> i32 { let mut x = 1; let mut y = 2; let t = Two.{ a = &x, b = &y }; x = 5; let v = t.b.*; y = 6; return v + t.b.*; }",
            "error[T0037] at 2:104: `y` is changed while a reference to it is still in use",
        ),
        (
            "fn id(r: &i32) -> &i32 { return r; }
fn main() -> i32 { let mut x = 1; let r = id(&x); x = 2; return r.*; }",
            "error[T0037] at 2:51: `x` is changed while a reference to it is still in use",
        ),
        (
            "fn id(r: &mut i32) -> &mut i32 { return r; }
fn main() -> i32 { let mut x = 1; let r = id(&mut x); let y = x; r.* = 2; return y; }",
            "error[T0037] at 2:63: `x` is read while a `&mut` reference to it is still in use",
        ),
        (
            "fn pick(a: &mut i32) -> &i32 { let r = &a.*; a.* = 2; return r; }
fn main() -> i32 { let mut n = 1; return pick(&mut n).*; }",
            "error[T0037] at 1:46: `a` is changed while a reference to it is still in use",
        ),
        (
            "fn pick(r: &i32) -> &i32 { let x = 1; return &x; }
fn main() -> i32 { let n = 2; return pick(&n).*; }",
            "error[T0036] at 1:39: returns a reference into `x`, which is gone once the function returns",
        ),
    ];
    for (source, expected) in cases {
        assert_eq!(errors(source), [expected], "{source}");
    }
}

/// What the real compiler accepts, with the value its build gives.
#[test]
fn path_checks_accept_what_the_real_compiler_accepts() {
    // A `&mut` argument is only reserved until its call starts.
    let source = "fn update(a: &mut i32, v: i32) -> i32 { a.* = v; return v; }
    fn read(b: &i32) -> i32 { return b.*; }
    fn main() -> i32 { let mut x = 1; let y = update(&mut x, read(&x)); return x + y; }";
    assert_eq!(value(source), 2);
    // A store to `r` ends the loan of `r.*`, which named what `r` pointed to.
    let source = "fn main() -> i32 { let mut a = 1; let mut b = 2; let mut r = &mut a;
    let s = &mut r.*; r = &mut b; r.* = 4; s.* = 3; return a + b; }";
    assert_eq!(value(source), 7);
    // A reference lasts to its last use, not to its block's end.
    let source =
        "fn main() -> i32 { let mut x = 1; let r = &x; let a = r.*; x = 2; return a + x; }";
    assert_eq!(value(source), 3);
    // Each turn of a loop declares `x` anew.
    let source =
        "fn main() -> i32 { let mut i = 0; for i < 2 { let x: i32; x = i; i = x + 1; } return i; }";
    assert_eq!(value(source), 2);
    // Each path sets `x` once.
    let source = "fn main() -> i32 { let x: i32; if 1 < 2 { x = 1; } else { x = 2; } return x; }";
    assert_eq!(value(source), 1);
}

#[test]
fn unreachable_code_is_a_warning() {
    let output = run("fn main() -> i32 { return 1; let x = 2; }", WIDE)
        .unwrap_or_else(|errors| panic!("{errors:#?}"));
    assert_eq!(output.value, 1);
    assert_eq!(
        output.warnings,
        ["warning[W0001] at 1:30: unreachable code"]
    );
}

/// The real compiler's MIR fixture, and its expected dump: a `&mut` in a
/// variable given to a call is borrowed again, a field of a reference goes
/// through it, and a borrow of a field is of that place.
#[test]
fn mir_matches_the_real_compiler() {
    let source = include_str!("../../../x_test/mir_references/src/main.peep");
    let expected = include_str!("../../../x_test/mir_references/expected.mir");
    assert_eq!(value(source), 5);
    let dump = mir_text(source);
    assert_eq!(dump, expected);
}

#[test]
fn mutable_reference_and_struct_move() {
    // A `&mut` is the one way to set its target, so reading it moves it.
    let source =
        "fn main() -> i32 { let mut n = 1; let r = &mut n; let s = r; s.* = 2; return n; }";
    assert_eq!(value(source), 2);
    let dump = mir_text(source);
    assert!(dump.contains("%3 = move r.1: &mut i32"), "{dump}");
    let source =
        "struct P { x: i32 } fn main() -> i32 { let p = P.{ x = 1 }; let q = p; return q.x; }";
    let dump = mir_text(source);
    assert!(dump.contains("%2 = move p.0: P"), "{dump}");
    // Values run in the order written; the parts are in the fields' order.
    let source =
        "struct P { x: i32, y: i32 } fn main() -> i32 { let p = P.{ y = 4, x = 3 }; return 0; }";
    let dump = mir_text(source);
    assert!(dump.contains("%0 = aggregate(3, 4): P"), "{dump}");
}

/// Cleanup sends a jump past a block that only jumps on: the `break`'s
/// block goes straight to the loop's exit.
#[test]
fn loop_mir_after_cleanup() {
    let source =
        "fn main() -> i32 { let mut n = 0; for { n = n + 1; if 4 < n { break; } } return n; }";
    let expected = "fn main:
  bb0:
    store n.0, 0
    goto bb1
  bb1:
    %1 = load n.0: i32
    %2 = Add %1, 1: i32
    store n.0, %2
    %4 = load n.0: i32
    %5 = LessThan 4, %4: bool
    branch %5 (4 < n) bb2 bb1
  bb2:
    %6 = load n.0: i32
    return %6
";
    assert_eq!(mir_text(source), expected);
}

/// Blocks are numbered as the real compiler numbers them
/// (`x_test/mir_branches`): then, else, then the join.
#[test]
fn if_else_mir_block_order() {
    let source =
        "fn main() -> i32 { let mut n = 0; if n < 1 { n = 5; } else { n = 6; } return n; }";
    let expected = "fn main:
  bb0:
    store n.0, 0
    %1 = load n.0: i32
    %2 = LessThan %1, 1: bool
    branch %2 (n < 1) bb1 bb2
  bb1:
    store n.0, 5
    goto bb3
  bb2:
    store n.0, 6
    goto bb3
  bb3:
    %5 = load n.0: i32
    return %5
";
    assert_eq!(mir_text(source), expected);
}
