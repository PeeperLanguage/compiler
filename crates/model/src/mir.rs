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

#[derive(Default)]
pub struct Body {
    pub blocks: Arena<Block>, // the first is the entry; with where each starts
    pub instructions: Arena<Instruction>, // with the expression each comes from
    pub variables: Arena<Variable>, // the parameters first; with each name
    /// The THIR body's blocks, with their spans, by the same ids: where each
    /// variable's name can be used, for a debugger.
    pub scopes: Arena<Scope>,
    pub constants: Vec<Constant>, // what `Operand::Constant` names; no spans, so not an arena
    pub lists: Lists,
}

/// A parameter or a `let`: an `alloca` in LLVM.
#[derive(Clone, Copy, Debug)]
pub struct Variable {
    pub ty: TypeId,
    pub mutable: bool,
    pub initialized: bool, // set where declared; false for a `let` without a value
    pub scope: Id<Scope>,  // the block it is declared in
}

/// A block of the source, as THIR's `Block` without its statements.
#[derive(Clone, Copy, Debug)]
pub struct Scope {
    pub parent: Option<Id<Scope>>, // `None` for the function's block
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
        let mut printer = Printer {
            body: self,
            file,
            out: String::new(),
        };
        for block in self.blocks.ids() {
            _ = writeln!(printer.out, "  bb{}:", block.index());
            let block = &self.blocks[block];
            for instruction in self.lists.items(block.instructions) {
                let Instruction { ty, kind } = &self.instructions[instruction];
                printer.out.push_str("    ");
                if *ty != TypeId::VOID {
                    _ = write!(printer.out, "%{} = ", instruction.index());
                }
                match kind {
                    InstructionKind::Store { variable, value } => {
                        printer.out.push_str("store ");
                        printer.variable(*variable);
                        printer.out.push_str(", ");
                        printer.operand(value);
                    }
                    InstructionKind::Load(variable) => {
                        printer.out.push_str("load ");
                        printer.variable(*variable);
                    }
                    InstructionKind::Binary { op, left, right } => {
                        _ = write!(printer.out, "{op:?} ");
                        printer.operand(left);
                        printer.out.push_str(", ");
                        printer.operand(right);
                    }
                    InstructionKind::Unary { op, value } => {
                        _ = write!(printer.out, "{op:?} ");
                        printer.operand(value);
                    }
                    InstructionKind::Widen(value) => {
                        printer.out.push_str("widen ");
                        printer.operand(value);
                    }
                    InstructionKind::Call {
                        function,
                        arguments,
                    } => {
                        _ = write!(printer.out, "call {}(", function_name(*function));
                        for (index, argument) in arguments.iter().enumerate() {
                            if index > 0 {
                                printer.out.push_str(", ");
                            }
                            printer.operand(argument);
                        }
                        printer.out.push(')');
                    }
                    InstructionKind::Phi(incoming) => {
                        printer.out.push_str("phi ");
                        for (index, (block, value)) in incoming.iter().enumerate() {
                            if index > 0 {
                                printer.out.push_str(", ");
                            }
                            _ = write!(printer.out, "bb{}: ", block.index());
                            printer.operand(value);
                        }
                    }
                }
                if *ty != TypeId::VOID {
                    _ = write!(printer.out, ": {}", types.text(*ty));
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
                    printer.operand(condition);
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
                        printer.operand(value);
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
    fn variable(&mut self, variable: Id<Variable>) {
        let name = self.file.slice(self.body.variables.span(variable));
        _ = write!(self.out, "{name}.{}", variable.index());
    }

    fn operand(&mut self, operand: &Operand) {
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
