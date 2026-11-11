//! Each kind of THIR node lowered into the MIR of core concepts, read as
//! its dump text.

use std::sync::Arc;

use check::Checker;
use model::DefId;
use model::FunctionCopy;
use model::ModuleId;
use model::ast;
use model::core_mir::DebugTypes;
use model::machine::Machine;
use model::semantic_types::ConstArguments;
use model::thir;
use source::SourceFile;

/// The MIR text of the function `main` of the one-module program `text`,
/// which must check without errors, for a 64-bit target.
fn lowered(text: &str) -> String {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (ast, parse) = parser::parse(&file);
    assert!(parse.is_empty(), "{parse:?}");
    let modules = [model::program::Module {
        path: "app:main".to_string(),
        ast,
        imports: Vec::new(),
    }];
    let target = Machine::named("x86_64-linux").unwrap();
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
    let main = FunctionCopy {
        definition: DefId {
            module: ModuleId::from_index(0),
            index: index as u32,
        },
        arguments: ConstArguments::NONE,
    };
    let ty = checker.copy_type(main, name);
    let mut body = thir::Body::default();
    assert!(checker.check_body(main, name, &mut body, false));
    let reported: Vec<String> = (checker.module_checks.iter())
        .flat_map(|module| &module.diagnostics)
        .map(|diagnostic| diagnostic.message.clone())
        .collect();
    assert!(reported.is_empty(), "{reported:?}");
    let mut debug_types = DebugTypes::default();
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
        |function: FunctionCopy| model::program::declared_path(&modules, function.definition);
    mir.text(&file, name_of)
}

// The block after a `return` is unreachable; it still ends the scopes, as
// the old lowering does, and cleanup removes it.

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
    store i32 .1[+0], 1
    store i64 .1[+8], 2
    copy 16 p.0, .1
    dead .1
    %4 = load i64 p.0[+8]
    dead p.0
    return %4
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
            "    moved p.0\n    copy 16 .2, p.0\n    %6 = address .2\n    %7 = call sum(%6)\n"
        ),
        "{text}"
    );
}

#[test]
fn a_struct_result_is_written_where_a_hidden_argument_points() {
    let text = lowered(
        "struct Point { x: i32, y: i64 }
fn main() -> i32 { let p = make(); return p.x; }
fn make() -> Point { return Point.{ x = 1, y = 2 }; }",
    );
    assert!(
        text.contains("    %0 = address .1\n    call make(%0)\n    copy 16 p.0, .1\n"),
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
        text.contains("    %1 = borrow mut a.0 as reference\n    store ptr r.1, %1\n"),
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
            "    %6 = load i64 i.1\n    %7 = ule i64 3, %6\n    \
             check %7, \"index out of bounds: the length is 3\"\n    %9 = load i32 a.0[%6 * 4]\n"
        ),
        "{text}"
    );
}

#[test]
fn a_slice_is_two_words_and_its_elements_are_behind_the_first() {
    let text =
        lowered("fn main() -> i32 { let a = [3]i32{1, 2, 3}; let s = &a[1..]; return s[0]; }");
    assert!(text.contains("borrow a.0[1 * 4] as slice\n"), "{text}");
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
    let text = lowered("fn main() { let a = 5; println(\"a is\", a, true); }");
    // The `&str` is its two words; each space is one call; `true` and
    // `false` are printed by the branch taken; the newline ends it.
    assert!(
        text.contains("    store ptr .1, global 0\n    store i64 .1[+8], 4\n"),
        "{text}"
    );
    assert!(
        text.contains("call peeper_rt_v1_print_bytes(%5, %6)\n"),
        "{text}"
    );
    assert!(
        text.contains("call peeper_rt_v2_print_int(1, 1, %10)\n"),
        "{text}"
    );
    assert!(
        text.contains("  bb1:\n    call peeper_rt_v1_print_bytes(global 3, 4)\n"),
        "{text}"
    );
    assert!(
        text.contains("  bb2:\n    call peeper_rt_v1_print_bytes(global 4, 5)\n"),
        "{text}"
    );
    assert!(
        text.contains(
            "call peeper_rt_v1_print_bytes(global 5, 1)\n    call peeper_rt_v1_flush()\n"
        ),
        "{text}"
    );
}

