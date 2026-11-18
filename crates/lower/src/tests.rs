//! Each kind of THIR node lowered into the MIR of core concepts, read as
//! its dump text.

use std::sync::Arc;

use check::Checker;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::ModuleId;
use model::machine::Machine;
use model::mir;
use model::mir::DebugType;
use model::mir::DebugTypes;
use model::thir;
use source::SourceFile;
use tree::ast;

/// The MIR text of the function `main` of the one-module program `text`,
/// which must check without errors, for a 64-bit target.
fn lowered(text: &str) -> String {
    lower_main(text, "x86_64-linux").0
}

/// `text`'s `main` lowered for the machine named `target`: its dump, its
/// MIR and the debug types lowering made.
fn lower_main(text: &str, target: &str) -> (String, mir::Body, DebugTypes) {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (ast, parse) = parser::parse(&file);
    assert!(parse.is_empty(), "{parse:?}");
    let modules = [model::program::Module {
        path: "app:main".to_string(),
        ast,
        imports: Vec::new(),
    }];
    let target = Machine::named(target).unwrap();
    let mut checker = Checker::new(&modules, target, false);
    let tree = &modules[0].ast;
    let (index, name) = tree
        .declarations()
        .enumerate()
        .find_map(|(index, declaration)| match tree.decls[declaration] {
            ast::Decl::Function(function) if function.name.text(&file) == "main" => {
                Some((index, function.name.span))
            }
            _ => None,
        })
        .expect("the program has a `main`");
    let main = DeclarationCopy {
        definition: DefId {
            module: ModuleId::from_index(0),
            index: index as u32,
        },
        arguments: ConstArguments::NONE,
    };
    let ty = checker.copy_type(main, name);
    let mut body = thir::Body::default();
    assert!(checker.check_body(main, name, &mut body, true));
    let reported: Vec<String> = (checker.module_checks.iter())
        .flat_map(|module| &module.diagnostics)
        .map(|diagnostic| diagnostic.message.clone())
        .collect();
    assert!(reported.is_empty(), "{reported:?}");
    let mut debug_types = super::ProgramDebugTypes::default();
    let mir = super::lower(
        &body,
        ty,
        &checker.types,
        target,
        &modules,
        &mut debug_types,
    )
    .expect("the body lowers");
    let name_of =
        |function: DeclarationCopy| model::program::declared_path(&modules, function.definition);
    (mir.text(&file, name_of), mir, debug_types.table)
}

#[test]
fn integers_are_registers_and_variables_are_slots() {
    let text = lowered("fn main() -> i32 { let a = 2; let b: i64 = a + 3; return a; }");
    assert_eq!(
        text,
        "  bb0:
    store i32 a.0, 2
    %1 = load i32 a.0
    %2 = add i32 %1, 3
    %3 = sext i32 %2 to i64
    store i64 b.1, %3
    %5 = load i32 a.0
    dead b.1
    dead a.0
    return %5
  bb1:
    dead b.1
    dead a.0
    fall off at `}`
"
    );
}

#[test]
fn a_narrow_integer_is_cut_back_to_its_width() {
    let text = lowered("fn main() -> i24 { let a: i24 = 1; return a + 1; }");
    assert!(
        text.contains("%2 = add i32 %1, 1\n    %3 = shl i32 %2, 8\n    %4 = sshr i32 %3, 8\n"),
        "{text}"
    );
}

#[test]
fn a_constant_shift_count_is_widened_by_lowering_not_by_the_run() {
    let text = lowered("fn main() -> i64 { let a: i64 = 5; return a << 3; }");
    assert!(text.contains("= shl i64 %1, 3\n"), "{text}");
    assert!(!text.contains("zext"), "{text}");
}

#[test]
fn a_wide_constant_is_loaded_from_read_only_data() {
    let text = lowered("fn main() { let a: u256 = 5; let b = -(a as i256); let c = ~a; }");
    // The zero of `-x` and the ones of `~x`: an operand holds 128 bits.
    let zero = "%3 = load i256 at global 1\n    %4 = sub i256 %3, %2\n";
    assert!(text.contains(zero), "{text}");
    let ones = "%7 = load i256 at global 2\n    %8 = xor i256 %6, %7\n";
    assert!(text.contains(ones), "{text}");
}

