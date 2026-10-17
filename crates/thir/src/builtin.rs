//! The built-in types, by the names a program writes for them. A number
//! literal's suffix (`5u24`) is one of these names too.

use ir::types::Integer;
use ir::types::Type;
use ir::types::TypeId;
use ir::types::Types;

/// The built-in type `name` stands for: `bool`, `f32`, `f64`, `isize` and
/// `usize` (as wide as a pointer, `pointer_bits`), or `iN` and `uN` for N from 1
/// to `Integer::MAX_BITS`, written without leading zeros.
pub fn builtin_type(name: &str, pointer_bits: u32, types: &mut Types) -> Option<TypeId> {
    let integer = |signed, bits| Some(Type::Integer(Integer { signed, bits }));
    let ty = match name {
        "bool" => return Some(TypeId::BOOL),
        "f32" => return Some(TypeId::F32),
        "f64" => return Some(TypeId::F64),
        "isize" => integer(true, pointer_bits),
        "usize" => integer(false, pointer_bits),
        _ => {
            let (signed, width) = match name.split_at_checked(1)? {
                ("i", width) => (true, width),
                ("u", width) => (false, width),
                _ => return None,
            };
            let digits = !width.is_empty() && width.bytes().all(|b| b.is_ascii_digit());
            if !digits || width.starts_with('0') {
                return None; // `u`, `i8x`, `u08`
            }
            let bits = width
                .parse()
                .ok()
                .filter(|&bits| bits <= Integer::MAX_BITS)?;
            integer(signed, bits)
        }
    };
    ty.map(|ty| types.intern(ty))
}

#[cfg(test)]
mod tests {
    use ir::types::Float;
    use ir::types::Integer;
    use ir::types::Type;
    use ir::types::TypeId;
    use ir::types::Types;

    use super::builtin_type;

    #[test]
    fn names() {
        let mut types = Types::new();
        let mut lookup = |name| builtin_type(name, 64, &mut types);
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
        let u24 = lookup("u24").unwrap();
        assert_eq!(lookup("u24"), Some(u24));
        assert!(lookup("i8388608").is_some(), "2^23 bits is the widest");
        for name in [
            "i0", "u08", "i8388609", "u", "i8x", "x8", "byte", "", "i-1", "u+8",
        ] {
            assert_eq!(lookup(name), None, "{name}");
        }
        assert_eq!(
            types[u24],
            Type::Integer(Integer {
                signed: false,
                bits: 24
            })
        );
        let mut types = Types::new();
        let usize32 = builtin_type("usize", 32, &mut types).unwrap();
        assert_eq!(usize32, TypeId::U32, "a pointer is 32 bits there");
        assert_eq!(types[TypeId::F64], Type::Float(Float::F64));
    }
}
