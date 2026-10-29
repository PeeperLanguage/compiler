//! The module and its declarations: imports, functions, constants and type
//! declarations, with their doc comments and attributes.

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ast::Attribute;
use model::ast::Decl;
use model::ast::Expr;
use model::ast::Function;
use model::ast::Ident;
use model::ast::Import;
use model::ast::Metadata;
use model::ast::Name;
use model::ast::Param;
use model::ast::Signature;
use model::ast::Type;
use model::ast::TypeDecl;
use model::operators::Precedence;
use source::Span;
use token::TokenKind;

use crate::Parser;
use crate::expr::starts_expression;
use crate::is_name;
use crate::types::starts_type;

/// Reads the declaration the current keyword begins, with its doc comment and
/// attributes.
type DeclarationRule = fn(&mut Parser, Metadata) -> Id<Decl>;

/// The reader of each declaration a keyword begins, indexed by token kind;
/// each takes the declaration's doc comment and attributes.
static DECLARATIONS: [Option<DeclarationRule>; 256] = {
    let mut declarations: [Option<DeclarationRule>; 256] = [None; 256];
    declarations[TokenKind::Fn as usize] = Some(function_decl);
    declarations[TokenKind::Const as usize] = Some(const_decl);
    declarations[TokenKind::Struct as usize] = Some(type_decl);
    declarations[TokenKind::Enum as usize] = Some(type_decl);
    declarations[TokenKind::Iface as usize] = Some(type_decl);
    declarations[TokenKind::Type as usize] = Some(type_decl);
    declarations
};

/// Whether `kind` begins a declaration or an import.
pub(crate) fn starts_declaration(kind: TokenKind) -> bool {
    DECLARATIONS[kind as usize].is_some() || kind == TokenKind::Import
}

fn starts_parameter(kind: TokenKind) -> bool {
    kind == TokenKind::Mut || kind == TokenKind::Underscore || starts_type(kind)
}

impl Parser {
    pub(crate) fn module(&mut self) {
        let mut imports = Vec::new();
        let mark = self.scratch.len();
        loop {
            let metadata = self.metadata();
            let kind = self.tok().kind;
            if starts_declaration(kind) {
                self.recovering = false;
            }
            let first = self.tree.children.lists.items(metadata.attributes).next();
            if DECLARATIONS[kind as usize].is_none()
                && let Some(attribute) = first
            {
                let span = self.tree.children.attributes.span(attribute);
                self.repair(
                    Diagnostic::new(
                        diagnostics::INVALID_DECLARATION,
                        "an attribute needs a declaration after it",
                    )
                    .label(&self.tree.file, span, ""),
                );
            }
            match kind {
                TokenKind::Eof => {
                    self.tree.imports = imports;
                    self.tree.top_level = self.finish_list(mark);
                    return;
                }
                TokenKind::Import => imports.extend(self.import_decl()),
                kind if let Some(read) = DECLARATIONS[kind as usize] => {
                    let decl = read(self, metadata);
                    self.scratch.push(decl.into());
                }
                TokenKind::Semicolon => {
                    self.next();
                }
                TokenKind::Let => {
                    // Read it to go on in step, but keep no module-level variable.
                    let keyword = self.next();
                    self.fail(
                        Diagnostic::new(
                            diagnostics::INVALID_DECLARATION,
                            "top-level `let` not allowed",
                        )
                        .label(&self.tree.file, keyword.span, "")
                        .help("use `const` for module-scope values", Vec::new()),
                    );
                    self.binding();
                }
                _ => {
                    // One error for the whole stretch, then go on at the next declaration.
                    let t = self.tok();
                    self.fail(
                        Diagnostic::new(
                            diagnostics::INVALID_DECLARATION,
                            "module scope expects declaration",
                        )
                        .label(&self.tree.file, t.span, "expected a declaration here")
                        .help("move this statement into a function", Vec::new()),
                    );
                    while !self.at(TokenKind::Eof) && !starts_declaration(self.tok().kind) {
                        self.next();
                    }
                }
            }
        }
    }

    /// Reads the doc comment and attributes in front of a declaration.
    fn metadata(&mut self) -> Metadata {
        let doc_comment = self.doc_comment();
        let mark = self.scratch.len();
        while self.at(TokenKind::Hash) {
            let attribute = self.attribute();
            self.scratch.push(attribute.into());
        }
        Metadata {
            doc_comment,
            attributes: self.finish_list(mark),
        }
    }

    /// Reads the `///` lines in front of a declaration. A `///` on the line of
    /// earlier code belongs to that code, not to what follows.
    fn doc_comment(&mut self) -> Option<Span> {
        let mut start = None;
        while self.tokens[self.pos].kind == TokenKind::DocComment {
            let t = self.tokens[self.pos];
            self.pos += 1;
            let before = self.pos - 1;
            if before > 0
                && self.tokens[before - 1].kind != TokenKind::DocComment
                && self.line(self.tokens[before - 1].span.end) == self.line(t.span.start)
            {
                start = None;
                continue;
            }
            start.get_or_insert(t.span.start);
            self.last_end = t.span.end;
        }
        start.map(|start| Span::new(start as usize, self.last_end as usize))
    }

