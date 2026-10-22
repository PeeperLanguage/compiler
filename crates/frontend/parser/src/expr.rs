//! Expressions, read by a Pratt parser. A prefix rule reads an expression that
//! a token begins (a name, a literal, `(`); an infix rule reads what a token
//! adds after an expression on its left (an operator and its right side, a
//! call). Each token with an infix rule has a precedence: it joins the expression being read only when it
//! binds tighter than the operator before it, so precedence and associativity
//! live in the table below and not in nested grammar rules.

use arena::Id;
use diagnostics::Diagnostic;
use model::BinaryOp;
use model::UnaryOp;
use model::ast::Expr;
use model::ast::Type;
use token::TokenKind;

use crate::Parser;
use crate::grow;
use crate::literals::anonymous_struct_literal;
use crate::literals::array_literal;
use crate::literals::bool_literal;
use crate::literals::character_literal;
use crate::literals::none_literal;
use crate::literals::number_literal;
use crate::literals::string_literal;

/// How tightly an operator binds, from loosest to tightest.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub(crate) enum Precedence {
    Lowest,
    OrElse,
    Pipe,
    LogicalOr,
    LogicalAnd,
    BitOr,
    BitXor,
    BitAnd,
    Equality,
    Compare,
    Shift,
    Sum,
    Product,
    Cast,
    Prefix,
    Call,
}

/// Reads the expression the current token begins.
type PrefixRule = fn(&mut Parser) -> Id<Expr>;

/// Reads what the current token adds to the expression `left`.
type InfixRule = fn(&mut Parser, Id<Expr>) -> Id<Expr>;

/// The rules of each token, indexed by token kind. A token without a prefix
/// rule cannot begin an expression; a token without an infix rule never
/// continues one.
struct Rules {
    prefix: [Option<PrefixRule>; 256],
    infix: [Option<(Precedence, InfixRule)>; 256],
}

impl Rules {
    const fn prefix(&mut self, kind: TokenKind, read: PrefixRule) {
        self.prefix[kind as usize] = Some(read);
    }

    const fn infix(&mut self, kind: TokenKind, precedence: Precedence, read: InfixRule) {
        self.infix[kind as usize] = Some((precedence, read));
    }
}

static RULES: Rules = {
    let mut rules = Rules {
        prefix: [None; 256],
        infix: [None; 256],
    };
    // values
    rules.prefix(TokenKind::Ident, name_or_path);
    rules.prefix(TokenKind::Number, number_literal);
    rules.prefix(TokenKind::String, string_literal);
    rules.prefix(TokenKind::CString, string_literal);
    rules.prefix(TokenKind::Char, character_literal);
    rules.prefix(TokenKind::ByteChar, character_literal);
    rules.prefix(TokenKind::True, bool_literal);
    rules.prefix(TokenKind::False, bool_literal);
    rules.prefix(TokenKind::None, none_literal);
    rules.prefix(TokenKind::LeftParen, parentheses);
    rules.prefix(TokenKind::LeftBracket, array_literal);
    rules.prefix(TokenKind::Dot, anonymous_struct_literal);

    // prefix operators and built-in calls
    rules.prefix(TokenKind::Plus, leading_plus);
    rules.prefix(TokenKind::Minus, unary);
    rules.prefix(TokenKind::Bang, unary);
    rules.prefix(TokenKind::Tilde, unary);
    rules.prefix(TokenKind::Ampersand, reference);
    rules.prefix(TokenKind::At, raw_address);
    rules.prefix(TokenKind::Free, builtin);
    rules.prefix(TokenKind::Print, builtin);
    rules.prefix(TokenKind::Println, builtin);

    // fallback and pipe
    rules.infix(TokenKind::Orelse, Precedence::OrElse, or_else);
    rules.infix(TokenKind::PipeArrow, Precedence::Pipe, pipe);

    // logical
    rules.infix(TokenKind::OrOr, Precedence::LogicalOr, binary);
    rules.infix(TokenKind::AndAnd, Precedence::LogicalAnd, binary);

    // bitwise
    rules.infix(TokenKind::Pipe, Precedence::BitOr, binary);
    rules.infix(TokenKind::Caret, Precedence::BitXor, binary);
    rules.infix(TokenKind::Ampersand, Precedence::BitAnd, binary);

    // equality and comparison
    rules.infix(TokenKind::EqualEqual, Precedence::Equality, binary);
    rules.infix(TokenKind::BangEqual, Precedence::Equality, binary);
    rules.infix(TokenKind::Is, Precedence::Equality, is_expression);
    rules.infix(TokenKind::Less, Precedence::Compare, binary);
    rules.infix(TokenKind::LessEqual, Precedence::Compare, binary);
    rules.infix(TokenKind::Greater, Precedence::Compare, binary);
    rules.infix(TokenKind::GreaterEqual, Precedence::Compare, binary);

    // arithmetic
    rules.infix(TokenKind::ShiftLeft, Precedence::Shift, binary);
    rules.infix(TokenKind::ShiftRight, Precedence::Shift, binary);
    rules.infix(TokenKind::Plus, Precedence::Sum, binary);
    rules.infix(TokenKind::Minus, Precedence::Sum, binary);
    rules.infix(TokenKind::Asterisk, Precedence::Product, binary);
    rules.infix(TokenKind::Slash, Precedence::Product, binary);
    rules.infix(TokenKind::Percent, Precedence::Product, binary);
    rules.infix(TokenKind::As, Precedence::Cast, cast);

    // call, index and selector
    rules.infix(TokenKind::LeftParen, Precedence::Call, call);
    rules.infix(TokenKind::LeftBracket, Precedence::Call, index);
    rules.infix(TokenKind::Dot, Precedence::Call, selector);
    rules
};

