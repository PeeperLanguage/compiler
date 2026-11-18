//! Interned types: equal types have equal ids, so comparing types is comparing
//! two numbers.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write as _;
use std::hash::Hash;
use std::hash::Hasher;
use std::ops::Index;

use rustc_hash::FxHashMap;

use crate::ConstArguments;
use crate::DefId;
use crate::constant::Value;
use crate::constant::integer_text;
use crate::mir;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct TypeId(u32);

/// What one `const` parameter is given: a type for `const T: type`, else a
/// value known when compiling.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum ConstArgument {
    Type(TypeId),
    Value(Constant),
}

/// A struct's functions that are an interface's methods, by its place in
/// `Types`'s list of them: the table of an interface reference.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ImplementationId(u32);

/// An integer type: `i5` is signed with 5 bits.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Integer {
    pub signed: bool,
    pub bits: u32,
}

/// What the compiler works out from a struct's fields, once, so that nothing
/// walks them again: they nest as deep as the declarations do and can repeat.
/// None of it is written in the source. `define_struct` makes it.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct StructInternals {
    /// Where each field is from the value's start, in bytes, in field order
    /// (`layout::field_placed`).
    pub offsets: Box<[u64]>,
    /// Its `layout` for the target.
    pub size: u64,
    pub align: u64,
    /// What `holders_in` gives for it, and whether one of those is mutable.
    pub holders: usize,
    pub mutable_holder: bool,
}

