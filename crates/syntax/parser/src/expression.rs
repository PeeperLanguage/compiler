//! Expressions, read by a Pratt parser. A prefix rule reads an expression that
//! a token begins (a name, a literal, `(`); an infix rule reads what a token
//! adds after an expression on its left (an operator and its right side, a
//! call). Each token with an infix rule has a precedence: it joins the expression being read only when it
//! binds tighter than the operator before it, so precedence and associativity
//! live in the table below and not in nested grammar rules.

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use source::Span;
use token::TokenKind;
use tree::ast::Expr;
use tree::ast::Type;
use tree::operators::BinaryOp;
use tree::operators::Precedence;
use tree::operators::UnaryOp;

use crate::Parser;
use crate::RULES;
use crate::Rules;

mod literals;

/// Reads the expression the current token begins.
pub(crate) type PrefixRule = fn(&mut Parser) -> Id<Expr>;

/// Reads what continues the path `path` in an expression, from the token
/// after it.
pub(crate) type AfterPathRule = fn(&mut Parser, Id<Type>) -> Id<Expr>;

/// Reads what the current token adds to the expression `left`.
pub(crate) type InfixRule = fn(&mut Parser, Id<Expr>) -> Id<Expr>;

impl Rules {
    pub(crate) const fn prefix(&mut self, kind: TokenKind, read: PrefixRule) {
        assert!(
            self.prefix[kind as usize].is_none(),
            "two rules begin an expression with one token"
        );
        self.prefix[kind as usize] = Some(read);
    }

    pub(crate) const fn infix(&mut self, kind: TokenKind, precedence: Precedence, read: InfixRule) {
        assert!(
            self.infix[kind as usize].is_none(),
            "two rules continue an expression with one token"
        );
        self.infix[kind as usize] = Some((precedence, read));
    }

    /// The rule that reads what continues a path when the two tokens after
    /// it are `first` and `second`.
    fn after_path_rule(&self, first: TokenKind, second: TokenKind) -> Option<AfterPathRule> {
        let (paired, read) = self.after_path[first as usize]?;
        (paired == second).then_some(read)
    }

    const fn binary_operator(&mut self, kind: TokenKind, op: BinaryOp) {
        self.infix(kind, op.precedence(), binary);
        self.binary[kind as usize] = Some(op);
    }
}

/// Adds the rules of names, literals and operators to `rules`.
pub(crate) const fn expression_rows(rules: &mut Rules) {
    // values
    rules.prefix(TokenKind::Ident, name_or_path);
    rules.prefix(TokenKind::SelfValue, self_name);
    rules.prefix(TokenKind::Number, literals::number_literal);
    rules.prefix(TokenKind::None, literals::none_literal);
    rules.prefix(TokenKind::True, literals::bool_literal);
    rules.prefix(TokenKind::False, literals::bool_literal);
    rules.prefix(TokenKind::String, literals::string_literal);
    rules.prefix(TokenKind::CString, literals::string_literal);
    rules.prefix(TokenKind::LeftParen, parentheses);

    // prefix operators
    rules.prefix(TokenKind::Plus, leading_plus);
    rules.prefix(TokenKind::Minus, unary);
    rules.prefix(TokenKind::Bang, unary);
    rules.prefix(TokenKind::Tilde, unary);
    rules.prefix(TokenKind::Ampersand, reference);

    // logical
    rules.binary_operator(TokenKind::OrOr, BinaryOp::LogicalOr);
    rules.binary_operator(TokenKind::AndAnd, BinaryOp::LogicalAnd);

    // bitwise
    rules.binary_operator(TokenKind::Pipe, BinaryOp::BitOr);
    rules.binary_operator(TokenKind::Caret, BinaryOp::BitXor);
    rules.binary_operator(TokenKind::Ampersand, BinaryOp::BitAnd);

    // equality and comparison
    rules.binary_operator(TokenKind::EqualEqual, BinaryOp::EqualTo);
    rules.binary_operator(TokenKind::BangEqual, BinaryOp::NotEqualTo);
    rules.binary_operator(TokenKind::Less, BinaryOp::LessThan);
    rules.binary_operator(TokenKind::LessEqual, BinaryOp::LessOrEqual);
    rules.binary_operator(TokenKind::Greater, BinaryOp::GreaterThan);
    rules.binary_operator(TokenKind::GreaterEqual, BinaryOp::GreaterOrEqual);

    // arithmetic
    rules.binary_operator(TokenKind::ShiftLeft, BinaryOp::ShiftLeft);
    rules.binary_operator(TokenKind::ShiftRight, BinaryOp::ShiftRight);
    rules.binary_operator(TokenKind::Plus, BinaryOp::Add);
    rules.binary_operator(TokenKind::Minus, BinaryOp::Subtract);
    rules.binary_operator(TokenKind::Asterisk, BinaryOp::Multiply);
    rules.binary_operator(TokenKind::Slash, BinaryOp::Divide);
    rules.binary_operator(TokenKind::Percent, BinaryOp::Remainder);

    // call, index and selector
    rules.infix(TokenKind::LeftParen, Precedence::Call, call);
    rules.infix(TokenKind::LeftBracket, Precedence::Call, index);
    rules.infix(TokenKind::Dot, Precedence::Call, selector);

    rules.infix(TokenKind::As, Precedence::Cast, cast);
}

