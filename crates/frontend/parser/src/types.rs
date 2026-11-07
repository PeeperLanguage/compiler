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
use crate::is_name;

/// Whether a type can begin with `kind`.
pub(crate) fn starts_type(kind: TokenKind) -> bool {
    matches!(
        kind,
        TokenKind::Ident
            | TokenKind::Ampersand
            | TokenKind::AndAnd
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

impl Parser {
    /// Reads a type.
    pub(crate) fn type_expr(&mut self) -> Id<Type> {
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
                TokenKind::Question | TokenKind::QuestionQuestion => self.optional_type(),
                TokenKind::Asterisk => {
                    self.next();
                    let target = self.type_expr();
                    self.add(t.span.start, Type::Pointer(target))
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
    pub(crate) fn reference_type(&mut self, start: u32) -> Id<Type> {
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
    pub(crate) fn named_type(&mut self) -> Id<Type> {
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

    /// Reads `[N]T`; a `[]T` alone is reported, as it has no fixed size.
    pub(crate) fn array_type(&mut self) -> Id<Type> {
        let open = self.next();
        let t = self.tok();
        // `[]T` alone, read whole so the message names it and parsing goes on.
        if t.kind == TokenKind::RightBracket {
            self.next();
            let element = self.type_expr();
            let ty = self.add(open.span.start, Type::Invalid);
            // An element reported already, as `[]i32` in `[][]i32`, is the
            // one mistake: its fix fixes the whole.
            if let Type::Invalid = self.tree.types[element] {
                return ty;
            }
            let element = self.tree.file.slice(self.tree.types.span(element));
            let message =
                format!("`[]{element}` is any number of `{element}`s, so it has no fixed size");
            let label =
                format!("use `[N]{element}` for a fixed array or `&[]{element}` for a slice");
            let span = self.tree.types.span(ty);
            self.repair(Diagnostic::new(diagnostics::INVALID_TYPE, message).label(
                &self.tree.file,
                span,
                label,
            ));
            return ty;
        }
        if t.kind != TokenKind::Number {
            self.fail(
                Diagnostic::new(diagnostics::INVALID_TYPE, "expected array length").label(
                    &self.tree.file,
                    t.span,
                    "use `[N]T` for fixed arrays or `&[]T` for slices",
                ),
            );
            return self.add(open.span.start, Type::Invalid);
        }
        self.next();
        self.check_number(t);
        self.close(TokenKind::RightBracket, open);
        let element = self.type_expr();
        let length = Length::Number(t.span);
        self.add(open.span.start, Type::FixedArray { length, element })
    }

    /// Reads `fn(name: T, U) -> R in name`; parameter names are optional.
    fn function_type(&mut self) -> Id<Type> {
        let start = self.next().span.start;
        let signature = self.signature(
            List::EMPTY,
            |kind| kind == TokenKind::Underscore || starts_type(kind),
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
                        variadic: false,
                        default: None,
                    },
                )
            },
        );
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

    /// Reads `{ fn name(parameters) -> Result, ... }`, the methods of an
    /// interface type that starts at `start`: function signatures without
    /// bodies. A method takes no default values.
    pub(crate) fn interface_type(&mut self, start: u32) -> Id<Type> {
        let methods = self.delimited(
            TokenKind::LeftBrace,
            TokenKind::RightBrace,
            true,
            |kind| kind == TokenKind::Fn,
            |p| {
                let start = p.next().span.start;
                let (name, signature) = p.named_signature();
                let parameters: Vec<_> =
                    p.tree.children.lists.items(signature.parameters).collect();
                for parameter in parameters {
                    if let Some(value) = p.tree.children.params[parameter].default {
                        let span = p.tree.exprs.span(value);
                        p.repair(
                            Diagnostic::new(
                                diagnostics::INVALID_DECLARATION,
                                "interface method defaults are not supported",
                            )
                            .label(&p.tree.file, span, ""),
                        );
                    }
                }
                p.add(
                    start,
                    Decl::Function(Function {
                        metadata: Metadata::NONE,
                        owner: None,
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
