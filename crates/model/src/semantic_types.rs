//! Interned types: equal types have equal ids, so comparing types is comparing
//! two numbers.

use std::collections::HashMap;
use std::fmt;
use std::ops::Index;

use crate::DefId;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct TypeId(u32);

/// An integer type: `i5` is signed with 5 bits.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Integer {
    pub signed: bool,
    pub bits: u32,
}

impl fmt::Display for Integer {
    fn fmt(&self, out: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(out, "{}{}", if self.signed { 'i' } else { 'u' }, self.bits)
    }
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
    /// `cstr`: a pointer to a C string, as C functions take it.
    CString,
    Function {
        parameters: Box<[TypeId]>,
        result: TypeId,
    },
    /// A type alias: the declaration that names it, and the type it names,
    /// never another alias (an alias of an alias names that alias's type).
    /// Indexing `Types` looks through it to the type itself (`Types::real`);
    /// only a message names the alias.
    Alias {
        definition: DefId,
        real: TypeId,
    },
    /// The name of a type declaration, such as a struct, before what it
    /// declares is checked: naming a type checks nothing. No value has it: a
    /// value's type is the declaration's own (`Type::Struct`).
    Named(DefId),
    /// The type a struct declaration makes, with its fields' types in order.
    /// The declaration is its identity, so two structs with the same fields
    /// differ; the field names stay in the declaration. A field held by value
    /// has its own struct's type, so the type is complete.
    Struct {
        definition: DefId,
        fields: Box<[TypeId]>,
    },
    /// `&T`, or `&mut T` when `mutable`: a view of a `target` value held
    /// elsewhere, never itself a reference.
    Reference {
        mutable: bool,
        target: TypeId,
    },
}

impl Type {
    /// Whether a value of this type copies when used by value: numbers,
    /// `bool`, `cstr` and shared references. Any other value moves, and the
    /// variable it moved from is unusable until set again.
    pub fn is_copy(&self) -> bool {
        match self {
            Type::Bool | Type::Integer(_) | Type::Float(_) | Type::CString => true,
            Type::Reference { mutable, .. } => !mutable,
            Type::Struct { .. } => false,
            // No value of these is moved: an error, nothing, or not yet a value.
            Type::Invalid | Type::Void | Type::Function { .. } => true,
            Type::Alias { .. } | Type::Named(_) => {
                unreachable!("indexing `Types` looks through aliases; no value is named")
            }
        }
    }

    /// Whether the type is a number: an integer or a float.
    pub fn is_number(&self) -> bool {
        matches!(self, Type::Integer(_) | Type::Float(_))
    }

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

/// Declares the types with fixed ids: `FIXED`, the types in id order (they
/// need no lookup); a `TypeId` constant for each, numbered in the order
/// written; and `NAMED`, the name a program writes for each type given one.
/// Other integers are named by rule (`Types::builtin`).
macro_rules! fixed_types {
    ($($name:ident $($text:literal)? => $ty:expr,)*) => {
        const FIXED: &[Type] = &[$($ty,)*];

        /// The position of each fixed type in `FIXED`, counted by the compiler.
        #[allow(non_camel_case_types, clippy::upper_case_acronyms)]
        enum Position {
            $($name,)*
        }

        impl TypeId {
            $(pub const $name: TypeId = TypeId(Position::$name as u32);)*
        }

        const NAMED: &[(&str, TypeId)] = &[$($(($text, TypeId::$name),)?)*];
    };
}

fixed_types! {
    INVALID => Type::Invalid,
    VOID => Type::Void,
    BOOL "bool" => Type::Bool,
    I8 "i8" => integer(true, 8),
    I16 "i16" => integer(true, 16),
    I32 "i32" => integer(true, 32),
    I64 "i64" => integer(true, 64),
    I128 "i128" => integer(true, 128),
    U8 "u8" => integer(false, 8),
    U16 "u16" => integer(false, 16),
    U32 "u32" => integer(false, 32),
    U64 "u64" => integer(false, 64),
    U128 "u128" => integer(false, 128),
    F32 "f32" => Type::Float(Float::F32),
    F64 "f64" => Type::Float(Float::F64),
    CSTR "cstr" => Type::CString,
}

/// The integer types as wide as a pointer, and whether each is signed.
const POINTER_SIZED: [(&str, bool); 2] = [("isize", true), ("usize", false)];

/// The built-in type names a program writes most, for "did you mean"
/// guesses: each in `NAMED`, `isize` and `usize`.
pub fn builtin_names() -> impl Iterator<Item = &'static str> {
    let named = NAMED.iter().map(|&(text, _)| text);
    named.chain(POINTER_SIZED.iter().map(|&(text, _)| text))
}

