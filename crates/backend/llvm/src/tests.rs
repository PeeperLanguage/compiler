use std::collections::HashMap;
use std::path::PathBuf;
use std::process::Command;

use model::machine::Machine;
use model::mir::Class;

use crate::link::call_tool;
use crate::link::run_tool;

/// The LLVM text of the module `app:main` holding `text`, for the target
/// named `target` (`program_ll`).
fn ll(text: &str, target: &str) -> String {
    program_ll(text, target)
        .remove("app:main")
        .expect("the program has `app:main`")
}

/// The LLVM text of each module, by import path, of the program whose
/// module `app:main` holds `text`, for the target named `target`, with a
/// prelude declaring the runtime's `Exit` and the library `lib:half`, which
/// is outside the project folder as the bundled libraries are.
fn program_ll(text: &str, target: &str) -> HashMap<String, String> {
    let target = Machine::named(target).unwrap();
    let folder = PathBuf::from("/peeper-llvm-test");
    let library = PathBuf::from("/peeper-llvm-libs/lib");
    let prelude = "#[extern(\"peeper_rt_v1_exit\")]\nfn Exit(code: i32);\n";
    let project = project::Project {
        packages: HashMap::from([
            ("app".to_string(), folder.join("src")),
            ("core".to_string(), folder.join("core")),
            ("lib".to_string(), library.clone()),
        ]),
        entry: Some(folder.join("src/main.peep")),
        root: folder.clone(),
        name: "app".to_string(),
    };
    let open_files = HashMap::from([
        (folder.join("src/main.peep"), text.to_string()),
        (folder.join("core/global.peep"), prelude.to_string()),
        (
            library.join("half.peep"),
            "fn Half(a: i32, b: i32) -> i32 { return a / b; }\n".to_string(),
        ),
    ]);
    let program = project::load(&project, &open_files);
    assert!(program.checkable);
    let (modules, results) =
        super::write::write_modules(&program, target, super::BuildMode::Debug, |queue| {
            compile::run(
                &program,
                target,
                compile::Results::default(),
                false,
                false,
                |output| {
                    if let compile::Output::Function(checked) = output {
                        queue.add(
                            checked.module,
                            checked.function,
                            checked.mir.unwrap(),
                            checked.types,
                            checked.debug_types,
                        );
                    }
                },
            )
        });
    assert_eq!(results.diagnostics().count(), 0);
    modules
        .iter()
        .map(|module| {
            (
                program.modules[module.module.index()].path.clone(),
                module.text(),
            )
        })
        .collect()
}

/// A division not known at compile time checks first, branching on the
/// comparison's own `i1`, and continues in a new block, from which `&&`
/// stores its right side's value; the lowest value divided by -1 divides
/// by 1 instead.
#[test]
fn traps_continue_in_a_new_block() {
    let text = ll(
        "fn both(a: i32, b: i32) -> bool { return a > 0 && a / b > 1; }",
        "x86_64-linux",
    );
    for line in [
        "%i7.c = icmp eq i32 %i6, 0, !dbg !11",
        "br i1 %i7.c, label %bb1.1.panic, label %bb1.1, !dbg !11\nbb1.1:",
        "%i9.d = select i1 %i9.m, i32 1, i32 %i6",
        "%i9 = select i1 %i9.m, i32 %i9.n, i32 %i9.q",
        "store i8 %i10, ptr %s2, !dbg !10\n  br label %bb2",
        "call i32 @\"peeper_rt_v1_write\"(i32 2, ptr @.str.0, i32 49)",
        "call void @\"peeper_rt_v1_exit\"(i32 101)",
        "[49 x i8] c\"panic: division by zero\\0A  --> src/main.peep:1:51\\0A\"",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
    }
    let constant = ll(
        "fn f(a: u8) -> u8 { return a / 3u8 << 2u8; }",
        "x86_64-linux",
    );
    assert!(!constant.contains("panic"), "{constant}");
}

/// A panic names its file from the project folder; a file outside it, such
/// as a library's, by its whole path.
#[test]
fn panics_name_files_from_the_project_folder() {
    let text = program_ll(
        "fn f(a: i32, b: i32) -> i32 { return a / b; }",
        "x86_64-linux",
    );
    let (main, library) = (&text["app:main"], &text["lib:half"]);
    assert!(main.contains("--> src/main.peep:1:"), "{main}");
    assert!(
        library.contains("--> /peeper-llvm-libs/lib/half.peep:1:"),
        "{library}"
    );
}

