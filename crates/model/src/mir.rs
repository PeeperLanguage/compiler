//! MIR: one function as a graph of basic blocks, shaped like LLVM IR at `-O0`
//! (docs/ir-design.md). Parameters and `let`s are variable slots that values
//! reach through `store` and leave through `load`; every other value is an
//! instruction, written once and used after it. Every part keeps the span it
//! came from, for messages and debug lines.

use std::fmt::Write;

use crate::DefId;
use crate::constant::Constant;
use crate::constant::Value;
use crate::constant::integer_text;
use crate::operators::BinaryOp;
use crate::operators::UnaryOp;
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
    pub hidden: bool,      // made by the checker; its span is not a name
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

/// Storage that a value is read from, written to or borrowed: a variable,
/// then each step from it in order (`s.start.x` is one place, two fields;
/// `r.*.x` goes through the reference in `r`, then to a field).
#[derive(Clone, Debug)]
pub struct Place {
    pub variable: Id<Variable>,
    pub projections: Box<[Projection]>,
}

/// One step of a `Place`.
#[derive(Clone, Copy, Debug)]
pub enum Projection {
    /// The field at this index, in declaration order, of the struct here.
    Field(u32),
    /// What the reference here refers to.
    Dereference,
    /// The element at this index, an integer computed before the place is
    /// used, of the array here; outside the array, the program fails.
    Index(Operand),
}

impl Projection {
    /// Whether the two steps, from one storage, may lead to the same
    /// storage: one field, or any two elements, whose indexes are not known.
    pub fn may_overlap(self, other: Projection) -> bool {
        match (self, other) {
            (Projection::Field(left), Projection::Field(right)) => left == right,
            (Projection::Dereference, Projection::Dereference)
            | (Projection::Index(_), Projection::Index(_)) => true,
            _ => unreachable!("one storage has one type, so one kind of step"),
        }
    }
}

impl Place {
    /// All of `variable`: no steps.
    pub fn whole(variable: Id<Variable>) -> Place {
        Place {
            variable,
            projections: Box::default(),
        }
    }

    /// The variable, when the place is all of it: no steps.
    pub fn whole_variable(&self) -> Option<Id<Variable>> {
        self.projections.is_empty().then_some(self.variable)
    }

    /// Whether the place goes through a reference, so it is not in its
    /// variable's own storage.
    pub fn is_behind_reference(&self) -> bool {
        self.projections
            .iter()
            .any(|step| matches!(step, Projection::Dereference))
    }
}

#[derive(Clone, Debug)]
pub enum InstructionKind {
    /// The value at the place, whose type copies (`Types::is_copy`).
    Load(Place),
    /// The value at the place, whose type moves: a whole variable is unset
    /// after; moving out of a struct's field, an array's element or a
    /// reference is an error.
    Move(Place),
    /// Sets the value at the place: a whole variable is set by it; a field,
    /// an element, or a value behind a reference, needs its variable already
    /// set.
    Store {
        place: Place,
        value: Operand,
    },
    /// A reference to the place, `&mut` when `mutable`; the variable must
    /// already be set.
    Borrow {
        place: Place,
        mutable: bool,
    },
    /// The place's address as a raw pointer (`@place`): as a `Borrow`, the
    /// variable must already be set, but no loan is made.
    Address(Place),
    Binary {
        op: BinaryOp,
        left: Operand,
        right: Operand,
    },
    Unary {
        op: UnaryOp,
        value: Operand,
    },
    /// The number or `bool` converted to the type `ty` (`Types::conversion`).
    Convert(Operand),
    Call {
        function: DefId,
        arguments: Box<[Operand]>,
    },
    /// A struct value from its fields' values, in declaration order, or an
    /// array value from its elements' values.
    Aggregate(Box<[Operand]>),
    /// Writes the values to the standard output, a space between each,
    /// then a newline when `newline`: as THIR's `Print`.
    Print {
        newline: bool,
        values: Box<[Operand]>,
    },
    /// The field its `fields` path leads to in a struct value that is not
    /// in a variable, as a `Place`'s path.
    Field {
        value: Operand,
        fields: Box<[u32]>,
    },
    /// The value from whichever listed block control came from; first in its
    /// block. `&&` and `||` give their result this way.
    Phi(Box<[(Id<Block>, Operand)]>),
    /// Stops the program with "panic: `message`" when `failed`, a `bool`
    /// not known at compile time, is true.
    Check {
        failed: Operand,
        message: &'static str,
    },
    /// The variable's block ends here, or a `break`, `continue` or `return`
    /// leaves it, so its storage is gone: no reference may still use it
    /// (`flow::loans::report_loan_errors`), and a loop's next pass may set
    /// it again (`flow::report_variable_state_errors`). Writes no code.
    ScopeEnd(Id<Variable>),
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
    /// `panic message` at `span`: writes the constant `message`, a
    /// `Value::Bytes`, and where, then ends the program; nothing runs after.
    Panic {
        message: Id<Constant>,
        span: Span,
    },
    /// The end of the function, at its closing brace, reached without a
    /// `return`: a function without a result returns there, and one with a
    /// result is missing a `return` (`flow::report_missing_return`). Every
    /// `Return` is a written `return`.
    FallOff(Span),
}

