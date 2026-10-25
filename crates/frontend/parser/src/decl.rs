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
    /// names no parameter.
    pub(crate) fn parameter_name(&mut self) -> Option<Ident> {
        let named = self.at(TokenKind::Ident);
        if !named && !self.at(TokenKind::Underscore) || self.ahead(1).kind != TokenKind::Colon {
            return None;
        }
        let name = named.then(|| self.name());
        if !named {
            self.next(); // `_`
        }
        self.next(); // `:`
        name
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
                origins = self.tree.lists.add(&[origin.into()]);
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
mod tests {
    use crate::tests::dump_of;
    use crate::tests::messages;
    use crate::tests::one_error;

    #[test]
    fn parses_signatures() {
        let text = "fn (self: &mut Counter) Add<T, U>(mut delta: i32, scale: f64 = 1, &Self) -> &T in (self, delta) {}
fn exit(code: i32);
fn Use(callback: fn(value: &Box, i32) -> &Box in value, items: []?*T, view: [..]u8, grid: [4]rawptr, cell: **u8, untyped: *rawptr) -> ?a::Box<Box<T>>;
";
        let want = "module
  function declaration
    parameter
      declared name self
      reference type mut
        type name Counter
    declared name Add
    declared name T
    declared name U
    parameter mut
      declared name delta
      type name i32
    parameter
      declared name scale
      type name f64
      number literal 1
    parameter
      reference type
        type name Self
    reference type
      type name T
    name reference self
    name reference delta
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
      declared name items
      dynamic array type
        optional type
          pointer type
            type name T
    parameter
      declared name view
      slice type
        type name u8
    parameter
      declared name grid
      fixed array type
        number literal 4
        raw pointer type rawptr
    parameter
      declared name cell
      pointer type
        pointer type
          type name u8
    parameter
      declared name untyped
      pointer type
        raw pointer type rawptr
    optional type
      path
        type name a
        generic type
          type name Box
          generic type
            type name Box
            type name T
";
        assert!(messages(text).is_empty());
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
            ("fn f(a: [n]i32) {}", "expected array length", None),
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
#[extern("rt_exit")]
fn exit(code: i32);
struct Pair<T> {
    first: T,
    second: struct { flag: bool },
};
enum Status { Ready, Waiting: Pair<i32>, }
iface Summer {
    fn (&Self) sum() -> i32,
    fn (self: &Self) add<T>(value: T)
}
type Alias<T> = ?T;
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
        string literal "rt_exit"
    declared name exit
    parameter
      declared name code
      type name i32
  struct declaration
    declared name Pair
    declared name T
    struct type
      field
        declared name first
        type name T
      field
        declared name second
        struct type
          field
            declared name flag
            type name bool
  enum declaration
    declared name Status
    enum type
      variant
        declared name Ready
      variant
        declared name Waiting
        generic type
          type name Pair
          type name i32
  interface declaration
    declared name Summer
    interface type
      function declaration
        parameter
          reference type
            type name Self
        declared name sum
        type name i32
      function declaration
        parameter
          declared name self
          reference type
            type name Self
        declared name add
        declared name T
        parameter
          declared name value
          type name T
  type alias declaration
    declared name Alias
    declared name T
    optional type
      type name T
"#;
        assert!(messages(text).is_empty());
        assert_eq!(dump_of(text), want);
    }

    /// The real prelude, the first file of every program, parses cleanly.
    #[test]
    fn parses_the_prelude() {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../../_builtin_library/core/src/global.peep"
        );
        let text = std::fs::read_to_string(path).unwrap();
        assert!(messages(&text).is_empty());
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
            (
                "iface I { fn (&Self) f(a: i32 = 1) }",
                "interface method defaults are not supported",
            ),
            ("iface I { fn f() }", "expected `(`, found `identifier`"),
            ("type A i32;", "expected `=`, found `identifier`"),
            ("struct S { a i32 }", "expected `:`, found `identifier`"),
            ("enum E { A B }", "expected `,`"),
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