/// Whether an expression can begin with `kind`.
pub(crate) fn starts_expression(kind: TokenKind) -> bool {
    RULES.prefix[kind as usize].is_some()
}

impl Parser {
    /// Reads an expression whose operators bind tighter than `min`.
    pub(crate) fn expr(&mut self, min: Precedence) -> Id<Expr> {
        grow(|| {
            let t = self.tok();
            let Some(read) = RULES.prefix[t.kind as usize] else {
                self.fail(
                    Diagnostic::new(
                        diagnostics::EXPECTED_TOKEN,
                        format!("expected expression, found `{}`", t.kind.text()),
                    )
                    .label(
                        &self.tree.file,
                        t.span,
                        "expected an expression here",
                    ),
                );
                return self.add(self.last_end, Expr::Invalid);
            };
            let mut left = read(self);
            while let Some((precedence, read)) = RULES.infix[self.tok().kind as usize]
                && precedence > min
            {
                left = read(self, left);
            }
            left
        })
    }

    /// Reads the current token as an expression without children.
    pub(crate) fn leaf(&mut self, node: Expr) -> Id<Expr> {
        let start = self.next().span.start;
        self.add(start, node)
    }

    /// The start of expression `id`, where an infix expression on it starts.
    fn start_of(&self, id: Id<Expr>) -> u32 {
        self.tree.exprs.span(id).start
    }

    /// Whether the name at the current token begins a path or a type rather
    /// than standing alone: `::` follows it, or `.{`, or type arguments that
    /// `::` or `.{` follow.
    fn starts_path(&self) -> bool {
        match self.ahead(1).kind {
            TokenKind::ColonColon => true,
            TokenKind::Dot => self.ahead(2).kind == TokenKind::LeftBrace,
            TokenKind::Less => self.opens_type_arguments(),
            _ => false,
        }
    }

    /// Whether the `<` after the current name opens type arguments: its
    /// matching `>` comes before the statement ends, and `::` or `.{` follows
    /// it. Only those two decide, so in `if a < b && c > d {` the `<` stays a
    /// comparison.
    fn opens_type_arguments(&self) -> bool {
        let mut depth = 0i32;
        for i in self.current() + 1..self.tokens.len() {
            match self.tokens[i].kind {
                TokenKind::Less => depth += 1,
                TokenKind::Greater => depth -= 1,
                TokenKind::ShiftRight => depth -= 2,
                TokenKind::Semicolon
                | TokenKind::LeftBrace
                | TokenKind::RightBrace
                | TokenKind::Eof => return false,
                _ => {}
            }
            if depth < 0 {
                return false;
            }
            if depth == 0 {
                let next = self.tokens[i + 1].kind;
                return next == TokenKind::ColonColon
                    || next == TokenKind::Dot && self.tokens[i + 2].kind == TokenKind::LeftBrace;
            }
        }
        false
    }

