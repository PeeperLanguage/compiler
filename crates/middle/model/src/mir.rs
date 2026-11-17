//! The MIR of core concepts only (docs/feature-free-mir.md): one function as
//! a graph of blocks over registers of a machine class and slots of bytes.
//! No part of it names a language feature: a struct is a slot with offsets,
//! an interface value is two pointers, a method call is a load from a table
//! and a call through the pointer. Lowering writes it; the path checks, the
//! interpreter and the LLVM writer read it and need no type. Two things
//! only the source types give are the exceptions: the words of the loan
//! check's messages (`Body.returned`, `LoanWords` and `Moved.lent_again`),
//! and the debug types a debugger shows (`DebugTypes`).

use std::fmt::Write;

use crate::DeclarationCopy;
use arena::Arena;
use arena::Id;
use arena::List;
use arena::Lists;
use runtime_abi::symbols::Carrier;
use runtime_abi::symbols::Runtime;
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

    /// How many bits a number class holds.
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
    /// The name the dump uses: `i32`, `i200`, `f64`, `ptr`; LLVM's too, but
    /// for floats (`llvm::body::llvm_type`).
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
/// (one per `signature.parameters`): one passed in a register is stored from
/// `Operand::Parameter` as the body starts; one passed in memory is the
/// caller's copy, at the address it passed (`parameter_in_memory`). The
/// first block is the entry.
pub struct Body {
    pub blocks: Arena<Block>,             // with where each starts
    pub instructions: Arena<Instruction>, // with the code each comes from
    pub slots: Arena<Slot>,               // with each name, or the code a temporary holds
    /// How the function takes its parameters and gives its result. A result
    /// kept in memory is written to `Root::Result`.
    pub signature: Signature,
    /// How a loan message names what the function returns: "a reference
    /// into", "a slice of", "an interface holding"; `None` when its result
    /// cannot hold a loan.
    pub returned: Option<&'static str>,
    /// A block of the source, for a debugger: block `i` of the source is
    /// debug scope `i`.
    pub debug_scopes: Arena<DebugScope>,
    /// What a debugger shows the result as; `None` when there is none.
    pub result_debug_type: Option<DebugTypeId>,
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
    /// The pointers in it that may hold a loan.
    pub holders: Box<[Holder]>,
    /// What a debugger shows it as; `None` for a slot made for a value, not
    /// written, whose span is not a name.
    pub debug_type: Option<DebugTypeId>,
    pub debug_scope: Id<DebugScope>, // the block it is declared in
    pub mutable: bool,
    /// Set where declared; false for a `let` without a value, whose first
    /// store sets it.
    pub initialized: bool,
}

/// A pointer in a slot that may hold a loan: a reference's, a slice's, an
/// interface's.
#[derive(Clone, Copy, Debug)]
pub struct Holder {
    pub offset: u64,
    /// How a message names it ("reference used later here").
    pub words: &'static LoanWords,
}

/// The words a loan message names a borrow or a holder with, chosen by
/// lowering from the type: a reference's or a slice's.
#[derive(Debug)]
pub struct LoanWords {
    pub noun: &'static str,             // "reference made here"
    pub writable: &'static str,         // "a `&mut` argument"
    pub to: &'static str,               // "a reference to it"
    pub mutably_borrowed: &'static str, // "`x` is borrowed as `&mut`"
}

/// Those of a reference, an interface reference or an owned interface.
pub const REFERENCE_WORDS: LoanWords = LoanWords {
    noun: "reference",
    writable: "`&mut`",
    to: "to",
    mutably_borrowed: "borrowed as `&mut`",
};

/// Those of a slice.
pub const SLICE_WORDS: LoanWords = LoanWords {
    noun: "slice",
    writable: "`&mut []`",
    to: "of",
    mutably_borrowed: "borrowed as `&mut []`",
};

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
pub struct DebugTypeId(u32);

