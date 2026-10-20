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
    pub constants: Vec<Constant>, // what `Operand::Constant` names; no spans, so not an arena
    pub lists: Lists,
}

/// A parameter or a `let`: an `alloca` in LLVM.
#[derive(Clone, Copy, Debug)]
pub struct Variable {
    pub ty: TypeId,
    pub mutable: bool,
    pub initialized: bool, // set where declared; false for a `let` without a value
    pub scope: Span,       // the block it is declared in, for a debugger
}

#[derive(Clone, Debug)]
pub struct Block {
    pub instructions: List<Instruction>,
    pub terminator: Terminator,
}

/// One operation; its id is the value it gives, of type `ty` (`void` when it
/// gives none).
#[derive(Clone, Debug)]
pub struct Instruction {
    pub ty: TypeId,
    pub kind: InstructionKind,
}

#[derive(Clone, Debug)]
pub enum InstructionKind {
    Load(Id<Variable>),
    Store {
        variable: Id<Variable>,
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
    /// result is missing a `return` (`path_checks::missing_return`). Every
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
    /// where the body is written; `function_name` names a called function. A
    /// variable shows as its name and index (`sum.2`), an integer constant as
    /// its bits read unsigned.
    pub fn text(
        &self,
        types: &Types,
        file: &SourceFile,
        function_name: impl Fn(DefId) -> String,
    ) -> String {
        let variable = |variable: Id<Variable>| {
            format!(
                "{}.{}",
                file.slice(self.variables.span(variable)),
                variable.index()
            )
        };
        let operand = |operand: &Operand| match *operand {
            Operand::Constant(constant) => match &self.constants[constant.index()].value {
                Value::Bool(value) => value.to_string(),
                Value::Integer(limbs) => integer_text(limbs),
                Value::Float(bits) => f64::from_bits(*bits).to_string(),
                Value::Bytes(bytes) => format!("c\"{}\"", bytes.escape_ascii()),
            },
            Operand::Argument(index) => format!("arg{index}"),
            Operand::Value(value) => format!("%{}", value.index()),
        };
        let mut out = String::new();
        for block in self.blocks.ids() {
            _ = writeln!(out, "  bb{}:", block.index());
            let block = &self.blocks[block];
            for instruction in self.lists.items(block.instructions) {
                let Instruction { ty, kind } = &self.instructions[instruction];
                out.push_str("    ");
                if *ty != TypeId::VOID {
                    _ = write!(out, "%{} = ", instruction.index());
                }
                _ = match kind {
                    InstructionKind::Store {
                        variable: stored,
                        value,
                    } => {
                        write!(out, "store {}, {}", variable(*stored), operand(value))
                    }
                    InstructionKind::Load(loaded) => write!(out, "load {}", variable(*loaded)),
                    InstructionKind::Binary { op, left, right } => {
                        write!(out, "{op:?} {}, {}", operand(left), operand(right))
                    }
                    InstructionKind::Unary { op, value } => {
                        write!(out, "{op:?} {}", operand(value))
                    }
                    InstructionKind::Widen(value) => write!(out, "widen {}", operand(value)),
                    InstructionKind::Call {
                        function,
                        arguments,
                    } => {
                        let arguments: Vec<String> = arguments.iter().map(operand).collect();
                        write!(
                            out,
                            "call {}({})",
                            function_name(*function),
                            arguments.join(", ")
                        )
                    }
                    InstructionKind::Phi(incoming) => {
                        let incoming: Vec<String> = incoming
                            .iter()
                            .map(|(block, value)| {
                                format!("bb{}: {}", block.index(), operand(value))
                            })
                            .collect();
                        write!(out, "phi {}", incoming.join(", "))
                    }
                };
                if *ty != TypeId::VOID {
                    _ = write!(out, ": {}", types.text(*ty));
                }
                out.push('\n');
            }
            _ = match &block.terminator {
                Terminator::Goto(target) => writeln!(out, "    goto bb{}", target.index()),
                Terminator::Branch {
                    condition,
                    span,
                    then,
                    otherwise,
                } => writeln!(
                    out,
                    "    branch {} ({}) bb{} bb{}",
                    operand(condition),
                    file.slice(*span),
                    then.index(),
                    otherwise.index()
                ),
                Terminator::Return { value: None, .. } => writeln!(out, "    return"),
                Terminator::Return {
                    value: Some(value), ..
                } => {
                    writeln!(out, "    return {}", operand(value))
                }
                Terminator::FallOff(span) => {
                    writeln!(out, "    fall off at `{}`", file.slice(*span))
                }
            };
        }
        out
    }
}
