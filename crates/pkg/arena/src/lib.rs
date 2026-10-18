//! Typed arenas. Nodes of one category (expressions, types, statements and so
//! on) are values of one enum, stored in that category's arena with their spans
//! and named by a typed `Id`, so a tree takes a few allocations and an id of one
//! category cannot stand where another is expected. Later phases keep their
//! results in vectors indexed by id.

use std::marker::PhantomData;
use std::num::NonZeroU32;
use std::ops::Index;
use std::ops::IndexMut;

use source::Span;

/// A node in the arena of `T`. `Option<Id<T>>` takes no more room than an id.
pub struct Id<T> {
    raw: NonZeroU32, // the node's position in its arena, plus one
    category: PhantomData<fn() -> T>,
}

impl<T> Id<T> {
    /// The node's position in its arena.
    pub fn index(self) -> usize {
        self.raw.get() as usize - 1
    }

    /// The node at position `index` of its arena: the id `index` came from.
    pub fn from_index(index: usize) -> Id<T> {
        let raw = u32::try_from(index + 1)
            .ok()
            .and_then(NonZeroU32::new)
            .expect("an arena holds fewer than 2^32 nodes");
        Id {
            raw,
            category: PhantomData,
        }
    }
}

// Derived impls would require T itself to be Clone, Copy and so on.
impl<T> Clone for Id<T> {
    fn clone(&self) -> Id<T> {
        *self
    }
}
impl<T> Copy for Id<T> {}
impl<T> PartialEq for Id<T> {
    fn eq(&self, other: &Id<T>) -> bool {
        self.raw == other.raw
    }
}
impl<T> Eq for Id<T> {}
impl<T> std::fmt::Debug for Id<T> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "#{}", self.index())
    }
}

/// Lets a builder keep the ids of the lists being read in one shared buffer.
impl<T> From<Id<T>> for NonZeroU32 {
    fn from(id: Id<T>) -> NonZeroU32 {
        id.raw
    }
}

/// A list of nodes of `T`: a range of the items in `Lists`.
pub struct List<T> {
    start: u32,
    len: u32,
    category: PhantomData<fn() -> T>,
}

impl<T> List<T> {
    pub const EMPTY: List<T> = List {
        start: 0,
        len: 0,
        category: PhantomData,
    };

    pub fn len(self) -> usize {
        self.len as usize
    }

    pub fn is_empty(self) -> bool {
        self.len == 0
    }
}

impl<T> Clone for List<T> {
    fn clone(&self) -> List<T> {
        *self
    }
}
impl<T> Copy for List<T> {}
impl<T> std::fmt::Debug for List<T> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "[{}; {}]", self.start, self.len)
    }
}

/// The items of every list of one tree or graph, each list's items together.
#[derive(Default)]
pub struct Lists {
    items: Vec<NonZeroU32>,
}

impl Lists {
    /// Stores the items as a list; `items` hold raw ids of `T` (see
    /// `From<Id<T>> for NonZeroU32`).
    pub fn add<T>(&mut self, items: &[NonZeroU32]) -> List<T> {
        if items.is_empty() {
            return List::EMPTY;
        }
        let start = self.items.len() as u32;
        self.items.extend_from_slice(items);
        List {
            start,
            len: items.len() as u32,
            category: PhantomData,
        }
    }

    /// The items of `list`.
    pub fn items<T>(&self, list: List<T>) -> impl ExactSizeIterator<Item = Id<T>> + '_ {
        self.items[list.start as usize..][..list.len()]
            .iter()
            .map(|&raw| Id {
                raw,
                category: PhantomData,
            })
    }
}

/// The nodes of one category and their spans.
pub struct Arena<T> {
    nodes: Vec<T>,
    spans: Vec<Span>,
}

impl<T> Arena<T> {
    /// Adds `node` with `span`.
    pub fn push(&mut self, node: T, span: Span) -> Id<T> {
        let id = Id::from_index(self.nodes.len());
        self.nodes.push(node);
        self.spans.push(span);
        id
    }

    pub fn span(&self, id: Id<T>) -> Span {
        self.spans[id.index()]
    }

    /// The id of each node, in the order they were added.
    pub fn ids(&self) -> impl ExactSizeIterator<Item = Id<T>> + use<T> {
        (0..self.nodes.len()).map(Id::from_index)
    }

    /// How many nodes it holds.
    pub fn len(&self) -> usize {
        self.nodes.len()
    }

    pub fn is_empty(&self) -> bool {
        self.nodes.is_empty()
    }
}

impl<T> Default for Arena<T> {
    fn default() -> Arena<T> {
        Arena {
            nodes: Vec::new(),
            spans: Vec::new(),
        }
    }
}

impl<T> Index<Id<T>> for Arena<T> {
    type Output = T;

    fn index(&self, id: Id<T>) -> &T {
        &self.nodes[id.index()]
    }
}

impl<T> IndexMut<Id<T>> for Arena<T> {
    fn index_mut(&mut self, id: Id<T>) -> &mut T {
        &mut self.nodes[id.index()]
    }
}

/// A node type: it knows its arena in the container that holds it (a syntax
/// tree, a THIR body, a MIR graph), so the container's `add` takes any node.
pub trait Node: Sized {
    type Container;
    fn arena(container: &mut Self::Container) -> &mut Arena<Self>;
}

/// `nodes!(Tree; Expr => exprs, ...)`: each node type lives in the named arena
/// field of the container.
#[macro_export]
macro_rules! nodes {
    ($container:ty; $($node:ty => $arena:ident),* $(,)?) => {
        $(impl $crate::Node for $node {
            type Container = $container;
            fn arena(container: &mut $container) -> &mut $crate::Arena<$node> {
                &mut container.$arena
            }
        })*
    };
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ids_and_lists_round_trip() {
        let mut arena = Arena::default();
        let a = arena.push('a', Span::new(0, 1));
        let b = arena.push('b', Span::new(2, 3));
        assert_eq!((arena[b], arena.span(b)), ('b', Span::new(2, 3)));
        assert_eq!(Id::<char>::from_index(b.index()), b);
        assert_eq!(arena.len(), 2);
        assert_eq!(arena.ids().collect::<Vec<_>>(), [a, b]);
        let mut lists = Lists::default();
        let list: List<char> = lists.add(&[a.into(), b.into()]);
        assert_eq!(lists.items(list).collect::<Vec<_>>(), [a, b]);
        assert!(lists.add::<char>(&[]).is_empty());
        assert_eq!(size_of::<Option<Id<char>>>(), 4);
    }
}
