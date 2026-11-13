//! The real prelude is written in Peeper, with every feature, so it is
//! read here, not in the parser's own tests (a language with no feature).

use std::sync::Arc;

use compile::language::Peeper;
use source::SourceFile;
use syntax::ast;

/// The real prelude, the first file of every program, parses cleanly.
#[test]
fn parses_the_prelude() {
    let path = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../_builtin_library/core/src/global.peep"
    );
    let text = std::fs::read_to_string(path).unwrap();
    let file = Arc::new(SourceFile::new("global.peep", &text));
    let (_, errors): (ast::Tree<Peeper>, _) = parser::parse(&file);
    assert!(errors.is_empty(), "{errors:?}");
}
