//! The MIR as text, in the real compiler's format
//! (`crates/middle/model/src/mir.rs`, `Body::text`), so a dump of one
//! program from each can be compared line by line.

use std::fmt::Write;

use gateway::check::Checker;
use model::mir::Body;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Place;
use model::mir::Program;
use model::mir::Projection;
use model::mir::Terminator;
use model::span::Span;
use model::types::TypeId;

pub fn text(checker: &Checker<'_>, program: &Program, _: u32) -> String {
    let mut out = String::new();
    for body in &program.functions {
        _ = writeln!(out, "fn {}:", slice(checker, body.name));
        write_body(&mut out, checker, program, body);
    }
    out
}

fn slice<'a>(checker: &Checker<'a>, span: Span) -> &'a str {
    &checker.source[span.start as usize..span.end as usize]
}

fn write_body(out: &mut String, checker: &Checker<'_>, program: &Program, body: &Body) {
    for (index, block) in body.blocks.iter().enumerate() {
        _ = writeln!(out, "  bb{index}:");
        for &id in &block.instructions {
            let instruction = &body.instructions[id.0 as usize];
            out.push_str("    ");
            if instruction.ty != TypeId::VOID {
                _ = write!(out, "%{} = ", id.0);
            }
            match &instruction.kind {
                InstructionKind::Load(place) => {
                    out.push_str("load ");
                    write_place(out, checker, body, place);
                }
                InstructionKind::Move(place) => {
                    out.push_str("move ");
                    write_place(out, checker, body, place);
                }
                InstructionKind::Store { place, value, .. } => {
                    out.push_str("store ");
                    write_place(out, checker, body, place);
                    out.push_str(", ");
                    write_operand(out, value);
                }
                InstructionKind::Borrow { place, mutable } => {
                    out.push_str(if *mutable { "borrow mut " } else { "borrow " });
                    write_place(out, checker, body, place);
                }
                InstructionKind::Binary {
                    operator,
                    left,
                    right,
                    ..
                } => {
                    _ = write!(out, "{operator:?} ");
                    write_operand(out, left);
                    out.push_str(", ");
                    write_operand(out, right);
                }
                InstructionKind::Call {
                    function,
                    arguments,
                } => {
                    let name = slice(checker, program.functions[*function as usize].name);
                    _ = write!(out, "call {name}(");
                    write_operands(out, arguments.iter());
                    out.push(')');
                }
                InstructionKind::Aggregate(parts) => {
                    out.push_str("aggregate(");
                    write_operands(out, parts.iter().map(|part| &part.value));
                    out.push(')');
                }
                InstructionKind::ScopeEnd(variable) => {
                    out.push_str("scope end ");
                    write_place(out, checker, body, &Place::whole(*variable));
                }
            }
            if instruction.ty != TypeId::VOID {
                _ = write!(out, ": {}", checker.type_text(instruction.ty));
            }
            out.push('\n');
        }
        out.push_str("    ");
        match &block.terminator {
            Terminator::Goto(target) => _ = write!(out, "goto bb{}", target.0),
            Terminator::Branch {
                condition,
                span,
                then,
                otherwise,
            } => {
                out.push_str("branch ");
                write_operand(out, condition);
                let text = slice(checker, *span);
                _ = write!(out, " ({text}) bb{} bb{}", then.0, otherwise.0);
            }
            Terminator::Return { value, .. } => {
                out.push_str("return");
                if let Some(value) = value {
                    out.push(' ');
                    write_operand(out, value);
                }
            }
            Terminator::FallOff(span) => _ = write!(out, "fall off at `{}`", slice(checker, *span)),
        }
        out.push('\n');
    }
}

fn write_operands<'a>(out: &mut String, operands: impl Iterator<Item = &'a Operand>) {
    for (index, operand) in operands.enumerate() {
        if index > 0 {
            out.push_str(", ");
        }
        write_operand(out, operand);
    }
}

fn write_operand(out: &mut String, operand: &Operand) {
    _ = match *operand {
        Operand::Constant(value) => write!(out, "{value}"),
        Operand::Argument(index) => write!(out, "arg{index}"),
        Operand::Value(id) => write!(out, "%{}", id.0),
        Operand::Function(function) => write!(out, "fn{function}"),
    };
}

/// Its variable as `name.index` (`_` for a hidden one), then `.*` for each
/// dereference and the field indexes next to each other in brackets.
fn write_place(out: &mut String, checker: &Checker<'_>, body: &Body, place: &Place) {
    let variable = body.variables[place.variable.0 as usize];
    let name = match variable.hidden {
        true => "_",
        false => slice(checker, variable.span),
    };
    _ = write!(out, "{name}.{}", place.variable.0);
    let mut fields = Vec::new();
    for projection in &place.projections {
        match *projection {
            Projection::Field { index, .. } => fields.push(index.to_string()),
            Projection::Dereference => {
                write_fields(out, &mut fields);
                out.push_str(".*");
            }
        }
    }
    write_fields(out, &mut fields);
}

fn write_fields(out: &mut String, fields: &mut Vec<String>) {
    if !fields.is_empty() {
        _ = write!(out, "[{}]", fields.join(", "));
        fields.clear();
    }
}
