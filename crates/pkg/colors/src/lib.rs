//! Terminal color codes.

pub const RESET: &str = "\x1b[0m";

pub const RED: &str = "\x1b[31m";
pub const GREEN: &str = "\x1b[32m";
pub const YELLOW: &str = "\x1b[33m";
pub const BLUE: &str = "\x1b[34m";
pub const PURPLE: &str = "\x1b[35m";
pub const CYAN: &str = "\x1b[36m";
pub const WHITE: &str = "\x1b[37m";
pub const GREY: &str = "\x1b[90m";

pub const BOLD_RED: &str = "\x1b[1;31m";
pub const BOLD_YELLOW: &str = "\x1b[1;33m";
pub const BOLD_CYAN: &str = "\x1b[1;36m";

pub const ORANGE: &str = "\x1b[38;5;208m";
pub const LIGHT_ORANGE: &str = "\x1b[38;5;215m";
pub const LIGHT_GREEN: &str = "\x1b[38;5;120m";

/// Text a suggested fix adds: a dark letter on a mid-tone block of the same
/// hue, readable on both dark and light terminals.
pub const FIX_ADDED: &str = "\x1b[38;5;22;48;5;114m";
