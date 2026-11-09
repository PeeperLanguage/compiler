//! `start..end`, the integers from `start` up to `end`. Built on integers,
//! a basic feature, so it names them: a range's ends must be integers.

use gateway::check::Checker;
use gateway::check::ExpressionKind;
use gateway::facts::Carrier;
use gateway::facts::Facts;
use gateway::feature::Code;
use gateway::feature::Feature;
use gateway::lower::Lowering;
use gateway::mir::Operand;
use gateway::syntax::Parsed;
use gateway::syntax::Parser;
use gateway::syntax::Precedence;
use gateway::syntax::Read;
use gateway::syntax::Rule;
use gateway::syntax::Start;
use gateway::syntax::Token;
use gateway::tree::ExprNode;
use gateway::tree::Kind;
use gateway::types::Iteration;
use gateway::types::TypeId;
use gateway::types::TypeKind;
use gateway::types::no_conversions;
use gateway::types::no_members;
use gateway::types::no_operators;
use gateway::types::no_pointee;

const NOT_INTEGER: Code = Code(1000);

/// `parts` is the integer type of both ends, held one after the other.
pub static RANGE: TypeKind = TypeKind {
    facts: |_, parts, _| Facts {
        size: 2 * parts[0].size,
        align: parts[0].align,
        carrier: Carrier::Memory,
        copies: true,
        holders: 0,
    },
    text: |cx, ty| format!("range[{}]", cx.type_text(cx.types().entry(ty).parts[0])),
    convert: no_conversions,
    operator: no_operators,
    member: no_members,
    iterate: |cx, ty| {
        Ok(Iteration::Numbers {
            number: cx.types().entry(ty).parts[0],
            bounds: |lw, value, ty| {
                let number = lw.checker.types().entry(ty).parts[0];
                let end_at = lw.offset(value, lw.checker.known_facts(number).size);
                (lw.load(value, number), lw.load(end_at, number))
            },
        })
    },
    pointee: no_pointee,
};

static RANGE_EXPRESSION: ExpressionKind = ExpressionKind {
    check: check_range,
    lower: lower_range,
    place: None,
};

const RANGES: Precedence = Precedence(50);

fn read_range(parser: &mut Parser<'_>, start: ExprNode) -> Parsed<ExprNode> {
    parser.advance();
    let end = parser.expression(Precedence(RANGES.0 + 1))?;
    let span = parser.since(parser.tree.node(start.0).span);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(&RANGE_EXPRESSION),
        span,
        &[start.0, end.0],
    )))
}

fn check_range(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> TypeId {
    let [start, end] = cx.children(node.0)[..] else {
        unreachable!("a range has a start and an end");
    };
    let number = cx.check_expression(ExprNode(start), None);
    cx.check_expression(ExprNode(end), Some(number));
    if number == TypeId::INVALID {
        return TypeId::INVALID;
    }
    if !integers::is_integer(cx, number) {
        let text = cx.type_text(number);
        cx.refuse(
            NOT_INTEGER,
            start,
            format!("a range's ends are integers, not {text}"),
        );
        return TypeId::INVALID;
    }
    cx.make_type(&RANGE, Box::new([number]), 0)
}

fn lower_range(lw: &mut Lowering<'_>, node: ExprNode, ty: TypeId) -> Operand {
    let [start, end] = lw.checker.children(node.0)[..] else {
        unreachable!("a range has a start and an end");
    };
    let number = lw.checker.types().entry(ty).parts[0];
    let place = lw.temporary_place(ty);
    let start = lw.lower_expression(ExprNode(start));
    lw.store(place, start, number);
    let end = lw.lower_expression(ExprNode(end));
    let end_at = lw.offset(place, lw.checker.known_facts(number).size);
    lw.store(end_at, end, number);
    place
}

pub static FEATURE: Feature = Feature {
    keywords: &[],
    rules: &[Rule {
        start: Start::Token(Token::DotDot),
        then: None,
        read: Read::Infix(RANGES, read_range),
    }],
    codes: &[NOT_INTEGER],
    type_names: &[],
};
