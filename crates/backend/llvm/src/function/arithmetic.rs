//! Writing operators and the checks they need: binary operators, the
//! traps of division and shifts, and the panic a failed check jumps to.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;

use arena::Id;
use model::constant::Value;
use model::mir;
use model::mir::Operand;
use model::operators::BinaryOp;
use model::operators::UnaryOp;
use model::semantic_types::Float;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use runtime_abi::abi::PANIC_EXIT_CODE;
use runtime_abi::abi::STDERR;
use runtime_abi::messages::DIVISION_BY_ZERO;
use runtime_abi::messages::panic_text;
use runtime_abi::messages::shift_count_message;
use runtime_abi::symbols::PANIC_EXIT;
use runtime_abi::symbols::PANIC_WRITE;
use source::Span;

use crate::function::Function;
use crate::function::traps;
use crate::function::value_name;
use crate::text::type_text;

/// The LLVM instruction of `op` on two operands of the type `operands`: a
/// float, an integer, or a `bool`, an `i1`. The interpreter must give what
/// each gives (eval's `llvm_parity` test runs both).
pub fn binary_instruction(op: BinaryOp, operands: &Type) -> &'static str {
    let signed = matches!(operands, Type::Integer(integer) if integer.signed);
    if let Type::Float(_) = operands {
        return match op {
            BinaryOp::Add => "fadd",
            BinaryOp::Subtract => "fsub",
            BinaryOp::Multiply => "fmul",
            BinaryOp::Divide => "fdiv",
            BinaryOp::Remainder => "frem",
            BinaryOp::EqualTo => "fcmp oeq",
            BinaryOp::NotEqualTo => "fcmp une",
            BinaryOp::LessThan => "fcmp olt",
            BinaryOp::LessOrEqual => "fcmp ole",
            BinaryOp::GreaterThan => "fcmp ogt",
            BinaryOp::GreaterOrEqual => "fcmp oge",
            _ => unreachable!("check accepts no {op:?} of floats"),
        };
    }
    match op {
        BinaryOp::Divide if signed => "sdiv",
        BinaryOp::Divide => "udiv",
        BinaryOp::Remainder if signed => "srem",
        BinaryOp::Remainder => "urem",
        BinaryOp::ShiftLeft => "shl",
        BinaryOp::ShiftRight if signed => "ashr",
        BinaryOp::ShiftRight => "lshr",
        BinaryOp::Add => "add",
        BinaryOp::Subtract => "sub",
        BinaryOp::Multiply => "mul",
        BinaryOp::BitAnd | BinaryOp::LogicalAnd => "and",
        BinaryOp::BitOr | BinaryOp::LogicalOr => "or",
        BinaryOp::BitXor => "xor",
        BinaryOp::EqualTo => "icmp eq",
        BinaryOp::NotEqualTo => "icmp ne",
        BinaryOp::LessThan if signed => "icmp slt",
        BinaryOp::LessThan => "icmp ult",
        BinaryOp::LessOrEqual if signed => "icmp sle",
        BinaryOp::LessOrEqual => "icmp ule",
        BinaryOp::GreaterThan if signed => "icmp sgt",
        BinaryOp::GreaterThan => "icmp ugt",
        BinaryOp::GreaterOrEqual if signed => "icmp sge",
        BinaryOp::GreaterOrEqual => "icmp uge",
    }
}

/// Writes the LLVM instruction of `op` on `value`, of the type `operand`
/// written `text`, after its `{name} = `. The interpreter must give what it
/// gives (eval's `llvm_parity` test runs both).
pub fn write_unary(
    out: &mut impl Write,
    op: UnaryOp,
    operand: &Type,
    text: impl Display,
    value: impl Display,
) -> fmt::Result {
    match (op, operand) {
        (UnaryOp::Negate, Type::Float(_)) => write!(out, "fneg {text} {value}"),
        (UnaryOp::Negate, _) => write!(out, "sub {text} 0, {value}"),
        (UnaryOp::LogicalNot, _) => write!(out, "xor {text} {value}, true"),
        (UnaryOp::BitNot, _) => write!(out, "xor {text} {value}, -1"),
    }
}

