use super::CheckedConstant;
use super::ConstArgument;
use super::Constant;
use super::Conversion;
use super::Float;
use super::Integer;
use super::Naming;
use super::StructInternals;
use super::Type;
use super::TypeId;
use super::Types;
use super::builtin;
use super::integers;
use crate::ConstArguments;
use crate::DefId;
use crate::ModuleId;
use crate::constant::Value;
use crate::layout::layout;
use crate::machine::Machine;
use crate::mir;

/// Every built-in type name has a doc; an integer's range is right at
/// each edge of how it is worked out, and one as wide as a pointer says
/// so.
#[test]
fn builtin_docs() {
    for name in builtin::builtin_names() {
        assert!(builtin::builtin_doc(name, 64).is_some(), "{name}");
    }
    assert_eq!(builtin::builtin_doc("Meters", 64), None);
    assert!(builtin::builtin_doc(builtin::RANGE, 64).is_some());
    let doc = |name: &str, pointer_bits: u32| builtin::builtin_doc(name, pointer_bits).unwrap();
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
    let mut types = Types::new(64);
    for (index, ty) in builtin::FIXED.iter().enumerate() {
        assert_eq!(types.intern(ty.clone()), TypeId(index as u32));
    }
    assert_eq!(types.intern(integers::integer(true, 32)), TypeId::I32);
    assert_eq!(types.intern(integers::integer(false, 128)), TypeId::U128);
    assert_eq!(types[TypeId::U8], integers::integer(false, 8));
    assert_eq!(types[TypeId::BOOL], Type::Bool);
    assert_eq!(types[TypeId::F64], Type::Float(Float::F64));
    assert_eq!(types[TypeId::CSTR], Type::CString);
    assert_eq!(types[TypeId::RAWPTR], Type::RawPointer { target: None });
    assert_eq!(types.builtin("rawptr"), None, "a keyword, not a type name");
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
    let mut types = Types::new(64);
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
    let array = types.array(TypeId::I32, types.array_length(3));
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
        (
            TypeId::U8,
            TypeId::I16,
            Conversion::Implicit(mir::Cast::ZeroExtend),
        ),
        (
            TypeId::I8,
            TypeId::I16,
            Conversion::Implicit(mir::Cast::SignExtend),
        ),
        (
            TypeId::U8,
            TypeId::U16,
            Conversion::Implicit(mir::Cast::ZeroExtend),
        ),
        (
            TypeId::F32,
            TypeId::F64,
            Conversion::Implicit(mir::Cast::FloatWiden),
        ),
        (TypeId::I32, TypeId::I32, Conversion::Same),
        (
            TypeId::I8,
            TypeId::U16,
            Conversion::NeedsCast(mir::Cast::SignExtend),
        ), // -1 is no u16
        (
            TypeId::U8,
            TypeId::I8,
            Conversion::NeedsCast(mir::Cast::ZeroExtend),
        ), // 255 is no i8
        (
            TypeId::I16,
            TypeId::I8,
            Conversion::NeedsCast(mir::Cast::Truncate),
        ),
        (
            TypeId::I32,
            TypeId::F64,
            Conversion::NeedsCast(mir::Cast::SignedToFloat),
        ),
        (
            TypeId::F64,
            TypeId::I32,
            Conversion::NeedsCast(mir::Cast::FloatToSigned { bits: 32 }),
        ),
        (
            TypeId::F64,
            TypeId::F32,
            Conversion::NeedsCast(mir::Cast::FloatNarrow),
        ),
        (
            TypeId::BOOL,
            TypeId::U8,
            Conversion::NeedsCast(mir::Cast::ZeroExtend),
        ),
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
        (
            untyped,
            to_i32,
            Conversion::NeedsCast(mir::Cast::PointerCast),
        ),
        (
            to_i32,
            to_i64,
            Conversion::NeedsCast(mir::Cast::PointerCast),
        ),
        (untyped, TypeId::U64, Conversion::Never),
    ];
    for (from, to, conversion) in cases {
        assert_eq!(types.conversion(from, to), conversion, "{from:?} to {to:?}");
    }
}

