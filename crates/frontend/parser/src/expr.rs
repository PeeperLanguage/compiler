//! Expressions, read by a Pratt parser. A prefix rule reads an expression that
//! a token begins (a name, a literal, `(`); an infix rule reads what a token
//! adds after an expression on its left (an operator and its right side, a
//! call). Each infix token has a precedence: it joins the expression being read
//! only when it binds tighter than the operator before it, so precedence and
//! associativity live in `prefix` and `infix` and not in nested grammar rules.

use ast::{Expr, FieldValue, Id, Length, Type};
use diagnostics::Diagnostic;
use token::{Token, TokenKind};

use crate::{K, Parser, grow, is_name};

/// How tightly an operator binds, from loosest to tightest.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub(crate) enum Prec {
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

/// What a token begins when it starts an expression.
#[derive(Clone, Copy)]
enum Prefix {
    NameOrPath,
    Number,
    String,
    Char,
    Bool,
    None,
    Parentheses,
    ArrayLiteral,
    StructLiteral,
    Unary,
    Reference,
    RawAddress,
    Builtin,
}

fn prefix(kind: TokenKind) -> Option<Prefix> {
    Some(match kind {
        // values
        K::Ident => Prefix::NameOrPath,
        K::Number => Prefix::Number,
        K::String | K::CString => Prefix::String,
        K::Char | K::ByteChar => Prefix::Char,
        K::True | K::False => Prefix::Bool,
        K::None => Prefix::None,
        K::LeftParen => Prefix::Parentheses,
        K::LeftBracket => Prefix::ArrayLiteral,
        K::Dot => Prefix::StructLiteral,
        // prefix operators and built-in calls
        K::Plus | K::Minus | K::Bang | K::Tilde => Prefix::Unary,
        K::Amp => Prefix::Reference,
        K::At => Prefix::RawAddress,
        K::Free | K::Print | K::Println => Prefix::Builtin,
        _ => return None,
    })
}

/// What a token adds after an expression.
#[derive(Clone, Copy)]
enum Infix {
    Binary,
    OrElse,
    Pipe,
    Is,
    Cast,
    Call,
    Index,
    Selector,
}

/// The precedence and rule of a token that continues an expression; a token
/// without one never continues an expression.
fn infix(kind: TokenKind) -> Option<(Prec, Infix)> {
    Some(match kind {
        // fallback and pipe
        K::Orelse => (Prec::OrElse, Infix::OrElse),
        K::PipeArrow => (Prec::Pipe, Infix::Pipe),
        // logical
        K::OrOr => (Prec::LogicalOr, Infix::Binary),
        K::AndAnd => (Prec::LogicalAnd, Infix::Binary),
        // bitwise
        K::Bar => (Prec::BitOr, Infix::Binary),
        K::Caret => (Prec::BitXor, Infix::Binary),
        K::Amp => (Prec::BitAnd, Infix::Binary),
        // equality and comparison
        K::EqualEqual | K::BangEqual => (Prec::Equality, Infix::Binary),
        K::Is => (Prec::Equality, Infix::Is),
        K::Less | K::LessEqual | K::Greater | K::GreaterEqual => (Prec::Compare, Infix::Binary),
        // arithmetic
        K::ShiftLeft | K::ShiftRight => (Prec::Shift, Infix::Binary),
        K::Plus | K::Minus => (Prec::Sum, Infix::Binary),
        K::Asterisk | K::Slash | K::Percent => (Prec::Product, Infix::Binary),
        K::As => (Prec::Cast, Infix::Cast),
        // call, index and selector
        K::LeftParen | K::LeftBracket | K::Dot => (
            Prec::Call,
            match kind {
                K::LeftParen => Infix::Call,
                K::LeftBracket => Infix::Index,
                _ => Infix::Selector,
            },
        ),
        _ => return None,
    })
}

