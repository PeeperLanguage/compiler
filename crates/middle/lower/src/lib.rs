//! Lowering a function's THIR into the MIR of core concepts
//! (`model::mir`, docs/feature-free-mir.md): the only phase after
//! checking that reads the program's types. Every language feature is
//! written here as bytes, offsets, loads, stores and calls; nothing after
//! it names one.

mod aggregates;
pub mod binary;
mod c_abi;
mod expression;
mod print;
mod statement;
#[cfg(test)]
mod tests;

use std::num::NonZeroU32;

use arena::Arena;
use arena::Id;
use arena::Lists;
use model::layout::field_placed;
use model::layout::layout;
use model::layout::struct_layout;
use model::machine::Machine;
use model::mir::Block;
use model::mir::Body;
use model::mir::Class;
use model::mir::Comparison;
use model::mir::DebugMember;
use model::mir::DebugScope;
use model::mir::DebugType;
use model::mir::DebugTypeId;
use model::mir::DebugTypes;
use model::mir::Global;
use model::mir::Holder;
use model::mir::InstructionKind;
use model::mir::IntegerOp;
use model::mir::Operand;
use model::mir::Passing;
use model::mir::Place;
use model::mir::REFERENCE_WORDS;
use model::mir::Root;
use model::mir::SLICE_WORDS;
use model::mir::Signature;
use model::mir::Slot;
use model::mir::Step;
use model::mir::Terminator;
use model::program::Module;
use model::semantic_types::Float;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::semantic_types::range_fields;
use model::thir;
use source::Span;

use crate::expression::float_class;

/// The MIR of the checked body `body` of a function of the type
/// `function`, for `target`. `None` when checking it reported an error
/// (`has_errors`) or left it incomplete. `modules` names types for a
/// debugger (`debug_types`, shared by the program's bodies).
pub fn lower(
    body: &thir::Body,
    function: TypeId,
    types: &Types,
    target: &'static Machine,
    modules: &[Module],
    debug_types: &mut DebugTypes,
) -> Option<Body> {
    if body.has_errors {
        return None;
    }
    let Type::Function {
        parameters, result, ..
    } = &types[function]
    else {
        unreachable!("a body is a function's")
    };
    let pointer_bytes = u64::from(target.pointer_bits / 8);
    let mut lowering = Lowering {
        thir: body,
        types,
        target,
        modules,
        debug_types,
        mir: Body {
            blocks: Arena::default(),
            instructions: Arena::default(),
            slots: Arena::default(),
            signature: Signature {
                parameters: Box::default(), // below, once passing can be asked
                result: None,
                c: None,
                origins: Box::default(),
            },
            returned: match &types[types.real(*result)] {
                Type::Interface { .. } => Some("an interface holding"),
                Type::Slice { .. } => Some("a slice of"),
                _ if types.holds_loans(*result).is_some() => Some("a reference into"),
                _ => None,
            },
            debug_scopes: Arena::default(),
            result_debug_type: None, // below, once a debug type can be made
            globals: Vec::new(),
            signatures: Vec::new(),
            lists: Lists::default(),
        },
        block: Id::from_index(0),
        instructions: Vec::new(),
        debug_scope: Id::from_index(0),
        loops: Vec::new(),
        declared: Vec::new(),
        kept: vec![Vec::new(); body.variables.len()],
        pointer_bytes,
        usize: Class::integer(target.pointer_bits),
    };
    lowering.mir.signature = lowering.signature(function);
    lowering.mir.result_debug_type =
        (types.real(*result) != TypeId::VOID).then(|| lowering.debug_type(*result));
    // One debug scope per block and one slot per THIR variable, in order:
    // block `i` is debug scope `i`, and the ids of variables match.
    for block in body.blocks.ids() {
        let parent = body.blocks[block]
            .parent
            .map(|parent| Id::from_index(parent.index()));
        let span = body.blocks.span(block);
        lowering.mir.debug_scopes.push(DebugScope { parent }, span);
    }
    for id in body.variables.ids() {
        let variable = body.variables[id];
        if variable.ty == TypeId::INVALID {
            return None;
        }
        let ty = types.real(variable.ty);
        let (size, align) = layout(types, ty, target);
        let debug_type = (!variable.hidden).then(|| lowering.debug_type(ty));
        lowering.mir.slots.push(
            Slot {
                size,
                align,
                holders: lowering.holders(ty, 0).into(),
                debug_type,
                debug_scope: Id::from_index(variable.scope.index()),
                mutable: variable.mutable,
                initialized: variable.initialized,
            },
            body.variables.span(id),
        );
    }
    for id in body.exprs.ids() {
        if let thir::ExprKind::Temporary {
            variable,
            kept_by: Some(kept_by),
            ..
        } = body.exprs[id].kind
        {
            lowering.kept[kept_by.index()].push(slot_of(variable));
        }
    }
    let function_block = Id::from_index(0);
    let span = body.blocks.span(function_block);
    lowering.block = lowering.new_block(span);
    // A parameter passed in memory is the caller's copy already
    // (`Body::parameter_in_memory`).
    for index in 0..parameters.len() {
        let slot = Id::from_index(index);
        if let Passing::Register { class, .. } = lowering.mir.signature.parameters[index] {
            let kind = InstructionKind::Store {
                class,
                place: Place::whole(slot),
                value: Operand::Parameter(index as u32),
            };
            lowering.emit(kind, lowering.mir.slots.span(slot));
        }
        lowering.declare(slot);
    }
    lowering.lower_block(function_block, 0)?; // the parameters are its variables too
    let end = Span::new(span.end as usize - 1, span.end as usize); // the `}`
    lowering.finish(Terminator::FallOff(end));
    Some(lowering.mir)
}