/// A pair holding a type parameter converts as written where it does for
/// every copy, is undecided where some copy converts it, and never converts
/// where no copy does.
#[test]
fn conversions_of_open_types() {
    let mut types = Types::new(64);
    let parameter = types.intern(Type::Parameter {
        definition: DefId {
            module: ModuleId::from_index(0),
            index: 0,
        },
        place: 0,
    });
    let reference =
        |types: &mut Types, mutable, target| types.intern(Type::Reference { mutable, target });
    let pair = types.array(parameter, types.array_length(2));
    let to_pair = reference(&mut types, false, pair);
    let mutable_parameter = reference(&mut types, true, parameter);
    let shared_parameter = reference(&mut types, false, parameter);
    let shared_i32 = reference(&mut types, false, TypeId::I32);
    let view = |types: &mut Types, element| {
        types.intern(Type::Slice {
            mutable: false,
            element,
        })
    };
    let parameters = view(&mut types, parameter);
    let numbers = view(&mut types, TypeId::I32);
    let triple = types.array(TypeId::I32, types.array_length(3));
    let to_triple = reference(&mut types, false, triple);
    // For every copy.
    assert_eq!(
        types.conversion(mutable_parameter, shared_parameter),
        Conversion::Reborrow
    );
    assert_eq!(types.conversion(to_pair, parameters), Conversion::Reborrow);
    // In some copy.
    assert_eq!(
        types.conversion(parameter, TypeId::I64),
        Conversion::Undecided
    );
    assert_eq!(
        types.conversion(mutable_parameter, shared_i32),
        Conversion::Undecided
    );
    assert_eq!(types.conversion(to_pair, numbers), Conversion::Undecided);
    let raw_parameter = types.intern(Type::RawPointer {
        target: Some(parameter),
    });
    let raw_i32 = types.intern(Type::RawPointer {
        target: Some(TypeId::I32),
    });
    assert_eq!(
        types.conversion(raw_parameter, raw_i32),
        Conversion::Undecided
    );
    let bools = types.array(TypeId::BOOL, types.array_length(2));
    assert!(types.could_be_same(pair, bools));
    // In none: an array is never a `bool`, and lengths do not change.
    assert_eq!(types.conversion(pair, TypeId::BOOL), Conversion::Never);
    assert_eq!(types.conversion(to_pair, to_triple), Conversion::Never);
    assert!(!types.could_be_same(pair, triple));
    // Nor is a pointer to a type one to no type known (`*opaque`).
    let opaque = types.intern(Type::RawPointer { target: None });
    assert!(!types.could_be_same(raw_parameter, opaque));
}

/// A `const` argument each copy gives makes the arguments open, so no
/// copy, which would hold a value it does not know, is made from them; a
/// known one does not.
#[test]
fn per_copy_const_arguments_are_open() {
    let mut types = Types::new(64);
    let per_copy = ConstArgument::Value(CheckedConstant::PerCopy(TypeId::I32));
    let per_copy = types.const_arguments(&[per_copy]);
    assert!(types.arguments_open(per_copy));
    let one = Constant {
        ty: TypeId::I32,
        value: Value::Integer(bigint::Limbs::new(32, &[1])),
    };
    let known = types.const_arguments(&[ConstArgument::Value(CheckedConstant::Known(one))]);
    assert!(!types.arguments_open(known));
}

