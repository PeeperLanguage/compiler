//! The type table. Each entry holds the kind that made it, and the kind
//! holds every answer about its types, so a container asks its parts by
//! calling their kinds, never by naming another feature.

use std::collections::HashMap;

use model::facts::Facts;
use model::facts::Target;
use model::mir::Operand;
use model::mir::Operator;
use model::types::TypeId;

use crate::check::Checker;
use crate::lower::Lowering;

/// Every question any phase asks about a type. Each field is required, so
/// a new kind that leaves one out does not build:
/// `error[E0063]: missing field `field` in initializer of `TypeKind``.
pub struct TypeKind {
    /// Its facts, from its own `detail` and its parts' facts only.
    pub facts: fn(detail: u32, parts: &[Facts], target: Target) -> Facts,
    /// The type as written in messages.
    pub text: fn(&Checker<'_>, TypeId) -> String,
    /// How a value of the type `from` becomes a value of `to`, a type of
    /// this kind; or why it cannot. The core asks it only when the two are
    /// not one type.
    pub convert: fn(&Checker<'_>, from: TypeId, to: TypeId) -> Result<Converter, String>,
    /// What `left operator right` gives with `left` of this kind.
    pub operator:
        fn(&mut Checker<'_>, Operator, left: TypeId, right: TypeId) -> Result<Operation, String>,
    /// The field `name` of a value of it.
    pub field: fn(&Checker<'_>, TypeId, name: &str) -> Option<Field>,
    /// What a value of it points at, for a reference.
    pub pointee: fn(&Checker<'_>, TypeId) -> Option<Pointee>,
}

/// What a reference points at, and whether it may be set through it.
#[derive(Clone, Copy)]
pub struct Pointee {
    pub ty: TypeId,
    pub mutable: bool,
}

/// Lowers a value of type `from` into one of type `to`.
pub type Converter = fn(&mut Lowering<'_>, value: Operand, from: TypeId, to: TypeId) -> Operand;

/// The type of `left operator right`, and how to lower it.
#[derive(Clone, Copy)]
pub struct Operation {
    pub result: TypeId,
    pub lower: OperatorLowering,
}

/// Lowers `left operator right`, both of type `operands`.
pub type OperatorLowering = fn(
    &mut Lowering<'_>,
    Operator,
    left: Operand,
    right: Operand,
    operands: TypeId,
    result: TypeId,
) -> Operand;

/// A field: its place among the fields, where it is, its type, and where
/// its loan holders are among the value's (`Projection::Field`).
#[derive(Clone, Copy)]
pub struct Field {
    pub index: u32,
    pub offset: u64,
    pub ty: TypeId,
    pub first_holder: u32,
    pub holders: u32,
}

/// The answers of a kind with no fields, named in its `TypeKind` so the
/// choice is written down, not a default.
pub fn no_fields(_: &Checker<'_>, _: TypeId, _: &str) -> Option<Field> {
    None
}

pub fn no_pointee(_: &Checker<'_>, _: TypeId) -> Option<Pointee> {
    None
}

pub fn no_operators(
    cx: &mut Checker<'_>,
    _: Operator,
    left: TypeId,
    _: TypeId,
) -> Result<Operation, String> {
    Err(format!("{} has no operators", cx.type_text(left)))
}

pub fn no_conversions(cx: &Checker<'_>, from: TypeId, to: TypeId) -> Result<Converter, String> {
    Err(format!(
        "expected {}, found {}",
        cx.type_text(to),
        cx.type_text(from)
    ))
}

pub struct TypeEntry {
    pub kind: &'static TypeKind,
    pub parts: Box<[TypeId]>,
    pub detail: u32,
    /// `None` until known: a declared struct's facts wait for its fields.
    pub facts: Option<Facts>,
}

pub struct Types {
    entries: Vec<TypeEntry>,
    ids: HashMap<(usize, Box<[TypeId]>, u32), TypeId>,
}

impl Default for Types {
    fn default() -> Types {
        let mut types = Types {
            entries: Vec::new(),
            ids: HashMap::new(),
        };
        for (kind, id) in [
            (&INVALID, TypeId::INVALID),
            (&VOID, TypeId::VOID),
            (&BOOL, TypeId::BOOL),
        ] {
            assert_eq!(types.make(kind, Box::new([]), 0), id);
        }
        types
    }
}

impl Types {
    pub fn entry(&self, ty: TypeId) -> &TypeEntry {
        &self.entries[ty.0 as usize]
    }

    pub fn len(&self) -> usize {
        self.entries.len()
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }

    /// Whether `ty` is of `kind`. Kinds are statics, so their addresses
    /// tell them apart.
    pub fn is(&self, ty: TypeId, kind: &'static TypeKind) -> bool {
        std::ptr::eq(self.entry(ty).kind, kind)
    }

    /// The one id of a type made of `kind`, `parts` and `detail`.
    pub(crate) fn make(
        &mut self,
        kind: &'static TypeKind,
        parts: Box<[TypeId]>,
        detail: u32,
    ) -> TypeId {
        let key = (kind as *const TypeKind as usize, parts, detail);
        if let Some(&id) = self.ids.get(&key) {
            return id;
        }
        let id = self.push(kind, key.1.clone(), detail);
        self.ids.insert(key, id);
        id
    }

    /// A new type of its own, such as a declared struct, whose parts come
    /// later (`define`).
    pub(crate) fn declare(&mut self, kind: &'static TypeKind) -> TypeId {
        self.push(kind, Box::new([]), 0)
    }

    pub(crate) fn define(&mut self, ty: TypeId, parts: Box<[TypeId]>) {
        self.entries[ty.0 as usize].parts = parts;
    }

    pub(crate) fn set_facts(&mut self, ty: TypeId, facts: Facts) {
        self.entries[ty.0 as usize].facts = Some(facts);
    }

    /// Every type's facts, once all are known, for the MIR program.
    pub(crate) fn all_facts(&self) -> Vec<Facts> {
        (self.entries.iter())
            .map(|entry| {
                entry
                    .facts
                    .expect("every type's facts are known after checking")
            })
            .collect()
    }

    fn push(&mut self, kind: &'static TypeKind, parts: Box<[TypeId]>, detail: u32) -> TypeId {
        let id = TypeId(self.entries.len() as u32);
        self.entries.push(TypeEntry {
            kind,
            parts,
            detail,
            facts: None,
        });
        id
    }
}

fn nothing(_: u32, _: &[Facts], _: Target) -> Facts {
    Facts::NOTHING
}

/// The type of what was refused. It answers every question with a yes of
/// its own type, so the refusal is reported once and no feature checks for
/// it. A program with a refusal is never lowered.
pub static INVALID: TypeKind = TypeKind {
    facts: nothing,
    text: |_, _| "{unknown}".to_string(),
    convert: |_, _, _| Ok(|_, value, _, _| value),
    operator: |_, _, _, _| {
        Ok(Operation {
            result: TypeId::INVALID,
            lower: |_, _, _, _, _, _| unreachable!("refused"),
        })
    },
    field: |_, _, _| {
        Some(Field {
            index: 0,
            offset: 0,
            ty: TypeId::INVALID,
            first_holder: 0,
            holders: 0,
        })
    },
    pointee: no_pointee,
};

pub static VOID: TypeKind = TypeKind {
    facts: nothing,
    text: |_, _| "void".to_string(),
    convert: no_conversions,
    operator: no_operators,
    field: no_fields,
    pointee: no_pointee,
};

/// `bool`, which the core's conditions need; its name and literals come
/// from the bools feature.
pub static BOOL: TypeKind = TypeKind {
    facts: |_, _, _| Facts::scalar(1),
    text: |_, _| "bool".to_string(),
    convert: no_conversions,
    operator: no_operators,
    field: no_fields,
    pointee: no_pointee,
};
