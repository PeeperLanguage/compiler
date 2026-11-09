//! The shared MIR: one graph per function, in the shape of the real
//! compiler's (`crates/middle/model/src/mir.rs`), close to LLVM at `-O0`.
//! Values reach a variable only through `Store` and leave it only through
//! `Load` or `Move`, so the path checks follow variables and places, never
//! types: a type is known here only by its facts.

use crate::facts::Facts;
use crate::span::Span;
use crate::types::TypeId;

pub struct Program {
    /// By function id.
    pub functions: Vec<Body>,
    /// Each type's facts, by `TypeId`: all a backend knows of a type.
    pub facts: Vec<Facts>,
    pub pointer_bytes: u64,
}

impl Program {
    pub fn facts(&self, ty: TypeId) -> Facts {
        self.facts[ty.0 as usize]
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct VariableId(pub u32);

#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct InstructionId(pub u32);

#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct BlockId(pub u32);

pub struct Body {
    /// Where the function's name is written.
    pub name: Span,
    /// The parameters first, in order: an `alloca` each in LLVM.
    pub variables: Vec<Variable>,
    /// How many of the first variables are the parameters.
    pub parameters: usize,
    pub instructions: Vec<Instruction>,
    /// The first is the entry.
    pub blocks: Vec<Block>,
    /// What it returns: a function giving a value must not fall off its end.
    pub result: TypeId,
}

/// A parameter, a `let`, or a temporary the lowering made.
#[derive(Clone, Copy, Debug)]
pub struct Variable {
    pub ty: TypeId,
    pub mutable: bool,
    /// Set where it is declared: a parameter, or a `let` with a value.
    pub initialized: bool,
    /// Its name, or for a temporary the expression it holds.
    pub span: Span,
    /// Made by lowering; its span is not a name.
    pub hidden: bool,
}

pub struct Block {
    pub instructions: Vec<InstructionId>,
    pub terminator: Terminator,
}

/// One operation; its id is the value it gives, of `ty`, which is `void`
/// for one that gives none.
pub struct Instruction {
    pub ty: TypeId,
    pub kind: InstructionKind,
    /// The code it is lowered from, for the path checks' errors.
    pub span: Span,
}

/// Storage a value is read from, written to or borrowed: a variable, then
/// each step from it (`r.*.x` goes through the reference in `r`, then to a
/// field).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Place {
    pub variable: VariableId,
    pub projections: Vec<Projection>,
}

impl Place {
    pub fn whole(variable: VariableId) -> Place {
        Place {
            variable,
            projections: Vec::new(),
        }
    }

    /// The variable, when the place is all of it.
    pub fn whole_variable(&self) -> Option<VariableId> {
        self.projections.is_empty().then_some(self.variable)
    }

    /// Whether the place goes through a reference, out of its variable's
    /// own storage.
    pub fn is_behind_reference(&self) -> bool {
        self.projections.contains(&Projection::Dereference)
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Projection {
    /// The field at `index`, in declaration order, `offset` bytes into the
    /// value here. Its loan holders (`Facts::holders`) are `holders` of the
    /// value's, from the one `first_holder` in: the loan check reads them,
    /// as it knows a type only by its facts.
    Field {
        index: u32,
        offset: u64,
        first_holder: u32,
        holders: u32,
    },
    /// What the reference here points to.
    Dereference,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Operator {
    Add,
    Subtract,
    Multiply,
    LessThan,
}

pub enum InstructionKind {
    /// The value at the place, whose type copies.
    Load(Place),
    /// The value at the place, whose type moves: a whole variable is unset
    /// after.
    Move(Place),
    /// Sets the place to `value`, of `ty`.
    Store {
        place: Place,
        value: Operand,
        ty: TypeId,
    },
    /// A reference to the place, `&mut` when `mutable`.
    Borrow { place: Place, mutable: bool },
    /// On two values of the type `operands`.
    Binary {
        operator: Operator,
        left: Operand,
        right: Operand,
        operands: TypeId,
    },
    Call {
        function: u32,
        arguments: Vec<Operand>,
    },
    /// A value in memory from its parts.
    Aggregate(Vec<Part>),
    /// The variable's scope ends here, or a jump leaves it, so its storage
    /// is gone: for the path checks only; cleanup removes it.
    ScopeEnd(VariableId),
}

/// One part of an `Aggregate`: its value, of `ty`, `offset` bytes in.
pub struct Part {
    pub offset: u64,
    pub ty: TypeId,
    pub value: Operand,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Operand {
    Constant(u64),
    /// The function's parameter at this position, as passed in.
    Argument(u32),
    /// What the instruction gives.
    Value(InstructionId),
    /// The address of the function with this id.
    Function(u32),
}

#[derive(Clone)]
pub enum Terminator {
    Goto(BlockId),
    /// To `then` when the condition, written at `span`, is true.
    Branch {
        condition: Operand,
        span: Span,
        then: BlockId,
        otherwise: BlockId,
    },
    /// A written `return`.
    Return {
        value: Option<Operand>,
        span: Span,
    },
    /// The function's closing brace, reached without a `return`.
    FallOff(Span),
}

impl Terminator {
    /// The blocks control can go to next.
    pub fn targets(&self) -> Vec<BlockId> {
        match *self {
            Terminator::Goto(to) => vec![to],
            Terminator::Branch {
                then, otherwise, ..
            } => vec![then, otherwise],
            Terminator::Return { .. } | Terminator::FallOff(_) => Vec::new(),
        }
    }

    /// Sends each jump to the block `to` gives for its target instead.
    pub fn retarget(&mut self, mut to: impl FnMut(BlockId) -> BlockId) {
        match self {
            Terminator::Goto(target) => *target = to(*target),
            Terminator::Branch {
                then, otherwise, ..
            } => {
                *then = to(*then);
                *otherwise = to(*otherwise);
            }
            Terminator::Return { .. } | Terminator::FallOff(_) => {}
        }
    }
}
