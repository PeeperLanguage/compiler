//! A module's scope: the names its top-level declarations and imports bring
//! in. It is collected from the declarations alone, before any body is
//! checked, so a body can use a name declared below it or in another module.
//! Names of the prelude (`core:global`) are not copied in; a lookup that
//! misses here goes on to the prelude module's scope.

use std::collections::HashMap;

use diagnostics::Diagnostic;
use model::Binding;
use model::DefId;
use model::ModuleId;
use model::ast;
use source::Span;

pub struct ModuleScope {
    names: HashMap<Box<str>, (Binding, Span)>, // and where the name is written
}

impl ModuleScope {
    pub fn get(&self, name: &str) -> Option<Binding> {
        self.names.get(name).map(|&(binding, _)| binding)
    }

    /// Every name, in no order.
    pub fn names(&self) -> impl Iterator<Item = &str> {
        self.names.keys().map(|name| &**name)
    }
}

/// The scope of the module `id` of `modules`. A name declared twice keeps
/// its first meaning and is reported.
pub fn collect(
    id: ModuleId,
    modules: &[project::Module],
    diagnostics: &mut Vec<Diagnostic>,
) -> ModuleScope {
    let module = &modules[id.index()];
    let file = &module.ast.file;
    let mut scope = ModuleScope {
        names: HashMap::new(),
    };
    let mut declare = |name: &str, binding: Binding, span: Span| {
        if name.is_empty() || name == "_" {
            return; // missing after a syntax error, or not a name
        }
        if let Some(&(_, first)) = scope.names.get(name) {
            diagnostics.push(
                Diagnostic::new(
                    diagnostics::ALREADY_DECLARED,
                    format!("`{name}` is already declared in this module"),
                )
                .label(file, span, "declared again here")
                .label(file, first, "first declared here"),
            );
            return;
        }
        scope.names.insert(name.into(), (binding, span));
    };
    // The loader already reported aliases used twice and kept the first.
    for import in &module.imports {
        if let Some((name, span)) = import.name(module, modules) {
            declare(name, Binding::Module(import.module), span);
        }
    }
    let tree = &module.ast;
    for declaration in tree.lists.items(tree.declarations) {
        let definition = DefId {
            module: id,
            index: declaration.index() as u32,
        };
        let (name, binding) = match tree.decls[declaration] {
            ast::Decl::Function(function) if function.receiver.is_none() => {
                (function.name, Binding::Function(definition))
            }
            ast::Decl::Function(_) => continue, // a method belongs to its receiver's type
            ast::Decl::Const { name, .. } => (name, Binding::Constant(definition)),
            ast::Decl::Struct(declaration)
            | ast::Decl::Enum(declaration)
            | ast::Decl::Interface(declaration)
            | ast::Decl::Alias(declaration) => (declaration.name, Binding::Type(definition)),
        };
        declare(name.text(file), binding, name.span);
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
    use model::Binding;

    /// The declaration `index` of the module at place `module`.
    fn definition(module: usize, index: u32) -> DefId {
        DefId {
            module: ModuleId::from_index(module),
            index,
        }
    }

    #[test]
    fn declarations_and_imports() {
        let text = "import \"app:utils\";\nfn main() { later(); }\nfn later() {}\nconst Limit: i32 = 4;\nstruct Point { x: i32 }\nfn (p: Point) length() -> i32 { return 0; }\n";
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
        assert_eq!(scope.get("main"), Some(Binding::Function(definition(2, 0))));
        assert_eq!(
            scope.get("later"),
            Some(Binding::Function(definition(2, 1)))
        );
        assert_eq!(
            scope.get("Limit"),
            Some(Binding::Constant(definition(2, 2)))
        );
        assert_eq!(scope.get("Point"), Some(Binding::Type(definition(2, 3))));
        assert_eq!(scope.get("utils"), Some(Binding::Module(utils)));
        assert_eq!(scope.get("length"), None, "a method is not a module name");
        assert_eq!(scope.get("global"), None, "the prelude is not copied in");
    }

    #[test]
    fn a_name_declared_twice_keeps_its_first_meaning() {
        let text = "import \"app:utils\";\nfn utils() {}\nconst A: i32 = 1;\nfn A() {}\nconst _: i32 = 2;\nconst _: i32 = 3;\n";
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
        assert_eq!(scope.get("A"), Some(Binding::Constant(definition(1, 1))));
        assert_eq!(scope.get("utils"), Some(Binding::Module(utils)));
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
