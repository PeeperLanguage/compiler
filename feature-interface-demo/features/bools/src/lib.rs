//! `bool`'s name and its literals. The type itself is the core's
//! (`types::BOOL`), as every condition needs it.

use gateway::check::Checker;
use gateway::feature::Feature;
use gateway::feature::TypeName;
use gateway::syntax::Parsed;
use gateway::syntax::Parser;
use gateway::syntax::Read;
use gateway::syntax::Rule;
use gateway::syntax::Start;
use gateway::thir;
use gateway::thir::Data;
use gateway::thir::ThirId;
use gateway::tree::ExprNode;
use gateway::tree::Kind;
use gateway::types::BOOL;
use model::types::TypeId;

fn read_literal(parser: &mut Parser<'_>) -> Parsed<ExprNode> {
    let span = parser.advance();
    Ok(ExprNode(parser.tree.push(
        Kind::Expression(check_literal),
        span,
        &[],
    )))
}

fn check_literal(cx: &mut Checker<'_>, node: ExprNode, _: Option<TypeId>) -> ThirId {
    let value = u64::from(cx.text(node.0) == "true");
    let span = cx.span(node.0);
    cx.push(
        &thir::CONSTANT,
        TypeId::BOOL,
        span,
        Data::Number(value),
        &[],
    )
}

pub static FEATURE: Feature = Feature {
    keywords: &["true", "false"],
    rules: &[
        Rule {
            start: Start::Keyword("true"),
            then: None,
            read: Read::Prefix(read_literal),
        },
        Rule {
            start: Start::Keyword("false"),
            then: None,
            read: Read::Prefix(read_literal),
        },
    ],
    codes: &[],
    type_names: &[TypeName {
        text: "bool",
        kind: &BOOL,
        detail: 0,
    }],
};
