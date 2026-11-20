//! The pattern of `let .{ }`.

use arena::Id;
use diagnostics::Diagnostic;
use tree::ast::Pattern;

use token::TokenKind;

use crate::Parser;
use crate::is_name;

impl Parser {
    /// Reads `.{ a, b as c, _ }`: fields to bind, each by its own name or by the
    /// name after `as`, and a `_` for the fields left out, which must come last.
    /// A `=` written for `as` is read as `as`.
    pub(crate) fn struct_pattern(&mut self) -> Id<Pattern> {
        let start = self.next().span.start; // `.`
        let fields = self.delimited(
            TokenKind::LeftBrace,
            TokenKind::RightBrace,
            false,
            |kind| is_name(kind) || kind == TokenKind::Underscore,
            |p| {
                let field_token = p.tok();
                if field_token.kind == TokenKind::Underscore {
                    p.next();
                    return p.add(field_token.span.start, Pattern::Rest);
                }
                let field = p.name();
                let mut binding = None;
                let t = p.tok();
                if t.kind == TokenKind::As || t.kind == TokenKind::Assign {
                    p.next();
                    if t.kind == TokenKind::Assign {
                        let fix = p.fix(t.span, "as");
                        let help = format!("write `{} as name`", p.text(field_token));
                        p.repair(
                            Diagnostic::new(
                                diagnostics::EXPECTED_TOKEN,
                                "a destructure field is renamed with `as`",
                            )
                            .label(&p.tree.file, t.span, "")
                            .help(help, vec![fix]),
                        );
                    }
                    binding = Some(p.name());
                }
                p.add(field_token.span.start, Pattern::Field { field, binding })
            },
        );
        let ids: Vec<_> = self.tree.children.lists.items(fields).collect();
        for &field in ids.iter().take(ids.len().saturating_sub(1)) {
            if let Pattern::Rest = self.tree.patterns[field] {
                let span = self.tree.patterns.span(field);
                self.repair(
                    Diagnostic::new(
                        diagnostics::EXPECTED_TOKEN,
                        "`_` must be the last destructure item",
                    )
                    .label(&self.tree.file, span, ""),
                );
            }
        }
        self.add(start, Pattern::Struct(fields))
    }
}
