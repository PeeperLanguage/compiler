use std::collections::HashMap;
use std::path::PathBuf;

use bigint::Limbs;
use model::constant::Constant;
use model::constant::Value;
use model::machine::Machine;
use model::semantic_types::TypeId;
use model::semantic_types::Types;

/// The LLVM text of the module `app:main` holding `text`, for the
/// target named `target`, with a prelude declaring the runtime's `Exit`.
fn ll(text: &str, target: &str) -> String {
    let target = Machine::named(target).unwrap();
    let folder = PathBuf::from("/peeper-llvm-test");
    let prelude = "#[extern(\"peeper_rt_v1_exit\")]\nfn Exit(code: i32);\n";
    let project = project::Project {
        packages: HashMap::from([
            ("app".to_string(), folder.join("src")),
            ("core".to_string(), folder.join("core")),
        ]),
        entry: folder.join("src/main.peep"),
        root: folder.clone(),
        name: "app".to_string(),
    };
    let open_files = HashMap::from([
        (folder.join("src/main.peep"), text.to_string()),
        (folder.join("core/global.peep"), prelude.to_string()),
    ]);
    let program = project::load(&project, &open_files);
    assert!(program.checkable);
    let (modules, results) =
        super::write::write_modules(&program, target, super::BuildMode::Debug, |queue| {
            compile::run(
                &program,
                target.pointer_bits,
                compile::Results::default(),
                false,
                |output| {
                    if let compile::Output::Function(compile::CheckedFunction {
                        definition,
                        mir,
                        types,
                        ..
                    }) = output
                    {
                        queue.add(definition, mir.unwrap(), types);
                    }
                },
            )
        });
    assert_eq!(results.diagnostics().count(), 0);
    modules.last().unwrap().text()
}

/// A division not known at compile time checks first and continues in
/// a new block, which the `phi` after `&&` names.
#[test]
fn traps_continue_in_a_new_block() {
    let text = ll(
        "fn both(a: i32, b: i32) -> bool { return a > 0 && a / b > 1; }",
        "x86_64-linux",
    );
    for line in [
        "br i1 %i6.z, label %bb1.1.panic, label %bb1.1\nbb1.1:",
        "%i6.d = select i1 %i6.n, i32 1, i32 %i5",
        "%i8 = phi i1 [ false, %bb0 ], [ %i7, %bb1.1 ]",
        "call i32 @\"peeper_rt_v1_write\"(i32 2, ptr @.str.0, i32 67)",
        "call void @\"peeper_rt_v1_exit\"(i32 101)",
        "c\"panic: division by zero\\0A  --> /peeper-llvm-test/src/main.peep:1:51\\0A\\00\"",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
    }
    let constant = ll(
        "fn f(a: u8) -> u8 { return a / 3u8 << 2u8; }",
        "x86_64-linux",
    );
    assert!(!constant.contains("panic"), "{constant}");
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
        "store ptr @.str.0, ptr %v2",
        "call void @\"peeper_rt_v1_exit\"(i32 3)",
        "declare void @\"peeper_rt_v1_exit\"(i32)",
        "define void @\"app:main.f\"(i8 %arg0, float %arg1)",
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
             let from: [&]i32 = &a[wide..];\n\
             let to: [&]i32 = &a[..narrow];\n}",
        "i686-linux",
    );
    for line in [
        "%i4 = icmp slt i64 %i3, 0",
        "%i6 = icmp sgt i64 %i3, 4",
        "%i8 = trunc i64 %i3 to i32",
        "%i9 = sub i32 4, %i8",
        "%i10.p = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i32 %i8",
        "%i10 = insertvalue %slice %i10.a, i32 %i9, 1",
        "%i13 = sext i8 %i12 to i32",
        "%i14 = icmp ugt i32 %i13, 4",
        "%i16 = insertvalue %slice %i16.a, i32 %i13, 1",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
    }
}

/// A slice spread into an array is counted in an integer one bit wider
/// than its `i64` elements, its length an `i32` on a 32-bit target, and
/// copied by a byte count extended to the `i64` `memcpy` takes.
#[test]
fn slice_spreads_are_target_sized() {
    let text = ll(
        "fn f(view: [&]i64) -> [2]i64 {\n\
             return [2]i64{view...};\n}",
        "i686-linux",
    );
    for line in [
        "%i2.r0.len = extractvalue %slice %i1, 1",
        "%i2.r0.count = zext i32 %i2.r0.len to i65",
        "%i2.r0.over = icmp ugt i65 %i2.r0.count, 2",
        "%i2.s0.bytes = mul i32 %i2.r0.len, 8",
        "%i2.s0.size = zext i32 %i2.s0.bytes to i64",
        "call void @llvm.memcpy.p0.p0.i64(ptr %i2.0, ptr %i2.s0, i64 %i2.s0.size, i1 false)",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
    }
}