    /// Reads what may stand inside `[ ]` or after `in`: an expression, or a
    /// range `a..b`, `a..=b`, `..b` or `a..`. Ranges appear nowhere else, and
    /// only right before the `]` may a range leave out its end.
    pub(crate) fn index_operand(&mut self) -> Id<Expr> {
        let start = self.tok().span.start;
        let is_range = |p: &Self| p.at(TokenKind::DotDot) || p.at(TokenKind::DotDotEqual);
        let mut low = None;
        if !is_range(self) {
            let value = self.expr(Precedence::Lowest);
            if !is_range(self) {
                return value;
            }
            low = Some(value);
        }
        let op = self.next();
        let mut high = None;
        if !self.at(TokenKind::RightBracket) {
            high = Some(self.expr(Precedence::Lowest));
        } else if op.kind == TokenKind::DotDotEqual {
            self.repair(
                Diagnostic::new(
                    diagnostics::INVALID_EXPRESSION,
                    "inclusive range requires an end bound",
                )
                .label(
                    &self.tree.file,
                    op.span,
                    "add the inclusive end bound after `..=`",
                ),
            );
        }
        let inclusive = op.kind == TokenKind::DotDotEqual;
        self.add(
            start,
            Expr::Range {
                inclusive,
                start: low,
                end: high,
            },
        )
    }

    /// Reports `case` unless it is a full path `Enum::Case` (or a type already
    /// reported as invalid).
    pub(crate) fn require_path(&mut self, case: Id<Type>) {
        if !matches!(self.tree.types[case], Type::Path(_) | Type::Invalid) {
            let span = self.tree.types.span(case);
            self.repair(
                Diagnostic::new(
                    diagnostics::INVALID_EXPRESSION,
                    "enum case must use a fully named path",
                )
                .label(&self.tree.file, span, "write `Enum::Variant`"),
            );
        }
    }
}

/// Reads a name; or, when a path or type arguments follow it, a path
/// (`a::B`, `Box<T>::new`), a struct literal (`Point.{...}`) or a variant
/// literal (`Enum::Case with payload`).
fn name_or_path(p: &mut Parser) -> Id<Expr> {
    if !p.starts_path() {
        return p.leaf(Expr::Name);
    }
    let path = p.named_type();
    let start = p.tree.types.span(path).start;
    if p.at(TokenKind::Dot) && p.ahead(1).kind == TokenKind::LeftBrace {
        return p.struct_literal(Some(path), start);
    }
    if p.at(TokenKind::With) && matches!(p.tree.types[path], Type::Path(_)) {
        p.next();
        let payload = p.expr(Precedence::Lowest);
        return p.add(
            start,
            Expr::VariantLiteral {
                case: path,
                payload,
            },
        );
    }
    p.add(start, Expr::Path(path))
}

/// Reads `( expression )`.
fn parentheses(p: &mut Parser) -> Id<Expr> {
    let open = p.next();
    let inner = p.expr(Precedence::Lowest);
    p.close(TokenKind::RightParen, open);
    p.add(open.span.start, Expr::Parentheses(inner))
}

