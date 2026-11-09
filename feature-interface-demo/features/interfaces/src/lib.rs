//! Owned interfaces: `iface Name { fn method(&self) -> type, ... }`. A value
//! is two words, the address of the value it took and a method table, so
//! all a struct or an array needs to hold one is these facts. The table is
//! data kept for each pair of a type and an interface; no code is written
//! per pair.

use gateway::check::Checker;
use gateway::check::DeclarationKind;
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
use gateway::tree::DeclNode;
use gateway::tree::Kind;
use gateway::tree::NodeId;
use gateway::tree::TypeNode;
use gateway::types::Converter;
use gateway::types::Member;
use gateway::types::TypeId;
use gateway::types::TypeKind;
use gateway::types::no_operators;
use gateway::types::no_pointee;
use gateway::types::not_iterable;

/// `parts` are the methods' return types, in the declaration's order.
pub static INTERFACE: TypeKind = TypeKind {
    facts: |_, _, target| Facts {
        size: 2 * target.pointer_bytes,
        align: target.pointer_bytes,
        carrier: Carrier::Pair,
        copies: false,
        holders: 1,
    },
    text: |cx, ty| cx.text(cx.children(cx.declaration(ty).0)[0]).to_string(),
    convert,
    operator: no_operators,
    member: |cx, ty, name| {
        let slot = method_names(cx, ty).position(|method| cx.text(method) == name)?;
        Some(Member::Dynamic {
            slot: slot as u32,
            result: cx.types().entry(ty).parts[slot],
            call: dynamic_call,
        })
    },
    iterate: not_iterable,
    pointee: no_pointee,
};

// A declaration's children: the name, then a name and a return type (or
// missing) per method.

fn method_names<'a>(cx: &Checker<'a>, ty: TypeId) -> impl Iterator<Item = NodeId> + 'a {
    cx.children(cx.declaration(ty).0)[1..]
        .iter()
        .step_by(2)
        .copied()
}

/// A value of `from` converts when `from` has a method of each name taking
/// only `&self`, with the same return type.
fn convert(cx: &Checker<'_>, from: TypeId, to: TypeId) -> Result<Converter, String> {
    for (slot, method) in method_names(cx, to).enumerate() {
        let name = cx.text(method);
        let result = cx.types().entry(to).parts[slot];
        let found = functions::method(cx, from, name)
            .map(|(_, parameters, found)| (parameters.len(), found));
        if found != Some((1, result)) {
            let (from, to) = (cx.type_text(from), cx.type_text(to));
            let result = cx.type_text(result);
            return Err(format!(
                "{from} has no method `fn {name}(&self) -> {result}`, which {to} needs"
            ));
        }
    }
    Ok(to_interface)
}

/// Moves the value into a place of its own, and pairs that place's address
/// with the table of `from`'s methods for `to`.
fn to_interface(lw: &mut Lowering<'_>, value: Operand, from: TypeId, to: TypeId) -> Operand {
    let functions: Vec<u32> = (method_names(lw.checker, to))
        .map(|method| {
            let name = lw.checker.text(method);
            lw.checker
                .method(from, name)
                .expect("checked: it has the method")
                .0
        })
        .collect();
    let table = lw.table(functions);
    let held = lw.temporary_place(from);
    lw.store(held, value, from);
    let pair = lw.word_place(2);
    lw.store_word(pair, held);
    let table_at = lw.offset(pair, lw.checker.target.pointer_bytes);
    lw.store_word(table_at, Operand::Constant(u64::from(table)));
    pair
}

/// The function at `slot` of the value's table, called on the address the
/// value holds.
fn dynamic_call(lw: &mut Lowering<'_>, value: Operand, slot: u32) -> (Operand, Operand) {
    let held = lw.load_word(value);
    let table_at = lw.offset(value, lw.checker.target.pointer_bytes);
    let table = lw.load_word(table_at);
    (lw.table_entry(table, slot), held)
}

static INTERFACE_DECLARATION: DeclarationKind = DeclarationKind {
    declare: |cx, node| {
        cx.declare_type(cx.children(node.0)[0], &INTERFACE, node);
    },
    define: |cx, node| {
        let children = cx.children(node.0);
        let ty = cx.type_named(children[0]);
        let results: Vec<TypeId> = (children[2..].iter().step_by(2))
            .map(|&result| match cx.tree.is_missing(result) {
                true => TypeId::VOID,
                false => cx.resolve_type(TypeNode(result)),
            })
            .collect();
        cx.define_type(ty, results.into());
    },
    check: |_, _| {},
    lower: |_, _| {},
};

/// Methods take `&self` only, in this demo.
fn read_declaration(parser: &mut Parser<'_>) -> Parsed<DeclNode> {
    let start = parser.advance();
    let mut children = vec![parser.name()?];
    parser.expect(Token::LeftBrace, "`{`")?;
    parser.list(Token::RightBrace, |parser| {
        parser.expect_keyword("fn")?;
        children.push(parser.name()?);
        parser.expect(Token::LeftParen, "`(`")?;
        parser.expect(Token::Ampersand, "`&self`")?;
        parser.exact_name("self")?;
        parser.expect(Token::RightParen, "`)`")?;
        children.push(match parser.eat(Token::Arrow) {
            true => parser.ty()?.0,
            false => parser.missing(),
        });
        Ok(())
    })?;
    let span = parser.since(start);
    Ok(DeclNode(parser.tree.push(
        Kind::Declaration(&INTERFACE_DECLARATION),
        span,
        &children,
    )))
}

pub static FEATURE: Feature = Feature {
    keywords: &["iface"],
    rules: &[Rule {
        start: Start::Keyword("iface"),
        then: None,
        read: Read::Declaration(read_declaration),
    }],
    codes: &[],
    type_names: &[],
};