/// What the compiler works out from an array's element, once, so that a size
/// and a copy check need not walk the levels of arrays in arrays, which nest
/// as deep as the source does. `Types::array` makes it.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ArrayInternals {
    /// What the arrays in arrays end in, never an array.
    pub base: TypeId,
    /// How many of `base` there are in all.
    pub count: u64,
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
    /// `&cstr`, a reference to C text: the address of bytes that end in a
    /// zero byte, as C functions take it. C text alone has no fixed size, so
    /// no value of it exists and this type stands for the reference.
    CString,
    /// `&str`, a reference to text held elsewhere (a literal's in the
    /// program's data): its bytes' address and count, laid out as a `&[]u8`
    /// slice. Text alone has no fixed size, so no value of it exists and
    /// this type stands for the reference.
    Str,
    /// `origins`: the positions of the parameters a returned reference may
    /// point into, in order; empty when the result holds no reference.
    /// `variadic`: the last parameter, a slice, takes all the arguments past
    /// the others.
    Function {
        parameters: Box<[TypeId]>,
        result: TypeId,
        origins: Box<[u32]>,
        variadic: bool,
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
    /// value's type is the declaration's own (`Type::Struct`), which takes
    /// over this id (`define_struct`); only a raw pointer's target can stay
    /// this, when its declaration has an error. `arguments`: the types a
    /// generic struct is given, `Box<i32>`, which make its own copy;
    /// `ConstArguments::NONE` for any other.
    Named {
        definition: DefId,
        arguments: ConstArguments,
    },
    /// The type a struct declaration makes, or a copy of a generic one for
    /// its `arguments` (`Type::Named`), with its fields' types in order.
    /// The declaration and the arguments are its identity, so two structs
    /// with the same fields differ; the field names stay in the declaration.
    /// A field held by value has its own struct's type, so the type is
    /// complete. A raw pointer in a field can reach the struct again, so a
    /// walk over types stops at pointers or remembers the structs it met.
    Struct {
        definition: DefId,
        arguments: ConstArguments,
        fields: Box<[TypeId]>,
        internals: StructInternals,
    },
    /// `[length]element`: `length` values of the `element` type, one after
    /// another.
    Array {
        element: TypeId,
        length: u64,
        internals: ArrayInternals,
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
    /// `&[]T`, or `&mut []T` when `mutable`: a view of some `element`
    /// values held elsewhere, one after another, with how many there are
    /// known only when the program runs. The loan rules hold for it as for
    /// a reference (`Type::borrows`).
    Slice {
        mutable: bool,
        element: TypeId,
    },
    /// The type an interface declaration makes: the function type of each of
    /// its methods, in declaration order, whose first parameter is `self` as
    /// an `InterfaceReference` to this type, or this type for a `self` taken
    /// whole. The declaration is its identity; the method names stay in it.
    /// A value of it, an owned interface, is laid out as an
    /// `InterfaceReference`: the address of the struct it took and that
    /// struct's table. The struct is moved into a hidden variable of the
    /// block, which the value holds a `&mut` loan on, so it lives as long as
    /// its struct.
    Interface {
        definition: DefId,
        methods: Box<[TypeId]>,
    },
    /// `&Shape`, or `&mut Shape` when `mutable`: a view of a value of any
    /// struct type with the methods of the `interface`: its address and the
    /// table of those methods, in the interface's order. The loan rules hold
    /// for it as for a reference (`Type::borrows`).
    InterfaceReference {
        mutable: bool,
        interface: TypeId,
    },
    /// `*T`, or `rawptr` without a `target`: an address that nothing
    /// checks, `none` when null; what it points to can always be written.
    RawPointer {
        target: Option<TypeId>,
    },
}

impl Type {
    /// The type a `.*` of this type reads: a reference's target, an
    /// interface reference's interface, or a typed raw pointer's target.
    pub fn pointee(&self) -> Option<TypeId> {
        match *self {
            Type::Reference { target, .. }
            | Type::InterfaceReference {
                interface: target, ..
            } => Some(target),
            Type::RawPointer { target } => target,
            _ => None,
        }
    }

    /// Whether a value of this type is laid out as a slice, an address then
    /// a length: a slice, or a `&str`, which views bytes.
    pub fn is_slice_shaped(&self) -> bool {
        matches!(self, Type::Slice { .. } | Type::Str)
    }

    /// `Some(mutable)` for a type whose values borrow what they view, a
    /// reference, a slice or an interface reference: the loan rules hold for
    /// each.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub fn borrows(&self) -> Option<bool> {
        // shortcut: `&str` and `&cstr` hold no loan, as each is a literal's
        // (which borrows nothing) or C's (which Peeper cannot see); make them
        // loan holders when one can view memory that ends, with a way for a
        // literal's to be returned with no parameter, which a loan holder's
        // cannot be today.
        match *self {
            Type::Reference { mutable, .. }
            | Type::Slice { mutable, .. }
            | Type::InterfaceReference { mutable, .. } => Some(mutable),
            // A struct, an array or an owned interface may hold loans without
            // being one (`Types::holds_loans`).
            Type::Invalid
            | Type::Void
            | Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::Str
            | Type::Function { .. }
            | Type::Alias { .. }
            | Type::Named { .. }
            | Type::Struct { .. }
            | Type::Array { .. }
            | Type::Range { .. }
            | Type::Interface { .. }
            | Type::RawPointer { .. } => None,
        }
    }

    /// The element type of an array or a slice, with an array's length; a
    /// slice's is known only when the program runs.
    pub fn elements(&self) -> Option<(TypeId, Option<u64>)> {
        match *self {
            Type::Array {
                element, length, ..
            } => Some((element, Some(length))),
            Type::Slice { element, .. } => Some((element, None)),
            _ => None,
        }
    }

    /// Whether the type is a number: an integer or a float.
    pub fn is_number(&self) -> bool {
        matches!(self, Type::Integer(_) | Type::Float(_))
    }

    /// The declaration that names the type, for a type with one: a struct,
    /// an interface, or a declared name not checked yet.
    pub fn definition(&self) -> Option<DefId> {
        match *self {
            Type::Named { definition, .. }
            | Type::Struct { definition, .. }
            | Type::Interface { definition, .. } => Some(definition),
            _ => None,
        }
    }
}

/// A type's fields (`Type::members`): the declaration that names them, and
/// each field's type and offset from the value's start, in field order.
#[derive(Clone, Copy, Debug)]
pub struct Members<'t> {
    pub definition: DefId,
    pub fields: &'t [TypeId],
    pub offsets: &'t [u64],
}