/// An interface keeps the id it was named by, so a `&Shape` written before
/// it was checked is the one of after; a reference to a struct converts to
/// a reference to it as a `&` converts to a `&`, the checker looking at the
/// methods.
#[test]
fn interface_references() {
    let mut types = Types::new(64);
    let declaration = |index| DefId {
        module: ModuleId::from_index(0),
        index,
    };
    let shape = types.intern(Type::Named {
        definition: declaration(0),
        arguments: ConstArguments::NONE,
    });
    let view = types.intern(Type::InterfaceReference {
        mutable: false,
        interface: shape,
    });
    let edit = types.intern(Type::InterfaceReference {
        mutable: true,
        interface: shape,
    });
    let area = types.intern(Type::Function {
        parameters: [view].into(),
        result: TypeId::F64,
        origins: [].into(),
        variadic: false,
    });
    types.define_interface(shape, [area].into());
    assert!(matches!(types[shape], Type::Interface { .. }));
    assert_eq!(
        types.intern(Type::Named {
            definition: declaration(0),
            arguments: ConstArguments::NONE,
        }),
        shape
    );
    let square = types.intern(Type::Named {
        definition: declaration(1),
        arguments: ConstArguments::NONE,
    });
    types.define_struct(square, [TypeId::F64].into(), (vec![0], 8, 8));
    let reference =
        |types: &mut Types, mutable, target| types.intern(Type::Reference { mutable, target });
    let to_square = reference(&mut types, false, square);
    let to_mutable_square = reference(&mut types, true, square);
    let to_number = reference(&mut types, false, TypeId::F64);
    let cases = [
        (to_square, view, Conversion::ToInterface),
        (to_mutable_square, view, Conversion::ToInterface),
        (to_mutable_square, edit, Conversion::ToInterface),
        (to_square, edit, Conversion::Never),
        (to_number, view, Conversion::Never), // only a declared type has methods
        (edit, view, Conversion::Reborrow),
        (view, edit, Conversion::Never),
        (view, to_square, Conversion::Never),
        (square, shape, Conversion::ToInterface), // moved into an owned one
        (TypeId::F64, shape, Conversion::Never),
        (to_square, shape, Conversion::Never),
        (shape, view, Conversion::Never),
    ];
    for (from, to, conversion) in cases {
        assert_eq!(types.conversion(from, to), conversion, "{from:?} to {to:?}");
    }
    // An owned interface holds one `&mut` loan, on the struct it took, and
    // is laid out as an interface reference.
    assert_eq!(
        (types.holders_in(shape), types.holds_loans(shape)),
        (1, Some(true))
    );
    for machine in ["x86_64-linux", "i686-linux"] {
        let machine = Machine::named(machine).unwrap();
        assert_eq!(
            layout(&types, shape, machine),
            layout(&types, view, machine)
        );
    }
    let text = |ty| {
        types.text(ty, |naming| match naming {
            Naming::Declaration(definition) => ["Shape", "Square"][definition.index as usize],
            Naming::Parameter { .. } => unreachable!("no type parameter here"),
        })
    };
    assert_eq!(text(view), "&Shape");
    assert_eq!(text(edit), "&mut Shape");
    assert_eq!(text(area), "fn(&Shape) -> f64");
    assert_eq!(types[view].borrows(), Some(false));
    assert_eq!(types[edit].borrows(), Some(true));
    assert!(types.is_copy(view) && !types.is_copy(edit) && !types.is_copy(shape));
    assert_eq!(types[edit].pointee(), Some(shape));
}

#[test]
fn builtin_names() {
    let mut types = Types::new(64);
    let mut lookup = |name| types.builtin(name);
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
    assert_eq!(types[u24], integers::integer(false, 24));
    assert_eq!(
        Types::new(32).builtin("usize"),
        Some(TypeId::U32),
        "a pointer is 32 bits there"
    );
    assert_eq!(Types::new(32).usize, TypeId::U32);
    assert_eq!(Types::new(64).usize, TypeId::U64);
}

#[test]
fn type_text() {
    let mut types = Types::new(64);
    let u24 = types.intern(integers::integer(false, 24));
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
    let point = types.intern(Type::Named {
        definition: declaration(0),
        arguments: ConstArguments::NONE,
    });
    let place = types.intern(Type::Alias {
        definition: declaration(1),
        real: point,
    });
    let complete = types.intern(Type::Struct {
        definition: declaration(0),
        arguments: ConstArguments::NONE,
        fields: [TypeId::I32, TypeId::I32].into(),
        internals: StructInternals {
            offsets: [0, 4].into(),
            size: 8,
            align: 4,
            holders: 0,
            mutable_holder: false,
        },
    });
    let raw = TypeId::RAWPTR;
    let typed = types.intern(Type::RawPointer { target: Some(raw) });
    // A generic struct's copy names what it is given.
    let given = [ConstArgument::Type(TypeId::I32), ConstArgument::Type(point)];
    let arguments = types.const_arguments(&given);
    let copy = types.intern(Type::Named {
        definition: declaration(0),
        arguments,
    });
    let text = |ty| {
        types.text(ty, |naming| match naming {
            Naming::Declaration(definition) => ["Point", "Place"][definition.index as usize],
            Naming::Parameter { .. } => unreachable!("no type parameter here"),
        })
    };
    assert_eq!(text(u24), "u24");
    assert_eq!(text(TypeId::F32), "f32");
    assert_eq!(text(TypeId::CSTR), "&cstr");
    assert_eq!(text(function), "fn(i32, u24) -> bool");
    assert_eq!(text(procedure), "fn()");
    assert_eq!(text(point), "Point");
    assert_eq!(text(place), "Place (aka Point)");
    assert_eq!(text(complete), "Point");
    assert_eq!(text(copy), "Point<i32, Point>");
    assert_eq!(text(raw), "rawptr");
    assert_eq!(text(typed), "*rawptr");
}

