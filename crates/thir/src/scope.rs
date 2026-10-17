//! A module's scope: the names its top-level declarations and imports bring
//! in. It is collected from the declarations alone, before any body is
//! checked, so a body can use a name declared below it or in another module.
//! Names of the prelude (`core:global`) are not copied in; a lookup that
//! misses here goes on to the prelude module's scope.

use std::collections::HashMap;

use diagnostics::Diagnostic;
use ir::DefId;
use source::Span;

/// What a module-level name stands for, by the kind of its declaration.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Binding {
    Function(DefId),
    Constant(DefId),
    /// a struct, enum, interface or type alias
    Type(DefId),
    /// An imported module, by its place in the program's order.
    Module(u32),
}

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

/// The scope of `module`, the module at `module_index` in the program's
/// order; `module_indexes` gives every module's place by its import path.
/// A name declared twice keeps its first meaning and is reported.
pub fn collect(
    module_index: u32,
    module: &project::Module,
    module_indexes: &HashMap<&str, u32>,
    diagnostics: &mut Vec<Diagnostic>,
) -> ModuleScope {
    let file = &module.syntax.file;
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
        if let Some(alias) = &import.alias {
            let index = module_indexes[import.path.as_str()];
            declare(alias, Binding::Module(index), import.span);
        }
    }
    let tree = &module.syntax;
    for declaration in tree.lists.items(tree.declarations) {
        let definition = DefId {
            module: module_index,
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
        declare(file.slice(name), binding, name);
    }
    scope
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::sync::Arc;

    use ir::DefId;
    use source::SourceFile;

    use super::Binding;
    use super::collect;

    fn module(text: &str, imports: Vec<project::Import>) -> project::Module {
        let file = Arc::new(SourceFile::new("main.peep", text));
        let (syntax, diagnostics) = parser::parse(&file);
        assert!(diagnostics.is_empty(), "{diagnostics:?}");
        project::Module {
            path: "app:main".to_string(),
            syntax,
            imports,
            diagnostics: Vec::new(),
        }
    }

    #[test]
    fn declarations_and_imports() {
        let text = "fn main() { later(); }\nfn later() {}\nconst Limit: i32 = 4;\nstruct Point { x: i32 }\nfn (p: Point) length() -> i32 { return 0; }\n";
        let imports = vec![
            project::Import {
                path: "app:utils".to_string(),
                alias: Some("utils".to_string()),
                span: source::Span::new(0, 0),
            },
            project::Import {
                path: "core:global".to_string(),
                alias: None,
                span: source::Span::new(0, 0),
            },
        ];
        let indexes = HashMap::from([("app:utils", 0), ("core:global", 1), ("app:main", 2)]);
        let mut diagnostics = Vec::new();
        let scope = collect(2, &module(text, imports), &indexes, &mut diagnostics);
        assert!(diagnostics.is_empty());
        assert_eq!(
            scope.get("main"),
            Some(Binding::Function(DefId {
                module: 2,
                index: 0
            }))
        );
        assert_eq!(
            scope.get("later"),
            Some(Binding::Function(DefId {
                module: 2,
                index: 1
            }))
        );
        assert_eq!(
            scope.get("Limit"),
            Some(Binding::Constant(DefId {
                module: 2,
                index: 2
            }))
        );
        assert_eq!(
            scope.get("Point"),
            Some(Binding::Type(DefId {
                module: 2,
                index: 3
            }))
        );
        assert_eq!(scope.get("utils"), Some(Binding::Module(0)));
        assert_eq!(scope.get("length"), None, "a method is not a module name");
        assert_eq!(scope.get("global"), None, "the prelude is not copied in");
    }

    #[test]
    fn a_name_declared_twice_keeps_its_first_meaning() {
        let text =
            "fn utils() {}\nconst A: i32 = 1;\nfn A() {}\nconst _: i32 = 2;\nconst _: i32 = 3;\n";
        let imports = vec![project::Import {
            path: "app:utils".to_string(),
            alias: Some("utils".to_string()),
            span: source::Span::new(0, 0),
        }];
        let indexes = HashMap::from([("app:utils", 0), ("app:main", 1)]);
        let mut diagnostics = Vec::new();
        let scope = collect(1, &module(text, imports), &indexes, &mut diagnostics);
        assert_eq!(
            scope.get("A"),
            Some(Binding::Constant(DefId {
                module: 1,
                index: 1
            }))
        );
        assert_eq!(scope.get("utils"), Some(Binding::Module(0)));
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
