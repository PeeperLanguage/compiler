//! Writing `print` and `println`: each value's text through the runtime,
//! and the printer function of each array type.

use std::fmt::Write;

use arena::Id;
use model::constant::Value;
use model::mir;
use model::mir::Operand;
use model::semantic_types::Float;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;

use crate::function::FLUSH;
use crate::function::Function;
use crate::function::PRINT_FLOAT;
use crate::function::PRINT_INT;
use crate::function::PRINT_POINTER;
use crate::function::PRINT_TEXT;
use crate::function::debug_info::DebugLocation;
use crate::function::global;
use crate::function::value_name;
use crate::layout::in_memory;
use crate::text::type_text;

impl Function<'_, '_, '_> {
    /// Writes the runtime calls that print `values` for the `print` `id`,
    /// a space between each, then a newline when `newline`, then a flush.
    /// Text known at compile time (spaces, a struct's names, a constant
    /// string up to its NUL) is gathered into one string between values.
    pub(super) fn write_print(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        newline: bool,
        values: &[Operand],
        location: &DebugLocation,
    ) {
        let mut text = Vec::new();
        for (index, &value) in values.iter().enumerate() {
            if index > 0 {
                text.push(b' ');
            }
            if let Operand::Constant(constant) = value
                && let Value::Bytes(bytes) = &self.body.constants[constant.index()].value
            {
                text.extend(bytes.split(|&byte| byte == 0).next().unwrap_or_default());
                continue;
            }
            let path = format!("{}.{index}", value_name(id));
            let ty = self.body.operand_type(value);
            let value = self.operand(value).to_string();
            self.write_printed(out, &mut text, &value, ty, &path, location);
        }
        if newline {
            text.push(b'\n');
        }
        self.write_print_text(out, &mut text, location);
        self.write_runtime_call(out, FLUSH, "", location);
    }

    /// Writes what prints `value`, of the type `ty`, after `text`; `path`
    /// starts the names of the values computed on the way.
    pub(super) fn write_printed(
        &mut self,
        out: &mut String,
        text: &mut Vec<u8>,
        value: &str,
        ty: TypeId,
        path: &str,
        location: &DebugLocation,
    ) {
        let types = self.types;
        match types[ty] {
            Type::Integer(integer) => {
                let limbs = write_limbs(out, value, integer, path, location);
                let (signed, count) = (i32::from(integer.signed), integer.bits.div_ceil(64));
                let arguments = format!("i32 {signed}, i32 {count}, {limbs}");
                self.write_print_text(out, text, location);
                self.write_runtime_call(out, PRINT_INT, &arguments, location);
            }
            Type::Float(float) => {
                let (value, bits) = match float {
                    Float::F32 => {
                        _ = writeln!(out, "  {path}.x = fpext float {value} to double{location}");
                        (format!("{path}.x"), 32)
                    }
                    Float::F64 => (value.to_string(), 64),
                };
                self.write_print_text(out, text, location);
                let arguments = format!("double {value}, i32 {bits}");
                self.write_runtime_call(out, PRINT_FLOAT, &arguments, location);
            }
            Type::Bool => {
                let yes = self.module.add_string(b"true");
                let no = self.module.add_string(b"false");
                _ = writeln!(
                    out,
                    "  {path}.b = select i1 {value}, ptr {yes}, ptr {no}{location}"
                );
                self.write_print_text(out, text, location);
                let arguments = format!("ptr {path}.b");
                self.write_runtime_call(out, PRINT_TEXT, &arguments, location);
            }
            Type::CString => {
                self.write_print_text(out, text, location);
                self.write_runtime_call(out, PRINT_TEXT, &format!("ptr {value}"), location);
            }
            Type::RawPointer { .. } => {
                self.write_print_text(out, text, location);
                self.write_runtime_call(out, PRINT_POINTER, &format!("ptr {value}"), location);
            }
            // A value kept in memory is its address, which the reference is.
            Type::Reference { target, .. } if in_memory(types, target) => {
                self.write_printed(out, text, value, target, path, location);
            }
            Type::Reference { target, .. } => {
                let target_text = type_text(types, target);
                _ = writeln!(
                    out,
                    "  {path}.d = load {target_text}, ptr {value}{location}"
                );
                let referred = format!("{path}.d");
                self.write_printed(out, text, &referred, target, &referred, location);
            }
            // shortcut: structs and arrays print built in until a trait or
            // interface does (`check` `printable`).
            Type::Struct {
                definition,
                ref fields,
            } => {
                let program = self.module.program;
                let (name, file) = project::declared_name(&program.modules, definition);
                text.extend_from_slice(name.text(file).as_bytes());
                let names = project::field_names(&program.modules, definition);
                let struct_text = type_text(types, ty);
                for (index, (field_name, &field)) in names.zip(fields).enumerate() {
                    text.extend_from_slice(if index == 0 { b" { " } else { b", " });
                    text.extend_from_slice(field_name.as_bytes());
                    text.extend_from_slice(b": ");
                    // Its address, then its value unless that is kept in memory.
                    let address = format!("{path}.p{index}");
                    _ = writeln!(
                        out,
                        "  {address} = getelementptr inbounds {struct_text}, ptr {value}, i32 0, \
                         i32 {index}{location}"
                    );
                    let field_value = format!("{path}.f{index}");
                    let field_value = if in_memory(types, field) {
                        address
                    } else {
                        let field_text = type_text(types, field);
                        _ = writeln!(
                            out,
                            "  {field_value} = load {field_text}, ptr {address}{location}"
                        );
                        field_value
                    };
                    self.write_printed(out, text, &field_value, field, &field_value, location);
                }
                text.extend_from_slice(if fields.is_empty() { b" {}" } else { b" }" });
            }
            Type::Array { length: 0, .. } => text.extend_from_slice(b"[]"),
            Type::Array { .. } => {
                let printer = self.array_printer(ty);
                text.push(b'[');
                self.write_print_text(out, text, location);
                _ = writeln!(out, "  call void {printer}(ptr {value}){location}");
                text.push(b']');
            }
            _ => unreachable!(
                "check prints only numbers, `bool`, `cstr`, pointers, structs and arrays"
            ),
        }
    }

    /// The private function that prints the elements of an array of the
    /// type `ty`, given its address, with `, ` between each, as
    /// `write_printed` prints any value: a loop, so the code does not grow
    /// with the length. Written into the module the first time its type is
    /// printed, then reused.
    // shortcut: built in until a trait or interface prints arrays (`check`
    // `printable`).
    pub(super) fn array_printer(&mut self, ty: TypeId) -> String {
        let printers = &mut self.module.written.array_printers;
        let count = printers.len();
        let number = *printers.entry(ty).or_insert(count);
        let name = format!("@print.array.{number}");
        if number < count {
            return name;
        }
        let types = self.types;
        let Type::Array { element, length } = types[ty] else {
            unreachable!("only an array type has an array printer")
        };
        let (first, between) = (self.module.add_string(b""), self.module.add_string(b", "));
        let none = DebugLocation(None);
        let mut body = String::new();
        // The pass ends in `step`, so the `phi` is right whatever blocks
        // printing an element adds.
        _ = writeln!(
            body,
            "define private void {name}(ptr %array) {{\nentry:\n  br label %loop\nloop:\n  \
             %index = phi i64 [ 0, %entry ], [ %next, %step ]\n  \
             %first = icmp eq i64 %index, 0\n  \
             %separator = select i1 %first, ptr {first}, ptr {between}"
        );
        self.write_runtime_call(&mut body, PRINT_TEXT, "ptr %separator", &none);
        _ = writeln!(
            body,
            "  %address = getelementptr inbounds {}, ptr %array, i64 0, i64 %index",
            type_text(types, ty)
        );
        let value = if in_memory(types, element) {
            "%address"
        } else {
            let element_text = type_text(types, element);
            _ = writeln!(body, "  %element = load {element_text}, ptr %address");
            "%element"
        };
        let mut text = Vec::new();
        self.write_printed(&mut body, &mut text, value, element, "%element", &none);
        self.write_print_text(&mut body, &mut text, &none);
        _ = writeln!(
            body,
            "  br label %step\nstep:\n  %next = add i64 %index, 1\n  \
             %more = icmp ult i64 %next, {length}\n  \
             br i1 %more, label %loop, label %done\ndone:\n  ret void\n}}\n"
        );
        self.module.private_functions.push_str(&body);
        name
    }

    /// Writes the call that prints `text`, if any, and empties it.
    pub(super) fn write_print_text(
        &mut self,
        out: &mut String,
        text: &mut Vec<u8>,
        location: &DebugLocation,
    ) {
        if text.is_empty() {
            return;
        }
        let string = self.module.add_string(text);
        text.clear();
        self.write_runtime_call(out, PRINT_TEXT, &format!("ptr {string}"), location);
    }

    /// Writes the call of the runtime function `(symbol, parameters)` with
    /// `arguments`, after noting the declaration the module needs for it.
    pub(super) fn write_runtime_call(
        &mut self,
        out: &mut String,
        (symbol, parameters): (&str, &str),
        arguments: &str,
        location: &DebugLocation,
    ) {
        let callee = global(symbol);
        self.module.declare(symbol, || {
            format!("declare void {callee}({parameters})\n\n")
        });
        _ = writeln!(
            out,
            "  call void ({parameters}) {callee}({arguments}){location}"
        );
    }
}