/// The types of a program, for one compile (`check::Checker` holds them).
// TEMPORARY, to be deleted: `Clone` is only for `llvm::write`, whose workers
// each get a copy of the types; delete it with `extend_from`.
#[derive(Clone)]
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

    /// Adds the types `other` has after these: keeps a copy of a table that
    /// only grows, as `other` is when this was made from it, up to date.
    ///
    /// TEMPORARY, to be deleted: only `llvm::write` uses it, to give its
    /// workers a copy of the types. Delete it, and the copy, when each
    /// module's MIR lists the types it uses (self-contained MIR).
    pub fn extend_from(&mut self, other: &Types) {
        for ty in &other.types[self.types.len()..] {
            self.intern(ty.clone());
        }
    }

    /// The id of `ty`, made the first time `ty` is seen. An alias of an alias
    /// is kept as an alias of that alias's type, so `real` takes one step.
    pub fn intern(&mut self, mut ty: Type) -> TypeId {
        if let Type::Alias { real, .. } = &mut ty {
            *real = self.real(*real);
        }
        if let Some(&id) = self.ids.get(&ty) {
            return id;
        }
        let id = TypeId(u32::try_from(self.types.len()).expect("fewer than 2^32 types"));
        self.types.push(ty.clone());
        self.ids.insert(ty, id);
        id
    }

    /// The built-in type `name` stands for: one in `NAMED`, `isize` and `usize`
    /// (as wide as a pointer, `pointer_bits`), or `iN` and `uN` for N from 1 to
    /// `Integer::MAX_BITS`, written without leading zeros. A number literal's
    /// suffix (`5u24`) is one of these names too.
    pub fn builtin(&mut self, name: &str, pointer_bits: u32) -> Option<TypeId> {
        if let Some(&(_, id)) = NAMED.iter().find(|(text, _)| *text == name) {
            return Some(id);
        }
        let pointer_sized = POINTER_SIZED.iter().find(|(text, _)| *text == name);
        let (signed, bits) = match pointer_sized {
            Some(&(_, signed)) => (signed, pointer_bits),
            None => {
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
                (signed, bits)
            }
        };
        Some(self.intern(integer(signed, bits)))
    }

    /// Whether `a` and `b` are one type, aliases looked through.
    pub fn same(&self, a: TypeId, b: TypeId) -> bool {
        self.real(a) == self.real(b)
    }

    /// The type `id` stands for: itself, or the type an alias names.
    pub fn real(&self, id: TypeId) -> TypeId {
        match self.types[id.0 as usize] {
            Type::Alias { real, .. } => real,
            _ => id,
        }
    }

    /// The type `id` as a program writes it, a declared type or an alias by its name,
    /// which `name_of` gives for its declaration: an alias is followed by the
    /// type itself (`Meters (aka i64)`).
    pub fn text<'n>(&self, id: TypeId, name_of: impl Fn(DefId) -> &'n str + Copy) -> String {
        match self.types[id.0 as usize] {
            Type::Alias { definition, real } => {
                format!(
                    "{} (aka {})",
                    name_of(definition),
                    self.real_text(real, name_of)
                )
            }
            _ => self.real_text(id, name_of),
        }
    }

    /// The type `id` itself, aliases looked through: `u8`, `fn(i32) -> bool`,
    /// a named type by its name.
    fn real_text<'n>(&self, id: TypeId, name_of: impl Fn(DefId) -> &'n str + Copy) -> String {
        let id = self.real(id);
        match &self[id] {
            Type::Invalid => "{invalid}".to_string(),
            Type::Void => "void".to_string(),
            Type::Bool | Type::Float(_) | Type::CString => {
                let (text, _) = NAMED
                    .iter()
                    .find(|&&(_, named)| named == id)
                    .expect("in `NAMED`");
                text.to_string()
            }
            Type::Integer(integer) => integer.to_string(),
            Type::Function { parameters, result } => {
                let parameters: Vec<String> = parameters
                    .iter()
                    .map(|&parameter| self.real_text(parameter, name_of))
                    .collect();
                let mut text = format!("fn({})", parameters.join(", "));
                if *result != TypeId::VOID {
                    text.push_str(" -> ");
                    text.push_str(&self.real_text(*result, name_of));
                }
                text
            }
            Type::Named(definition) | Type::Struct { definition, .. } => {
                name_of(*definition).to_string()
            }
            Type::Reference { mutable, target } => {
                let target = self.real_text(*target, name_of);
                if *mutable {
                    format!("&mut {target}")
                } else {
                    format!("&{target}")
                }
            }
            Type::Alias { .. } => unreachable!("`real` looks through aliases"),
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

    /// The type itself, aliases looked through (`Types::real`).
    fn index(&self, id: TypeId) -> &Type {
        &self.types[self.real(id).0 as usize]
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
    use crate::DefId;
    use crate::ModuleId;

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
        assert_eq!(types[TypeId::CSTR], Type::CString);
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
    fn builtin_names() {
        let mut types = Types::new();
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
        let mut types = Types::new();
        assert_eq!(
            types.builtin("usize", 32),
            Some(TypeId::U32),
            "a pointer is 32 bits there"
        );
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
