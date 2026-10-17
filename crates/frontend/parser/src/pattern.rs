//! The patterns of `match` arms and `let .{ }`.

use ast::{Id, Pattern};
use diagnostics::Diagnostic;

use token::TokenKind;

use crate::{Parser, grow, is_name};

impl Parser<'_> {
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

    /// Reads the pattern after `with`: `none`, a name to bind, `.{ fields }`, or
    /// another case.
    fn payload_pattern(&mut self) -> Id<Pattern> {
        let t = self.tok();
        match t.kind {
            TokenKind::None => {
                self.next();
                self.add(t.span.start, Pattern::None)
            }
            TokenKind::Dot => self.struct_pattern(),
            TokenKind::Ident
                if !matches!(self.ahead(1).kind, TokenKind::ColonColon | TokenKind::Less) =>
            {
                let name = self.name();
                self.tree.add(Pattern::Binding, name)
            }
            TokenKind::Ident => self.case_pattern(),
            _ => {
                self.fail(
                    Diagnostic::new(
                        diagnostics::EXPECTED_TOKEN,
                        "expected a pattern after `with`",
                    )
                    .label(self.file, t.span, "")
                    .help(
                        "write `none`, a name, `.{ field }`, or a case of the payload's enum",
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
            is_name,
            |p| {
                let field_token = p.tok();
                if p.text(field_token) == "_" {
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
                            .label(p.file, t.span, "")
                            .help(help, vec![fix]),
                        );
                    }
                    binding = Some(p.name());
                }
                p.add(field_token.span.start, Pattern::Field { field, binding })
            },
        );
        let ids: Vec<_> = self.tree.list(fields).collect();
        for &field in ids.iter().take(ids.len().saturating_sub(1)) {
            if let Pattern::Rest = self.tree.patterns[field] {
                let span = self.tree.patterns.span(field);
                self.repair(
                    Diagnostic::new(
                        diagnostics::EXPECTED_TOKEN,
                        "`_` must be the last destructure item",
                    )
                    .label(self.file, span, ""),
                );
            }
        }
        self.add(start, Pattern::Struct(fields))
    }
}
