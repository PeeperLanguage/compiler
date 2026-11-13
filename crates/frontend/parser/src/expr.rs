//! Expressions, read by a Pratt parser. A prefix rule reads an expression that
//! a token begins (a name, a literal, `(`); an infix rule reads what a token
//! adds after an expression on its left (an operator and its right side, a
//! call). Each token with an infix rule has a precedence: it joins the expression being read only when it
//! binds tighter than the operator before it, so precedence and associativity
//! live in the table below and not in nested grammar rules.

use arena::Id;
use diagnostics::Diagnostic;
use source::Span;
use syntax::ast::Expr;
use syntax::ast::Type;
use syntax::operators::BinaryOp;
use syntax::operators::Precedence;
use syntax::operators::UnaryOp;
use token::TokenKind;

use crate::Grammar;
use crate::Parser;
use crate::Rules;
use crate::literals::array_literal;
use crate::literals::character_literal;
use crate::literals::none_literal;
use crate::literals::number_literal;

/// Reads the expression the current token begins.
pub type PrefixRule<L> = fn(&mut Parser<L>) -> Id<Expr<L>>;

/// Reads what continues the path `path` in an expression, from the token
/// after it.
pub type AfterPathRule<L> = fn(&mut Parser<L>, Id<Type<L>>) -> Id<Expr<L>>;

/// Reads what the current token adds to the expression `left`.
pub type InfixRule<L> = fn(&mut Parser<L>, Id<Expr<L>>) -> Id<Expr<L>>;

impl<L: Grammar> Rules<L> {
    pub const fn prefix(&mut self, kind: TokenKind, read: PrefixRule<L>) {
        assert!(
            self.prefix[kind as usize].is_none(),
            "two rules begin an expression with one token"
        );
        self.prefix[kind as usize] = Some(read);
    }

    pub const fn infix(&mut self, kind: TokenKind, precedence: Precedence, read: InfixRule<L>) {
        assert!(
            self.infix[kind as usize].is_none(),
            "two rules continue an expression with one token"
        );
        self.infix[kind as usize] = Some((precedence, read));
    }

    /// The rule that reads what continues a path when the two tokens after
    /// it are `first` and `second`.
    fn after_path_rule(&self, first: TokenKind, second: TokenKind) -> Option<AfterPathRule<L>> {
        let (paired, read) = self.after_path[first as usize]?;
        (paired == second).then_some(read)
    }

    const fn binary_operator(&mut self, kind: TokenKind, op: BinaryOp) {
        self.infix(kind, op.precedence(), binary);
        self.binary[kind as usize] = Some(op);
    }
}

/// Adds the core's expression rules to `rules`.
pub(crate) const fn expression_rows<L: Grammar>(rules: &mut Rules<L>) {
    // values
    rules.prefix(TokenKind::Ident, name_or_path);
    rules.prefix(TokenKind::SelfValue, self_name);
    rules.prefix(TokenKind::Number, number_literal);
    rules.prefix(TokenKind::Char, character_literal);
    rules.prefix(TokenKind::ByteChar, character_literal);
    rules.prefix(TokenKind::None, none_literal);
    rules.prefix(TokenKind::LeftParen, parentheses);
    rules.prefix(TokenKind::LeftBracket, array_literal);

    // prefix operators and built-in calls
    rules.prefix(TokenKind::Plus, leading_plus);
    rules.prefix(TokenKind::Minus, unary);
    rules.prefix(TokenKind::Bang, unary);
    rules.prefix(TokenKind::Tilde, unary);
    rules.prefix(TokenKind::Ampersand, reference);
    rules.prefix(TokenKind::Print, print);
    rules.prefix(TokenKind::Println, print);

    // ranges, fallback and pipe
    rules.infix(TokenKind::Orelse, Precedence::OrElse, or_else);
    rules.infix(TokenKind::PipeArrow, Precedence::Pipe, pipe);

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
    rules.infix(TokenKind::Is, Precedence::Equality, case_test);
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
}

/// Whether an expression can begin with `kind`.
pub(crate) fn starts_expression<L: Grammar>(kind: TokenKind) -> bool {
    L::rules().prefix[kind as usize].is_some()
}

impl<L: Grammar> Parser<L> {
    /// Reads an expression whose operators bind tighter than `min`.
    pub fn expr(&mut self, min: Precedence) -> Id<Expr<L>> {
        source::grow_stack(|| {
            let t = self.tok();
            let Some(read) = L::rules().prefix[t.kind as usize] else {
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
            while let Some((precedence, read)) = L::rules().infix[self.tok().kind as usize]
                && precedence > min
            {
                left = read(self, left);
            }
            left
        })
    }

    /// Reads the current token as an expression without children.
    pub(crate) fn leaf(&mut self, node: Expr<L>) -> Id<Expr<L>> {
        let start = self.next().span.start;
        self.add(start, node)
    }

    /// The start of expression `id`, where an infix expression on it starts.
    pub fn start_of(&self, id: Id<Expr<L>>) -> u32 {
        self.tree.exprs.span(id).start
    }

    /// Whether the name at the current token begins a path or a type rather
    /// than standing alone: `::` follows it, or `.{`, or type arguments that
    /// `::` or `.{` follow.
    fn starts_path(&self) -> bool {
        match self.ahead(1).kind {
            TokenKind::ColonColon => true,
            TokenKind::Less => self.opens_type_arguments(),
            first => L::rules()
                .after_path_rule(first, self.ahead(2).kind)
                .is_some(),
        }
    }

    /// Whether the `<` after the current name opens type arguments: its
    /// matching `>` comes before the statement ends, and `::` or what
    /// continues a path (`.{`) follows it. Only those decide, so in
    /// `if a < b && c > d {` the `<` stays a comparison.
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
                let after = self.tokens.get(i + 2).map_or(TokenKind::Eof, |t| t.kind);
                return next == TokenKind::ColonColon
                    || L::rules().after_path_rule(next, after).is_some();
            }
        }
        false
    }

    /// Reports `case` unless it is a full path `Enum::Case` (or a type already
    /// reported as invalid).
    pub(crate) fn require_path(&mut self, case: Id<Type<L>>) {
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
/// Reads `self`, the name of a type's function's first parameter.
fn self_name<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    p.leaf(Expr::Name)
}