/// Whether an expression can begin with `kind`.
pub(crate) fn starts_expression(kind: TokenKind) -> bool {
    prefix(kind).is_some()
}

impl Parser<'_> {
    /// Reads an expression whose operators bind tighter than `min`.
    pub(crate) fn expr(&mut self, min: Prec) -> Id<Expr> {
        grow(|| {
            let t = self.tok();
            let Some(rule) = prefix(t.kind) else {
                self.fail(
                    Diagnostic::new(
                        diagnostics::EXPECTED_TOKEN,
                        format!("expected expression, found `{}`", t.kind.name()),
                    )
                    .label(self.file, t.span, "expected an expression here"),
                );
                return self.add(self.last_end, Expr::Invalid);
            };
            let mut left = match rule {
                Prefix::NameOrPath => self.name_or_path(),
                Prefix::Number => self.number_literal(),
                Prefix::String => self.leaf(Expr::String {
                    c_string: t.kind == K::CString,
                }),
                Prefix::Char => self.leaf(Expr::Char {
                    byte: t.kind == K::ByteChar,
                }),
                Prefix::Bool => self.leaf(Expr::Bool),
                Prefix::None => self.leaf(Expr::None),
                Prefix::Parentheses => self.parentheses(),
                Prefix::ArrayLiteral => self.array_literal(),
                Prefix::StructLiteral => self.struct_literal(None, t.span.start),
                Prefix::Unary => self.unary(),
                Prefix::Reference => self.reference(),
                Prefix::RawAddress => self.raw_address(),
                Prefix::Builtin => self.builtin(),
            };
            while let Some((prec, rule)) = infix(self.tok().kind)
                && prec > min
            {
                left = match rule {
                    Infix::Binary => self.binary(left, prec),
                    Infix::OrElse => self.or_else(left),
                    Infix::Pipe => self.pipe(left),
                    Infix::Is => self.is(left),
                    Infix::Cast => self.cast(left),
                    Infix::Call => self.call(left),
                    Infix::Index => self.index(left),
                    Infix::Selector => self.selector(left),
                };
            }
            left
        })
    }

    /// Reads the current token as an expression without children.
    fn leaf(&mut self, node: Expr) -> Id<Expr> {
        let start = self.next().span.start;
        self.add(start, node)
    }

    /// The start of expression `id`, where an infix expression on it starts.
    fn start_of(&self, id: Id<Expr>) -> u32 {
        self.tree.exprs.span(id).start
    }

    /// Reads a name; or, when a path or type arguments follow it, a path
    /// (`a::B`, `Box<T>::new`), a struct literal (`Point.{...}`) or a variant
    /// literal (`Enum::Case with payload`).
    fn name_or_path(&mut self) -> Id<Expr> {
        if !self.starts_path() {
            return self.leaf(Expr::Name);
        }
        let path = self.named_type();
        let start = self.tree.types.span(path).start;
        if self.at(K::Dot) && self.ahead(1).kind == K::LeftBrace {
            return self.struct_literal(Some(path), start);
        }
        if self.at(K::With) && matches!(self.tree.types[path], Type::Path(_)) {
            self.next();
            let payload = self.expr(Prec::Lowest);
            return self.add(
                start,
                Expr::VariantLiteral {
                    case: path,
                    payload,
                },
            );
        }
        self.add(start, Expr::Path(path))
    }

    /// Whether the name at the current token begins a path or a type rather
    /// than standing alone: `::` follows it, or `.{`, or type arguments that
    /// `::` or `.{` follow.
    fn starts_path(&self) -> bool {
        match self.ahead(1).kind {
            K::ColonColon => true,
            K::Dot => self.ahead(2).kind == K::LeftBrace,
            K::Less => self.type_arguments_ahead(),
            _ => false,
        }
    }

    /// Whether the `<` after the current name opens type arguments: its
    /// matching `>` comes before the statement ends, and `::` or `.{` follows
    /// it. Only those two decide, so in `if a < b && c > d {` the `<` stays a
    /// comparison.
    fn type_arguments_ahead(&self) -> bool {
        let mut depth = 0i32;
        for i in self.current() + 1..self.tokens.len() {
            match self.tokens[i].kind {
                K::Less => depth += 1,
                K::Greater => depth -= 1,
                K::ShiftRight => depth -= 2,
                K::Semicolon | K::LeftBrace | K::RightBrace | K::Eof => return false,
                _ => {}
            }
            if depth < 0 {
                return false;
            }
            if depth == 0 {
                let next = self.tokens[i + 1].kind;
                return next == K::ColonColon
                    || next == K::Dot && self.tokens[i + 2].kind == K::LeftBrace;
            }
        }
        false
    }

    fn number_literal(&mut self) -> Id<Expr> {
        self.check_number(self.tok());
        self.leaf(Expr::Number)
    }

    /// Reads `( expression )`.
    fn parentheses(&mut self) -> Id<Expr> {
        let open = self.next();
        let inner = self.expr(Prec::Lowest);
        self.close(K::RightParen, open);
        self.add(open.span.start, Expr::Parentheses(inner))
    }

    /// Reads a binary operator of precedence `prec` and its right side. The
    /// right side takes only operators that bind tighter, so operators of one
    /// precedence group to the left: a - b - c is (a - b) - c.
    fn binary(&mut self, left: Id<Expr>, prec: Prec) -> Id<Expr> {
        let op = self.next().kind;
        let right = self.expr(prec);
        self.add(self.start_of(left), Expr::Binary { op, left, right })
    }

    /// Reads the arguments of a call to `callee`.
    fn call(&mut self, callee: Id<Expr>) -> Id<Expr> {
        let open = self.next();
        let arguments = self.items(K::RightParen, false, starts_expression, |p| {
            p.expr(Prec::Lowest)
        });
        self.close(K::RightParen, open);
        self.add(self.start_of(callee), Expr::Call { callee, arguments })
    }

    /// Reads `+x`, `-x`, `!x` or `~x`.
    fn unary(&mut self) -> Id<Expr> {
        let op = self.next();
        let value = self.expr(Prec::Prefix);
        self.add(op.span.start, Expr::Unary { op: op.kind, value })
    }

    /// Reads `&x` or `&mut x`.
    fn reference(&mut self) -> Id<Expr> {
        let start = self.next().span.start;
        let mutable = self.at(K::Mut);
        if mutable {
            self.next();
        }
        let value = self.expr(Prec::Prefix);
        self.add(start, Expr::Reference { mutable, value })
    }

    /// Reads `@x`.
    fn raw_address(&mut self) -> Id<Expr> {
        let start = self.next().span.start;
        let value = self.expr(Prec::Prefix);
        self.add(start, Expr::RawAddress(value))
    }

    /// Reads `free(x)`, `print(x)` or `println(x)`.
    fn builtin(&mut self) -> Id<Expr> {
        let keyword = self.next();
        let value = self.expect(K::LeftParen).map(|open| {
            let value = self.expr(Prec::Lowest);
            self.close(K::RightParen, open);
            value
        });
        let node = match keyword.kind {
            K::Free => Expr::Free(value),
            _ => Expr::Print {
                newline: keyword.kind == K::Println,
                value,
            },
        };
        self.add(keyword.span.start, node)
    }

    /// Reads `.{ name = value, ... }` after `ty` (none when there is no type), a
    /// literal that starts at `start`. A `name: value` field, the colon of a
    /// declaration where a field is set, is read as if it were `=`, and all such
    /// colons are reported in one error.
    fn struct_literal(&mut self, ty: Option<Id<Type>>, start: u32) -> Id<Expr> {
        self.next(); // `.`
        let mut colons = Vec::new();
        let fields = match self.expect(K::LeftBrace) {
            Some(open) => {
                let fields = self.items(K::RightBrace, false, is_name, |p| {
                    let field_start = p.tok().span.start;
                    let name = p.name();
                    let t = p.tok();
                    if t.kind == K::Colon {
                        p.next();
                        colons.push(p.fix(t.span, " ="));
                    } else {
                        p.expect(K::Assign);
                    }
                    let value = p.expr(Prec::Lowest);
                    p.add(field_start, FieldValue { name, value })
                });
                self.close(K::RightBrace, open);
                fields
            }
            None => ast::List::EMPTY,
        };
        if !colons.is_empty() {
            let d = Diagnostic::new(
                diagnostics::EXPECTED_TOKEN,
                "expected `=` after struct literal field name",
            )
            .label(self.file, colons[0].span, "");
            self.repair(d.help("set a field with `=`", colons));
        }
        self.add(start, Expr::StructLiteral { ty, fields })
    }

    /// Reads `[N]T{ values }`, `[_]T{ values }` (the length is the number of
    /// values) or `[]T{ values }`.
    fn array_literal(&mut self) -> Id<Expr> {
        let start = self.tok().span.start;
        let after = self.ahead(1);
        let ty = if after.kind == K::Ident && self.text(after) == "_" {
            let open = self.next();
            let length = Length::Inferred(self.next().span);
            self.close(K::RightBracket, open);
            let element = self.type_expr();
            self.add(open.span.start, Type::FixedArray { length, element })
        } else {
            self.array_type()
        };
        let values = match self.expect(K::LeftBrace) {
            Some(open) => {
                let values = self.items(K::RightBrace, false, starts_expression, |p| {
                    p.expr(Prec::Lowest)
                });
                self.close(K::RightBrace, open);
                values
            }
            None => ast::List::EMPTY,
        };
        self.add(start, Expr::ArrayLiteral { ty, values })
    }

    /// Reads `[index]` after `value`.
    fn index(&mut self, value: Id<Expr>) -> Id<Expr> {
        let open = self.next();
        let index = self.index_operand();
        self.close(K::RightBracket, open);
        self.add(self.start_of(value), Expr::Index { value, index })
    }

    /// Reads what may stand inside `[ ]` or after `in`: an expression, or a
    /// range `a..b`, `a..=b`, `..b` or `a..`. Ranges appear nowhere else, and
    /// only right before the `]` may a range leave out its end.
    pub(crate) fn index_operand(&mut self) -> Id<Expr> {
        let start = self.tok().span.start;
        let is_range = |p: &Self| p.at(K::DotDot) || p.at(K::DotDotEq);
        let mut low = None;
        if !is_range(self) {
            let value = self.expr(Prec::Lowest);
            if !is_range(self) {
                return value;
            }
            low = Some(value);
        }
        let op = self.next();
        let mut high = None;
        if !self.at(K::RightBracket) {
            high = Some(self.expr(Prec::Lowest));
        } else if op.kind == K::DotDotEq {
            self.repair(
                Diagnostic::new(
                    diagnostics::INVALID_EXPRESSION,
                    "inclusive range requires an end bound",
                )
                .label(
                    self.file,
                    op.span,
                    "add the inclusive end bound after `..=`",
                ),
            );
        }
        let inclusive = op.kind == K::DotDotEq;
        self.add(
            start,
            Expr::Range {
                inclusive,
                start: low,
                end: high,
            },
        )
    }

    /// Reads `.name` after `value`.
    fn selector(&mut self, value: Id<Expr>) -> Id<Expr> {
        self.next();
        let field = self.name();
        self.add(self.start_of(value), Expr::Selector { value, field })
    }

    /// Reads `as Type` after `value`.
    fn cast(&mut self, value: Id<Expr>) -> Id<Expr> {
        self.next();
        let ty = self.type_expr();
        self.add(self.start_of(value), Expr::Cast { value, ty })
    }

    /// Reads `is Enum::Case` after `value`; the case is a full path.
    fn is(&mut self, value: Id<Expr>) -> Id<Expr> {
        self.next();
        let case = self.type_expr();
        self.require_path(case);
        self.add(self.start_of(value), Expr::Is { value, case })
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
                .label(self.file, span, "write `Enum::Variant`"),
            );
        }
    }

    /// Reads `orelse fallback` or `orelse { block }` after `value`. The fallback
    /// may itself end in `orelse`: a orelse b orelse c is a orelse (b orelse c).
    fn or_else(&mut self, value: Id<Expr>) -> Id<Expr> {
        self.next();
        let fallback = if self.at(K::LeftBrace) {
            let block = self.block();
            let span = self.tree.blocks.span(block);
            self.tree.add(Expr::Block(block), span)
        } else {
            self.expr(Prec::Lowest) // one below OrElse: groups to the right
        };
        self.add(self.start_of(value), Expr::OrElse { value, fallback })
    }

    /// Reads `|> f(arguments)` after `value`. Its right side takes only what
    /// binds tighter than a prefix operator, a call, so `a |> len() as i32` is
    /// (a |> len()) as i32. The target must call a function, not a method.
    fn pipe(&mut self, value: Id<Expr>) -> Id<Expr> {
        self.next();
        let call = self.expr(Prec::Prefix);
        match self.tree.exprs[call] {
            Expr::Call { callee, .. } => {
                if let Expr::Selector { .. } = self.tree.exprs[callee] {
                    let span = self.tree.exprs.span(callee);
                    self.repair(
                        Diagnostic::new(
                            diagnostics::INVALID_EXPRESSION,
                            "pipe cannot call a method",
                        )
                        .label(self.file, span, "use `value.method(...)`"),
                    );
                }
            }
            _ => {
                let span = self.tree.exprs.span(call);
                self.repair(
                    Diagnostic::new(
                        diagnostics::INVALID_EXPRESSION,
                        "pipe target must be a free-function call",
                    )
                    .label(self.file, span, "write `value |> function(...)`"),
                );
            }
        }
        self.add(self.start_of(value), Expr::Pipe { value, call })
    }

    /// Checks the type suffix of number literal `t`, the part of a number the
    /// lexer leaves to the parser. Whether the value fits its type is the type
    /// checker's to decide.
    pub(crate) fn check_number(&mut self, t: Token) {
        let literal = numeric::split(self.text(t));
        let message = if literal.suffix.is_empty() {
            return; // most numbers
        } else if !literal.suffix_well_formed() {
            format!("invalid numeric literal suffix {}", literal.suffix)
        } else if literal.integer_suffix() && literal.is_float() {
            format!(
                "integer suffix {} cannot be used on a float literal",
                literal.suffix
            )
        } else {
            return;
        };
        self.repair(
            Diagnostic::new(diagnostics::INVALID_NUMBER_SUFFIX, message)
                .label(self.file, t.span, ""),
        );
    }
}

#[cfg(test)]
mod tests {
    use ast::{Decl, Stmt, Tree};

    use super::*;
    use crate::tests::{dump_of, messages, one_error, parse_text};

    /// The expression of the first statement of the first function.
    fn first_expression(tree: &Tree) -> Id<Expr> {
        let decl = tree.list(tree.declarations).next().unwrap();
        let Decl::Function(f) = tree.decls[decl] else {
            panic!("not a function")
        };
        let statement = tree
            .list(tree.blocks[f.body.unwrap()].statements)
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
                format!("({} {} {})", grouped(t, left), op.name(), grouped(t, right))
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
            Expr::Selector { value, field } => format!("{}.{}", grouped(t, value), text(field)),
            Expr::Index { value, index } => {
                format!("{}[{}]", grouped(t, value), text(t.exprs.span(index)))
            }
            Expr::Call { callee, arguments } => {
                let arguments: Vec<_> = t.list(arguments).map(|a| grouped(t, a)).collect();
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
