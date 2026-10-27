//! Writing what reads and writes memory: a place's address, stores, and
//! copies of values kept in memory.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;
use std::iter::Peekable;

use arena::Id;
use model::mir;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;

use crate::function::Function;
use crate::function::MEMCPY;
use crate::function::debug_info::DebugLocation;
use crate::function::spreads_slice;
use crate::function::traps;
use crate::function::value_name;
use crate::layout::in_memory;
use crate::layout::layout;
use crate::text::type_text;

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
            self.write_place_address(out, &address, place, location, id);
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
            // Computed before the value, so it must not load a reference or
            // check an index.
            && !place.is_behind_reference()
            && traps(body, self.types, store) == 0
            && !spreads_slice(body, self.types, id)
        {
            rest.next();
            let span = body.instructions.span(store);
            let location = self.location(span, body.instructions[store].debug_scope);
            return self.write_address(out, place, &location, store).to_string();
        }
        let (name, text) = (value_name(id), type_text(self.types, ty));
        _ = writeln!(self.memory, "  {name} = alloca {text}");
        name.to_string()
    }

    /// Writes the lines computing `address`, the address of `place`, for the
    /// instruction `id`: one `getelementptr` for each run of fields and
    /// elements, after a check of each index there not known at compile
    /// time, or into a slice's elements, and before each dereference a load
    /// of the pointer there, `<address>.<n>` for the `n`th; for a slice's
    /// elements, its length too, `<address>.<n>.len`. Gives the place's
    /// type, a slice's for its elements.
    pub(super) fn write_place_address(
        &mut self,
        out: &mut String,
        address: &dyn Display,
        place: &mir::Place,
        location: &DebugLocation,
        id: Id<mir::Instruction>,
    ) -> TypeId {
        let variable = place.variable.index();
        let mut ty = self.body.variables[place.variable].ty;
        let (types, body) = (self.types, self.body);
        // Whether an element's index gets its own `i64`, named
        // `<address>.<run>.<step>`: one not known at compile time and not an
        // `i64` already.
        let renamed = |index: Operand| {
            !matches!(index, Operand::Constant(_))
                && !matches!(
                    types[body.operand_type(index)],
                    Type::Integer(Integer { bits: 64, .. })
                )
        };
        let mut runs = place
            .projections
            .split(|step| matches!(step, mir::Projection::Dereference))
            .enumerate()
            .peekable();
        while let Some((run, steps)) = runs.next() {
            let run_type = ty;
            // Each index not known at compile time, checked against its
            // array's length, and `renamed` when it is not an `i64`.
            for (position, &step) in steps.iter().enumerate() {
                ty = match (step, &self.types[ty]) {
                    (mir::Projection::Field(index), Type::Struct { fields, .. }) => {
                        fields[index as usize]
                    }
                    (
                        mir::Projection::Index(Operand::Constant(_)),
                        &Type::Array { element, .. },
                    ) => {
                        element // check found it inside the array
                    }
                    // Lowering checked its start and count.
                    (mir::Projection::Subslice(_), &Type::Array { element, .. }) => element,
                    // A slice's elements are past its dereference, so never
                    // in the first run, which would step into its header.
                    (mir::Projection::Subslice(_), &Type::Slice { element, .. }) => {
                        assert!(run > 0, "a slice is taken of a slice's elements");
                        element
                    }
                    (mir::Projection::Index(index), &Type::Array { element, .. })
                    | (mir::Projection::Index(index), &Type::Slice { element, .. }) => {
                        let Type::Integer(integer) = self.types[self.body.operand_type(index)]
                        else {
                            unreachable!("check takes only an integer index")
                        };
                        let (bits, value) = (integer.bits, self.operand(index).to_string());
                        let name = format!("{address}.{run}.{position}");
                        // Compared as an `i64`, or in its own type when wider; a
                        // negative index, extended, is above any length.
                        let (compared, compared_bits) = match bits {
                            _ if !renamed(index) => (format!("i64 {value}"), 64),
                            65.. => (format!("i{bits} {value}"), bits),
                            _ => {
                                let extension = if integer.signed { "sext" } else { "zext" };
                                _ = writeln!(
                                    out,
                                    "  {name} = {extension} i{bits} {value} to i64{location}"
                                );
                                (format!("i64 {name}"), 64)
                            }
                        };
                        // A slice's length was loaded at its dereference.
                        let (bound, message) = if let Type::Array { length, .. } = self.types[ty] {
                            let message = format!("index out of bounds: the length is {length}");
                            (length.to_string(), message)
                        } else {
                            let mut bound = format!("{address}.{}.len", run - 1);
                            let pointer_bits = self.module.target.pointer_bits;
                            if compared_bits > pointer_bits {
                                _ = writeln!(
                                    out,
                                    "  {name}.len = zext i{pointer_bits} {bound} to i{compared_bits}{location}"
                                );
                                bound = format!("{name}.len");
                            }
                            (bound, "index out of bounds of the slice".to_string())
                        };
                        _ = writeln!(out, "  {name}.out = icmp uge {compared}, {bound}{location}");
                        self.check(out, &format!("{name}.out"), &message, id);
                        if bits > 64 && renamed(index) {
                            _ = writeln!(out, "  {name} = trunc {compared} to i64{location}");
                        }
                        element
                    }
                    _ => unreachable!(
                        "a field is taken of a struct, an element of an array or slice"
                    ),
                };
            }
            // The address so far: the variable's, or the pointer loaded last.
            let base = fmt::from_fn(|out| match run {
                0 => write!(out, "%v{variable}"),
                _ => write!(out, "{address}.{}", run - 1),
            });
            let last = runs.peek().is_none();
            let run_address = fmt::from_fn(|out| {
                if last {
                    write!(out, "{address}")
                } else {
                    write!(out, "{address}.{run}.f")
                }
            });
            // A slice's elements, after its dereference, start at the loaded
            // address, so the first index steps from there.
            _ = match self.types[run_type] {
                Type::Slice { element, .. } if run > 0 => write!(
                    out,
                    "  {run_address} = getelementptr inbounds {}, ptr {base}",
                    type_text(self.types, element)
                ),
                _ => write!(
                    out,
                    "  {run_address} = getelementptr inbounds {}, ptr {base}, i32 0",
                    type_text(self.types, run_type)
                ),
            };
            for (position, &step) in steps.iter().enumerate() {
                _ = match step {
                    mir::Projection::Field(index) => write!(out, ", i32 {index}"),
                    mir::Projection::Index(index) if renamed(index) => {
                        write!(out, ", i64 {address}.{run}.{position}")
                    }
                    mir::Projection::Index(index) => write!(out, ", i64 {}", self.operand(index)),
                    mir::Projection::Subslice(subslice) => {
                        let start = self.body.subslices[subslice.index()].start;
                        let bits = self.module.target.pointer_bits;
                        write!(out, ", i{bits} {}", self.operand(start))
                    }
                    mir::Projection::Dereference => unreachable!("split at each dereference"),
                };
            }
            _ = writeln!(out, "{location}");
            if last {
                continue;
            }
            // A slice's elements keep the slice's type: the next run indexes
            // them with the length loaded here.
            if let Type::Slice { .. } = self.types[ty] {
                let slice = format!("{address}.{run}.s");
                _ = writeln!(
                    out,
                    "  {slice} = load %slice, ptr {run_address}{location}\n  \
                     {address}.{run} = extractvalue %slice {slice}, 0{location}\n  \
                     {address}.{run}.len = extractvalue %slice {slice}, 1{location}"
                );
                continue;
            }
            let Some(target) = self.types[ty].pointee() else {
                unreachable!("a dereference is taken of a typed pointer")
            };
            _ = writeln!(
                out,
                "  {address}.{run} = load ptr, ptr {run_address}{location}"
            );
            ty = target;
        }
        ty
    }
}