/// Writes `value`, an integer of type `integer`, as 64-bit limbs, least
/// significant first, of it extended to their width (`write_extended`), and
/// gives them as call arguments: `i64 %p.l0, i64 %p.l1`.
pub(super) fn write_limbs(
    out: &mut String,
    value: &str,
    integer: Integer,
    path: &str,
    location: &DebugLocation,
) -> String {
    let count = integer.bits.div_ceil(64);
    let width = count * 64;
    let wide = write_extended(out, value, integer, width, path, location);
    if count == 1 {
        return format!("i64 {wide}");
    }
    let mut limbs = String::new();
    for limb in 0..count {
        let shift = limb * 64;
        _ = writeln!(
            out,
            "  {path}.s{limb} = lshr i{width} {wide}, {shift}{location}\n  \
             {path}.l{limb} = trunc i{width} {path}.s{limb} to i64{location}"
        );
        let separator = if limb > 0 { ", " } else { "" };
        _ = write!(limbs, "{separator}i64 {path}.l{limb}");
    }
    limbs
}

/// Writes `value`, an integer of type `integer`, extended by its sign to
/// `width` bits as `<path>.x`, and gives that name; `value` itself when it
/// has `width` bits.
pub(super) fn write_extended(
    out: &mut String,
    value: &str,
    integer: Integer,
    width: u32,
    path: &str,
    location: &DebugLocation,
) -> String {
    let Integer { signed, bits } = integer;
    if bits == width {
        return value.to_string();
    }
    let extension = if signed { "sext" } else { "zext" };
    _ = writeln!(
        out,
        "  {path}.x = {extension} i{bits} {value} to i{width}{location}"
    );
    format!("{path}.x")
}