impl InstructionKind {
    /// Whether it is code that runs: all but a `ScopeEnd`, which only marks
    /// a point for the checks.
    pub fn runs(&self) -> bool {
        !matches!(self, InstructionKind::ScopeEnd(_))
    }
}

impl Terminator {
    /// The blocks control can go to next.
    pub fn targets(&self) -> impl Iterator<Item = Id<Block>> {
        let targets = match *self {
            Terminator::Goto(target) => [Some(target), None],
            Terminator::Branch {
                then, otherwise, ..
            } => [Some(then), Some(otherwise)],
            Terminator::Return { .. } | Terminator::Panic { .. } | Terminator::FallOff(_) => {
                [None, None]
            }
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
                    InstructionKind::Store { place, value } => {
                        printer.out.push_str("store ");
                        printer.write_place(place);
                        printer.out.push_str(", ");
                        printer.write_operand(value);
                    }
                    InstructionKind::Borrow { place, mutable } => {
                        printer
                            .out
                            .push_str(if *mutable { "borrow mut " } else { "borrow " });
                        printer.write_place(place);
                    }
                    InstructionKind::Address(place) => {
                        printer.out.push_str("address ");
                        printer.write_place(place);
                    }
                    InstructionKind::Load(place) => {
                        printer.out.push_str("load ");
                        printer.write_place(place);
                    }
                    InstructionKind::Move(place) => {
                        printer.out.push_str("move ");
                        printer.write_place(place);
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
                    InstructionKind::Convert(value) => {
                        printer.out.push_str("convert ");
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
                    InstructionKind::Print { newline, values } => {
                        printer
                            .out
                            .push_str(if *newline { "println(" } else { "print(" });
                        for (index, value) in values.iter().enumerate() {
                            if index > 0 {
                                printer.out.push_str(", ");
                            }
                            printer.write_operand(value);
                        }
                        printer.out.push(')');
                    }
                    InstructionKind::Field { value, fields } => {
                        printer.out.push_str("field ");
                        printer.write_operand(value);
                        printer.write_fields(fields);
                    }
                    InstructionKind::Check { failed, message } => {
                        printer.out.push_str("check ");
                        printer.write_operand(failed);
                        _ = write!(printer.out, ", \"{message}\"");
                    }
                    InstructionKind::ScopeEnd(variable) => {
                        printer.out.push_str("scope end ");
                        printer.write_place(&Place::whole(*variable));
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
                Terminator::Panic { message, .. } => {
                    printer.out.push_str("panic ");
                    printer.write_operand(&Operand::Constant(*message));
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
    /// A place: its variable, then each step, `.*` for a dereference, a
    /// field path for fields next to each other and `[at index]` for an
    /// element (`line.2[1, 0]`, `r.3.*[0]`, `a.1[at %4][2]`).
    fn write_place(&mut self, place: &Place) {
        let name = match self.body.variables[place.variable].hidden {
            true => "_",
            false => self.file.slice(self.body.variables.span(place.variable)),
        };
        _ = write!(self.out, "{name}.{}", place.variable.index());
        let mut fields = Vec::new();
        for &step in &place.projections {
            if let Projection::Field(index) = step {
                fields.push(index);
                continue;
            }
            self.write_fields(&fields);
            fields.clear();
            if let Projection::Index(index) = step {
                self.out.push_str("[at ");
                self.write_operand(&index);
                self.out.push(']');
            } else {
                self.out.push_str(".*");
            }
        }
        self.write_fields(&fields);
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
                Value::Null => write!(self.out, "none"),
            },
            Operand::Argument(index) => write!(self.out, "arg{index}"),
            Operand::Value(value) => write!(self.out, "%{}", value.index()),
        };
    }
}
