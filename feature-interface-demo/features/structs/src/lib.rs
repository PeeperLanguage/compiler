//! Structs: `struct Name { field: type, ... }` and `Name.{ field = value }`.
//! A struct asks its fields: its facts are made from theirs, so a field of
//! any type works the day that type's kind gives its facts.

use gateway::check::Checker;
use gateway::check::DeclarationKind;
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
use gateway::tree::DeclNode;
use gateway::tree::ExprNode;
use gateway::tree::Kind;
use gateway::tree::NodeId;
use gateway::tree::TypeNode;
use gateway::types::Field;
use gateway::types::TypeKind;
use gateway::types::no_conversions;
use gateway::types::no_operators;
use gateway::types::no_pointee;
use model::facts::Carrier;
use model::facts::Facts;
use model::mir::InstructionKind;
use model::mir::Part;
use model::types::TypeId;

const NOT_A_STRUCT: Code = Code(700);
const FIELD_MISSING: Code = Code(701);
const NO_SUCH_FIELD: Code = Code(702);
const FIELD_REPEATED: Code = Code(703);

/// `parts` are the fields' types.
pub static STRUCT: TypeKind = TypeKind {
    facts: |_, fields, _| {
        let align = fields.iter().map(|field| field.align).max().unwrap_or(1);
        let end = offsets(fields.iter().copied())
            .last()
            .map_or(0, |(_, end)| end);
        let size = end.next_multiple_of(align);
        Facts {
            size,
            align,
            carrier: match fields.is_empty() {
                true => Carrier::Nothing,
                false => Carrier::Memory,
            },
            copies: false,
            holders: fields.iter().map(|field| field.holders).sum(),
        }
    },
    text: |cx, ty| cx.text(name_of(cx, ty)).to_string(),
    convert: no_conversions,
    operator: no_operators,
    field: |cx, ty, name| {
        let index = field_names(cx, ty).position(|field| cx.text(field) == name)?;
        let parts = &cx.types().entry(ty).parts;
        let facts: Vec<Facts> = parts.iter().map(|&part| cx.known_facts(part)).collect();
        // The fields' holders follow each other in field order, as the
        // struct's facts count them.
        let before = facts[..index]
            .iter()
            .map(|field| field.holders)
            .sum::<usize>();
        Some(Field {
            index: index as u32,
            offset: offsets(facts.iter().copied()).nth(index)?.0,
            ty: parts[index],
            first_holder: before as u32,
            holders: facts[index].holders as u32,
        })
    },
    pointee: no_pointee,
};

/// Each field's start and end, in order: the one layout rule, read lazily
/// so a field lookup stops at its field.
fn offsets(fields: impl Iterator<Item = Facts>) -> impl Iterator<Item = (u64, u64)> {
    fields.scan(0u64, |end, field| {
        let start = end.next_multiple_of(field.align);
        *end = start + field.size;
        Some((start, *end))
    })
}

// A declaration's children: the name, then a name and a type per field.

fn name_of(cx: &Checker<'_>, ty: TypeId) -> NodeId {
    cx.children(cx.declaration(ty).0)[0]
}

fn field_names<'a>(cx: &Checker<'a>, ty: TypeId) -> impl Iterator<Item = NodeId> + 'a {
    cx.children(cx.declaration(ty).0)[1..]
        .iter()
        .step_by(2)
        .copied()
}

static STRUCT_DECLARATION: DeclarationKind = DeclarationKind {
    declare: |cx, node| {
        cx.declare_type(cx.children(node.0)[0], &STRUCT, node);
    },
    define: |cx, node| {
        let children = cx.children(node.0);
        let ty = cx.type_named(children[0]);
        // The name, then each field's name and type: none for `struct E { }`.
        let fields: Vec<TypeId> = (children.iter().skip(2).step_by(2))
            .map(|&field| cx.resolve_type(TypeNode(field)))
            .collect();
        cx.define_type(ty, fields.into());
    },
    check: |_, _| {},
};

