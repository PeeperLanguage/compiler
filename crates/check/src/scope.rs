//! A module's scope: the names its top-level declarations and imports bring
//! in. It is collected from the declarations alone, before any body is
//! checked, so a body can use a name declared below it or in another module.
//! Names of the prelude (`core:global`) are not copied in; a lookup that
//! misses here goes on to the prelude module's scope.

use std::collections::HashMap;
use std::sync::Arc;

use diagnostics::Diagnostic;
use model::DefId;
use model::ModuleId;
use model::Symbol;
use model::ast;
use project::ImportKind;
use source::SourceFile;
use source::Span;

/// A place that holds declarations, and where each name is written. A module
/// has one for its top-level declarations and imports. A name it does not
/// hold is looked up next in its parent's.
pub struct Scope {
    /// The module whose scope comes next: the prelude (`core:global`), for
    /// every module but the prelude.
    pub parent: Option<ModuleId>,
    names: HashMap<Box<str>, (Symbol, Span)>,
}

impl Scope {
    pub fn new(parent: Option<ModuleId>) -> Scope {
        Scope {
            parent,
            names: HashMap::new(),
        }
    }

    /// What `name` stands for in this scope alone.
    pub fn get(&self, name: &str) -> Option<Symbol> {
        self.names.get(name).map(|&(symbol, _)| symbol)
    }

    /// Every name, in no order.
    pub fn names(&self) -> impl Iterator<Item = &str> {
        self.names.keys().map(|name| &**name)
    }

    /// Brings `name` in as `symbol`, written at `span`. A name already in
    /// keeps its first meaning, and gives where that was written.
    pub fn declare(&mut self, name: &str, symbol: Symbol, span: Span) -> Result<(), Span> {
        match self.names.get(name) {
            Some(&(_, first)) => Err(first),
            None => {
                self.names.insert(name.into(), (symbol, span));
                Ok(())
            }
        }
    }
}

/// Reports `name` declared again at `again` in the same `place` (a module or
/// a block) of `file`, where it was first declared at `first`.
pub(crate) fn report_redeclared(
    file: &Arc<SourceFile>,
    name: &str,
    place: &str,
    again: Span,
    first: Span,
) -> Diagnostic {
    Diagnostic::new(
        diagnostics::ALREADY_DECLARED,
        format!("`{name}` is already declared in this {place}"),
    )
    .label(file, again, "declared again here")
    .label(file, first, "first declared here")
}

/// The scope of the module `id` of `modules`. A name declared twice keeps
/// its first meaning and is reported.
pub fn collect(
    id: ModuleId,
    modules: &[project::Module],
    diagnostics: &mut Vec<Diagnostic>,
) -> Scope {
    let module = &modules[id.index()];
    let file = &module.ast.file;
    let prelude = module
        .imports
        .iter()
        .find(|import| import.kind == ImportKind::Prelude);
    let mut scope = Scope::new(prelude.map(|import| import.module));
    let mut declare = |name: &str, symbol: Symbol, span: Span| {
        if name.is_empty() {
            return; // missing after a syntax error
        }
        if let Err(first) = scope.declare(name, symbol, span) {
            diagnostics.push(report_redeclared(file, name, "module", span, first));
        }
    };
    // The loader already reported aliases used twice and kept the first.
    for import in &module.imports {
        if let Some((name, span)) = import.name(module, modules) {
            declare(name, Symbol::Module(import.module), span);
        }
    }
    let tree = &module.ast;
    for (index, declaration) in tree.lists.items(tree.top_level).enumerate() {
        let definition = DefId {
            module: id,
            index: index as u32,
        };
        let (name, symbol) = match tree.decls[declaration] {
            ast::Decl::Function(function) if function.signature.receiver.is_none() => {
                (function.name, Symbol::Function(definition))
            }
            ast::Decl::Function(_) => continue, // a method belongs to its receiver's type
            ast::Decl::Const { name, .. } => (name, Symbol::Constant(definition)),
            ast::Decl::Type(declaration) | ast::Decl::Alias(declaration) => {
                (declaration.name, Symbol::Type(definition))
            }
        };
        declare(name.text(file), symbol, name.span);
    }
    scope
}

#[cfg(test)]
mod tests {
    use model::DefId;
    use model::ModuleId;
    use project::Import;
    use project::ImportKind;

    use super::collect;
    use crate::testing::module;
    use model::Symbol;

    /// The declaration `index` of the module at place `module`.
    fn definition(module: usize, index: u32) -> DefId {
        DefId {
            module: ModuleId::from_index(module),
            index,
        }
    }

    #[test]
    fn declarations_and_imports() {
        let text = "import \"app:utils\";\nfn main() { const K: i32 = 1; later(); }\nfn later() {}\nconst Limit: i32 = 4;\nstruct Point { x: i32 }\nfn (p: Point) length() -> i32 { return 0; }\n";
        let (utils, global) = (ModuleId::from_index(0), ModuleId::from_index(1));
        let imports = vec![
            Import {
                kind: ImportKind::Declared(0),
                module: utils,
            },
            Import {
                kind: ImportKind::Prelude,
                module: global,
            },
        ];
        let modules = [
            module("app:utils", "", Vec::new()),
            module("core:global", "fn global() {}", Vec::new()),
            module("app:main", text, imports),
        ];
        let mut diagnostics = Vec::new();
        let scope = collect(ModuleId::from_index(2), &modules, &mut diagnostics);
        assert!(diagnostics.is_empty());
        // A declaration's place counts top-level declarations only: `main`'s
        // body `const` does not move `later`.
        assert_eq!(scope.get("main"), Some(Symbol::Function(definition(2, 0))));
        assert_eq!(scope.get("later"), Some(Symbol::Function(definition(2, 1))));
        assert_eq!(scope.get("Limit"), Some(Symbol::Constant(definition(2, 2))));
        assert_eq!(scope.get("Point"), Some(Symbol::Type(definition(2, 3))));
        assert_eq!(scope.get("utils"), Some(Symbol::Module(utils)));
        assert_eq!(scope.get("length"), None, "a method is not a module name");
        assert_eq!(scope.get("global"), None, "the prelude is not copied in");
        assert_eq!(scope.parent, Some(global), "the prelude's scope comes next");
    }

    #[test]
    fn a_name_declared_twice_keeps_its_first_meaning() {
        let text = "import \"app:utils\";\nfn utils() {}\nconst A: i32 = 1;\nfn A() {}\n";
        let utils = ModuleId::from_index(0);
        let imports = vec![Import {
            kind: ImportKind::Declared(0),
            module: utils,
        }];
        let modules = [
            module("app:utils", "", Vec::new()),
            module("app:main", text, imports),
        ];
        let mut diagnostics = Vec::new();
        let scope = collect(ModuleId::from_index(1), &modules, &mut diagnostics);
        assert_eq!(scope.get("A"), Some(Symbol::Constant(definition(1, 1))));
        assert_eq!(scope.get("utils"), Some(Symbol::Module(utils)));
        let messages: Vec<_> = diagnostics
            .iter()
            .map(|d| (d.code.id, d.message.as_str(), d.labels.len()))
            .collect();
        assert_eq!(
            messages,
            [
                ("T0003", "`utils` is already declared in this module", 2),
                ("T0003", "`A` is already declared in this module", 2),
            ]
        );
    }
}
