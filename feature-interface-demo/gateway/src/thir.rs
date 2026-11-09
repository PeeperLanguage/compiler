//! THIR: the typed tree of every function body, built by checking. Every
//! name is resolved, and every step the source leaves out (a conversion,
//! the dereference before a field, the borrow of a method's receiver, a
//! reborrow of a `&mut` argument) is a node of its own. Lowering reads only
//! this, never the syntax tree. Like the syntax tree it is two flat lists,
//! and each node holds the kind that made it, so a feature lowers its own
//! nodes and the core never matches on them.

use model::mir::Operand;
use model::mir::Operator;
use model::mir::Place;
use model::mir::Projection;
use model::mir::VariableId;
use model::span::Span;
use model::types::TypeId;

use crate::check::Checker;
use crate::check::FunctionId;
use crate::lower::Lowering;
use crate::lower::closing_brace;
use crate::types::Converter;
use crate::types::Field;
use crate::types::OperatorLowering;

#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct ThirId(pub u32);

/// Every question lowering asks about a node. A missing one does not build.
pub struct ThirKind {
    /// Its value: a register for a scalar, else the value in memory; for a
    /// node of no value, anything.
    pub lower: fn(&mut Lowering<'_>, ThirId) -> Operand,
    /// How it names storage; `None` when it never does.
    pub place: Option<PlaceKind>,
}

pub struct PlaceKind {
    pub lower: fn(&mut Lowering<'_>, ThirId) -> Place,
    /// Whether the storage may be set through it, or borrowed `&mut`.
    pub mutable: fn(&Checker<'_>, ThirId) -> bool,
}

/// What a node knows besides its type and its parts; its kind says which.
pub enum Data {
    None,
    Number(u64),
    Variable(VariableId),
    Function(FunctionId),
    Field(Field),
    Mutable(bool),
    /// The declaration index of each part, in the order written.
    Fields(Box<[u32]>),
    Converter(Converter),
    Operator(Operator, OperatorLowering),
}

pub struct Node {
    pub kind: &'static ThirKind,
    pub ty: TypeId,
    pub span: Span,
    pub data: Data,
    first_child: u32,
    child_count: u32,
}

#[derive(Default)]
pub struct Thir {
    nodes: Vec<Node>,
    children: Vec<ThirId>,
}

impl Thir {
    pub fn push(
        &mut self,
        kind: &'static ThirKind,
        ty: TypeId,
        span: Span,
        data: Data,
        children: &[ThirId],
    ) -> ThirId {
        let id = ThirId(self.nodes.len() as u32);
        self.nodes.push(Node {
            kind,
            ty,
            span,
            data,
            first_child: self.children.len() as u32,
            child_count: children.len() as u32,
        });
        self.children.extend_from_slice(children);
        id
    }

    pub fn node(&self, id: ThirId) -> &Node {
        &self.nodes[id.0 as usize]
    }

    pub fn children(&self, id: ThirId) -> &[ThirId] {
        let node = self.node(id);
        let first = node.first_child as usize;
        &self.children[first..first + node.child_count as usize]
    }
}

/// The value of a place node: what its storage holds.
pub fn read_place(lw: &mut Lowering<'_>, id: ThirId) -> Operand {
    let place = lw.place(id);
    lw.read(place, lw.checker.thir().node(id).ty)
}

// The core's own kinds.

/// A variable, whole.
pub static LOCAL: ThirKind = ThirKind {
    lower: read_place,
    place: Some(PlaceKind {
        lower: |lw, id| match lw.checker.thir().node(id).data {
            Data::Variable(variable) => Place::whole(variable),
            _ => unreachable!("a local names its variable"),
        },
        mutable: |cx, id| match cx.thir().node(id).data {
            Data::Variable(variable) => cx.variable(variable).mutable,
            _ => unreachable!("a local names its variable"),
        },
    }),
};

/// A function named as a value.
pub static FUNCTION: ThirKind = ThirKind {
    lower: |lw, id| match lw.checker.thir().node(id).data {
        Data::Function(function) => Operand::Function(function.0),
        _ => unreachable!("a function node names its function"),
    },
    place: None,
};

/// A value known while checking, such as a literal.
pub static CONSTANT: ThirKind = ThirKind {
    lower: |lw, id| match lw.checker.thir().node(id).data {
        Data::Number(value) => Operand::Constant(value),
        _ => unreachable!("a constant holds its value"),
    },
    place: None,
};

/// Its one part, as the type expected where it is used.
pub static CONVERT: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        let (node, part) = (checker.thir().node(id), checker.thir().children(id)[0]);
        let Data::Converter(converter) = node.data else {
            unreachable!("a conversion holds its converter");
        };
        let value = lw.value(part);
        converter(lw, value, checker.thir().node(part).ty, node.ty)
    },
    place: None,
};

/// `{ statements }`, a scope: its variables end at its `}`.
pub static BLOCK: ThirKind = ThirKind {
    lower: |lw, id| {
        let checker = lw.checker;
        lw.enter_scope();
        for &statement in checker.thir().children(id) {
            lw.value(statement);
        }
        lw.leave_scope(closing_brace(checker.thir().node(id).span));
        Operand::Constant(0)
    },
    place: None,
};

/// What the reference that is its one part points at. Several features
/// see through a reference (`.*`, a field of what it points at), so the
/// core owns the step; the reference's type kind says what it points at.
pub static DEREF: ThirKind = ThirKind {
    lower: read_place,
    place: Some(PlaceKind {
        lower: |lw, id| {
            let mut place = lw.place(lw.checker.thir().children(id)[0]);
            place.projections.push(Projection::Dereference);
            place
        },
        mutable: |cx, id| matches!(cx.thir().node(id).data, Data::Mutable(true)),
    }),
};

/// What stands for a refused node. A program with a refusal is never
/// lowered.
pub static INVALID: ThirKind = ThirKind {
    lower: |_, _| unreachable!("a program with a refusal is never lowered"),
    place: None,
};