fn read_declaration(parser: &mut Parser<'_>) -> Parsed<DeclNode> {
    let start = parser.advance();
    let mut children = vec![parser.name()?];
    parser.expect(Token::LeftBrace, "`{`")?;
    parser.list(Token::RightBrace, |parser| {
        children.push(parser.name()?);
        parser.expect(Token::Colon, "`:`")?;
        children.push(parser.ty()?.0);
        Ok(())
    })?;
    let span = parser.since(start);
    Ok(DeclNode(parser.tree.push(
        Kind::Declaration(&STRUCT_DECLARATION),
        span,
        &children,
    )))
}

// A literal's children: the struct's name, then a name and a value per
// field written.

fn read_literal(parser: &mut Parser<'_>, name: ExprNode) -> Parsed<ExprNode> {
    parser.advance();
    parser.advance();
    let mut children = vec![name.0];
    parser.list(Token::RightBrace, |parser| {
        children.push(parser.name()?);
        parser.expect(Token::Equal, "`=`")?;
        children.push(parser.expression(Precedence(0))?.0);
        Ok(())
    })?;
    let span = parser.since(parser.tree.node(name.0).span);
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check_literal),
        span,
        &children,
    )))
}

fn check_literal(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> ThirId {
    let children = cx.children(node.0);
    let ty = cx.type_named(children[0]);
    if ty == TypeId::INVALID {
        return cx.invalid(node.0);
    }
    if !cx.types().is(ty, &STRUCT) {
        let text = cx.type_text(ty);
        cx.refuse(NOT_A_STRUCT, children[0], format!("{text} is not a struct"));
        return cx.invalid(node.0);
    }
    let fields: Vec<NodeId> = field_names(cx, ty).collect();
    let mut given = vec![false; fields.len()];
    let (mut values, mut indexes) = (Vec::new(), Vec::new());
    // Every written value is checked once, even one that is refused.
    for pair in children[1..].chunks(2) {
        let name = cx.text(pair[0]);
        let index = fields.iter().position(|&field| cx.text(field) == name);
        let field_type = index.map(|index| cx.types().entry(ty).parts[index]);
        let kept = match index {
            Some(index) if given[index] => {
                let message = format!("field `{name}` is given twice");
                cx.refuse(FIELD_REPEATED, pair[0], message);
                None
            }
            Some(index) => {
                given[index] = true;
                Some(index as u32)
            }
            None => {
                let message = format!("the struct has no field `{name}`");
                cx.refuse(NO_SUCH_FIELD, pair[1], message);
                None
            }
        };
        let value = cx.check_expression(ExprNode(pair[1]), field_type);
        if let Some(index) = kept {
            values.push(value);
            indexes.push(index);
        }
    }
    for (&field, _) in fields.iter().zip(given).filter(|(_, given)| !given) {
        let message = format!("field `{}` is not given", cx.text(field));
        cx.refuse(FIELD_MISSING, node.0, message);
    }
    let span = cx.span(node.0);
    cx.push(&LITERAL, ty, span, Data::Fields(indexes.into()), &values)
}

/// The values in the order written; the parts in the fields' order.
static LITERAL: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let node = checker.thir().node(id);
        let Data::Fields(indexes) = &node.data else {
            unreachable!("a struct literal names its fields");
        };
        let fields = &checker.types().entry(node.ty).parts;
        let facts = fields.iter().map(|&field| checker.known_facts(field));
        let starts: Vec<u64> = offsets(facts).map(|(start, _)| start).collect();
        let mut parts: Vec<(u32, Part)> = Vec::new();
        for (&value, &index) in checker.thir().children(id).iter().zip(indexes) {
            let part = Part {
                offset: starts[index as usize],
                ty: fields[index as usize],
                value: lw.value(value),
            };
            parts.push((index, part));
        }
        parts.sort_by_key(|&(index, _)| index);
        let parts = parts.into_iter().map(|(_, part)| part).collect();
        lw.emit(InstructionKind::Aggregate(parts), node.ty)
    },
    place: None,
};

pub static FEATURE: Feature = Feature {
    keywords: &["struct"],
    rules: &[
        Rule {
            start: Start::Keyword("struct"),
            then: None,
            read: Read::Declaration(read_declaration),
        },
        Rule {
            start: Start::Token(Token::Dot),
            then: Some(Token::LeftBrace),
            read: Read::Infix(Precedence(95), read_literal),
        },
    ],
    codes: &[NOT_A_STRUCT, FIELD_MISSING, NO_SUCH_FIELD, FIELD_REPEATED],
    type_names: &[],
};