#[test]
fn a_c_call_carries_how_c_passes_each_value() {
    let program = "#[extern(\"c_mix\")]
fn mix(a: i5, b: u100, c: i200, d: bool) -> i100;
fn main() { let x = mix(1, 2, 3, true); }";
    let text = lowered(program);
    let call = "in C (signext, 2 x i64, memory 32 align 8 byval, zeroext) -> 2 x i64\n";
    assert!(text.contains(call), "{text}");
    // i386 passes a wide integer as a copy's address, in 4-byte words.
    let i386 = lower_main(program, "i686-linux").0;
    let call =
        "in C (signext, memory 16 align 4, memory 28 align 4, zeroext) -> memory 16 align 4\n";
    assert!(i386.contains(call), "{i386}");
    // The result's bits above 100 are cut back, as C leaves them unset.
    assert!(text.contains("shl i128 %"), "{text}");
}

#[test]
fn a_division_is_checked_unless_by_a_constant() {
    let text = lowered("fn main() -> i32 { let a = 7; let b = a / 2; return b / a; }");
    assert!(text.contains("%2 = sdiv i32 %1, 2\n"), "{text}");
    assert!(
        text.contains(
            "%6 = eq i32 %5, 0\n    check %6, \"division by zero\"\n    %8 = sdiv i32 %4, %5\n"
        ),
        "{text}"
    );
}

#[test]
fn a_struct_lives_in_a_slot_and_its_fields_at_offsets() {
    let text = lowered(
        "struct Point { x: i32, y: i64 }
fn main() -> i64 { let p = Point.{ x = 1, y = 2 }; return p.y; }",
    );
    assert_eq!(
        text,
        "  bb0:
    store i32 p.0[+0], 1
    store i64 p.0[+8], 2
    %2 = load i64 p.0[+8]
    dead p.0
    return %2
  bb1:
    dead p.0
    fall off at `}`
"
    );
}

#[test]
fn a_struct_argument_is_a_copy_passed_by_address() {
    let text = lowered(
        "struct Point { x: i32, y: i64 }
fn sum(p: Point) -> i64 { return p.y; }
fn main() -> i64 { let p = Point.{ x = 1, y = 2 }; return sum(p); }",
    );
    assert!(
        text.contains(
            "    copy 16 .1, p.0\n    moved 16 p.0\n    %4 = address 16 .1\n    %5 = call sum(%4)\n"
        ),
        "{text}"
    );
}

/// The caller's copy is the callee's parameter: read where it was passed,
/// with no copy of its own.
#[test]
fn a_struct_parameter_is_the_callers_copy() {
    let text = lowered(
        "struct Point { x: i32, y: i64 }
fn main(p: Point) -> i64 { return p.y; }",
    );
    assert!(
        text.starts_with("  bb0:\n    %0 = load i64 p.0[+8]\n"),
        "{text}"
    );
    assert!(!text.contains("copy"), "{text}");
}

/// A range spread with constant ends is counted while lowering, so the
/// array's length needs no check; a range held in a variable is counted
/// and checked when the program runs.
#[test]
fn a_constant_range_spread_needs_no_check() {
    let constant = lowered("fn main() -> [6]i32 { return [_]i32{(0..3)..., 9, (5..=4)...}; }");
    assert!(!constant.contains("needs exactly"), "{constant}");
    assert!(!constant.contains("udiv"), "{constant}");
    let held = lowered("fn main(r: range[i32]) -> [3]i32 { return [3]i32{r...}; }");
    assert!(held.contains("needs exactly 3 values"), "{held}");
}

#[test]
fn a_call_writes_a_struct_result_into_its_place() {
    let text = lowered(
        "struct Point { x: i32, y: i64 }
fn main() -> i32 { let p = make(); return p.x; }
fn make() -> Point { return Point.{ x = 1, y = 2 }; }",
    );
    assert!(
        text.contains("    call make() into 16 p.0\n    %1 = load i32 p.0[+0]\n"),
        "{text}"
    );
}

