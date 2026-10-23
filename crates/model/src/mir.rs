//! MIR: one function as a graph of basic blocks, shaped like LLVM IR at `-O0`
//! (docs/ir-design.md). Parameters and `let`s are variable slots that values
//! reach through `store` and leave through `load`; every other value is an
//! instruction, written once and used after it. Every part keeps the span it
//! came from, for messages and debug lines.

use std::fmt::Write;

use crate::BinaryOp;
use crate::DefId;
use crate::UnaryOp;
use crate::constant::Constant;
use crate::constant::Value;
use crate::constant::integer_text;
use crate::semantic_types::TypeId;
use crate::semantic_types::Types;
use arena::Arena;
use arena::Id;
use arena::List;
use arena::Lists;
use source::SourceFile;
use source::Span;

pub struct Body {
    pub blocks: Arena<Block>, // the first is the entry; with where each starts
    pub instructions: Arena<Instruction>, // with the expression each comes from
    pub variables: Arena<Variable>, // the parameters first; with each name
    pub parameters: usize,    // how many of the variables are parameters
    pub result: TypeId,       // the type the function gives
    /// The THIR body's blocks, with their spans, by the same ids: where each
    /// variable's name can be used and each instruction is written. Only debug
    /// info reads them.
    pub debug_scopes: Arena<DebugScope>,
    pub constants: Vec<Constant>, // what `Operand::Constant` names; no spans, so not an arena
    pub lists: Lists,
}

/// A parameter or a `let`: an `alloca` in LLVM.
#[derive(Clone, Copy, Debug)]
pub struct Variable {
    pub ty: TypeId,
    pub mutable: bool,
    pub initialized: bool, // set where declared; false for a `let` without a value
    pub debug_scope: Id<DebugScope>, // the block it is declared in
}

/// A block of the source, as THIR's `Block` without its statements, for a
/// debugger.
#[derive(Clone, Copy, Debug)]
pub struct DebugScope {
    pub parent: Option<Id<DebugScope>>, // `None` for the function's block
}

#[derive(Clone, Debug)]
pub struct Block {
    pub instructions: List<Instruction>,
    pub terminator: Terminator,
    pub debug_scope: Id<DebugScope>, // where the terminator is written
}

/// One operation; its id is the value it gives, of type `ty` (`void` when it
/// gives none).
#[derive(Clone, Debug)]
pub struct Instruction {
    pub ty: TypeId,
    pub kind: InstructionKind,
    pub debug_scope: Id<DebugScope>, // the block of the source it is written in
}

#[derive(Clone, Debug)]
pub enum InstructionKind {
    Load(Id<Variable>),
    /// The field of a struct variable that its `fields` path leads to: field
    /// indices in declaration order, outermost first (`s.start.x` is one
    /// load).
    LoadField {
        variable: Id<Variable>,
        fields: Box<[u32]>,
    },
    Store {
        variable: Id<Variable>,
        value: Operand,
    },
    /// Sets the field of a struct variable that its `fields` path leads to,
    /// as `LoadField`'s path; the variable must already be set.
    StoreField {
        variable: Id<Variable>,
        fields: Box<[u32]>,
        value: Operand,
    },
    Binary {
        op: BinaryOp,
        left: Operand,
        right: Operand,
    },
    Unary {
        op: UnaryOp,
        value: Operand,
    },
    /// The value converted to the wider type `ty` (`Type::widens_to`).
    Widen(Operand),
    Call {
        function: DefId,
        arguments: Box<[Operand]>,
    },
    /// A struct value from its fields' values, in declaration order.
    Aggregate(Box<[Operand]>),
    /// The field its `fields` path leads to in a struct value that is not
    /// in a variable, as `LoadField`'s path.
    Field {
        value: Operand,
        fields: Box<[u32]>,
    },
    /// The value from whichever listed block control came from; first in its
    /// block. `&&` and `||` give their result this way.
    Phi(Box<[(Id<Block>, Operand)]>),
}

#[derive(Clone, Copy, Debug)]
pub enum Operand {
    /// One of the body's `constants`.
    Constant(Id<Constant>),
    /// The function's parameter at this position, as passed in.
    Argument(u32),
    /// What the instruction gives.
    Value(Id<Instruction>),
}

#[derive(Clone, Debug)]
pub enum Terminator {
    Goto(Id<Block>),
    /// Goes to `then` when the condition, written at `span`, is true.
    Branch {
        condition: Operand,
        span: Span,
        then: Id<Block>,
        otherwise: Id<Block>,
    },
    Return {
        value: Option<Operand>,
        span: Span,
    },
    /// The end of the function, at its closing brace, reached without a
    /// `return`: a function without a result returns there, and one with a
    /// result is missing a `return` (`flow::report_missing_return`). Every
    /// `Return` is a written `return`.
    FallOff(Span),
}

impl Terminator {
    /// The blocks control can go to next.
    pub fn targets(&self) -> impl Iterator<Item = Id<Block>> {
        let targets = match *self {
            Terminator::Goto(target) => [Some(target), None],
            Terminator::Branch {
                then, otherwise, ..
            } => [Some(then), Some(otherwise)],
            Terminator::Return { .. } | Terminator::FallOff(_) => [None, None],
        };
        targets.into_iter().flatten()
    }
}