/// How a value of one type becomes a value of another (`Types::conversion`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Conversion {
    /// Nothing to do: one type, perhaps under an alias, or a `*T` used
    /// as a `rawptr`, the same address.
    Same,
    /// Every value of the one type is a value of the other, so it converts
    /// without `as`, by the cast: to a wider integer of the same signedness,
    /// from unsigned to a wider signed integer, and from `f32` to `f64`.
    Implicit(mir::Cast),
    /// A `&mut T` used as a `&T`, a reference to an array used as a slice
    /// of it, a `&mut []T` used as a `&[]T`, or a `&mut Shape` used as a
    /// `&Shape`: lent again, so the reference or slice itself is not moved.
    Reborrow,
    /// A `&T` or `&mut T` of a struct `T` used as an interface reference,
    /// `&Shape` (`&mut Shape` from a `&mut T` only): its address with the
    /// table of `T`'s methods, when `T` has every method of the interface,
    /// which the checker finds; or a struct moved into an owned interface,
    /// `Shape`, which views it the same way.
    ToInterface,
    /// Converts only when written with `as`, by the cast: between integers
    /// that lose values, between floats and integers, from `f64` to `f32`,
    /// from `bool` to an integer, and between raw pointers of other types
    /// (the same address).
    NeedsCast(mir::Cast),
    /// No conversion.
    Never,
}

/// The types of a program, for one compile (`check::Checker` holds them).
pub struct Types {
    pub usize: TypeId, // as wide as a pointer of the target
    types: Vec<Type>,
    ids: FxHashMap<Type, TypeId>,
    /// Each implementation's functions, in its interface's order, by
    /// `ImplementationId`; the checker adds one at the first conversion of
    /// a reference to its struct to a reference to its interface.
    implementations: Vec<Box<[DefId]>>,
    /// Each list of `const` arguments, by `ConstArguments`, the empty one
    /// first.
    argument_lists: Vec<Box<[ConstArgument]>>,
    argument_ids: FxHashMap<Box<[ConstArgument]>, ConstArguments>,
}

impl Types {
    /// The fixed types alone, each at its `TypeId` constant, and `usize` of
    /// `pointer_bits` bits.
    pub fn new(pointer_bits: u32) -> Types {
        let mut types = Types {
            usize: TypeId::INVALID,
            types: Vec::new(),
            ids: FxHashMap::default(),
            implementations: Vec::new(),
            argument_lists: Vec::new(),
            argument_ids: FxHashMap::default(),
        };
        types.const_arguments(&[]);
        for ty in builtin::FIXED {
            types.intern(ty.clone());
        }
        types.usize = types.intern(Type::Integer(Integer {
            signed: false,
            bits: pointer_bits,
        }));
        types
    }

    /// The id of the list of `const` arguments `list`, made the first time
    /// it is seen.
    pub fn const_arguments(&mut self, list: &[ConstArgument]) -> ConstArguments {
        if let Some(&id) = self.argument_ids.get(list) {
            return id;
        }
        let id = ConstArguments(
            u32::try_from(self.argument_lists.len()).expect("fewer than 2^32 lists"),
        );
        self.argument_lists.push(list.into());
        self.argument_ids.insert(list.into(), id);
        id
    }

    /// What the list `arguments` holds.
    pub fn arguments(&self, arguments: ConstArguments) -> &[ConstArgument] {
        &self.argument_lists[arguments.0 as usize]
    }

    /// `name<T, 3>`: the function `name` of the copy made for `arguments`,
    /// each type as `name_of` names its declaration, each value as Peeper
    /// text; `name` alone without `const` arguments.
    pub fn copy_text<N: fmt::Display>(
        &self,
        name: String,
        arguments: ConstArguments,
        name_of: impl Fn(DefId) -> N + Copy,
    ) -> String {
        let given = self.arguments(arguments);
        if given.is_empty() {
            return name;
        }
        let texts: Vec<String> = (given.iter())
            .map(|argument| self.argument_text(argument, name_of))
            .collect();
        format!("{name}<{}>", texts.join(", "))
    }

    /// `argument` as `copy_text` writes it: a type as `name_of` names its
    /// declaration, a value as Peeper text.
    pub fn argument_text<N: fmt::Display>(
        &self,
        argument: &ConstArgument,
        name_of: impl Fn(DefId) -> N + Copy,
    ) -> String {
        match argument {
            ConstArgument::Type(ty) => self.text(*ty, name_of),
            ConstArgument::Value(Constant { ty, value }) => {
                value_text(value, &self[*ty]).to_string()
            }
        }
    }

