//! The MIR of core concepts only (docs/feature-free-mir.md): one function as
//! a graph of blocks over registers of a machine class and slots of bytes.
//! No part of it names a language feature: a struct is a slot with offsets,
//! an interface value is two pointers, a method call is a load from a table
//! and a call through the pointer. Lowering writes it; the path checks, the
//! interpreter and the LLVM writer read it and need no type.
//!
//! shortcut: named `core_mir` while the old `mir` is still read by the
//! backends; it becomes `mir` when the old one is deleted (the plan's
//! removal step).

use std::fmt::Write;

use crate::FunctionCopy;
use crate::semantic_types::TypeId;
use arena::Arena;
use arena::Id;
use arena::List;
use arena::Lists;
use rustc_hash::FxHashMap;
use source::SourceFile;
use source::Span;

arena::nodes! {
    Body;
    Block => blocks,
    Instruction => instructions,
    Slot => slots,
    DebugScope => debug_scopes,
}

/// What one register holds: an integer of that many bits, a float, or an
/// address. Signedness is not a class; it is in the operation. An integer
/// wider than 128 bits is `Wide` of its exact width: its operations wrap
/// there, as the machine's do at a fixed width.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Class {
    I8,
    I16,
    I32,
    I64,
    I128,
    Wide(u32),
    F32,
    F64,
    Pointer,
}

impl Class {
    /// How many bytes a value of the class takes in memory, on a target
    /// whose pointers are `pointer_bytes` wide: a wide integer is rounded
    /// up to whole 128-bit words, as `layout` aligns it.
    pub fn bytes(self, pointer_bytes: u64) -> u64 {
        match self {
            Class::I8 => 1,
            Class::I16 => 2,
            Class::I32 | Class::F32 => 4,
            Class::I64 | Class::F64 => 8,
            Class::I128 => 16,
            Class::Wide(bits) => u64::from(bits.div_ceil(8)).next_multiple_of(16),
            Class::Pointer => pointer_bytes,
        }
    }

    /// The integer class of `bits` bits: the narrowest that holds them, or
    /// `Wide` of them past 128.
    pub fn integer(bits: u32) -> Class {
        match bits {
            1..=8 => Class::I8,
            9..=16 => Class::I16,
            17..=32 => Class::I32,
            33..=64 => Class::I64,
            65..=128 => Class::I128,
            _ => Class::Wide(bits),
        }
    }

    /// How many bits an integer class holds.
    pub fn bits(self) -> u32 {
        match self {
            Class::I8 => 8,
            Class::I16 => 16,
            Class::I32 | Class::F32 => 32,
            Class::I64 | Class::F64 => 64,
            Class::I128 => 128,
            Class::Wide(bits) => bits,
            Class::Pointer => unreachable!("a pointer's bits are the target's"),
        }
    }
}

impl std::fmt::Display for Class {
    /// The name the dump and the LLVM writer use: `i32`, `i200`, `f64`, `ptr`.
    fn fmt(&self, out: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Class::I8 => out.write_str("i8"),
            Class::I16 => out.write_str("i16"),
            Class::I32 => out.write_str("i32"),
            Class::I64 => out.write_str("i64"),
            Class::I128 => out.write_str("i128"),
            Class::Wide(bits) => write!(out, "i{bits}"),
            Class::F32 => out.write_str("f32"),
            Class::F64 => out.write_str("f64"),
            Class::Pointer => out.write_str("ptr"),
        }
    }
}

/// One function's MIR. Its parameters are its first slots, in order
/// (`parameters` of them): each is stored from `Operand::Parameter` as the
/// body starts. The first block is the entry.
pub struct Body {
    pub blocks: Arena<Block>,             // with where each starts
    pub instructions: Arena<Instruction>, // with the code each comes from
    pub slots: Arena<Slot>,               // with each name, or the code a temporary holds
    pub parameters: usize,
    /// How the function takes its parameters and gives its result. A result
    /// kept in memory is written where `Operand::Parameter(0)` points, a
    /// hidden first parameter; the written ones follow it.
    pub signature: Signature,
    /// The positions of the parameters a returned pointer may point into,
    /// for the loan check.
    pub origins: Box<[u32]>,
    /// A block of the source, for a debugger: block `i` of the source is
    /// debug scope `i`.
    pub debug_scopes: Arena<DebugScope>,
    /// Read-only data the body names (`Operand::Global`).
    pub globals: Vec<Global>,
    /// The signature of each call (`InstructionKind::Call`).
    pub signatures: Vec<Signature>,
    pub lists: Lists,
}