/// A `c"..."` is a global string; a call of an extern function uses
/// its `#[extern]` name.
#[test]
fn strings_and_extern_calls() {
    let text = ll(
        "fn f(a: u8, b: f32) { let s = c\"hi\\n\"; Exit(3); }",
        "x86_64-linux",
    );
    for line in [
        "@.str.0 = private unnamed_addr constant [4 x i8] c\"hi\\0A\\00\"",
        "store ptr @.str.0, ptr %s2",
        "call void @\"peeper_rt_v1_exit\"(i32 3)",
        "declare void @\"peeper_rt_v1_exit\"(i32)",
        "define void @\"app:main.f\"(i8 %a0, float %a1)",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
    }
}

/// A slice's start and count are `usize`s, `i32`s on a 32-bit target:
/// a wider `i64` end is checked in its own type before it is cut to
/// one, a narrower `i8` one is extended first.
#[test]
fn slice_bounds_are_target_sized() {
    let text = ll(
        "fn f(a: [4]i32, wide: i64, narrow: i8) {\n\
             let from: &[]i32 = &a[wide..];\n\
             let to: &[]i32 = &a[..narrow];\n}",
        "i686-linux",
    );
    for line in [
        "%i3.c = icmp slt i64 %i2, 0",
        "%i5.c = icmp ult i64 4, %i2",
        "%i7 = trunc i64 %i2 to i32",
        "%i8 = sub i32 4, %i7",
        "%i9 = getelementptr inbounds [4 x i8], ptr %a0, i32 %i7",
        "store i32 %i8, ptr %i11",
        "%i13 = sext i8 %i12 to i32",
        "%i14.c = icmp ult i32 4, %i13",
        "store i32 %i13, ptr %i18",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
    }
}

/// A slice spread into an array is counted in an integer wider than its
/// `i64` elements (the class of 65 bits), its length an `i32` on a 32-bit
/// target, and copied element by element at `i32` indexes.
#[test]
fn slice_spreads_are_target_sized() {
    let text = ll(
        "fn f(view: &[]i64) -> [2]i64 {\n\
             return [2]i64{view...};\n}",
        "i686-linux",
    );
    for line in [
        "%i0 = load i32, ptr %i0.a",
        "%i1 = zext i32 %i0 to i128",
        "%i2.c = icmp ult i128 2, %i1",
        "%i12.f = getelementptr inbounds [8 x i8], ptr %i12.f.1, i32 %i10",
        "call void @llvm.memcpy.p0.p0.i64(ptr %i12.t, ptr %i12.f, i64 8, i1 false)",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
    }
}

