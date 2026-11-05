//! A module's scope: the names its top-level declarations and imports bring
//! in, the functions of its types (`fn Point::name`) among them. It is
//! collected from the declarations alone, before any body is checked, so a
//! body can use a name declared below it or in another module. Names of the
//! prelude (`core:global`) are not copied in; a lookup that misses here goes
//! on to the prelude's exported names.

use std::sync::Arc;

use diagnostics::Diagnostic;
use diagnostics::suggest::nearest_name;
use model::DefId;
use model::ModuleId;
use model::ast;
use model::thir::Symbol;
use project::ImportKind;
use rustc_hash::FxHashMap;
use source::SourceFile;
use source::Span;

/// A place that holds declarations, and where each name is written. A module
/// has one for its top-level declarations, imports and type functions. A name
/// it does not hold is looked up next among its parent's exported names.
pub struct Scope<'p> {
    /// The module whose scope comes next: the prelude (`core:global`), for
    /// every module but the prelude.
    pub parent: Option<ModuleId>,
    /// Each name, keyed by its owner and its text: no owner for a top-level
    /// name, the type's declaration for `fn Point::name`. The text is the
    /// program's own, so a lookup builds no string. Fx hashing: every name a
    /// body uses that is not a local is looked up here, and the default hash
    /// costs about 1% of a compile.
    names: FxHashMap<(Option<DefId>, &'p str), (Symbol, Span)>,
}

impl<'p> Scope<'p> {
    pub fn new(parent: Option<ModuleId>) -> Scope<'p> {
        Scope {
            parent,
            names: FxHashMap::default(),
        }
    }

    /// What `name` of `owner` stands for in this scope alone.
    pub fn get(&self, owner: Option<DefId>, name: &str) -> Option<Symbol> {
        self.names.get(&(owner, name)).map(|&(symbol, _)| symbol)
    }

    /// The struct this module declares as `name`, the only type that can
    /// have functions (`fn Point::name`); `None` for any other name. A type
    /// here is always this module's: imports bring in modules, and the
    /// prelude is not copied in.
    pub(crate) fn struct_named(&self, modules: &[project::Module], name: &str) -> Option<DefId> {
        match self.get(None, name) {
            Some(Symbol::Type(definition)) if has_functions(modules, definition) => {
                Some(definition)
            }
            _ => None,
        }
    }

    /// Every name of `owner` with what it stands for, in no order.
    pub fn symbols(&self, owner: Option<DefId>) -> impl Iterator<Item = (&'p str, Symbol)> {
        (self.names.iter())
            .filter(move |((of, _), _)| *of == owner)
            .map(|(&(_, name), &(symbol, _))| (name, symbol))
    }

    /// Brings `name` of `owner` in as `symbol`, written at `span`. A name
    /// already in keeps its first meaning, and gives where that was written.
    pub fn declare(
        &mut self,
        owner: Option<DefId>,
        name: &'p str,
        symbol: Symbol,
        span: Span,
    ) -> Result<(), Span> {
        if let Some(&(_, first)) = self.names.get(&(owner, name)) {
            Err(first)
        } else {
            self.names.insert((owner, name), (symbol, span));
            Ok(())
        }
    }
}

/// Whether the type declaration `definition` can have functions
/// (`fn Point::name`): only a struct can.
pub(crate) fn has_functions(modules: &[project::Module], definition: DefId) -> bool {
    let tree = &modules[definition.module.index()].ast;
    match project::declaration(modules, definition) {
        ast::Decl::Type(declaration) => matches!(tree.types[declaration.ty], ast::Type::Struct(_)),
        _ => false,
    }
}