/// C code gets a narrow integer extended to `int` by its signedness; a
/// Peeper function gets it as it is.
#[test]
fn extern_calls_extend_narrow_integers() {
    let text = ll(
        "#[extern(\"c_narrow\")]\nfn narrow(a: i8, b: u8, c: bool, d: i32) -> i16;\n\
             fn local(a: i8) {}\n\
             fn f() { local(1i8); let x = narrow(1i8, 2u8, true, 4); }",
        "x86_64-linux",
    );
    for line in [
        "declare signext i16 @\"c_narrow\"(i8 signext, i8 zeroext, i1 zeroext, i32)",
        "call signext i16 @\"c_narrow\"(i8 signext 1, i8 zeroext 2, i1 zeroext true, i32 4)",
        "call void @\"app:main.local\"(i8 1)",
    ] {
        assert!(text.contains(line), "{line}\n{text}");
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

/// The metadata id after `scope: !` in the metadata `line`.
fn scope(line: &str) -> &str {
    line.split("scope: !")
        .nth(1)
        .unwrap()
        .split(',')
        .next()
        .unwrap()
}

/// Each struct's field offsets and size match what LLVM gives on x86-64
/// and AArch64, and with 32-bit pointers on i686 (measured with
/// `getelementptr` folded by clang `-O1`, `-m32` for i686).
#[test]
fn struct_layout_matches_llvm() {
    let mut types = Types::new();
    let mut integer = |name: &str| types.builtin(name, 64).unwrap();
    let (i8, i24, i40, i128, i200) = (
        integer("i8"),
        integer("i24"),
        integer("i40"),
        integer("i128"),
        integer("i200"),
    );
    let layout = |fields: &[TypeId]| {
        let (offsets, size, _) =
            crate::layout::struct_layout(&types, fields, Machine::named("x86_64-linux").unwrap());
        (offsets, size)
    };
    assert_eq!(layout(&[TypeId::I32, TypeId::I64]), (vec![0, 8], 16));
    assert_eq!(
        layout(&[TypeId::BOOL, i128, TypeId::I32]),
        (vec![0, 16, 32], 48)
    );
    assert_eq!(layout(&[i8, i24, i8]), (vec![0, 4, 8], 12));
    assert_eq!(layout(&[i8, i200, i8]), (vec![0, 16, 48], 64));
    assert_eq!(layout(&[i8, i40]), (vec![0, 8], 16));
    assert_eq!(
        layout(&[i8, TypeId::F64, TypeId::CSTR]),
        (vec![0, 8, 16], 24)
    );
    assert_eq!(layout(&[]), (vec![], 0));
    let i686 = |fields: &[TypeId]| {
        let (offsets, size, _) =
            crate::layout::struct_layout(&types, fields, Machine::named("i686-linux").unwrap());
        (offsets, size)
    };
    assert_eq!(i686(&[TypeId::I32, TypeId::I64]), (vec![0, 4], 12));
    assert_eq!(
        i686(&[TypeId::BOOL, i128, TypeId::I32]),
        (vec![0, 16, 32], 48)
    );
    assert_eq!(i686(&[i8, i24, i8]), (vec![0, 4, 8], 12));
    assert_eq!(i686(&[i8, i200, i8]), (vec![0, 16, 48], 64));
    assert_eq!(i686(&[i8, i40]), (vec![0, 4], 12));
    assert_eq!(i686(&[i8, TypeId::F64, TypeId::CSTR]), (vec![0, 4, 12], 16));
}

#[test]
fn constants_from_bits() {
    let mut types = Types::new();
    let u200 = types.builtin("u200", 64).unwrap();
    let i3 = types.builtin("i3", 64).unwrap();
    let constant = |ty: TypeId, value: Value| {
        crate::text::constant_text(&types, &Constant { ty, value }).to_string()
    };
    assert_eq!(
        constant(TypeId::I8, Value::Integer(Limbs::new(8, &[0xff]))),
        "-1"
    );
    assert_eq!(constant(i3, Value::Integer(Limbs::new(3, &[4]))), "-4");
    assert_eq!(
        constant(TypeId::U8, Value::Integer(Limbs::new(8, &[0xff]))),
        "255"
    );
    assert_eq!(
        constant(u200, Value::Integer(Limbs::new(200, &[5, 0, 0, 1]))),
        "u0x0000000000000001000000000000000000000000000000000000000000000005"
    );
    assert_eq!(constant(TypeId::BOOL, Value::Bool(true)), "true");
    assert_eq!(
        constant(TypeId::F32, Value::Float(0.5f64.to_bits())),
        "0x3FE0000000000000"
    );
}