#[test]
fn and_keeps_its_result_in_a_hidden_byte() {
    let text = lowered("fn main() -> bool { let a = true; let b = false; return a && b; }");
    assert_eq!(
        text,
        "  bb0:
    store i8 a.0, 1
    store i8 b.1, 0
    %2 = load i8 a.0
    store i8 .2, 0
    branch %2, bb1, bb2
  bb1:
    %4 = load i8 b.1
    store i8 .2, %4
    jump bb2
  bb2:
    %6 = load i8 .2
    dead .2
    dead b.1
    dead a.0
    return %6
  bb3:
    dead b.1
    dead a.0
    fall off at `}`
"
    );
}

#[test]
fn a_reference_is_a_borrow_and_a_dereference_goes_through() {
    let text = lowered("fn main() -> i32 { let mut a = 1; let r = &mut a; r.* = 2; return a; }");
    assert!(
        text.contains("    %1 = borrow mut 4 a.0 as reference\n    store ptr r.1, %1\n"),
        "{text}"
    );
    assert!(text.contains("    store i32 r.1[*], 2\n"), "{text}");
}

#[test]
fn an_array_index_is_checked_and_scaled() {
    let text =
        lowered("fn main() -> i32 { let a = [3]i32{1, 2, 3}; let i: usize = 1; return a[i]; }");
    assert!(
        text.contains(
            "    %4 = load i64 i.1\n    %5 = ule i64 3, %4\n    \
             check %5, \"index out of bounds: the length is 3\"\n    %7 = load i32 a.0[%4 * 4 of 3]\n"
        ),
        "{text}"
    );
}

#[test]
fn a_slice_is_two_words_and_its_elements_are_behind_the_first() {
    let text =
        lowered("fn main() -> i32 { let a = [3]i32{1, 2, 3}; let s = &a[1..]; return s[0]; }");
    assert!(
        text.contains("borrow 8 a.0[1 * 4 of 3] as slice\n"),
        "{text}"
    );
    assert!(text.contains("load i64 s.1[+8]\n"), "{text}");
    assert!(text.contains("load i32 s.1[*, 0 * 4]\n"), "{text}");
}

#[test]
fn a_range_loop_keeps_its_state_in_hidden_slots() {
    let text = lowered(
        "fn main() -> i32 { let mut sum = 0; for i in 0..3 { sum = sum + i; } return sum; }",
    );
    assert!(text.contains("store i32 .2, 0\n"), "{text}"); // the counter starts at 0
    assert!(text.contains("store i32 .3, 3\n"), "{text}"); // `left` is the distance
    assert!(text.contains("store i8 .4, %1\n"), "{text}"); // a first pass runs when `left` is not 0
    assert!(
        text.contains("jump bb1\n  bb1:\n    %5 = load i8 .4\n    branch %5, bb2, bb3\n"),
        "{text}"
    );
    assert!(text.contains("    %9 = ult i32 1, %7\n"), "{text}"); // a next pass while `step < left`
}