/// The debug types of a program, by id: lowering adds them, a backend
/// reads each (`get`).
#[derive(Clone, Default)]
pub struct DebugTypes {
    /// Each type, at its `DebugTypeId`; `None` while it is being made
    /// (`reserve`), as a struct's members may name the struct.
    types: Vec<Option<DebugType>>,
}

impl DebugTypes {
    pub fn get(&self, id: DebugTypeId) -> &DebugType {
        self.types[id.0 as usize]
            .as_ref()
            .expect("lowering defined each debug type it reserved")
    }

    /// Adds to `self`, a copy of `from` made earlier, the types `from` made
    /// since.
    pub fn copy_new(&mut self, from: &DebugTypes) {
        self.types
            .extend_from_slice(&from.types[self.types.len()..]);
    }

    /// Adds `debug_type`, defined now.
    pub fn push(&mut self, debug_type: DebugType) -> DebugTypeId {
        self.types.push(Some(debug_type));
        DebugTypeId((self.types.len() - 1) as u32)
    }

    /// A number for a debug type to be defined once its members are made
    /// (`define`), as a struct's members may name the struct.
    pub fn reserve(&mut self) -> DebugTypeId {
        self.types.push(None);
        DebugTypeId((self.types.len() - 1) as u32)
    }

    pub fn define(&mut self, id: DebugTypeId, debug_type: DebugType) {
        self.types[id.0 as usize] = Some(debug_type);
    }
}

/// Read-only data.
#[derive(Clone, Debug)]
pub enum Global {
    Bytes(Box<[u8]>),
    /// A table of function addresses, in order, each with how it is called:
    /// a module may hold a table of functions it never calls.
    Functions(Box<[(DeclarationCopy, Signature)]>),
}

/// How a function takes its parameters and gives its result.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Signature {
    pub parameters: Box<[Passing]>,
    pub result: Option<Passing>,
    /// For a call of a C function: how C passes each argument and the
    /// result, as clang does on the target. `None` in Peeper's own
    /// convention.
    pub c: Option<CSignature>,
    /// The positions of the parameters a returned pointer may point into,
    /// for the loan check.
    pub origins: Box<[u32]>,
}

/// How C passes the values of one call.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CSignature {
    /// The C function's name (`#[extern("name")]`).
    pub symbol: Box<str>,
    pub parameters: Box<[CPassing]>,
    pub result: Option<CPassing>,
}

/// How C passes one value, in LLVM's terms rather than one machine's, so
/// a 32-bit machine's rules are written as a 64-bit one's are. A value is
/// still a register of its class (`Passing`) before and after the call.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CPassing {
    /// As its register, widened by C as `extension` says, if at all.
    Register { extension: Option<Extension> },
    /// As `count` integers of `bits` bits each, the lowest first: x86-64's
    /// two halves of a 65- to 127-bit integer.
    Pieces { count: u32, bits: u32 },
    /// A result as a vector of `count` integers of `bits` bits: Windows
    /// x64's `__int128`.
    Vector { count: u32, bits: u32 },
    /// In `size` bytes aligned to `align`: a parameter copied onto the
    /// stack when `by_value`, else passed as a copy's address; a result
    /// where a hidden first argument points (`sret`).
    Memory {
        size: u64,
        align: u64,
        by_value: bool,
    },
}

/// How C widens an integer narrower than its register.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Extension {
    Sign,
    Zero,
}

impl std::fmt::Display for CPassing {
    /// The dump's text: `signext`, `2 x i64`, `memory 32 align 8 byval`.
    fn fmt(&self, out: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match *self {
            CPassing::Register { extension: None } => out.write_str("register"),
            CPassing::Register {
                extension: Some(Extension::Sign),
            } => out.write_str("signext"),
            CPassing::Register {
                extension: Some(Extension::Zero),
            } => out.write_str("zeroext"),
            CPassing::Pieces { count, bits } => write!(out, "{count} x i{bits}"),
            CPassing::Vector { count, bits } => write!(out, "<{count} x i{bits}>"),
            CPassing::Memory {
                size,
                align,
                by_value,
            } => {
                write!(out, "memory {size} align {align}")?;
                if by_value {
                    out.write_str(" byval")?;
                }
                Ok(())
            }
        }
    }
}