/// Reads a binary operator and its right side. The right side takes only
/// operators that bind tighter, so operators of one precedence group to the
/// left: a - b - c is (a - b) - c.
fn binary(p: &mut Parser, left: Id<Expr>) -> Id<Expr> {
    let kind = p.next().kind;
    let (precedence, _) = RULES.infix[kind as usize].expect("binary is an infix rule");
    let op = match kind {
        TokenKind::Plus => BinaryOp::Add,
        TokenKind::Minus => BinaryOp::Subtract,
        TokenKind::Asterisk => BinaryOp::Multiply,
        TokenKind::Slash => BinaryOp::Divide,
        TokenKind::Percent => BinaryOp::Remainder,
        TokenKind::ShiftLeft => BinaryOp::ShiftLeft,
        TokenKind::ShiftRight => BinaryOp::ShiftRight,
        TokenKind::Ampersand => BinaryOp::BitAnd,
        TokenKind::Pipe => BinaryOp::BitOr,
        TokenKind::Caret => BinaryOp::BitXor,
        TokenKind::EqualEqual => BinaryOp::EqualTo,
        TokenKind::BangEqual => BinaryOp::NotEqualTo,
        TokenKind::Less => BinaryOp::LessThan,
        TokenKind::LessEqual => BinaryOp::LessOrEqual,
        TokenKind::Greater => BinaryOp::GreaterThan,
        TokenKind::GreaterEqual => BinaryOp::GreaterOrEqual,
        TokenKind::AndAnd => BinaryOp::LogicalAnd,
        TokenKind::OrOr => BinaryOp::LogicalOr,
        _ => unreachable!("binary is the rule of operator tokens only"),
    };
    let right = p.expr(precedence);
    p.add(p.start_of(left), Expr::Binary { op, left, right })
}

/// Reads the arguments of a call to `callee`.
fn call(p: &mut Parser, callee: Id<Expr>) -> Id<Expr> {
    let arguments = p.delimited(
        TokenKind::LeftParen,
        TokenKind::RightParen,
        false,
        starts_expression,
        |p| p.expr(Precedence::Lowest),
    );
    p.add(p.start_of(callee), Expr::Call { callee, arguments })
}

/// Reads `+x` as `x`, reporting the `+`: there is no unary `+`, as it would
/// change nothing.
fn leading_plus(p: &mut Parser) -> Id<Expr> {
    let plus = p.next().span;
    let fix = p.fix(plus, "");
    p.repair(
        Diagnostic::new(
            diagnostics::INVALID_EXPRESSION,
            "`+` is not a prefix operator",
        )
        .label(&p.tree.file, plus, "")
        .help("remove it; `+x` is just `x`", vec![fix]),
    );
    p.expr(Precedence::Prefix)
}

/// Reads `-x`, `!x` or `~x`.
fn unary(p: &mut Parser) -> Id<Expr> {
    let token = p.next();
    let op = match token.kind {
        TokenKind::Minus => UnaryOp::Negate,
        TokenKind::Bang => UnaryOp::LogicalNot,
        TokenKind::Tilde => UnaryOp::BitNot,
        _ => unreachable!("unary is the rule of operator tokens only"),
    };
    let value = p.expr(Precedence::Prefix);
    p.add(token.span.start, Expr::Unary { op, value })
}

/// Reads `&x` or `&mut x`.
fn reference(p: &mut Parser) -> Id<Expr> {
    let start = p.next().span.start;
    let mutable = p.at(TokenKind::Mut);
    if mutable {
        p.next();
    }
    let value = p.expr(Precedence::Prefix);
    p.add(start, Expr::Reference { mutable, value })
}

/// Reads `@x`.
fn raw_address(p: &mut Parser) -> Id<Expr> {
    let start = p.next().span.start;
    let value = p.expr(Precedence::Prefix);
    p.add(start, Expr::RawAddress(value))
}

/// Reads `free(x)`, `print(x)` or `println(x)`.
fn builtin(p: &mut Parser) -> Id<Expr> {
    let keyword = p.next();
    let value = p.expect(TokenKind::LeftParen).map(|open| {
        let value = p.expr(Precedence::Lowest);
        p.close(TokenKind::RightParen, open);
        value
    });
    let node = match keyword.kind {
        TokenKind::Free => Expr::Free(value),
        _ => Expr::Print {
            newline: keyword.kind == TokenKind::Println,
            value,
        },
    };
    p.add(keyword.span.start, node)
}

/// Reads `[index]` after `value`.
fn index(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    let open = p.next();
    let index = p.index_operand();
    p.close(TokenKind::RightBracket, open);
    p.add(p.start_of(value), Expr::Index { value, index })
}

/// Reads `.name` after `value`.
fn selector(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    p.next();
    let field = p.name();
    p.add(p.start_of(value), Expr::Selector { value, field })
}

/// Reads `as Type` after `value`.
fn cast(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    p.next();
    let ty = p.type_expr();
    p.add(p.start_of(value), Expr::Cast { value, ty })
}

