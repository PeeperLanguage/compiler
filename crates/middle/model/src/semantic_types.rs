//! Interned types: equal types have equal ids, so comparing types is comparing
//! two numbers.

use std::collections::HashMap;
use std::fmt;
use std::ops::Index;

use bigint::Limbs;

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

    /// Whether a range of this type from the constant `start` to `end`
    /// counts up (`start <= end`), and how far apart the two are, read as
    /// unsigned of the same width, so it never overflows. `range_count`
    /// and lowering's walk of a constant range both start here, so they
    /// agree on how many values it has.
    pub fn range_distance(self, start: &Limbs, end: &Limbs) -> (bool, Limbs) {
        let up = bigint::compare(self.bits, self.signed, start, end).is_le();
        let (near, far) = if up { (start, end) } else { (end, start) };
        (up, bigint::subtract(self.bits, far, near))
    }

    /// How many values a range of this type from the constant `start` to
    /// `end` by `step` has, `end` counted when `inclusive`, as `for` counts
    /// them: one bit wider than the type, so `0..=u64::MAX`, 2^64 values,
    /// is exact. `None` for a zero step.
    pub fn range_count(
        self,
        start: &Limbs,
        end: &Limbs,
        step: &Limbs,
        inclusive: bool,
    ) -> Option<Limbs> {
        let (_, distance) = self.range_distance(start, end);
        let whole = bigint::divide(self.bits, false, &distance, step)?;
        let rest = bigint::remainder(self.bits, false, &distance, step)?;
        let extra = inclusive || rest.as_slice().iter().any(|&limb| limb != 0);
        let wide = self.bits + 1;
        let whole = bigint::extend(self.bits, false, &whole, wide);
        Some(bigint::add(
            wide,
            &whole,
            &Limbs::new(wide, &[u64::from(extra)]),
        ))
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
    /// `origins`: the positions of the parameters a returned reference may
    /// point into, in order; empty when the result is not a reference.
    Function {
        parameters: Box<[TypeId]>,
        result: TypeId,
        origins: Box<[u32]>,
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
    /// `[length]element`: `length` values of the `element` type, one after
    /// another.
    Array {
        element: TypeId,
        length: u64,
    },
    /// `range[element]`: the integers from a start toward an end, a step
    /// apart, up or down; its fields are `range_fields`. `step` is the
    /// unsigned integer type as wide as `element`, the step's type, kept
    /// here so a reader of the types finds it without adding it
    /// (`Types::range` makes both).
    Range {
        element: TypeId,
        step: TypeId,
    },
    /// `&T`, or `&mut T` when `mutable`: a view of a `target` value held
    /// elsewhere, never itself a reference.
    Reference {
        mutable: bool,
        target: TypeId,
    },
    /// `[&]T`, or `[&mut]T` when `mutable`: a view of some `element`
    /// values held elsewhere, one after another, with how many there are
    /// known only when the program runs. The loan rules hold for it as for
    /// a reference (`Type::borrows`).
    Slice {
        mutable: bool,
        element: TypeId,
    },
    /// `*T`, or `rawptr` without a `target`: an address that nothing
    /// checks, `none` when null; what it points to can always be written.
    RawPointer {
        target: Option<TypeId>,
    },
}

impl Type {
    /// The type a `.*` of this type reads: a reference's target, or a typed
    /// raw pointer's.
    pub fn pointee(&self) -> Option<TypeId> {
        match *self {
            Type::Reference { target, .. } => Some(target),
            Type::RawPointer { target } => target,
            _ => None,
        }
    }

    /// `Some(mutable)` for a type whose values borrow what they view, a
    /// reference or a slice: the loan rules hold for both.
    pub fn borrows(&self) -> Option<bool> {
        match *self {
            Type::Reference { mutable, .. } | Type::Slice { mutable, .. } => Some(mutable),
            _ => None,
        }
    }

    /// Whether the type is a number: an integer or a float.
    pub fn is_number(&self) -> bool {
        matches!(self, Type::Integer(_) | Type::Float(_))
    }
}

/// How a value of one type becomes a value of another (`Types::conversion`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Conversion {
    /// Nothing to do: one type, perhaps under an alias, or a `*T` used
    /// as a `rawptr`, the same address.
    Same,
    /// Every value of the one type is a value of the other, so it converts
    /// without `as`: to a wider integer of the same signedness, from unsigned
    /// to a wider signed integer, and from `f32` to `f64`.
    Implicit,
    /// A `&mut T` used as a `&T`, a reference to an array used as a slice
    /// of it, or a `[&mut]T` used as a `[&]T`: lent again, so the reference
    /// or slice itself is not moved.
    Reborrow,
    /// Converts only when written with `as`: between integers that lose
    /// values, between floats and integers, from `f64` to `f32`, from
    /// `bool` to an integer, and between raw pointers of other types (the
    /// same address).
    NeedsCast,
    /// No conversion.
    Never,
}