fn name_or_path<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    if !p.starts_path() {
        return p.leaf(Expr::Name);
    }
    let path = p.named_type();
    let start = p.tree.types.span(path).start;
    if let Some(read) = L::rules().after_path_rule(p.tok().kind, p.ahead(1).kind) {
        return read(p, path);
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
fn parentheses<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    let open = p.next();
    let inner = p.expr(Precedence::Lowest);
    p.close(TokenKind::RightParen, open);
    p.add(open.span.start, Expr::Parentheses(inner))
}

/// Reads a binary operator and its right side. The right side takes only
/// operators that bind tighter, so operators of one precedence group to the
/// left: a - b - c is (a - b) - c.
fn binary<L: Grammar>(p: &mut Parser<L>, left: Id<Expr<L>>) -> Id<Expr<L>> {
    let kind = p.next().kind;
    let op = L::rules().binary[kind as usize].expect("binary is the rule of operator tokens only");
    let right = p.expr(op.precedence());
    p.add(p.start_of(left), Expr::Binary { op, left, right })
}

/// Reads the arguments of a call to `callee`.
fn call<L: Grammar>(p: &mut Parser<L>, callee: Id<Expr<L>>) -> Id<Expr<L>> {
    let arguments = p.delimited(
        TokenKind::LeftParen,
        TokenKind::RightParen,
        false,
        starts_expression::<L>,
        spreadable,
    );
    p.add(p.start_of(callee), Expr::Call { callee, arguments })
}

/// Reads an entry of an array literal or an argument of a call: `value` or
/// `value...`.
pub(crate) fn spreadable<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
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
fn leading_plus<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
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
fn unary<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
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
fn reference<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
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

/// Reads `print(values)` or `println(values)`, which take any number of
/// values.
fn print<L: Grammar>(p: &mut Parser<L>) -> Id<Expr<L>> {
    let keyword = p.next();
    let node = Expr::Print {
        newline: keyword.kind == TokenKind::Println,
        values: p.delimited(
            TokenKind::LeftParen,
            TokenKind::RightParen,
            false,
            starts_expression::<L>,
            |p| p.expr(Precedence::Lowest),
        ),
    };
    p.add(keyword.span.start, node)
}

/// Reads `[index]` after `value`.
fn index<L: Grammar>(p: &mut Parser<L>, value: Id<Expr<L>>) -> Id<Expr<L>> {
    let open = p.next();
    let index = p.expr(Precedence::Lowest);
    p.close(TokenKind::RightBracket, open);
    p.add(p.start_of(value), Expr::Index { value, index })
}

/// Reads `.name` or `.*` after `value`.
fn selector<L: Grammar>(p: &mut Parser<L>, value: Id<Expr<L>>) -> Id<Expr<L>> {
    p.next();
    if p.at(TokenKind::Asterisk) {
        p.next();
        return p.add(p.start_of(value), Expr::Dereference(value));
    }
    let field = p.name();
    p.add(p.start_of(value), Expr::Selector { value, field })
}

/// Reads `is Enum::Case` after `value`; the case is a full path.
fn case_test<L: Grammar>(p: &mut Parser<L>, value: Id<Expr<L>>) -> Id<Expr<L>> {
    p.next();
    let case = p.type_expr();
    p.require_path(case);
    p.add(p.start_of(value), Expr::Is { value, case })
}

/// Reads `orelse fallback` or `orelse { block }` after `value`. The fallback
/// may itself end in `orelse`: a orelse b orelse c is a orelse (b orelse c).
fn or_else<L: Grammar>(p: &mut Parser<L>, value: Id<Expr<L>>) -> Id<Expr<L>> {
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
fn pipe<L: Grammar>(p: &mut Parser<L>, value: Id<Expr<L>>) -> Id<Expr<L>> {
    p.next();
    let call = p.expr(Precedence::Prefix);
    match p.tree.exprs[call] {
        Expr::Call { callee, .. } if matches!(p.tree.exprs[callee], Expr::Selector { .. }) => {
            let span = p.tree.exprs.span(callee);
            p.repair(
                Diagnostic::new(diagnostics::INVALID_EXPRESSION, "pipe cannot call a method")
                    .label(&p.tree.file, span, "use `value.method(...)`"),
            );
        }
        Expr::Call { .. } => {}
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
mod tests;
