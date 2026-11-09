/// A type: an index into the checker's type table, and into
/// `mir::Program::facts`.
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub struct TypeId(pub u32);

impl TypeId {
    /// The type of something already refused, so one error is not reported
    /// again by everything that uses it.
    pub const INVALID: TypeId = TypeId(0);
    /// The type of no value: a statement, a function that gives nothing.
    pub const VOID: TypeId = TypeId(1);
    /// What a condition is: the core needs it for `if` and `for`.
    pub const BOOL: TypeId = TypeId(2);
}
