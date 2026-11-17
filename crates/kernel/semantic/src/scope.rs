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
use model::program::ImportKind;
use model::thir::Symbol;
use rustc_hash::FxHashMap;
use source::SourceFile;
use source::Span;
use syntax::ast;

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

    /// The type this module declares as `name`, which can have functions
    /// (`fn Point::name`, `has_functions`); `None` for any other name. A type
    /// here is always this module's: imports bring in modules, and the
    /// prelude is not copied in.
    pub(crate) fn owner_named(
        &self,
        modules: &[model::program::Module],
        name: &str,
    ) -> Option<DefId> {
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

/// Whether the declaration `definition` can have functions
/// (`fn Point::name`): any type it declares, not an alias, which names a
/// type declared elsewhere.
pub(crate) fn has_functions(modules: &[model::program::Module], definition: DefId) -> bool {
    matches!(
        model::program::declaration(modules, definition),
        ast::Decl::Type(_)
    )
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
pub fn report_redeclared(
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
/// type must be one this module declares, so it has the same functions
/// everywhere.
pub fn collect<'p>(
    id: ModuleId,
    modules: &'p [model::program::Module],
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
        let Some(owner_type) = scope.owner_named(modules, owner.text(file)) else {
            let message = format!(
                "`{}` is not a type declared in this module",
                owner.text(file)
            );
            diagnostics.push(
                Diagnostic::new(diagnostics::INVALID_DECLARATION, message)
                    .label(file, owner.span, "")
                    .note("a type's functions are declared in the module of the type"),
            );
            continue;
        };
        // An interface's own methods are its names too: a call through it
        // must mean one.
        let text = name.text(file);
        let methods = model::program::interface_methods(modules, owner_type).into_iter();
        if let Some(method) = methods
            .flatten()
            .find(|method| method.name.text(file) == text)
        {
            let diagnostic = report_redeclared(file, text, "type", name.span, method.name.span);
            diagnostics.push(diagnostic);
            continue;
        }
        let symbol = Symbol::Function(definition(index));
        diagnostics.extend(declare(
            &mut scope,
            Some(owner_type),
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
    use model::program::Import;
    use model::program::ImportKind;

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