impl Signature {
    /// How the runtime function `runtime` takes and gives its values, on a
    /// target whose `usize` is of the class `usize`.
    pub fn of_runtime(runtime: Runtime, usize: Class) -> Signature {
        let passing = |carrier: &Carrier| match carrier {
            Carrier::I32 => Passing::Register {
                class: Class::I32,
                signed: true,
            },
            Carrier::Usize => Passing::Register {
                class: usize,
                signed: false,
            },
            Carrier::F64 => Passing::Register {
                class: Class::F64,
                signed: false,
            },
            Carrier::Pointer => Passing::Register {
                class: Class::Pointer,
                signed: false,
            },
        };
        let signature = runtime.signature();
        Signature {
            parameters: signature.parameters.iter().map(passing).collect(),
            result: signature.result.as_ref().map(passing),
            c: None,
            origins: Box::default(), // the runtime returns no pointer
        }
    }

    /// How many hidden arguments a call passes before the written ones:
    /// one, the address of a result kept in memory, which the called body
    /// names `Root::Result`.
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

impl Passing {
    /// The class of the register that carries it: its own, or the address
    /// of its memory.
    pub fn register_class(self) -> Class {
        match self {
            Passing::Register { class, .. } => class,
            Passing::Memory { .. } => Class::Pointer,
        }
    }
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
    /// `bits` read as the class: an integer's low bits, a float's bits,
    /// the low word first (`constant_value`); every bit above the class is
    /// zero. A wide integer's constant is read-only data, loaded (`Global`).
    Constant { class: Class, bits: [u64; 2] },
    /// The function's written parameter at this index, as passed in.
    Parameter(u32),
    /// The address of one of the body's `globals`.
    Global(u32),
}

impl Operand {
    /// `value`'s bits as a constant of `class`, which is not `Wide`:
    /// lowering keeps a wide constant as read-only data.
    pub fn integer(class: Class, value: u64) -> Operand {
        let fits = match class {
            Class::Wide(_) => unreachable!("a wide constant is read-only data"),
            Class::Pointer => value == 0, // only `null` is known while compiling
            class => class.bits() >= 64 || value >> class.bits() == 0,
        };
        assert!(fits, "a constant of {class} has no bit above it set");
        Operand::Constant {
            class,
            bits: [value, 0],
        }
    }
}

/// The value of a constant's `bits` (`Operand::Constant`).
pub fn constant_value(bits: [u64; 2]) -> u128 {
    u128::from(bits[1]) << 64 | u128::from(bits[0])
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

    pub fn as_place_ref(&self) -> PlaceRef<'_> {
        PlaceRef {
            root: self.root,
            steps: &self.steps,
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

/// A place, borrowed: a stored `Place`, or all of a slot, which a `Dead`
/// names without one.
#[derive(Clone, Copy)]
pub struct PlaceRef<'p> {
    pub root: Root,
    pub steps: &'p [Step],
}

impl PlaceRef<'_> {
    /// The slot when the place is all of its memory.
    pub fn whole_slot(self) -> Option<Id<Slot>> {
        match self.root {
            Root::Slot(slot) if self.steps.is_empty() => Some(slot),
            _ => None,
        }
    }

    /// Whether it is memory a pointer leads to, not a slot's own.
    pub fn is_behind_pointer(self) -> bool {
        matches!(self.root, Root::At(_) | Root::Result) || self.steps.contains(&Step::Through)
    }

    /// The values finding it reads: the address it starts at, and each
    /// index.
    pub fn operands(self) -> impl Iterator<Item = Operand> {
        let root = match self.root {
            Root::At(address) => Some(address),
            Root::Slot(_) | Root::Result => None,
        };
        let indexes = self.steps.iter().filter_map(|step| match *step {
            Step::Index { index, .. } => Some(index),
            Step::Offset(_) | Step::Through => None,
        });
        root.into_iter().chain(indexes)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Root {
    /// The slot's own memory.
    Slot(Id<Slot>),
    /// The memory at the address the operand gives.
    At(Operand),
    /// The memory the caller keeps the function's result in, when it is
    /// passed in memory (`Passing::Memory`).
    Result,
}

/// One step of a `Place`.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Step {
    /// This many bytes further in.
    Offset(u64),
    /// What the pointer here points to.
    Through,
    /// The element `index`, a pointer-wide integer that lowering checked
    /// to be inside, of `size` bytes each. `count` is how many elements
    /// there are from here, for the path checks; `None` when only the
    /// program knows (a slice's elements).
    Index {
        index: Operand,
        size: u64,
        count: Option<u64>,
    },
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

impl Comparison {
    /// `<` and `<=` of two integers, `signed` or not.
    pub fn integer_less(signed: bool) -> (Comparison, Comparison) {
        if signed {
            (Comparison::SignedLess, Comparison::SignedLessOrEqual)
        } else {
            (Comparison::UnsignedLess, Comparison::UnsignedLessOrEqual)
        }
    }
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
    /// The place's address: a pointer that holds no loan. `size` is how
    /// many bytes are there, for the path checks.
    Address {
        place: Place,
        size: u64,
    },
    /// A call, with the callee's signature (`Body::signatures`). A result
    /// kept in memory is written to `result`'s place, of its size in bytes:
    /// the call passes its address as a hidden first argument. The size is
    /// the signature's, kept here as `Address` keeps its own, because
    /// `accesses` reads an instruction without its body.
    Call {
        callee: Callee,
        signature: u32,
        arguments: Box<[Operand]>,
        result: Option<(Place, u64)>,
    },
    /// Stops the program with "panic: `message`" when `failed`, an `I8`, is
    /// not 0. Gives no value.
    Check {
        failed: Operand,
        message: Box<str>,
    },
    /// A reference to the place, `&mut` when `mutable`: a loan. Its value
    /// is the place's address. `size` is how many bytes from the place
    /// the loan covers, `None` for all there are, which only the program
    /// knows (a slice's elements). `words` are how a message names it.
    Borrow {
        place: Place,
        size: Option<u64>,
        mutable: bool,
        words: &'static LoanWords,
    },
    /// The value of `size` bytes at the place, just read, is moved out.
    /// `lent_again` is the step a message adds to the place to lend it
    /// again instead (`&mut x.*`, `&mut x[..]`), for a value that can be.
    Moved {
        place: Place,
        size: u64,
        lent_again: Option<&'static str>,
    },
    /// The slot's storage ends: its block ends, or a jump leaves it.
    Dead(Id<Slot>),
}

/// Who a `Call` calls.
#[derive(Clone, Copy, Debug)]
pub enum Callee {
    /// A function of the program, or a copy of one; an `extern` function
    /// has no body.
    Function(DeclarationCopy),
    /// The function whose address the operand gives.
    Pointer(Operand),
}

/// A place an instruction touches (`InstructionKind::accesses`).
pub struct PlaceAccess<'p> {
    pub place: PlaceRef<'p>,
    pub kind: AccessKind,
    touched: AccessWidth,
}

/// How many bytes an access touches: a register's, or a size.
#[derive(Clone, Copy)]
enum AccessWidth {
    Register(Class),
    Bytes(Option<u64>), // `None`: all there are
}

impl PlaceAccess<'_> {
    /// How many bytes from the place it touches, on a target whose pointers
    /// are `pointer_bytes` wide; `None` for all there are.
    #[inline]
    pub fn width(&self, pointer_bytes: u64) -> Option<u64> {
        match self.touched {
            AccessWidth::Register(class) => Some(class.bytes(pointer_bytes)),
            AccessWidth::Bytes(size) => size,
        }
    }
}

/// What an instruction does to a place it touches.
#[derive(Clone, Copy)]
pub enum AccessKind {
    Read,
    Write,
    Borrow {
        mutable: bool,
    },
    /// Takes its address, which holds no loan.
    Address,
    /// Moves out the value just read there (`Moved`).
    Move,
    /// Ends the storage of all of a slot (`Dead`).
    End,
}

impl InstructionKind {
    /// Gives `each` the places it touches, a copy's read first. A function,
    /// not a list: the path checks ask for every line, so it compiles to a
    /// `match`.
    #[inline]
    pub fn accesses<'p>(&'p self, mut each: impl FnMut(PlaceAccess<'p>)) {
        let mut on = |place: &'p Place, touched, kind| {
            let place = place.as_place_ref();
            each(PlaceAccess {
                place,
                kind,
                touched,
            });
        };
        let bytes = |size: &u64| AccessWidth::Bytes(Some(*size));
        match self {
            InstructionKind::Load { class, place } => {
                on(place, AccessWidth::Register(*class), AccessKind::Read);
            }
            InstructionKind::Store { class, place, .. } => {
                on(place, AccessWidth::Register(*class), AccessKind::Write);
            }
            InstructionKind::Copy { to, from, size } => {
                on(from, bytes(size), AccessKind::Read);
                on(to, bytes(size), AccessKind::Write);
            }
            InstructionKind::Borrow {
                place,
                size,
                mutable,
                ..
            } => {
                let kind = AccessKind::Borrow { mutable: *mutable };
                on(place, AccessWidth::Bytes(*size), kind);
            }
            InstructionKind::Address { place, size } => on(place, bytes(size), AccessKind::Address),
            InstructionKind::Call {
                result: Some((place, size)),
                ..
            } => on(place, bytes(size), AccessKind::Write),
            InstructionKind::Moved { place, size, .. } => on(place, bytes(size), AccessKind::Move),
            InstructionKind::Integer { .. }
            | InstructionKind::Float { .. }
            | InstructionKind::Compare { .. }
            | InstructionKind::Convert { .. }
            | InstructionKind::Call { .. }
            | InstructionKind::Check { .. } => {}
            InstructionKind::Dead(slot) => each(PlaceAccess {
                place: PlaceRef {
                    root: Root::Slot(*slot),
                    steps: &[],
                },
                kind: AccessKind::End,
                touched: AccessWidth::Bytes(None),
            }),
        }
    }

