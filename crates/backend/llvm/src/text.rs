//! The LLVM text of names, types and constants.

use std::fmt;
use std::fmt::Write;

use model::DefId;
use model::constant::Constant;
use model::constant::Value;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::semantic_types::range_fields;

/// The name the linker knows the function `definition` by: its `#[extern]`
/// name, else its module's path and its name (`myapp:util.Parse`).
pub(crate) fn symbol(program: &project::Program, definition: DefId) -> String {
    project::extern_symbol(&program.modules, definition)
        .expect("the checker reported a malformed `extern`")
        .unwrap_or_else(|| {
            let module = &program.modules[definition.module.index()];
            let declaration = &module.ast.decls[module.ast.declaration(definition.index)];
            format!(
                "{}.{}",
                module.path,
                declaration.name().text(&module.ast.file)
            )
        })
}
/// The LLVM type of a value of type `ty`, written where it is printed.
pub(crate) fn type_text(types: &Types, ty: TypeId) -> impl fmt::Display {
    fmt::from_fn(move |out| match types[ty] {
        Type::Void => out.write_str("void"),
        Type::Bool => out.write_str("i1"),
        Type::Integer(integer) => write!(out, "i{}", integer.bits),
        Type::Float(Float::F32) => out.write_str("float"),
        Type::Float(Float::F64) => out.write_str("double"),
        Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => out.write_str("ptr"),
        Type::Slice { .. } => out.write_str("%slice"),
        Type::Struct { ref fields, .. } => struct_type_text(types, fields, out),
        Type::Range { element, step } => struct_type_text(types, &range_fields(element, step), out),
        Type::Array { element, length } => {
            write!(out, "[{length} x {}]", type_text(types, element))
        }
        Type::Invalid | Type::Function { .. } => unreachable!("MIR holds no value of this type"),
        Type::Alias { .. } => unreachable!("indexing looks through aliases"),
        Type::Named(_) => unreachable!("no value has a named type"),
    })
}
/// A struct's LLVM type, `{ i32, i64 }`, of its `fields`' types. Out of
/// line, so `type_text`, which writes every operand's type, stays small
/// (measured: 11 M fewer instructions per compile of the benchmark); its
/// array arm recursing in line measured flat.
#[cold]
#[inline(never)]
pub(crate) fn struct_type_text(
    types: &Types,
    fields: &[TypeId],
    out: &mut fmt::Formatter,
) -> fmt::Result {
    out.write_str("{")?;
    for (index, &field) in fields.iter().enumerate() {
        let separator = if index > 0 { ", " } else { " " };
        write!(out, "{separator}{}", type_text(types, field))?;
    }
    out.write_str(if fields.is_empty() { "}" } else { " }" })
}
/// The attribute that extends a value of type `ty` to C's `int` when it
/// passes to or from C code, as the C ABI requires of a narrower type; `None`
/// for any other type.
pub(crate) fn c_extension(types: &Types, ty: TypeId) -> Option<&'static str> {
    match types[ty] {
        Type::Bool => Some("zeroext"),
        Type::Integer(integer) if integer.bits < 32 && integer.signed => Some("signext"),
        Type::Integer(integer) if integer.bits < 32 => Some("zeroext"),
        _ => None,
    }
}
/// `constant` as an LLVM constant of its type, written from its bits
/// (docs/ir-design.md, "Integer values must match LLVM"): an integer up to
/// 64 bits in decimal, a wider one in hexadecimal, a float as its `f64` bits.
pub(crate) fn constant_text(types: &Types, constant: &Constant) -> impl fmt::Display {
    fmt::from_fn(move |out| match (&constant.value, &types[constant.ty]) {
        (Value::Bool(value), _) => write!(out, "{value}"),
        (Value::Integer(limbs), Type::Integer(integer)) if integer.bits <= 64 => {
            let low = limbs.as_slice()[0];
            if integer.signed {
                let unused = 64 - integer.bits;
                write!(out, "{}", ((low << unused) as i64) >> unused)
            } else {
                write!(out, "{low}")
            }
        }
        (Value::Integer(limbs), _) => {
            out.write_str("u0x")?;
            for limb in limbs.as_slice().iter().rev() {
                write!(out, "{limb:016X}")?;
            }
            Ok(())
        }
        (Value::Float(bits), _) => write!(out, "0x{bits:016X}"),
        (Value::Null, _) => out.write_str("null"),
        (Value::Bytes(_), _) => {
            unreachable!("a string constant is a global (`Module::add_string`)")
        }
    })
}
/// `bytes` in double quotes, as LLVM reads a string: a quote, a backslash
/// and each byte outside printable ASCII as `\XX`.
pub(crate) fn quoted(bytes: &[u8]) -> String {
    let mut text = String::with_capacity(bytes.len() + 2);
    text.push('"');
    for &byte in bytes {
        if byte.is_ascii_graphic() && byte != b'"' && byte != b'\\' || byte == b' ' {
            text.push(byte as char);
        } else {
            _ = write!(text, "\\{byte:02X}");
        }
    }
    text.push('"');
    text
}
