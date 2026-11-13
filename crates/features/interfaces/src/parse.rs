//! Reading interface types.

use arena::Id;
use diagnostics::Diagnostic;
use parser::Grammar;
use parser::Parser;
use parser::Rules;
use parser::decl::type_decl;
use syntax::ast::Decl;
use syntax::ast::Function;
use syntax::ast::Metadata;
use syntax::ast::Type;
use token::TokenKind;

use crate::InterfaceType;

/// Adds the rules that read interfaces to `rules`.
pub const fn rows<L>(rules: &mut Rules<L>)
where
    L: Grammar<Type: From<InterfaceType<L>>>,
{
    rules.declaration(TokenKind::Iface, type_decl);
    rules.type_start(TokenKind::Iface, interface_type);
}

/// Reads `{ fn name(parameters) -> Result, ... }`, the methods of an
/// interface type that starts at `start`: function signatures without
/// bodies. A method takes no default values.
fn interface_type<L>(p: &mut Parser<L>, start: u32) -> Id<Type<L>>
where
    L: Grammar<Type: From<InterfaceType<L>>>,
{
    let methods = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        true,
        |kind| kind == TokenKind::Fn,
        |p| {
            let start = p.next().span.start;
            let (name, signature) = p.named_signature();
            let parameters: Vec<_> = p.tree.children.lists.items(signature.parameters).collect();
            for parameter in parameters {
                if let Some(value) = p.tree.children.params[parameter].default {
                    let span = p.tree.exprs.span(value);
                    p.repair(
                        Diagnostic::new(
                            diagnostics::INVALID_DECLARATION,
                            "interface method defaults are not supported",
                        )
                        .label(&p.tree.file, span, ""),
                    );
                }
            }
            p.add(
                start,
                Decl::Function(Function {
                    metadata: Metadata::NONE,
                    owner: None,
                    name,
                    signature,
                    body: None,
                }),
            )
        },
    );
    p.add(start, Type::Feature(InterfaceType { methods }.into()))
}
