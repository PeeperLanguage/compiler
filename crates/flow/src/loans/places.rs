//! Places as byte ranges, one per level (`Bytes`), and what is known
//! about a place from its steps.

use std::ops::Range;

use model::mir::Operand;
use model::mir::PlaceRef;
use model::mir::Root;
use model::mir::Step;

use crate::loans::Holders;

/// Whether the steps name their bytes exactly up to the first `Through`:
/// each index is known while compiling.
pub(super) fn known_until_through(steps: &[Step]) -> bool {
    (steps.iter().take_while(|&&step| step != Step::Through)).all(|step| match step {
        Step::Index { index, .. } => matches!(index, Operand::Constant { .. }),
        Step::Offset(_) | Step::Through => true,
    })
}

/// Whether `place` names its holders one by one, so a write gives each a
/// new value and a read copies each: it is in a slot's own storage, and
/// each index is known while compiling. An index known only when the
/// program runs may be any element.
pub(super) fn names_exactly(place: PlaceRef) -> bool {
    matches!(place.root, Root::Slot(_))
        && !place.steps.contains(&Step::Through)
        && known_until_through(place.steps)
}

/// The bytes a place covers at one level: `count` runs of `width` bytes,
/// `stride` apart from `start`. One run, unless an index only the program
/// knows makes it a part of each element the index may pick.
#[derive(Clone, Copy)]
pub(super) struct Bytes {
    start: u64,
    width: u64,
    stride: u64,
    count: u64,
}

impl Bytes {
    /// From the first run's start to the last run's end.
    pub(super) fn span(self) -> Range<u64> {
        let last = self.count.saturating_sub(1).saturating_mul(self.stride);
        self.start..self.start.saturating_add(last).saturating_add(self.width)
    }

    /// Whether the runs cover all of `span`: one run, or runs as wide as
    /// their stride.
    fn is_solid(self) -> bool {
        self.count == 1 || self.width >= self.stride
    }

    /// Whether some run meets `range`.
    fn run_meets(self, range: &Range<u64>) -> bool {
        // The first run that ends after `range` starts.
        let first = match range.start.checked_sub(self.start + self.width) {
            None => 0,
            Some(past) => past / self.stride + 1,
        };
        first < self.count && self.start + first.saturating_mul(self.stride) < range.end
    }

    /// Whether `self` and `other` may share a byte. Runs of one stride
    /// meet where their parts of an element meet; runs of two strides are
    /// taken to meet.
    pub(super) fn meets(self, other: Bytes) -> bool {
        let (a, b) = (self.span(), other.span());
        if a.start >= b.end || b.start >= a.end {
            return false;
        }
        match (self.is_solid(), other.is_solid()) {
            (true, true) => true,
            (false, true) => self.run_meets(&b),
            (true, false) => other.run_meets(&a),
            (false, false) if self.stride == other.stride => {
                let stride = self.stride;
                // Where `other`'s part starts in `self`'s element.
                let shift = (other.start % stride + stride - self.start % stride) % stride;
                shift < self.width || shift + other.width > stride
            }
            (false, false) => true,
        }
    }
}

impl<'b> Holders<'b> {
    /// The bytes of a place of `steps`, one per level: in its slot, then in
    /// what each pointer it goes through points to. A level before a
    /// `Through` is the pointer gone through; the last covers `width` bytes,
    /// or all there are when `None`. An index only the program knows makes
    /// a level a part of each element it may pick; a second one there, all
    /// the bytes from its first element on.
    pub(super) fn levels<'s>(
        &self,
        steps: &'s [Step],
        width: Option<u64>,
    ) -> impl Iterator<Item = Bytes> + 's {
        let pointer_bytes = self.pointer_bytes;
        let mut steps = steps.iter();
        let mut done = false;
        std::iter::from_fn(move || {
            if done {
                return None;
            }
            let mut bytes = Bytes {
                start: 0,
                width: 0,
                stride: 0,
                count: 1,
            };
            let mut to_the_end = false; // a second index the program picks
            for &step in steps.by_ref() {
                match step {
                    Step::Through => {
                        if !to_the_end {
                            bytes.width = pointer_bytes;
                        }
                        return Some(bytes);
                    }
                    _ if to_the_end => {}
                    Step::Offset(offset) => bytes.start += offset,
                    Step::Index {
                        index: Operand::Constant { bits, .. },
                        size,
                        ..
                    } => bytes.start += bits[0] * size,
                    Step::Index { size, count, .. } if bytes.count == 1 => {
                        bytes.stride = size;
                        bytes.count = count.unwrap_or(u64::MAX);
                    }
                    Step::Index { .. } => {
                        (bytes.width, bytes.count, to_the_end) = (u64::MAX, 1, true);
                    }
                }
            }
            done = true;
            if !to_the_end {
                bytes.width = width.unwrap_or(u64::MAX);
            }
            Some(bytes)
        })
    }

    /// The bytes of a place of `steps` in its slot (`levels`).
    pub(super) fn first_level(&self, steps: &[Step], width: Option<u64>) -> Bytes {
        (self.levels(steps, width).next()).expect("a place has a level")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// `count` runs of `width` bytes, `stride` apart from `start`.
    fn runs(start: u64, width: u64, stride: u64, count: u64) -> Bytes {
        Bytes {
            start,
            width,
            stride,
            count,
        }
    }

    /// The second word of each 16-byte element, as a field read at an index
    /// only the program knows, meets only the bytes of a second word.
    #[test]
    fn a_part_of_each_element_meets_only_that_part() {
        let second_words = runs(8, 8, 16, 4);
        assert!(!second_words.meets(runs(0, 8, 0, 1)), "the first word");
        assert!(
            second_words.meets(runs(24, 8, 0, 1)),
            "element 1's second word"
        );
        assert!(
            !second_words.meets(runs(64, 8, 0, 1)),
            "past the last element"
        );
        assert!(!second_words.meets(runs(0, 8, 16, 4)), "every first word");
        assert!(
            second_words.meets(runs(12, 4, 16, 4)),
            "a part of each second word"
        );
        assert!(
            second_words.meets(runs(0, 16, 16, 4)),
            "every element whole"
        );
        // Inside the span, between the runs.
        assert!(
            !second_words.meets(runs(32, 8, 0, 1)),
            "element 2's first word"
        );
        assert!(
            second_words.meets(runs(0, 4, 32, 2)),
            "runs of another stride are taken to meet"
        );
    }

    /// An index into a slice, whose length only the program knows, may pick
    /// any element: the runs go on without end and their span saturates.
    #[test]
    fn runs_of_unknown_count_reach_every_element() {
        let second_words = runs(8, 8, 16, u64::MAX);
        assert_eq!(second_words.span(), 8..u64::MAX);
        assert!(!second_words.meets(runs(1 << 40, 8, 0, 1)), "a first word");
        assert!(
            second_words.meets(runs((1 << 40) + 8, 8, 0, 1)),
            "a second word"
        );
    }
}
