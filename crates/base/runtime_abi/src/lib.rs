//! What a running program does as seen from outside: its streams and exit
//! code, the runtime functions it calls, what it is told when it fails, and
//! how its numbers read. The backend writes code for these and the interpreter
//! acts them out, so a program does the same either way.
//!
//! The runtime library is built by one `rustc` call, with no crates, so
//! what it needs too stays in runtime/ and is read from there.

#[path = "../../../../runtime/abi.rs"]
pub mod abi;
pub mod messages;
#[path = "../../../../runtime/number_text.rs"]
pub mod number_text;
pub mod symbols;