/// Stack memory of a function: a parameter, a `let`, or a temporary.
#[derive(Clone, Debug)]
pub struct Slot {
    pub size: u64,
    pub align: u64,
    /// The offsets of the pointers in it that may hold a loan.
    pub holders: Box<[u64]>,
    /// What a debugger shows it as; `None` for a temporary.
    pub debug_type: Option<DebugTypeId>,
    pub debug_scope: Id<DebugScope>, // the block it is declared in
    pub mutable: bool,
    /// Set where declared; false for a `let` without a value, whose first
    /// store sets it.
    pub initialized: bool,
    /// Made for a value, not written; its span is not a name.
    pub hidden: bool,
}

/// A block of the source, as THIR's `Block` without its statements, for a
/// debugger.
#[derive(Clone, Copy, Debug)]
pub struct DebugScope {
    pub parent: Option<Id<DebugScope>>, // `None` for the function's block
}

/// A type as a debugger shows it, in a table the whole program shares
/// (`DebugTypes`): names, sizes and offsets, which is all debug info holds.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum DebugType {
    Basic {
        name: Box<str>,
        bits: u32,
        /// The DWARF encoding, as `DW_ATE_signed`.
        encoding: &'static str,
    },
    Pointer {
        name: Box<str>,
        to: Option<DebugTypeId>, // `None` for a pointer to anything
    },
    Composite {
        name: Box<str>,
        size: u64,
        members: Box<[DebugMember]>,
    },
    Array {
        element: DebugTypeId,
        size: u64,
        lengths: Box<[u64]>, // of each level, outermost first
    },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DebugMember {
    pub name: Box<str>,
    pub offset: u64,
    pub ty: DebugTypeId,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct DebugTypeId(pub u32);

/// The debug types of a program, one per language type that has a value
/// (`of_type`). Lowering fills it; a backend reads `types` by id.
#[derive(Default)]
pub struct DebugTypes {
    /// Each type, at its `DebugTypeId`; `None` while it is being made
    /// (`reserve`), as a struct's members may name the struct.
    pub types: Vec<Option<DebugType>>,
    of_type: FxHashMap<TypeId, DebugTypeId>,
}

impl DebugTypes {
    /// The debug type made for the language type `ty`, if one was.
    pub fn of_type(&self, ty: TypeId) -> Option<DebugTypeId> {
        self.of_type.get(&ty).copied()
    }

    /// Adds `debug_type` as the language type `ty`'s.
    pub fn add(&mut self, ty: TypeId, debug_type: DebugType) -> DebugTypeId {
        let id = self.reserve(ty);
        self.define(id, debug_type);
        id
    }

    /// Adds `debug_type`, which stands for no language type: a pointer a
    /// composite's member is, a C `char`.
    pub fn push(&mut self, debug_type: DebugType) -> DebugTypeId {
        self.types.push(Some(debug_type));
        DebugTypeId((self.types.len() - 1) as u32)
    }

    /// A number for the language type `ty`'s debug type, to be defined
    /// once its members are made (`define`).
    pub fn reserve(&mut self, ty: TypeId) -> DebugTypeId {
        self.types.push(None);
        let id = DebugTypeId((self.types.len() - 1) as u32);
        self.of_type.insert(ty, id);
        id
    }

    pub fn define(&mut self, id: DebugTypeId, debug_type: DebugType) {
        self.types[id.0 as usize] = Some(debug_type);
    }
}

/// Read-only data.
#[derive(Clone, Debug)]
pub enum Global {
    Bytes(Box<[u8]>),
    /// A table of function addresses, in order.
    Functions(Box<[FunctionCopy]>),
}

/// How a function takes its parameters and gives its result.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Signature {
    pub parameters: Box<[Passing]>,
    pub result: Option<Passing>,
    /// Whether it takes more arguments after the parameters, as a C
    /// function may.
    pub variadic: bool,
}

impl Signature {
    /// How many hidden parameters come before the written ones: one, the
    /// address a result kept in memory is written to.
    pub fn hidden_parameters(&self) -> u32 {
        u32::from(matches!(self.result, Some(Passing::Memory { .. })))
    }
}

/// How one value is passed to or from a function.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Passing {
    /// In a register. Whether an integer is signed says how a C ABI
    /// extends a narrow one.
    Register { class: Class, signed: bool },
    /// In `size` bytes of memory aligned to `align`: a parameter as the
    /// address of a copy the callee may change, a result where a hidden
    /// first argument points.
    Memory { size: u64, align: u64 },
}

