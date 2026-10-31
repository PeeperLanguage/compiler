//! The runtime functions a program calls, which `core` and the runtime
//! library spell too. The backend keeps those only `print` calls, with their
//! LLVM parameters.

pub const ALLOC: &str = "peeper_rt_v1_alloc";
pub const FREE: &str = "peeper_rt_v1_free";
pub const WRITE: &str = "peeper_rt_v1_write";
pub const READ: &str = "peeper_rt_v1_read";
pub const EXIT: &str = "peeper_rt_v1_exit";
