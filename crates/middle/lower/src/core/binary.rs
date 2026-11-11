//! What a binary operator on two numbers becomes in the MIR of core
//! concepts. One rule: lowering follows it, and eval's LLVM parity test
//! runs it against the interpreter's arithmetic.

use model::core_mir::Comparison;
use model::core_mir::FloatOp;
use model::core_mir::IntegerOp;
use model::operators::BinaryOp;
use model::semantic_types::Integer;
use model::semantic_types::Type;

/// The MIR operation of a binary operator on two numbers.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BinaryOperation {
    Integer(IntegerOp),
    Float(FloatOp),
    /// A comparison; `swapped` when it compares the right side with the
    /// left, as `>` and `>=` are `<` and `<=` the other way round.
    Compare {
        comparison: Comparison,
        swapped: bool,
    },
}

/// The MIR operation of `op` on two values of the number type `number`.
pub fn binary_operation(op: BinaryOp, number: &Type) -> BinaryOperation {
    let (float, signed) = match *number {
        Type::Float(_) => (true, false),
        Type::Integer(Integer { signed, .. }) => (false, signed),
        _ => unreachable!("only numbers have arithmetic"),
    };
    let (less, less_or_equal) = match (float, signed) {
        (true, _) => (Comparison::FloatLess, Comparison::FloatLessOrEqual),
        (false, true) => (Comparison::SignedLess, Comparison::SignedLessOrEqual),
        (false, false) => (Comparison::UnsignedLess, Comparison::UnsignedLessOrEqual),
    };
    let compare = |comparison, swapped| BinaryOperation::Compare {
        comparison,
        swapped,
    };
    let integer = BinaryOperation::Integer;
    let float_operation = BinaryOperation::Float;
    match (op, float) {
        (BinaryOp::EqualTo, _) => compare(Comparison::Equal, false),
        (BinaryOp::NotEqualTo, _) => compare(Comparison::NotEqual, false),
        (BinaryOp::LessThan, _) => compare(less, false),
        (BinaryOp::LessOrEqual, _) => compare(less_or_equal, false),
        (BinaryOp::GreaterThan, _) => compare(less, true),
        (BinaryOp::GreaterOrEqual, _) => compare(less_or_equal, true),
        (BinaryOp::Add, true) => float_operation(FloatOp::Add),
        (BinaryOp::Subtract, true) => float_operation(FloatOp::Subtract),
        (BinaryOp::Multiply, true) => float_operation(FloatOp::Multiply),
        (BinaryOp::Divide, true) => float_operation(FloatOp::Divide),
        (BinaryOp::Remainder, true) => float_operation(FloatOp::Remainder),
        (BinaryOp::Add, false) => integer(IntegerOp::Add),
        (BinaryOp::Subtract, false) => integer(IntegerOp::Subtract),
        (BinaryOp::Multiply, false) => integer(IntegerOp::Multiply),
        (BinaryOp::Divide, false) if signed => integer(IntegerOp::SignedDivide),
        (BinaryOp::Divide, false) => integer(IntegerOp::UnsignedDivide),
        (BinaryOp::Remainder, false) if signed => integer(IntegerOp::SignedRemainder),
        (BinaryOp::Remainder, false) => integer(IntegerOp::UnsignedRemainder),
        (BinaryOp::ShiftLeft, false) => integer(IntegerOp::ShiftLeft),
        (BinaryOp::ShiftRight, false) if signed => integer(IntegerOp::SignedShiftRight),
        (BinaryOp::ShiftRight, false) => integer(IntegerOp::UnsignedShiftRight),
        (BinaryOp::BitAnd, false) => integer(IntegerOp::And),
        (BinaryOp::BitOr, false) => integer(IntegerOp::Or),
        (BinaryOp::BitXor, false) => integer(IntegerOp::Xor),
        _ => unreachable!("the checker allows no other operator on numbers"),
    }
}
