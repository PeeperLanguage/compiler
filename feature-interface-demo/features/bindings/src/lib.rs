//! `let [mut] name [: type] [= value];` and `place = value`. A target is a
//! place when its THIR kind says so (`ThirKind::place`); this feature never
//! asks which feature made it.

use gateway::check::Checker;
use gateway::check::NOT_MUTABLE;
use gateway::feature::Code;
use gateway::feature::Feature;
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
use gateway::tree::StmtNode;
use gateway::tree::TypeNode;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Place;
use model::types::TypeId;

const NOT_A_PLACE: Code = Code(300);
const NEEDS_TYPE: Code = Code(301);

// A let's children: the name, the type and the value, each of the last two
// missing when not written. Whether it is `mut` is which check it has.

fn read_let(parser: &mut Parser<'_>) -> Parsed<StmtNode> {
    let start = parser.advance();
    let check = match parser.eat_keyword("mut") {
        true => |cx: &mut Checker<'_>, node| check_let(cx, node, true),
        false => |cx: &mut Checker<'_>, node| check_let(cx, node, false),
    };
    let name = parser.name()?;
    let ty = match parser.eat(Token::Colon) {
        true => parser.ty()?.0,
        false => parser.missing(),
    };
    let value = match parser.eat(Token::Equal) {
        true => parser.expression(Precedence(0))?.0,
        false => parser.missing(),
    };
    parser.expect(Token::Semicolon, "`;`")?;
    let span = parser.since(start);
    Ok(StmtNode(parser.tree.push(
        Kind::Statement(check),
        span,
        &[name, ty, value],
    )))
}

/// The variable is declared after its value is checked, so the value sees
/// an earlier variable of the same name.
fn check_let(cx: &mut Checker<'_>, node: StmtNode, mutable: bool) -> ThirId {
    let [name, ty, value] = cx.children(node.0)[..] else {
        unreachable!("a let has a name, a type and a value");
    };
    let written = (!cx.tree.is_missing(ty)).then(|| cx.resolve_type(TypeNode(ty)));
    let value = (!cx.tree.is_missing(value)).then(|| cx.check_expression(ExprNode(value), written));
    let ty = match (written, value) {
        (Some(ty), _) => ty,
        (None, Some(value)) => cx.thir().node(value).ty,
        (None, None) => {
            let message = format!("`{}` needs a type or a value", cx.text(name));
            cx.refuse(NEEDS_TYPE, name, message);
            TypeId::INVALID
        }
    };
    let variable = cx.declare_variable(name, ty, mutable, value.is_some());
    let span = cx.span(node.0);
    let value: &[ThirId] = match &value {
        Some(value) => std::slice::from_ref(value),
        None => &[],
    };
    cx.push(&LET, TypeId::VOID, span, Data::Variable(variable), value)
}

/// The value, if any, then the variable's scope opens and it is set.
static LET: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let Data::Variable(variable) = checker.thir().node(id).data else {
            unreachable!("a let declares its variable");
        };
        // Checked against the variable's type, so of that type.
        let value = (checker.thir().children(id).first())
            .map(|&value| (lw.value(value), checker.thir().node(value).ty));
        lw.declare(variable);
        if let Some((value, ty)) = value {
            let store = InstructionKind::Store {
                place: Place::whole(variable),
                value,
                ty,
            };
            lw.emit(store, TypeId::VOID);
        }
        Operand::Constant(0)
    },
    place: None,
};

const ASSIGNS: Precedence = Precedence(10);

/// `place = value`, grouping rightward.
fn read_assignment(parser: &mut Parser<'_>, target: ExprNode) -> Parsed<ExprNode> {
    parser.advance();
    let value = parser.expression(ASSIGNS)?;
    let span = parser.since(parser.tree.node(target.0).span);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check_assignment),
        span,
        &[target.0, value.0],
    )))
}

/// A whole variable without `mut` may be set only when its `let` gave it
/// no value, and then once, which the path checks see to; part of one, or
/// what a reference points at, needs `mut`.
fn check_assignment(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> ThirId {
    let [target_node, value] = cx.children(node.0)[..] else {
        unreachable!("an assignment has a target and a value");
    };
    let target = cx.check_expression(ExprNode(target_node), None);
    let target_type = cx.thir().node(target).ty;
    let value = cx.check_expression(ExprNode(value), Some(target_type));
    if target_type != TypeId::INVALID {
        let whole = cx
            .whole_variable(target)
            .map(|variable| *cx.variable(variable));
        if !cx.is_place(target) {
            let message = "this cannot be set; only a place can".to_string();
            cx.refuse(NOT_A_PLACE, target_node, message);
        } else if let Some(variable) = whole {
            if variable.initialized && !variable.mutable {
                let span = variable.span;
                let name = &cx.source[span.start as usize..span.end as usize];
                let message = format!("cannot assign to immutable `{name}`");
                cx.refuse(NOT_MUTABLE, target_node, message);
            }
        } else if !cx.is_mutable(target) {
            let message = "this cannot be set; it is not `mut`".to_string();
            cx.refuse(NOT_MUTABLE, target_node, message);
        }
    }
    let span = cx.span(node.0);
    cx.push(&ASSIGN, TypeId::VOID, span, Data::None, &[target, value])
}

/// The value, then the place it is stored in.
static ASSIGN: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let [target, value] = checker.thir().children(id)[..] else {
            unreachable!("an assignment has a target and a value");
        };
        let value = lw.value(value);
        let place = lw.place(target);
        let ty = checker.thir().node(target).ty;
        lw.emit(InstructionKind::Store { place, value, ty }, TypeId::VOID);
        Operand::Constant(0)
    },
    place: None,
};

pub static FEATURE: Feature = Feature {
    keywords: &["let", "mut"],
    rules: &[
        Rule {
            start: Start::Keyword("let"),
            then: None,
            read: Read::Statement(read_let),
        },
        Rule {
            start: Start::Token(Token::Equal),
            then: None,
            read: Read::Infix(ASSIGNS, read_assignment),
        },
    ],
    codes: &[NOT_A_PLACE, NEEDS_TYPE],
    type_names: &[],
};