pub struct Block {
    pub instructions: List<Instruction>,
    pub terminator: Terminator,
    pub debug_scope: Id<DebugScope>, // where the terminator is written
}

pub struct Instruction {
    pub kind: InstructionKind,
    pub debug_scope: Id<DebugScope>, // the block of the source it is written in
}

/// A value an instruction reads.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Operand {
    /// What an instruction gives.
    Value(Id<Instruction>),
    /// `bits` read as the class: an integer's low bits, a float's bits. A
    /// wide integer's constant is read-only data, loaded (`Global`).
    Constant { class: Class, bits: [u64; 2] },
    /// The function's parameter at this index, as passed in.
    Parameter(u32),
    /// The address of one of the body's `globals`.
    Global(u32),
}

impl Operand {
    /// The integer `value` as a constant of `class`, which is not `Wide`:
    /// lowering keeps a wide constant as read-only data.
    pub fn integer(class: Class, value: u64) -> Operand {
        assert!(
            !matches!(class, Class::Wide(_)),
            "a wide constant is read-only data"
        );
        Operand::Constant {
            class,
            bits: [value, 0],
        }
    }
}

/// Memory: where it starts, then the steps from there.
#[derive(Clone, Debug, PartialEq)]
pub struct Place {
    pub root: Root,
    pub steps: Box<[Step]>,
}

impl Place {
    /// All the memory of `slot`: a place of no steps.
    pub fn whole(slot: Id<Slot>) -> Place {
        Place {
            root: Root::Slot(slot),
            steps: Box::default(),
        }
    }

    /// The place `step` further in.
    pub fn then(mut self, step: Step) -> Place {
        let mut steps = self.steps.into_vec();
        steps.push(step);
        self.steps = steps.into();
        self
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Root {
    /// The slot's own memory.
    Slot(Id<Slot>),
    /// The memory at the address the operand gives.
    At(Operand),
}

/// One step of a `Place`.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Step {
    /// This many bytes further in.
    Offset(u64),
    /// What the pointer here points to.
    Through,
    /// The element `index`, a pointer-wide integer that lowering checked
    /// to be inside, of `size` bytes each.
    Index { index: Operand, size: u64 },
}

/// What an `Integer` instruction does to two registers of its class, the
/// result cut to that width.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum IntegerOp {
    Add,
    Subtract,
    Multiply,
    SignedDivide,
    UnsignedDivide,
    SignedRemainder,
    UnsignedRemainder,
    ShiftLeft,
    SignedShiftRight,
    UnsignedShiftRight,
    And,
    Or,
    Xor,
}

/// What a `Float` instruction does to two registers of its class.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FloatOp {
    Add,
    Subtract,
    Multiply,
    Divide,
    Remainder,
}

/// How a `Compare` instruction compares two registers of its class. On
/// floats, `Equal`, `FloatLess` and `FloatLessOrEqual` are false when
/// either side is NaN, and `NotEqual` is true.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Comparison {
    Equal,
    NotEqual,
    SignedLess,
    SignedLessOrEqual,
    UnsignedLess,
    UnsignedLessOrEqual,
    FloatLess,
    FloatLessOrEqual,
}

/// How a `Convert` instruction reads a register as another class.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Conversion {
    SignExtend,
    ZeroExtend,
    Truncate,
    SignedToFloat,
    UnsignedToFloat,
    /// Toward zero, to an integer of `bits` bits kept extended in its
    /// class; a value outside that range gives its nearest end, NaN gives 0.
    FloatToSigned {
        bits: u32,
    },
    FloatToUnsigned {
        bits: u32,
    },
    FloatWiden,
    FloatNarrow,
    /// The same address, as a pointer to other bytes: the way to the bytes
    /// of a value, and so to those of an address kept in memory, which a
    /// constant's run cannot know.
    PointerCast,
}

