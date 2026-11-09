//! The syntax tree: one flat list of nodes and one flat list of children,
//! so building it makes no allocation per node. Each node holds how the
//! feature that made it checks it into THIR, so the core never matches on
//! which feature a node is.

use model::span::Span;
use model::types::TypeId;

use crate::check::Checker;
use crate::check::DeclarationKind;
use crate::thir::ThirId;

#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct NodeId(pub u32);

/// Typed ids, so a statement cannot be handed where an expression goes.
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct ExprNode(pub NodeId);
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct StmtNode(pub NodeId);
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct DeclNode(pub NodeId);
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct TypeNode(pub NodeId);
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct BlockNode(pub NodeId);

#[derive(Clone, Copy)]
pub enum Kind {
    /// Checks it where a value of the type given, if any, is expected.
    Expression(fn(&mut Checker<'_>, ExprNode, expected: Option<TypeId>) -> ThirId),
    Statement(fn(&mut Checker<'_>, StmtNode) -> ThirId),
    Declaration(&'static DeclarationKind),
    Type(fn(&mut Checker<'_>, TypeNode) -> TypeId),
    /// A name as written: `x` in `let x`, `side` in `.side`.
    Name,
    /// `{ statements }`, a scope.
    Block,
    /// An optional part that is not written, such as `let x = 1;`'s type.
    Missing,
}

pub struct Node {
    pub kind: Kind,
    pub span: Span,
    first_child: u32,
    child_count: u32,
}

#[derive(Default)]
pub struct Tree {
    nodes: Vec<Node>,
    children: Vec<NodeId>,
}

impl Tree {
    pub fn push(&mut self, kind: Kind, span: Span, children: &[NodeId]) -> NodeId {
        let id = NodeId(self.nodes.len() as u32);
        self.nodes.push(Node {
            kind,
            span,
            first_child: self.children.len() as u32,
            child_count: children.len() as u32,
        });
        self.children.extend_from_slice(children);
        id
    }

    pub fn node(&self, id: NodeId) -> &Node {
        &self.nodes[id.0 as usize]
    }

    pub fn children(&self, id: NodeId) -> &[NodeId] {
        let node = self.node(id);
        let first = node.first_child as usize;
        &self.children[first..first + node.child_count as usize]
    }

    pub fn len(&self) -> usize {
        self.nodes.len()
    }

    pub fn is_empty(&self) -> bool {
        self.nodes.is_empty()
    }

    pub fn is_missing(&self, id: NodeId) -> bool {
        matches!(self.node(id).kind, Kind::Missing)
    }
}
