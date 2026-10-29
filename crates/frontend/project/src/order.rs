//! The order modules are checked in: each after the modules it imports, and
//! the import cycles that leave no such order.

use std::collections::HashMap;

use diagnostics::Diagnostic;

use crate::ImportKind;
use crate::Loaded;

#[derive(Clone, Copy, PartialEq)]
enum Visit {
    Started,
    Finished,
}
/// Orders modules so each follows the modules it imports, and reports every
/// import cycle. It visits modules and imports in path order, so the result
/// does not depend on how the workers were scheduled.
struct Sorter<'a> {
    modules: &'a HashMap<String, Loaded>,
    visits: HashMap<&'a str, Visit>,
    /// The modules being visited, outermost first, each with the import that
    /// reached it: none for the first.
    stack: Vec<(&'a str, Option<ImportKind>)>,
    order: Vec<String>,
    cycles: Vec<Diagnostic>,
}
pub(crate) fn sort_modules(modules: &HashMap<String, Loaded>) -> (Vec<String>, Vec<Diagnostic>) {
    let mut sorter = Sorter {
        modules,
        visits: HashMap::with_capacity(modules.len()),
        stack: Vec::new(),
        order: Vec::with_capacity(modules.len()),
        cycles: Vec::new(),
    };
    let mut paths: Vec<&str> = modules.keys().map(String::as_str).collect();
    paths.sort_unstable();
    for path in paths {
        if !sorter.visits.contains_key(path) {
            sorter.visit(path, None);
        }
    }
    (sorter.order, sorter.cycles)
}
impl<'a> Sorter<'a> {
    /// Visits the module `path`, reached by the import `reached_by`.
    fn visit(&mut self, path: &'a str, reached_by: Option<ImportKind>) {
        self.visits.insert(path, Visit::Started);
        self.stack.push((path, reached_by));
        let module = &self.modules[path];
        let mut imports: Vec<&'a (ImportKind, String)> = module.imports.iter().collect();
        imports.sort_by(|(_, a), (_, b)| a.cmp(b));
        // A module imported twice (`core:global`, written and implicit) is
        // one edge; the stable sort keeps the written import, which has a
        // place to point at.
        imports.dedup_by(|(_, a), (_, b)| a == b);
        // A long chain of imports is a deep recursion here.
        const RED_ZONE: usize = 128 * 1024;
        const SEGMENT: usize = 1024 * 1024;
        for &(kind, ref imported) in imports {
            match self.visits.get(imported.as_str()) {
                None => stacker::maybe_grow(RED_ZONE, SEGMENT, || self.visit(imported, Some(kind))),
                Some(Visit::Started) => {
                    let start = (self.stack.iter())
                        .position(|&(visiting, _)| visiting == imported)
                        .unwrap_or(0);
                    self.report_cycle(start, kind);
                }
                Some(Visit::Finished) => {}
            }
        }
        self.stack.pop();
        self.visits.insert(path, Visit::Finished);
        self.order.push(path.to_string());
    }

    /// Reports the cycle that the module visited last closes with its import
    /// `closing` of `stack[start]`: one place for each written import of the
    /// cycle, and a note for the prelude's implicit one.
    fn report_cycle(&mut self, start: usize, closing: ImportKind) {
        let mut diagnostic = Diagnostic::new(
            diagnostics::CYCLIC_IMPORT,
            "modules import each other in a cycle",
        );
        let cycle = &self.stack[start..];
        // Each module of the cycle takes the import that reached the next;
        // the last takes `closing` back to the first.
        let kinds = (cycle[1..].iter())
            .map(|&(_, reached_by)| {
                reached_by.expect("only the outermost module is reached by no import")
            })
            .chain([closing]);
        let imported = cycle[1..].iter().chain([&cycle[0]]);
        for (step, ((&(importer, _), kind), &(imported, _))) in
            cycle.iter().zip(kinds).zip(imported).enumerate()
        {
            let closes = if step + 1 == cycle.len() {
                ", closing the cycle"
            } else {
                ""
            };
            let tree = &self.modules[importer].ast;
            diagnostic = match kind.declaration(tree) {
                Some(declaration) => diagnostic.label(
                    &tree.file,
                    declaration.span,
                    format!("`{importer}` imports `{imported}`{closes}"),
                ),
                None => diagnostic.note(format!(
                    "`{importer}` imports `{imported}` implicitly, as every module does{closes}"
                )),
            };
        }
        self.cycles.push(diagnostic);
    }
}
