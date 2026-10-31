//! Writing operators and the checks they need: binary operators, the
//! traps of division and shifts, and the panic a failed check jumps to.

use std::fmt::Write;
use std::path::Path;

use arena::Id;
use eval::messages::trap_message;
use eval::operators::ValueError;
use model::constant::Value;
use model::mir;
use model::mir::Operand;
use model::operators::BinaryOp;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use source::Span;

use crate::function::EXIT;
use crate::function::Function;
use crate::function::PANIC_EXIT_CODE;
use crate::function::WRITE;
use crate::function::global;
use crate::function::traps;
use crate::function::value_name;
use crate::text::type_text;

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
        let checks = traps(self.body, self.types, id) > 0;
        // The value the right operand was converted to, if it was.
        let mut converted: Option<String> = None;
        let instruction = match self.types[left_type] {
            Type::Float(_) => match op {
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
            },
            ref integer => {
                let signed = matches!(integer, Type::Integer(integer) if integer.signed);
                match op {
                    BinaryOp::Divide | BinaryOp::Remainder => {
                        if checks {
                            let zero = format!("{name}.z");
                            _ = writeln!(out, "  {zero} = icmp eq {ty} {}, 0", self.operand(right));
                            let message = trap_message(ValueError::DivisionByZero, integer);
                            self.check(out, &zero, &message, id);
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
                        match (op, signed) {
                            (BinaryOp::Divide, true) => "sdiv",
                            (BinaryOp::Divide, false) => "udiv",
                            (_, true) => "srem",
                            (_, false) => "urem",
                        }
                    }
                    BinaryOp::ShiftLeft | BinaryOp::ShiftRight => {
                        converted = self.shift_count(out, id, right, left_type, checks);
                        match (op, signed) {
                            (BinaryOp::ShiftLeft, _) => "shl",
                            (_, true) => "ashr",
                            (_, false) => "lshr",
                        }
                    }
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
        let Operand::Constant(constant) = operand else {
            return false;
        };
        let constant = &self.body.constants[constant.index()];
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
            let message = trap_message(ValueError::ShiftOutOfRange, &self.types[shifted]);
            self.check(out, &too_far, &message, id);
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
        let file = self.module.file();
        let (line, column) = file.position(span.start as usize);
        let mut text = b"panic: ".to_vec();
        text.extend_from_slice(message);
        let path = Path::new(&file.path);
        let path = path.strip_prefix(&self.module.program.root).unwrap_or(path);
        let place = format!("\n  --> {}:{line}:{column}\n", path.display());
        text.extend_from_slice(place.as_bytes());
        let string = self.module.add_string(&text);
        self.module.declare(WRITE, || {
            format!("declare i32 {}(i32, ptr, i32)\n\n", global(WRITE))
        });
        self.module
            .declare(EXIT, || format!("declare void {}(i32)\n\n", global(EXIT)));
        _ = write!(
            out,
            "  call i32 {}(i32 2, ptr {string}, i32 {}){location}\n  \
             call void {}(i32 {PANIC_EXIT_CODE}){location}\n  unreachable",
            global(WRITE),
            text.len(),
            global(EXIT),
        );
    }
}