/// Reads `is Enum::Case` after `value`; the case is a full path.
fn is_expression(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    p.next();
    let case = p.type_expr();
    p.require_path(case);
    p.add(p.start_of(value), Expr::Is { value, case })
}

/// Reads `orelse fallback` or `orelse { block }` after `value`. The fallback
/// may itself end in `orelse`: a orelse b orelse c is a orelse (b orelse c).
fn or_else(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    p.next();
    let fallback = if p.at(TokenKind::LeftBrace) {
        let block = p.block();
        let span = p.tree.blocks.span(block);
        p.tree.add(Expr::Block(block), span)
    } else {
        p.expr(Precedence::Lowest) // one below OrElse: groups to the right
    };
    p.add(p.start_of(value), Expr::OrElse { value, fallback })
}

/// Reads `|> f(arguments)` after `value`. Its right side takes only what
/// binds tighter than a prefix operator, a call, so `a |> len() as i32` is
/// (a |> len()) as i32. The target must call a function, not a method.
fn pipe(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    p.next();
    let call = p.expr(Precedence::Prefix);
    match p.tree.exprs[call] {
        Expr::Call { callee, .. } => {
            if let Expr::Selector { .. } = p.tree.exprs[callee] {
                let span = p.tree.exprs.span(callee);
                p.repair(
                    Diagnostic::new(diagnostics::INVALID_EXPRESSION, "pipe cannot call a method")
                        .label(&p.tree.file, span, "use `value.method(...)`"),
                );
            }
        }
        _ => {
            let span = p.tree.exprs.span(call);
            p.repair(
                Diagnostic::new(
                    diagnostics::INVALID_EXPRESSION,
                    "pipe target must be a free-function call",
                )
                .label(&p.tree.file, span, "write `value |> function(...)`"),
            );
        }
    }
    p.add(p.start_of(value), Expr::Pipe { value, call })
}

#[cfg(test)]
mod tests {
    use model::ast::Decl;
    use model::ast::Stmt;
    use model::ast::Tree;

    use super::*;
    use crate::tests::dump_of;
    use crate::tests::messages;
    use crate::tests::one_error;
    use crate::tests::parse_text;

    /// There is no unary `+`: a leading one is reported, and its fix removes it.
    #[test]
    fn leading_plus() {
        one_error(
            "fn f() { let a = +5; }",
            "`+` is not a prefix operator",
            Some("fn f() { let a = 5; }"),
        );
    }

    /// An operator is written as the token it is read from: each binary and
    /// unary operator comes from exactly one operator token, whose text is
    /// the operator's symbol.
    #[test]
    fn operators_are_written_as_their_tokens() {
        let (mut binary, mut unary) = (0, 0);
        for &kind in token::OPERATORS {
            for text in [
                format!("fn f() {{ a {} b; }}", kind.text()),
                format!("fn f() {{ {}a; }}", kind.text()),
            ] {
                let (tree, errors) = parse_text(&text);
                if !errors.is_empty() {
                    continue;
                }
                for id in tree.exprs.ids() {
                    match tree.exprs[id] {
                        Expr::Binary { op, .. } => {
                            assert_eq!(op.symbol(), kind.text(), "{text}");
                            binary += 1;
                        }
                        Expr::Unary { op, .. } => {
                            assert_eq!(op.symbol(), kind.text(), "{text}");
                            unary += 1;
                        }
                        _ => {}
                    }
                }
            }
        }
        assert_eq!((binary, unary), (18, 3));
    }

    /// The expression of the first statement of the first function.
    fn first_expression(tree: &Tree) -> Id<Expr> {
        let decl = tree.lists.items(tree.top_level).next().unwrap();
        let Decl::Function(f) = tree.decls[decl] else {
            panic!("not a function")
        };
        let statement = tree
            .lists
            .items(tree.blocks[f.body.unwrap()].statements)
            .next()
            .unwrap();
        let Stmt::Expression(e) = tree.stmts[statement] else {
            panic!("not an expression statement")
        };
        e
    }

