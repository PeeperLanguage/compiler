//! Control flow, which is core: `if`/`else`, `for {}` and `for condition {}`,
//! `break`, `continue` and `return`. Every phase after checking follows the
//! blocks these make, so they are part of the pipeline, not a feature; a
//! condition is a `bool`, the core's own type.

use model::mir::BlockId;
use model::mir::Operand;
use model::mir::Terminator;
use model::types::TypeId;

use crate::check::Checker;
use crate::feature::Code;
use crate::feature::Feature;
use crate::lower::Loop;
use crate::lower::Lowering;
use crate::syntax::Parsed;
use crate::syntax::Parser;
use crate::syntax::Precedence;
use crate::syntax::Read;
use crate::syntax::Rule;
use crate::syntax::Start;
use crate::syntax::Token;
use crate::thir::Data;
use crate::thir::ThirId;
use crate::thir::ThirKind;
use crate::tree::BlockNode;
use crate::tree::ExprNode;
use crate::tree::Kind;
use crate::tree::StmtNode;

const OUTSIDE_LOOP: Code = Code(10);
const RETURN_NEEDS_VALUE: Code = Code(11);

/// Ends the block with a jump away. What follows is written after it, so
/// it goes to a new block no path reaches.
fn jump_away(lw: &mut Lowering<'_>, terminator: Terminator) {
    lw.terminate(terminator);
    let after = lw.new_block();
    lw.switch_to(after);
}

// `if condition { } [else if ... | else { }]`: the children are the
// condition, the block, and the `else` part or a missing node.

fn read_if(parser: &mut Parser<'_>) -> Parsed<StmtNode> {
    let start = parser.advance();
    let condition = parser.expression(Precedence(0))?;
    let then = parser.block()?;
    let otherwise = match parser.eat_keyword("else") {
        false => parser.missing(),
        // `else if`: an `if` of its own.
        true if parser.at_keyword("if") => read_if(parser)?.0,
        true => parser.block()?.0,
    };
    let span = parser.since(start);
    Ok(StmtNode(parser.tree.push(
        Kind::Statement(check_if),
        span,
        &[condition.0, then.0, otherwise],
    )))
}

fn check_if(cx: &mut Checker<'_>, node: StmtNode) -> ThirId {
    let [condition, then, otherwise] = cx.children(node.0)[..] else {
        unreachable!("an if has a condition, a block and an else part");
    };
    let mut children = vec![
        cx.check_expression(ExprNode(condition), Some(TypeId::BOOL)),
        cx.check_block(BlockNode(then)),
    ];
    match cx.tree.node(otherwise).kind {
        Kind::Missing => {}
        Kind::Block => children.push(cx.check_block(BlockNode(otherwise))),
        _ => children.push(cx.check_statement(StmtNode(otherwise))),
    }
    let span = cx.span(node.0);
    cx.push(&IF, TypeId::VOID, span, Data::None, &children)
}

static IF: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let children = checker.thir().children(id);
        let condition = lw.value(children[0]);
        // Made in the real compiler's order (then, else, join), so the
        // two dumps number blocks alike.
        let then = lw.new_block();
        let otherwise = (children.len() == 3).then(|| lw.new_block());
        let join = lw.new_block();
        let otherwise = otherwise.unwrap_or(join);
        lw.terminate(Terminator::Branch {
            condition,
            span: checker.thir().node(children[0]).span,
            then,
            otherwise,
        });
        for (&part, block) in children[1..].iter().zip([then, otherwise]) {
            lw.switch_to(block);
            lw.value(part);
            lw.terminate(Terminator::Goto(join));
        }
        lw.switch_to(join);
        Operand::Constant(0)
    },
    place: None,
};

// `for [condition] { }`: the children are the condition or a missing
// node, and the block. With no condition it runs until a `break`.

fn read_for(parser: &mut Parser<'_>) -> Parsed<StmtNode> {
    let start = parser.advance();
    let condition = match parser.peek() {
        Token::LeftBrace => parser.missing(),
        _ => parser.expression(Precedence(0))?.0,
    };
    let body = parser.block()?;
    let span = parser.since(start);
    Ok(StmtNode(parser.tree.push(
        Kind::Statement(check_for),
        span,
        &[condition, body.0],
    )))
}

fn check_for(cx: &mut Checker<'_>, node: StmtNode) -> ThirId {
    let [condition, body] = cx.children(node.0)[..] else {
        unreachable!("a for has a condition and a block");
    };
    let mut children = Vec::new();
    if !cx.tree.is_missing(condition) {
        children.push(cx.check_expression(ExprNode(condition), Some(TypeId::BOOL)));
    }
    cx.loops += 1;
    children.push(cx.check_block(BlockNode(body)));
    cx.loops -= 1;
    let span = cx.span(node.0);
    cx.push(&FOR, TypeId::VOID, span, Data::None, &children)
}