    /// The parameters a reference returned by a function of type `signature`
    /// may point into.
    pub fn origins(&self, signature: TypeId) -> &[u32] {
        match &self[signature] {
            Type::Function { origins, .. } => origins,
            // No origins would let a returned reference dangle unseen.
            _ => unreachable!("a call to a function with a signature error is not lowered"),
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

    /// Whether `a` and `b` are one type, aliases looked through.
    pub fn same(&self, a: TypeId, b: TypeId) -> bool {
        self.real(a) == self.real(b)
    }

    /// `Some(whether one is mutable)` for a type whose values hold loans: a
    /// reference, a slice, an owned interface (on the struct it took), or a
    /// struct or array with one in a field or element, however deep.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub fn holds_loans(&self, id: TypeId) -> Option<bool> {
        match &self[id] {
            Type::Struct { internals, .. } => {
                (internals.holders > 0).then_some(internals.mutable_holder)
            }
            Type::Array { internals, .. } => self.holds_loans(internals.base),
            Type::Interface { .. } => Some(true),
            // Any other type holds a loan only by being one.
            other @ (Type::Invalid
            | Type::Void
            | Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::Str
            | Type::Function { .. }
            | Type::Alias { .. }
            | Type::Named { .. }
            | Type::Range { .. }
            | Type::Reference { .. }
            | Type::Slice { .. }
            | Type::InterfaceReference { .. }
            | Type::RawPointer { .. }) => other.borrows(),
        }
    }

    /// How many references, slices, interface references and owned
    /// interfaces a value of the type `id` is: one for each, its fields' for
    /// a struct, its elements' for an array, as no other type holds one. Not
    /// past `usize`.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub fn holders_in(&self, id: TypeId) -> usize {
        match &self[id] {
            Type::Struct { internals, .. } => internals.holders,
            Type::Array { internals, .. } => usize::try_from(internals.count)
                .unwrap_or(usize::MAX)
                .saturating_mul(self.holders_in(internals.base)),
            Type::Interface { .. } => 1,
            other @ (Type::Invalid
            | Type::Void
            | Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::Str
            | Type::Function { .. }
            | Type::Alias { .. }
            | Type::Named { .. }
            | Type::Range { .. }
            | Type::Reference { .. }
            | Type::Slice { .. }
            | Type::InterfaceReference { .. }
            | Type::RawPointer { .. }) => usize::from(other.borrows().is_some()),
        }
    }

    /// Whether borrowing a place of type `place` as `borrow` lends again the
    /// elements of the slice there: a slice borrowed as a slice, not as
    /// `&[]T`, which lends the slice itself.
    pub fn lends_slice_elements(&self, place: TypeId, borrow: TypeId) -> bool {
        matches!(
            (&self[place], &self[borrow]),
            (Type::Slice { .. }, Type::Slice { .. })
        )
    }

    /// `holders_in` for a struct of `fields`, not past `usize`.
    pub fn holders_in_fields(&self, fields: &[TypeId]) -> usize {
        (fields.iter())
            .map(|&field| self.holders_in(field))
            .fold(0, usize::saturating_add)
    }