impl Body {
    /// The body as text, one block after another, for `peeper -dump`. `file` is
    /// where the body is written; `name_of` gives a declaration's name. A
    /// variable shows as its name and index (`sum.2`), an integer constant as
    /// its bits read unsigned, and a field path in brackets (`load p.0[1, 0]`).
    pub fn text<'n>(
        &self,
        types: &Types,
        file: &SourceFile,
        name_of: impl Fn(DefId) -> &'n str + Copy,
    ) -> String {
        let mut printer = Printer {
            body: self,
            file,
            out: String::new(),
        };
        for block in self.blocks.ids() {
            _ = writeln!(printer.out, "  bb{}:", block.index());
            let block = &self.blocks[block];
            for instruction in self.lists.items(block.instructions) {
                let Instruction { ty, kind, .. } = &self.instructions[instruction];
                printer.out.push_str("    ");
                if *ty != TypeId::VOID {
                    _ = write!(printer.out, "%{} = ", instruction.index());
                }
                match kind {
                    InstructionKind::Store { variable, value } => {
                        printer.out.push_str("store ");
                        printer.write_variable(*variable);
                        printer.out.push_str(", ");
                        printer.write_operand(value);
                    }
                    InstructionKind::StoreField {
                        variable,
                        fields,
                        value,
                    } => {
                        printer.out.push_str("store ");
                        printer.write_variable(*variable);
                        printer.write_fields(fields);
                        printer.out.push_str(", ");
                        printer.write_operand(value);
                    }
                    InstructionKind::Load(variable) => {
                        printer.out.push_str("load ");
                        printer.write_variable(*variable);
                    }
                    InstructionKind::LoadField { variable, fields } => {
                        printer.out.push_str("load ");
                        printer.write_variable(*variable);
                        printer.write_fields(fields);
                    }
                    InstructionKind::Binary { op, left, right } => {
                        _ = write!(printer.out, "{op:?} ");
                        printer.write_operand(left);
                        printer.out.push_str(", ");
                        printer.write_operand(right);
                    }
                    InstructionKind::Unary { op, value } => {
                        _ = write!(printer.out, "{op:?} ");
                        printer.write_operand(value);
                    }
                    InstructionKind::Widen(value) => {
                        printer.out.push_str("widen ");
                        printer.write_operand(value);
                    }
                    InstructionKind::Call {
                        function,
                        arguments,
                    } => {
                        _ = write!(printer.out, "call {}(", name_of(*function));
                        for (index, argument) in arguments.iter().enumerate() {
                            if index > 0 {
                                printer.out.push_str(", ");
                            }
                            printer.write_operand(argument);
                        }
                        printer.out.push(')');
                    }
                    InstructionKind::Aggregate(fields) => {
                        printer.out.push_str("aggregate(");
                        for (index, field) in fields.iter().enumerate() {
                            if index > 0 {
                                printer.out.push_str(", ");
                            }
                            printer.write_operand(field);
                        }
                        printer.out.push(')');
                    }
                    InstructionKind::Field { value, fields } => {
                        printer.out.push_str("field ");
                        printer.write_operand(value);
                        printer.write_fields(fields);
                    }
                    InstructionKind::Phi(incoming) => {
                        printer.out.push_str("phi ");
                        for (index, (block, value)) in incoming.iter().enumerate() {
                            if index > 0 {
                                printer.out.push_str(", ");
                            }
                            _ = write!(printer.out, "bb{}: ", block.index());
                            printer.write_operand(value);
                        }
                    }
                }
                if *ty != TypeId::VOID {
                    _ = write!(printer.out, ": {}", types.text(*ty, name_of));
                }
                printer.out.push('\n');
            }
            printer.out.push_str("    ");
            match &block.terminator {
                Terminator::Goto(target) => _ = write!(printer.out, "goto bb{}", target.index()),
                Terminator::Branch {
                    condition,
                    span,
                    then,
                    otherwise,
                } => {
                    printer.out.push_str("branch ");
                    printer.write_operand(condition);
                    _ = write!(
                        printer.out,
                        " ({}) bb{} bb{}",
                        file.slice(*span),
                        then.index(),
                        otherwise.index()
                    );
                }
                Terminator::Return { value, .. } => {
                    printer.out.push_str("return");
                    if let Some(value) = value {
                        printer.out.push(' ');
                        printer.write_operand(value);
                    }
                }
                Terminator::FallOff(span) => {
                    _ = write!(printer.out, "fall off at `{}`", file.slice(*span));
                }
            }
            printer.out.push('\n');
        }
        printer.out
    }
}

/// What `Body::text` writes with.
struct Printer<'b> {
    body: &'b Body,
    file: &'b SourceFile,
    out: String,
}

impl Printer<'_> {
    fn write_variable(&mut self, variable: Id<Variable>) {
        let name = self.file.slice(self.body.variables.span(variable));
        _ = write!(self.out, "{name}.{}", variable.index());
    }

    /// A field path, `[1, 0]`; nothing for an empty one.
    fn write_fields(&mut self, fields: &[u32]) {
        if fields.is_empty() {
            return;
        }
        let path: Vec<String> = fields.iter().map(u32::to_string).collect();
        _ = write!(self.out, "[{}]", path.join(", "));
    }

    fn write_operand(&mut self, operand: &Operand) {
        _ = match *operand {
            Operand::Constant(constant) => match &self.body.constants[constant.index()].value {
                Value::Bool(value) => write!(self.out, "{value}"),
                Value::Integer(limbs) => write!(self.out, "{}", integer_text(limbs)),
                Value::Float(bits) => write!(self.out, "{}", f64::from_bits(*bits)),
                Value::Bytes(bytes) => write!(self.out, "c\"{}\"", bytes.escape_ascii()),
            },
            Operand::Argument(index) => write!(self.out, "arg{index}"),
            Operand::Value(value) => write!(self.out, "%{}", value.index()),
        };
    }
}
