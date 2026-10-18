//! Folding in `ir` must give the values LLVM computes at run time, for every
//! integer width and both float precisions. This test runs thousands of
//! operations both ways: `ir` folds them, `lli` runs them on values it cannot
//! fold (volatile loads), and the bits must match; a NaN matches any NaN, as
//! LLVM does not specify which NaN an operation gives. Cases the runtime traps
//! on (division by zero, out-of-range shifts) or guards (`MIN / -1`) are left
//! out; the unit tests in constant.rs cover them.
//!
//! Needs `lli` on the path: `cargo test -p ir -- --ignored`.

use std::fmt::Write;
use std::process::Command;

use bigint::Limbs;
use model::BinaryOp;
use model::UnaryOp;
use model::constant::Value;
use model::constant::binary;
use model::constant::unary;
use model::semantic_types::Float;
use model::semantic_types::Integer;
use model::semantic_types::Type;

const WIDTHS: &[u32] = &[
    1, 2, 3, 5, 7, 8, 9, 15, 16, 17, 24, 31, 32, 33, 63, 64, 65, 100, 127, 128, 129, 192, 200, 255,
    256, 257, 511, 1000,
];

/// Each float operator with its LLVM instruction.
const FLOAT_BINARY: &[(BinaryOp, &str)] = &[
    (BinaryOp::Add, "fadd"),
    (BinaryOp::Subtract, "fsub"),
    (BinaryOp::Multiply, "fmul"),
    (BinaryOp::Divide, "fdiv"),
    (BinaryOp::Remainder, "frem"),
    (BinaryOp::EqualTo, "fcmp oeq"),
    (BinaryOp::NotEqualTo, "fcmp une"),
    (BinaryOp::LessThan, "fcmp olt"),
    (BinaryOp::LessOrEqual, "fcmp ole"),
    (BinaryOp::GreaterThan, "fcmp ogt"),
    (BinaryOp::GreaterOrEqual, "fcmp oge"),
];

/// Each integer operator with its LLVM instruction for unsigned and for signed
/// operands.
const BINARY: &[(BinaryOp, &str, &str)] = &[
    (BinaryOp::Add, "add", "add"),
    (BinaryOp::Subtract, "sub", "sub"),
    (BinaryOp::Multiply, "mul", "mul"),
    (BinaryOp::Divide, "udiv", "sdiv"),
    (BinaryOp::Remainder, "urem", "srem"),
    (BinaryOp::ShiftLeft, "shl", "shl"),
    (BinaryOp::ShiftRight, "lshr", "ashr"),
    (BinaryOp::BitAnd, "and", "and"),
    (BinaryOp::BitOr, "or", "or"),
    (BinaryOp::BitXor, "xor", "xor"),
    (BinaryOp::EqualTo, "icmp eq", "icmp eq"),
    (BinaryOp::NotEqualTo, "icmp ne", "icmp ne"),
    (BinaryOp::LessThan, "icmp ult", "icmp slt"),
    (BinaryOp::LessOrEqual, "icmp ule", "icmp sle"),
    (BinaryOp::GreaterThan, "icmp ugt", "icmp sgt"),
    (BinaryOp::GreaterOrEqual, "icmp uge", "icmp sge"),
];

fn integer_value(integer: Integer, limbs: &[u64]) -> Value {
    Value::Integer(Limbs::new(integer.bits, limbs))
}

/// xorshift64: the same cases on every run.
struct Random(u64);

impl Random {
    fn next(&mut self) -> u64 {
        self.0 ^= self.0 << 13;
        self.0 ^= self.0 >> 7;
        self.0 ^= self.0 << 17;
        self.0
    }

    /// An interesting value of `float`, as its bits widened to an `f64`.
    fn float(&mut self, float: Float) -> u64 {
        const SPECIAL: &[f64] = &[
            0.0,
            -0.0,
            1.0,
            -1.0,
            0.1,
            0.5,
            1.5,
            3.0,
            -7.25,
            1e30,
            f64::INFINITY,
            f64::NEG_INFINITY,
            f64::NAN,
            f64::MAX,
            f64::MIN_POSITIVE,
        ];
        let pick = self.next();
        let value = match (pick % 3, float) {
            (0, _) => SPECIAL[(pick >> 8) as usize % SPECIAL.len()],
            (1, Float::F32) => f64::from(f32::from_bits(1 + (pick >> 40) as u32 % 64)), // subnormal
            (1, Float::F64) => f64::from_bits(1 + (pick >> 40) % 64),
            (_, Float::F32) => f64::from(f32::from_bits(self.next() as u32)),
            (_, Float::F64) => f64::from_bits(self.next()),
        };
        match float {
            Float::F32 => f64::from(value as f32).to_bits(),
            Float::F64 => value.to_bits(),
        }
    }

