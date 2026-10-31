//! The bytes a run keeps its values in: a stack of frames, and the data that
//! outlives them. Any address a run makes may be wrong (a raw pointer is
//! whatever the program says), so each access is checked and a wrong one
//! gives `None`, for the run to stop with a message.

use std::collections::HashMap;
use std::ops::Range;

/// Where the stack starts: above 0, the null address.
pub(super) const STACK_START: u64 = 16;
/// How many bytes of frames a run may use, as much as a usual native stack.
const STACK_LIMIT: u64 = 8 << 20;
/// Where the data starts, an address every target's pointers hold.
const DATA_START: u64 = 1 << 30;

#[derive(Default)]
pub(super) struct Memory {
    stack: Vec<u8>,
    data: Vec<u8>,
    strings: HashMap<Box<[u8]>, u64>, // each one in `data`, by its text
}

impl Memory {
    /// Makes the stack reach `end`: `None` past its limit. What is new may
    /// hold anything, as stack memory does; flow reads no unset variable.
    pub(super) fn grow_stack(&mut self, end: u64) -> Option<()> {
        let size = end.checked_sub(STACK_START)?;
        if size > STACK_LIMIT {
            return None;
        }
        if size as usize > self.stack.len() {
            self.stack.resize(size as usize, 0);
        }
        Some(())
    }

    /// The `length` bytes at `address`.
    pub(super) fn bytes(&self, address: u64, length: u64) -> Option<&[u8]> {
        let (region, start) = if address >= DATA_START {
            (&self.data, address - DATA_START)
        } else {
            (&self.stack, address.checked_sub(STACK_START)?)
        };
        region.get(host_range(start, length)?)
    }

    /// The `length` bytes at `address`, to write.
    pub(super) fn bytes_mut(&mut self, address: u64, length: u64) -> Option<&mut [u8]> {
        let (region, start) = if address >= DATA_START {
            (&mut self.data, address - DATA_START)
        } else {
            (&mut self.stack, address.checked_sub(STACK_START)?)
        };
        region.get_mut(host_range(start, length)?)
    }

    /// Copies `length` bytes from `from` to `to`, which may overlap, as a
    /// value of a place assigned to itself does.
    pub(super) fn copy(&mut self, from: u64, to: u64, length: u64) -> Option<()> {
        self.bytes(from, length)?;
        self.bytes(to, length)?;
        let (region, start) = match (from >= DATA_START, to >= DATA_START) {
            (false, false) => (&mut self.stack, STACK_START),
            (true, true) => (&mut self.data, DATA_START),
            _ => {
                let source = self.bytes(from, length)?.to_vec();
                self.bytes_mut(to, length)?.copy_from_slice(&source);
                return Some(());
            }
        };
        let (from, to) = ((from - start) as usize, (to - start) as usize);
        region.copy_within(from..from + length as usize, to);
        Some(())
    }

    /// The address of the string `text`, kept with the zero byte that ends a
    /// C string.
    pub(super) fn string(&mut self, text: &[u8]) -> u64 {
        if let Some(&address) = self.strings.get(text) {
            return address;
        }
        let address = DATA_START + self.data.len() as u64;
        self.data.extend_from_slice(text);
        self.data.push(0);
        self.strings.insert(text.into(), address);
        address
    }

    /// The string at `address`: all of one `string` made, zero bytes in it
    /// included, else the bytes up to the first zero byte, as C reads it.
    pub(super) fn string_at(&self, address: u64) -> Option<Box<[u8]>> {
        if let Some((text, _)) = self.strings.iter().find(|&(_, &start)| start == address) {
            return Some(text.clone());
        }
        let start = usize::try_from(address.checked_sub(DATA_START)?).ok()?;
        let rest = self.data.get(start..)?;
        let end = rest.iter().position(|&byte| byte == 0)?;
        Some(rest[..end].into())
    }
}

/// The `length` bytes from `start` of a region, as the host indexes them:
/// `None` when they do not fit its `usize`, which on wasm is 32 bits, so an
/// address past that cannot wrap round to one the run owns.
fn host_range(start: u64, length: u64) -> Option<Range<usize>> {
    let end = start.checked_add(length)?;
    Some(usize::try_from(start).ok()?..usize::try_from(end).ok()?)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn copies_overlapping_bytes_and_refuses_wrong_addresses() {
        let mut memory = Memory::default();
        memory.grow_stack(STACK_START + 4).unwrap();
        memory
            .bytes_mut(STACK_START, 4)
            .unwrap()
            .copy_from_slice(&[1, 2, 3, 4]);
        memory.copy(STACK_START, STACK_START + 1, 3).unwrap();
        assert_eq!(memory.bytes(STACK_START, 4), Some(&[1, 1, 2, 3][..]));

        assert_eq!(memory.bytes(0, 1), None, "the null address");
        assert_eq!(memory.bytes(STACK_START + 3, 2), None, "past the stack");
        assert_eq!(
            memory.bytes(STACK_START, u64::MAX),
            None,
            "an overflowing length"
        );
        assert_eq!(memory.copy(STACK_START, STACK_START + 3, 2), None);
        assert_eq!(memory.grow_stack(STACK_START + STACK_LIMIT + 1), None);
    }

    #[test]
    fn a_string_keeps_its_zero_bytes_and_is_kept_once() {
        let mut memory = Memory::default();
        let address = memory.string(b"ab\0cd");
        assert_eq!(memory.string(b"ab\0cd"), address);
        assert_eq!(memory.string_at(address).as_deref(), Some(&b"ab\0cd"[..]));
        assert_eq!(memory.bytes(address + 5, 1), Some(&[0][..]), "C's end");
    }
}
