//! MIR: one function as a graph of basic blocks, shaped like LLVM IR at `-O0`
//! (docs/ir-design.md). Parameters and `let`s are variable slots that values
//! reach through `store` and leave through `load`; every other value is an
//! instruction, written once and used after it. Every part keeps the span it
//! came from, for messages and debug lines.

use std::fmt::Write;

use crate::BinaryOp;
use crate::DefId;
use crate::UnaryOp;
use crate::types::TypeId;
use crate::types::Types;
use crate::value::Value;
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
    pub constants: Vec<Value>, // what `Operand::Constant` names
    pub lists: Lists,
}

/// A parameter or a `let`: an `alloca` in LLVM.
#[derive(Clone, Copy, Debug)]
pub struct Variable {
    pub ty: TypeId,
    pub mutable: bool,
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
    /// The body's constant at this position.
    Constant(u32),
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
    /// The end of a function with a result, at its closing brace, reached
    /// without a `return`.
    FallOff(Span),
}

impl Body {
    /// The body as text, one block after another, for `peeper mir`. `file` is
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
            Operand::Constant(index) => match &self.constants[index as usize] {
                Value::Bool(value) => value.to_string(),
                Value::Integer(limbs) => match limbs.as_slice() {
                    [low, high @ ..] if high.iter().all(|&limb| limb == 0) => low.to_string(),
                    wide => {
                        let hex: Vec<String> = wide
                            .iter()
                            .rev()
                            .map(|limb| format!("{limb:016x}"))
                            .collect();
                        format!("0x{}", hex.concat())
                    }
                },
                Value::Float(bits) => f64::from_bits(*bits).to_string(),
            },
            Operand::Argument(index) => format!("arg{index}"),
            Operand::Value(value) => format!("%{}", value.index()),
        };
        let mut out = String::new();
        for index in 0..self.blocks.len() {
            let block = &self.blocks[Id::from_index(index)];
            _ = writeln!(out, "  bb{index}:");
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