/// A C function's integers pass as clang passes C's on each target, as
/// measured with clang 23 (its `i128` and `i256` sizes written as bytes):
/// a narrow one extended to `int`, a wider one as its register, as two
/// `i64` pieces, or in memory, cut or extended to it, and `i128` as
/// `__int128`. A Peeper function gets them as they are. A `bool` is an
/// `i8` register, which C reads as it reads an `i1`.
#[test]
fn extern_calls_pass_integers_as_c_does() {
    let externs = "#[extern(\"c_narrow\")]\nfn narrow(a: i8, b: u8, c: bool, d: i32) -> i16;\n\
         #[extern(\"c_limbs\")]\nfn limbs(a: u65) -> i100;\n\
         #[extern(\"c_i128\")]\nfn wide(a: i128) -> i128;\n\
         #[extern(\"c_full\")]\nfn full(a: i65, b: i65, c: i32, d: i65, e: i65, f: i64) -> i65;\n\
         fn local(a: i8) {}\n";
    let calls = "local(1i8); narrow(1i8, 2u8, true, 4); limbs(5u65); wide(6i128); \
         full(1i65, 2i65, 3, 4i65, 5i65, 6);";
    // C has no integer wider than 128 bits on AArch64.
    let memory = "#[extern(\"c_memory\")]\nfn memory(a: i200, b: i65) -> i200;\n";
    let program = |wider: bool| {
        if wider {
            format!("{externs}{memory}fn f() {{ {calls} memory(7i200, 8i65); }}")
        } else {
            format!("{externs}fn f() {{ {calls} }}")
        }
    };
    let expected: [(&str, &[&str]); 5] = [
        (
            "x86_64-linux",
            &[
                "declare signext i16 @\"c_narrow\"(i8 signext, i8 zeroext, i8 zeroext, i32)",
                "declare { i64, i64 } @\"c_limbs\"(i64, i64)",
                "declare i128 @\"c_i128\"(i128)",
                // Two limbs that no longer fit in registers go whole on the stack.
                "declare { i64, i64 } @\"c_full\"(i64, i64, i64, i64, i32, \
                 ptr byval([16 x i8]) align 8, ptr byval([16 x i8]) align 8, i64)",
                "declare void @\"c_memory\"(ptr sret([32 x i8]) align 8, \
                 ptr byval([32 x i8]) align 8, i64, i64)",
                "call void @\"app:main.local\"(i8 1)",
                "%i2.0.s1 = lshr i128 5, 64",
                "%i2.0.p1 = trunc i128 %i2.0.s1 to i64",
                "%i2.c = call { i64, i64 } @\"c_limbs\"(i64 %i2.0.p0, i64 %i2.0.p1)",
                "store { i64, i64 } %i2.c, ptr %i2.r, align 16",
                "%i2 = load i128, ptr %i2.r, align 16",
                "%i10.0.x = sext i200 %i9 to i256",
                "store i256 %i10.0.x, ptr %i10.0, align 8",
                "%i10.w = load i256, ptr %i10.r, align 8",
                "%i10 = trunc i256 %i10.w to i200",
            ],
        ),
        (
            "i686-linux",
            &[
                "declare signext i16 @\"c_narrow\"(i8 signext, i8 zeroext, i8 zeroext, i32)",
                "declare void @\"c_limbs\"(ptr sret([16 x i8]) align 4, ptr)",
                "declare void @\"c_i128\"(ptr sret([16 x i8]) align 4, ptr)",
                "declare void @\"c_full\"(ptr sret([12 x i8]) align 4, ptr, ptr, i32, ptr, ptr, i64)",
                "declare void @\"c_memory\"(ptr sret([28 x i8]) align 4, ptr, ptr)",
                // 65 to 96 bits take 12 bytes: cut from the register's 16.
                "%i2.0.x = trunc i128 5 to i96",
                "store i96 %i2.0.x, ptr %i2.0, align 4",
                "%i6.w = load i96, ptr %i6.r, align 4",
                "%i6 = sext i96 %i6.w to i128",
                "%i10.0.x = sext i200 %i9 to i224",
            ],
        ),
        (
            "x86_64-windows",
            &[
                "declare i16 @\"c_narrow\"(i8, i8, i8 zeroext, i32)",
                "declare void @\"c_limbs\"(ptr sret([16 x i8]) align 8, ptr)",
                "declare <2 x i64> @\"c_i128\"(ptr)",
                "declare void @\"c_full\"(ptr sret([16 x i8]) align 8, ptr, ptr, i32, ptr, ptr, i64)",
                "declare void @\"c_memory\"(ptr sret([32 x i8]) align 8, ptr, ptr)",
                "%i5.0 = alloca [16 x i8], align 16",
                "%i5.c = call <2 x i64> @\"c_i128\"(ptr %i5.0)",
            ],
        ),
        (
            "aarch64-linux",
            &[
                // As registers of their classes, which AArch64 passes as
                // it passes `i65` and `i100`.
                "declare i16 @\"c_narrow\"(i8, i8, i8, i32)",
                "declare i128 @\"c_limbs\"(i128)",
                "declare i128 @\"c_full\"(i128, i128, i32, i128, i128, i64)",
            ],
        ),
        (
            "aarch64-macos",
            &["declare signext i16 @\"c_narrow\"(i8 signext, i8 zeroext, i8 zeroext, i32)"],
        ),
    ];
    for (target, lines) in expected {
        let text = ll(&program(!target.starts_with("aarch64")), target);
        for line in lines {
            assert!(text.contains(line), "{target}: {line}\n{text}");
        }
    }
}

