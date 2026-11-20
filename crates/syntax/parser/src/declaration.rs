//! The module and its declarations: imports, functions, constants and type
//! declarations, with their doc comments and attributes.

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use source::Span;
use token::TokenKind;
use tree::ast::Attribute;
use tree::ast::Constraint;
use tree::ast::Decl;
use tree::ast::Expr;
use tree::ast::Function;
use tree::ast::Ident;
use tree::ast::Import;
use tree::ast::Metadata;
use tree::ast::Name;
use tree::ast::Param;
use tree::ast::Signature;
use tree::ast::Type;
use tree::ast::TypeDecl;
use tree::operators::Precedence;

use crate::Parser;
use crate::RULES;
use crate::Rules;
use crate::expression::name_or_path;
use crate::expression::starts_expression;
use crate::is_name;
use crate::types::starts_type;

/// Reads the declaration the current keyword begins, with its doc comment and
/// attributes.
pub(crate) type DeclarationRule = fn(&mut Parser, Metadata) -> Id<Decl>;

/// The reader of each declaration a keyword begins, indexed by token kind;
/// each takes the declaration's doc comment and attributes.
pub(crate) const fn declaration_rows(rules: &mut Rules) {
    rules.declaration(TokenKind::Fn, function_decl);
    rules.declaration(TokenKind::Const, const_decl);
    rules.declaration(TokenKind::Type, type_decl);
}

/// Whether `kind` begins a declaration or an import.
pub(crate) fn starts_declaration(kind: TokenKind) -> bool {
    RULES.declarations[kind as usize].is_some() || kind == TokenKind::Import
}

fn starts_parameter(kind: TokenKind) -> bool {
    matches!(
        kind,
        TokenKind::Const | TokenKind::Mut | TokenKind::Underscore | TokenKind::SelfValue
    ) || starts_type(kind)
}

