//! The patterns of `match` arms and `let .{ }`.

use arena::Id;
use diagnostics::Diagnostic;
use model::ast::Pattern;

use token::TokenKind;

use crate::Parser;
use crate::grow;
use crate::is_name;

impl Parser {
    /// Reads `Enum::Case`, perhaps followed by `with` and the pattern of its
    /// payload. The case is a full path.
    pub(crate) fn case_pattern(&mut self) -> Id<Pattern> {
        grow(|| {
            let start = self.tok().span.start;
            let case = self.type_expr();
            self.require_path(case);
            let inner = self.at(TokenKind::With).then(|| {
                self.next();
                self.payload_pattern()
            });
            self.add(start, Pattern::Case { case, inner })
        })
    }

    /// Reads the pattern after `with`: `none`, `_`, a name to bind,
    /// `.{ fields }`, or another case.
    fn payload_pattern(&mut self) -> Id<Pattern> {
        let t = self.tok();
        match t.kind {
            TokenKind::None => {
                self.next();
                self.add(t.span.start, Pattern::None)
            }
            TokenKind::Underscore => {
                self.next();
                self.add(t.span.start, Pattern::Wildcard)
            }
            TokenKind::Dot => self.struct_pattern(),
            TokenKind::Ident
                if !matches!(self.ahead(1).kind, TokenKind::ColonColon | TokenKind::Less) =>
            {
                let name = self.name();
                self.tree.add(Pattern::Binding, name.span)
            }
            TokenKind::Ident => self.case_pattern(),
            _ => {
                self.fail(
                    Diagnostic::new(
                        diagnostics::EXPECTED_TOKEN,
                        "expected a pattern after `with`",
                    )
                    .label(&self.tree.file, t.span, "")
                    .help(
                        "write `none`, `_`, a name, `.{ field }`, or a case of the payload's enum",
                        Vec::new(),
                    ),
                );
                self.add(self.last_end, Pattern::Invalid)
            }
        }
    }

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
