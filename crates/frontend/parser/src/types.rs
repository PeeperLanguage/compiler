//! Types: names and paths, pointers, optionals, arrays, function types, and the
//! struct, enum and interface types of type declarations.

use ast::{Decl, Field, Id, Length, List, Param, Type, Variant};
use diagnostics::Diagnostic;
use source::Span;
use token::{Token, TokenKind};

use crate::{K, Parser, grow, is_name};

/// Whether a type can begin with `kind`.
pub(crate) fn starts_type(kind: TokenKind) -> bool {
    matches!(
        kind,
        K::Ident
            | K::Amp
            | K::Question
            | K::QuestionQuestion
            | K::Asterisk
            | K::Rawptr
            | K::LeftBracket
            | K::Fn
            | K::Struct
            | K::Enum
            | K::Iface
    )
}

impl Parser<'_> {
    /// Reads a type.
    pub(crate) fn type_expr(&mut self) -> Id<Type> {
        grow(|| {
            let t = self.tok();
            match t.kind {
                K::Ident => self.named_type(),
                K::Amp => {
                    self.next();
                    let mutable = self.at(K::Mut);
                    if mutable {
                        self.next();
                    }
                    let target = self.type_expr();
                    self.add(t.span.start, Type::Reference { mutable, target })
                }
                K::Question | K::QuestionQuestion => self.optional_type(),
                K::Asterisk => {
                    self.next();
                    let target = self.type_expr();
                    self.add(t.span.start, Type::OwnedPointer(target))
                }
                K::Rawptr => {
                    self.next();
                    self.add(t.span.start, Type::RawPointer)
                }
                K::LeftBracket => self.array_type(),
                K::Fn => self.function_type(),
                K::Struct => {
                    self.next();
                    self.struct_type(t.span.start)
                }
                K::Enum => {
                    self.next();
                    self.enum_type(t.span.start)
                }
                K::Iface => {
                    self.next();
                    self.interface_type(t.span.start)
                }
                _ => {
                    self.fail(
                        Diagnostic::new(
                            diagnostics::INVALID_TYPE,
                            format!("expected type, found `{}`", t.kind.name()),
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
        if !self.at(K::ColonColon) {
            return first;
        }
        let mark = self.scratch.len();
        self.scratch.push(first.into());
        while self.at(K::ColonColon) {
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
        if !self.at(K::Less) {
            return self.tree.add(Type::Name, name);
        }
        let open = self.next();
        let arguments = self.items(K::Greater, false, starts_type, |p| p.type_expr());
        self.close_angle(open);
        self.add(start, Type::Generic { name, arguments })
    }

    /// Reads the `>` closing type arguments. The lexer reads `>>` as one token;
    /// in `Box<Box<T>>` it closes two lists, so its first `>` is read here and
    /// the token, owned by the parser, is cut down to the second.
    pub(crate) fn close_angle(&mut self, open: Token) {
        let t = self.tok();
        if t.kind == K::ShiftRight {
            self.last_end = t.span.start + 1;
            let current = self.current();
            self.tokens[current] = Token {
                kind: K::Greater,
                span: Span::new(t.span.start as usize + 1, t.span.end as usize),
            };
            return;
        }
        self.close(K::Greater, open);
    }

    /// Reads `?T`. A second marker changes nothing (`??T` is `?T`); it is kept
    /// in the tree as written and left to a lint to report.
    fn optional_type(&mut self) -> Id<Type> {
        let mut markers = Vec::new(); // where each `?` starts; `??` is one token holding two
        while self.at(K::Question) || self.at(K::QuestionQuestion) {
            let t = self.next();
            markers.push(t.span.start);
            if t.kind == K::QuestionQuestion {
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
            K::RightBracket => {
                self.next();
                let element = self.type_expr();
                self.add(open.span.start, Type::DynamicArray(element))
            }
            K::DotDot => {
                self.next();
                self.close(K::RightBracket, open);
                let element = self.type_expr();
                self.add(open.span.start, Type::Slice(element))
            }
            K::Number => {
                self.next();
                self.check_number(t);
                self.close(K::RightBracket, open);
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
        let parameters = match self.expect(K::LeftParen) {
            Some(open) => {
                let parameters = self.items(K::RightParen, false, starts_type, |p| {
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
                self.close(K::RightParen, open);
                parameters
            }
            None => List::EMPTY,
        };
        let (result, origins) = self.result();
        self.add(
            start,
            Type::Function {
                parameters,
                result,
                origins,
            },
        )
    }

    /// Reads `{ name: Type, ... }`, the fields of a struct type that starts at
    /// `start`.
    pub(crate) fn struct_type(&mut self, start: u32) -> Id<Type> {
        let fields = self.members(is_name, |p| {
            let start = p.tok().span.start;
            let name = p.name();
            p.expect(K::Colon);
            let ty = p.type_expr();
            p.add(start, Field { name, ty })
        });
        self.add(start, Type::Struct(fields))
    }

    /// Reads `{ Name, Name: Payload, ... }`, the variants of an enum type that
    /// starts at `start`.
    pub(crate) fn enum_type(&mut self, start: u32) -> Id<Type> {
        let variants = self.members(is_name, |p| {
            let start = p.tok().span.start;
            let name = p.name();
            let payload = p.at(K::Colon).then(|| {
                p.next();
                p.type_expr()
            });
            p.add(start, Variant { name, payload })
        });
        self.add(start, Type::Enum(variants))
    }

    /// Reads `{ fn (receiver) name(parameters) -> Result, ... }`, the methods of
    /// an interface type that starts at `start`: function signatures without
    /// bodies. A method needs a receiver and takes no default values.
    pub(crate) fn interface_type(&mut self, start: u32) -> Id<Type> {
        let methods = self.members(
            |kind| kind == K::Fn,
            |p| {
                let start = p.next().span.start;
                if !p.at(K::LeftParen) {
                    p.expect(K::LeftParen);
                }
                let f = p.signature();
                let parameters: Vec<_> = f
                    .receiver
                    .into_iter()
                    .chain(p.tree.list(f.parameters))
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
                p.add(start, Decl::Function(f))
            },
        );
        self.add(start, Type::Interface(methods))
    }

    /// Reads the `{ ... }` list of the members of a struct, enum or interface
    /// type.
    fn members<T>(
        &mut self,
        starts_member: fn(TokenKind) -> bool,
        member: impl FnMut(&mut Self) -> Id<T>,
    ) -> List<T> {
        let Some(open) = self.expect(K::LeftBrace) else {
            return List::EMPTY;
        };
        let items = self.items(K::RightBrace, true, starts_member, member);
        self.close(K::RightBrace, open);
        items
    }
}