/// The head tests the condition, if any, each turn; the body jumps back
/// to it.
static FOR: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let (body, condition) = (checker.thir().children(id))
            .split_last()
            .expect("a for has a block");
        let head = lw.new_block();
        let exit = lw.new_block();
        lw.terminate(Terminator::Goto(head));
        lw.switch_to(head);
        if let [condition] = condition {
            let value = lw.value(*condition);
            let turn = lw.new_block();
            lw.terminate(Terminator::Branch {
                condition: value,
                span: checker.thir().node(*condition).span,
                then: turn,
                otherwise: exit,
            });
            lw.switch_to(turn);
        }
        let scopes = lw.open_scopes();
        lw.loops.push(Loop { head, exit, scopes });
        lw.value(*body);
        lw.loops.pop();
        lw.terminate(Terminator::Goto(head));
        lw.switch_to(exit);
        Operand::Constant(0)
    },
    place: None,
};

/// `break;` or `continue;`, checked by `check`.
fn read_jump(
    parser: &mut Parser<'_>,
    check: fn(&mut Checker<'_>, StmtNode) -> ThirId,
) -> Parsed<StmtNode> {
    let start = parser.advance();
    parser.expect(Token::Semicolon, "`;`")?;
    let span = parser.since(start);
    Ok(StmtNode(parser.tree.push(
        Kind::Statement(check),
        span,
        &[],
    )))
}

fn check_jump(cx: &mut Checker<'_>, node: StmtNode, kind: &'static ThirKind, word: &str) -> ThirId {
    if cx.loops == 0 {
        let message = format!("`{word}` is only inside a loop");
        cx.refuse(OUTSIDE_LOOP, node.0, message);
        return cx.invalid(node.0);
    }
    let span = cx.span(node.0);
    cx.push(kind, TypeId::VOID, span, Data::None, &[])
}

/// Leaves the scopes of the innermost loop, to the block `to` picks.
fn lower_jump(lw: &mut Lowering<'_>, to: fn(&Loop) -> BlockId) -> Operand {
    let innermost = lw.loops.last().expect("checked: a jump is inside a loop");
    let (target, scopes) = (to(innermost), innermost.scopes);
    lw.end_scopes(scopes);
    jump_away(lw, Terminator::Goto(target));
    Operand::Constant(0)
}

static BREAK: ThirKind = ThirKind {
    lower: |lw, _| lower_jump(lw, |innermost| innermost.exit),
    place: None,
};

static CONTINUE: ThirKind = ThirKind {
    lower: |lw, _| lower_jump(lw, |innermost| innermost.head),
    place: None,
};

// `return [value];`: the child is the value or a missing node.

fn read_return(parser: &mut Parser<'_>) -> Parsed<StmtNode> {
    let start = parser.advance();
    let value = match parser.peek() {
        Token::Semicolon => parser.missing(),
        _ => parser.expression(Precedence(0))?.0,
    };
    parser.expect(Token::Semicolon, "`;`")?;
    let span = parser.since(start);
    Ok(StmtNode(parser.tree.push(
        Kind::Statement(check_return),
        span,
        &[value],
    )))
}

fn check_return(cx: &mut Checker<'_>, node: StmtNode) -> ThirId {
    let value = cx.children(node.0)[0];
    let span = cx.span(node.0);
    if !cx.tree.is_missing(value) {
        let value = cx.check_expression(ExprNode(value), Some(cx.return_type));
        return cx.push(&RETURN, TypeId::VOID, span, Data::None, &[value]);
    }
    if cx.return_type != TypeId::VOID {
        let text = cx.type_text(cx.return_type);
        let message = format!("this function returns {text}, so `return` needs a value");
        cx.refuse(RETURN_NEEDS_VALUE, node.0, message);
    }
    cx.push(&RETURN, TypeId::VOID, span, Data::None, &[])
}

/// The value first, then every open scope ends.
static RETURN: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let value = (checker.thir().children(id).first()).map(|&value| lw.value(value));
        lw.end_scopes(0);
        let span = checker.thir().node(id).span;
        jump_away(lw, Terminator::Return { value, span });
        Operand::Constant(0)
    },
    place: None,
};

pub static FEATURE: Feature = Feature {
    keywords: &["if", "else", "for", "break", "continue", "return"],
    rules: &[
        Rule {
            start: Start::Keyword("if"),
            then: None,
            read: Read::Statement(read_if),
        },
        Rule {
            start: Start::Keyword("for"),
            then: None,
            read: Read::Statement(read_for),
        },
        Rule {
            start: Start::Keyword("break"),
            then: None,
            read: Read::Statement(|parser| {
                read_jump(parser, |cx, node| check_jump(cx, node, &BREAK, "break"))
            }),
        },
        Rule {
            start: Start::Keyword("continue"),
            then: None,
            read: Read::Statement(|parser| {
                read_jump(parser, |cx, node| {
                    check_jump(cx, node, &CONTINUE, "continue")
                })
            }),
        },
        Rule {
            start: Start::Keyword("return"),
            then: None,
            read: Read::Statement(read_return),
        },
    ],
    codes: &[OUTSIDE_LOOP, RETURN_NEEDS_VALUE],
    type_names: &[],
};