    /// An expression with each binary operation in parentheses.
    fn grouped(t: &Tree, id: Id<Expr>) -> String {
        let text = |span| t.file.slice(span).to_string();
        match t.exprs[id] {
            Expr::Binary { op, left, right } => {
                format!(
                    "({} {} {})",
                    grouped(t, left),
                    op.symbol(),
                    grouped(t, right)
                )
            }
            Expr::Cast { value, ty } => {
                format!("({} as {})", grouped(t, value), text(t.types.span(ty)))
            }
            Expr::Is { value, case } => {
                format!("({} is {})", grouped(t, value), text(t.types.span(case)))
            }
            Expr::OrElse { value, fallback } => {
                format!("({} orelse {})", grouped(t, value), grouped(t, fallback))
            }
            Expr::Pipe { value, call } => {
                format!("({} |> {})", grouped(t, value), grouped(t, call))
            }
            Expr::Unary { value, .. } | Expr::Reference { value, .. } => {
                let (span, inner) = (t.exprs.span(id), t.exprs.span(value));
                let prefix = t.file.text[span.start as usize..inner.start as usize].trim();
                let space = if let Expr::Reference { mutable: true, .. } = t.exprs[id] {
                    " "
                } else {
                    ""
                };
                format!("{prefix}{space}{}", grouped(t, value))
            }
            Expr::Selector { value, field } => {
                format!("{}.{}", grouped(t, value), text(field.span))
            }
            Expr::Index { value, index } => {
                format!("{}[{}]", grouped(t, value), text(t.exprs.span(index)))
            }
            Expr::Call { callee, arguments } => {
                let arguments: Vec<_> = t.lists.items(arguments).map(|a| grouped(t, a)).collect();
                format!("{}({})", grouped(t, callee), arguments.join(", "))
            }
            _ => text(t.exprs.span(id)),
        }
    }

    /// How operators group, from loosest to tightest, and that operators of
    /// one precedence group to the left.
    #[test]
    fn precedence() {
        for (text, want) in [
            ("a || b && c", "(a || (b && c))"),
            ("a | b ^ c & d", "(a | (b ^ (c & d)))"),
            ("a & b == c", "(a & (b == c))"),
            ("a == b < c", "(a == (b < c))"),
            ("a < b << c", "(a < (b << c))"),
            ("a << b + c", "(a << (b + c))"),
            ("a + b * c", "(a + (b * c))"),
            ("a - b - c", "((a - b) - c)"),
            ("a * b % c / d", "(((a * b) % c) / d)"),
            ("(a + b) * f(c, d + e)", "((a + b) * f(c, (d + e)))"),
            ("a != b >= c > d <= e", "(a != (((b >= c) > d) <= e))"),
            ("-a * b", "(-a * b)"),
            ("-a.b(c)[d]", "-a.b(c)[d]"),
            ("!a && b", "(!a && b)"),
            ("&mut a + b", "(&mut a + b)"),
            ("a + b as i64", "(a + (b as i64))"),
            ("a as i32 * b", "((a as i32) * b)"),
            ("a is E::B && c", "((a is E::B) && c)"),
            ("a orelse b orelse c", "(a orelse (b orelse c))"),
            ("a || b orelse c", "((a || b) orelse c)"),
            ("a |> f(b) |> g()", "((a |> f(b)) |> g())"),
            ("a |> len() as i32", "((a |> len()) as i32)"),
            ("a + b |> f()", "((a + b) |> f())"),
            ("x.y.z[0][i..j]", "x.y.z[0][i..j]"),
        ] {
            let (tree, errors) = parse_text(&format!("fn f() {{ {text}; }}"));
            assert!(errors.is_empty(), "{text}: {errors:?}");
            assert_eq!(grouped(&tree, first_expression(&tree)), want, "{text}");
        }
    }

