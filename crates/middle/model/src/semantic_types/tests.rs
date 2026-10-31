use super::Conversion;
use super::FIXED;
use super::Float;
use super::Integer;
use super::Type;
use super::TypeId;
use super::Types;
use super::builtin_doc;
use super::integer;
use crate::DefId;
use crate::ModuleId;

/// Every built-in type name has a doc; an integer's range is right at
/// each edge of how it is worked out, and one as wide as a pointer says
/// so.
#[test]
fn builtin_docs() {
    for name in super::builtin_names() {
        assert!(builtin_doc(name, 64).is_some(), "{name}");
    }
    assert_eq!(builtin_doc("Meters", 64), None);
    assert!(builtin_doc(super::RANGE, 64).is_some());
    let doc = |name: &str, pointer_bits: u32| builtin_doc(name, pointer_bits).unwrap();
    assert_eq!(doc("i1", 64), "A signed 1-bit integer: from `-1` to `0`.");
    assert_eq!(
        doc("u8", 64),
        "An unsigned 8-bit integer: from `0` to `255`."
    );
    assert!(doc("i128", 64).ends_with("from `-170141183460469231731687303715884105728` to `170141183460469231731687303715884105727`."));
    assert!(doc("u128", 64).ends_with("to `340282366920938463463374607431768211455`."));
    assert!(doc("i129", 64).ends_with("from `-2^128` to `2^128 - 1`."));
    assert!(doc("u256", 64).ends_with("from `0` to `2^256 - 1`."));
    assert_eq!(
        doc("usize", 64),
        "An unsigned integer as wide as a pointer, 64 bits on the target: from `0` to `18446744073709551615`."
    );
    assert!(doc("isize", 32).starts_with("A signed integer as wide as a pointer, 32 bits"));
}

#[test]
fn fixed_ids_are_the_interned_ids() {
    let mut types = Types::default();
    for (index, ty) in FIXED.iter().enumerate() {
        assert_eq!(types.intern(ty.clone()), TypeId(index as u32));
    }
    assert_eq!(types.intern(integer(true, 32)), TypeId::I32);
    assert_eq!(types.intern(integer(false, 128)), TypeId::U128);
    assert_eq!(types[TypeId::U8], integer(false, 8));
    assert_eq!(types[TypeId::BOOL], Type::Bool);
    assert_eq!(types[TypeId::F64], Type::Float(Float::F64));
    assert_eq!(types[TypeId::CSTR], Type::CString);
    assert_eq!(types[TypeId::RAWPTR], Type::RawPointer { target: None });
    assert_eq!(
        types.builtin("rawptr", 64),
        None,
        "a keyword, not a type name"
    );
}

#[test]
fn literals_that_fit() {
    let fits = |signed, bits, magnitude: &[u64], negative| {
        Integer { signed, bits }.fits(magnitude, negative)
    };
    assert!(fits(true, 8, &[127], false));
    assert!(!fits(true, 8, &[128], false));
    assert!(fits(true, 8, &[128], true), "-128");
    assert!(!fits(true, 8, &[129], true));
    assert!(fits(false, 8, &[255], false));
    assert!(!fits(false, 8, &[256], false));
    assert!(!fits(false, 8, &[1], true));
    assert!(fits(false, 8, &[0], true), "-0");
    assert!(fits(false, 1, &[1], false));
    assert!(fits(true, 1, &[1], true), "-1 is the only negative i1");
    assert!(!fits(true, 1, &[1], false));
    assert!(fits(false, 64, &[u64::MAX], false));
    assert!(!fits(false, 64, &[0, 1], false));
    assert!(fits(true, 128, &[0, 1 << 63], true), "i128::MIN");
    assert!(!fits(true, 128, &[1, 1 << 63], true));
    assert!(fits(false, 256, &[u64::MAX; 4], false));
    assert!(fits(false, 8, &[200, 0, 0], false), "high zero limbs");
}

