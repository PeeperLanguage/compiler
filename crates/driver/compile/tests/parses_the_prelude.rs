//! The real prelude, written in Peeper with every feature, parses with no
//! error.

use std::sync::Arc;

use source::SourceFile;
use tree::ast;

/// The real prelude, the first file of every program, parses cleanly.
#[test]
fn parses_the_prelude() {
    let path = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../_builtin_library/core/src/global.peep"
    );
    let text = std::fs::read_to_string(path).unwrap();
    let file = Arc::new(SourceFile::new("global.peep", &text));
    let (_, errors): (ast::Tree, _) = parser::parse(&file);
    assert!(errors.is_empty(), "{errors:?}");
}
