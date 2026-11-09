//! `for name in value { ... }`. It never asks what `value` is: it asks the
//! value's type kind how to walk it (`TypeKind::iterate`), which answers
//! with elements or numbers, or refuses in its own words.

use gateway::check::Answer;
use gateway::check::Checker;
use gateway::check::Resolution;
use gateway::check::StatementKind;
use gateway::feature::Code;
use gateway::feature::Feature;
use gateway::lower::Lowering;
use gateway::mir::End;
use gateway::mir::Operand;
use gateway::mir::Operator;
use gateway::syntax::Parsed;
use gateway::syntax::Parser;
use gateway::syntax::Precedence;
use gateway::syntax::Read;
use gateway::syntax::Rule;
use gateway::syntax::Start;
use gateway::tree::BlockNode;
use gateway::tree::ExprNode;
use gateway::tree::Kind;
use gateway::tree::StmtNode;
use gateway::types::Iteration;
use gateway::types::TypeId;

const NOT_ITERABLE: Code = Code(900);

static FOR: StatementKind = StatementKind {
    check: check_for,
    lower: lower_for,
};

fn read_for(parser: &mut Parser<'_>) -> Parsed<StmtNode> {
    let start = parser.advance();
    let name = parser.name()?;
    parser.expect_keyword("in")?;
    let value = parser.expression(Precedence(0))?;
    let body = parser.block()?;
    let span = parser.since(start);
    Ok(StmtNode(parser.tree.push(
        Kind::Statement(&FOR),
        span,
        &[name, value.0, body.0],
    )))
}

fn check_for(cx: &mut Checker<'_>, node: StmtNode) {
    let [name, value, body] = cx.children(node.0)[..] else {
        unreachable!("a for has a name, a value and a body");
    };
    let ty = cx.check_expression(ExprNode(value), None);
    let item = match cx.iterate(ty) {
        Ok(iteration) => {
            cx.record(node.0, Answer::Iteration(iteration));
            match iteration {
                Iteration::Elements { element, .. } => element,
                Iteration::Numbers { number, .. } => number,
            }
        }
        Err(message) => {
            cx.refuse(NOT_ITERABLE, value, message);
            TypeId::INVALID
        }
    };
    cx.enter_scope();
    cx.declare_variable(name, item);
    cx.check_block(BlockNode(body));
    cx.leave_scope();
}

/// A counter from the start to the end; each turn sets the variable to the
/// element at the counter, or to the counter itself for numbers.
fn lower_for(lw: &mut Lowering<'_>, node: StmtNode) {
    let [name, value, body] = lw.checker.children(node.0)[..] else {
        unreachable!("a for has a name, a value and a body");
    };
    let Resolution::Variable(variable) = lw.checker.resolution(name) else {
        unreachable!("a for declares its variable");
    };
    let ty = lw.checker.type_of(ExprNode(value));
    let value = lw.lower_expression(ExprNode(value));
    let Some(Answer::Iteration(iteration)) = lw.checker.answer(node.0) else {
        unreachable!("a checked for knows how to walk its value");
    };
    // `first` is the address of the first element, for elements; `number`
    // the counter's type, for numbers, which may be wider than a word.
    let (start, end, first, number, bits) = match iteration {
        Iteration::Elements { elements, .. } => {
            let (first, count) = elements(lw, value, ty);
            (Operand::Constant(0), count, Some(first), None, 64)
        }
        Iteration::Numbers { number, bounds } => {
            let (start, end) = bounds(lw, value, ty);
            let bits = lw.checker.known_facts(number).size as u16 * 8;
            (start, end, None, Some(number), bits)
        }
    };
    let counter = match number {
        Some(number) => lw.temporary_place(number),
        None => lw.word_place(1),
    };
    let write = move |lw: &mut Lowering<'_>, value| match number {
        Some(number) => lw.store(counter, value, number),
        None => lw.store_word(counter, value),
    };
    write(lw, start);
    let head = lw.new_block();
    let turn = lw.new_block();
    let exit = lw.new_block();
    lw.end(End::Jump(head));
    lw.switch_to(head);
    let at = match number {
        Some(number) => lw.load(counter, number),
        None => lw.load_word(counter),
    };
    let more = lw.binary(Operator::Less, at, end, bits);
    lw.end(End::Branch {
        condition: more,
        then: turn,
        otherwise: exit,
    });
    lw.switch_to(turn);
    let item_type = lw.checker.variable_type(variable);
    let item = match first {
        Some(first) => {
            let size = lw.checker.known_facts(item_type).size;
            let offset = lw.binary(Operator::Multiply, at, Operand::Constant(size), 64);
            let address = lw.binary(Operator::Add, first, offset, 64);
            lw.load(address, item_type)
        }
        None => at,
    };
    let address = lw.variable_address(variable);
    lw.store(address, item, item_type);
    lw.lower_block(BlockNode(body));
    let next = lw.binary(Operator::Add, at, Operand::Constant(1), bits);
    write(lw, next);
    lw.end(End::Jump(head));
    lw.switch_to(exit);
}

pub static FEATURE: Feature = Feature {
    keywords: &["for", "in"],
    rules: &[Rule {
        start: Start::Keyword("for"),
        then: None,
        read: Read::Statement(read_for),
    }],
    codes: &[NOT_ITERABLE],
    type_names: &[],
};
