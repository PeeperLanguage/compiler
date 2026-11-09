//! Integers: `i32`, `i64` and the target-sized `isize`, their literals, and
//! what their operators give.

use gateway::check::Checker;
use gateway::feature::Code;
use gateway::feature::Feature;
use gateway::feature::TypeName;
use gateway::lower::Lowering;
use gateway::syntax::Parsed;
use gateway::syntax::Parser;
use gateway::syntax::Read;
use gateway::syntax::Rule;
use gateway::syntax::Start;
use gateway::syntax::Token;
use gateway::thir;
use gateway::thir::Data;
use gateway::thir::ThirId;
use gateway::tree::ExprNode;
use gateway::tree::Kind;
use gateway::types::Operation;
use gateway::types::TypeKind;
use gateway::types::no_conversions;
use gateway::types::no_fields;
use gateway::types::no_pointee;
use model::facts::Facts;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Operator;
use model::types::TypeId;

/// `detail` of `isize`: as wide as a pointer of the target.
const TARGET_SIZED: u32 = 0;

const DOES_NOT_FIT: Code = Code(100);

pub static INTEGER: TypeKind = TypeKind {
    facts: |bits, _, target| match bits {
        TARGET_SIZED => Facts::scalar(target.pointer_bytes),
        bits => Facts::scalar(u64::from(bits / 8)),
    },
    text: |cx, ty| match cx.types().entry(ty).detail {
        TARGET_SIZED => "isize".to_string(),
        bits => format!("i{bits}"),
    },
    convert: no_conversions,
    operator,
    field: no_fields,
    pointee: no_pointee,
};

/// Both sides are one integer type: the right side was checked against the
/// left's.
fn operator(
    _: &mut Checker<'_>,
    operator: Operator,
    left: TypeId,
    _: TypeId,
) -> Result<Operation, String> {
    let result = match operator {
        Operator::LessThan => TypeId::BOOL,
        Operator::Add | Operator::Subtract | Operator::Multiply => left,
    };
    Ok(Operation {
        result,
        lower: lower_operator,
    })
}

/// One machine instruction; the operands' type says how wide.
fn lower_operator(
    lw: &mut Lowering<'_>,
    operator: Operator,
    left: Operand,
    right: Operand,
    operands: TypeId,
    result: TypeId,
) -> Operand {
    let binary = InstructionKind::Binary {
        operator,
        left,
        right,
        operands,
    };
    lw.emit(binary, result)
}

pub fn is_integer(cx: &Checker<'_>, ty: TypeId) -> bool {
    cx.types().is(ty, &INTEGER)
}

fn read_literal(parser: &mut Parser<'_>) -> Parsed<ExprNode> {
    let span = parser.advance();
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check_literal),
        span,
        &[],
    )))
}

/// The expected integer type, or `i32`.
fn check_literal(cx: &mut Checker<'_>, node: ExprNode, expected: Option<TypeId>) -> ThirId {
    let ty = match expected {
        Some(ty) if is_integer(cx, ty) => ty,
        _ => cx.make_type(&INTEGER, Box::new([]), 32),
    };
    let bits = cx.known_facts(ty).size * 8;
    let value = (cx.text(node.0).parse::<u64>())
        .ok()
        .filter(|&value| value < 1 << (bits - 1));
    let Some(value) = value else {
        let text = cx.type_text(ty);
        let message = format!("this number does not fit in an {text}");
        cx.refuse(DOES_NOT_FIT, node.0, message);
        return cx.invalid(node.0);
    };
    let span = cx.span(node.0);
    cx.push(&thir::CONSTANT, ty, span, Data::Number(value), &[])
}

pub static FEATURE: Feature = Feature {
    keywords: &[],
    rules: &[Rule {
        start: Start::Token(Token::Number),
        then: None,
        read: Read::Prefix(read_literal),
    }],
    codes: &[DOES_NOT_FIT],
    type_names: &[
        TypeName {
            text: "i32",
            kind: &INTEGER,
            detail: 32,
        },
        TypeName {
            text: "i64",
            kind: &INTEGER,
            detail: 64,
        },
        TypeName {
            text: "isize",
            kind: &INTEGER,
            detail: TARGET_SIZED,
        },
    ],
};
