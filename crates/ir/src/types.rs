//! Interned types: equal types have equal ids, so comparing types is comparing
//! two numbers.

use std::collections::HashMap;
use std::ops::Index;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct TypeId(u32);

/// An integer type: `i5` is signed with 5 bits.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Integer {
    pub signed: bool,
    pub bits: u32,
}

impl Integer {
    /// The widest integer: LLVM's limit.
    pub const MAX_BITS: u32 = 1 << 23;

    /// Whether the number `magnitude` (64-bit limbs, lowest first), negated
    /// when `negative`, is a value of this type: `-128` fits `i8`, `128` does
    /// not.
    pub fn fits(self, magnitude: &[u64], negative: bool) -> bool {
        let length = magnitude
            .iter()
            .rposition(|&limb| limb != 0)
            .map_or(0, |top| {
                top as u64 * 64 + u64::from(64 - magnitude[top].leading_zeros())
            });
        let bits = u64::from(self.bits);
        match (self.signed, negative) {
            _ if length == 0 => true, // zero, also written `-0`
            (false, false) => length <= bits,
            (false, true) => false,
            (true, false) => length < bits,
            // Down to -2^(bits-1): shorter, or exactly that power of two.
            (true, true) => {
                let power_of_two = magnitude.iter().map(|limb| limb.count_ones()).sum::<u32>() == 1;
                length < bits || length == bits && power_of_two
            }
        }
    }
}

/// A float type: IEEE 754 single or double precision.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Float {
    F32,
    F64,
}

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum Type {
    /// The type of something whose error was already reported; uses of it
    /// report nothing more.
    Invalid,
    /// What a function without a result gives.
    Void,
    Bool,
    Integer(Integer),
    Float(Float),
    Function {
        parameters: Box<[TypeId]>,
        result: TypeId,
    },
}

impl Type {
    /// Whether every value of this type is also a value of `target`, so a value
    /// converts without `as`: to a wider integer of the same signedness, from
    /// unsigned to a wider signed integer, and from `f32` to `f64`. A type does
    /// not widen to itself.
    pub fn widens_to(&self, target: &Type) -> bool {
        match (self, target) {
            (Type::Integer(from), Type::Integer(to)) => {
                to.bits > from.bits && (from.signed == to.signed || to.signed)
            }
            (Type::Float(Float::F32), Type::Float(Float::F64)) => true,
            _ => false,
        }
    }
}

const fn integer(signed: bool, bits: u32) -> Type {
    Type::Integer(Integer { signed, bits })
}

/// The types with fixed ids, in id order; they need no lookup.
const FIXED: &[Type] = &[
    Type::Invalid,
    Type::Void,
    Type::Bool,
    integer(true, 8),
    integer(true, 16),
    integer(true, 32),
    integer(true, 64),
    integer(true, 128),
    integer(false, 8),
    integer(false, 16),
    integer(false, 32),
    integer(false, 64),
    integer(false, 128),
    Type::Float(Float::F32),
    Type::Float(Float::F64),
];

impl TypeId {
    pub const INVALID: TypeId = TypeId(0);
    pub const VOID: TypeId = TypeId(1);
    pub const BOOL: TypeId = TypeId(2);
    pub const I8: TypeId = TypeId(3);
    pub const I16: TypeId = TypeId(4);
    pub const I32: TypeId = TypeId(5);
    pub const I64: TypeId = TypeId(6);
    pub const I128: TypeId = TypeId(7);
    pub const U8: TypeId = TypeId(8);
    pub const U16: TypeId = TypeId(9);
    pub const U32: TypeId = TypeId(10);
    pub const U64: TypeId = TypeId(11);
    pub const U128: TypeId = TypeId(12);
    pub const F32: TypeId = TypeId(13);
    pub const F64: TypeId = TypeId(14);
}

/// The types of a program. The scheduler shares it between threads behind one
/// lock (docs/ir-design.md).
pub struct Types {
    types: Vec<Type>,
    ids: HashMap<Type, TypeId>,
}

impl Types {
    pub fn new() -> Types {
        let mut types = Types {
            types: Vec::new(),
            ids: HashMap::new(),
        };
        for ty in FIXED {
            types.intern(ty.clone());
        }
        types
    }