    /// An interesting value of `integer`, as limbs before wrapping.
    fn value(&mut self, integer: Integer) -> Vec<u64> {
        let n = integer.bits.div_ceil(64) as usize;
        let ones = vec![u64::MAX; n];
        let mut sign_bit = vec![0; n];
        let top = integer.bits as usize - 1;
        sign_bit[top / 64] = 1 << (top % 64);
        let mut small = vec![0; n];
        small[0] = self.next() % 300;
        match self.next() % 10 {
            0 => vec![0; n],
            1 => [vec![1], vec![0; n - 1]].concat(),
            2 => ones,     // MAX unsigned, -1 signed
            3 => sign_bit, // MIN signed
            4 => sign_bit.iter().map(|&b| b ^ u64::MAX).collect(), // MAX signed
            5 | 6 => small,
            _ => (0..n).map(|_| self.next()).collect(),
        }
    }
}

fn limbs(value: &Value) -> Vec<u64> {
    match value {
        Value::Bool(value) => vec![u64::from(*value)],
        Value::Integer(limbs) => limbs.as_slice().to_vec(),
        Value::Float(bits) => vec![*bits],
    }
}

/// `limbs` as an LLVM unsigned hex constant.
fn literal(limbs: &[u64]) -> String {
    let mut text = String::from("u0x");
    for limb in limbs.iter().rev() {
        write!(text, "{limb:016x}").unwrap();
    }
    text
}

/// One operation: its LLVM instruction, the right operand of a binary one,
/// and the bits `ir` folded.
struct Case {
    instruction: &'static str,
    right: Option<Value>,
    expected: Vec<u64>,
}

