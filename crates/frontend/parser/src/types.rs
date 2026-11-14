//! Types: names and paths, references, slices, function types, and the
//! types a keyword begins, as the enum type of a type declaration.

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use source::Span;
use syntax::ast::Param;
use syntax::ast::Type;
use token::Token;
use token::TokenKind;

use crate::Grammar;
use crate::Parser;

/// Reads the type a token begins, after the token, as one starting at
/// `start`.
pub type TypeStartRule<L> = fn(&mut Parser<L>, u32) -> Id<Type<L>>;

/// Whether `kind` begins one of the types `Parser::type_expr` has an arm
/// for, so a new arm there is added here too.
pub(crate) const fn starts_core_type(kind: TokenKind) -> bool {
    matches!(
        kind,
        TokenKind::Ident | TokenKind::Ampersand | TokenKind::AndAnd | TokenKind::Fn
    )
}

/// Whether a type can begin with `kind`.
pub(crate) fn starts_type<L: Grammar>(kind: TokenKind) -> bool {
    starts_core_type(kind) || L::rules().type_starts[kind as usize].is_some()
}

impl<L: Grammar> Parser<L> {
    /// Reads a type.
    pub fn type_expr(&mut self) -> Id<Type<L>> {
        source::grow_stack(|| {
            let t = self.tok();
            match t.kind {
                TokenKind::Ident if self.ahead(1).kind == TokenKind::LeftBracket => {
                    let name = self.name();
                    let open = self.next();
                    let inner = self.type_expr();
                    self.close(TokenKind::RightBracket, open);
                    self.add(t.span.start, Type::Bracketed { name, inner })
                }
                TokenKind::Ident => self.named_type(),
                TokenKind::Ampersand => {
                    self.next();
                    self.reference_type(t.span.start)
                }
                // `&&T` is two references, though the lexer reads one token.
                TokenKind::AndAnd => {
                    self.next();
                    let target = self.reference_type(t.span.start + 1);
                    let mutable = false;
                    self.add(t.span.start, Type::Reference { mutable, target })
                }
                TokenKind::Fn => self.function_type(),
                _ => {
                    if let Some(read) = L::rules().type_starts[t.kind as usize] {
                        self.next();
                        return read(self, t.span.start);
                    }
                    self.fail(
                        Diagnostic::new(
                            diagnostics::INVALID_TYPE,
                            format!("expected type, found `{}`", t.kind.text()),
                        )
                        .label(
                            &self.tree.file,
                            t.span,
                            "expected a type here",
                        ),
                    );
                    self.add(self.last_end, Type::Invalid)
                }
            }
        })
    }

    /// Reads what follows a `&` that starts at `start`: `mut`, if written,
    /// then the target type, or `[]T` for a slice, its own type, not a
    /// reference to a `[]T`.
    pub(crate) fn reference_type(&mut self, start: u32) -> Id<Type<L>> {
        let mutable = self.at(TokenKind::Mut);
        if mutable {
            self.next();
        }
        if self.at(TokenKind::LeftBracket) && self.ahead(1).kind == TokenKind::RightBracket {
            self.next();
            self.next();
            let element = self.type_expr();
            return self.add(start, Type::Slice { mutable, element });
        }
        let target = self.type_expr();
        self.add(start, Type::Reference { mutable, target })
    }

    /// Reads a type name with its type arguments, if any (`Box<T>`), or a path
    /// of them (`a::B<T>`).
    pub(crate) fn named_type(&mut self) -> Id<Type<L>> {
        let start = self.tok().span.start;
        let first = self.type_segment();
        if !self.at(TokenKind::ColonColon) {
            return first;
        }
        let mark = self.list_items.len();
        self.list_items.push(first.into());
        while self.at(TokenKind::ColonColon) {
            self.next();
            let segment = self.type_segment();
            self.list_items.push(segment.into());
        }
        let segments = self.finish_list(mark);
        self.add(start, Type::Path(segments))
    }

    fn type_segment(&mut self) -> Id<Type<L>> {
        let start = self.tok().span.start;
        let name = self.name();
        if !self.at(TokenKind::Less) {
            return self.tree.add(Type::Name, name.span);
        }
        let open = self.next();
        let arguments = self.items(TokenKind::Greater, false, starts_type::<L>, |p| {
            p.type_expr()
        });
        self.close_angle(open);
        self.add(start, Type::Generic { name, arguments })
    }

    /// Reads the `>` closing type arguments. The lexer reads `>>` as one token;
    /// in `Box<Box<T>>` it closes two lists, so its first `>` is read here and
    /// the token, owned by the parser, is cut down to the second.
    pub(crate) fn close_angle(&mut self, open: Token) {
        let t = self.tok();
        if t.kind == TokenKind::ShiftRight {
            self.last_end = t.span.start + 1;
            let current = self.current();
            self.tokens[current] = Token {
                kind: TokenKind::Greater,
                span: Span::new(t.span.start as usize + 1, t.span.end as usize),
            };
            return;
        }
        self.close(TokenKind::Greater, open);
    }

    /// Reads `fn(name: T, U) -> R in name`; parameter names are optional.
    fn function_type(&mut self) -> Id<Type<L>> {
        let start = self.next().span.start;
        let signature = self.signature(
            List::EMPTY,
            |kind| kind == TokenKind::Underscore || starts_type::<L>(kind),
            |p| {
                let start = p.tok().span.start;
                let name = p.parameter_name();
                let ty = p.type_expr();
                p.add(
                    start,
                    Param {
                        constant: false,
                        mutable: false,
                        name,
                        ty,
                        default: None,
                    },
                )
            },
        );
        self.add(start, Type::Function(signature))
    }
}
