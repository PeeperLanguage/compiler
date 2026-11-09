/// Where something is written: its start and end byte in the source.
#[derive(Clone, Copy, PartialEq, Eq, Default, Debug)]
pub struct Span {
    pub start: u32,
    pub end: u32,
}