    /// Whether a value of the type `id` copies when used by value: numbers,
    /// `bool`, `&cstr`, shared references and slices, raw pointers, ranges, and arrays of values
    /// that copy. Any other value moves, and the variable it moved from is
    /// unusable until set again.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub fn is_copy(&self, id: TypeId) -> bool {
        match self[id] {
            Type::Bool
            | Type::Integer(_)
            | Type::Float(_)
            | Type::CString
            | Type::Str
            | Type::RawPointer { .. }
            | Type::Range { .. } => true,
            Type::Reference { mutable, .. }
            | Type::Slice { mutable, .. }
            | Type::InterfaceReference { mutable, .. } => !mutable,
            Type::Array { internals, .. } => self.is_copy(internals.base),
            Type::Struct { .. } | Type::Interface { .. } => false,
            // No value of these is moved: an error, nothing, or not yet a value.
            Type::Invalid | Type::Void | Type::Function { .. } => true,
            Type::Alias { .. } | Type::Named { .. } => {
                unreachable!("indexing `Types` looks through aliases; no value is named")
            }
        }
    }

    /// The type `id` stands for: itself, or the type an alias names.
    pub fn real(&self, id: TypeId) -> TypeId {
        match self.types[id.0 as usize] {
            Type::Alias { real, .. } => real,
            _ => id,
        }
    }

    /// Feeds the type `id` to `hasher` by what it is, not by its id, which
    /// differs between compiles: a declared type by what `declaration`
    /// feeds for it, then what a copy of it is given. A struct's fields and
    /// an interface's methods are not fed: its declaration holds them.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    pub fn hash<H: Hasher>(
        &self,
        id: TypeId,
        hasher: &mut H,
        declaration: impl Fn(DefId, &mut H) + Copy,
    ) {
        let ty = &self[id];
        std::mem::discriminant(ty).hash(hasher);
        match ty {
            Type::Integer(integer) => integer.hash(hasher),
            Type::Float(float) => float.hash(hasher),
            Type::Function {
                parameters,
                result,
                origins,
                variadic,
            } => {
                parameters.len().hash(hasher);
                for &parameter in parameters {
                    self.hash(parameter, hasher, declaration);
                }
                self.hash(*result, hasher, declaration);
                origins.hash(hasher);
                variadic.hash(hasher);
            }
            Type::Named {
                definition,
                arguments,
            }
            | Type::Struct {
                definition,
                arguments,
                ..
            } => {
                declaration(*definition, hasher);
                let given = self.arguments(*arguments);
                given.len().hash(hasher);
                for argument in given {
                    match argument {
                        ConstArgument::Type(ty) => self.hash(*ty, hasher, declaration),
                        ConstArgument::Value(constant) => {
                            self.hash(constant.ty, hasher, declaration);
                            constant.value.hash(hasher);
                        }
                    }
                }
            }
            Type::Interface { definition, .. } => declaration(*definition, hasher),
            Type::Array { internals, .. } => {
                // Lengths are any number: the count keeps one from reading as
                // the type after it.
                self.array_lengths(id).count().hash(hasher);
                for length in self.array_lengths(id) {
                    length.hash(hasher);
                }
                self.hash(internals.base, hasher, declaration);
            }
            Type::Range { element, .. } => self.hash(*element, hasher, declaration), // `step` follows
            Type::Reference { mutable, target }
            | Type::Slice {
                mutable,
                element: target,
            }
            | Type::InterfaceReference {
                mutable,
                interface: target,
            } => {
                mutable.hash(hasher);
                // References and pointers nest as deep as the source does.
                source::grow_stack(|| self.hash(*target, hasher, declaration));
            }
            Type::RawPointer { target } => {
                target.is_some().hash(hasher);
                if let Some(target) = target {
                    source::grow_stack(|| self.hash(*target, hasher, declaration));
                }
            }
            Type::Invalid | Type::Void | Type::Bool | Type::CString | Type::Str => {}
            Type::Alias { .. } => unreachable!("indexing looks through aliases"),
        }
    }

    /// The type `id` as a program writes it, a declared type or an alias by its name,
    /// which `name_of` gives for its declaration: an alias is followed by the
    /// type itself (`Meters (aka i64)`).
    pub fn text<N: fmt::Display>(&self, id: TypeId, name_of: impl Fn(DefId) -> N + Copy) -> String {
        let mut text = String::new();
        if let Type::Alias { definition, real } = self.types[id.0 as usize] {
            _ = write!(text, "{} (aka ", name_of(definition));
            self.write_real_text(&mut text, real, name_of);
            text.push(')');
        } else {
            self.write_real_text(&mut text, id, name_of);
        }
        text
    }

    /// Writes the type `id` itself to `out`, aliases looked through: `u8`,
    /// `fn(i32) -> bool`, a named type by its name. Into one text, so a type
    /// nested as deep as the source does costs its length, not its square.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    fn write_real_text<N: fmt::Display>(
        &self,
        out: &mut String,
        id: TypeId,
        name_of: impl Fn(DefId) -> N + Copy,
    ) {
        let id = self.real(id);
        match &self[id] {
            Type::Invalid => out.push_str("{invalid}"),
            Type::Void => out.push_str("void"),
            Type::Bool | Type::Float(_) | Type::CString | Type::Str => {
                let (text, _) = builtin::NAMED
                    .iter()
                    .find(|&&(_, named)| named == id)
                    .expect("in `NAMED`");
                // A text type is the view, written behind `&`.
                if let Type::CString | Type::Str = self[id] {
                    out.push('&');
                }
                out.push_str(text);
            }
            Type::Integer(integer) => _ = write!(out, "{integer}"),
            Type::Function {
                parameters, result, ..
            } => {
                out.push_str("fn(");
                for (index, &parameter) in parameters.iter().enumerate() {
                    if index > 0 {
                        out.push_str(", ");
                    }
                    self.write_real_text(out, parameter, name_of);
                }
                out.push(')');
                if *result != TypeId::VOID {
                    out.push_str(" -> ");
                    self.write_real_text(out, *result, name_of);
                }
            }
            // A generic struct's copy as written: `Box<i32>`.
            Type::Named {
                definition,
                arguments,
            }
            | Type::Struct {
                definition,
                arguments,
                ..
            } => {
                let name = name_of(*definition).to_string();
                out.push_str(&self.copy_text(name, *arguments, name_of));
            }
            Type::Interface { definition, .. } => _ = write!(out, "{}", name_of(*definition)),
            Type::Array { internals, .. } => {
                for length in self.array_lengths(id) {
                    _ = write!(out, "[{length}]");
                }
                self.write_real_text(out, internals.base, name_of);
            }
            Type::Range { element, .. } => {
                _ = write!(out, "{}[", builtin::RANGE);
                self.write_real_text(out, *element, name_of);
                out.push(']');
            }
            // References, slices and pointers nest as deep as the source does.
            Type::Reference { mutable, target }
            | Type::InterfaceReference {
                mutable,
                interface: target,
            } => {
                out.push_str(if *mutable { "&mut " } else { "&" });
                source::grow_stack(|| self.write_real_text(out, *target, name_of));
            }
            Type::Slice { mutable, element } => {
                out.push_str(if *mutable { "&mut []" } else { "&[]" });
                source::grow_stack(|| self.write_real_text(out, *element, name_of));
            }
            Type::RawPointer { target: None } => out.push_str("rawptr"),
            Type::RawPointer {
                target: Some(target),
            } => {
                out.push('*');
                source::grow_stack(|| self.write_real_text(out, *target, name_of));
            }
            Type::Alias { .. } => unreachable!("`real` looks through aliases"),
        }
    }
}

