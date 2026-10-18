//! Types: names and paths, pointers, optionals, arrays, function types, and the
//! struct, enum and interface types of type declarations.

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ast::Decl;
use model::ast::Field;
use model::ast::Function;
use model::ast::Length;
use model::ast::Metadata;
use model::ast::Param;
use model::ast::Type;
use model::ast::Variant;
use source::Span;
use token::Token;
use token::TokenKind;

use crate::Parser;
use crate::grow;
use crate::is_name;

/// Whether a type can begin with `kind`.
pub(crate) fn starts_type(kind: TokenKind) -> bool {
    matches!(
        kind,
        TokenKind::Ident
            | TokenKind::Amp
            | TokenKind::Question
            | TokenKind::QuestionQuestion
            | TokenKind::Asterisk
            | TokenKind::Rawptr
            | TokenKind::LeftBracket
            | TokenKind::Fn
            | TokenKind::Struct
            | TokenKind::Enum
            | TokenKind::Iface
    )
}

impl Parser<'_> {
    /// Reads a type.
    pub(crate) fn type_expr(&mut self) -> Id<Type> {
        grow(|| {
            let t = self.tok();
            match t.kind {
                TokenKind::Ident => self.named_type(),
                TokenKind::Amp => {
                    self.next();
                    let mutable = self.at(TokenKind::Mut);
                    if mutable {
                        self.next();
                    }
                    let target = self.type_expr();
                    self.add(t.span.start, Type::Reference { mutable, target })
                }
                TokenKind::Question | TokenKind::QuestionQuestion => self.optional_type(),
                TokenKind::Asterisk => {
                    self.next();
                    let target = self.type_expr();
                    self.add(t.span.start, Type::OwnedPointer(target))
                }
                TokenKind::Rawptr => {
                    self.next();
                    self.add(t.span.start, Type::RawPointer)
                }
                TokenKind::LeftBracket => self.array_type(),
                TokenKind::Fn => self.function_type(),
                TokenKind::Struct => {
                    self.next();
                    self.struct_type(t.span.start)
                }
                TokenKind::Enum => {
                    self.next();
                    self.enum_type(t.span.start)
                }
                TokenKind::Iface => {
                    self.next();
                    self.interface_type(t.span.start)
                }
                _ => {
                    self.fail(
                        Diagnostic::new(
                            diagnostics::INVALID_TYPE,
                            format!("expected type, found `{}`", t.kind.text()),
                        )
                        .label(self.file, t.span, "expected a type here"),
                    );
                    self.add(self.last_end, Type::Invalid)
                }
            }
        })
    }

    /// Reads a type name with its type arguments, if any (`Box<T>`), or a path
    /// of them (`a::B<T>`).
    pub(crate) fn named_type(&mut self) -> Id<Type> {
        let start = self.tok().span.start;
        let first = self.type_segment();
        if !self.at(TokenKind::ColonColon) {
            return first;
        }
        let mark = self.scratch.len();
        self.scratch.push(first.into());
        while self.at(TokenKind::ColonColon) {
            self.next();
            let segment = self.type_segment();
            self.scratch.push(segment.into());
        }
        let segments = self.finish_list(mark);
        self.add(start, Type::Path(segments))
    }

    fn type_segment(&mut self) -> Id<Type> {
        let start = self.tok().span.start;
        let name = self.name();
        if !self.at(TokenKind::Less) {
            return self.tree.add(Type::Name, name.span);
        }
        let open = self.next();
        let arguments = self.items(TokenKind::Greater, false, starts_type, |p| p.type_expr());
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

    /// Reads `?T`. A second marker changes nothing (`??T` is `?T`); it is kept
    /// in the tree as written and left to a lint to report.
    fn optional_type(&mut self) -> Id<Type> {
        let mut markers = Vec::new(); // where each `?` starts; `??` is one token holding two
        while self.at(TokenKind::Question) || self.at(TokenKind::QuestionQuestion) {
            let t = self.next();
            markers.push(t.span.start);
            if t.kind == TokenKind::QuestionQuestion {
                markers.push(t.span.start + 1);
            }
        }
        let mut inner = self.type_expr();
        for &start in markers.iter().rev() {
            inner = self.add(start, Type::Optional(inner));
        }
        inner
    }

    /// Reads `[N]T`, `[]T` or `[..]T`.
    pub(crate) fn array_type(&mut self) -> Id<Type> {
        let open = self.next();
        let t = self.tok();
        match t.kind {
            TokenKind::RightBracket => {
                self.next();
                let element = self.type_expr();
                self.add(open.span.start, Type::DynamicArray(element))
            }
            TokenKind::DotDot => {
                self.next();
                self.close(TokenKind::RightBracket, open);
                let element = self.type_expr();
                self.add(open.span.start, Type::Slice(element))
            }
            TokenKind::Number => {
                self.next();
                self.check_number(t);
                self.close(TokenKind::RightBracket, open);
                let element = self.type_expr();
                let length = Length::Number(t.span);
                self.add(open.span.start, Type::FixedArray { length, element })
            }
            _ => {
                self.fail(
                    Diagnostic::new(diagnostics::INVALID_TYPE, "expected array length").label(
                        self.file,
                        t.span,
                        "use `[N]T` for fixed arrays, `[]T` for dynamic arrays, or `[..]T` for slices",
                    ),
                );
                self.add(open.span.start, Type::Invalid)
            }
        }
    }

    /// Reads `fn(name: T, U) -> R from name`; parameter names are optional.
    fn function_type(&mut self) -> Id<Type> {
        let start = self.next().span.start;
        let signature = self.signature(None, List::EMPTY, starts_type, |p| {
            let start = p.tok().span.start;
            let name = p.parameter_name();
            let ty = p.type_expr();
            p.add(
                start,
                Param {
                    mutable: false,
                    name,
                    ty,
                    default: None,
                },
            )
        });
        self.add(start, Type::Function(signature))
    }

    /// Reads `{ name: Type, ... }`, the fields of a struct type that starts at
    /// `start`.
    pub(crate) fn struct_type(&mut self, start: u32) -> Id<Type> {
        let fields = self.delimited(
            TokenKind::LeftBrace,
            TokenKind::RightBrace,
            true,
            is_name,
            |p| {
                let start = p.tok().span.start;
                let name = p.name();
                p.expect(TokenKind::Colon);
                let ty = p.type_expr();
                p.add(start, Field { name, ty })
            },
        );
        self.add(start, Type::Struct(fields))
    }

    /// Reads `{ Name, Name: Payload, ... }`, the variants of an enum type that
    /// starts at `start`.
    pub(crate) fn enum_type(&mut self, start: u32) -> Id<Type> {
        let variants = self.delimited(
            TokenKind::LeftBrace,
            TokenKind::RightBrace,
            true,
            is_name,
            |p| {
                let start = p.tok().span.start;
                let name = p.name();
                let payload = p.at(TokenKind::Colon).then(|| {
                    p.next();
                    p.type_expr()
                });
                p.add(start, Variant { name, payload })
            },
        );
        self.add(start, Type::Enum(variants))
    }

    /// Reads `{ fn (receiver) name(parameters) -> Result, ... }`, the methods of
    /// an interface type that starts at `start`: function signatures without
    /// bodies. A method needs a receiver and takes no default values.
    pub(crate) fn interface_type(&mut self, start: u32) -> Id<Type> {
        let methods = self.delimited(
            TokenKind::LeftBrace,
            TokenKind::RightBrace,
            true,
            |kind| kind == TokenKind::Fn,
            |p| {
                let start = p.next().span.start;
                if !p.at(TokenKind::LeftParen) {
                    p.expect(TokenKind::LeftParen);
                }
                let (name, signature) = p.named_signature();
                let parameters: Vec<_> = signature
                    .receiver
                    .into_iter()
                    .chain(p.tree.lists.items(signature.parameters))
                    .collect();
                for parameter in parameters {
                    if let Some(value) = p.tree.params[parameter].default {
                        let span = p.tree.exprs.span(value);
                        p.repair(
                            Diagnostic::new(
                                diagnostics::INVALID_DECLARATION,
                                "interface method defaults are not supported",
                            )
                            .label(p.file, span, ""),
                        );
                    }
                }
                p.add(
                    start,
                    Decl::Function(Function {
                        metadata: Metadata::NONE,
                        name,
                        signature,
                        body: None,
                    }),
                )
            },
        );
        self.add(start, Type::Interface(methods))
    }
}
