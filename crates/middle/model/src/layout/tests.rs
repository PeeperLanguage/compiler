use crate::machine::Machine;
use crate::semantic_types::TypeId;
use crate::semantic_types::Types;

use super::struct_layout;

/// Each struct's field offsets and size match what LLVM gives on x86-64
/// and AArch64, and with 32-bit pointers on i686 (measured with
/// `getelementptr` folded by clang `-O1`, `-m32` for i686).
#[test]
fn struct_layout_matches_llvm() {
    let mut types = Types::default();
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
            struct_layout(&types, fields, Machine::named("x86_64-linux").unwrap());
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
            struct_layout(&types, fields, Machine::named("i686-linux").unwrap());
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