/// The LLVM cast from a value of the type `from` to the type `to`: numbers,
/// `bool` (an `i1`, a 1-bit unsigned integer) and raw pointers. A float
/// becomes an integer through `saturating_conversion` instead. The
/// interpreter must give what it gives (eval's `llvm_parity` test runs both).
pub fn cast_instruction(from: &Type, to: &Type) -> &'static str {
    let integer = |ty: &Type| match *ty {
        Type::Integer(integer) => Some(integer),
        Type::Bool => Some(Integer {
            signed: false,
            bits: 1,
        }),
        _ => None,
    };
    match (integer(from), integer(to), from) {
        (Some(from), Some(to), _) if to.bits > from.bits && from.signed => "sext",
        (Some(from), Some(to), _) if to.bits > from.bits => "zext",
        (Some(from), Some(to), _) if to.bits < from.bits => "trunc",
        // The same bits, read with the other sign.
        (Some(_), Some(_), _) => "bitcast",
        (Some(from), None, _) if from.signed => "sitofp",
        (Some(_), None, _) => "uitofp",
        // A pointer cast keeps the address.
        (None, None, Type::RawPointer { .. }) => "bitcast",
        (None, None, Type::Float(Float::F32)) => "fpext",
        (None, None, _) => "fptrunc",
        (None, Some(_), _) => {
            unreachable!("a float becomes an integer by `saturating_conversion`")
        }
    }
}

/// The LLVM intrinsic that turns a `from` float into an integer, `signed`
/// or not, of the LLVM type `to_text`, saturating as check folds it: cut
/// toward zero, the nearest end of the integer's range outside it, 0 for NaN.
pub fn saturating_conversion(from: Float, signed: bool, to_text: impl Display) -> String {
    let sign = if signed { 's' } else { 'u' };
    let suffix = match from {
        Float::F32 => "f32",
        Float::F64 => "f64",
    };
    format!("llvm.fpto{sign}i.sat.{to_text}.{suffix}")
}