    /// Reads `#[name]` or `#[name(arguments)]`; one attribute per block.
    fn attribute(&mut self) -> Id<Attribute> {
        let start = self.next().span.start;
        let Some(open) = self.expect(TokenKind::LeftBracket) else {
            let name = Ident { span: self.here() };
            return self.add(
                start,
                Attribute {
                    name,
                    arguments: List::EMPTY,
                },
            );
        };
        let name = self.name();
        let mut arguments = List::EMPTY;
        if self.at(TokenKind::LeftParen) {
            arguments = self.delimited(
                TokenKind::LeftParen,
                TokenKind::RightParen,
                false,
                starts_expression,
                |p| p.expr(Precedence::Lowest),
            );
        }
        let t = self.tok();
        if t.kind == TokenKind::Comma {
            self.fail(
                Diagnostic::new(
                    diagnostics::EXPECTED_TOKEN,
                    "only one attribute is allowed per `#[...]` block",
                )
                .label(
                    &self.tree.file,
                    t.span,
                    "split attributes into separate `#[...]` blocks",
                ),
            );
            while !self.at(TokenKind::RightBracket)
                && !self.at(TokenKind::Eof)
                && !starts_declaration(self.tok().kind)
            {
                self.next();
            }
        }
        self.close(TokenKind::RightBracket, open);
        self.add(start, Attribute { name, arguments })
    }

    fn import_decl(&mut self) -> Option<Import> {
        let start = self.next().span.start;
        let Some(path) = self.expect(TokenKind::String) else {
            self.sync();
            return None;
        };
        let alias = self.at(TokenKind::As).then(|| {
            self.next();
            self.name()
        });
        self.end();
        Some(Import {
            span: Span::new(start as usize, self.last_end as usize),
            path: path.span,
            alias,
        })
    }

    /// Reads `(receiver) name<T>(parameters) -> Result in origins` after a
    /// function's `fn`: its name and signature.
    pub(crate) fn named_signature(&mut self) -> (Ident, Signature) {
        let receiver = self.at(TokenKind::LeftParen).then(|| {
            let open = self.next();
            let receiver = self.parameter();
            self.close(TokenKind::RightParen, open);
            receiver
        });
        let name = self.name();
        let type_parameters = self.type_parameters();
        let signature =
            self.signature(receiver, type_parameters, starts_parameter, Self::parameter);
        (name, signature)
    }

    /// Reads a name into a node of its own: a type parameter or an origin.
    fn name_node(&mut self) -> Id<Name> {
        let name = self.name();
        self.tree.add(Name, name.span)
    }

    /// Reads `<T, U>`; it may be left out.
    fn type_parameters(&mut self) -> List<Name> {
        if !self.at(TokenKind::Less) {
            return List::EMPTY;
        }
        let open = self.next();
        let names = self.items(TokenKind::Greater, false, is_name, Self::name_node);
        self.close_angle(open);
        names
    }

    /// Reads `name: Type = value;` after `let` or `const`; the type and the
    /// value may be left out.
    pub(crate) fn binding(&mut self) -> (Ident, Option<Id<Type>>, Option<Id<Expr>>) {
        let name = self.name();
        let ty = self.at(TokenKind::Colon).then(|| {
            self.next();
            self.type_expr()
        });
        let value = self.at(TokenKind::Assign).then(|| {
            self.next();
            self.expr(Precedence::Lowest)
        });
        self.end();
        (name, ty, value)
    }

    /// Reads a function's parameter: `mut name: Type = default`, where `mut`,
    /// the name and the default may be left out.
    fn parameter(&mut self) -> Id<Param> {
        let start = self.tok().span.start;
        let mut keyword = None;
        if self.at(TokenKind::Mut) {
            keyword = Some(self.next());
            while self.at(TokenKind::Mut) {
                let extra = self.next();
                let mut removed = extra.span;
                let after = self.tok().span.start;
                if self.line(extra.span.end) == self.line(after) {
                    removed.end = after; // and the space after it
                }
                let fix = self.fix(removed, "");
                self.repair(
                    Diagnostic::new(diagnostics::INVALID_DECLARATION, "`mut` is repeated")
                        .label(&self.tree.file, extra.span, "")
                        .help("remove the extra `mut`", vec![fix]),
                );
            }
        }
        let name = self.parameter_name();
        let ty = self.type_expr();
        if let Some(keyword) = keyword
            && name.is_none()
        {
            let span = Span::new(keyword.span.start as usize, self.last_end as usize);
            self.repair(
                Diagnostic::new(
                    diagnostics::INVALID_DECLARATION,
                    "mutable parameter requires a named binding",
                )
                .label(
                    &self.tree.file,
                    span,
                    "add a parameter name after the modifier",
                ),
            );
        }
        let default = self.at(TokenKind::Assign).then(|| {
            self.next();
            self.expr(Precedence::Lowest)
        });
        self.add(
            start,
            Param {
                mutable: keyword.is_some(),
                name,
                ty,
                default,
            },
        )
    }