#[test]
fn equal_types_have_equal_ids() {
    let mut types = Types::new(64);
    let u24 = types.intern(integers::integer(false, 24));
    assert_eq!(types.intern(integers::integer(false, 24)), u24);
    assert_ne!(types.intern(integers::integer(true, 24)), u24);
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

/// An array in an array keeps what they end in and how many of it there are
/// in all: any zero length empties it, and a count past `u64` stays the largest.
#[test]
fn arrays_in_arrays_keep_their_base_and_count() {
    let mut types = Types::new(64);
    let row = types.array(TypeId::I32, types.array_length(3));
    let grid = types.array(row, types.array_length(2));
    let Type::Array { internals, .. } = types[grid] else {
        panic!("an array");
    };
    assert_eq!((internals.base, internals.count), (TypeId::I32, Some(6)));
    assert_eq!(
        (types.array_lengths(grid))
            .map(CheckedConstant::known_length)
            .collect::<Vec<_>>(),
        [Some(2), Some(3)]
    );
    assert_eq!(types.array_lengths(TypeId::I32).count(), 0);
    let row_alias = types.intern(Type::Alias {
        definition: DefId {
            module: ModuleId::from_index(0),
            index: 0,
        },
        real: row,
    });
    let through_alias = types.array(row_alias, types.array_length(2));
    let Type::Array { internals, .. } = types[through_alias] else {
        panic!("an array");
    };
    assert_eq!((internals.base, internals.count), (TypeId::I32, Some(6)));
    let count_of = |types: &Types, ty| match types[ty] {
        Type::Array { internals, .. } => internals.count,
        _ => panic!("an array"),
    };
    let huge = types.array(TypeId::I32, types.array_length(u64::MAX));
    let past = types.array(huge, types.array_length(2));
    assert_eq!(count_of(&types, past), Some(u64::MAX));
    let empty = types.array(past, types.array_length(0));
    assert_eq!(count_of(&types, empty), Some(0));
    let open = types.array(TypeId::I32, CheckedConstant::PerCopy(types.usize));
    let rows = types.array(open, types.array_length(2));
    assert_eq!(count_of(&types, rows), None);
    assert!(types.is_open(rows));
    // A length each copy gives is the same hole as a `const` argument's:
    // some copy gives any length, never another element.
    let three = types.array(TypeId::I32, types.array_length(3));
    let bools = types.array(TypeId::BOOL, types.array_length(3));
    assert!(types.could_be_same(open, three));
    assert!(!types.could_be_same(open, bools));
    assert_eq!(types.text(open, |_| ""), "[_]i32");
}

/// An array holds one set of loan holders per element, however deep, and a
/// count past `usize` stays the largest.
#[test]
fn arrays_hold_each_elements_holders() {
    let mut types = Types::new(64);
    let shared = types.intern(Type::Reference {
        mutable: false,
        target: TypeId::I32,
    });
    let mutable = types.intern(Type::Reference {
        mutable: true,
        target: TypeId::I32,
    });
    let pair = types.intern(Type::Struct {
        definition: DefId {
            module: ModuleId::from_index(0),
            index: 0,
        },
        arguments: ConstArguments::NONE,
        fields: [shared, shared].into(),
        internals: StructInternals {
            offsets: [0, 8].into(),
            size: 16,
            align: 8,
            holders: 2,
            mutable_holder: false,
        },
    });
    let refs = types.array(shared, types.array_length(3));
    assert_eq!(
        (types.holders_in(refs), types.holds_loans(refs)),
        (3, Some(false))
    );
    let row = types.array(mutable, types.array_length(3));
    let grid = types.array(row, types.array_length(2));
    assert_eq!(
        (types.holders_in(grid), types.holds_loans(grid)),
        (6, Some(true))
    );
    let pairs = types.array(pair, types.array_length(2));
    assert_eq!(
        (types.holders_in(pairs), types.holds_loans(pairs)),
        (4, Some(false))
    );
    let numbers = types.array(TypeId::I32, types.array_length(3));
    assert_eq!(
        (types.holders_in(numbers), types.holds_loans(numbers)),
        (0, None)
    );
    let huge = types.array(refs, types.array_length(u64::MAX));
    assert_eq!(types.holders_in(huge), usize::MAX);
}