impl Function<'_, '_, '_> {
    /// Writes the line computing the binary operation `id`, after the checks
    /// it needs first.
    pub(super) fn write_binary(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        op: BinaryOp,
        left: Operand,
        right: Operand,
    ) {
        let name = value_name(id);
        let left_type = self.body.operand_type(left);
        let ty = type_text(self.types, left_type);
        let checks = traps(self.body, self.types, id);
        // The value the right operand was converted to, if it was.
        let mut converted: Option<String> = None;
        let operands = &self.types[left_type];
        let instruction = match operands {
            Type::Float(_) => binary_instruction(op, operands),
            // Not `inbounds`: a raw pointer may point anywhere.
            Type::RawPointer { .. } if op == BinaryOp::Add => {
                let bits = self.module.target.pointer_bits;
                let (address, offset) = (self.operand(left), self.operand(right));
                _ = write!(
                    out,
                    "  {name} = getelementptr i8, ptr {address}, i{bits} {offset}"
                );
                return;
            }
            integer => {
                let signed = matches!(integer, Type::Integer(integer) if integer.signed);
                match op {
                    BinaryOp::Divide | BinaryOp::Remainder => {
                        if checks {
                            let zero = format!("{name}.z");
                            _ = writeln!(out, "  {zero} = icmp eq {ty} {}, 0", self.operand(right));
                            self.check(out, &zero, DIVISION_BY_ZERO, id);
                        }
                        let (l, r) = (self.operand(left), self.operand(right));
                        if signed && (checks || self.is_minus_one(right)) {
                            // MIN / -1 is MIN and MIN % -1 is 0, as check
                            // folds them: divide by 1 instead and negate.
                            let minus_one = format!("{name}.n");
                            let divisor = format!("{name}.d");
                            _ = writeln!(
                                out,
                                "  {minus_one} = icmp eq {ty} {r}, -1\n  \
                                 {divisor} = select i1 {minus_one}, {ty} 1, {ty} {r}"
                            );
                            if op == BinaryOp::Divide {
                                let quotient = format!("{name}.q");
                                let negated = format!("{name}.m");
                                _ = write!(
                                    out,
                                    "  {quotient} = sdiv {ty} {l}, {divisor}\n  \
                                     {negated} = sub {ty} 0, {l}\n  \
                                     {name} = select i1 {minus_one}, {ty} {negated}, {ty} {quotient}"
                                );
                                return;
                            }
                            converted = Some(divisor);
                        }
                    }
                    BinaryOp::ShiftLeft | BinaryOp::ShiftRight => {
                        converted = self.shift_count(out, id, right, left_type, checks);
                    }
                    _ => {}
                }
                binary_instruction(op, integer)
            }
        };
        _ = write!(
            out,
            "  {name} = {instruction} {ty} {}, ",
            self.operand(left)
        );
        _ = match converted {
            Some(converted) => write!(out, "{converted}"),
            None => write!(out, "{}", self.operand(right)),
        };
    }

    /// Whether `operand` is the constant -1: every bit of its type set.
    pub(super) fn is_minus_one(&self, operand: Operand) -> bool {
        let Some(constant) = self.body.constant(operand) else {
            return false;
        };
        let (Value::Integer(limbs), Type::Integer(integer)) =
            (&constant.value, &self.types[constant.ty])
        else {
            return false;
        };
        let ones: u32 = limbs.as_slice().iter().map(|limb| limb.count_ones()).sum();
        ones == integer.bits
    }

    /// The count of a shift of a `shifted` value, converted to that type:
    /// the name of the converted value, or `None` when the count has that
    /// type already. A count not known at compile time is checked first to be
    /// from 0 to the width less one; the check compares at 32 bits or more,
    /// where the width fits and a negative count reads as too big
    /// (docs/ir-design.md, rule 4).
    pub(super) fn shift_count(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        count: Operand,
        shifted: TypeId,
        checks: bool,
    ) -> Option<String> {
        let (Type::Integer(shifted_type), Type::Integer(count_type)) = (
            &self.types[shifted],
            &self.types[self.body.operand_type(count)],
        ) else {
            unreachable!("check accepts shifts of integers by integers only");
        };
        let (width, count_bits) = (shifted_type.bits, count_type.bits);
        let name = value_name(id);
        if checks {
            let too_far = format!("{name}.c");
            if count_bits < 32 {
                let conversion = if count_type.signed { "sext" } else { "zext" };
                _ = writeln!(
                    out,
                    "  {name}.e = {conversion} i{count_bits} {} to i32\n  \
                     {too_far} = icmp uge i32 {name}.e, {width}",
                    self.operand(count)
                );
            } else {
                _ = writeln!(
                    out,
                    "  {too_far} = icmp uge i{count_bits} {}, {width}",
                    self.operand(count)
                );
            }
            self.check(out, &too_far, &shift_count_message(width), id);
        }
        if count_bits == width {
            return None;
        }
        let conversion = if count_bits < width { "zext" } else { "trunc" };
        _ = writeln!(
            out,
            "  {name}.s = {conversion} i{count_bits} {} to i{width}",
            self.operand(count)
        );
        Some(format!("{name}.s"))
    }

    /// Goes on in the next block of the MIR block being written when
    /// `failed` is false, else panics (`write_panic`) with `message` where
    /// the instruction `id` is written.
    pub(super) fn check(
        &mut self,
        out: &mut String,
        failed: &str,
        message: &str,
        id: Id<mir::Instruction>,
    ) {
        self.current.checked += 1;
        let next = format!("bb{}.{}", self.current.block, self.current.checked);
        let panic = format!("{next}.panic");
        _ = writeln!(
            out,
            "  br i1 {failed}, label %{panic}, label %{next}\n{next}:"
        );
        let span = self.body.instructions.span(id);
        let debug_scope = self.body.instructions[id].debug_scope;
        let mut panics = std::mem::take(&mut self.panics);
        _ = writeln!(panics, "{panic}:");
        self.write_panic(&mut panics, message.as_bytes(), span, debug_scope);
        panics.push('\n');
        self.panics = panics;
    }

    /// Writes the calls that write `message` and where `span` is to the
    /// standard error and exit with `PANIC_EXIT_CODE`, then `unreachable`,
    /// with no newline after it. The text is known at compile time, so it is
    /// one string constant.
    pub(super) fn write_panic(
        &mut self,
        out: &mut String,
        message: &[u8],
        span: Span,
        debug_scope: Id<mir::DebugScope>,
    ) {
        let location = self.location(span, debug_scope);
        let file = self.file;
        let text = panic_text(&self.module.program.root, file, message, span);
        let string = self.module.add_string(&text);
        let (stderr, length, code) = (
            STDERR.to_string(),
            text.len().to_string(),
            PANIC_EXIT_CODE.to_string(),
        );
        self.write_runtime_call(out, &PANIC_WRITE, &[&stderr, &string, &length], &location);
        self.write_runtime_call(out, &PANIC_EXIT, &[&code], &location);
        out.push_str("  unreachable");
    }
}