    /// Reads the `name:` in front of a parameter's type, if it is there; `_:`
    /// names no parameter. `_` is no type, so a `:` must follow it.
    pub(crate) fn parameter_name(&mut self) -> Option<Ident> {
        if self.at(TokenKind::Underscore) {
            self.next();
            self.expect(TokenKind::Colon);
            return None;
        }
        if !self.at(TokenKind::Ident) || self.ahead(1).kind != TokenKind::Colon {
            return None;
        }
        let name = self.name();
        self.next(); // `:`
        Some(name)
    }

    /// Reads `(parameters) -> Type in origins`, each parameter with
    /// `parameter`, into a signature with `receiver` and `type_parameters`
    /// read before it. The result and the origins of a returned reference,
    /// `in name` or `in (a, b)`, may be left out.
    pub(crate) fn signature(
        &mut self,
        receiver: Option<Id<Param>>,
        type_parameters: List<Name>,
        starts_parameter: fn(TokenKind) -> bool,
        parameter: impl FnMut(&mut Self) -> Id<Param>,
    ) -> Signature {
        let parameters = self.delimited(
            TokenKind::LeftParen,
            TokenKind::RightParen,
            false,
            starts_parameter,
            parameter,
        );
        let kind = self.tok().kind;
        let mut result = None;
        if kind == TokenKind::Arrow {
            self.next();
            result = Some(self.type_expr());
        } else if starts_type(kind) && !starts_declaration(kind) {
            // A type right after the parameters can only be the result; `fn`,
            // `struct`, `enum` or `iface` there starts the next declaration.
            let at = self.here();
            let fix = self.fix(at, " ->");
            self.repair(
                Diagnostic::new(
                    diagnostics::EXPECTED_TOKEN,
                    "expected `->` before the return type",
                )
                .label(&self.tree.file, at, "")
                .help("add missing `->`", vec![fix]),
            );
            result = Some(self.type_expr());
        }
        let mut origins = List::EMPTY;
        if self.at(TokenKind::In) {
            self.next();
            if self.at(TokenKind::LeftParen) {
                origins = self.delimited(
                    TokenKind::LeftParen,
                    TokenKind::RightParen,
                    false,
                    is_name,
                    Self::name_node,
                );
            } else {
                let origin = self.name_node();
                origins = self.tree.children.lists.add(&[origin.into()]);
            }
        }
        Signature {
            receiver,
            type_parameters,
            parameters,
            result,
            origins,
        }
    }
}

/// Reads `fn (receiver) name<T>(parameters) -> Result in origins` followed
/// by a body, or by `;` for a function defined elsewhere.
fn function_decl(p: &mut Parser, metadata: Metadata) -> Id<Decl> {
    let start = p.next().span.start;
    let (name, signature) = p.named_signature();
    let body = match p.tok().kind {
        TokenKind::Semicolon => {
            p.end();
            None
        }
        TokenKind::LeftBrace => Some(p.block()),
        _ => {
            let at = p.here();
            p.fail(
                Diagnostic::new(diagnostics::EXPECTED_TOKEN, "missing function body").label(
                    &p.tree.file,
                    at,
                    "expected `{` or `;` here",
                ),
            );
            None
        }
    };
    p.add(
        start,
        Decl::Function(Function {
            metadata,
            name,
            signature,
            body,
        }),
    )
}

/// Reads `const name: Type = value;`.
pub(crate) fn const_decl(p: &mut Parser, metadata: Metadata) -> Id<Decl> {
    let start = p.next().span.start;
    let (name, ty, value) = p.binding();
    p.add(
        start,
        Decl::Const {
            metadata,
            name,
            ty,
            value,
        },
    )
}

/// Reads `struct Name<T> { fields }`, `enum Name<T> { variants }`,
/// `iface Name<T> { methods }` or `type Name<T> = Type;`. A `;` after the `}`
/// is allowed.
fn type_decl(p: &mut Parser, metadata: Metadata) -> Id<Decl> {
    let keyword = p.next();
    let name = p.name();
    let type_parameters = p.type_parameters();
    let start = p.tok().span.start;
    let ty = match keyword.kind {
        TokenKind::Struct => p.struct_type(start),
        TokenKind::Enum => p.enum_type(start),
        TokenKind::Iface => p.interface_type(start),
        _ => {
            p.expect(TokenKind::Assign);
            let ty = p.type_expr();
            p.end();
            ty
        }
    };
    if keyword.kind != TokenKind::Type && p.at(TokenKind::Semicolon) {
        p.next();
    }
    let decl = TypeDecl {
        metadata,
        name,
        type_parameters,
        ty,
    };
    let node = match keyword.kind {
        TokenKind::Type => Decl::Alias(decl),
        _ => Decl::Type(decl),
    };
    p.add(keyword.span.start, node)
}

#[cfg(test)]
mod tests;