#[test]
fn print_writes_text_once_and_each_value_by_its_kind() {
    // With no prelude, the program declares what `print` calls.
    let text = lowered(
        "#[extern(\"print_text\")] fn PrintText(text: &cstr);
#[extern(\"print_bytes\")] fn PrintBytes(bytes: rawptr, length: usize);
#[extern(\"print_int\")] fn PrintInt(signed: i32, limbs: i32, number: rawptr);
#[extern(\"print_float\")] fn PrintFloat(value: f64, bits: i32);
#[extern(\"print_pointer\")] fn PrintPointer(pointer: rawptr);
#[extern(\"flush\")] fn Flush();
fn main() { let a = 5; println(\"a is\", a, true); }",
    );
    // The `&str` is its two words; each space is one call; `true` and
    // `false` are printed by the branch taken; the newline ends it.
    assert!(
        text.contains("    store ptr .2, global 0\n    store i64 .2[+8], 4\n"),
        "{text}"
    );
    for line in [
        "call PrintBytes(%6, %7) in C",
        "call PrintInt(1, 1, %10) in C",
        "  bb1:\n    call PrintText(global 3) in C",
        "  bb2:\n    call PrintText(global 4) in C",
        "call PrintText(global 5) in C (register)\n    call Flush() in C",
    ] {
        assert!(text.contains(line), "{line}not in\n{text}");
    }
}

#[test]
fn an_interface_value_is_an_address_and_a_table() {
    let text = lowered(
        "struct Square { side: i32 }
iface Shape { fn area(&self) -> i32 }
fn Square::area(&self) -> i32 { return self.side * self.side; }
fn main() -> i32 { let s = Square.{ side = 3 }; let shape: &Shape = &s; return shape.area(); }",
    );
    assert!(text.contains("borrow 4 s.0 as reference\n"), "{text}");
    assert!(text.contains("store ptr shape.1[+8], global 0\n"), "{text}");
    assert!(
        text.contains("  global 0 = table [Square::area]\n"),
        "{text}"
    );
    // The call lends what the reference views again, then reads the
    // method from the table.
    assert!(
        text.contains("    %4 = borrow shape.1[*] as reference\n    %5 = load ptr shape.1[+8]\n"),
        "{text}"
    );
    assert!(
        text.contains(
            "    %8 = load ptr .2\n    %9 = load ptr .2[+8]\n    \
             %10 = load ptr at %9[+0]\n    %11 = call through %10(%8)\n"
        ),
        "{text}"
    );
}

#[test]
fn a_wide_integer_has_its_own_class_and_its_constants_are_data() {
    let text = lowered(
        "fn main() -> i32 { let a: u200 = 1 << 150; let b = a + a; if b > a { return 1; } return 0; }",
    );
    // The constant is loaded from read-only data; the operations are of the
    // exact width, so nothing is cut back.
    assert!(
        text.contains("    %0 = load i200 at global 0\n    store i200 a.0, %0\n"),
        "{text}"
    );
    assert!(
        text.contains("    %4 = add i200 %2, %3\n    store i200 b.1, %4\n"),
        "{text}"
    );
    assert!(text.contains("    %8 = ult i200 %7, %6\n"), "{text}");
}

#[test]
fn a_narrow_signed_division_is_cut_back() {
    let text =
        lowered("fn main() -> i32 { let a: i5 = -16; let b: i5 = -1; let c = a / b; return 0; }");
    // `-16 / -1` is 16 in the `i8` class; cut back, it is -16 again.
    assert!(
        text.contains("    %6 = sdiv i8 %2, %3\n    %7 = shl i8 %6, 3\n    %8 = sshr i8 %7, 3\n"),
        "{text}"
    );
}

#[test]
fn a_signed_shift_count_is_checked_however_narrow() {
    let text = lowered("fn main() -> i32 { let c: i5 = -1; let x = 8; return x << c; }");
    // An `i5` cannot reach 32, but it can be negative: only that is checked.
    assert!(
        text.contains(
            "    %4 = slt i8 %3, 0\n    check %4, \"a shift count must be from 0 to 31\"\n"
        ),
        "{text}"
    );
    // An `i7` can reach 32: read unsigned, a negative count is above it.
    let text = lowered("fn main() -> i32 { let c: i7 = 2; let x = 8; return x << c; }");
    assert!(text.contains("    %4 = ule i8 32, %3\n"), "{text}");
}

#[test]
fn an_unsigned_shift_count_too_narrow_to_reach_the_width_is_not_checked() {
    let text = lowered("fn main() -> i32 { let c: u5 = 31; let x = 8; return x << c; }");
    assert!(!text.contains("shift count"), "{text}");
    // A `u6` can reach 32.
    let text = lowered("fn main() -> i32 { let c: u6 = 32; let x = 8; return x << c; }");
    assert!(text.contains("shift count"), "{text}");
}

#[test]
fn a_narrow_range_reads_its_distance_with_the_other_sign_cut_back() {
    let text = lowered("fn main() { let a: i7 = -3; let b: i7 = 3; for i in a..b { } }");
    // `left` is the distance as a `u7`: the `i7` bits read unsigned, so the
    // bits above the width are cleared.
    assert!(
        text.contains("    %5 = sub i8 %3, %2\n    %6 = shl i8 %5, 1\n    %7 = sshr i8 %6, 1\n    %8 = and i8 %7, 127\n"),
        "{text}"
    );
}

/// A `&mut` moves though it is one register, and a `&` copies; the step
/// that lends one again comes with the move, for the message.
#[test]
fn a_mutable_reference_or_slice_moves_with_how_to_lend_it_again() {
    let text = lowered(
        "fn main() {
    let mut x = 1;
    let mut a = [2]i32{1, 2};
    let r = &mut x;
    let s = &mut a[..];
    let q = &x;
    let r2 = r;
    let s2 = s;
    let q2 = q;
}",
    );
    assert!(text.contains("moved 8 r.2 lent again with .*\n"), "{text}");
    assert!(
        text.contains("moved 16 s.3 lent again with [..]\n"),
        "{text}"
    );
    assert!(!text.contains("moved q."), "{text}");
}

/// A struct is moved once its bytes are read; an owned interface taken by
/// value once its words are read; a field read out of a call's temporary
/// moves nothing, as nothing reads the temporary again.
#[test]
fn a_value_in_memory_moves_after_it_is_read() {
    let text = lowered(
        "iface Shape { fn retire(self) -> i32 }
struct Square { side: i32 }
fn Square::retire(self) -> i32 { return self.side; }
struct Pair { a: Square, b: Square }
fn make() -> Pair { return Pair.{ a = Square.{ side = 1 }, b = Square.{ side = 2 } }; }
fn main() {
    let p = make();
    let q = p;
    let s: Shape = Square.{ side = 3 };
    let n = s.retire();
    let a = make().a;
}",
    );
    assert!(
        text.contains("    copy 8 q.1, p.0\n    moved 8 p.0\n"),
        "{text}"
    );
    assert!(
        text.contains("load ptr s.3[+8]\n    moved 16 s.3\n"),
        "{text}"
    );
    assert_eq!(text.matches("moved").count(), 2, "{text}");
}

/// Debug types follow pointers, slices and structs `DEEPEST_DEBUG_TYPE` deep, then
/// stop: clang runs out of stack on deeper ones.
#[test]
fn debug_types_stop_at_the_deepest_depth() {
    let deepest = super::DEEPEST_DEBUG_TYPE as usize;
    let source = format!("fn main(a: {}i32) {{ }}", "*".repeat(deepest + 10));
    let (_, mir, debug_types) = lower_main(&source, "x86_64-linux");
    let mut next = (mir.slots.ids()).find_map(|slot| mir.slots[slot].debug_type);
    let mut pointers = 0;
    while let Some(id) = next {
        let DebugType::Pointer { to, .. } = debug_types.get(id) else {
            break;
        };
        (pointers, next) = (pointers + 1, *to);
    }
    assert_eq!(pointers, deepest + 1);

    let chain: String = (1..deepest + 10)
        .map(|i| format!("struct S{i} {{ s: S{} }}\n", i - 1))
        .collect();
    let last = deepest + 9;
    let source = format!("struct S0 {{ n: i32 }}\n{chain}fn main(a: S{last}) {{ }}");
    let (_, mir, debug_types) = lower_main(&source, "x86_64-linux");
    let mut next = (mir.slots.ids()).find_map(|slot| mir.slots[slot].debug_type);
    let mut structs = 0;
    while let Some(id) = next {
        let DebugType::Composite { members, .. } = debug_types.get(id) else {
            break;
        };
        (structs, next) = (structs + 1, members.first().map(|member| member.ty));
    }
    assert_eq!(structs, deepest + 1);

    // A slice's element is behind its first member, a pointer.
    let source = format!("fn main(a: {}i32) {{ }}", "&[]".repeat(deepest + 10));
    let (_, mir, debug_types) = lower_main(&source, "x86_64-linux");
    let mut next = (mir.slots.ids()).find_map(|slot| mir.slots[slot].debug_type);
    let mut slices = 0;
    while let Some(id) = next {
        let DebugType::Composite { members, .. } = debug_types.get(id) else {
            break;
        };
        let DebugType::Pointer { to, .. } = debug_types.get(members[0].ty) else {
            unreachable!("a slice's first member is its pointer")
        };
        (slices, next) = (slices + 1, *to);
    }
    assert_eq!(slices, deepest + 1);
}

/// Each way out of a block ends the variables it leaves, newest first.
#[test]
fn each_way_out_of_a_block_ends_its_variables_newest_first() {
    let text = lowered(
        "fn main(limit: i32) -> i32 {
            let first: i32 = limit;
            for first > 0 {
                let second: i32 = first;
                let third: i32 = second;
                if third == 1 {
                    break;
                }
                if third == 2 {
                    continue;
                }
                if third == 3 {
                    return third;
                }
            }
            return first;
        }",
    );
    let ends: Vec<&str> = (text.lines())
        .map(str::trim)
        .filter(|line| line.starts_with("dead "))
        .collect();
    let (outer, inner) = (
        ["dead first.1", "dead limit.0"],
        ["dead third.3", "dead second.2"],
    );
    let expected = [
        &outer[..], // after the loop, which `break` jumps to
        &inner,     // at the `break`
        &inner,     // at the `continue`
        &inner,     // at the `return`, with the rest
        &outer,
        &inner, // at the loop's `}`
        &outer, // at the function's `}`
    ];
    assert_eq!(ends, expected.concat());
}

