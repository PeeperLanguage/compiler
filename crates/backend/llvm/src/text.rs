//! The LLVM text of names, types and constants.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;

use model::DefId;
use model::FunctionCopy;
use model::constant::Constant;
use model::constant::Value;
use model::layout::in_memory;
use model::layout::layout;
use model::machine::Machine;
use model::semantic_types::ConstArgument;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;

/// The name the linker knows the function `definition` by: its `#[extern]`
/// name, else its module's path and its name (`myapp:util.Parse`).
pub(crate) fn symbol(program: &model::program::Program, definition: DefId) -> String {
    model::program::c_symbol(&program.modules, definition).unwrap_or_else(|| {
        let path = &program.modules[definition.module.index()].path;
        let name = model::program::declared_path(&program.modules, definition);
        format!("{path}.{name}")
    })
}

/// The name the linker knows the copy `copy` by: its function's `symbol`,
/// with its `const` arguments, each type by its module's path and name
/// (`myapp:main.New<myapp:main.Node>`), so two modules' types of one name
/// make two names, and each float value by its bits, as two NaNs print
/// alike.
pub(crate) fn copy_symbol(
    program: &model::program::Program,
    types: &Types,
    copy: FunctionCopy,
) -> String {
    let modules = &program.modules;
    let qualified = |definition: DefId| {
        let path = &modules[definition.module.index()].path;
        format!(
            "{path}.{}",
            model::program::declared_text(modules, definition)
        )
    };
    let name = symbol(program, copy.definition);
    let given = types.arguments(copy.arguments);
    if given.is_empty() {
        return name;
    }
    let text = |argument: &ConstArgument| match argument {
        ConstArgument::Value(Constant {
            value: Value::Float(bits),
            ..
        }) => format!("{bits:#x}"),
        _ => types.argument_text(argument, qualified),
    };
    let texts: Vec<String> = given.iter().map(text).collect();
    format!("{name}<{}>", texts.join(", "))
}
/// The LLVM type of a value of type `ty`, written where it is printed.
pub(crate) fn type_text(types: &Types, ty: TypeId) -> impl Display {
    fmt::from_fn(move |out| match types[ty] {
        Type::Void => out.write_str("void"),
        Type::Bool => out.write_str("i1"),
        Type::Integer(integer) => write!(out, "i{}", integer.bits),
        Type::Float(Float::F32) => out.write_str("float"),
        Type::Float(Float::F64) => out.write_str("double"),
        Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => out.write_str("ptr"),
        Type::Slice { .. } | Type::Str => out.write_str("%slice"),
        // The address it views, then its methods' table.
        Type::InterfaceReference { .. } => out.write_str("{ ptr, ptr }"),
        Type::Struct { .. } | Type::Range { .. } | Type::Array { .. } => {
            unreachable!("a value kept in memory is bytes to LLVM (`memory_text`)")
        }
        Type::Invalid | Type::Function { .. } | Type::Interface { .. } => {
            unreachable!("MIR holds no value of this type")
        }
        Type::Alias { .. } => unreachable!("indexing looks through aliases"),
        Type::Named(_) => unreachable!("no value has a named type"),
    })
}
/// The LLVM type of a value of type `ty` in memory: a value kept there is
/// bytes, laid out here (`layout`), so LLVM needs nothing of its shape, and a
/// walk of a type never reaches LLVM. As a `getelementptr`'s type, it steps by
/// the value's size.
pub(crate) fn memory_text(types: &Types, ty: TypeId, target: &Machine) -> impl Display {
    fmt::from_fn(move |out| {
        if in_memory(types, ty) {
            write!(out, "[{} x i8]", layout(types, ty, target).0)
        } else {
            Display::fmt(&type_text(types, ty), out)
        }
    })
}

/// What an `alloca` of a value of type `ty` takes, with the alignment bytes
/// have none of.
pub(crate) fn alloca_text(types: &Types, ty: TypeId, target: &Machine) -> impl Display {
    fmt::from_fn(move |out| {
        Display::fmt(&memory_text(types, ty, target), out)?;
        if in_memory(types, ty) {
            write!(out, ", align {}", layout(types, ty, target).1)?;
        }
        Ok(())
    })
}

/// `constant` as an LLVM constant of its type, written from its bits
/// (docs/ir-design.md, "Integer values must match LLVM"): an integer up to
/// 64 bits in decimal, a wider one in hexadecimal, a float as its `f64` bits.
pub(crate) fn constant_text(types: &Types, constant: &Constant) -> impl Display {
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
