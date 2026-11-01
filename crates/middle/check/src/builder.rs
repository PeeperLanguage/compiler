//! The builder that checks one piece of code into THIR, which the
//! statement and expression checks are methods of.

use arena::Id;
use model::semantic_types::TypeId;
use model::thir::Block;
use model::thir::Body;
use model::thir::Local;
use rustc_hash::FxHashMap;

use crate::Checker;

/// A name declared in a block, and what it stands for.
pub(crate) struct Declared<'p> {
    pub(crate) name: &'p str,
    pub(crate) local: Local,
    /// Where in `names` the declaration it hides is, which `innermost` names
    /// again when this one's block ends.
    pub(crate) hides: Option<usize>,
}

/// Building the THIR of one piece of code: a function's body, or a
/// constant's value. It checks the code as it builds it.
pub(crate) struct ThirBuilder<'a, 'p> {
    /// The checker, borrowed for the build: its module, tables and
    /// diagnostics.
    pub(crate) checker: &'a mut Checker<'p>,
    pub(crate) body: &'a mut Body,
    /// The names declared in the enclosing blocks so far, innermost last.
    pub(crate) names: Vec<Declared<'p>>,
    /// Where in `names` each name's innermost declaration is, so a lookup
    /// costs the same however many names a long body declares.
    pub(crate) innermost: FxHashMap<&'p str, usize>,
    /// Where in `names` the current block's names start.
    pub(crate) block_start: usize,
    /// The current block, for the variables it declares.
    pub(crate) block: Id<Block>,
    pub(crate) result: TypeId, // the function's result type
    pub(crate) loops: u32,     // how many loops the code being checked is in
    /// Whether the code being checked is a constant's value, whose calls run
    /// at compile time (`Checker::evaluate_call`).
    pub(crate) in_constant: bool,
}

impl<'a, 'p> ThirBuilder<'a, 'p> {
    /// Building code into `body` whose `return` gives `result`.
    pub(crate) fn new(
        checker: &'a mut Checker<'p>,
        body: &'a mut Body,
        result: TypeId,
    ) -> ThirBuilder<'a, 'p> {
        ThirBuilder {
            checker,
            body,
            names: Vec::new(),
            innermost: FxHashMap::default(),
            block_start: 0,
            block: Id::from_index(0), // the function's, added first (`Checker::check_body`)
            result,
            loops: 0,
            in_constant: false,
        }
    }
}