    #[test]
    fn parses_expressions() {
        let text = r#"fn f() {
    c"c";
    'x';
    b'y';
    true;
    none;
    Point.{ x = 1, y = -2 };
    .{ a = &mut v, b = @raw };
    Shape::Line with .{ end = Box<i32>::new(1) };
    [_]u8{ 1, 2 };
    []str{};
    free(p);
    println(items[1..=n]);
}
"#;
        let want = r#"module
  function declaration
    declared name f
    block
      expression statement
        string literal c string c"c"
      expression statement
        character literal 'x'
      expression statement
        character literal byte character b'y'
      expression statement
        bool literal true
      expression statement
        none literal none
      expression statement
        struct literal
          type name Point
          field value
            field name x
            number literal 1
          field value
            field name y
            unary expression -
              number literal 2
      expression statement
        struct literal
          field value
            field name a
            reference expression mut
              name reference v
          field value
            field name b
            raw address expression
              name reference raw
      expression statement
        variant literal
          path
            type name Shape
            type name Line
          struct literal
            field value
              field name end
              call expression
                path
                  generic type
                    type name Box
                    type name i32
                  type name new
                number literal 1
      expression statement
        array literal
          fixed array type
            inferred length _
            type name u8
          number literal 1
          number literal 2
      expression statement
        array literal
          dynamic array type
            type name str
      expression statement
        free expression
          name reference p
      expression statement
        print expression println
          index expression
            name reference items
            range ..=
              number literal 1
              name reference n
"#;
        assert!(messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    #[test]
    fn expression_errors() {
        for (text, message) in [
            ("a[1..=];", "inclusive range requires an end bound"),
            ("a is b;", "enum case must use a fully named path"),
            ("a |> b;", "pipe target must be a free-function call"),
            ("a |> b.c();", "pipe cannot call a method"),
            (
                ".{ a: 1, b: 2 };",
                "expected `=` after struct literal field name",
            ),
            ("[x]i32{};", "expected array length"),
            ("a.;", "expected `identifier`, found `;`"),
        ] {
            one_error(&format!("fn f() {{ {text} }}"), message, None);
        }
    }

    /// `<` begins type arguments only when `::` or `.{` follows the matching `>`.
    #[test]
    fn comparison_is_not_type_arguments() {
        for (text, want) in [
            ("a < b && c > d;", "binary"),
            ("Box<T>::new();", "call"),
            ("Pair<A, B>.{};", "struct literal"),
            ("a < b;", "binary"),
            ("f(a < b, c > d);", "call"),
        ] {
            let (tree, errors) = parse_text(&format!("fn f() {{ {text} }}"));
            assert!(errors.is_empty(), "{text}: {errors:?}");
            let got = match tree.exprs[first_expression(&tree)] {
                Expr::Binary { .. } => "binary",
                Expr::Call { .. } => "call",
                Expr::StructLiteral { .. } => "struct literal",
                other => panic!("{text}: {other:?}"),
            };
            assert_eq!(got, want, "{text}");
        }
    }

    /// Number literal suffixes, with the old compiler's messages. Digits outside
    /// the base are the lexer's to report; a leading zero (007) is left to a
    /// later lint pass.
    #[test]
    fn number_checks() {
        for (literal, message) in [
            ("1i", "invalid numeric literal suffix i"),
            ("1i_32", "invalid numeric literal suffix i_32"),
            ("1i8_", "invalid numeric literal suffix i8_"),
            ("1u", "invalid numeric literal suffix u"),
            ("1f", "invalid numeric literal suffix f"),
            ("0b1i", "invalid numeric literal suffix i"),
            ("0o7u", "invalid numeric literal suffix u"),
            (
                "2.4i32",
                "integer suffix i32 cannot be used on a float literal",
            ),
            (
                "1e3u8",
                "integer suffix u8 cannot be used on a float literal",
            ),
            ("0", ""),
            ("0.5", ""),
            ("007", ""),
            ("01i32", ""),
            ("01f32", ""),
            ("1.5f32", ""),
            ("0x1f_u8", ""),
            ("0xffu24", ""),
            ("1_000i64", ""),
            ("1e3f64", ""),
        ] {
            let text = format!("fn f() {{ {literal}; }}");
            let (_, errors) = parse_text(&text);
            if message.is_empty() {
                assert!(errors.is_empty(), "{literal}: {errors:?}");
                continue;
            }
            let start = text.find(literal).unwrap();
            assert!(
                errors.len() == 1
                    && errors[0].message == message
                    && errors[0].labels[0].span.range() == (start..start + literal.len()),
                "{literal}: {errors:?}"
            );
        }
    }
}