/// The slot of the THIR variable `variable`: their ids match (`lower`).
fn slot_of(variable: Id<thir::Variable>) -> Id<Slot> {
    Id::from_index(variable.index())
}

struct Lowering<'t> {
    thir: &'t thir::Body,
    types: &'t Types,
    target: &'static Machine,
    modules: &'t [Module],
    debug_types: &'t mut DebugTypes,
    mir: Body,
    block: Id<Block>,              // the block being filled
    instructions: Vec<NonZeroU32>, // that block's instructions so far
    debug_scope: Id<DebugScope>,   // the one new instructions are written in
    loops: Vec<Loop>,              // the loops being lowered, innermost last
    /// The slots in scope, in the order declared: those of a block, from
    /// where it starts (`lower_block`), are the last.
    declared: Vec<Id<Slot>>,
    /// The temporaries each variable keeps (THIR's `kept_by`), found before
    /// any code: each joins `declared` just before its variable (`declare`),
    /// so it ends with it on every path.
    kept: Vec<Vec<Id<Slot>>>,
    pointer_bytes: u64,
    usize: Class, // the class of a pointer-wide integer
}

/// Where a loop's `continue` and `break` go, and where the slots they
/// leave start in `declared`.
struct Loop {
    next: Id<Block>, // the start of the next pass
    exit: Id<Block>,
    declared: usize,
}

/// What an expression gives: a register, the memory a value kept there
/// is in, or nothing.
#[derive(Clone, Debug)]
enum Value {
    Register(Operand),
    Memory(Place),
    /// Kept in memory at the place, read at the span out of a variable
    /// whose type moves: whoever takes the value marks it moved once its
    /// bytes are read (`put`, a method's receiver, `print`).
    Moving(Place, Span),
    Nothing,
}

impl Value {
    fn register(&self) -> Operand {
        match self {
            Value::Register(operand) => *operand,
            Value::Memory(_) | Value::Moving(..) | Value::Nothing => {
                unreachable!("the value is a register")
            }
        }
    }

    fn memory(self) -> Place {
        match self {
            Value::Memory(place) => place,
            Value::Moving(..) => unreachable!("a value that moves is taken where it moves"),
            Value::Register(_) | Value::Nothing => unreachable!("the value is kept in memory"),
        }
    }
}

/// How deep the debug types of pointers and structs follow the source:
/// clang runs out of stack on debug types tens of thousands of nodes deep.
const DEEPEST_DEBUG_TYPE: u32 = 5_000;