/// Whether an expression can begin with `kind`.
pub(crate) fn starts_expression(kind: TokenKind) -> bool {
    RULES.prefix[kind as usize].is_some()
}

impl Parser {
    /// Reads an expression whose operators bind tighter than `min`.
    pub(crate) fn expr(&mut self, min: Precedence) -> Id<Expr> {
        source::grow_stack(|| {
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
    pub(crate) fn start_of(&self, id: Id<Expr>) -> u32 {
        self.tree.exprs.span(id).start
    }

    /// Whether the name at the current token begins a path or a type rather
    /// than standing alone: `::` follows it, or `.{`, or type arguments that
    /// `::` or `.{` follow.
    fn starts_path(&self) -> bool {
        match self.ahead(1).kind {
            TokenKind::ColonColon => true,
            first => RULES.after_path_rule(first, self.ahead(2).kind).is_some(),
        }
    }
}

/// Reads `self`, the name of a type's function's first parameter.
fn self_name(p: &mut Parser) -> Id<Expr> {
    p.leaf(Expr::Name)
}

/// Reads a name; or, when a path or type arguments follow it, a path
/// (`a::B`, `Box::<T>::new`) or what a feature reads after one
/// (`Point.{...}`).
pub(crate) fn name_or_path(p: &mut Parser) -> Id<Expr> {
    if !p.starts_path() {
        return p.leaf(Expr::Name);
    }
    let path = p.expression_path();
    let start = p.tree.types.span(path).start;
    if let Some(read) = RULES.after_path_rule(p.tok().kind, p.ahead(1).kind) {
        return read(p, path);
    }
    p.add(start, Expr::Path(path))
}

/// Reads `as Type` after `value`.
fn cast(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    p.next();
    let ty = p.type_expr();
    p.add(p.start_of(value), Expr::Cast { value, ty })
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
    let op = RULES.binary[kind as usize].expect("binary is the rule of operator tokens only");
    let right = p.expr(op.precedence());
    p.add(p.start_of(left), Expr::Binary { op, left, right })
}

/// Reads the arguments of a call to `callee`. The type arguments of a
/// callee written `f::<T>` or `a::f::<T>` are the call's, and the callee
/// is the name or path without them.
fn call(p: &mut Parser, callee: Id<Expr>) -> Id<Expr> {
    let start = p.start_of(callee);
    let mut callee = callee;
    let mut type_arguments = List::EMPTY;
    if let Expr::Path(path) = p.tree.exprs[callee] {
        let segments: Vec<Id<Type>> = match p.tree.types[path] {
            Type::Path(segments) => p.tree.children.lists.items(segments).collect(),
            _ => vec![path],
        };
        let (&last, before) = segments.split_last().expect("a path has a name");
        if let Type::Generic { name, arguments } = p.tree.types[last] {
            type_arguments = arguments;
            callee = if before.is_empty() {
                p.tree.add(Expr::Name, name.span)
            } else {
                let last = p.tree.add(Type::Name, name.span);
                let items: Vec<_> = before.iter().chain([&last]).map(|&s| s.into()).collect();
                let segments = p.tree.children.lists.add(&items);
                let span = Span::new(start as usize, name.span.end as usize);
                let path = p.tree.add(Type::Path(segments), span);
                p.tree.add(Expr::Path(path), span)
            };
        }
    }
    let arguments = p.delimited(
        TokenKind::LeftParen,
        TokenKind::RightParen,
        false,
        starts_expression,
        spreadable,
    );
    p.add(
        start,
        Expr::Call {
            callee,
            type_arguments,
            arguments,
        },
    )
}

/// Reads an entry of an array literal or an argument of a call: `value` or
/// `value...`.
pub(crate) fn spreadable(p: &mut Parser) -> Id<Expr> {
    let start = p.tok().span.start;
    let value = p.expr(Precedence::Lowest);
    if !p.at(TokenKind::Ellipsis) {
        return value;
    }
    p.next();
    p.add(start, Expr::Spread(value))
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
    // `&[]T` and `&mut []T` are slice types; nothing after `&` reads `[]`.
    let after_mut = usize::from(p.at(TokenKind::Mut));
    let open = p.ahead(after_mut);
    if open.kind == TokenKind::LeftBracket && p.ahead(after_mut + 1).kind == TokenKind::RightBracket
    {
        let ty = p.reference_type(start);
        if p.at(TokenKind::LeftBrace) {
            let written = p.tree.file.slice(p.tree.types.span(ty));
            let fix = p.fix(Span::at(open.span.end as usize), "_");
            p.fail(
                Diagnostic::new(
                    diagnostics::INVALID_EXPRESSION,
                    format!("`{written}` is a slice type, so it takes no values"),
                )
                .label(&p.tree.file, p.tree.types.span(ty), "")
                .help(
                    "write `[_]` to lend an array's values as a slice",
                    vec![fix],
                ),
            );
        }
        return p.add(start, Expr::Type(ty));
    }
    let mutable = p.at(TokenKind::Mut);
    if mutable {
        p.next();
    }
    let value = p.expr(Precedence::Prefix);
    p.add(start, Expr::Reference { mutable, value })
}

/// Reads `[index]` after `value`.
fn index(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    let open = p.next();
    let index = p.expr(Precedence::Lowest);
    p.close(TokenKind::RightBracket, open);
    p.add(p.start_of(value), Expr::Index { value, index })
}

/// Reads `.name` or `.*` after `value`.
fn selector(p: &mut Parser, value: Id<Expr>) -> Id<Expr> {
    p.next();
    if p.at(TokenKind::Asterisk) {
        p.next();
        return p.add(p.start_of(value), Expr::Dereference(value));
    }
    let field = p.name();
    p.add(p.start_of(value), Expr::Selector { value, field })
}

#[cfg(test)]
mod tests {
    use tree::ast::Decl;
    use tree::ast::Stmt;
    use tree::ast::StringKind;
    use tree::ast::Tree;

    use super::*;
    use crate::testing;
    use crate::tests::dump_of;
    use crate::tests::one_error;

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
                let (tree, errors) = testing::parse_text(&text);
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
        let decl = tree.declarations().next().unwrap();
        let Decl::Function(f) = tree.decls[decl] else {
            panic!("not a function")
        };
        let statement = tree
            .children
            .lists
            .items(tree.blocks[f.body.unwrap()].statements)
            .next()
            .unwrap();
        let Stmt::Expression(e) = tree.stmts[statement] else {
            panic!("not an expression statement")
        };
        e
    }

    /// An expression with each binary operation and cast in parentheses.
    fn grouped(t: &Tree, id: Id<Expr>) -> String {
        let text = |span| t.file.slice(span).to_string();
        match t.exprs[id] {
            Expr::Cast { value, ty } => {
                format!("({} as {})", grouped(t, value), text(t.types.span(ty)))
            }
            Expr::Binary { op, left, right } => {
                format!(
                    "({} {} {})",
                    grouped(t, left),
                    op.symbol(),
                    grouped(t, right)
                )
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
            Expr::Dereference(value) => format!("{}.*", grouped(t, value)),
            Expr::Index { value, index } => {
                format!("{}[{}]", grouped(t, value), text(t.exprs.span(index)))
            }
            Expr::Call {
                callee, arguments, ..
            } => {
                let arguments: Vec<_> = t
                    .children
                    .lists
                    .items(arguments)
                    .map(|a| grouped(t, a))
                    .collect();
                format!("{}({})", grouped(t, callee), arguments.join(", "))
            }
            _ => text(t.exprs.span(id)),
        }
    }

    /// How operators group, from loosest to tightest, and that operators of
    /// one precedence group to the left. A cast binds tighter than every binary
    /// operator.
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
            ("x.y.z[0][i]", "x.y.z[0][i]"),
            ("-r.* * r.x.*", "(-r.* * r.x.*)"),
            ("&mut r.*.x", "&mut r.*.x"),
            ("a + b as i64", "(a + (b as i64))"),
            ("a as i32 * b", "((a as i32) * b)"),
        ] {
            let (tree, errors) = testing::parse_text(&format!("fn f() {{ {text}; }}"));
            assert!(errors.is_empty(), "{text}: {errors:?}");
            assert_eq!(grouped(&tree, first_expression(&tree)), want, "{text}");
        }
    }

    #[test]
    fn parses_expressions() {
        let text = r#"fn f() {
    none;
    Box::<i32>::new(1);
    Max::<f64, i32>(1);
    list::Sum::<u8>();
    sum(1, (a)..., &mut b...);
    items[n];
}
"#;
        let want = r#"module
  function declaration
    declared name f
    block
      expression statement
        none literal none
      expression statement
        call expression
          path
            generic type
              type name Box
              type name i32
            type name new
          number literal 1
      expression statement
        call expression
          name reference Max
          type name f64
          type name i32
          number literal 1
      expression statement
        call expression
          path
            type name list
            type name Sum
          type name u8
      expression statement
        call expression
          name reference sum
          number literal 1
          spread
            parentheses
              name reference a
          spread
            reference expression mut
              name reference b
      expression statement
        index expression
          name reference items
          name reference n
"#;
        assert!(testing::messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    #[test]
    fn expression_errors() {
        one_error("fn f() { a.; }", "expected `identifier`, found `;`", None);
    }

    /// Values after a slice type are an array's, lent: `&[_]i32{ 1, 2 }`.
    #[test]
    fn slice_type_with_values() {
        one_error(
            "fn f() { let s = &[]i32{ 1, 2 }; }",
            "`&[]i32` is a slice type, so it takes no values",
            Some("fn f() { let s = &[_]i32{ 1, 2 }; }"),
        );
    }

    /// A spelling that can only be a type is read as one where a value could go,
    /// for an argument that takes a type.
    #[test]
    fn type_expressions() {
        let text = "fn f() { g(&[]i32, &mut []u8, &x); }\n";
        let want = r#"module
  function declaration
    declared name f
    block
      expression statement
        call expression
          name reference g
          type expression
            slice type
              type name i32
          type expression
            slice type mut
              type name u8
          reference expression
            name reference x
"#;
        assert!(testing::messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    /// In an expression `<` compares; type arguments follow `::`.
    #[test]
    fn comparison_is_not_type_arguments() {
        for (text, want) in [
            ("a < b && c > d;", "binary"),
            ("Box::<T>::new();", "call"),
            ("a < b;", "binary"),
            ("f(a < b, c > d);", "call"),
            ("Max::<f64>(1);", "call"),
            ("a < b > (c);", "binary"),
            ("a < b && c > (d);", "binary"),
        ] {
            let (tree, errors) = testing::parse_text(&format!("fn f() {{ {text} }}"));
            assert!(errors.is_empty(), "{text}: {errors:?}");
            let got = match tree.exprs[first_expression(&tree)] {
                Expr::Binary { .. } => "binary",
                Expr::Call { .. } => "call",
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
            let (_, errors) = testing::parse_text(&text);
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

    /// `true` and `false` are bool literals, each spanning its word, and an
    /// operand like any value.
    #[test]
    fn bool_literals() {
        let (tree, errors) = testing::parse_text("fn f() { true; false; !true == false; }");
        assert!(errors.is_empty(), "{errors:?}");
        let literals: Vec<(bool, &str)> = (tree.exprs.ids())
            .filter_map(|id| match tree.exprs[id] {
                Expr::Bool(value) => Some((value, tree.file.slice(tree.exprs.span(id)))),
                _ => None,
            })
            .collect();
        assert_eq!(
            literals,
            [
                (true, "true"),
                (false, "false"),
                (true, "true"),
                (false, "false")
            ]
        );
    }

    /// `"..."` is a plain string literal and `c"..."` a C one, each spanning
    /// its quotes.
    #[test]
    fn string_literals() {
        let (tree, errors) = testing::parse_text(r#"fn f() { g("a", c"b"); }"#);
        assert!(errors.is_empty(), "{errors:?}");
        let literals: Vec<(StringKind, &str)> = (tree.exprs.ids())
            .filter_map(|id| match tree.exprs[id] {
                Expr::String(kind) => Some((kind, tree.file.slice(tree.exprs.span(id)))),
                _ => None,
            })
            .collect();
        assert_eq!(
            literals,
            [(StringKind::Plain, "\"a\""), (StringKind::C, "c\"b\"")]
        );
    }
}