impl Index<TypeId> for Types {
    type Output = Type;

    /// The type itself, aliases looked through (`Types::real`).
    fn index(&self, id: TypeId) -> &Type {
        &self.types[self.real(id).0 as usize]
    }
}

mod arrays;
pub mod builtin;
mod conversions;
mod integers;
mod interfaces;
mod primitive;
pub mod ranges;
mod structs;
#[cfg(test)]
mod tests;

/// A constant's type and value.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Constant {
    pub ty: TypeId,
    pub value: Value,
}

/// `value`, of the type `ty`, as Peeper text: an integer in decimal, read
/// through its type's sign, when 128 bits hold it, else every limb in hex
/// (`integer_text`); a float as the shortest text that reads back as it, in
/// its own precision when it holds an `f32` exactly (a value that does not
/// shows in full); a `&str` as a `"..."` literal, a `&cstr` as `c"..."`; `none` for the null
/// address.
pub fn value_text<'v>(value: &'v Value, ty: &'v Type) -> impl Display + 'v {
    fmt::from_fn(move |out| match (value, ty) {
        (Value::Bool(value), _) => write!(out, "{value}"),
        (Value::Null, _) => out.write_str("none"),
        (Value::Bytes(bytes), Type::Str) => write!(out, "\"{}\"", bytes.escape_ascii()),
        (Value::Bytes(bytes), _) => write!(out, "c\"{}\"", bytes.escape_ascii()),
        (Value::Float(bits), Type::Float(Float::F32))
            if f64::from(f64::from_bits(*bits) as f32) == f64::from_bits(*bits) =>
        {
            write!(out, "{}", f64::from_bits(*bits) as f32)
        }
        (Value::Float(bits), _) => write!(out, "{}", f64::from_bits(*bits)),
        (Value::Integer(limbs), Type::Integer(integer)) => {
            let value = match integer.bits {
                ..=128 => bigint::extend(integer.bits, integer.signed, limbs, 128),
                _ => limbs.clone(),
            };
            let [low, high, rest @ ..] = value.as_slice() else {
                unreachable!("an integer constant has at least two limbs")
            };
            let low = *low as u128 | (*high as u128) << 64;
            if !rest.iter().all(|&limb| limb == 0) {
                write!(out, "{}", integer_text(&value))
            } else if integer.signed && integer.bits <= 128 {
                write!(out, "{}", low as i128)
            } else {
                write!(out, "{low}")
            }
        }
        (Value::Integer(_), _) => unreachable!("an integer constant has an integer type"),
    })
}