    /// Gives `each` the values it reads as its operands, a call's callee
    /// first; not those a place it touches reads (`PlaceRef::operands`).
    #[inline]
    pub fn operands(&self, mut each: impl FnMut(Operand)) {
        match self {
            InstructionKind::Integer { left, right, .. }
            | InstructionKind::Float { left, right, .. }
            | InstructionKind::Compare { left, right, .. } => {
                each(*left);
                each(*right);
            }
            InstructionKind::Convert { value, .. } | InstructionKind::Store { value, .. } => {
                each(*value);
            }
            InstructionKind::Check { failed, .. } => each(*failed),
            InstructionKind::Call {
                callee, arguments, ..
            } => {
                if let Callee::Pointer(pointer) = callee {
                    each(*pointer);
                }
                arguments.iter().copied().for_each(each);
            }
            InstructionKind::Load { .. }
            | InstructionKind::Copy { .. }
            | InstructionKind::Address { .. }
            | InstructionKind::Borrow { .. }
            | InstructionKind::Moved { .. }
            | InstructionKind::Dead(_) => {}
        }
    }

    /// Whether it is code a compiled program runs: all but `Moved` and
    /// `Dead`, which only mark a point for the path checks.
    pub fn runs(&self) -> bool {
        !matches!(
            self,
            InstructionKind::Moved { .. } | InstructionKind::Dead(_)
        )
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
    /// `panic message` at `span`: writes `message` and where, then ends
    /// the program.
    Panic {
        message: Box<[u8]>,
        span: Span,
    },
    /// The function's closing brace, reached without a `return`.
    FallOff(Span),
}

impl Terminator {
    /// The blocks control can go to next.
    pub fn targets(&self) -> impl Iterator<Item = Id<Block>> {
        let targets = match *self {
            Terminator::Jump(target) => [Some(target), None],
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
    /// Which blocks control can reach from the entry, by index.
    pub fn reachable(&self) -> Vec<bool> {
        let mut reachable = vec![false; self.blocks.len()];
        self.mark_reachable(Id::from_index(0), &mut reachable, |_| {});
        reachable
    }

    /// Marks in `marked` `from` and every block control can reach from it,
    /// stopping at blocks already marked, and gives each it marks to
    /// `newly_marked`.
    pub fn mark_reachable(
        &self,
        from: Id<Block>,
        marked: &mut [bool],
        mut newly_marked: impl FnMut(Id<Block>),
    ) {
        let mut pending = vec![from];
        while let Some(block) = pending.pop() {
            if !std::mem::replace(&mut marked[block.index()], true) {
                newly_marked(block);
                pending.extend(self.blocks[block].terminator.targets());
            }
        }
    }

    /// Each function the body may run: those it calls, and those in its
    /// tables, which a method call reaches through an interface value.
    pub fn named_functions(&self) -> impl Iterator<Item = DeclarationCopy> {
        let called = self
            .instructions
            .ids()
            .filter_map(|id| match self.instructions[id].kind {
                InstructionKind::Call {
                    callee: Callee::Function(function),
                    ..
                } => Some(function),
                _ => None,
            });
        let tabled = (self.globals.iter()).flat_map(|global| match global {
            Global::Functions(entries) => &entries[..],
            Global::Bytes(_) => &[],
        });
        called.chain(tabled.map(|&(function, _)| function))
    }

    /// The parameter, as an `Operand::Parameter` index, whose address the
    /// slot `slot` is: a parameter passed in memory is the caller's private
    /// copy, so it has no memory of its own.
    pub fn parameter_in_memory(&self, slot: Id<Slot>) -> Option<u32> {
        let index = slot.index();
        match self.signature.parameters.get(index)? {
            Passing::Memory { .. } => Some(index as u32),
            Passing::Register { .. } => None,
        }
    }

    /// The class of the register `operand` is; `None` for an instruction
    /// that gives no register: a store, copy, check, `Moved`, `Dead`, or a
    /// call that gives nothing or gives its result in memory.
    pub fn class_of(&self, operand: Operand) -> Option<Class> {
        match operand {
            Operand::Value(id) => match &self.instructions[id].kind {
                InstructionKind::Call { signature, .. } => {
                    match self.signatures[*signature as usize].result {
                        Some(Passing::Register { class, .. }) => Some(class),
                        Some(Passing::Memory { .. }) | None => None,
                    }
                }
                InstructionKind::Integer { class, .. }
                | InstructionKind::Float { class, .. }
                | InstructionKind::Load { class, .. } => Some(*class),
                InstructionKind::Compare { .. } => Some(Class::I8),
                InstructionKind::Convert { to, .. } => Some(*to),
                InstructionKind::Address { .. } | InstructionKind::Borrow { .. } => {
                    Some(Class::Pointer)
                }
                InstructionKind::Store { .. }
                | InstructionKind::Copy { .. }
                | InstructionKind::Check { .. }
                | InstructionKind::Moved { .. }
                | InstructionKind::Dead(_) => None,
            },
            Operand::Constant { class, .. } => Some(class),
            Operand::Parameter(index) => {
                Some(self.signature.parameters[index as usize].register_class())
            }
            Operand::Global(_) => Some(Class::Pointer),
        }
    }

    /// The body as text, one block after another, for `peeper -dump`.
    /// `file` is where the body is written; `name_of` gives a function's
    /// name. A slot shows as its name and index (`sum.2`, `.5` for a
    /// temporary), a place as its root then `[+8]`, `[*]` or `[%3 * 4]`.
    /// The globals follow the blocks: bytes as text, a table by its
    /// functions' names.
    pub fn text(&self, file: &SourceFile, name_of: impl Fn(DeclarationCopy) -> String) -> String {
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
        for (index, global) in self.globals.iter().enumerate() {
            _ = write!(printer.out, "  global {index} = ");
            match global {
                Global::Bytes(bytes) => _ = writeln!(printer.out, "\"{}\"", bytes.escape_ascii()),
                Global::Functions(entries) => {
                    let names: Vec<String> =
                        entries.iter().map(|&(copy, _)| name_of(copy)).collect();
                    _ = writeln!(printer.out, "table [{}]", names.join(", "));
                }
            }
        }
        printer.out
    }
}

/// What `Body::text` writes with.
struct Printer<'b> {
    body: &'b Body,
    file: &'b SourceFile,
    name_of: &'b dyn Fn(DeclarationCopy) -> String,
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
            InstructionKind::Address { place, size } => {
                _ = write!(self.out, "address {size} ");
                self.write_place(place);
            }
            InstructionKind::Call {
                callee,
                signature,
                arguments,
                result,
            } => {
                match callee {
                    Callee::Function(function) => {
                        _ = write!(self.out, "call {}(", (self.name_of)(*function));
                    }
                    Callee::Pointer(pointer) => {
                        self.out.push_str("call through ");
                        self.write_operand(pointer);
                        self.out.push('(');
                    }
                }
                self.write_operands(arguments);
                self.out.push(')');
                if let Some((place, size)) = result {
                    _ = write!(self.out, " into {size} ");
                    self.write_place(place);
                }
                if let Some(c) = &self.body.signatures[*signature as usize].c {
                    self.out.push_str(" in C (");
                    for (index, passing) in c.parameters.iter().enumerate() {
                        let separator = if index > 0 { ", " } else { "" };
                        _ = write!(self.out, "{separator}{passing}");
                    }
                    self.out.push(')');
                    if let Some(result) = &c.result {
                        _ = write!(self.out, " -> {result}");
                    }
                }
            }
            InstructionKind::Check { failed, message } => {
                self.out.push_str("check ");
                self.write_operand(failed);
                _ = write!(self.out, ", \"{message}\"");
            }
            InstructionKind::Borrow {
                place,
                size,
                mutable,
                words,
            } => {
                let mutable = if *mutable { " mut" } else { "" };
                _ = write!(self.out, "borrow{mutable} ");
                if let Some(size) = size {
                    _ = write!(self.out, "{size} ");
                }
                self.write_place(place);
                _ = write!(self.out, " as {}", words.noun);
            }
            InstructionKind::Moved {
                place,
                size,
                lent_again,
            } => {
                _ = write!(self.out, "moved {size} ");
                self.write_place(place);
                if let Some(step) = lent_again {
                    _ = write!(self.out, " lent again with {step}");
                }
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
                _ = write!(self.out, "panic \"{}\"", message.escape_ascii());
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
                Class::I128 if bits[1] != 0 => _ = write!(self.out, "{:#x}", constant_value(bits)),
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
            Root::Result => self.out.push_str("result"),
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
                Step::Index { index, size, count } => {
                    self.write_operand(&index);
                    _ = write!(self.out, " * {size}");
                    if let Some(count) = count {
                        _ = write!(self.out, " of {count}");
                    }
                }
            }
        }
        self.out.push(']');
    }

    /// A slot as its name and index, `sum.2`; a temporary has no name.
    fn write_slot(&mut self, slot: Id<Slot>) {
        if self.body.slots[slot].debug_type.is_some() {
            self.out
                .push_str(self.file.slice(self.body.slots.span(slot)));
        }
        _ = write!(self.out, ".{}", slot.index());
    }
}