#[derive(Clone, Debug)]
pub enum InstructionKind {
    /// Wraps at the class's width.
    Integer {
        operation: IntegerOp,
        class: Class,
        left: Operand,
        right: Operand,
    },
    Float {
        operation: FloatOp,
        class: Class,
        left: Operand,
        right: Operand,
    },
    /// Whether `left` and `right` compare so: an `I8` that is 0 or 1.
    Compare {
        comparison: Comparison,
        class: Class,
        left: Operand,
        right: Operand,
    },
    /// `value`, of the class `from`, as the class `to`.
    Convert {
        conversion: Conversion,
        from: Class,
        to: Class,
        value: Operand,
    },
    Load {
        class: Class,
        place: Place,
    },
    Store {
        class: Class,
        place: Place,
        value: Operand,
    },
    /// `size` bytes from `from` to `to`, which do not overlap.
    Copy {
        to: Place,
        from: Place,
        size: u64,
    },
    /// The place's address: a pointer that holds no loan.
    Address(Place),
    /// A call, with the callee's signature (`Body::signatures`).
    Call {
        callee: Callee,
        signature: u32,
        arguments: Box<[Operand]>,
    },
    /// Stops the program with "panic: `message`" when `failed`, an `I8`, is
    /// not 0. Gives no value.
    Check {
        failed: Operand,
        message: Box<str>,
    },
    // The events, for the path checks only; removed before a backend runs.
    /// A reference to the place, `&mut` when `mutable`: a loan. Its value
    /// is the place's address. `noun` is the word a message calls it:
    /// "reference", "slice" or "interface".
    Borrow {
        place: Place,
        mutable: bool,
        noun: &'static str,
    },
    /// The value at the place, just read, is moved out.
    Moved(Place),
    /// The slot's storage ends: its block ends, or a jump leaves it.
    Dead(Id<Slot>),
}

/// Who a `Call` calls.
#[derive(Clone, Copy, Debug)]
pub enum Callee {
    /// A function of the program, or a copy of one; an `extern` function
    /// has no body.
    Function(FunctionCopy),
    /// A function of the runtime library, by its symbol
    /// (`runtime_abi::symbols`).
    Runtime(&'static str),
    /// The function whose address the operand gives.
    Pointer(Operand),
}

impl InstructionKind {
    /// The class of the value it gives, if any; a call's is read from its
    /// signature (`Body::class_of`).
    fn own_class(&self) -> Option<Class> {
        match self {
            InstructionKind::Integer { class, .. }
            | InstructionKind::Float { class, .. }
            | InstructionKind::Load { class, .. } => Some(*class),
            InstructionKind::Compare { .. } => Some(Class::I8),
            InstructionKind::Convert { to, .. } => Some(*to),
            InstructionKind::Address(_) | InstructionKind::Borrow { .. } => Some(Class::Pointer),
            InstructionKind::Call { .. }
            | InstructionKind::Store { .. }
            | InstructionKind::Copy { .. }
            | InstructionKind::Check { .. }
            | InstructionKind::Moved(_)
            | InstructionKind::Dead(_) => None,
        }
    }
}

pub enum Terminator {
    Jump(Id<Block>),
    /// To `then` when the `I8` condition, written at `span`, is not 0.
    Branch {
        condition: Operand,
        span: Span,
        then: Id<Block>,
        otherwise: Id<Block>,
    },
    /// A written `return`.
    Return {
        value: Option<Operand>,
        span: Span,
    },
    /// `panic message` at `span`: writes the global's bytes and where, then
    /// ends the program.
    Panic {
        message: u32,
        span: Span,
    },
    /// The function's closing brace, reached without a `return`.
    FallOff(Span),
}

impl Body {
    /// The class of the register `operand` is; `None` for a call that gives
    /// nothing or gives its result in memory.
    pub fn class_of(&self, operand: Operand) -> Option<Class> {
        match operand {
            Operand::Value(id) => match &self.instructions[id].kind {
                InstructionKind::Call { signature, .. } => {
                    match self.signatures[*signature as usize].result {
                        Some(Passing::Register { class, .. }) => Some(class),
                        Some(Passing::Memory { .. }) | None => None,
                    }
                }
                kind => kind.own_class(),
            },
            Operand::Constant { class, .. } => Some(class),
            Operand::Parameter(index) => {
                let hidden = self.signature.hidden_parameters();
                if index < hidden {
                    return Some(Class::Pointer);
                }
                match self.signature.parameters[(index - hidden) as usize] {
                    Passing::Register { class, .. } => Some(class),
                    Passing::Memory { .. } => Some(Class::Pointer),
                }
            }
            Operand::Global(_) => Some(Class::Pointer),
        }
    }

