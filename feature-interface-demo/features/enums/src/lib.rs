//! A new feature, half written on purpose: its type kind gives its facts
//! and its text, and nothing else yet. The build lists the rest
//! (`cargo check -p enums`):
//!
//! ```text
//! error[E0063]: missing fields `convert`, `iterate`, `member` and 2 other fields
//!   in initializer of `TypeKind`
//! ```

use gateway::facts::Carrier;
use gateway::facts::Facts;
use gateway::types::TypeKind;

/// `parts` are the payload types; the tag is one byte before them.
pub static ENUM: TypeKind = TypeKind {
    facts: |_, payloads, _| {
        let align = payloads
            .iter()
            .map(|payload| payload.align)
            .max()
            .unwrap_or(1);
        let largest = payloads
            .iter()
            .map(|payload| payload.size)
            .max()
            .unwrap_or(0);
        Facts {
            size: (align + largest).next_multiple_of(align),
            align,
            carrier: Carrier::Memory,
            copies: payloads.iter().all(|payload| payload.copies),
            holders: payloads
                .iter()
                .map(|payload| payload.holders)
                .max()
                .unwrap_or(0),
        }
    },
    text: |_, _| "enum".to_string(),
};
