//! The LLVM text of names and lists.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;

use model::DefId;
use model::FunctionCopy;
use model::constant::Constant;
use model::constant::Value;
use model::semantic_types::ConstArgument;
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

/// `symbol` as an LLVM global name.
pub(crate) fn global(symbol: &str) -> String {
    format!("@{}", quoted(symbol.as_bytes()))
}

/// `items` with a comma between each two, written where it is printed.
pub(crate) fn comma_separated<T: Display>(items: impl Iterator<Item = T> + Clone) -> impl Display {
    fmt::from_fn(move |out| {
        for (index, item) in items.clone().enumerate() {
            if index > 0 {
                out.write_str(", ")?;
            }
            item.fmt(out)?;
        }
        Ok(())
    })
}
