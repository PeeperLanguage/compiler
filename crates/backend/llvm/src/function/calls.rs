//! Writing calls: of a Peeper function in Peeper's convention, of a C
//! function as C on the target passes its arguments and result.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;
use std::iter::Peekable;

use arena::Id;
use model::DefId;
use model::machine::CAbi;
use model::machine::Machine;
use model::mir;
use model::mir::Operand;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;

use crate::function::Function;
use crate::function::comma_separated;
use crate::function::debug_info::DebugLocation;
use crate::function::global;
use crate::function::print::write_extended;
use crate::function::print::write_limbs;
use crate::function::value_name;
use crate::layout::in_memory;
use crate::text::symbol;
use crate::text::type_text;

impl Function<'_, '_, '_> {
    /// Writes the line calling `function` with `arguments` for the
    /// instruction `id`, after noting the declaration the module needs for
    /// it; a result kept in memory is built where `value_address` says,
    /// before the `rest` of the block. A C function's call is
    /// `write_c_call`'s.
    pub(super) fn write_call(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        function: DefId,
        arguments: &[Operand],
        rest: &mut Peekable<impl Iterator<Item = Id<mir::Instruction>>>,
        location: &DebugLocation,
    ) {
        let (ty, types, body) = (self.body.instructions[id].ty, self.types, self.body);
        let symbol = symbol(self.module.program, function);
        if project::extern_symbol(&self.module.program.modules, function) != Ok(None) {
            return self.write_c_call(out, id, &symbol, arguments, location);
        }
        let callee = global(&symbol);
        // A result kept in memory is written where a first argument points
        // (`add_function`). An argument kept in memory is always a private
        // copy (a load's or move's, a struct's or array's value, a result,
        // or a field of one), which the callee may change.
        let returned_in_memory = in_memory(types, ty);
        let result = fmt::from_fn(|out| {
            if returned_in_memory {
                out.write_str("void")
            } else {
                type_text(types, ty).fmt(out)
            }
        });
        let parameter = |argument| {
            let ty = body.operand_type(argument);
            fmt::from_fn(move |out| {
                if in_memory(types, ty) {
                    out.write_str("ptr")
                } else {
                    type_text(types, ty).fmt(out)
                }
            })
        };
        let separator = if arguments.is_empty() { "" } else { ", " };
        self.module.declare(&symbol, || {
            let parameters = comma_separated(arguments.iter().copied().map(parameter));
            let first = if returned_in_memory {
                ["ptr", separator].concat()
            } else {
                String::new()
            };
            format!("declare {result} {callee}({first}{parameters})\n\n")
        });
        let result_address = returned_in_memory.then(|| self.value_address(out, id, ty, rest));
        out.push_str("  ");
        if !returned_in_memory && ty != TypeId::VOID {
            _ = write!(out, "{} = ", value_name(id));
        }
        let function = &*self;
        let arguments = arguments.iter().map(|&argument| {
            let (parameter, value) = (parameter(argument), function.operand(argument));
            fmt::from_fn(move |out| write!(out, "{parameter} {value}"))
        });
        let arguments = comma_separated(arguments);
        if let Some(result_address) = result_address {
            _ = write!(
                out,
                "call {result} {callee}(ptr {result_address}{separator}{arguments})"
            );
        } else {
            _ = write!(out, "call {result} {callee}({arguments})");
        }
    }

    /// Writes the lines calling the C function `symbol` with `arguments`
    /// for the instruction `id`, each passed as `c_passing` says
    /// (`write_c_argument`), after noting its declaration. Each line but the
    /// last ends with `location`.
    fn write_c_call(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        symbol: &str,
        arguments: &[Operand],
        location: &DebugLocation,
    ) {
        let (ty, types, body) = (self.body.instructions[id].ty, self.types, self.body);
        let parameter_types = arguments
            .iter()
            .map(|&argument| body.operand_type(argument));
        let (result, parameters) = c_passing(types, self.module.target, ty, parameter_types);
        let name = value_name(id);
        // The memory a result kept there is written to.
        let returned = format!("{name}.r");
        let (mut declared, mut passed) = (Vec::new(), Vec::new());
        if let Passing::Memory { bytes, align, .. } = result {
            let parameter = format!("ptr sret([{bytes} x i8]) align {align}");
            passed.push(format!("{parameter} {returned}"));
            declared.push(parameter);
        }
        for (index, (&argument, passing)) in arguments.iter().zip(parameters).enumerate() {
            let path = format!("{name}.{index}");
            let (parameter, value) = self.write_c_argument(out, argument, passing, &path, location);
            passed.push(value);
            declared.push(parameter);
        }
        let result_text = match result {
            Passing::Direct(Some(extension)) => format!("{extension} {}", type_text(types, ty)),
            Passing::Direct(None) => type_text(types, ty).to_string(),
            Passing::Halves => "{ i64, i64 }".to_owned(),
            Passing::Vector => "<2 x i64>".to_owned(),
            Passing::Memory { .. } => "void".to_owned(),
        };
        let callee = global(symbol);
        let call = format!("call {result_text} {callee}({})", passed.join(", "));
        self.module.declare(symbol, || {
            format!(
                "declare {result_text} {callee}({})\n\n",
                declared.join(", ")
            )
        });
        // A result C gives in registers is stored to memory, to be read
        // there as one integer, as one in memory is.
        let (bytes, align) = match result {
            Passing::Direct(_) => {
                out.push_str("  ");
                if ty != TypeId::VOID {
                    _ = write!(out, "{name} = ");
                }
                out.push_str(&call);
                return;
            }
            Passing::Memory { bytes, align, .. } => {
                _ = writeln!(out, "  {call}{location}");
                (bytes, align)
            }
            Passing::Halves | Passing::Vector => {
                _ = writeln!(out, "  {name}.c = {call}{location}");
                _ = writeln!(
                    out,
                    "  store {result_text} {name}.c, ptr {returned}, align 16{location}"
                );
                (16, 16)
            }
        };
        _ = writeln!(
            self.memory,
            "  {returned} = alloca [{bytes} x i8], align {align}"
        );
        let (width, bits) = (bytes * 8, integer(types, ty).bits);
        if bits == width {
            _ = write!(
                out,
                "  {name} = load i{width}, ptr {returned}, align {align}"
            );
        } else {
            _ = writeln!(
                out,
                "  {name}.w = load i{width}, ptr {returned}, align {align}{location}"
            );
            _ = write!(out, "  {name} = trunc i{width} {name}.w to i{bits}");
        }
    }

    /// Writes the lines that pass `argument` to C as `passing` says, named
    /// from `path`, and gives the parameter's text in the declaration and
    /// the argument's in the call.
    fn write_c_argument(
        &mut self,
        out: &mut String,
        argument: Operand,
        passing: Passing,
        path: &str,
        location: &DebugLocation,
    ) -> (String, String) {
        let types = self.types;
        let ty = self.body.operand_type(argument);
        let (text, value) = (type_text(types, ty), self.operand(argument).to_string());
        match passing {
            Passing::Direct(None) => (text.to_string(), format!("{text} {value}")),
            Passing::Direct(Some(extension)) => (
                format!("{text} {extension}"),
                format!("{text} {extension} {value}"),
            ),
            Passing::Halves => {
                let limbs = write_limbs(out, &value, integer(types, ty), path, location);
                ("i64, i64".to_owned(), limbs)
            }
            // A copy C may change, of the value extended by its sign.
            Passing::Memory {
                bytes,
                align,
                by_value,
            } => {
                _ = writeln!(
                    self.memory,
                    "  {path} = alloca [{bytes} x i8], align {align}"
                );
                let width = bytes * 8;
                let wide = write_extended(out, &value, integer(types, ty), width, path, location);
                _ = writeln!(
                    out,
                    "  store i{width} {wide}, ptr {path}, align {align}{location}"
                );
                let parameter = if by_value {
                    format!("ptr byval([{bytes} x i8]) align {align}")
                } else {
                    "ptr".to_owned()
                };
                let passed = format!("{parameter} {path}");
                (parameter, passed)
            }
            Passing::Vector => unreachable!("only a result is passed as a vector"),
        }
    }
}