impl Parser {
    pub(crate) fn module(&mut self) {
        let mut imports = Vec::new();
        let mark = self.list_items.len();
        loop {
            let metadata = self.metadata();
            let kind = self.tok().kind;
            if starts_declaration(kind) {
                self.recovering = false;
            }
            let first = self.tree.children.lists.items(metadata.attributes).next();
            if RULES.declarations[kind as usize].is_none()
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
                kind if let Some(read) = RULES.declarations[kind as usize] => {
                    let decl = read(self, metadata);
                    self.list_items.push(decl.into());
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
        let mark = self.list_items.len();
        while self.at(TokenKind::Hash) {
            let attribute = self.attribute();
            self.list_items.push(attribute.into());
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

    /// Reads `name<T>(parameters) -> Result in origins where T: Bound`
    /// after a function's `fn` and owner: its name and signature.
    pub(crate) fn named_signature(&mut self) -> (Ident, Signature) {
        let name = self.name();
        let type_parameters = self.type_parameters();
        let mut signature = self.signature(type_parameters, starts_parameter, Self::parameter);
        if self.at(TokenKind::Where) {
            self.next();
            let mark = self.list_items.len();
            loop {
                let constraint = self.constraint();
                self.list_items.push(constraint.into());
                // Only a constraint continues it: an interface's `,` after
                // its last one separates its methods.
                if !(self.at(TokenKind::Comma) && self.ahead(1).kind == TokenKind::Ident) {
                    break;
                }
                self.next();
            }
            signature.constraints = self.finish_list(mark);
        }
        (name, signature)
    }

    /// Reads `T: Bound` or `T: Bound(arguments)`, a constraint after `where`.
    fn constraint(&mut self) -> Id<Constraint> {
        let start = self.tok().span.start;
        let subject = self.name();
        let subject = self.tree.add(Expr::Name, subject.span);
        self.expect(TokenKind::Colon);
        let bound = if self.at(TokenKind::Ident) {
            name_or_path(self)
        } else {
            let missing = self.name(); // reported
            self.tree.add(Expr::Name, missing.span)
        };
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
        let constraint = Constraint {
            subject,
            bound,
            arguments,
        };
        let span = Span::new(start as usize, self.last_end as usize);
        self.tree.children.constraints.push(constraint, span)
    }

    /// Reads a name into a node of its own: a type parameter or an origin.
    fn name_node(&mut self) -> Id<Name> {
        let name = self.name();
        self.tree.children.names.push(Name, name.span)
    }

    /// Reads what a returned reference may point into: a parameter's name, or
    /// `self`.
    fn origin(&mut self) -> Id<Name> {
        if self.at(TokenKind::SelfValue) {
            let span = self.next().span;
            return self.tree.children.names.push(Name, span);
        }
        self.name_node()
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
    /// the name and the default may be left out, and the type may be `...T`
    /// or `...mut T`; `const name: type`, which takes a type; or `self`,
    /// `mut self`, `&self` or `&mut self`, the parameter `self` of the type
    /// the function belongs to.
    fn parameter(&mut self) -> Id<Param> {
        let start = self.tok().span.start;
        let reference = self.at(TokenKind::Ampersand);
        let mutable = self.ahead(usize::from(reference)).kind == TokenKind::Mut;
        let before = usize::from(reference) + usize::from(mutable);
        let word = self.ahead(before);
        if word.kind == TokenKind::SelfValue {
            for _ in 0..before {
                self.next();
            }
            let name = Ident {
                span: self.next().span,
            };
            let owner = self.add(word.span.start, Type::Owner);
            let ty = if reference {
                self.add(
                    start,
                    Type::Reference {
                        mutable,
                        target: owner,
                    },
                )
            } else {
                owner
            };
            return self.add(
                start,
                Param {
                    constant: false,
                    mutable: mutable && !reference,
                    name: Some(name),
                    ty,
                    default: None,
                },
            );
        }
        let constant = self.at(TokenKind::Const);
        if constant {
            self.next();
        }
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
        let ty = if self.at(TokenKind::Ellipsis) {
            let start = self.next().span.start;
            let mutable = self.at(TokenKind::Mut);
            if mutable {
                self.next();
            }
            let element = self.type_expr();
            self.add(start, Type::Variadic { mutable, element })
        } else {
            self.type_expr()
        };
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
                constant,
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
    /// `parameter`, into a signature with the `type_parameters` read before
    /// it and no constraints. The result and the origins of a returned
    /// reference, `in name` or `in (a, b)`, may be left out.
    pub(crate) fn signature(
        &mut self,
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
                    |kind| is_name(kind) || kind == TokenKind::SelfValue,
                    Self::origin,
                );
            } else {
                let origin = self.origin();
                origins = self.tree.children.lists.add(&[origin.into()]);
            }
        }
        Signature {
            type_parameters,
            parameters,
            result,
            origins,
            constraints: List::EMPTY,
        }
    }
}

/// Reads `fn name<T>(parameters) -> Result in origins where T: Bound`, with `Owner::`
/// before the name for a function of the type `Owner`, followed by a body,
/// or by `;` for a function defined elsewhere.
fn function_decl(p: &mut Parser, metadata: Metadata) -> Id<Decl> {
    let start = p.next().span.start;
    let owner = (p.at(TokenKind::Ident) && p.ahead(1).kind == TokenKind::ColonColon).then(|| {
        let owner = p.name();
        p.next();
        owner
    });
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
            owner,
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

/// Reads `type Name<T> = Type;`, or a keyword, a name and the rest of the
/// type the keyword begins: `enum Name<T> { variants }`. A `;` after the
/// `}` is allowed.
pub(crate) fn type_decl(p: &mut Parser, metadata: Metadata) -> Id<Decl> {
    let keyword = p.next();
    let name = p.name();
    let type_parameters = p.type_parameters();
    let start = p.tok().span.start;
    let ty = if let Some(read) = RULES.type_starts[keyword.kind as usize] {
        read(p, start)
    } else {
        p.expect(TokenKind::Assign);
        let ty = p.type_expr();
        p.end();
        ty
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
mod tests {
    use crate::testing;
    use crate::tests::dump_of;
    use crate::tests::one_error;

    #[test]
    fn parses_variadic_parameters() {
        let text = "fn f(first: i32, rest: ...mut i32);\nfn g(_: ...i32);\n";
        let want = "module
  function declaration
    declared name f
    parameter
      declared name first
      type name i32
    variadic parameter
      declared name rest
      variadic type mut
        type name i32
  function declaration
    declared name g
    variadic parameter
      variadic type
        type name i32
";
        assert!(testing::messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    /// `const T: type` takes a type; `type` is a type only there, which the
    /// checker decides, so the parser reads it in any parameter.
    #[test]
    fn parses_type_parameters() {
        let text = "fn f(x: i32, const T: type, t: type);\n";
        let want = "module
  function declaration
    declared name f
    parameter
      declared name x
      type name i32
    const parameter
      declared name T
      type of a type type
    parameter
      declared name t
      type of a type type
";
        assert!(testing::messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    /// The lexer reads `&&` as one token; in a type it is two references.
    #[test]
    fn parses_two_references_written_as_one_token() {
        let text = "fn f(a: &&i32, b: &&mut []u8);\n";
        let want = "module
  function declaration
    declared name f
    parameter
      declared name a
      reference type
        reference type
          type name i32
    parameter
      declared name b
      reference type
        slice type mut
          type name u8
";
        assert!(testing::messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    #[test]
    fn parses_signatures() {
        let text = "fn Counter::Add<T, U>(&mut self, mut delta: i32, scale: f64 = 1, &Counter) -> &T in (self, delta) where T: Shape, U: Sized(8) {}
fn Counter::Get(&self) {}
fn Counter::Into(self) {}
fn Counter::Doubled(mut self) {}
fn exit(code: i32);
fn Use(callback: fn(value: &Box, i32) -> &Box in value, view: &[]u8, edit: &mut []u8) -> a::Box<Box<T>>;
";
        let want = "module
  function declaration
    owner type Counter
    declared name Add
    declared name T
    declared name U
    parameter
      declared name self
      reference type mut
        self type self
    parameter mut
      declared name delta
      type name i32
    parameter
      declared name scale
      type name f64
      number literal 1
    parameter
      reference type
        type name Counter
    reference type
      type name T
    name reference self
    name reference delta
    constraint
      name reference T
      name reference Shape
    constraint
      name reference U
      name reference Sized
      number literal 8
    block {}
  function declaration
    owner type Counter
    declared name Get
    parameter
      declared name self
      reference type
        self type self
    block {}
  function declaration
    owner type Counter
    declared name Into
    parameter
      declared name self
      self type self
    block {}
  function declaration
    owner type Counter
    declared name Doubled
    parameter mut
      declared name self
      self type self
    block {}
  function declaration
    declared name exit
    parameter
      declared name code
      type name i32
  function declaration
    declared name Use
    parameter
      declared name callback
      function type
        parameter
          declared name value
          reference type
            type name Box
        parameter
          type name i32
        reference type
          type name Box
        name reference value
    parameter
      declared name view
      slice type
        type name u8
    parameter
      declared name edit
      slice type mut
        type name u8
    path
      type name a
      generic type
        type name Box
        generic type
          type name Box
          type name T
";
        assert!(testing::messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    #[test]
    fn signature_errors() {
        for (text, message, fixed) in [
            (
                "fn f() i32 {}",
                "expected `->` before the return type",
                Some("fn f() -> i32 {}"),
            ),
            (
                "fn f(mut mut a: i32) {}",
                "`mut` is repeated",
                Some("fn f(mut a: i32) {}"),
            ),
            (
                "fn f(mut i32) {}",
                "mutable parameter requires a named binding",
                None,
            ),
            ("fn f(a: ) {}", "expected type, found `)`", None),
            ("fn f() -> i32", "missing function body", None),
            ("fn f<T U>() {}", "expected `,`", Some("fn f<T, U>() {}")),
        ] {
            one_error(text, message, fixed);
        }
    }

    #[test]
    fn parses_declarations() {
        let text = r#"/// The answer.
/// Twice checked.
const Answer: i32 = 42;
const Untyped = 1;
#[extern(rt_exit)]
fn exit(code: i32);
type Alias<T> = T;
fn Pair::new() {}
"#;
        let want = r#"module
  const declaration
    metadata
      doc comment /// The answer.
/// Twice checked.
    declared name Answer
    type name i32
    number literal 42
  const declaration
    declared name Untyped
    number literal 1
  function declaration
    metadata
      attribute
        name reference extern
        name reference rt_exit
    declared name exit
    parameter
      declared name code
      type name i32
  type alias declaration
    declared name Alias
    declared name T
    type name T
  function declaration
    owner type Pair
    declared name new
    block {}
"#;
        assert!(testing::messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    #[test]
    fn declaration_errors() {
        for (text, message) in [
            ("let a = 1;", "top-level `let` not allowed"),
            ("a + 1;", "module scope expects declaration"),
            (
                "#[a, b] fn f();",
                "only one attribute is allowed per `#[...]` block",
            ),
            ("fn (p: Point) f() {}", "expected `identifier`, found `(`"),
            ("type A i32;", "expected `=`, found `identifier`"),
        ] {
            one_error(text, message, None);
        }
    }

    /// A `///` on the line of earlier code is not the next declaration's
    /// documentation.
    #[test]
    fn doc_comment_after_code() {
        assert!(!dump_of("const a = 1; /// about a\nconst b = 2;").contains("metadata"));
    }
}
