//! Writes a tree one node per line, children indented under their parent: the
//! kind, then its operator or `mut` if it has one, then the source text of a
//! node without children. This is the Go parser's dump format, so trees can be
//! compared with it. With `spans`, each line also shows the node's span.

use arena::Id;
use arena::List;
use model::ast;
use model::ast::Attribute;
use model::ast::Block;
use model::ast::Decl;
use model::ast::Expr;
use model::ast::Field;
use model::ast::FieldValue;
use model::ast::Ident;
use model::ast::Import;
use model::ast::Length;
use model::ast::MatchArm;
use model::ast::Metadata;
use model::ast::Param;
use model::ast::Pattern;
use model::ast::Signature;
use model::ast::Stmt;
use model::ast::Tree;
use model::ast::Type;
use model::ast::Variant;
use source::Span;
use token::TokenKind;

pub(crate) fn dump(tree: &Tree, spans: bool) -> String {
    let mut d = Dumper {
        tree,
        spans,
        out: String::new(),
    };
    let mut children: Vec<Item> = tree.imports.iter().map(|&i| Item::Import(i)).collect();
    children.extend(tree.lists.items(tree.top_level).map(Item::Decl));
    let file = Span::new(0, tree.file.text.len());
    d.write("module", None, file, &children, 0);
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
    Type(Id<Type>),
    Field(Id<Field>),
    Variant(Id<Variant>),
    Block(Id<Block>),
    Stmt(Id<Stmt>),
    MatchArm(Id<MatchArm>),
    Pattern(Id<Pattern>),
    Expr(Id<Expr>),
    FieldValue(Id<FieldValue>),
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

impl Dumper<'_> {
    fn write(
        &mut self,
        kind: &str,
        op: Option<&'static str>,
        span: Span,
        children: &[Item],
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
            self.out.push_str(&format!(" @{}-{}", span.start, span.end));
        }
        if children.is_empty() {
            self.out.push(' ');
            self.out.push_str(self.tree.file.slice(span));
        }
        self.out.push('\n');
        for &child in children {
            self.item(child, depth + 1);
        }
    }

    fn metadata(&self, metadata: Metadata) -> Option<Item> {
        (metadata.doc_comment.is_some() || !metadata.attributes.is_empty())
            .then_some(Item::Metadata(metadata))
    }

    fn item(&mut self, item: Item, depth: usize) {
        let t = self.tree;
        let names = |kind: &'static str, list: List<ast::Name>| {
            t.lists
                .items(list)
                .map(move |name| Item::Leaf(kind, t.names.span(name)))
        };
        let (kind, op, span, children): (&str, Option<&'static str>, Span, Vec<Item>) = match item {
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
                let attributes: Vec<_> = t.lists.items(m.attributes).collect();
                let start = m
                    .doc_comment
                    .or(attributes.first().map(|&a| t.attributes.span(a)))
                    .unwrap_or_default()
                    .start;
                let end = attributes
                    .last()
                    .map(|&a| t.attributes.span(a))
                    .or(m.doc_comment)
                    .unwrap_or_default()
                    .end;
                let mut children: Vec<_> = m
                    .doc_comment
                    .map(|s| Item::Leaf("doc comment", s))
                    .into_iter()
                    .collect();
                children.extend(attributes.into_iter().map(Item::Attribute));
                (
                    "metadata",
                    None,
                    Span::new(start as usize, end as usize),
                    children,
                )
            }
            Item::Attribute(id) => {
                let a = t.attributes[id];
                let mut children = vec![Item::Leaf("name reference", a.name.span)];
                children.extend(t.lists.items(a.arguments).map(Item::Expr));
                ("attribute", None, t.attributes.span(id), children)
            }
            Item::Decl(id) => {
                let span = t.decls.span(id);
                match t.decls[id] {
                    Decl::Function(f) => {
                        let mut children: Vec<Item> =
                            self.metadata(f.metadata).into_iter().collect();
                        children.extend(self.signature(Some(f.name), f.signature));
                        children.extend(f.body.map(Item::Block));
                        ("function declaration", None, span, children)
                    }
                    Decl::Const {
                        metadata,
                        name,
                        ty,
                        value,
                    } => {
                        let mut children: Vec<Item> = self.metadata(metadata).into_iter().collect();
                        children.push(declared(name));
                        children.extend(ty.map(Item::Type));
                        children.extend(value.map(Item::Expr));
                        ("const declaration", None, span, children)
                    }
                    Decl::Type(d) | Decl::Alias(d) => {
                        let kind = match (t.decls[id], t.types[d.ty]) {
                            (Decl::Alias(_), _) => "type alias declaration",
                            (_, Type::Struct(_)) => "struct declaration",
                            (_, Type::Enum(_)) => "enum declaration",
                            (_, Type::Interface(_)) => "interface declaration",
                            _ => {
                                unreachable!("the parser gives a type declaration only these types")
                            }
                        };
                        let mut children: Vec<Item> =
                            self.metadata(d.metadata).into_iter().collect();
                        children.push(declared(d.name));
                        children.extend(names("declared name", d.type_parameters));
                        children.push(Item::Type(d.ty));
                        (kind, None, span, children)
                    }
                }
            }
            Item::Param(id) => {
                let p = t.params[id];
                let mut children: Vec<Item> = p.name.map(declared).into_iter().collect();
                children.push(Item::Type(p.ty));
                children.extend(p.default.map(Item::Expr));
                ("parameter", mutable(p.mutable), t.params.span(id), children)
            }
            Item::Field(id) => {
                let f = t.fields[id];
                (
                    "field",
                    None,
                    t.fields.span(id),
                    vec![declared(f.name), Item::Type(f.ty)],
                )
            }
            Item::Variant(id) => {
                let v = t.variants[id];
                let mut children = vec![declared(v.name)];
                children.extend(v.payload.map(Item::Type));
                ("variant", None, t.variants.span(id), children)
            }
            Item::Type(id) => self.type_node(id),
            Item::Block(id) => (
                "block",
                None,
                t.blocks.span(id),
                t.lists
                    .items(t.blocks[id].statements)
                    .map(Item::Stmt)
                    .collect(),
            ),
            Item::Stmt(id) => match t.stmts[id] {
                Stmt::Block(block) => return self.item(Item::Block(block), depth),
                Stmt::Const(decl) => return self.item(Item::Decl(decl), depth),
                _ => self.stmt(id),
            },
            Item::MatchArm(id) => {
                let arm = t.match_arms[id];
                (
                    "match arm",
                    None,
                    t.match_arms.span(id),
                    vec![Item::Pattern(arm.pattern), Item::Block(arm.body)],
                )
            }
            Item::Pattern(id) => self.pattern(id),
            Item::Expr(id) => match t.exprs[id] {
                Expr::Path(ty) => return self.item(Item::Type(ty), depth),
                Expr::Block(block) => return self.item(Item::Block(block), depth),
                _ => self.expr(id),
            },
            Item::FieldValue(id) => {
                let f = t.field_values[id];
                (
                    "field value",
                    None,
                    t.field_values.span(id),
                    vec![Item::Leaf("field name", f.name.span), Item::Expr(f.value)],
                )
            }
        };
        self.write(kind, op, span, &children, depth);
    }

    /// A signature's parts, with the function's `name`, if any, in source
    /// order after the receiver.
    fn signature(&self, name: Option<Ident>, signature: Signature) -> Vec<Item> {
        let t = self.tree;
        let names = |kind: &'static str, list: List<ast::Name>| {
            t.lists
                .items(list)
                .map(move |name| Item::Leaf(kind, t.names.span(name)))
        };
        let mut children: Vec<Item> = signature.receiver.map(Item::Param).into_iter().collect();
        children.extend(name.map(declared));
        children.extend(names("declared name", signature.type_parameters));
        children.extend(t.lists.items(signature.parameters).map(Item::Param));
        children.extend(signature.result.map(Item::Type));
        children.extend(names("name reference", signature.origins));
        children
    }

    fn type_node(&self, id: Id<Type>) -> (&'static str, Option<&'static str>, Span, Vec<Item>) {
        let t = self.tree;
        let span = t.types.span(id);
        let one = |ty: Id<Type>| vec![Item::Type(ty)];
        let (kind, op, children) = match t.types[id] {
            Type::Name => ("type name", None, vec![]),
            Type::Path(segments) => (
                "path",
                None,
                t.lists.items(segments).map(Item::Type).collect(),
            ),
            Type::Generic { name, arguments } => {
                let mut children = vec![Item::Leaf("type name", name.span)];
                children.extend(t.lists.items(arguments).map(Item::Type));
                ("generic type", None, children)
            }
            Type::Reference { mutable: m, target } => ("reference type", mutable(m), one(target)),
            Type::OwnedPointer(target) => ("owned pointer type", None, one(target)),
            Type::Optional(inner) => ("optional type", None, one(inner)),
            Type::RawPointer => ("raw pointer type", None, vec![]),
            Type::FixedArray { length, element } => {
                let length = match length {
                    Length::Number(span) => Item::Leaf("number literal", span),
                    Length::Inferred(span) => Item::Leaf("inferred length", span),
                };
                ("fixed array type", None, vec![length, Item::Type(element)])
            }
            Type::DynamicArray(element) => ("dynamic array type", None, one(element)),
            Type::Slice(element) => ("slice type", None, one(element)),
            Type::Function(signature) => ("function type", None, self.signature(None, signature)),
            Type::Struct(fields) => (
                "struct type",
                None,
                t.lists.items(fields).map(Item::Field).collect(),
            ),
            Type::Enum(variants) => (
                "enum type",
                None,
                t.lists.items(variants).map(Item::Variant).collect(),
            ),
            Type::Interface(methods) => (
                "interface type",
                None,
                t.lists.items(methods).map(Item::Decl).collect(),
            ),
            Type::Invalid => ("invalid type", None, vec![]),
        };
        (kind, op, span, children)
    }

    fn stmt(&self, id: Id<Stmt>) -> (&'static str, Option<&'static str>, Span, Vec<Item>) {
        let t = self.tree;
        let (kind, op, children) = match t.stmts[id] {
            Stmt::Let {
                mutable: m,
                name,
                ty,
                value,
            } => {
                let mut children = vec![declared(name)];
                children.extend(ty.map(Item::Type));
                children.extend(value.map(Item::Expr));
                ("let statement", mutable(m), children)
            }
            Stmt::Return(value) => (
                "return statement",
                None,
                value.map(Item::Expr).into_iter().collect(),
            ),
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
                let mut children = vec![Item::Expr(condition)];
                children.extend(then.map(Item::Block));
                children.extend(otherwise.map(Item::Stmt));
                ("if statement", None, children)
            }
            Stmt::Loop { condition, body } => {
                let mut children: Vec<Item> = condition.map(Item::Expr).into_iter().collect();
                children.extend(body.map(Item::Block));
                ("loop statement", None, children)
            }
            Stmt::ForIn {
                index,
                value,
                iterable,
                body,
            } => {
                let mut children: Vec<Item> = index.map(declared).into_iter().collect();
                children.push(declared(value));
                children.push(Item::Expr(iterable));
                children.extend(body.map(Item::Block));
                ("for in statement", None, children)
            }
            Stmt::Break => ("break statement", None, vec![]),
            Stmt::Continue => ("continue statement", None, vec![]),
            Stmt::Match { subject, arms } => {
                let mut children = vec![Item::Expr(subject)];
                children.extend(t.lists.items(arms).map(Item::MatchArm));
                ("match statement", None, children)
            }
            Stmt::Block(_) | Stmt::Const(_) => {
                unreachable!("written as their block or declaration")
            }
        };
        (kind, op, t.stmts.span(id), children)
    }

    fn pattern(&self, id: Id<Pattern>) -> (&'static str, Option<&'static str>, Span, Vec<Item>) {
        let t = self.tree;
        let (kind, children) = match t.patterns[id] {
            Pattern::Wildcard => ("wildcard pattern", vec![]),
            Pattern::None => ("none pattern", vec![]),
            Pattern::Case { case, inner } => {
                let mut children = vec![Item::Type(case)];
                children.extend(inner.map(Item::Pattern));
                ("case pattern", children)
            }
            Pattern::Binding => ("declared name", vec![]),
            Pattern::Struct(fields) => (
                "struct pattern",
                t.lists.items(fields).map(Item::Pattern).collect(),
            ),
            Pattern::Field { field, binding } => {
                let mut children = vec![Item::Leaf("field name", field.span)];
                children.extend(binding.map(declared));
                ("field pattern", children)
            }
            Pattern::Rest => ("rest pattern", vec![]),
            Pattern::Invalid => ("invalid expression", vec![]),
        };
        (kind, None, t.patterns.span(id), children)
    }

    fn expr(&self, id: Id<Expr>) -> (&'static str, Option<&'static str>, Span, Vec<Item>) {
        let t = self.tree;
        let one = |e: Id<Expr>| vec![Item::Expr(e)];
        let two = |a: Id<Expr>, b: Item| vec![Item::Expr(a), b];
        let (kind, op, children) = match t.exprs[id] {
            Expr::Name => ("name reference", None, vec![]),
            Expr::Number => ("number literal", None, vec![]),
            Expr::String { c_string } => (
                "string literal",
                c_string.then_some(TokenKind::CString.text()),
                vec![],
            ),
            Expr::Char { byte } => (
                "character literal",
                byte.then_some(TokenKind::ByteChar.text()),
                vec![],
            ),
            Expr::Bool(_) => ("bool literal", None, vec![]),
            Expr::None => ("none literal", None, vec![]),
            Expr::Binary { op, left, right } => (
                "binary expression",
                Some(op.symbol()),
                two(left, Item::Expr(right)),
            ),
            Expr::Unary { op, value } => ("unary expression", Some(op.symbol()), one(value)),
            Expr::Reference { mutable: m, value } => {
                ("reference expression", mutable(m), one(value))
            }
            Expr::RawAddress(value) => ("raw address expression", None, one(value)),
            Expr::Free(value) => (
                "free expression",
                None,
                value.map(Item::Expr).into_iter().collect(),
            ),
            Expr::Print { newline, value } => (
                "print expression",
                Some(if newline {
                    TokenKind::Println.text()
                } else {
                    TokenKind::Print.text()
                }),
                value.map(Item::Expr).into_iter().collect(),
            ),
            Expr::Call { callee, arguments } => {
                let mut children = one(callee);
                children.extend(t.lists.items(arguments).map(Item::Expr));
                ("call expression", None, children)
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
            Expr::Range {
                inclusive,
                start,
                end,
            } => (
                "range",
                Some(if inclusive {
                    TokenKind::DotDotEqual.text()
                } else {
                    TokenKind::DotDot.text()
                }),
                start.into_iter().chain(end).map(Item::Expr).collect(),
            ),
            Expr::StructLiteral { ty, fields } => {
                let mut children: Vec<Item> = ty.map(Item::Type).into_iter().collect();
                children.extend(t.lists.items(fields).map(Item::FieldValue));
                ("struct literal", None, children)
            }
            Expr::VariantLiteral { case, payload } => (
                "variant literal",
                None,
                vec![Item::Type(case), Item::Expr(payload)],
            ),
            Expr::ArrayLiteral { ty, values } => {
                let mut children = vec![Item::Type(ty)];
                children.extend(t.lists.items(values).map(Item::Expr));
                ("array literal", None, children)
            }
            Expr::Cast { value, ty } => ("cast expression", None, two(value, Item::Type(ty))),
            Expr::Is { value, case } => ("is expression", None, two(value, Item::Type(case))),
            Expr::OrElse { value, fallback } => {
                ("orelse expression", None, two(value, Item::Expr(fallback)))
            }
            Expr::Pipe { value, call } => ("pipe expression", None, two(value, Item::Expr(call))),
            Expr::Parentheses(inner) => ("parentheses", None, one(inner)),
            Expr::Invalid => ("invalid expression", None, vec![]),
            Expr::Path(_) | Expr::Block(_) => unreachable!("written as their type or block"),
        };
        (kind, op, t.exprs.span(id), children)
    }
}