impl Lowering<'_> {
    /// A new block starting at `span`; `finish` gives it its instructions and
    /// terminator.
    fn new_block(&mut self, span: Span) -> Id<Block> {
        let block = Block {
            instructions: arena::List::EMPTY,
            terminator: Terminator::FallOff(span), // replaced by `finish`
            debug_scope: self.debug_scope,
        };
        self.mir.blocks.push(block, span)
    }

    /// Ends the block being filled with `terminator`.
    fn finish(&mut self, terminator: Terminator) {
        let instructions = self.mir.lists.add(&self.instructions);
        self.instructions.clear();
        self.mir.blocks[self.block] = Block {
            instructions,
            terminator,
            debug_scope: self.debug_scope,
        };
    }

    /// Adds an instruction to the block being filled and gives its value.
    #[inline(always)]
    fn emit(&mut self, kind: InstructionKind, span: Span) -> Operand {
        let instruction = self.mir.instructions.push(
            model::mir::Instruction {
                kind,
                debug_scope: self.debug_scope,
            },
            span,
        );
        self.instructions.push(instruction.into());
        Operand::Value(instruction)
    }

    /// Adds `left operation right` on values of the type `integer`, cut
    /// back to its width when the operation may carry past it (`narrow`).
    fn arithmetic(
        &mut self,
        operation: IntegerOp,
        integer: Integer,
        left: Operand,
        right: Operand,
        span: Span,
    ) -> Operand {
        let class = Class::integer(integer.bits);
        let result = self.integer(operation, class, left, right, span);
        match operation {
            IntegerOp::Add | IntegerOp::Subtract | IntegerOp::Multiply | IntegerOp::ShiftLeft => {
                self.narrow(result, integer, span)
            }
            _ => result,
        }
    }

    /// Adds `left operation right` in `class`, whose width the result fits
    /// (a counter's, or an operation that cannot carry past the width).
    fn integer(
        &mut self,
        operation: IntegerOp,
        class: Class,
        left: Operand,
        right: Operand,
        span: Span,
    ) -> Operand {
        let kind = InstructionKind::Integer {
            operation,
            class,
            left,
            right,
        };
        self.emit(kind, span)
    }

    /// Adds `left comparison right` in `class`, an `I8`.
    fn compare(
        &mut self,
        comparison: Comparison,
        class: Class,
        left: Operand,
        right: Operand,
        span: Span,
    ) -> Operand {
        let kind = InstructionKind::Compare {
            comparison,
            class,
            left,
            right,
        };
        self.emit(kind, span)
    }

    /// Stops the program with "panic: `message`" when `failed`, an `I8`
    /// written at `span`, is not 0.
    fn emit_check(&mut self, failed: Operand, message: &str, span: Span) {
        let kind = InstructionKind::Check {
            failed,
            message: message.into(),
        };
        self.emit(kind, span);
    }

    /// Loads the register of `class` at `place`.
    fn load(&mut self, class: Class, place: Place, span: Span) -> Operand {
        self.emit(InstructionKind::Load { class, place }, span)
    }

    /// Stores the register `value` of `class` at `place`.
    fn store(&mut self, class: Class, place: Place, value: Operand, span: Span) {
        let kind = InstructionKind::Store {
            class,
            place,
            value,
        };
        self.emit(kind, span);
    }

    /// Puts `value`, of the type `ty`, at `place`: a store, or a copy of
    /// the bytes of a value kept in memory. Nothing when the value is
    /// already there.
    fn put(&mut self, place: Place, value: Value, ty: TypeId, span: Span) {
        let (from, moved_at) = match value {
            Value::Register(value) => {
                let class = self.class(ty).expect("a register has a class");
                self.store(class, place, value, span);
                return;
            }
            Value::Memory(from) => (from, None),
            Value::Moving(from, read) => (from, Some(read)),
            Value::Nothing => unreachable!("only a value is put somewhere"),
        };
        if from == place {
            return; // `a = a`: the bytes are already there
        }
        let (size, _) = layout(self.types, ty, self.target);
        let kind = InstructionKind::Copy {
            to: place,
            from: from.clone(),
            size,
        };
        self.emit(kind, span);
        if let Some(read) = moved_at {
            self.mark_moved(from, ty, read);
        }
    }

    /// The value of the type `ty` read at `place`, at `span`: a load,
    /// marked moved when it moves out, or the place itself for a value kept
    /// in memory, `Moving` when it moves out. A value read out of one of
    /// lowering's temporaries moves nothing: nothing reads the temporary
    /// again.
    fn read(&mut self, place: Place, ty: TypeId, span: Span) -> Value {
        let temporary = matches!(
            place.root,
            Root::Slot(slot) if slot.index() >= self.thir.variables.len()
        );
        let moves = !self.types.is_copy(ty) && !temporary;
        let Some(class) = self.class(ty) else {
            return if moves {
                Value::Moving(place, span)
            } else {
                Value::Memory(place)
            };
        };
        let value = self.load(class, place.clone(), span);
        if moves {
            self.mark_moved(place, ty, span);
        }
        Value::Register(value)
    }

    /// `Moved` for the value of the type `ty`, which moves, just read at
    /// `place` at `span`.
    fn mark_moved(&mut self, place: Place, ty: TypeId, span: Span) {
        // As the checker lends one again (`ThirBuilder::reborrow`).
        let lent_again = match self.types[ty] {
            Type::Reference { mutable: true, .. }
            | Type::InterfaceReference { mutable: true, .. } => Some(".*"),
            Type::Slice { mutable: true, .. } => Some("[..]"),
            _ => None,
        };
        let (size, _) = layout(self.types, ty, self.target);
        let kind = InstructionKind::Moved {
            place,
            size,
            lent_again,
        };
        self.emit(kind, span);
    }

    /// A new slot for a value of the type `ty` made at `span`, in scope
    /// until the statement ends (`declared`).
    fn temporary(&mut self, ty: TypeId, span: Span) -> Id<Slot> {
        let (size, align) = layout(self.types, ty, self.target);
        let holders = self.holders(ty, 0).into();
        let slot = self.hidden_slot(size, align, holders, span);
        self.declared.push(slot);
        slot
    }

    /// A new slot holding one register of `class`, in scope until the
    /// statement ends.
    fn register_slot(&mut self, class: Class, span: Span) -> Id<Slot> {
        let size = class.bytes(self.pointer_bytes);
        let slot = self.hidden_slot(size, size.min(16), Box::default(), span);
        self.declared.push(slot);
        slot
    }

    /// A new slot of `size` bytes with no name, declared by whoever asks.
    fn hidden_slot(
        &mut self,
        size: u64,
        align: u64,
        holders: Box<[Holder]>,
        span: Span,
    ) -> Id<Slot> {
        self.mir.slots.push(
            Slot {
                size,
                align,
                holders,
                debug_type: None,
                debug_scope: self.debug_scope,
                mutable: true,
                initialized: true,
            },
            span,
        )
    }

    /// Adds read-only data and gives its operand.
    fn global(&mut self, global: Global) -> Operand {
        self.mir.globals.push(global);
        Operand::Global((self.mir.globals.len() - 1) as u32)
    }

    /// The class of a value of the type `ty`, or `None` for a value kept
    /// in memory.
    fn class(&self, ty: TypeId) -> Option<Class> {
        match self.types[ty] {
            Type::Bool => Some(Class::I8),
            Type::Integer(integer) => Some(Class::integer(integer.bits)),
            Type::Float(float) => Some(float_class(float)),
            Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => {
                Some(Class::Pointer)
            }
            Type::Slice { .. }
            | Type::Str
            | Type::InterfaceReference { .. }
            | Type::Interface { .. }
            | Type::Struct { .. }
            | Type::Array { .. }
            | Type::Range { .. } => None,
            Type::Invalid | Type::Void | Type::Function { .. } | Type::Named(_) => {
                unreachable!("no value has this type")
            }
            Type::Alias { .. } => unreachable!("indexing looks through aliases"),
        }
    }

    /// How a value of the type `ty` is passed to or from a function.
    fn passing(&self, ty: TypeId) -> Passing {
        let ty = self.types.real(ty);
        if let Some(class) = self.class(ty) {
            return Passing::Register {
                class,
                signed: matches!(self.types[ty], Type::Integer(Integer { signed: true, .. })),
            };
        }
        let (size, align) = layout(self.types, ty, self.target);
        Passing::Memory { size, align }
    }

    /// How a function's result of the type `result` is given; `None` for
    /// none.
    fn result_passing(&self, result: TypeId) -> Option<Passing> {
        (self.types.real(result) != TypeId::VOID).then(|| self.passing(result))
    }

    /// How a Peeper function of the type `function` is passed its
    /// parameters and gives its result.
    fn signature(&self, function: TypeId) -> Signature {
        let Type::Function {
            parameters,
            result,
            origins,
            ..
        } = &self.types[function]
        else {
            unreachable!("a signature is a function type's")
        };
        Signature {
            parameters: parameters.iter().map(|&ty| self.passing(ty)).collect(),
            result: self.result_passing(*result),
            c: None,
            origins: origins.clone(),
        }
    }

    /// The pointers, from `at`, in a value of the type `ty` that may hold
    /// a loan, with the words a message names each by: a reference, a slice, an interface reference or
    /// an owned interface (on the struct it took) is one, at its start; a
    /// struct's fields' and an array's elements' are each at theirs.
    fn holders(&self, ty: TypeId, at: u64) -> Vec<Holder> {
        let mut holders = Vec::new();
        self.add_holders(ty, at, &mut holders);
        holders
    }

    fn add_holders(&self, ty: TypeId, at: u64, holders: &mut Vec<Holder>) {
        let ty = self.types.real(ty);
        // A struct holding another twice has 2^depth fields, so a type with
        // no holders is not walked.
        if self.types.holders_in(ty) == 0 {
            return;
        }
        match &self.types[ty] {
            Type::Slice { .. } => holders.push(Holder {
                offset: at,
                words: &SLICE_WORDS,
            }),
            Type::Reference { .. } | Type::InterfaceReference { .. } | Type::Interface { .. } => {
                holders.push(Holder {
                    offset: at,
                    words: &REFERENCE_WORDS,
                });
            }
            Type::Struct {
                fields, internals, ..
            } => {
                for (&field, &offset) in fields.iter().zip(&internals.offsets) {
                    self.add_holders(field, at + offset, holders);
                }
            }
            Type::Array { internals, .. } => {
                let base = self.types.real(internals.base);
                let (size, _) = layout(self.types, base, self.target);
                for index in 0..internals.count {
                    self.add_holders(base, at + index * size, holders);
                }
            }
            _ => {}
        }
    }

    /// The debug type of the language type `ty`, made once per program.
    fn debug_type(&mut self, ty: TypeId) -> DebugTypeId {
        self.debug_type_at_depth(ty, 0)
    }

    fn debug_type_at_depth(&mut self, ty: TypeId, depth: u32) -> DebugTypeId {
        let ty = self.types.real(ty);
        if let Some(id) = self.debug_types.of_type(ty) {
            return id;
        }
        let pointer_bits = self.target.pointer_bits;
        let basic = |name: &str, bits: u32, encoding: &'static str| DebugType::Basic {
            name: name.into(),
            bits,
            encoding,
        };
        let debug_type = match &self.types[ty] {
            Type::Bool => basic("bool", 8, "DW_ATE_boolean"),
            Type::Integer(integer) => {
                let encoding = if integer.signed {
                    "DW_ATE_signed"
                } else {
                    "DW_ATE_unsigned"
                };
                basic(&integer.to_string(), integer.bits, encoding)
            }
            Type::Float(Float::F32) => basic("f32", 32, "DW_ATE_float"),
            Type::Float(Float::F64) => basic("f64", 64, "DW_ATE_float"),
            Type::CString => {
                let char = self
                    .debug_types
                    .push(basic("char", 8, "DW_ATE_signed_char"));
                DebugType::Pointer {
                    name: "cstr".into(),
                    to: Some(char),
                }
            }
            Type::Reference { .. } | Type::RawPointer { .. } => {
                let to = match self.types[ty].pointee() {
                    // A struct with an error: this output is never linked.
                    Some(pointee) if matches!(self.types[pointee], Type::Named(_)) => None,
                    // shortcut: clang runs out of stack on debug types tens
                    // of thousands of nodes deep; past this depth a debugger
                    // sees an untyped pointer, or a struct with no fields.
                    Some(_) if depth == DEEPEST_DEBUG_TYPE => None,
                    // Pointers break a flat array, so they nest as deep as
                    // the source does.
                    Some(pointee) => Some(source::grow_stack(|| {
                        self.debug_type_at_depth(pointee, depth + 1)
                    })),
                    None => None, // `rawptr`
                };
                DebugType::Pointer {
                    name: "".into(),
                    to,
                }
            }
            Type::Slice { .. } | Type::Str => {
                // A `&str` views bytes.
                let element = self.types[ty]
                    .elements()
                    .map_or(TypeId::U8, |(element, _)| element);
                let base = self.debug_type_at_depth(element, depth);
                let name = model::program::type_text(self.modules, self.types, ty);
                let composite = self.debug_types.reserve(ty);
                let pointer = DebugType::Pointer {
                    name: "".into(),
                    to: Some(base),
                };
                let pointer = self.debug_types.push(pointer);
                let usize = basic("usize", pointer_bits, "DW_ATE_unsigned");
                let usize = self.debug_types.push(usize);
                let pointer_bytes = self.pointer_bytes;
                let members = [
                    DebugMember {
                        name: "ptr".into(),
                        offset: 0,
                        ty: pointer,
                    },
                    DebugMember {
                        name: "len".into(),
                        offset: pointer_bytes,
                        ty: usize,
                    },
                ];
                let composite_type = DebugType::Composite {
                    name: name.into(),
                    size: 2 * pointer_bytes,
                    members: members.into(),
                };
                self.debug_types.define(composite, composite_type);
                return composite;
            }
            // Its address and its methods' table, neither of a known type.
            Type::InterfaceReference { .. } | Type::Interface { .. } => {
                let name = model::program::type_text(self.modules, self.types, ty);
                let pointer = DebugType::Pointer {
                    name: "".into(),
                    to: None,
                };
                let pointer = self.debug_types.push(pointer);
                let pointer_bytes = self.pointer_bytes;
                let members = [
                    DebugMember {
                        name: "ptr".into(),
                        offset: 0,
                        ty: pointer,
                    },
                    DebugMember {
                        name: "table".into(),
                        offset: pointer_bytes,
                        ty: pointer,
                    },
                ];
                DebugType::Composite {
                    name: name.into(),
                    size: 2 * pointer_bytes,
                    members: members.into(),
                }
            }
            // One node with a range per level, as C's debug info has.
            Type::Array { internals, .. } => {
                let element = self.debug_type_at_depth(internals.base, depth);
                let lengths = self.types.array_lengths(ty).collect();
                let (size, _) = layout(self.types, ty, self.target);
                DebugType::Array {
                    element,
                    size,
                    lengths,
                }
            }
            Type::Struct {
                definition, fields, ..
            } => {
                let name = model::program::declared_text(self.modules, *definition).to_string();
                let names = model::program::field_names(self.modules, *definition);
                let fields: Vec<(String, TypeId)> =
                    (names.map(str::to_string).zip(fields.iter().copied())).collect();
                return self.debug_struct(ty, &name, &fields, depth);
            }
            Type::Range { element, step } => {
                let Type::Integer(integer) = self.types[*element] else {
                    unreachable!("a range is of integers")
                };
                let name = format!("{}[{integer}]", model::semantic_types::RANGE);
                let fields: Vec<(String, TypeId)> = range_fields(*element, *step)
                    .map(|(field, ty)| (field.to_string(), ty))
                    .into();
                return self.debug_struct(ty, &name, &fields, depth);
            }
            Type::Invalid | Type::Void | Type::Function { .. } | Type::Named(_) => {
                unreachable!("no value has this type")
            }
            Type::Alias { .. } => unreachable!("indexing looks through aliases"),
        };
        self.debug_types.add(ty, debug_type)
    }

    /// The debug type of the struct `name`, of type `ty`, with `fields`:
    /// its number comes before its members, as a `*T` field may reach it
    /// again.
    fn debug_struct(
        &mut self,
        ty: TypeId,
        name: &str,
        fields: &[(String, TypeId)],
        depth: u32,
    ) -> DebugTypeId {
        let composite = self.debug_types.reserve(ty);
        let field_types: Vec<TypeId> = fields.iter().map(|&(_, field)| field).collect();
        let (offsets, size, _) = struct_layout(self.types, &field_types, self.target);
        let mut members = Vec::with_capacity(fields.len());
        if depth < DEEPEST_DEBUG_TYPE {
            for ((field_name, field), offset) in fields.iter().zip(offsets) {
                // Structs hold structs as deep as the declarations nest.
                let base = source::grow_stack(|| self.debug_type_at_depth(*field, depth + 1));
                members.push(DebugMember {
                    name: field_name.as_str().into(),
                    offset,
                    ty: base,
                });
            }
        }
        let debug_type = DebugType::Composite {
            name: name.into(),
            size,
            members: members.into(),
        };
        self.debug_types.define(composite, debug_type);
        composite
    }

    /// Field `index` of the struct or range value of type `ty` at `place`:
    /// where it is, and its type.
    fn field(&self, place: Place, ty: TypeId, index: usize) -> (Place, TypeId) {
        let (offset, field) = field_placed(self.types, ty, index, self.target);
        (place.then(Step::Offset(offset)), self.types.real(field))
    }

    /// The step from a slice's words to its elements: through the pointer
    /// at its start.
    fn through_slice(place: Place) -> Place {
        place.then(Step::Through)
    }

    /// The second word of a two-word value at `place`: a slice's length,
    /// an interface value's table (the first word is the address).
    fn second_word(&self, place: Place) -> Place {
        place.then(Step::Offset(self.pointer_bytes))
    }
}