/// How C passes one parameter or result (`c_passing`).
#[derive(Clone, Copy)]
enum Passing {
    /// As its LLVM type, after the attribute C extends it to `int` by, if
    /// any (`signext`).
    Direct(Option<&'static str>),
    /// As two `i64` limbs (`write_limbs`); a result as `{ i64, i64 }`.
    Halves,
    /// A result as `<2 x i64>`, as Windows x64 returns `__int128`.
    Vector,
    /// In `bytes` of memory aligned to `align`: a parameter as a copy's
    /// address, which C copies onto the stack when `by_value`; a result
    /// where a first `sret` argument points.
    Memory {
        bytes: u32,
        align: u32,
        by_value: bool,
    },
}

/// How C on `target` passes the result and each parameter of a call, of
/// types `result` and `parameters`, as clang does (`CAbi`). x86-64 System V
/// passes the first six integer words in registers and two limbs that no
/// longer fit there whole on the stack, so a parameter's way depends on
/// those before it.
fn c_passing(
    types: &Types,
    target: &Machine,
    result: TypeId,
    parameters: impl Iterator<Item = TypeId>,
) -> (Passing, Vec<Passing>) {
    let result = c_passing_of(types, target, result, true);
    let mut registers = if let Passing::Memory { .. } = result {
        5
    } else {
        6
    };
    let parameters = parameters.map(|ty| {
        let passing = c_passing_of(types, target, ty, false);
        if target.c_abi != CAbi::SystemV {
            return passing;
        }
        let needed = match (passing, &types[ty]) {
            (Passing::Halves, _) => 2,
            (Passing::Direct(_), Type::Integer(integer)) if integer.bits == 128 => 2,
            (Passing::Direct(_), Type::Float(_)) | (Passing::Memory { .. }, _) => 0,
            _ => 1,
        };
        match passing {
            _ if needed <= registers => {
                registers -= needed;
                passing
            }
            Passing::Halves => Passing::Memory {
                bytes: 16,
                align: 8,
                by_value: true,
            },
            // An `i128` goes on the stack by itself.
            _ => passing,
        }
    });
    (result, parameters.collect())
}

/// How C on `target` passes a value of type `ty` as a parameter, or else
/// as a `result`, while registers are left (`c_passing`). Only scalars pass
/// to C (`resolve_signature_type`). An integer in memory takes whole words:
/// 4 bytes on i386, else 8.
fn c_passing_of(types: &Types, target: &Machine, ty: TypeId, result: bool) -> Passing {
    let abi = target.c_abi;
    let Integer { signed, bits } = match types[ty] {
        Type::Bool if abi == CAbi::Aapcs64 => return Passing::Direct(None),
        Type::Bool => return Passing::Direct(Some("zeroext")),
        Type::Integer(integer) => integer,
        _ => return Passing::Direct(None),
    };
    let memory = |align: u32, by_value| Passing::Memory {
        bytes: bits.div_ceil(align * 8) * align,
        align,
        by_value,
    };
    match abi {
        CAbi::SystemV | CAbi::I386 | CAbi::Darwin64 if bits < 32 => {
            Passing::Direct(Some(if signed { "signext" } else { "zeroext" }))
        }
        CAbi::SystemV | CAbi::I386 | CAbi::Windows64 if bits <= 64 => Passing::Direct(None),
        CAbi::SystemV if bits < 128 => Passing::Halves,
        CAbi::SystemV if bits == 128 => Passing::Direct(None),
        CAbi::SystemV => memory(8, true),
        CAbi::I386 => memory(4, false),
        // As `__int128`, not `_BitInt(128)` (owner, 2026-10-29).
        CAbi::Windows64 if bits == 128 && result => Passing::Vector,
        CAbi::Windows64 if bits == 128 => Passing::Memory {
            bytes: 16,
            align: 16,
            by_value: false,
        },
        CAbi::Windows64 => memory(8, false),
        CAbi::Aapcs64 | CAbi::Darwin64 if bits <= target.widest_c_integer() => {
            Passing::Direct(None)
        }
        CAbi::Aapcs64 | CAbi::Darwin64 => {
            unreachable!("the checker rejects a C integer wider than the target's")
        }
    }
}

/// The integer type `ty`, which a value C passes in memory or as limbs has.
fn integer(types: &Types, ty: TypeId) -> Integer {
    let Type::Integer(integer) = types[ty] else {
        unreachable!("only an integer passes to C in memory or as limbs")
    };
    integer
}