/// The name nearest to `name` (`nearest_name`): one of `first`, else one of
/// `declared`. A name of `first` wins a tie. Declared names come in hash
/// order, which cannot change the answer: two names that tie suggest nothing.
pub(crate) fn nearest<'d, 'f: 'd>(
    name: &str,
    first: impl Iterator<Item = &'f str>,
    declared: impl Iterator<Item = &'d str>,
) -> Option<String> {
    // `first` may live longer than `declared` (built-in names are
    // `'static`); each is shortened to `declared`'s lifetime so both chain.
    let candidates = first
        .map(|first| -> (&'d str, i32) { (first, 0) })
        .chain(declared.map(|declared| (declared, 1)));
    nearest_name(name, candidates).map(str::to_string)
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
/// its first meaning and is reported. A type's functions come after the
/// top-level names, since a type may be declared below its functions; the
/// type must be a struct of this module, so it has the same functions
/// everywhere.
pub fn collect<'p>(
    id: ModuleId,
    modules: &'p [project::Module],
    diagnostics: &mut Vec<Diagnostic>,
) -> Scope<'p> {
    let module = &modules[id.index()];
    let file = &module.ast.file;
    let prelude = module
        .imports
        .iter()
        .find(|import| import.kind == ImportKind::Prelude);
    let mut scope = Scope::new(prelude.map(|import| import.module));
    // The error for a name declared twice.
    let declare = |scope: &mut Scope<'p>, owner, name: &'p str, symbol, span| {
        if name.is_empty() {
            return None; // missing after a syntax error
        }
        let first = scope.declare(owner, name, symbol, span).err()?;
        let place = if owner.is_some() { "type" } else { "module" };
        Some(report_redeclared(file, name, place, span, first))
    };
    // The loader already reported aliases used twice and kept the first.
    for import in &module.imports {
        if let Some((name, span)) = import.name(module, modules) {
            diagnostics.extend(declare(
                &mut scope,
                None,
                name,
                Symbol::Module(import.module),
                span,
            ));
        }
    }
    let tree = &module.ast;
    let definition = |index: usize| DefId {
        module: id,
        index: index as u32,
    };
    for (index, declaration) in tree.declarations().enumerate() {
        let (name, symbol) = match tree.decls[declaration] {
            ast::Decl::Function(function) if function.owner.is_none() => {
                (function.name, Symbol::Function(definition(index)))
            }
            ast::Decl::Function(_) => continue, // a type's function, below
            ast::Decl::Const { name, .. } => (name, Symbol::Constant(definition(index))),
            ast::Decl::Type(declaration) | ast::Decl::Alias(declaration) => {
                (declaration.name, Symbol::Type(definition(index)))
            }
        };
        diagnostics.extend(declare(
            &mut scope,
            None,
            name.text(file),
            symbol,
            name.span,
        ));
    }
    for (index, declaration) in tree.declarations().enumerate() {
        let ast::Decl::Function(ast::Function {
            owner: Some(owner),
            name,
            ..
        }) = tree.decls[declaration]
        else {
            continue;
        };
        let Some(owner_struct) = scope.struct_named(modules, owner.text(file)) else {
            let message = format!(
                "`{}` is not a struct declared in this module",
                owner.text(file)
            );
            diagnostics.push(
                Diagnostic::new(diagnostics::INVALID_DECLARATION, message)
                    .label(file, owner.span, "")
                    .note("a type's functions are declared in the module of the type"),
            );
            continue;
        };
        let symbol = Symbol::Function(definition(index));
        diagnostics.extend(declare(
            &mut scope,
            Some(owner_struct),
            name.text(file),
            symbol,
            name.span,
        ));
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
    use model::thir::Symbol;

    /// The declaration `index` of the module at place `module`.
    fn definition(module: usize, index: u32) -> DefId {
        DefId {
            module: ModuleId::from_index(module),
            index,
        }
    }

    #[test]
    fn declarations_and_imports() {
        let text = "import \"app:utils\";\nfn main() { const K: i32 = 1; later(); }\nfn later() {}\nconst Limit: i32 = 4;\nstruct Point { x: i32 }\nfn Point::length(&self) -> i32 { return 0; }\nfn Point::origin() -> Point { return .{ x = 0 }; }\n";
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
        assert_eq!(
            scope.get(None, "main"),
            Some(Symbol::Function(definition(2, 0)))
        );
        assert_eq!(
            scope.get(None, "later"),
            Some(Symbol::Function(definition(2, 1)))
        );
        assert_eq!(
            scope.get(None, "Limit"),
            Some(Symbol::Constant(definition(2, 2)))
        );
        assert_eq!(
            scope.get(None, "Point"),
            Some(Symbol::Type(definition(2, 3)))
        );
        assert_eq!(scope.get(None, "utils"), Some(Symbol::Module(utils)));
        assert_eq!(
            scope.get(None, "length"),
            None,
            "a method is not a module name"
        );
        let point = Some(definition(2, 3));
        assert_eq!(
            scope.get(point, "origin"),
            Some(Symbol::Function(definition(2, 5)))
        );
        assert_eq!(
            scope.get(None, "origin"),
            None,
            "a type's function is not a module name"
        );
        assert_eq!(
            scope.get(None, "global"),
            None,
            "the prelude is not copied in"
        );
        assert_eq!(scope.parent, Some(global), "the prelude's scope comes next");
    }

    #[test]
    fn a_type_function_needs_a_struct_of_its_module() {
        // The owner may be declared below; anything but a struct of this
        // module is refused, and a function declared twice in a type is
        // reported; the module's own `a` is another name.
        let text = "fn Point::a() {}\nstruct Point { x: i32 }\nfn Point::a() {}\nconst Limit: i32 = 1;\nfn Limit::b() {}\nfn Missing::c() {}\ntype Other = Point;\nfn Other::d() {}\nfn i32::e() {}\nfn a() {}\n";
        let modules = [module("app:main", text, Vec::new())];
        let mut diagnostics = Vec::new();
        let scope = collect(ModuleId::from_index(0), &modules, &mut diagnostics);
        let point = Some(definition(0, 1));
        assert_eq!(
            scope.get(point, "a"),
            Some(Symbol::Function(definition(0, 0)))
        );
        assert_eq!(
            scope.get(None, "a"),
            Some(Symbol::Function(definition(0, 9)))
        );
        let messages: Vec<&str> = diagnostics.iter().map(|d| d.message.as_str()).collect();
        assert_eq!(
            messages,
            [
                "`a` is already declared in this type",
                "`Limit` is not a struct declared in this module",
                "`Missing` is not a struct declared in this module",
                "`Other` is not a struct declared in this module",
                "`i32` is not a struct declared in this module",
            ]
        );
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
        assert_eq!(
            scope.get(None, "A"),
            Some(Symbol::Constant(definition(1, 1)))
        );
        assert_eq!(scope.get(None, "utils"), Some(Symbol::Module(utils)));
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