#[test]
fn an_interface_value_is_an_address_and_a_table() {
    let text = lowered(
        "struct Square { side: i32 }
iface Shape { fn area(&self) -> i32 }
fn Square::area(&self) -> i32 { return self.side * self.side; }
fn main() -> i32 { let s = Square.{ side = 3 }; let shape: &Shape = &s; return shape.area(); }",
    );
    assert!(text.contains("borrow s.0 as reference\n"), "{text}");
    assert!(text.contains("store ptr .3[+8], global 0\n"), "{text}");
    // The call lends what the reference views again, then reads the
    // method from the table.
    assert!(
        text.contains(
            "    %8 = borrow shape.1[*] as interface reference\n    %9 = load ptr shape.1[+8]\n"
        ),
        "{text}"
    );
    assert!(
        text.contains(
            "    %12 = load ptr .4\n    %13 = load ptr .4[+8]\n    \
             %14 = load ptr at %13[+0]\n    %15 = call through %14(%12)\n"
        ),
        "{text}"
    );
}

/// Every fixture program lowers, on its own target: no THIR shape makes
/// the lowering give up. Errors a negative fixture expects leave its
/// bodies unlowered, as the old lowering does.
#[test]
fn every_fixture_lowers() {
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../..");
    let libraries = root.join("build/libs");
    let mut lowered = 0;
    for entry in std::fs::read_dir(root.join("x_test")).unwrap() {
        let folder = entry.unwrap().path();
        let Ok(manifest) = std::fs::read_to_string(folder.join("peeper.toml")) else {
            continue;
        };
        let target = manifest
            .lines()
            .find_map(|line| line.strip_prefix("target = \"")?.strip_suffix('"'))
            .unwrap_or("x86_64-linux");
        let target = Machine::named(target).unwrap();
        let project = project::find(&folder, &libraries).unwrap();
        let program = project::load(&project, &std::collections::HashMap::<_, String>::new());
        if !program.checkable {
            continue;
        }
        let mut checker = Checker::new(&program.modules, target, false);
        let mut debug_types = DebugTypes::default();
        let mut body = thir::Body::default();
        let mut lower_function = |checker: &mut Checker, function: FunctionCopy, span| {
            let ty = checker.copy_type(function, span);
            if !checker.check_body(function, span, &mut body, false) {
                return;
            }
            let lowered_body = super::lower(
                &body,
                ty,
                &checker.types,
                target,
                &program.modules,
                &mut debug_types,
            );
            if let Some(mir) = lowered_body {
                let name_of = |function: FunctionCopy| {
                    model::program::declared_path(&program.modules, function.definition)
                };
                let file = &program.modules[function.definition.module.index()].ast.file;
                assert!(!mir.text(file, name_of).is_empty());
                lowered += 1;
            }
        };
        for (index, module) in program.modules.iter().enumerate() {
            let id = ModuleId::from_index(index);
            let tree = &module.ast;
            for (position, declaration) in tree.declarations().enumerate() {
                let definition = DefId {
                    module: id,
                    index: position as u32,
                };
                match tree.decls[declaration] {
                    ast::Decl::Function(function)
                        if check::declaration::const_parameter(tree, function.signature)
                            .is_some() =>
                    {
                        checker.function_type(definition, function.name.span);
                    }
                    ast::Decl::Function(function) => {
                        let copy = FunctionCopy {
                            definition,
                            arguments: ConstArguments::NONE,
                        };
                        lower_function(&mut checker, copy, function.name.span);
                    }
                    ast::Decl::Const { name, .. } => {
                        checker.constant(definition, name.span);
                    }
                    ast::Decl::Type(written) | ast::Decl::Alias(written) => {
                        checker.declared_type(definition, written.name.span);
                    }
                }
            }
            while let Some(copy) = checker.next_copy() {
                let span = model::program::declared_name(&program.modules, copy.definition)
                    .0
                    .span;
                lower_function(&mut checker, copy, span);
            }
        }
    }
    assert!(lowered > 500, "{lowered} bodies lowered");
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
fn a_signed_shift_count_is_checked_however_narrow() {
    let text = lowered("fn main() -> i32 { let c: i5 = -1; let x = 8; return x << c; }");
    // An `i5` cannot reach 32, but it can be negative: read unsigned, that
    // is above the width, so the check stays.
    assert!(
        text.contains("check %4, \"a shift count must be from 0 to 31\"\n"),
        "{text}"
    );
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
