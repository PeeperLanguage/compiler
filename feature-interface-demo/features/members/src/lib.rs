//! `value.name` and `value.name(arguments)`. Several features have members
//! (struct fields, declared methods), so none owns `.`: this feature owns
//! the syntax and asks the value's type kind (`TypeKind::field`) or the
//! declared methods. Through a reference it asks what the reference points
//! at, and the dereference is a THIR node of its own.

use gateway::check::Checker;
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
use gateway::thir::PlaceKind;
use gateway::thir::ThirId;
use gateway::thir::ThirKind;
use gateway::thir::read_place;
use gateway::tree::ExprNode;
use gateway::tree::Kind;
use model::mir::Projection;
use model::types::TypeId;

const NO_FIELD: Code = Code(600);
const NO_METHOD: Code = Code(601);

/// The value whose member `name` is meant: `base`, or what it points at
/// when only that has the member.
fn owner(cx: &mut Checker<'_>, base: ThirId, name: &str) -> ThirId {
    let has = |cx: &Checker<'_>, ty| cx.field(ty, name).is_some() || cx.method(ty, name).is_some();
    let node = cx.thir().node(base);
    let span = node.span;
    match cx.pointee(node.ty) {
        Some(target) if !has(cx, node.ty) && has(cx, target.ty) => {
            (cx.dereference(base, span)).expect("a reference points at something")
        }
        _ => base,
    }
}

// A field read's children: the value, the name. A method call's: the
// value, the name, the arguments.

/// The field of its one part's storage.
static FIELD: ThirKind = ThirKind {
    lower: read_place,
    place: Some(PlaceKind {
        lower: |lw, id| {
            let checker = lw.checker;
            let Data::Field(field) = checker.thir().node(id).data else {
                unreachable!("a field read names its field");
            };
            let mut place = lw.place(checker.thir().children(id)[0]);
            place.projections.push(Projection::Field {
                index: field.index,
                offset: field.offset,
                first_holder: field.first_holder,
                holders: field.holders,
            });
            place
        },
        mutable: |cx, id| cx.is_mutable(cx.thir().children(id)[0]),
    }),
};

fn check_field(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> ThirId {
    let [base, name] = cx.children(node.0)[..] else {
        unreachable!("a field read has a value and a name");
    };
    let base = cx.check_expression(ExprNode(base), None);
    let name = cx.text(name);
    let base = owner(cx, base, name);
    let owner_type = cx.thir().node(base).ty;
    let span = cx.span(node.0);
    match cx.field(owner_type, name) {
        Some(field) => cx.push(&FIELD, field.ty, span, Data::Field(field), &[base]),
        None => {
            let text = cx.type_text(owner_type);
            cx.refuse(NO_FIELD, node.0, format!("{text} has no field `{name}`"));
            cx.invalid(node.0)
        }
    }
}

/// A call whose first argument borrows the value, for `&self`.
fn check_method_call(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> ThirId {
    let children = cx.children(node.0);
    let base = cx.check_expression(ExprNode(children[0]), None);
    if cx.thir().node(base).ty == TypeId::INVALID {
        return base;
    }
    let name = cx.text(children[1]);
    let base = owner(cx, base, name);
    let owner_type = cx.thir().node(base).ty;
    let Some(function) = cx.method(owner_type, name) else {
        let text = cx.type_text(owner_type);
        cx.refuse(NO_METHOD, node.0, format!("{text} has no method `{name}`"));
        return cx.invalid(node.0);
    };
    let span = cx.thir().node(base).span;
    let receiver = references::borrow(cx, base, false, span);
    functions::call(cx, node.0, function, vec![receiver], &children[2..])
}

fn read_member(parser: &mut Parser<'_>, base: ExprNode) -> Parsed<ExprNode> {
    parser.advance();
    let name = parser.name()?;
    let mut children = vec![base.0, name];
    let check = match parser.peek() {
        Token::LeftParen => {
            functions::read_arguments(parser, &mut children)?;
            check_method_call
        }
        _ => check_field,
    };
    let span = parser.since(parser.tree.node(base.0).span);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check),
        span,
        &children,
    )))
}

pub static FEATURE: Feature = Feature {
    keywords: &[],
    rules: &[Rule {
        start: Start::Token(Token::Dot),
        then: Some(Token::Name),
        read: Read::Infix(Precedence(95), read_member),
    }],
    codes: &[NO_FIELD, NO_METHOD],
    type_names: &[],
};
