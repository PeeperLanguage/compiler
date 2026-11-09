//! Fixed arrays: `[N]T` and `[a, b, c]`. Like a struct, an array asks its
//! element, so an array of structs needs no code here that knows structs.

use gateway::check::Checker;
use gateway::check::ExpressionKind;
use gateway::check::TypeSyntax;
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
use gateway::tree::TypeNode;
use gateway::types::Iteration;
use gateway::types::TypeId;
use gateway::types::TypeKind;
use gateway::types::no_conversions;
use gateway::types::no_members;
use gateway::types::no_operators;
use gateway::types::no_pointee;

const NO_ELEMENT_TYPE: Code = Code(801);
const COUNT_TOO_BIG: Code = Code(802);

/// `detail` is the count; `parts` is the element type.
pub static ARRAY: TypeKind = TypeKind {
    facts: |count, parts, _| {
        let element = parts[0];
        let count = u64::from(count);
        Facts {
            size: element.size * count,
            align: element.align,
            carrier: match count {
                0 => Carrier::Nothing,
                _ => Carrier::Memory,
            },
            copies: element.copies,
            holders: element.holders * count as usize,
        }
    },
    text: |cx, ty| {
        let entry = cx.types().entry(ty);
        format!("[{}]{}", entry.detail, cx.type_text(entry.parts[0]))
    },
    convert: no_conversions,
    operator: no_operators,
    member: no_members,
    iterate: |cx, ty| {
        Ok(Iteration::Elements {
            element: cx.types().entry(ty).parts[0],
            // The value of an array is its address; its count is its type's.
            elements: |lw, value, ty| {
                let count = lw.checker.types().entry(ty).detail;
                (value, Operand::Constant(u64::from(count)))
            },
        })
    },
    pointee: no_pointee,
};

pub fn array(cx: &mut Checker<'_>, element: TypeId, count: u32) -> TypeId {
    cx.make_type(&ARRAY, Box::new([element]), count)
}

static LITERAL: ExpressionKind = ExpressionKind {
    check: check_literal,
    lower: lower_literal,
    place: None,
};

fn read_literal(parser: &mut Parser<'_>) -> Parsed<ExprNode> {
    let start = parser.advance();
    let mut elements = Vec::new();
    parser.list(Token::RightBracket, |parser| {
        elements.push(parser.expression(Precedence(0))?.0);
        Ok(())
    })?;
    let span = parser.since(start);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(&LITERAL),
        span,
        &elements,
    )))
}

fn check_literal(cx: &mut Checker<'_>, node: ExprNode, expected: Option<TypeId>) -> TypeId {
    let elements = cx.children(node.0);
    let wanted = expected
        .filter(|&ty| cx.types().is(ty, &ARRAY))
        .map(|ty| cx.types().entry(ty).parts[0]);
    let mut element_type = wanted;
    // Every element after the first is checked against the first's type.
    for &element in elements {
        let ty = cx.check_expression(ExprNode(element), element_type);
        element_type.get_or_insert(ty);
    }
    let Some(element_type) = element_type else {
        let message = "an empty array needs its type written".to_string();
        cx.refuse(NO_ELEMENT_TYPE, node.0, message);
        return TypeId::INVALID;
    };
    array(cx, element_type, elements.len() as u32)
}

fn lower_literal(lw: &mut Lowering<'_>, node: ExprNode, ty: TypeId) -> Operand {
    let element = lw.checker.types().entry(ty).parts[0];
    let size = lw.checker.known_facts(element).size;
    let place = lw.temporary_place(ty);
    for (index, &value) in lw.checker.children(node.0).iter().enumerate() {
        let value = lw.lower_expression(ExprNode(value));
        let address = lw.offset(place, index as u64 * size);
        lw.store(address, value, element);
    }
    place
}

// `[N]T`: its children are the count, written as a name node, and `T`.

static ARRAY_TYPE: TypeSyntax = TypeSyntax {
    resolve: |cx, node| {
        let [count, element] = cx.children(node.0)[..] else {
            unreachable!("an array type has a count and an element");
        };
        let element = cx.resolve_type(TypeNode(element));
        let Ok(count) = cx.text(count).parse() else {
            let message = format!("an array holds at most {} elements", u32::MAX);
            cx.refuse(COUNT_TOO_BIG, count, message);
            return TypeId::INVALID;
        };
        array(cx, element, count)
    },
};

fn read_type(parser: &mut Parser<'_>) -> Parsed<TypeNode> {
    let start = parser.advance();
    let count_span = parser.expect(Token::Number, "the count")?;
    let count = parser.tree.push(Kind::Name, count_span, &[]);
    parser.expect(Token::RightBracket, "`]`")?;
    let element = parser.ty()?;
    let span = parser.since(start);
    Ok(TypeNode(parser.tree.push(
        Kind::Type(&ARRAY_TYPE),
        span,
        &[count, element.0],
    )))
}

pub static FEATURE: Feature = Feature {
    keywords: &[],
    rules: &[
        Rule {
            start: Start::Token(Token::LeftBracket),
            then: None,
            read: Read::Prefix(read_literal),
        },
        Rule {
            start: Start::Token(Token::LeftBracket),
            then: Some(Token::Number),
            read: Read::Type(read_type),
        },
    ],
    codes: &[NO_ELEMENT_TYPE, COUNT_TOO_BIG],
    type_names: &[],
};