const fn integer(signed: bool, bits: u32) -> Type {
    Type::Integer(Integer { signed, bits })
}

/// Declares the types with fixed ids: `FIXED`, the types in id order (they
/// need no lookup); a `TypeId` constant for each, numbered in the order
/// written; `NAMED`, the name a program writes for each type given one; and
/// `fixed_doc`, the doc comment written on a type's row. Other integers are
/// named by rule (`Types::builtin`).
macro_rules! fixed_types {
    ($(
        $(#[doc = $doc:literal] $(#[doc = $doc_more:literal])*)?
        $name:ident $($text:literal)? => $ty:expr,
    )*) => {
        const FIXED: &[Type] = &[$($ty,)*];

        /// The position of each fixed type in `FIXED`, counted by the compiler.
        #[allow(non_camel_case_types, clippy::upper_case_acronyms)]
        enum Position {
            $($name,)*
        }

        impl TypeId {
            $(
                $(#[doc = $doc] $(#[doc = $doc_more])*)?
                pub const $name: TypeId = TypeId(Position::$name as u32);
            )*
        }

        const NAMED: &[(&str, TypeId)] = &[$($(($text, TypeId::$name),)?)*];

        /// The doc comment on the row of the fixed type `id`, as Markdown,
        /// each line with the space after its `///`, which Markdown drops;
        /// `None` when its row has none.
        fn fixed_doc(id: TypeId) -> Option<&'static str> {
            match id {
                $($(TypeId::$name => Some(concat!($doc $(, "\n", $doc_more)*)),)?)*
                _ => None,
            }
        }
    };
}

// A named type that is not an integer has a doc; an integer's is worked out
// (`builtin_doc`).
fixed_types! {
    INVALID => Type::Invalid,
    VOID => Type::Void,
    /// A truth value: `true` or `false`.
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
    /// A 32-bit floating-point number (IEEE 754 single precision).
    F32 "f32" => Type::Float(Float::F32),
    /// A 64-bit floating-point number (IEEE 754 double precision).
    F64 "f64" => Type::Float(Float::F64),
    /// A pointer to a C string, bytes ending in a zero byte, as C functions
    /// take it.
    CSTR "cstr" => Type::CString,
    // No name: `rawptr` is a keyword, not a type name.
    RAWPTR => Type::RawPointer { target: None },
}

/// The name of the built-in range type, written with its values' type
/// after it in brackets: `range[i32]`.
pub const RANGE: &str = "range";

/// What `range[T]` is, as `builtin_doc` gives it: in the shape of a row's
/// doc, each line keeping the space after its `///`.
const RANGE_DOC: &str = " The integers from a start toward an end, a step apart: the type of `a..b`, `a..=b` and `a..b:step`.

 `range[T]` holds integers of the type `T`. It counts up or down, as its ends say, includes its end when written with `..=`, and copies. `for v in r` counts each of its values, and `[N]T{r...}` spreads them into an array.

 ```peeper
 fn evens(n: i32) -> range[i32] {
     return 0..n:2;
 }
 ```";

/// The fields of a value of `Type::Range { element, step }`, in order, each
/// with its name, as debug info shows it, and type: its walk, found once
/// when the range is made (`lower::Lowering::walk_range`). Its values are
/// `start`, then each `delta` more than the one before, wrapping, while
/// `left`, how far its end is, lasts by `step`, the end counted when
/// `inclusive`. `range_field` gives where each one is.
pub fn range_fields(element: TypeId, step: TypeId) -> [(&'static str, TypeId); 5] {
    [
        ("start", element),
        ("left", step),
        ("step", step),
        ("delta", element),
        ("inclusive", TypeId::BOOL),
    ]
}

/// Where each field of a range value is in `range_fields`.
pub mod range_field {
    pub const START: usize = 0;
    pub const LEFT: usize = 1;
    pub const STEP: usize = 2;
    pub const DELTA: usize = 3;
    pub const INCLUSIVE: usize = 4;
}

/// The integer types as wide as a pointer, and whether each is signed.
const POINTER_SIZED: [(&str, bool); 2] = [("isize", true), ("usize", false)];

/// The built-in type names a program writes most, for "did you mean"
/// guesses: each in `NAMED`, `isize` and `usize`.
pub fn builtin_names() -> impl Iterator<Item = &'static str> {
    let named = NAMED.iter().map(|&(text, _)| text);
    named.chain(POINTER_SIZED.iter().map(|&(text, _)| text))
}

/// The integer type `name` stands for when it is not in `NAMED`: `isize` and
/// `usize` (as wide as a pointer, `pointer_bits`), or `iN` and `uN` for N
/// from 1 to `Integer::MAX_BITS`, written without leading zeros.
fn integer_named(name: &str, pointer_bits: u32) -> Option<Integer> {
    if let Some(&(_, signed)) = POINTER_SIZED.iter().find(|(text, _)| *text == name) {
        return Some(Integer {
            signed,
            bits: pointer_bits,
        });
    }
    let (signed, width) = match name.split_at_checked(1)? {
        ("i", width) => (true, width),
        ("u", width) => (false, width),
        _ => return None,
    };
    let digits = !width.is_empty() && width.bytes().all(|b| b.is_ascii_digit());
    if !digits || width.starts_with('0') {
        return None; // `u`, `i8x`, `u08`
    }
    let bits = (width.parse().ok()).filter(|&bits| bits <= Integer::MAX_BITS)?;
    Some(Integer { signed, bits })
}

/// What the built-in type `name` is (`Types::builtin`), or `range`, as
/// Markdown, as an editor shows it: an integer's range, worked out, and
/// whether it is as wide as a pointer, `pointer_bits`; another type's doc,
/// from its row (`fixed_doc`). `None` when `name` is not a built-in type.
pub fn builtin_doc(name: &str, pointer_bits: u32) -> Option<String> {
    if name == RANGE {
        return Some(RANGE_DOC.to_string());
    }
    let Integer { signed, bits } = match NAMED.iter().find(|&&(text, _)| text == name) {
        Some(&(_, id)) => match FIXED[id.0 as usize] {
            Type::Integer(integer) => integer,
            _ => return fixed_doc(id).map(str::to_string),
        },
        None => integer_named(name, pointer_bits)?,
    };
    let (min, max) = match (signed, bits) {
        (true, ..=128) => (
            (i128::MIN >> (128 - bits)).to_string(),
            (i128::MAX >> (128 - bits)).to_string(),
        ),
        (false, ..=128) => ("0".to_string(), (u128::MAX >> (128 - bits)).to_string()),
        // Too many digits to read.
        (true, _) => (format!("-2^{}", bits - 1), format!("2^{} - 1", bits - 1)),
        (false, _) => ("0".to_string(), format!("2^{bits} - 1")),
    };
    let kind = if signed { "A signed" } else { "An unsigned" };
    let size = if POINTER_SIZED.iter().any(|&(text, _)| text == name) {
        format!("integer as wide as a pointer, {bits} bits on the target")
    } else {
        format!("{bits}-bit integer")
    };
    Some(format!("{kind} {size}: from `{min}` to `{max}`."))
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
        Some(self.intern(Type::Integer(integer_named(name, pointer_bits)?)))
    }

    /// `range[element]`, of the integer type `element` (an alias looked
    /// through), with its step type; `None` when `element` is not an
    /// integer.
    pub fn range(&mut self, element: TypeId) -> Option<TypeId> {
        let Type::Integer(integer) = self[element] else {
            return None;
        };
        let element = self.real(element);
        let step = self.intern(Type::Integer(Integer {
            signed: false,
            ..integer
        }));
        Some(self.intern(Type::Range { element, step }))
    }

    /// Whether `a` and `b` are one type, aliases looked through.
    pub fn same(&self, a: TypeId, b: TypeId) -> bool {
        self.real(a) == self.real(b)
    }

    /// How a value of the type `from` becomes a value of the type `to`.
    pub fn conversion(&self, from: TypeId, to: TypeId) -> Conversion {
        if self.same(from, to) {
            return Conversion::Same;
        }
        match (&self[from], &self[to]) {
            (Type::Integer(from), Type::Integer(to)) => {
                if to.bits > from.bits && (from.signed == to.signed || to.signed) {
                    Conversion::Implicit
                } else {
                    Conversion::NeedsCast
                }
            }
            (Type::Float(Float::F32), Type::Float(Float::F64)) => Conversion::Implicit,
            (Type::Float(_) | Type::Integer(_), Type::Float(_) | Type::Integer(_))
            | (Type::Bool, Type::Integer(_)) => Conversion::NeedsCast,
            (
                &Type::Reference {
                    mutable: true,
                    target: from,
                },
                &Type::Reference {
                    mutable: false,
                    target: to,
                },
            ) if self.same(from, to) => Conversion::Reborrow,
            (
                &Type::Reference {
                    mutable: from_mutable,
                    target,
                },
                &Type::Slice { mutable, element },
            ) if from_mutable || !mutable => match self[target] {
                Type::Array { element: from, .. } if self.same(from, element) => {
                    Conversion::Reborrow
                }
                _ => Conversion::Never,
            },
            (
                &Type::Slice {
                    mutable: true,
                    element: from,
                },
                &Type::Slice {
                    mutable: false,
                    element: to,
                },
            ) if self.same(from, to) => Conversion::Reborrow,
            (Type::RawPointer { target: Some(_) }, Type::RawPointer { target: None }) => {
                Conversion::Same
            }
            (Type::RawPointer { .. }, Type::RawPointer { .. }) => Conversion::NeedsCast,
            _ => Conversion::Never,
        }
    }

    /// The type `id` stands for: itself, or the type an alias names.
    /// Whether a value of the type `id` copies when used by value: numbers,
    /// `bool`, `cstr`, shared references and slices, raw pointers, ranges, and arrays of values
    /// that copy. Any other value moves, and the variable it moved from is
    /// unusable until set again.
    pub fn is_copy(&self, id: TypeId) -> bool {
        match self[id] {
            Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::RawPointer { .. }
            | Type::Range { .. } => true,
            Type::Reference { mutable, .. } | Type::Slice { mutable, .. } => !mutable,
            Type::Array { element, .. } => self.is_copy(element),
            Type::Struct { .. } => false,
            // No value of these is moved: an error, nothing, or not yet a value.
            Type::Invalid | Type::Void | Type::Function { .. } => true,
            Type::Alias { .. } | Type::Named(_) => {
                unreachable!("indexing `Types` looks through aliases; no value is named")
            }
        }
    }

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
            Type::Function {
                parameters, result, ..
            } => {
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
            Type::Array { element, length } => {
                format!("[{length}]{}", self.real_text(*element, name_of))
            }
            Type::Range { element, .. } => {
                format!("{RANGE}[{}]", self.real_text(*element, name_of))
            }
            Type::Reference { mutable, target } => {
                let target = self.real_text(*target, name_of);
                if *mutable {
                    format!("&mut {target}")
                } else {
                    format!("&{target}")
                }
            }
            Type::Slice { mutable, element } => {
                let element = self.real_text(*element, name_of);
                if *mutable {
                    format!("[&mut]{element}")
                } else {
                    format!("[&]{element}")
                }
            }
            Type::RawPointer { target: None } => "rawptr".to_string(),
            Type::RawPointer {
                target: Some(target),
            } => format!("*{}", self.real_text(*target, name_of)),
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
mod tests;
