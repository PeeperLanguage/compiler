//! Slices: `&[]T`, an address and a count. A value converts to a slice when
//! it points at elements of the slice's element type, which slices learn by
//! asking the value's kind (`pointee`) and the target's kind (`iterate`),
//! never by naming references or arrays.

use gateway::check::Checker;
use gateway::check::TypeSyntax;
use gateway::facts::Carrier;
use gateway::facts::Facts;
use gateway::feature::Feature;
use gateway::lower::Lowering;
use gateway::mir::Operand;
use gateway::syntax::Parsed;
use gateway::syntax::Parser;
use gateway::syntax::Read;
use gateway::syntax::Rule;
use gateway::syntax::Start;
use gateway::syntax::Token;
use gateway::tree::Kind;
use gateway::tree::TypeNode;
use gateway::types::Converter;
use gateway::types::Iteration;
use gateway::types::TypeId;
use gateway::types::TypeKind;
use gateway::types::no_members;
use gateway::types::no_operators;
use gateway::types::no_pointee;

/// `parts` is the element type.
pub static SLICE: TypeKind = TypeKind {
    facts: |_, _, target| Facts {
        size: 2 * target.pointer_bytes,
        align: target.pointer_bytes,
        carrier: Carrier::Pair,
        copies: true,
        holders: 1,
    },
    text: |cx, ty| format!("&[]{}", cx.type_text(cx.types().entry(ty).parts[0])),
    convert,
    operator: no_operators,
    member: no_members,
    iterate: |cx, ty| {
        Ok(Iteration::Elements {
            element: cx.types().entry(ty).parts[0],
            elements: |lw, value, _| {
                let count_at = lw.offset(value, lw.checker.target.pointer_bytes);
                (lw.load_word(value), lw.load_word(count_at))
            },
        })
    },
    pointee: no_pointee,
};

fn convert(cx: &Checker<'_>, from: TypeId, to: TypeId) -> Result<Converter, String> {
    let element = cx.types().entry(to).parts[0];
    // Only what points at elements converts, not the elements themselves.
    let elements = cx.pointee(from).and_then(|_| cx.iterate(from).ok());
    match elements {
        Some(Iteration::Elements { element: found, .. }) if found == element => Ok(to_slice),
        _ => Err(format!(
            "expected {}, found {}",
            cx.type_text(to),
            cx.type_text(from)
        )),
    }
}

/// From a value pointing at elements: the address of the first and the
/// count, as the target's kind gives them.
fn to_slice(lw: &mut Lowering<'_>, value: Operand, from: TypeId, _: TypeId) -> Operand {
    let Ok(Iteration::Elements { elements, .. }) = lw.checker.iterate(from) else {
        unreachable!("checked: it points at elements");
    };
    let (first, count) = elements(lw, value, from);
    let pair = lw.word_place(2);
    lw.store_word(pair, first);
    let count_at = lw.offset(pair, lw.checker.target.pointer_bytes);
    lw.store_word(count_at, count);
    pair
}

static SLICE_TYPE: TypeSyntax = TypeSyntax {
    resolve: |cx, node| {
        let element = cx.resolve_type(TypeNode(cx.children(node.0)[0]));
        cx.make_type(&SLICE, Box::new([element]), 0)
    },
};

fn read_type(parser: &mut Parser<'_>) -> Parsed<TypeNode> {
    let start = parser.advance();
    parser.expect(Token::LeftBracket, "`[`")?;
    parser.expect(Token::RightBracket, "`]`")?;
    let element = parser.ty()?;
    let span = parser.since(start);
    Ok(TypeNode(parser.tree.push(
        Kind::Type(&SLICE_TYPE),
        span,
        &[element.0],
    )))
}

pub static FEATURE: Feature = Feature {
    keywords: &[],
    rules: &[Rule {
        start: Start::Token(Token::Ampersand),
        then: Some(Token::LeftBracket),
        read: Read::Type(read_type),
    }],
    codes: &[],
    type_names: &[],
};