#[test]
fn conversions() {
    let mut types = Types::default();
    let shared = types.intern(Type::Reference {
        mutable: false,
        target: TypeId::I32,
    });
    let mutable = types.intern(Type::Reference {
        mutable: true,
        target: TypeId::I32,
    });
    let other = types.intern(Type::Reference {
        mutable: false,
        target: TypeId::I64,
    });
    let array = types.intern(Type::Array {
        element: TypeId::I32,
        length: 3,
    });
    let to_array = types.intern(Type::Reference {
        mutable: false,
        target: array,
    });
    let to_mutable_array = types.intern(Type::Reference {
        mutable: true,
        target: array,
    });
    let view = types.intern(Type::Slice {
        mutable: false,
        element: TypeId::I32,
    });
    let edit = types.intern(Type::Slice {
        mutable: true,
        element: TypeId::I32,
    });
    let wide_view = types.intern(Type::Slice {
        mutable: false,
        element: TypeId::I64,
    });
    let untyped = TypeId::RAWPTR;
    let to_i32 = types.intern(Type::RawPointer {
        target: Some(TypeId::I32),
    });
    let to_i64 = types.intern(Type::RawPointer {
        target: Some(TypeId::I64),
    });
    let cases = [
        (TypeId::U8, TypeId::I16, Conversion::Implicit),
        (TypeId::I8, TypeId::I16, Conversion::Implicit),
        (TypeId::U8, TypeId::U16, Conversion::Implicit),
        (TypeId::F32, TypeId::F64, Conversion::Implicit),
        (TypeId::I32, TypeId::I32, Conversion::Same),
        (TypeId::I8, TypeId::U16, Conversion::NeedsCast), // -1 is no u16
        (TypeId::U8, TypeId::I8, Conversion::NeedsCast),  // 255 is no i8
        (TypeId::I16, TypeId::I8, Conversion::NeedsCast),
        (TypeId::I32, TypeId::F64, Conversion::NeedsCast),
        (TypeId::F64, TypeId::I32, Conversion::NeedsCast),
        (TypeId::F64, TypeId::F32, Conversion::NeedsCast),
        (TypeId::BOOL, TypeId::U8, Conversion::NeedsCast),
        (TypeId::I32, TypeId::BOOL, Conversion::Never),
        (TypeId::BOOL, TypeId::F64, Conversion::Never),
        (mutable, shared, Conversion::Reborrow),
        (shared, mutable, Conversion::Never),
        (mutable, other, Conversion::Never),
        (shared, TypeId::I32, Conversion::Never),
        (to_array, view, Conversion::Reborrow),
        (to_mutable_array, edit, Conversion::Reborrow),
        (to_mutable_array, view, Conversion::Reborrow),
        (edit, view, Conversion::Reborrow),
        (to_array, edit, Conversion::Never),
        (view, edit, Conversion::Never),
        (to_array, wide_view, Conversion::Never),
        (edit, wide_view, Conversion::Never),
        (shared, view, Conversion::Never), // `&i32` is no array
        (array, view, Conversion::Never),  // a slice is made only with `&`
        (TypeId::CSTR, TypeId::I64, Conversion::Never),
        (to_i32, untyped, Conversion::Same),
        (untyped, to_i32, Conversion::NeedsCast),
        (to_i32, to_i64, Conversion::NeedsCast),
        (untyped, TypeId::U64, Conversion::Never),
    ];
    for (from, to, conversion) in cases {
        assert_eq!(types.conversion(from, to), conversion, "{from:?} to {to:?}");
    }
}

#[test]
fn builtin_names() {
    let mut types = Types::default();
    let mut lookup = |name| types.builtin(name, 64);
    assert_eq!(lookup("bool"), Some(TypeId::BOOL));
    assert_eq!(lookup("i32"), Some(TypeId::I32));
    assert_eq!(lookup("u128"), Some(TypeId::U128));
    assert_eq!(
        lookup("usize"),
        Some(TypeId::U64),
        "a pointer is 64 bits here"
    );
    assert_eq!(lookup("isize"), Some(TypeId::I64));
    assert_eq!(lookup("f32"), Some(TypeId::F32));
    assert_eq!(lookup("cstr"), Some(TypeId::CSTR));
    let u24 = lookup("u24").unwrap();
    assert_eq!(lookup("u24"), Some(u24));
    assert!(lookup("i8388608").is_some(), "2^23 bits is the widest");
    for name in [
        "i0", "u08", "i8388609", "u", "i8x", "x8", "byte", "", "i-1", "u+8", "void",
    ] {
        assert_eq!(lookup(name), None, "{name}");
    }
    assert_eq!(types[u24], integer(false, 24));
    let mut types = Types::default();
    assert_eq!(
        types.builtin("usize", 32),
        Some(TypeId::U32),
        "a pointer is 32 bits there"
    );
}

#[test]
fn type_text() {
    let mut types = Types::default();
    let u24 = types.intern(integer(false, 24));
    let function = types.intern(Type::Function {
        parameters: [TypeId::I32, u24].into(),
        result: TypeId::BOOL,
        origins: [].into(),
        variadic: false,
    });
    let procedure = types.intern(Type::Function {
        parameters: [].into(),
        result: TypeId::VOID,
        origins: [].into(),
        variadic: false,
    });
    let declaration = |index| DefId {
        module: ModuleId::from_index(0),
        index,
    };
    let point = types.intern(Type::Named(declaration(0)));
    let place = types.intern(Type::Alias {
        definition: declaration(1),
        real: point,
    });
    let complete = types.intern(Type::Struct {
        definition: declaration(0),
        fields: [TypeId::I32, TypeId::I32].into(),
    });
    let raw = TypeId::RAWPTR;
    let typed = types.intern(Type::RawPointer { target: Some(raw) });
    let text = |ty| {
        types.text(ty, |definition| {
            ["Point", "Place"][definition.index as usize]
        })
    };
    assert_eq!(text(u24), "u24");
    assert_eq!(text(TypeId::F32), "f32");
    assert_eq!(text(TypeId::CSTR), "cstr");
    assert_eq!(text(function), "fn(i32, u24) -> bool");
    assert_eq!(text(procedure), "fn()");
    assert_eq!(text(point), "Point");
    assert_eq!(text(place), "Place (aka Point)");
    assert_eq!(text(complete), "Point");
    assert_eq!(text(raw), "rawptr");
    assert_eq!(text(typed), "*rawptr");
}

#[test]
fn equal_types_have_equal_ids() {
    let mut types = Types::default();
    let u24 = types.intern(integer(false, 24));
    assert_eq!(types.intern(integer(false, 24)), u24);
    assert_ne!(types.intern(integer(true, 24)), u24);
    let function = |parameters: &[TypeId]| Type::Function {
        parameters: parameters.into(),
        result: TypeId::VOID,
        origins: [].into(),
        variadic: false,
    };
    let f = types.intern(function(&[u24, TypeId::BOOL]));
    assert_eq!(types.intern(function(&[u24, TypeId::BOOL])), f);
    assert_ne!(types.intern(function(&[TypeId::BOOL, u24])), f);
}
