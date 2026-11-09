//! `&T` and `&mut T`, `&place` and `&mut place`, and `value.*`. A reference
//! answers `pointee`, which is how the core's dereference and the members
//! feature see through it without naming this feature.

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
use gateway::tree::TypeNode;
use gateway::types::Pointee;
use gateway::types::TypeKind;
use gateway::types::no_conversions;
use gateway::types::no_fields;
use gateway::types::no_operators;
use model::facts::Carrier;
use model::facts::Facts;
use model::mir::InstructionKind;
use model::span::Span;
use model::types::TypeId;

const NOT_A_PLACE: Code = Code(400);
const NOT_A_REFERENCE: Code = Code(401);

/// `detail` of a `&mut T`; a `&T` has 0.
const MUTABLE: u32 = 1;

/// `parts` are what it points at. A `&mut` is the one way to set its
/// target, so it moves, never copies.
pub static REFERENCE: TypeKind = TypeKind {
    facts: |detail, _, target| Facts {
        carrier: Carrier::Scalar,
        copies: detail != MUTABLE,
        holders: 1,
        ..Facts::scalar(target.pointer_bytes)
    },
    text: |cx, ty| {
        let entry = cx.types().entry(ty);
        let word = match entry.detail {
            MUTABLE => "&mut ",
            _ => "&",
        };
        format!("{word}{}", cx.type_text(entry.parts[0]))
    },
    // shortcut: a `&mut T` is not taken where a `&T` is expected; the real
    // compiler borrows it again as `&T`, which needs a conversion that
    // lowers a place, not a value.
    convert: no_conversions,
    operator: no_operators,
    field: no_fields,
    pointee: |cx, ty| {
        let entry = cx.types().entry(ty);
        Some(Pointee {
            ty: entry.parts[0],
            mutable: entry.detail == MUTABLE,
        })
    },
};

pub fn reference(cx: &mut Checker<'_>, target: TypeId, mutable: bool) -> TypeId {
    cx.make_type(&REFERENCE, Box::new([target]), u32::from(mutable))
}

/// A borrow of the place `place`, written at `span`.
pub fn borrow(cx: &mut Checker<'_>, place: ThirId, mutable: bool, span: Span) -> ThirId {
    let ty = reference(cx, cx.thir().node(place).ty, mutable);
    cx.push(&BORROW, ty, span, Data::Mutable(mutable), &[place])
}

/// `value` as given where a `&mut` is expected: one held in a place is
/// borrowed again from what it points at, so the place keeps it.
pub fn reborrow(cx: &mut Checker<'_>, value: ThirId) -> ThirId {
    let node = cx.thir().node(value);
    let span = node.span;
    let held = cx.is_place(value) && cx.types().is(node.ty, &REFERENCE);
    if !held || cx.types().entry(node.ty).detail != MUTABLE {
        return value;
    }
    let target = (cx.dereference(value, span)).expect("a reference points at something");
    borrow(cx, target, true, span)
}

/// The address of its place.
static BORROW: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let node = checker.thir().node(id);
        let Data::Mutable(mutable) = node.data else {
            unreachable!("a borrow says whether it is `&mut`");
        };
        let place = lw.place(checker.thir().children(id)[0]);
        lw.emit(InstructionKind::Borrow { place, mutable }, node.ty)
    },
    place: None,
};

fn check_borrow(cx: &mut Checker<'_>, node: ExprNode, mutable: bool) -> ThirId {
    let place_node = cx.children(node.0)[0];
    let place = cx.check_expression(ExprNode(place_node), None);
    if cx.thir().node(place).ty != TypeId::INVALID {
        if !cx.is_place(place) {
            let message = "only a place can be borrowed".to_string();
            cx.refuse(NOT_A_PLACE, place_node, message);
        } else if mutable && !cx.is_mutable(place) {
            let message = "this cannot be borrowed `&mut`; it is not `mut`".to_string();
            cx.refuse(NOT_MUTABLE, place_node, message);
        }
    }
    let span = cx.span(node.0);
    borrow(cx, place, mutable, span)
}

fn read_borrow(parser: &mut Parser<'_>) -> Parsed<ExprNode> {
    let start = parser.advance();
    let check = match parser.eat_keyword("mut") {
        true => |cx: &mut Checker<'_>, node, _| check_borrow(cx, node, true),
        false => |cx: &mut Checker<'_>, node, _| check_borrow(cx, node, false),
    };
    let place = parser.expression(Precedence(80))?;
    let span = parser.since(start);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check),
        span,
        &[place.0],
    )))
}

/// `value.*`: what the reference points at, a place.
fn read_dereference(parser: &mut Parser<'_>, value: ExprNode) -> Parsed<ExprNode> {
    parser.advance();
    parser.advance();
    let span = parser.since(parser.tree.node(value.0).span);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check_dereference),
        span,
        &[value.0],
    )))
}

fn check_dereference(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> ThirId {
    let value_node = cx.children(node.0)[0];
    let value = cx.check_expression(ExprNode(value_node), None);
    let ty = cx.thir().node(value).ty;
    if ty == TypeId::INVALID {
        return value;
    }
    let span = cx.span(node.0);
    match cx.dereference(value, span) {
        Some(target) => target,
        None => {
            let message = format!("a value of {} is not a reference", cx.type_text(ty));
            cx.refuse(NOT_A_REFERENCE, value_node, message);
            cx.invalid(node.0)
        }
    }
}

fn read_type(parser: &mut Parser<'_>) -> Parsed<TypeNode> {
    let start = parser.advance();
    let resolve = match parser.eat_keyword("mut") {
        true => |cx: &mut Checker<'_>, node| resolve_type(cx, node, true),
        false => |cx: &mut Checker<'_>, node| resolve_type(cx, node, false),
    };
    let target = parser.ty()?;
    let span = parser.since(start);
    Ok(TypeNode(parser.tree.push(
        Kind::Type(resolve),
        span,
        &[target.0],
    )))
}

fn resolve_type(cx: &mut Checker<'_>, node: TypeNode, mutable: bool) -> TypeId {
    let target = cx.resolve_type(TypeNode(cx.children(node.0)[0]));
    reference(cx, target, mutable)
}

pub static FEATURE: Feature = Feature {
    keywords: &[],
    rules: &[
        Rule {
            start: Start::Token(Token::Ampersand),
            then: None,
            read: Read::Prefix(read_borrow),
        },
        Rule {
            start: Start::Token(Token::Ampersand),
            then: None,
            read: Read::Type(read_type),
        },
        Rule {
            start: Start::Token(Token::Dot),
            then: Some(Token::Star),
            read: Read::Infix(Precedence(95), read_dereference),
        },
    ],
    codes: &[NOT_A_PLACE, NOT_A_REFERENCE],
    type_names: &[],
};
