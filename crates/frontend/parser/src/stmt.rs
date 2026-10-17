//! Blocks and statements.

use arena::{Id, List};
use diagnostics::Diagnostic;
use model::syntax::{Block, Expr, MatchArm, Metadata, Pattern, Stmt};
use token::TokenKind;

use crate::decl::{const_decl, starts_declaration};
use crate::expr::Precedence;
use crate::{Parser, grow, starts_statement};

/// Reads the statement the current keyword begins.
type StatementRule = fn(&mut Parser<'_>) -> Id<Stmt>;

/// The reader of each statement a keyword begins, indexed by token kind; any
/// other token begins an expression statement.
static STATEMENTS: [Option<StatementRule>; 256] = {
    let mut statements: [Option<StatementRule>; 256] = [None; 256];
    statements[TokenKind::Let as usize] = Some(let_statement);
    statements[TokenKind::Const as usize] = Some(|p| {
        let decl = const_decl(p, Metadata::NONE);
        let span = p.tree.decls.span(decl);
        p.tree.add(Stmt::Const(decl), span)
    });
    statements[TokenKind::Return as usize] = Some(return_statement);
    statements[TokenKind::If as usize] = Some(if_statement);
    statements[TokenKind::For as usize] = Some(for_statement);
    statements[TokenKind::Break as usize] = Some(jump_statement);
    statements[TokenKind::Continue as usize] = Some(jump_statement);
    statements[TokenKind::Match as usize] = Some(match_statement);
    statements[TokenKind::LeftBrace as usize] = Some(|p| {
        let block = p.block();
        p.block_statement(block)
    });
    statements
};

/// Whether `kind` begins a statement other than an expression statement.
pub(crate) fn is_statement_keyword(kind: TokenKind) -> bool {
    STATEMENTS[kind as usize].is_some()
}

impl Parser<'_> {
    /// Reads a block. A keyword that begins a declaration but not a statement
    /// means its `}` is missing: the block ends there and the declaration is
    /// read next.
    pub(crate) fn block(&mut self) -> Id<Block> {
        let Some(open) = self.expect(TokenKind::LeftBrace) else {
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
            if kind == TokenKind::RightBrace
                || kind == TokenKind::Eof
                || starts_declaration(kind) && !is_statement_keyword(kind)
            {
                break;
            }
            if kind == TokenKind::Semicolon {
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
        if self.at(TokenKind::RightBrace) {
            self.recovering = false; // the block's own `}`: in step again
        }
        self.close(TokenKind::RightBrace, open);
        self.add(open.span.start, Block { statements })
    }

    /// Reads a statement: one a keyword begins, or an expression statement, or
    /// an assignment `target = value;`.
    fn stmt(&mut self) -> Id<Stmt> {
        grow(|| {
            if let Some(read) = STATEMENTS[self.tok().kind as usize] {
                return read(self);
            }
            let start = self.tok().span.start;
            let target = self.expr(Precedence::Lowest);
            if self.at(TokenKind::Assign) {
                self.next();
                let value = self.expr(Precedence::Lowest);
                self.end();
                return self.add(start, Stmt::Assign { target, value });
            }
            self.end();
            self.add(start, Stmt::Expression(target))
        })
    }

    /// Makes `block` a statement: a block inside a block, or an `else` block.
    fn block_statement(&mut self, block: Id<Block>) -> Id<Stmt> {
        let span = self.tree.blocks.span(block);
        self.tree.add(Stmt::Block(block), span)
    }

    /// Reads the block of an if, else or for; when it is missing, it reports
    /// "missing <what> body" where the block should start.
    fn body(&mut self, what: &str) -> Option<Id<Block>> {
        if self.at(TokenKind::LeftBrace) {
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

    /// Reads one arm's pattern, its `=>` and its block. A missing `=>` before
    /// the block is read as if it were there.
    fn match_arm(&mut self) -> (Id<Pattern>, Id<Block>) {
        let t = self.tok();
        let pattern = if t.kind == TokenKind::Ident && self.text(t) == "_" {
            let start = self.next().span.start;
            self.add(start, Pattern::Wildcard)
        } else {
            self.case_pattern()
        };
        if self.at(TokenKind::LeftBrace) {
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
        } else if self.expect(TokenKind::FatArrow).is_none() {
            self.sync();
        }
        (pattern, self.block())
    }
}

/// Reads `let mut name: Type = value;`, where `mut`, the type and the value
/// may be left out, or `let .{ fields } = value;`.
fn let_statement(p: &mut Parser) -> Id<Stmt> {
    let start = p.next().span.start;
    if p.at(TokenKind::Dot) {
        let pattern = p.struct_pattern();
        p.expect(TokenKind::Assign);
        let value = p.expr(Precedence::Lowest);
        p.end();
        return p.add(start, Stmt::Destructure { pattern, value });
    }
    let mutable = p.at(TokenKind::Mut);
    if mutable {
        p.next();
    }
    let (name, ty, value) = p.binding();
    p.add(
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
fn if_statement(p: &mut Parser) -> Id<Stmt> {
    grow(|| {
        let start = p.next().span.start;
        let mut condition = p.expr(Precedence::Lowest);
        let t = p.tok();
        if t.kind == TokenKind::Assign {
            p.next();
            let fix = p.fix(t.span, "==");
            p.repair(
                Diagnostic::new(
                    diagnostics::EXPECTED_TOKEN,
                    "expected `==` in condition, found `=`",
                )
                .label(p.file, t.span, "")
                .help("compare with `==`", vec![fix]),
            );
            let right = p.expr(Precedence::Lowest);
            let left_start = p.tree.exprs.span(condition).start;
            condition = p.add(
                left_start,
                Expr::Binary {
                    op: TokenKind::EqualEqual,
                    left: condition,
                    right,
                },
            );
        }
        let then = p.body("if");
        let mut otherwise = None;
        if p.at(TokenKind::Else) {
            p.next();
            otherwise = if p.at(TokenKind::If) {
                Some(if_statement(p))
            } else {
                p.body("else").map(|block| p.block_statement(block))
            };
        }
        p.add(
            start,
            Stmt::If {
                condition,
                then,
                otherwise,
            },
        )
    })
}

/// Reads `for { }`, `for condition { }`, `for value in items { }` or
/// `for index, value in items { }`. The iterable may be a range.
fn for_statement(p: &mut Parser) -> Id<Stmt> {
    let start = p.next().span.start;
    if p.at(TokenKind::Ident) && matches!(p.ahead(1).kind, TokenKind::In | TokenKind::Comma) {
        let mut index = None;
        let mut value = p.name();
        if p.at(TokenKind::Comma) {
            p.next();
            index = Some(value);
            value = p.name();
        }
        p.expect(TokenKind::In);
        let iterable = p.index_operand();
        let body = p.body("for");
        return p.add(
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
    if !p.at(TokenKind::LeftBrace) {
        let value = p.expr(Precedence::Lowest);
        if p.at(TokenKind::In) {
            let span = p.tree.exprs.span(value);
            p.fail(
                Diagnostic::new(diagnostics::INVALID_EXPRESSION, "invalid loop variable").label(
                    p.file,
                    span,
                    "expected an identifier before `in`",
                ),
            );
        }
        condition = Some(value);
    }
    let body = p.body("for");
    p.add(start, Stmt::Loop { condition, body })
}

/// Reads `break;` or `continue;`. Loop labels are not part of the language
/// yet; a label is reported and skipped.
fn jump_statement(p: &mut Parser) -> Id<Stmt> {
    let keyword = p.next();
    let t = p.tok();
    if t.kind == TokenKind::Ident {
        p.next();
        p.repair(
            Diagnostic::new(
                diagnostics::INVALID_STATEMENT,
                format!("labeled {} is not supported yet", keyword.kind.name()),
            )
            .label(
                p.file,
                t.span,
                "loop labels are planned for a later release",
            ),
        );
    }
    p.end();
    let node = if keyword.kind == TokenKind::Continue {
        Stmt::Continue
    } else {
        Stmt::Break
    };
    p.add(keyword.span.start, node)
}

/// Reads `match subject { arms }`. An arm is `_ => { }`, `Enum::Case => { }`
/// or `Enum::Case with pattern => { }`; arms are not separated by commas.
fn match_statement(p: &mut Parser) -> Id<Stmt> {
    let start = p.next().span.start;
    let subject = p.expr(Precedence::Lowest);
    let mut arms = List::EMPTY;
    if let Some(open) = p.expect(TokenKind::LeftBrace) {
        let mark = p.scratch.len();
        // Every arm begins with a name (`_` or a path) and reads at least it,
        // so the loop always moves on.
        while p.at(TokenKind::Ident) {
            let arm_start = p.tok().span.start;
            let (pattern, body) = p.match_arm();
            let arm = p.add(arm_start, MatchArm { pattern, body });
            p.scratch.push(arm.into());
            let t = p.tok();
            if t.kind == TokenKind::Comma {
                p.next();
                let fix = p.fix(t.span, "");
                p.repair(
                    Diagnostic::new(diagnostics::EXPECTED_TOKEN, "match arms do not use commas")
                        .label(p.file, t.span, "")
                        .help("remove this comma", vec![fix]),
                );
            }
        }
        arms = p.finish_list(mark);
        p.close(TokenKind::RightBrace, open);
    }
    p.add(start, Stmt::Match { subject, arms })
}

/// Reads `return value;`; the value may be left out.
fn return_statement(p: &mut Parser) -> Id<Stmt> {
    let start = p.next().span.start;
    let value = (!p.at(TokenKind::Semicolon)).then(|| p.expr(Precedence::Lowest));
    p.end();
    p.add(start, Stmt::Return(value))
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
