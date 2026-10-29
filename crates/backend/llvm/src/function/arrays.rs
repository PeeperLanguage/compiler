//! Writing array literals from their parts: elements, arrays, slices and
//! ranges spread into them, with each part's count and a filler per range.

use std::fmt::Write;
use std::iter::Peekable;

use arena::Id;
use model::layout::layout;
use model::mir;
use model::mir::Operand;
use model::semantic_types::RANGE_FIELD_NAMES;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::range_field;
use model::semantic_types::range_fields;

use crate::function::Function;
use crate::function::debug_info::DebugLocation;
use crate::function::traps;
use crate::function::value_name;
use crate::function::written_range_field;
use crate::text::type_text;

impl Function<'_, '_, '_> {
    /// Writes the array value `id` from `parts`
    /// (`InstructionKind::ArrayParts`): counts them and checks the count
    /// (`write_part_counts`), then writes each element at its place, copies
    /// each array and each slice's elements to its place, and fills each
    /// range through its element type's filler. Out of line, so
    /// `write_instruction` stays small enough to inline into `write_block`
    /// (measured: 10 M fewer instructions per compile).
    #[cold]
    #[inline(never)]
    pub(super) fn write_array_parts(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        parts: &[Operand],
        rest: &mut Peekable<impl Iterator<Item = Id<mir::Instruction>>>,
        location: &DebugLocation,
    ) {
        let counts = self.write_part_counts(out, id, parts, location);
        let (types, name) = (self.types, value_name(id));
        let ty = self.body.instructions[id].ty;
        let Type::Array { element, .. } = types[ty] else {
            unreachable!("array parts make an array")
        };
        let (pointer, number) = (self.module.target.pointer_bits, type_text(types, element));
        let memory = self.value_address(out, id, ty, rest);
        let array = type_text(types, ty);
        let mut offset = "0".to_string();
        for (index, (&part, count)) in parts.iter().zip(&counts).enumerate() {
            let address = format!("{name}.{index}");
            _ = writeln!(
                out,
                "  {address} = getelementptr inbounds {array}, ptr {memory}, i32 0, \
                 i{pointer} {offset}{location}"
            );
            if let Type::Range { .. } = types[self.body.operand_type(part)] {
                let range = format!("{name}.r{index}");
                let start = self.read_range_field(out, &range, part, range_field::START, location);
                let delta = self.read_range_field(out, &range, part, range_field::DELTA, location);
                let filler = self.range_filler(element);
                _ = writeln!(
                    out,
                    "  call void {filler}(ptr {address}, {number} {start}, {number} {delta}, \
                     i{pointer} {count}){location}"
                );
            } else if let Type::Slice { .. } = types[self.body.operand_type(part)] {
                // The count was checked to fit, so the bytes do too.
                let from = format!("{name}.s{index}");
                let (size, _) = layout(types, element, self.module.target);
                _ = writeln!(
                    out,
                    "  {from} = extractvalue %slice {}, 0{location}\n  \
                     {from}.bytes = mul i{pointer} {count}, {size}{location}",
                    self.operand(part)
                );
                let mut bytes = format!("{from}.bytes");
                if pointer < 64 {
                    _ = writeln!(
                        out,
                        "  {from}.size = zext i{pointer} {bytes} to i64{location}"
                    );
                    bytes = format!("{from}.size");
                }
                self.write_memcpy(out, &address, &from, &bytes, location);
            } else {
                // An element, or an array of them copied whole.
                self.write_store(out, &address, part, location);
            }
            if index + 1 < parts.len() {
                let next = format!("{name}.o{index}");
                _ = writeln!(out, "  {next} = add i{pointer} {offset}, {count}{location}");
                offset = next;
            }
        }
    }

    /// How many elements each of `parts`, of the array value `id`, gives,
    /// as pointer-wide LLVM operands: one for an element, an array's
    /// length, a slice's length, and each range's count
    /// (`write_range_count`), counted in an integer wider than its element
    /// and at least as wide as a pointer, so it never overflows. With a
    /// part counted when the program runs (`traps`), also writes the check
    /// that the parts give exactly the array's length, before any element
    /// is written.
    pub(super) fn write_part_counts(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        parts: &[Operand],
        location: &DebugLocation,
    ) -> Vec<String> {
        let (types, name) = (self.types, value_name(id));
        let Type::Array { element, length } = types[self.body.instructions[id].ty] else {
            unreachable!("array parts make an array")
        };
        let pointer = self.module.target.pointer_bits;
        let wide = match types[element] {
            Type::Integer(integer) => (integer.bits + 1).max(pointer),
            _ => pointer, // no range to count
        };
        let fixed = |part: Operand| {
            let ty = self.body.operand_type(part);
            match types[ty] {
                _ if types.same(ty, element) => Some(1),
                Type::Array { length, .. } => Some(length),
                Type::Range { .. } | Type::Slice { .. } => None,
                _ => unreachable!("the checker spreads only an array, a slice or a range"),
            }
        };
        let fixed_total: u64 = parts.iter().filter_map(|&part| fixed(part)).sum();
        let mut remaining = (length - fixed_total).to_string();
        let mut failed = "false".to_string();
        let checked = traps(self.body, types, id) > 0;
        let mut counts = Vec::with_capacity(parts.len());
        for (index, &part) in parts.iter().enumerate() {
            if let Some(count) = fixed(part) {
                counts.push(count.to_string());
                continue;
            }
            let part_name = format!("{name}.r{index}");
            if let Type::Slice { .. } = types[self.body.operand_type(part)] {
                let length = if wide > pointer { "len" } else { "count" };
                _ = writeln!(
                    out,
                    "  {part_name}.{length} = extractvalue %slice {}, 1{location}",
                    self.operand(part)
                );
                if wide > pointer {
                    _ = writeln!(
                        out,
                        "  {part_name}.count = zext i{pointer} {part_name}.len to i{wide}{location}"
                    );
                }
            } else {
                self.write_range_count(out, &part_name, part, element, wide, location);
            }
            if checked {
                _ = writeln!(
                    out,
                    "  {part_name}.over = icmp ugt i{wide} {part_name}.count, {remaining}{location}\n  \
                     {part_name}.failed = or i1 {failed}, {part_name}.over{location}\n  \
                     {part_name}.left.after = sub i{wide} {remaining}, {part_name}.count{location}"
                );
                (remaining, failed) = (
                    format!("{part_name}.left.after"),
                    format!("{part_name}.failed"),
                );
            }
            let count = match types[self.body.operand_type(part)] {
                _ if wide == pointer => format!("{part_name}.count"),
                Type::Slice { .. } => format!("{part_name}.len"),
                _ => {
                    _ = writeln!(
                        out,
                        "  {part_name}.n = trunc i{wide} {part_name}.count to i{pointer}{location}"
                    );
                    format!("{part_name}.n")
                }
            };
            counts.push(count);
        }
        if checked {
            _ = writeln!(
                out,
                "  {name}.short = icmp ne i{wide} {remaining}, 0{location}\n  \
                 {name}.failed = or i1 {failed}, {name}.short{location}"
            );
            let message = format!("array literal needs exactly {length} values");
            self.check(out, &format!("{name}.failed"), &message, id);
        }
        counts
    }

    /// Writes the lines that count the values of the range value `range`,
    /// named `name`, of `element`s, as `Stmt::ForRange` counts them, into
    /// `{name}.count`, an `i{wide}`: its `left` over its `step`, one more
    /// when it includes its end or when that leaves a rest
    /// (`write_part_counts`).
    pub(super) fn write_range_count(
        &self,
        out: &mut String,
        name: &str,
        range: Operand,
        element: TypeId,
        wide: u32,
        location: &DebugLocation,
    ) {
        let left = self.read_range_field(out, name, range, range_field::LEFT, location);
        let step = self.read_range_field(out, name, range, range_field::STEP, location);
        let inclusive = self.read_range_field(out, name, range, range_field::INCLUSIVE, location);
        let number = type_text(self.types, element);
        _ = writeln!(
            out,
            "  {name}.whole = udiv {number} {left}, {step}{location}\n  \
             {name}.wide = zext {number} {name}.whole to i{wide}{location}\n  \
             {name}.rest = urem {number} {left}, {step}{location}\n  \
             {name}.more = icmp ne {number} {name}.rest, 0{location}\n  \
             {name}.last = or i1 {inclusive}, {name}.more{location}\n  \
             {name}.extra = zext i1 {name}.last to i{wide}{location}\n  \
             {name}.count = add i{wide} {name}.wide, {name}.extra{location}"
        );
    }

    /// Writes the lines that read field `field` of the range value `range`,
    /// named `name` (`range_fields`), and gives the value read; a range
    /// written where it is spread gives the field as written, so its
    /// constants stay constants (`written_range_field`).
    pub(super) fn read_range_field(
        &self,
        out: &mut String,
        name: &str,
        range: Operand,
        field: usize,
        location: &DebugLocation,
    ) -> String {
        if let Some(written) = written_range_field(self.body, range, field) {
            return self.operand(written).to_string();
        }
        let types = self.types;
        let ty = self.body.operand_type(range);
        let Type::Range { element, step } = types[ty] else {
            unreachable!("only a range has range fields")
        };
        let read = format!("{name}.{}", RANGE_FIELD_NAMES[field]);
        _ = writeln!(
            out,
            "  {read}.at = getelementptr inbounds {}, ptr {}, i32 0, i32 {field}{location}\n  \
             {read} = load {}, ptr {read}.at{location}",
            type_text(types, ty),
            self.operand(range),
            type_text(types, range_fields(element, step)[field])
        );
        read
    }

    /// The private function that writes `count` integers of the type
    /// `element` from the address `dest` on: `start`, then each `delta` more
    /// than the one before, wrapping. Written into the module the first
    /// time its type fills a range, then reused.
    pub(super) fn range_filler(&mut self, element: TypeId) -> String {
        let fillers = &mut self.module.written.range_fillers;
        let count = fillers.len();
        let number = *fillers.entry(element).or_insert(count);
        let name = format!("@fill.range.{number}");
        if number < count {
            return name;
        }
        let (ty, pointer) = (
            type_text(self.types, element),
            self.module.target.pointer_bits,
        );
        _ = writeln!(
            self.module.private_functions,
            "define private void {name}(ptr %dest, {ty} %start, {ty} %delta, i{pointer} %count) {{\n\
             entry:\n  \
             %empty = icmp eq i{pointer} %count, 0\n  \
             br i1 %empty, label %done, label %loop\n\
             loop:\n  \
             %index = phi i{pointer} [ 0, %entry ], [ %next, %loop ]\n  \
             %value = phi {ty} [ %start, %entry ], [ %after, %loop ]\n  \
             %address = getelementptr inbounds {ty}, ptr %dest, i{pointer} %index\n  \
             store {ty} %value, ptr %address\n  \
             %after = add {ty} %value, %delta\n  \
             %next = add i{pointer} %index, 1\n  \
             %more = icmp ult i{pointer} %next, %count\n  \
             br i1 %more, label %loop, label %done\n\
             done:\n  \
             ret void\n}}\n"
        );
        name
    }
}