/// A variable of an inner block is in that block's scope, so a debugger
/// shows the outer variable of the same name outside it. A block that
/// declares nothing gets no scope: the inner one is in the function's.
#[test]
fn variables_have_the_scope_of_their_block() {
    let text = ll(
        "fn f(p: i32) { let x = 1; { { let x = 2; } } }",
        "x86_64-linux",
    );
    let blocks: Vec<&str> = text
        .lines()
        .filter(|line| line.contains("!DILexicalBlock("))
        .collect();
    let variables: Vec<&str> = text
        .lines()
        .filter(|line| line.contains("!DILocalVariable(name: \"x\""))
        .collect();
    assert_eq!((blocks.len(), variables.len()), (1, 2), "{text}");
    let block = blocks[0]
        .split(" = ")
        .next()
        .unwrap()
        .trim_start_matches('!');
    assert_eq!(scope(variables[1]), block, "{text}");
    assert_eq!(scope(blocks[0]), scope(variables[0]), "{text}");
}

/// A value kept in memory is bytes to LLVM, however it nests: an index steps
/// by its element's size, and fields and constant indexes add up to one
/// offset after it.
#[test]
fn values_in_memory_are_bytes() {
    let text = ll(
        "struct Cell { tag: i64, grid: [2][3]i32 }
        fn f(cells: &mut [2]Cell, i: i64, j: i64) -> i32 { return cells.*[i].grid[1][j]; }",
        "x86_64-linux",
    );
    for line in [
        "%i9.a.2 = getelementptr inbounds [32 x i8], ptr %i9.a.1, i64 %i3",
        "%i9.a.3 = getelementptr inbounds i8, ptr %i9.a.2, i64 20",
        "%i9.a = getelementptr inbounds [4 x i8], ptr %i9.a.3, i64 %i6",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
    }
    let nested = ll(
        "fn f() -> i32 { let g = [2][3]i32{[3]i32{1, 2, 3}, [3]i32{4, 5, 6}}; return g[1][2]; }",
        "i686-linux",
    );
    assert!(nested.contains("alloca [24 x i8], align 4"), "{nested}");
    assert!(!nested.contains("x i32]"), "{nested}");
}

/// The address of a place no step moves from, as `a[0]`, is its root's:
/// no instruction computes it, and the value that uses it names the root.
#[test]
fn an_address_at_no_offset_is_its_root() {
    let text = ll(
        "fn f(a: [2]i32) -> i32 { let r = &a[0]; return r.*; }",
        "x86_64-linux",
    );
    assert!(text.contains("store ptr %a0, ptr %s1"), "{text}");
}

/// A jump goes past a block that only jumps on (the `break` alone in a branch,
/// the `if`'s join at the end of the pass), and the code no path reaches (the
/// join after two `return`s) is not written.
#[test]
fn jumps_skip_empty_blocks_and_unreached_ones_are_left_out() {
    let text = ll(
        "fn f(n: i32) -> i32 { for { if n > 1 { break; } } \
         if n > 0 { return 1; } else { return 2; } }",
        "x86_64-linux",
    );
    let lines: Vec<&str> = text.lines().collect();
    let only_jumps = lines.windows(3).any(|window| {
        window[0].starts_with("bb")
            && window[1].starts_with("  br label")
            && !window[2].starts_with("  ")
    });
    assert!(!only_jumps, "{text}");
    assert!(!text.contains("unreachable"), "{text}");
}

/// A comparison is widened to a byte only where its value is kept: here the
/// one stored in `b` and the one returned, not the `if` condition's.
#[test]
fn comparisons_widen_only_when_kept() {
    let text = ll(
        "fn f(a: i32) -> bool { let b = a > 1; if a < 0 { return b; } return a == 3; }",
        "x86_64-linux",
    );
    assert_eq!(text.matches("zext i1").count(), 2, "{text}");
}

/// A pointer breaks a flat array, so its debug type recurses through each one,
/// and stops where clang would run out of stack on it.
#[test]
fn debug_types_stop_at_deep_pointers() {
    let source = format!("fn f(a: {}i32) {{ }}", "[1]*".repeat(50_000));
    let text = ll(&source, "x86_64-linux");
    let pointers = text.matches("DW_TAG_pointer_type").count();
    assert!((5_000..5_100).contains(&pointers), "{pointers}");
    assert!(text.contains("DW_TAG_pointer_type, baseType: null"));
}

