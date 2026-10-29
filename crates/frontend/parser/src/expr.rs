//! Expressions, read by a Pratt parser. A prefix rule reads an expression that
//! a token begins (a name, a literal, `(`); an infix rule reads what a token
//! adds after an expression on its left (an operator and its right side, a
//! call). Each token with an infix rule has a precedence: it joins the expression being read only when it
//! binds tighter than the operator before it, so precedence and associativity
//! live in the table below and not in nested grammar rules.

use arena::Id;
use diagnostics::Diagnostic;
use model::ast::Expr;
use model::ast::Type;
use model::operators::BinaryOp;
use model::operators::Precedence;
use model::operators::UnaryOp;
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

/// Reads the expression the current token begins.
type PrefixRule = fn(&mut Parser) -> Id<Expr>;

/// Reads what the current token adds to the expression `left`.
type InfixRule = fn(&mut Parser, Id<Expr>) -> Id<Expr>;

/// The rules of each token, indexed by token kind. A token without a prefix
/// rule cannot begin an expression; a token without an infix rule never
/// continues one. `binary` is the operator a token of the `binary` rule
/// writes.
struct Rules {
    prefix: [Option<PrefixRule>; 256],
    infix: [Option<(Precedence, InfixRule)>; 256],
    binary: [Option<BinaryOp>; 256],
}

impl Rules {
    const fn prefix(&mut self, kind: TokenKind, read: PrefixRule) {
        self.prefix[kind as usize] = Some(read);
    }

    const fn infix(&mut self, kind: TokenKind, precedence: Precedence, read: InfixRule) {
        self.infix[kind as usize] = Some((precedence, read));
    }

    const fn binary_operator(&mut self, kind: TokenKind, op: BinaryOp) {
        self.infix(kind, op.precedence(), binary);
        self.binary[kind as usize] = Some(op);
    }
}

static RULES: Rules = {
    let mut rules = Rules {
        prefix: [None; 256],
        infix: [None; 256],
        binary: [None; 256],
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

    // ranges, fallback and pipe
    rules.prefix(TokenKind::DotDot, open_range);
    rules.prefix(TokenKind::DotDotEqual, open_range);
    rules.infix(TokenKind::DotDot, Precedence::Range, range);
    rules.infix(TokenKind::DotDotEqual, Precedence::Range, range);
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
    rules.infix(TokenKind::Is, Precedence::Equality, is_expression);
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

/// Reads a range with no start: `..b`, `..=b`, or `..` before `]`.
fn open_range(p: &mut Parser) -> Id<Expr> {
    let start = p.tok().span.start;
    range_from(p, start, None)
}

/// Reads a range from `low`: `a..b`, `a..=b`, or `a..` before `]`.
fn range(p: &mut Parser, low: Id<Expr>) -> Id<Expr> {
    range_from(p, p.start_of(low), Some(low))
}

/// Reads `..` or `..=` and what follows it: an end, then `:step` to step by
/// more than one. Only right before the `]` may a range leave out its end.
fn range_from(p: &mut Parser, start: u32, low: Option<Id<Expr>>) -> Id<Expr> {
    let op = p.next();
    let mut high = None;
    if !p.at(TokenKind::RightBracket) {
        high = Some(p.expr(Precedence::Range));
    } else if op.kind == TokenKind::DotDotEqual {
        p.repair(
            Diagnostic::new(
                diagnostics::INVALID_EXPRESSION,
                "inclusive range requires an end bound",
            )
            .label(
                &p.tree.file,
                op.span,
                "add the inclusive end bound after `..=`",
            ),
        );
    }
    let mut step = None;
    if high.is_some() && p.at(TokenKind::Colon) {
        p.next();
        step = Some(p.expr(Precedence::Range));
    }
    let inclusive = op.kind == TokenKind::DotDotEqual;
    p.add(
        start,
        Expr::Range {
            inclusive,
            start: low,
            end: high,
            step,
        },
    )
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

/// Reads `free(x)`, or `print(values)` or `println(values)`, which take any
/// number of values.
fn builtin(p: &mut Parser) -> Id<Expr> {
    let keyword = p.next();
    let node = match keyword.kind {
        TokenKind::Free => Expr::Free(p.expect(TokenKind::LeftParen).map(|open| {
            let value = p.expr(Precedence::Lowest);
            p.close(TokenKind::RightParen, open);
            value
        })),
        _ => Expr::Print {
            newline: keyword.kind == TokenKind::Println,
            values: p.delimited(
                TokenKind::LeftParen,
                TokenKind::RightParen,
                false,
                starts_expression,
                |p| p.expr(Precedence::Lowest),
            ),
        },
    };
    p.add(keyword.span.start, node)
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
