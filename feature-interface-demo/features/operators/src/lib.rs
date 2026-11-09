//! `+ - * <`. No value feature owns an operator token, since several types
//! have `+`: this feature owns the syntax and asks the left operand's type
//! kind what the operator gives (`TypeKind::operator`) and how to lower it.

use gateway::check::Checker;
use gateway::feature::Code;
use gateway::feature::Feature;
use gateway::lower::Lowering;
use gateway::syntax::Parsed;
use gateway::syntax::Parser;
use gateway::syntax::Precedence;
use gateway::syntax::Read;
use gateway::syntax::Rule;
use gateway::syntax::Start;
use gateway::syntax::Token;
use gateway::thir::Data;
use gateway::thir::ThirId;
use gateway::thir::ThirKind;
use gateway::tree::ExprNode;
use gateway::tree::Kind;
use model::mir::Operand;
use model::mir::Operator;
use model::types::TypeId;

const NO_OPERATOR: Code = Code(200);

const SUM: Precedence = Precedence(60);
const PRODUCT: Precedence = Precedence(70);
const COMPARISON: Precedence = Precedence(40);

type Check = fn(&mut Checker<'_>, ExprNode, Option<TypeId>) -> ThirId;

fn read(
    parser: &mut Parser<'_>,
    left: ExprNode,
    check: Check,
    precedence: Precedence,
) -> Parsed<ExprNode> {
    parser.advance();
    let right = parser.expression(Precedence(precedence.0 + 1))?;
    let span = parser.since(parser.tree.node(left.0).span);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check),
        span,
        &[left.0, right.0],
    )))
}

fn check(cx: &mut Checker<'_>, node: ExprNode, operator: Operator) -> ThirId {
    let [left, right] = cx.children(node.0)[..] else {
        unreachable!("an operator has two sides");
    };
    let left = cx.check_expression(ExprNode(left), None);
    let left_type = cx.thir().node(left).ty;
    let right = cx.check_expression(ExprNode(right), Some(left_type));
    let right_type = cx.thir().node(right).ty;
    match (cx.types().entry(left_type).kind.operator)(cx, operator, left_type, right_type) {
        Ok(operation) => {
            let span = cx.span(node.0);
            let data = Data::Operator(operator, operation.lower);
            cx.push(&BINARY, operation.result, span, data, &[left, right])
        }
        Err(message) => {
            cx.refuse(NO_OPERATOR, node.0, message);
            cx.invalid(node.0)
        }
    }
}

/// Both sides, then the operator as the left side's type kind lowers it.
static BINARY: ThirKind = ThirKind {
    lower: lower_binary,
    place: None,
};

fn lower_binary(lw: &mut Lowering<'_>, id: ThirId) -> Operand {
    let checker = lw.checker;
    let node = checker.thir().node(id);
    let Data::Operator(operator, lowering) = node.data else {
        unreachable!("a checked operator has its lowering");
    };
    let [left, right] = checker.thir().children(id)[..] else {
        unreachable!("an operator has two sides");
    };
    let operands = checker.thir().node(left).ty;
    let left = lw.value(left);
    let right = lw.value(right);
    lowering(lw, operator, left, right, operands, node.ty)
}

pub static FEATURE: Feature = Feature {
    keywords: &[],
    rules: &[
        Rule {
            start: Start::Token(Token::Plus),
            then: None,
            read: Read::Infix(SUM, |parser, left| {
                read(
                    parser,
                    left,
                    |cx, node, _| check(cx, node, Operator::Add),
                    SUM,
                )
            }),
        },
        Rule {
            start: Start::Token(Token::Minus),
            then: None,
            read: Read::Infix(SUM, |parser, left| {
                read(
                    parser,
                    left,
                    |cx, node, _| check(cx, node, Operator::Subtract),
                    SUM,
                )
            }),
        },
        Rule {
            start: Start::Token(Token::Star),
            then: None,
            read: Read::Infix(PRODUCT, |parser, left| {
                read(
                    parser,
                    left,
                    |cx, node, _| check(cx, node, Operator::Multiply),
                    PRODUCT,
                )
            }),
        },
        Rule {
            start: Start::Token(Token::Less),
            then: None,
            read: Read::Infix(COMPARISON, |parser, left| {
                read(
                    parser,
                    left,
                    |cx, node, _| check(cx, node, Operator::LessThan),
                    COMPARISON,
                )
            }),
        },
    ],
    codes: &[NO_OPERATOR],
    type_names: &[],
};
