//! Interned types: equal types have equal ids, so comparing types is comparing
//! two numbers.

use std::collections::HashMap;
use std::ops::Index;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct TypeId(u32);

/// An integer type: `i5` is signed with 5 bits. Widths run from 1 to 2^23.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Integer {
    pub signed: bool,
    pub bits: u32,
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