#[test]
#[ignore = "needs lli"]
fn folding_matches_llvm() {
    let mut random = Random(0x9e37_79b9_7f4a_7c15);
    let mut module = String::new();
    let mut body = String::new(); // one function per integer type: one huge main is slow to compile
    let mut calls = String::new();
    let mut expected = Vec::new();
    for &bits in WIDTHS {
        for signed in [false, true] {
            let integer = Integer { signed, bits };
            let ty = Type::Integer(integer);
            let llvm = format!("i{bits}");
            let function = format!("{}{bits}", if signed { "signed" } else { "unsigned" });
            writeln!(body, "define void @{function}() {{").unwrap();
            writeln!(calls, "  call void @{function}()").unwrap();
            let minus_one = integer_value(integer, &[u64::MAX; 16]);
            let mut sign_bit = vec![0; bits.div_ceil(64) as usize];
            sign_bit[(bits as usize - 1) / 64] = 1 << ((bits - 1) % 64);
            let min = integer_value(integer, &sign_bit);
            for _ in 0..12 {
                let left = integer_value(integer, &random.value(integer));
                let mut cases = Vec::new();
                for &(op, unsigned_instruction, signed_instruction) in BINARY {
                    let right = match op {
                        BinaryOp::ShiftLeft | BinaryOp::ShiftRight => {
                            integer_value(integer, &[random.next() % u64::from(bits)])
                        }
                        _ => integer_value(integer, &random.value(integer)),
                    };
                    let Ok(value) = binary(op, &ty, &left, &ty, &right) else {
                        continue; // traps at run time
                    };
                    let divides = matches!(op, BinaryOp::Divide | BinaryOp::Remainder);
                    if signed && divides && left == min && right == minus_one {
                        continue; // the runtime guards MIN / -1; LLVM leaves it undefined
                    }
                    let instruction = if signed {
                        signed_instruction
                    } else {
                        unsigned_instruction
                    };
                    cases.push(Case {
                        instruction,
                        right: Some(right),
                        expected: limbs(&value),
                    });
                }
                for (instruction, op) in [("neg", UnaryOp::Negate), ("not", UnaryOp::BitNot)] {
                    let value = unary(op, &ty, &left).unwrap();
                    cases.push(Case {
                        instruction,
                        right: None,
                        expected: limbs(&value),
                    });
                }
                for case in cases {
                    let k = expected.len();
                    let is_compare = case.instruction.starts_with("icmp");
                    let words = if is_compare {
                        1
                    } else {
                        bits.div_ceil(64) as usize
                    };
                    writeln!(module, "@l{k} = global {llvm} {}", literal(&limbs(&left))).unwrap();
                    writeln!(module, "@out{k} = global [{words} x i64] zeroinitializer").unwrap();
                    writeln!(body, "  %l{k} = load volatile {llvm}, ptr @l{k}").unwrap();
                    let result = match (case.instruction, &case.right) {
                        ("neg", None) => format!("sub {llvm} 0, %l{k}"),
                        ("not", None) => format!("xor {llvm} %l{k}, -1"),
                        (instruction, Some(right)) => {
                            writeln!(module, "@r{k} = global {llvm} {}", literal(&limbs(right)))
                                .unwrap();
                            writeln!(body, "  %r{k} = load volatile {llvm}, ptr @r{k}").unwrap();
                            format!("{instruction} {llvm} %l{k}, %r{k}")
                        }
                        _ => unreachable!(),
                    };
                    writeln!(body, "  %v{k} = {result}").unwrap();
                    let from = if is_compare { "i1" } else { llvm.as_str() };
                    let wide = format!("i{}", words * 64);
                    let mut stored = format!("%v{k}");
                    if from != wide {
                        writeln!(body, "  %w{k} = zext {from} %v{k} to {wide}").unwrap();
                        stored = format!("%w{k}");
                    }
                    writeln!(body, "  store {wide} {stored}, ptr @out{k}").unwrap();
                    writeln!(body, "  call void @print(ptr @out{k}, i64 {words})").unwrap();
                    expected.push(
                        case.expected[..words]
                            .iter()
                            .rev()
                            .map(|w| format!("{w:016x}"))
                            .collect::<String>(),
                    );
                }
            }
            writeln!(body, "  ret void\n}}\n").unwrap();
        }
    }
    for float in [Float::F32, Float::F64] {
        let ty = Type::Float(float);
        let (llvm, function) = match float {
            Float::F32 => ("float", "float32"),
            Float::F64 => ("double", "float64"),
        };
        writeln!(body, "define void @{function}() {{").unwrap();
        writeln!(calls, "  call void @{function}()").unwrap();
        for _ in 0..300 {
            let left = random.float(float);
            let mut cases: Vec<(&str, Option<u64>, Value)> = FLOAT_BINARY
                .iter()
                .map(|&(op, instruction)| {
                    let right = random.float(float);
                    let value = binary(op, &ty, &Value::Float(left), &ty, &Value::Float(right));
                    (instruction, Some(right), value.unwrap())
                })
                .collect();
            cases.push((
                "fneg",
                None,
                unary(UnaryOp::Negate, &ty, &Value::Float(left)).unwrap(),
            ));
            for (instruction, right, value) in cases {
                let k = expected.len();
                // A double hex constant: exact for an f32 value too.
                writeln!(module, "@l{k} = global {llvm} 0x{left:016X}").unwrap();
                writeln!(module, "@out{k} = global [1 x i64] zeroinitializer").unwrap();
                writeln!(body, "  %l{k} = load volatile {llvm}, ptr @l{k}").unwrap();
                let result = match right {
                    None => format!("{instruction} {llvm} %l{k}"),
                    Some(right) => {
                        writeln!(module, "@r{k} = global {llvm} 0x{right:016X}").unwrap();
                        writeln!(body, "  %r{k} = load volatile {llvm}, ptr @r{k}").unwrap();
                        format!("{instruction} {llvm} %l{k}, %r{k}")
                    }
                };
                writeln!(body, "  %v{k} = {result}").unwrap();
                let stored = match (instruction.starts_with("fcmp"), float) {
                    (true, _) => format!("zext i1 %v{k} to i64"),
                    (false, Float::F32) => {
                        writeln!(body, "  %d{k} = fpext float %v{k} to double").unwrap();
                        format!("bitcast double %d{k} to i64")
                    }
                    (false, Float::F64) => format!("bitcast double %v{k} to i64"),
                };
                writeln!(body, "  %w{k} = {stored}").unwrap();
                writeln!(body, "  store i64 %w{k}, ptr @out{k}").unwrap();
                writeln!(body, "  call void @print(ptr @out{k}, i64 1)").unwrap();
                expected.push(match value {
                    Value::Float(bits) if f64::from_bits(bits).is_nan() => "nan".to_string(),
                    value => format!("{:016x}", limbs(&value)[0]),
                });
            }
        }
        writeln!(body, "  ret void\n}}\n").unwrap();
    }
    let program = format!(
        "{module}
@format = private constant [8 x i8] c\"%016llx\\00\"
@newline = private constant [2 x i8] c\"\\0A\\00\"
declare i32 @printf(ptr, ...)

define void @print(ptr %words, i64 %count) {{
entry:
  br label %loop
loop:
  %i = phi i64 [ %count, %entry ], [ %next, %loop ]
  %next = sub i64 %i, 1
  %at = getelementptr i64, ptr %words, i64 %next
  %word = load i64, ptr %at
  call i32 (ptr, ...) @printf(ptr @format, i64 %word)
  %done = icmp eq i64 %next, 0
  br i1 %done, label %end, label %loop
end:
  call i32 (ptr, ...) @printf(ptr @newline)
  ret void
}}

{body}
define i32 @main() {{
{calls}  ret i32 0
}}
"
    );
    let path = std::path::Path::new(env!("CARGO_TARGET_TMPDIR")).join("llvm_parity.ll");
    std::fs::write(&path, program).unwrap();
    let output = Command::new("lli").arg(&path).output().expect("lli runs");
    assert!(
        output.status.success(),
        "lli failed:\n{}",
        String::from_utf8_lossy(&output.stderr)
    );
    let lines: Vec<_> = String::from_utf8(output.stdout)
        .unwrap()
        .lines()
        .map(str::to_string)
        .collect();
    assert_eq!(lines.len(), expected.len());
    let mut mismatches = 0;
    for (k, (expected, line)) in expected.iter().zip(&lines).enumerate() {
        let nan = || f64::from_bits(u64::from_str_radix(line, 16).unwrap()).is_nan();
        if expected != line && !(expected == "nan" && nan()) {
            mismatches += 1;
            if mismatches <= 5 {
                eprintln!("case {k}: ir {expected}\n         llvm {line}");
            }
        }
    }
    assert_eq!(
        mismatches,
        0,
        "{mismatches} of {} cases differ (module at {})",
        expected.len(),
        path.display()
    );
    eprintln!("{} cases match", expected.len());
}
