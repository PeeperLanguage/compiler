//! Writes a tree one node per line, children indented under their parent: the
//! kind, then its operator or `mut` if it has one, then the source text of a
//! node without children. This is the Go parser's dump format, so trees can be
//! compared with it. With `spans`, each line also shows the node's span.

use std::fmt::Write;

use arena::Id;
use arena::List;
use source::Span;
use tree::ast;
use tree::ast::Attribute;
use tree::ast::Block;
use tree::ast::Constraint;
use tree::ast::Decl;
use tree::ast::Expr;
use tree::ast::Ident;
use tree::ast::Import;
use tree::ast::Metadata;
use tree::ast::Param;
use tree::ast::Pattern;
use tree::ast::Signature;
use tree::ast::Stmt;
use tree::ast::StringKind;
use tree::ast::Tree;
use tree::ast::Type;

use token::TokenKind;

pub(crate) fn dump(tree: &Tree, spans: bool) -> String {
    let mut d = Dumper {
        tree,
        spans,
        out: String::new(),
    };
    let mut nested: Vec<Item> = tree.imports.iter().map(|&i| Item::Import(i)).collect();
    nested.extend(tree.declarations().map(Item::Decl));
    let file = Span::new(0, tree.file.text.len());
    d.write("module", None, file, &nested, 0);
    d.out
}

/// A node to write, by category; names and other leaves are their kind and span.
#[derive(Clone, Copy)]
enum Item {
    Import(Import),
    Metadata(Metadata),
    Attribute(Id<Attribute>),
    Decl(Id<Decl>),
    Param(Id<Param>),
    Constraint(Id<Constraint>),
    Type(Id<Type>),
    Block(Id<Block>),
    Stmt(Id<Stmt>),
    Pattern(Id<Pattern>),
    Expr(Id<Expr>),
    Field(Id<ast::Field>),
    FieldValue(Id<ast::FieldValue>),
    Variant(Id<ast::Variant>),
    MatchArm(Id<ast::MatchArm>),
    Leaf(&'static str, Span),
}

struct Dumper<'t> {
    tree: &'t Tree,
    spans: bool,
    out: String,
}

fn declared(name: Ident) -> Item {
    Item::Leaf("declared name", name.span)
}

fn mutable(on: bool) -> Option<&'static str> {
    on.then_some(TokenKind::Mut.text())
}