    /// The body as text, one block after another, for `peeper -dump`.
    /// `file` is where the body is written; `name_of` gives a function's
    /// name. A slot shows as its name and index (`sum.2`, `.5` for a
    /// temporary), a place as its root then `[+8]`, `[*]` or `[%3 * 4]`.
    pub fn text(&self, file: &SourceFile, name_of: impl Fn(FunctionCopy) -> String) -> String {
        let mut printer = Printer {
            body: self,
            file,
            name_of: &name_of,
            out: String::new(),
        };
        for block in self.blocks.ids() {
            _ = writeln!(printer.out, "  bb{}:", block.index());
            let block = &self.blocks[block];
            for instruction in self.lists.items(block.instructions) {
                printer.out.push_str("    ");
                if self.class_of(Operand::Value(instruction)).is_some() {
                    _ = write!(printer.out, "%{} = ", instruction.index());
                }
                printer.write_instruction(&self.instructions[instruction].kind);
                printer.out.push('\n');
            }
            printer.out.push_str("    ");
            printer.write_terminator(&block.terminator);
            printer.out.push('\n');
        }
        printer.out
    }
}

/// What `Body::text` writes with.
struct Printer<'b> {
    body: &'b Body,
    file: &'b SourceFile,
    name_of: &'b dyn Fn(FunctionCopy) -> String,
    out: String,
}