/// Debug info goes through every struct in a chain, and the debug types stop
/// where clang would run out of stack on them.
#[test]
fn deep_structs_have_debug_types() {
    const DEPTH: usize = 20_000;
    let chain: String = (1..DEPTH)
        .map(|i| format!("struct S{i} {{ s: S{} }}\n", i - 1))
        .collect();
    let last = DEPTH - 1;
    let source = format!("struct S0 {{ n: i32 }}\n{chain}fn f(a: &S{last}) {{}}");
    let text = ll(&source, "x86_64-linux");
    let members = text.matches("DW_TAG_member").count();
    assert!((4_900..5_100).contains(&members), "{members}");
}

/// LLVM refuses a column past 16 bits, so a location or a block further along
/// a line keeps the last column it can hold. Clang fails on the module without
/// this, in a debug build only: a release build has no locations.
#[test]
fn columns_past_16_bits_stop_at_the_last_one() {
    let padding = " ".repeat(usize::from(u16::MAX));
    let text = ll(
        &format!("fn f() {{ {padding}{{ let x = 1; }} }}"),
        "x86_64-linux",
    );
    let largest = |node: &str| {
        (text.lines().filter(|line| line.contains(node)))
            .map(|line| line.split("column: ").nth(1).unwrap())
            .map(|rest| rest.split([',', ')']).next().unwrap())
            .map(|column| column.parse::<u32>().unwrap())
            .max()
    };
    assert_eq!(largest("!DILocation("), Some(65_535), "{text}");
    assert_eq!(largest("!DILexicalBlock("), Some(65_535), "{text}");
}

/// The metadata id after `scope: !` in the metadata `line`.
fn scope(line: &str) -> &str {
    line.split("scope: !")
        .nth(1)
        .unwrap()
        .split(',')
        .next()
        .unwrap()
}

/// A constant is written from its bits as the class reads them: an integer
/// as signed, a float as its value's `f64` bits, a NaN with its payload.
#[test]
fn constants_from_bits() {
    let constant = |class, bits| crate::body::constant_text(class, bits).to_string();
    assert_eq!(constant(Class::I8, [0xff, 0]), "-1");
    assert_eq!(constant(Class::I8, [u64::MAX, 0]), "-1");
    assert_eq!(constant(Class::I32, [4, 0]), "4");
    assert_eq!(constant(Class::I128, [5, 1]), "18446744073709551621");
    assert_eq!(constant(Class::I128, [u64::MAX, u64::MAX]), "-1");
    assert_eq!(
        constant(Class::F32, [u64::from(0.5f32.to_bits()), 0]),
        "0x3FE0000000000000"
    );
    // A signaling NaN keeps its payload, which a conversion would quiet.
    assert_eq!(constant(Class::F32, [0x7F80_0001, 0]), "0x7FF0000020000000");
    assert_eq!(
        constant(Class::F64, [1.5f64.to_bits(), 0]),
        "0x3FF8000000000000"
    );
    assert_eq!(constant(Class::Pointer, [0, 0]), "null");
}

/// A tool that cannot start, or that fails, is named in the error with what
/// it printed as errors; `call_tool` says first what it was for.
#[test]
fn a_failing_tool_is_named_with_what_it_printed() {
    let missing = Command::new("peeper-no-such-tool");
    let error = call_tool(missing, "link the program").unwrap_err();
    assert!(
        error.starts_with("cannot link the program: cannot run peeper-no-such-tool: "),
        "{error}"
    );
    if cfg!(unix) {
        let mut failing = Command::new("sh");
        failing.args(["-c", "echo kept; echo broken >&2; exit 3"]);
        let error = run_tool(&mut failing).unwrap_err();
        assert_eq!(error, "sh failed (exit status: 3)\nbroken");
        let mut working = Command::new("sh");
        working.args(["-c", "echo kept"]);
        assert_eq!(run_tool(&mut working), Ok(b"kept\n".to_vec()));
    }
}
