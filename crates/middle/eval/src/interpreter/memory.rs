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
/// How many bytes of data a run may use. Past this an allocation fails, as
/// when a machine has no more memory.
const DATA_LIMIT: u64 = 256 << 20;
/// The alignment of each block, as of what C's `malloc` gives.
const BLOCK_ALIGN: u64 = 16;

#[derive(Default)]
pub(super) struct Memory {
    stack: Vec<u8>,
    data: Vec<u8>,
    strings: HashMap<Box<[u8]>, u64>, // each one in `data`, by its text
    blocks: HashMap<u64, u64>,        // the size of each block in use, by its address
    freed: HashMap<u64, Vec<u64>>,    // the addresses of blocks free to reuse, by size
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

    /// The address of a block of at least `size` bytes, `None` past
    /// `DATA_LIMIT`. Freed blocks of its size come back, so a loop that
    /// allocates and frees stays in place.
    pub(super) fn allocate(&mut self, size: u64) -> Option<u64> {
        let size = size.max(1).checked_next_multiple_of(BLOCK_ALIGN)?;
        let reused = self.freed.get_mut(&size).and_then(Vec::pop);
        let address = if let Some(address) = reused {
            address
        } else {
            let offset = (self.data.len() as u64).next_multiple_of(BLOCK_ALIGN);
            let end = offset.checked_add(size).filter(|&end| end <= DATA_LIMIT)?;
            self.data.resize(end as usize, 0);
            DATA_START + offset
        };
        self.blocks.insert(address, size);
        Some(address)
    }

    /// Frees the block at `address`: `None` when no block starts there, so
    /// freeing twice stops the run.
    pub(super) fn free(&mut self, address: u64) -> Option<()> {
        let size = self.blocks.remove(&address)?;
        self.freed.entry(size).or_default().push(address);
        Some(())
    }

    /// The bytes from `address` up to the first zero byte, as C reads them.
    pub(super) fn c_text(&self, address: u64) -> Option<&[u8]> {
        let (region, start) = if address >= DATA_START {
            (&self.data, address - DATA_START)
        } else {
            (&self.stack, address.checked_sub(STACK_START)?)
        };
        let rest = region.get(usize::try_from(start).ok()?..)?;
        let end = rest.iter().position(|&byte| byte == 0)?;
        Some(&rest[..end])
    }

    /// The string at `address`: all of one `string` made, zero bytes in it
    /// included, else the bytes up to the first zero byte, as C reads it.
    pub(super) fn string_at(&self, address: u64) -> Option<Box<[u8]>> {
        if let Some((text, _)) = self.strings.iter().find(|&(_, &start)| start == address) {
            return Some(text.clone());
        }
        self.c_text(address).map(Into::into)
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
    fn a_freed_block_comes_back_and_cannot_be_freed_twice() {
        let mut memory = Memory::default();
        let first = memory.allocate(20).unwrap();
        let second = memory.allocate(30).unwrap();
        assert_eq!(first % BLOCK_ALIGN, 0);
        assert_eq!(second - first, 32, "20 bytes round up to 32");
        memory.free(first).unwrap();
        assert_eq!(memory.free(first), None, "freed twice");
        assert_eq!(memory.free(first + 1), None, "not the start of a block");
        assert_eq!(memory.allocate(25), Some(first), "the same size, reused");
        assert_eq!(memory.allocate(DATA_LIMIT), None, "past the limit");
        assert_eq!(memory.allocate(u64::MAX), None, "an overflowing size");
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