/// A slice's start and count are `usize`s, `i32`s on a 32-bit target:
/// a wider `i64` end is checked in its own type before it is cut to
/// one, a narrower `i8` one is extended first.
#[test]
fn slice_bounds_are_target_sized() {
    let text = lower_main(
        "fn main(a: [4]i32, wide: i64, narrow: i8) {
            let from: &[]i32 = &a[wide..];
            let to: &[]i32 = &a[..narrow];
        }",
        "i686-linux",
    )
    .0;
    for lines in [
        "    %3 = slt i64 %2, 0\n    check %3, \"slice out of bounds\"\n    \
         %5 = ult i64 4, %2\n    check %5, \"slice out of bounds\"\n    \
         %7 = trunc i64 %2 to i32\n    %8 = sub i32 4, %7\n",
        "    store i32 from.3[+4], %8\n",
        "    %13 = sext i8 %12 to i32\n    %14 = ult i32 4, %13\n",
        "    store i32 to.4[+4], %13\n",
    ] {
        assert!(text.contains(lines), "{lines}\n{text}");
    }
}

/// A slice spread into an array is counted in an integer wider than its
/// `i64` elements (the class of 65 bits), its length an `i32` on a 32-bit
/// target, and copied element by element at `i32` indexes.
#[test]
fn slice_spreads_are_target_sized() {
    let text = lower_main(
        "fn main(view: &[]i64) -> [2]i64 { return [2]i64{view...}; }",
        "i686-linux",
    )
    .0;
    for lines in [
        "    %0 = load i32 view.0[+4]\n    %1 = zext i32 %0 to i128\n    %2 = ult i128 2, %1\n",
        "    %10 = load i32 .1\n    %11 = ult i32 %10, %8\n",
        "    copy 8 result[+0, %10 * 8 of 2], view.0[*, %10 * 8]\n",
    ] {
        assert!(text.contains(lines), "{lines}\n{text}");
    }
}

#[test]
fn the_length_of_a_call_is_checked_where_it_is_used() {
    let text = lowered(
        "fn make() -> [0]i32 { return [0]i32{}; }
        fn main() -> i32 { let x: usize = 7; let y = x / make().len; return 0; }",
    );
    // Known only once the call runs, it is a register, so the division by
    // it is checked as by any value.
    assert!(
        text.contains(
            "    %4 = load i64 .3\n    %5 = eq i64 %4, 0\n    check %5, \"division by zero\"\n"
        ),
        "{text}"
    );
}