impl Printer<'_> {
    fn write_instruction(&mut self, kind: &InstructionKind) {
        match kind {
            InstructionKind::Integer {
                operation,
                class,
                left,
                right,
            } => {
                let name = match operation {
                    IntegerOp::Add => "add",
                    IntegerOp::Subtract => "sub",
                    IntegerOp::Multiply => "mul",
                    IntegerOp::SignedDivide => "sdiv",
                    IntegerOp::UnsignedDivide => "udiv",
                    IntegerOp::SignedRemainder => "srem",
                    IntegerOp::UnsignedRemainder => "urem",
                    IntegerOp::ShiftLeft => "shl",
                    IntegerOp::SignedShiftRight => "sshr",
                    IntegerOp::UnsignedShiftRight => "ushr",
                    IntegerOp::And => "and",
                    IntegerOp::Or => "or",
                    IntegerOp::Xor => "xor",
                };
                self.write_binary(name, *class, left, right);
            }
            InstructionKind::Float {
                operation,
                class,
                left,
                right,
            } => {
                let name = match operation {
                    FloatOp::Add => "fadd",
                    FloatOp::Subtract => "fsub",
                    FloatOp::Multiply => "fmul",
                    FloatOp::Divide => "fdiv",
                    FloatOp::Remainder => "frem",
                };
                self.write_binary(name, *class, left, right);
            }
            InstructionKind::Compare {
                comparison,
                class,
                left,
                right,
            } => {
                let name = match comparison {
                    Comparison::Equal => "eq",
                    Comparison::NotEqual => "ne",
                    Comparison::SignedLess => "slt",
                    Comparison::SignedLessOrEqual => "sle",
                    Comparison::UnsignedLess => "ult",
                    Comparison::UnsignedLessOrEqual => "ule",
                    Comparison::FloatLess => "flt",
                    Comparison::FloatLessOrEqual => "fle",
                };
                self.write_binary(name, *class, left, right);
            }
            InstructionKind::Convert {
                conversion,
                from,
                to,
                value,
            } => {
                let name = match conversion {
                    Conversion::SignExtend => "sext",
                    Conversion::ZeroExtend => "zext",
                    Conversion::Truncate => "trunc",
                    Conversion::SignedToFloat => "sitofp",
                    Conversion::UnsignedToFloat => "uitofp",
                    Conversion::FloatToSigned { .. } => "fptosi",
                    Conversion::FloatToUnsigned { .. } => "fptoui",
                    Conversion::FloatWiden => "fpext",
                    Conversion::FloatNarrow => "fptrunc",
                    Conversion::PointerCast => "bitcast",
                };
                _ = write!(self.out, "{name} {from} ");
                self.write_operand(value);
                _ = write!(self.out, " to {to}");
                if let Conversion::FloatToSigned { bits } | Conversion::FloatToUnsigned { bits } =
                    conversion
                {
                    _ = write!(self.out, " at {bits} bits");
                }
            }
            InstructionKind::Load { class, place } => {
                _ = write!(self.out, "load {class} ");
                self.write_place(place);
            }
            InstructionKind::Store {
                class,
                place,
                value,
            } => {
                _ = write!(self.out, "store {class} ");
                self.write_place(place);
                self.out.push_str(", ");
                self.write_operand(value);
            }
            InstructionKind::Copy { to, from, size } => {
                _ = write!(self.out, "copy {size} ");
                self.write_place(to);
                self.out.push_str(", ");
                self.write_place(from);
            }
            InstructionKind::Address(place) => {
                self.out.push_str("address ");
                self.write_place(place);
            }
            InstructionKind::Call {
                callee, arguments, ..
            } => {
                match callee {
                    Callee::Function(function) => {
                        _ = write!(self.out, "call {}(", (self.name_of)(*function));
                    }
                    Callee::Runtime(symbol) => _ = write!(self.out, "call {symbol}("),
                    Callee::Pointer(pointer) => {
                        self.out.push_str("call through ");
                        self.write_operand(pointer);
                        self.out.push('(');
                    }
                }
                self.write_operands(arguments);
                self.out.push(')');
            }
            InstructionKind::Check { failed, message } => {
                self.out.push_str("check ");
                self.write_operand(failed);
                _ = write!(self.out, ", \"{message}\"");
            }
            InstructionKind::Borrow {
                place,
                mutable,
                noun,
            } => {
                let mutable = if *mutable { " mut" } else { "" };
                _ = write!(self.out, "borrow{mutable} ");
                self.write_place(place);
                _ = write!(self.out, " as {noun}");
            }
            InstructionKind::Moved(place) => {
                self.out.push_str("moved ");
                self.write_place(place);
            }
            InstructionKind::Dead(slot) => {
                self.out.push_str("dead ");
                self.write_slot(*slot);
            }
        }
    }

    fn write_binary(&mut self, name: &str, class: Class, left: &Operand, right: &Operand) {
        _ = write!(self.out, "{name} {class} ");
        self.write_operand(left);
        self.out.push_str(", ");
        self.write_operand(right);
    }

    fn write_terminator(&mut self, terminator: &Terminator) {
        match terminator {
            Terminator::Jump(target) => _ = write!(self.out, "jump bb{}", target.index()),
            Terminator::Branch {
                condition,
                then,
                otherwise,
                ..
            } => {
                self.out.push_str("branch ");
                self.write_operand(condition);
                _ = write!(self.out, ", bb{}, bb{}", then.index(), otherwise.index());
            }
            Terminator::Return { value, .. } => {
                self.out.push_str("return");
                if let Some(value) = value {
                    self.out.push(' ');
                    self.write_operand(value);
                }
            }
            Terminator::Panic { message, .. } => {
                _ = write!(self.out, "panic global {message}");
            }
            Terminator::FallOff(_) => self.out.push_str("fall off at `}`"),
        }
    }

    fn write_operands(&mut self, operands: &[Operand]) {
        for (index, operand) in operands.iter().enumerate() {
            if index > 0 {
                self.out.push_str(", ");
            }
            self.write_operand(operand);
        }
    }

    fn write_operand(&mut self, operand: &Operand) {
        match *operand {
            Operand::Value(id) => _ = write!(self.out, "%{}", id.index()),
            Operand::Constant { class, bits } => match class {
                Class::F32 => _ = write!(self.out, "{}", f32::from_bits(bits[0] as u32)),
                Class::F64 => _ = write!(self.out, "{}", f64::from_bits(bits[0])),
                Class::I128 if bits[1] != 0 => {
                    _ = write!(
                        self.out,
                        "{:#x}",
                        u128::from(bits[1]) << 64 | u128::from(bits[0])
                    );
                }
                _ => _ = write!(self.out, "{}", bits[0]),
            },
            Operand::Parameter(index) => _ = write!(self.out, "arg{index}"),
            Operand::Global(index) => _ = write!(self.out, "global {index}"),
        }
    }

    fn write_place(&mut self, place: &Place) {
        match place.root {
            Root::Slot(slot) => self.write_slot(slot),
            Root::At(address) => {
                self.out.push_str("at ");
                self.write_operand(&address);
            }
        }
        if place.steps.is_empty() {
            return;
        }
        self.out.push('[');
        for (index, step) in place.steps.iter().enumerate() {
            if index > 0 {
                self.out.push_str(", ");
            }
            match *step {
                Step::Offset(bytes) => _ = write!(self.out, "+{bytes}"),
                Step::Through => self.out.push('*'),
                Step::Index { index, size } => {
                    self.write_operand(&index);
                    _ = write!(self.out, " * {size}");
                }
            }
        }
        self.out.push(']');
    }

    /// A slot as its name and index, `sum.2`; a temporary has no name.
    fn write_slot(&mut self, slot: Id<Slot>) {
        if !self.body.slots[slot].hidden {
            self.out
                .push_str(self.file.slice(self.body.slots.span(slot)));
        }
        _ = write!(self.out, ".{}", slot.index());
    }
}