    /// The id of `ty`, made the first time `ty` is seen.
    pub fn intern(&mut self, ty: Type) -> TypeId {
        if let Some(&id) = self.ids.get(&ty) {
            return id;
        }
        let id = TypeId(u32::try_from(self.types.len()).expect("fewer than 2^32 types"));
        self.types.push(ty.clone());
        self.ids.insert(ty, id);
        id
    }
}

impl Types {
    /// The type as a program writes it: `u8`, `fn(i32) -> bool`.
    pub fn text(&self, id: TypeId) -> String {
        match &self[id] {
            Type::Invalid => "{invalid}".to_string(),
            Type::Void => "void".to_string(),
            Type::Bool => "bool".to_string(),
            Type::Integer(Integer { signed, bits }) => {
                format!("{}{bits}", if *signed { 'i' } else { 'u' })
            }
            Type::Float(Float::F32) => "f32".to_string(),
            Type::Float(Float::F64) => "f64".to_string(),
            Type::Function { parameters, result } => {
                let parameters: Vec<String> = parameters.iter().map(|&p| self.text(p)).collect();
                let mut text = format!("fn({})", parameters.join(", "));
                if *result != TypeId::VOID {
                    text.push_str(" -> ");
                    text.push_str(&self.text(*result));
                }
                text
            }
        }
    }
}

impl Default for Types {
    fn default() -> Types {
        Types::new()
    }
}

impl Index<TypeId> for Types {
    type Output = Type;

    fn index(&self, id: TypeId) -> &Type {
        &self.types[id.0 as usize]
    }
}

#[cfg(test)]
mod tests {
    use super::FIXED;
    use super::Float;
    use super::Integer;
    use super::Type;
    use super::TypeId;
    use super::Types;
    use super::integer;

    #[test]
    fn fixed_ids_are_the_interned_ids() {
        let mut types = Types::new();
        for (index, ty) in FIXED.iter().enumerate() {
            assert_eq!(types.intern(ty.clone()), TypeId(index as u32));
        }
        assert_eq!(types.intern(integer(true, 32)), TypeId::I32);
        assert_eq!(types.intern(integer(false, 128)), TypeId::U128);
        assert_eq!(types[TypeId::U8], integer(false, 8));
        assert_eq!(types[TypeId::BOOL], Type::Bool);
        assert_eq!(types[TypeId::F64], Type::Float(Float::F64));
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
    fn widening_keeps_every_value() {
        let widens = |from: Type, to: Type| from.widens_to(&to);
        assert!(widens(integer(false, 8), integer(true, 16)));
        assert!(widens(integer(true, 8), integer(true, 16)));
        assert!(widens(integer(false, 8), integer(false, 16)));
        assert!(widens(Type::Float(Float::F32), Type::Float(Float::F64)));
        assert!(
            !widens(integer(true, 8), integer(false, 16)),
            "-1 is no u16"
        );
        assert!(!widens(integer(false, 8), integer(true, 8)), "255 is no i8");
        assert!(!widens(integer(true, 16), integer(true, 8)));
        assert!(!widens(integer(true, 32), integer(true, 32)));
        assert!(!widens(integer(true, 32), Type::Float(Float::F64)));
        assert!(!widens(Type::Float(Float::F64), Type::Float(Float::F32)));
    }

    #[test]
    fn type_text() {
        let mut types = Types::new();
        let u24 = types.intern(integer(false, 24));
        let function = types.intern(Type::Function {
            parameters: [TypeId::I32, u24].into(),
            result: TypeId::BOOL,
        });
        let procedure = types.intern(Type::Function {
            parameters: [].into(),
            result: TypeId::VOID,
        });
        assert_eq!(types.text(u24), "u24");
        assert_eq!(types.text(TypeId::F32), "f32");
        assert_eq!(types.text(function), "fn(i32, u24) -> bool");
        assert_eq!(types.text(procedure), "fn()");
    }

    #[test]
    fn equal_types_have_equal_ids() {
        let mut types = Types::new();
        let u24 = types.intern(integer(false, 24));
        assert_eq!(types.intern(integer(false, 24)), u24);
        assert_ne!(types.intern(integer(true, 24)), u24);
        let function = |parameters: &[TypeId]| Type::Function {
            parameters: parameters.into(),
            result: TypeId::VOID,
        };
        let f = types.intern(function(&[u24, TypeId::BOOL]));
        assert_eq!(types.intern(function(&[u24, TypeId::BOOL])), f);
        assert_ne!(types.intern(function(&[TypeId::BOOL, u24])), f);
    }
}