impl<'t> Dumper<'t> {
    /// A leaf of the kind `kind` for each name of `list`.
    fn names(&self, kind: &'static str, list: List<ast::Name>) -> impl Iterator<Item = Item> + 't {
        let tree = self.tree;
        (tree.children.lists.items(list))
            .map(move |name| Item::Leaf(kind, tree.children.names.span(name)))
    }

    fn write(
        &mut self,
        kind: &str,
        op: Option<&'static str>,
        span: Span,
        nested: &[Item],
        depth: usize,
    ) {
        for _ in 0..depth {
            self.out.push_str("  ");
        }
        self.out.push_str(kind);
        if let Some(op) = op {
            self.out.push(' ');
            self.out.push_str(op);
        }
        if self.spans {
            _ = write!(self.out, " @{}-{}", span.start, span.end);
        }
        if nested.is_empty() {
            self.out.push(' ');
            self.out.push_str(self.tree.file.slice(span));
        }
        self.out.push('\n');
        for &child in nested {
            self.item(child, depth + 1);
        }
    }

    fn metadata(&self, metadata: Metadata) -> Option<Item> {
        (metadata.doc_comment.is_some() || !metadata.attributes.is_empty())
            .then_some(Item::Metadata(metadata))
    }

    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    fn item(&mut self, item: Item, depth: usize) {
        let t = self.tree;
        let (kind, op, span, nested): (&str, Option<&'static str>, Span, Vec<Item>) = match item {
            Item::Leaf(kind, span) => (kind, None, span, vec![]),
            Item::Import(i) => (
                "import declaration",
                None,
                i.span,
                [
                    Some(Item::Leaf("string literal", i.path)),
                    i.alias.map(declared),
                ]
                .into_iter()
                .flatten()
                .collect(),
            ),
            Item::Metadata(m) => {
                let attributes: Vec<_> = t.children.lists.items(m.attributes).collect();
                let start = m
                    .doc_comment
                    .or(attributes.first().map(|&a| t.children.attributes.span(a)))
                    .unwrap_or_default()
                    .start;
                let end = attributes
                    .last()
                    .map(|&a| t.children.attributes.span(a))
                    .or(m.doc_comment)
                    .unwrap_or_default()
                    .end;
                let mut nested: Vec<_> = m
                    .doc_comment
                    .map(|s| Item::Leaf("doc comment", s))
                    .into_iter()
                    .collect();
                nested.extend(attributes.into_iter().map(Item::Attribute));
                (
                    "metadata",
                    None,
                    Span::new(start as usize, end as usize),
                    nested,
                )
            }
            Item::Attribute(id) => {
                let a = t.children.attributes[id];
                let mut nested = vec![Item::Leaf("name reference", a.name.span)];
                nested.extend(t.children.lists.items(a.arguments).map(Item::Expr));
                ("attribute", None, t.children.attributes.span(id), nested)
            }
            Item::Decl(id) => {
                let span = t.decls.span(id);
                match t.decls[id] {
                    Decl::Function(f) => {
                        let mut nested: Vec<Item> = self.metadata(f.metadata).into_iter().collect();
                        let owner = f.owner.map(|owner| Item::Leaf("owner type", owner.span));
                        nested.extend(owner);
                        nested.extend(self.signature(Some(f.name), f.signature));
                        nested.extend(f.body.map(Item::Block));
                        ("function declaration", None, span, nested)
                    }
                    Decl::Const {
                        metadata,
                        name,
                        ty,
                        value,
                    } => {
                        let mut nested: Vec<Item> = self.metadata(metadata).into_iter().collect();
                        nested.push(declared(name));
                        nested.extend(ty.map(Item::Type));
                        nested.extend(value.map(Item::Expr));
                        ("const declaration", None, span, nested)
                    }
                    Decl::Type(d) | Decl::Alias(d) => {
                        let kind = if let Decl::Alias(_) = t.decls[id] {
                            "type alias declaration"
                        } else {
                            "type declaration"
                        };
                        let mut nested: Vec<Item> = self.metadata(d.metadata).into_iter().collect();
                        nested.push(declared(d.name));
                        nested.extend(self.names("declared name", d.type_parameters));
                        nested.push(Item::Type(d.ty));
                        (kind, None, span, nested)
                    }
                }
            }
            Item::Constraint(id) => {
                let c = t.children.constraints[id];
                let mut nested = vec![Item::Expr(c.subject), Item::Expr(c.bound)];
                nested.extend(t.children.lists.items(c.arguments).map(Item::Expr));
                ("constraint", None, t.children.constraints.span(id), nested)
            }
            Item::Param(id) => {
                let p = t.children.params[id];
                let mut nested: Vec<Item> = p.name.map(declared).into_iter().collect();
                nested.push(Item::Type(p.ty));
                nested.extend(p.default.map(Item::Expr));
                (
                    if let Type::Variadic { .. } = t.types[p.ty] {
                        "variadic parameter"
                    } else if p.constant {
                        "const parameter"
                    } else {
                        "parameter"
                    },
                    mutable(p.mutable),
                    t.children.params.span(id),
                    nested,
                )
            }
            Item::Type(id) => self.type_node(id),
            Item::Field(id) => {
                let field = t.children.fields[id];
                let nested = vec![declared(field.name), Item::Type(field.ty)];
                ("field", None, t.children.fields.span(id), nested)
            }
            Item::FieldValue(id) => {
                let field = t.children.field_values[id];
                let nested = vec![
                    Item::Leaf("field name", field.name.span),
                    Item::Expr(field.value),
                ];
                (
                    "field value",
                    None,
                    t.children.field_values.span(id),
                    nested,
                )
            }
            Item::Variant(id) => {
                let variant = t.children.variants[id];
                let mut nested = vec![declared(variant.name)];
                nested.extend(variant.payload.map(Item::Type));
                ("variant", None, t.children.variants.span(id), nested)
            }
            Item::MatchArm(id) => {
                let arm = t.children.match_arms[id];
                let nested = vec![Item::Pattern(arm.pattern), Item::Block(arm.body)];
                ("match arm", None, t.children.match_arms.span(id), nested)
            }
            Item::Block(id) => (
                "block",
                None,
                t.blocks.span(id),
                t.children
                    .lists
                    .items(t.blocks[id].statements)
                    .map(Item::Stmt)
                    .collect(),
            ),
            // A block or a `const` statement is written as the block or the
            // declaration it holds; every other statement by `stmt`.
            Item::Stmt(id) => {
                if let Stmt::Block(block) = t.stmts[id] {
                    return self.item(Item::Block(block), depth);
                }
                if let Stmt::Const(decl) = t.stmts[id] {
                    return self.item(Item::Decl(decl), depth);
                }
                self.stmt(id)
            }
            Item::Pattern(id) => self.pattern(id),
            // A path is written as the type it names; every other expression
            // by `expr`.
            Item::Expr(id) => {
                if let Expr::Path(ty) = t.exprs[id] {
                    return self.item(Item::Type(ty), depth);
                }
                self.expr(id)
            }
        };
        self.write(kind, op, span, &nested, depth);
    }

    /// A signature's parts, with the function's `name`, if any, in source
    /// order.
    fn signature(&self, name: Option<Ident>, signature: Signature) -> Vec<Item> {
        let mut nested: Vec<Item> = name.map(declared).into_iter().collect();
        nested.extend(self.names("declared name", signature.type_parameters));
        let parameters = self.tree.children.lists.items(signature.parameters);
        nested.extend(parameters.map(Item::Param));
        nested.extend(signature.result.map(Item::Type));
        nested.extend(self.names("name reference", signature.origins));
        let constraints = self.tree.children.lists.items(signature.constraints);
        nested.extend(constraints.map(Item::Constraint));
        nested
    }

    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    fn type_node(&self, id: Id<Type>) -> (&'static str, Option<&'static str>, Span, Vec<Item>) {
        let t = self.tree;
        let span = t.types.span(id);
        let one = |ty: Id<Type>| vec![Item::Type(ty)];
        let (kind, op, nested) = match t.types[id] {
            Type::Array { length, element } => (
                "array type",
                None,
                vec![Item::Leaf("array length", length), Item::Type(element)],
            ),
            Type::Enum { variants } => (
                "enum type",
                None,
                t.children
                    .lists
                    .items(variants)
                    .map(Item::Variant)
                    .collect(),
            ),
            Type::Interface { methods } => (
                "interface type",
                None,
                t.children.lists.items(methods).map(Item::Decl).collect(),
            ),
            Type::Optional(inner) => ("optional type", None, one(inner)),
            Type::Pointer { target } => (
                "pointer type",
                None,
                target.map(Item::Type).into_iter().collect(),
            ),
            Type::Struct { fields } => (
                "struct type",
                None,
                t.children.lists.items(fields).map(Item::Field).collect(),
            ),
            Type::Name => ("type name", None, vec![]),
            Type::Path(segments) => (
                "path",
                None,
                t.children.lists.items(segments).map(Item::Type).collect(),
            ),
            Type::Generic { name, arguments } => {
                let mut nested = vec![Item::Leaf("type name", name.span)];
                nested.extend(t.children.lists.items(arguments).map(Item::Type));
                ("generic type", None, nested)
            }
            Type::Bracketed { name, inner } => (
                "bracketed type",
                None,
                vec![Item::Leaf("type name", name.span), Item::Type(inner)],
            ),
            Type::Reference { mutable: m, target } => ("reference type", mutable(m), one(target)),
            Type::Owner => ("self type", None, vec![]),
            Type::Type => ("type of a type", None, vec![]),
            Type::Variadic {
                mutable: m,
                element,
            } => ("variadic type", mutable(m), one(element)),
            Type::Slice {
                mutable: m,
                element,
            } => ("slice type", mutable(m), one(element)),
            Type::Function(signature) => ("function type", None, self.signature(None, signature)),
            Type::Invalid => ("invalid type", None, vec![]),
        };
        (kind, op, span, nested)
    }

    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    fn stmt(&self, id: Id<Stmt>) -> (&'static str, Option<&'static str>, Span, Vec<Item>) {
        let t = self.tree;
        let (kind, op, nested) = match t.stmts[id] {
            Stmt::Let {
                mutable: m,
                name,
                ty,
                value,
            } => {
                let mut nested = vec![declared(name)];
                nested.extend(ty.map(Item::Type));
                nested.extend(value.map(Item::Expr));
                ("let statement", mutable(m), nested)
            }
            Stmt::Return(value) => (
                "return statement",
                None,
                value.map(Item::Expr).into_iter().collect(),
            ),
            Stmt::Panic(message) => ("panic statement", None, vec![Item::Expr(message)]),
            Stmt::Match { subject, arms } => {
                let mut nested = vec![Item::Expr(subject)];
                nested.extend(t.children.lists.items(arms).map(Item::MatchArm));
                ("match statement", None, nested)
            }
            Stmt::Expression(e) => ("expression statement", None, vec![Item::Expr(e)]),
            Stmt::Assign { target, value } => (
                "assign statement",
                None,
                vec![Item::Expr(target), Item::Expr(value)],
            ),
            Stmt::Destructure { pattern, value } => (
                "destructure statement",
                None,
                vec![Item::Pattern(pattern), Item::Expr(value)],
            ),
            Stmt::If {
                condition,
                then,
                otherwise,
            } => {
                let mut nested = vec![Item::Expr(condition)];
                nested.extend(then.map(Item::Block));
                nested.extend(otherwise.map(Item::Stmt));
                ("if statement", None, nested)
            }
            Stmt::Loop { control, body } => {
                let mut nested: Vec<Item> = control.map(Item::Expr).into_iter().collect();
                nested.extend(body.map(Item::Block));
                ("loop statement", None, nested)
            }
            Stmt::ForIn {
                index,
                value,
                iterable,
                body,
            } => {
                let mut nested: Vec<Item> = index.map(declared).into_iter().collect();
                nested.push(declared(value));
                nested.push(Item::Expr(iterable));
                nested.extend(body.map(Item::Block));
                ("for in statement", None, nested)
            }
            Stmt::Break => ("break statement", None, vec![]),
            Stmt::Continue => ("continue statement", None, vec![]),
            Stmt::Block(_) | Stmt::Const(_) => {
                unreachable!("written as their block or declaration")
            }
        };
        (kind, op, t.stmts.span(id), nested)
    }

    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    fn pattern(&self, id: Id<Pattern>) -> (&'static str, Option<&'static str>, Span, Vec<Item>) {
        let t = self.tree;
        let (kind, nested) = match t.patterns[id] {
            Pattern::Wildcard => ("wildcard pattern", vec![]),
            Pattern::None => ("none pattern", vec![]),
            Pattern::Case { case, inner } => {
                let mut nested = vec![Item::Type(case)];
                nested.extend(inner.map(Item::Pattern));
                ("case pattern", nested)
            }
            Pattern::Binding => ("declared name", vec![]),
            Pattern::Struct(fields) => (
                "struct pattern",
                t.children.lists.items(fields).map(Item::Pattern).collect(),
            ),
            Pattern::Field { field, binding } => {
                let mut nested = vec![Item::Leaf("field name", field.span)];
                nested.extend(binding.map(declared));
                ("field pattern", nested)
            }
            Pattern::Rest => ("rest pattern", vec![]),
            Pattern::Invalid => ("invalid expression", vec![]),
        };
        (kind, None, t.patterns.span(id), nested)
    }

    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    fn expr(&self, id: Id<Expr>) -> (&'static str, Option<&'static str>, Span, Vec<Item>) {
        let t = self.tree;
        let one = |e: Id<Expr>| vec![Item::Expr(e)];
        let two = |a: Id<Expr>, b: Item| vec![Item::Expr(a), b];
        let (kind, op, nested) = match t.exprs[id] {
            Expr::ArrayLiteral { ty, entries } => {
                let (counted, ty) = match ty {
                    ast::ArrayLiteralType::Written(ty) => (None, ty),
                    ast::ArrayLiteralType::Counted { element } => (Some("_"), element),
                };
                let mut nested = vec![Item::Type(ty)];
                nested.extend(t.children.lists.items(entries).map(Item::Expr));
                ("array literal", counted, nested)
            }
            Expr::CaseTest { value, case } => ("case test", None, two(value, Item::Type(case))),
            Expr::Char { .. } => ("char literal", None, vec![]),
            Expr::OrElse { value, fallback } => {
                let fallback = match fallback {
                    ast::OrElseFallback::Value(fallback) => Item::Expr(fallback),
                    ast::OrElseFallback::Block(fallback) => Item::Block(fallback),
                };
                ("orelse expression", None, two(value, fallback))
            }
            Expr::Pipe { value, target } => {
                ("pipe expression", None, two(value, Item::Expr(target)))
            }
            Expr::Print { newline, values } => (
                "print expression",
                newline.then_some("ln"),
                t.children.lists.items(values).map(Item::Expr).collect(),
            ),
            Expr::Range(written) => {
                let parts = [written.start, written.end, written.step];
                (
                    "range expression",
                    written.inclusive.then_some("..="),
                    parts.into_iter().flatten().map(Item::Expr).collect(),
                )
            }
            Expr::RawAddress(place) => ("address expression", None, one(place)),
            Expr::StructLiteral { ty, fields } => {
                let mut nested: Vec<Item> = ty.map(Item::Type).into_iter().collect();
                nested.extend(t.children.lists.items(fields).map(Item::FieldValue));
                ("struct literal", None, nested)
            }
            Expr::VariantLiteral { case, payload } => (
                "variant literal",
                None,
                vec![Item::Type(case), Item::Expr(payload)],
            ),
            Expr::Name => ("name reference", None, vec![]),
            Expr::Number => ("number literal", None, vec![]),
            Expr::None => ("none literal", None, vec![]),
            Expr::Bool(_) => ("bool literal", None, vec![]),
            Expr::String(kind) => (
                "string literal",
                (kind == StringKind::C).then_some(TokenKind::CString.text()),
                vec![],
            ),
            Expr::Cast { value, ty } => ("cast expression", None, two(value, Item::Type(ty))),
            Expr::Binary { op, left, right } => (
                "binary expression",
                Some(op.symbol()),
                two(left, Item::Expr(right)),
            ),
            Expr::Unary { op, value } => ("unary expression", Some(op.symbol()), one(value)),
            Expr::Reference { mutable: m, value } => {
                ("reference expression", mutable(m), one(value))
            }
            Expr::Call {
                callee,
                type_arguments,
                arguments,
            } => {
                let mut nested = one(callee);
                nested.extend(t.children.lists.items(type_arguments).map(Item::Type));
                nested.extend(t.children.lists.items(arguments).map(Item::Expr));
                ("call expression", None, nested)
            }
            Expr::Selector { value, field } => (
                "selector expression",
                None,
                two(value, Item::Leaf("field name", field.span)),
            ),
            Expr::Dereference(value) => ("dereference expression", None, one(value)),
            Expr::Index { value, index } => {
                ("index expression", None, two(value, Item::Expr(index)))
            }
            Expr::Spread(values) => ("spread", None, one(values)),
            Expr::Parentheses(inner) => ("parentheses", None, one(inner)),
            Expr::Type(ty) => ("type expression", None, vec![Item::Type(ty)]),
            Expr::Invalid => ("invalid expression", None, vec![]),
            Expr::Path(_) => unreachable!("written as its type"),
        };
        (kind, op, t.exprs.span(id), nested)
    }
}
