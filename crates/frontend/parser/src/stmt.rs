//! Blocks, statements and the patterns of `match` and `let .{ }`.

use ast::{Block, Expr, Id, List, MatchArm, Metadata, Pattern, Stmt};
use diagnostics::Diagnostic;
use token::TokenKind;

use crate::decl::starts_declaration;
use crate::expr::Prec;
use crate::{K, Parser, grow, is_name, starts_statement};

/// Whether `kind` begins a statement (see `Parser::stmt`); any other token
/// begins an expression statement.
pub(crate) fn is_statement_keyword(kind: TokenKind) -> bool {
    matches!(
        kind,
        K::Let
            | K::Const
            | K::Return
            | K::If
            | K::For
            | K::Break
            | K::Continue
            | K::Match
            | K::LeftBrace
    )
}

impl Parser<'_> {
    /// Reads a block. A keyword that begins a declaration but not a statement
    /// means its `}` is missing: the block ends there and the declaration is
    /// read next.
    pub(crate) fn block(&mut self) -> Id<Block> {
        let Some(open) = self.expect(K::LeftBrace) else {
            return self.add(
                self.last_end,
                Block {
                    statements: List::EMPTY,
                },
            );
        };
        let mark = self.scratch.len();
        loop {
            let kind = self.tok().kind;
            if kind == K::RightBrace
                || kind == K::Eof
                || starts_declaration(kind) && !is_statement_keyword(kind)
            {
                break;
            }
            if kind == K::Semicolon {
                // an empty statement: a matter of style, left to a lint
                self.next();
                continue;
            }
            if starts_statement(kind) {
                self.recovering = false;
            }
            let statement = self.stmt();
            self.scratch.push(statement.into());
        }
        let statements = self.finish_list(mark);
        if self.at(K::RightBrace) {
            self.recovering = false; // the block's own `}`: in step again
        }
        self.close(K::RightBrace, open);
        self.add(open.span.start, Block { statements })
    }

    /// Reads a statement: one a keyword begins, or an expression statement, or
    /// an assignment `target = value;`.
    fn stmt(&mut self) -> Id<Stmt> {
        grow(|| match self.tok().kind {
            K::Let => self.let_statement(),
            K::Const => {
                let decl = self.const_decl(Metadata::NONE);
                let span = self.tree.decls.span(decl);
                self.tree.add(Stmt::Const(decl), span)
            }
            K::Return => self.return_statement(),
            K::If => self.if_statement(),
            K::For => self.for_statement(),
            K::Break | K::Continue => self.jump_statement(),
            K::Match => self.match_statement(),
            K::LeftBrace => {
                let block = self.block();
                self.block_statement(block)
            }
            _ => {
                let start = self.tok().span.start;
                let target = self.expr(Prec::Lowest);
                if self.at(K::Assign) {
                    self.next();
                    let value = self.expr(Prec::Lowest);
                    self.end();
                    return self.add(start, Stmt::Assign { target, value });
                }
                self.end();
                self.add(start, Stmt::Expression(target))
            }
        })
    }

    /// Makes `block` a statement: a block inside a block, or an `else` block.
    fn block_statement(&mut self, block: Id<Block>) -> Id<Stmt> {
        let span = self.tree.blocks.span(block);
        self.tree.add(Stmt::Block(block), span)
    }

    /// Reads `let mut name: Type = value;`, where `mut`, the type and the value
    /// may be left out, or `let .{ fields } = value;`.
    fn let_statement(&mut self) -> Id<Stmt> {
        let start = self.next().span.start;
        if self.at(K::Dot) {
            let pattern = self.struct_pattern();
            self.expect(K::Assign);
            let value = self.expr(Prec::Lowest);
            self.end();
            return self.add(start, Stmt::Destructure { pattern, value });
        }
        let mutable = self.at(K::Mut);
        if mutable {
            self.next();
        }
        let (name, ty, value) = self.binding();
        self.add(
            start,
            Stmt::Let {
                mutable,
                name,
                ty,
                value,
            },
        )
    }

    /// Reads `if condition { } else if ... else { }`. A `=` written for `==` in
    /// the condition is read as `==`.
    fn if_statement(&mut self) -> Id<Stmt> {
        grow(|| {
            let start = self.next().span.start;
            let mut condition = self.expr(Prec::Lowest);
            let t = self.tok();
            if t.kind == K::Assign {
                self.next();
                let fix = self.fix(t.span, "==");
                self.repair(
                    Diagnostic::new(
                        diagnostics::EXPECTED_TOKEN,
                        "expected `==` in condition, found `=`",
                    )
                    .label(self.file, t.span, "")
                    .help("compare with `==`", vec![fix]),
                );
                let right = self.expr(Prec::Lowest);
                let left_start = self.tree.exprs.span(condition).start;
                condition = self.add(
                    left_start,
                    Expr::Binary {
                        op: K::EqualEqual,
                        left: condition,
                        right,
                    },
                );
            }
            let then = self.body("if");
            let mut otherwise = None;
            if self.at(K::Else) {
                self.next();
                otherwise = if self.at(K::If) {
                    Some(self.if_statement())
                } else {
                    self.body("else").map(|block| self.block_statement(block))
                };
            }
            self.add(
                start,
                Stmt::If {
                    condition,
                    then,
                    otherwise,
                },
            )
        })
    }

    /// Reads the block of an if, else or for; when it is missing, it reports
    /// "missing <what> body" where the block should start.
    fn body(&mut self, what: &str) -> Option<Id<Block>> {
        if self.at(K::LeftBrace) {
            return Some(self.block());
        }
        let at = self.here();
        self.fail(
            Diagnostic::new(diagnostics::EXPECTED_TOKEN, format!("missing {what} body")).label(
                self.file,
                at,
                "expected `{` here",
            ),
        );
        None
    }

    /// Reads `for { }`, `for condition { }`, `for value in items { }` or
    /// `for index, value in items { }`. The iterable may be a range.
    fn for_statement(&mut self) -> Id<Stmt> {
        let start = self.next().span.start;
        if self.at(K::Ident) && matches!(self.ahead(1).kind, K::In | K::Comma) {
            let mut index = None;
            let mut value = self.name();
            if self.at(K::Comma) {
                self.next();
                index = Some(value);
                value = self.name();
            }
            self.expect(K::In);
            let iterable = self.index_operand();
            let body = self.body("for");
            return self.add(
                start,
                Stmt::ForIn {
                    index,
                    value,
                    iterable,
                    body,
                },
            );
        }
        let mut condition = None;
        if !self.at(K::LeftBrace) {
            let value = self.expr(Prec::Lowest);
            if self.at(K::In) {
                let span = self.tree.exprs.span(value);
                self.fail(
                    Diagnostic::new(diagnostics::INVALID_EXPRESSION, "invalid loop variable")
                        .label(self.file, span, "expected an identifier before `in`"),
                );
            }
            condition = Some(value);
        }
        let body = self.body("for");
        self.add(start, Stmt::Loop { condition, body })
    }

    /// Reads `break;` or `continue;`. Loop labels are not part of the language
    /// yet; a label is reported and skipped.
    fn jump_statement(&mut self) -> Id<Stmt> {
        let keyword = self.next();
        let t = self.tok();
        if t.kind == K::Ident {
            self.next();
            self.repair(
                Diagnostic::new(
                    diagnostics::INVALID_STATEMENT,
                    format!("labeled {} is not supported yet", keyword.kind.name()),
                )
                .label(
                    self.file,
                    t.span,
                    "loop labels are planned for a later release",
                ),
            );
        }
        self.end();
        let node = if keyword.kind == K::Continue {
            Stmt::Continue
        } else {
            Stmt::Break
        };
        self.add(keyword.span.start, node)
    }

    /// Reads `match subject { arms }`. An arm is `_ => { }`, `Enum::Case => { }`
    /// or `Enum::Case with pattern => { }`; arms are not separated by commas.
    fn match_statement(&mut self) -> Id<Stmt> {
        let start = self.next().span.start;
        let subject = self.expr(Prec::Lowest);
        let mut arms = List::EMPTY;
        if let Some(open) = self.expect(K::LeftBrace) {
            let mark = self.scratch.len();
            // Every arm begins with a name (`_` or a path) and reads at least it,
            // so the loop always moves on.
            while self.at(K::Ident) {
                let arm_start = self.tok().span.start;
                let (pattern, body) = self.match_arm();
                let arm = self.add(arm_start, MatchArm { pattern, body });
                self.scratch.push(arm.into());
                let t = self.tok();
                if t.kind == K::Comma {
                    self.next();
                    let fix = self.fix(t.span, "");
                    self.repair(
                        Diagnostic::new(
                            diagnostics::EXPECTED_TOKEN,
                            "match arms do not use commas",
                        )
                        .label(self.file, t.span, "")
                        .help("remove this comma", vec![fix]),
                    );
                }
            }
            arms = self.finish_list(mark);
            self.close(K::RightBrace, open);
        }
        self.add(start, Stmt::Match { subject, arms })
    }

    /// Reads one arm's pattern, its `=>` and its block. A missing `=>` before
    /// the block is read as if it were there.
    fn match_arm(&mut self) -> (Id<Pattern>, Id<Block>) {
        let t = self.tok();
        let pattern = if t.kind == K::Ident && self.text(t) == "_" {
            let start = self.next().span.start;
            self.add(start, Pattern::Wildcard)
        } else {
            self.case_pattern()
        };
        if self.at(K::LeftBrace) {
            let at = self.here();
            let fix = self.fix(at, " =>");
            self.repair(
                Diagnostic::new(
                    diagnostics::EXPECTED_TOKEN,
                    "expected `=>` after match case",
                )
                .label(self.file, at, "")
                .help("add missing `=>`", vec![fix]),
            );
        } else if self.expect(K::FatArrow).is_none() {
            self.sync();
        }
        (pattern, self.block())
    }

    /// Reads `Enum::Case`, perhaps followed by `with` and the pattern of its
    /// payload. The case is a full path.
    fn case_pattern(&mut self) -> Id<Pattern> {
        grow(|| {
            let start = self.tok().span.start;
            let case = self.type_expr();
            self.require_path(case);
            let inner = self.at(K::With).then(|| {
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
            K::None => {
                self.next();
                self.add(t.span.start, Pattern::None)
            }
            K::Dot => self.struct_pattern(),
            K::Ident if !matches!(self.ahead(1).kind, K::ColonColon | K::Less) => {
                let name = self.name();
                self.tree.add(Pattern::Binding, name)
            }
            K::Ident => self.case_pattern(),
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
    fn struct_pattern(&mut self) -> Id<Pattern> {
        let start = self.next().span.start; // `.`
        let mut fields = List::EMPTY;
        if let Some(open) = self.expect(K::LeftBrace) {
            fields = self.items(K::RightBrace, false, is_name, |p| {
                let field_token = p.tok();
                if p.text(field_token) == "_" {
                    p.next();
                    return p.add(field_token.span.start, Pattern::Rest);
                }
                let field = p.name();
                let mut binding = None;
                let t = p.tok();
                if t.kind == K::As || t.kind == K::Assign {
                    p.next();
                    if t.kind == K::Assign {
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
            });
            self.close(K::RightBrace, open);
        }
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

    /// Reads `return value;`; the value may be left out.
    fn return_statement(&mut self) -> Id<Stmt> {
        let start = self.next().span.start;
        let value = (!self.at(K::Semicolon)).then(|| self.expr(Prec::Lowest));
        self.end();
        self.add(start, Stmt::Return(value))
    }
}

#[cfg(test)]
mod tests {
    use crate::tests::{dump_of, messages, one_error};

    #[test]
    fn parses_statements() {
        let text = "fn f() {
    let mut n: i32 = 0;
    let .{ x, y as z, _ } = point;
    const limit = 10;
    n = n + 1;
    if n == 1 { } else if n == 2 { } else { }
    for { break; }
    for n < limit { continue; }
    for item in items { }
    for i, item in items[1..] { }
    match shape {
        Shape::Dot => { }
        Shape::Line with .{ end } => { }
        Shape::Maybe with none => { }
        Shape::Wrap with Inner::Leaf with value => { }
        _ => { }
    }
    { }
}
";
        let want = "module
  function declaration
    declared name f
    block
      let statement mut
        declared name n
        type name i32
        number literal 0
      destructure statement
        struct pattern
          field pattern
            field name x
          field pattern
            field name y
            declared name z
          rest pattern _
        name reference point
      const declaration
        declared name limit
        number literal 10
      assign statement
        name reference n
        binary expression +
          name reference n
          number literal 1
      if statement
        binary expression ==
          name reference n
          number literal 1
        block { }
        if statement
          binary expression ==
            name reference n
            number literal 2
          block { }
          block { }
      loop statement
        block
          break statement break;
      loop statement
        binary expression <
          name reference n
          name reference limit
        block
          continue statement continue;
      for in statement
        declared name item
        name reference items
        block { }
      for in statement
        declared name i
        declared name item
        index expression
          name reference items
          range ..
            number literal 1
        block { }
      match statement
        name reference shape
        match arm
          case pattern
            path
              type name Shape
              type name Dot
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Line
            struct pattern
              field pattern
                field name end
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Maybe
            none pattern none
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Wrap
            case pattern
              path
                type name Inner
                type name Leaf
              declared name value
          block { }
        match arm
          wildcard pattern _
          block { }
      block { }
";
        assert!(messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    #[test]
    fn statement_errors() {
        for (text, message, fixed) in [
            (
                "if a = b {}",
                "expected `==` in condition, found `=`",
                Some("if a == b {}"),
            ),
            ("if a", "missing if body", None),
            ("if a {} else", "missing else body", None),
            ("for x in items", "missing for body", None),
            ("for a.b in x {}", "invalid loop variable", None),
            ("break outer;", "labeled break is not supported yet", None),
            (
                "match x { A::B {} }",
                "expected `=>` after match case",
                Some("match x { A::B => {} }"),
            ),
            (
                "match x { A::B => {}, _ => {} }",
                "match arms do not use commas",
                Some("match x { A::B => {} _ => {} }"),
            ),
            (
                "match x { a => {} }",
                "enum case must use a fully named path",
                None,
            ),
            (
                "match x { A::B with => {} }",
                "expected a pattern after `with`",
                None,
            ),
            (
                "let .{ a = b } = v;",
                "a destructure field is renamed with `as`",
                Some("let .{ a as b } = v;"),
            ),
            (
                "let .{ _, a } = v;",
                "`_` must be the last destructure item",
                None,
            ),
        ] {
            let fixed = fixed.map(|f| format!("fn f() {{ {f} }}"));
            one_error(&format!("fn f() {{ {text} }}"), message, fixed.as_deref());
        }
    }
}
