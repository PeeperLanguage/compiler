//! Writing what reads and writes memory: a place's address, stores, and
//! copies of values kept in memory.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;
use std::iter::Peekable;

use arena::Id;
use model::layout::layout;
use model::mir;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::semantic_types::Type;
use model::semantic_types::TypeId;

use crate::function::Function;
use crate::function::MEMCPY;
use crate::function::debug_info::DebugLocation;
use crate::function::spreads_slice;
use crate::function::value_name;
use crate::text::alloca_text;
use crate::text::memory_text;
use model::layout::field_placed;
use model::layout::in_memory;

impl Function<'_, '_, '_> {
    /// The address of `place` for the instruction `id`, after writing the
    /// lines that compute it: a whole variable's own, else `<id's name>.f`.
    pub(super) fn write_address(
        &mut self,
        out: &mut String,
        place: &mir::Place,
        location: &DebugLocation,
        id: Id<mir::Instruction>,
    ) -> impl Display + use<> {
        let whole = place.whole_variable();
        let address = fmt::from_fn(move |out| match whole {
            Some(variable) => write!(out, "%v{}", variable.index()),
            None => write!(out, "{}.f", value_name(id)),
        });
        if whole.is_none() {
            self.write_place_address(out, &address, place, location);
        }
        address
    }

    /// Writes `value` to `address`: copied there when it is kept in memory
    /// (`in_memory`), else stored.
    pub(super) fn write_store(
        &mut self,
        out: &mut String,
        address: &dyn Display,
        value: Operand,
        location: &DebugLocation,
    ) {
        let ty = self.body.operand_type(value);
        if in_memory(self.types, ty) {
            let from = self.operand(value).to_string();
            self.write_copy(out, address, &from, ty, location);
        } else {
            _ = writeln!(
                out,
                "  store {}, ptr {address}{location}",
                self.typed(value)
            );
        }
    }

    /// Writes the copy of a value of type `ty`, kept in memory, from the
    /// address `from` to `to`.
    pub(super) fn write_copy(
        &mut self,
        out: &mut String,
        to: &dyn Display,
        from: &dyn Display,
        ty: TypeId,
        location: &DebugLocation,
    ) {
        // A number, not LLVM's `ptrtoint (getelementptr (T, ptr null, i32 1))`,
        // which clang's optimizer splits no value by (measured: twice the
        // `-release` build time of a program of structs).
        let (size, _) = layout(self.types, ty, self.module.target);
        self.write_memcpy(out, to, from, &size, location);
    }

    /// Writes the copy of `bytes`, an `i64`, from the address `from` to
    /// `to`, which do not overlap.
    pub(super) fn write_memcpy(
        &mut self,
        out: &mut String,
        to: &dyn Display,
        from: &dyn Display,
        bytes: &dyn Display,
        location: &DebugLocation,
    ) {
        self.module.declare(MEMCPY, || {
            format!("declare void @{MEMCPY}(ptr, ptr, i64, i1)\n\n")
        });
        _ = writeln!(
            out,
            "  call void @{MEMCPY}(ptr {to}, ptr {from}, i64 {bytes}, i1 false){location}"
        );
    }

    /// The address the value of the instruction `id`, of type `ty`, kept in
    /// memory, is built at, before the `rest` of its block. When its only use
    /// comes next at an address known before, it is built there, saving a
    /// copy: `%result` for the block's return (`write_block`), or the place
    /// of a store right after it, whose address lines are written and which
    /// is taken from `rest`. Else its own memory, an `alloca` noted for the
    /// entry block and named as the value.
    pub(super) fn value_address(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        ty: TypeId,
        rest: &mut Peekable<impl Iterator<Item = Id<mir::Instruction>>>,
    ) -> String {
        let body = self.body;
        if self.current.returned == Some(id) {
            return "%result".to_owned();
        }
        if let Some(&store) = rest.peek()
            && let InstructionKind::Store {
                ref place,
                value: Operand::Value(value),
            } = body.instructions[store].kind
            && value == id
            // Computed before the value, so it must not load a reference.
            && !place.is_behind_reference()
            && !spreads_slice(body, self.types, id)
        {
            rest.next();
            let span = body.instructions.span(store);
            let location = self.location(span, body.instructions[store].debug_scope);
            return self.write_address(out, place, &location, store).to_string();
        }
        let name = value_name(id);
        let text = alloca_text(self.types, ty, self.module.target);
        _ = writeln!(self.memory, "  {name} = alloca {text}");
        name.to_string()
    }

    /// Writes the lines computing `address`, the address of `place`: in each
    /// run of fields and elements, one `getelementptr` for each index, a
    /// `usize` lowering checked, and one for the bytes of the fields after
    /// them; and before each dereference a load of the pointer there,
    /// `<address>.<n>` for the `n`th. Gives the place's type, a slice's for
    /// its elements.
    pub(super) fn write_place_address(
        &mut self,
        out: &mut String,
        address: &dyn Display,
        place: &mir::Place,
        location: &DebugLocation,
    ) -> TypeId {
        let variable = place.variable.index();
        let mut ty = self.body.variables[place.variable].ty;
        let pointer_bits = self.module.target.pointer_bits;
        let mut runs = place
            .projections
            .split(|step| step.leaves_storage())
            .enumerate()
            .peekable();
        while let Some((run, steps)) = runs.next() {
            // The address so far: the variable's, or the pointer loaded last.
            let base = match run {
                0 => format!("%v{variable}"),
                _ => format!("{address}.{}", run - 1),
            };
            let last = runs.peek().is_none();
            // With no step before a load, it reads from where the run starts.
            let run_address = if last {
                address.to_string()
            } else if steps.is_empty() {
                base.clone()
            } else {
                format!("{address}.{run}.f")
            };
            // Each index steps from `at` by its element's size; the fields
            // add up to `offset`, written once, after them.
            let (mut at, mut offset) = (base, 0);
            for (position, &step) in steps.iter().enumerate() {
                let index = match (step, &self.types[ty]) {
                    (mir::Projection::Field(field_index), Type::Struct { .. }) => {
                        let target = self.module.target;
                        let (field_offset, field) =
                            field_placed(self.types, ty, field_index as usize, target);
                        offset += field_offset;
                        ty = field;
                        continue;
                    }
                    // A slice's elements are past its dereference, so never
                    // in the first run, which would step into its header.
                    (
                        mir::Projection::Index(_) | mir::Projection::Subslice(_),
                        Type::Slice { .. },
                    ) if run == 0 => {
                        unreachable!("an element is taken of a slice's elements")
                    }
                    (
                        mir::Projection::Index(index),
                        &(Type::Array { element, .. } | Type::Slice { element, .. }),
                    ) => {
                        ty = element;
                        index
                    }
                    // Lowering checked its start and count.
                    (
                        mir::Projection::Subslice(subslice),
                        &(Type::Array { element, .. } | Type::Slice { element, .. }),
                    ) => {
                        ty = element;
                        self.body.subslices[subslice.index()].start
                    }
                    _ => unreachable!(
                        "a field is taken of a struct, an element of an array or slice"
                    ),
                };
                let next = if position + 1 == steps.len() && offset == 0 {
                    run_address.clone()
                } else {
                    format!("{address}.{run}.e{position}")
                };
                _ = writeln!(
                    out,
                    "  {next} = getelementptr inbounds {}, ptr {at}, i{pointer_bits} {}{location}",
                    memory_text(self.types, ty, self.module.target),
                    self.operand(index)
                );
                at = next;
            }
            if at != run_address {
                _ = writeln!(
                    out,
                    "  {run_address} = getelementptr inbounds i8, ptr {at}, i64 {offset}{location}"
                );
            }
            if last {
                continue;
            }
            // A slice's elements' address is its first part, and they keep
            // the slice's type.
            _ = writeln!(
                out,
                "  {address}.{run} = load ptr, ptr {run_address}{location}"
            );
            if let Type::Slice { .. } = self.types[ty] {
                continue;
            }
            let Some(target) = self.types[ty].pointee() else {
                unreachable!("a dereference is taken of a typed pointer")
            };
            ty = target;
        }
        ty
    }
}
