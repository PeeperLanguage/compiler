//! Writing calls, as the C ABI passes their arguments and results.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;
use std::iter::Peekable;

use arena::Id;
use model::DefId;
use model::mir;
use model::mir::Operand;
use model::semantic_types::TypeId;

use crate::function::Function;
use crate::function::comma_separated;
use crate::function::global;
use crate::function::value_name;
use crate::layout::in_memory;
use crate::text::c_extension;
use crate::text::symbol;
use crate::text::type_text;

impl Function<'_, '_, '_> {
    /// Writes the line calling `function` with `arguments` for the
    /// instruction `id`, after noting the declaration the module needs for
    /// it; a result kept in memory is built where `value_address` says,
    /// before the `rest` of the block.
    pub(super) fn write_call(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        function: DefId,
        arguments: &[Operand],
        rest: &mut Peekable<impl Iterator<Item = Id<mir::Instruction>>>,
    ) {
        let (ty, types, body) = (self.body.instructions[id].ty, self.types, self.body);
        let symbol = symbol(self.module.program, function);
        let callee = global(&symbol);
        // Only C code needs the C ABI's extension of narrow integers; it
        // follows a parameter's type and comes before a result's.
        let external = project::extern_symbol(&self.module.program.modules, function).is_some();
        let extension = |ty| c_extension(types, ty).filter(|_| external);
        // A result kept in memory is written where a first argument points
        // (`add_function`). An argument kept in memory is always a private
        // copy (a load's or move's, a struct's or array's value, a result,
        // or a field of one), which the callee may change.
        let returned_in_memory = in_memory(types, ty);
        let result = fmt::from_fn(|out| match extension(ty) {
            _ if returned_in_memory => out.write_str("void"),
            Some(extension) => write!(out, "{extension} {}", type_text(types, ty)),
            None => type_text(types, ty).fmt(out),
        });
        let parameter = |argument| {
            let ty = body.operand_type(argument);
            fmt::from_fn(move |out| match extension(ty) {
                Some(extension) => write!(out, "{} {extension}", type_text(types, ty)),
                None if in_memory(types, ty) => out.write_str("ptr"),
                None => type_text(types, ty).fmt(out),
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
}
