//! Semantic analysis: what each name means, the type of each expression and
//! the value of each constant, built into THIR in one walk per body
//! (docs/ir-design.md). It starts with each module's scope.

pub mod builtin;
pub mod declaration;
mod expression;
pub mod scope;
mod statement;
#[cfg(test)]
mod testing;

use std::sync::Arc;

use diagnostics::Code;
use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use model::DefId;
use model::ModuleId;
use model::ast;
use model::constant::Constant;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use scope::Scope;
use source::Span;

use model::Symbol;

/// Checks a program's declarations. Each module-scope declaration (`DefId`)
/// is checked once, the first time anything asks for it, and its result is
/// kept in the table of its category: types, functions or constants. A body's
/// own names (`Symbol::Local`) are checked in place with the body.
pub struct Checker<'p> {
    pub modules: &'p [project::Module], // in the program's order
    /// The names each module declares and imports, by `ModuleId`, collected
    /// before anything is asked for: a body can use a name declared below it
    /// or in another module.
    scopes: Vec<Scope>,
    pub types: Types, // the program's types, built-in and declared
    /// How wide a pointer is on the target, in bits: the width of `isize` and
    /// `usize`.
    pub pointer_bits: u32,
    /// Each type declaration and alias, by its place: the type it declares
    /// (`check_type_declaration`). Another declaration's slot stays
    /// `NotStarted`, as in each table.
    pub declared_types: Vec<Vec<State<TypeId>>>,
    /// Each function, by its place: its type (`check_function`).
    pub function_types: Vec<Vec<State<TypeId>>>,
    /// Each module constant, by its place: its type and value, `None` when it
    /// had an error (`check_constant`).
    pub constants: Vec<Vec<State<Option<Constant>>>>,
    /// What checking each module reported, by `ModuleId`, in order.
    pub diagnostics: Vec<Vec<Diagnostic>>,
    pub(crate) current_module: ModuleId, // the module being checked
    /// The declarations being checked, outermost first, each with the span
    /// where its asker wrote its name (its own name, when checked for its own
    /// sake). One asked for again while it is here depends on itself.
    checking: Vec<(DefId, Span)>,
}

/// Where the check of one declaration is.
#[derive(Clone, Debug)]
pub enum State<T> {
    NotStarted, // or being checked: then it is in `Checker::checking`
    Done(T),
}

impl<'p> Checker<'p> {
    /// Nothing checked yet of `modules` but their scopes, whose errors are
    /// each module's first diagnostics. Pointers have `pointer_bits` bits.
    pub fn new(modules: &'p [project::Module], pointer_bits: u32) -> Checker<'p> {
        let mut diagnostics = vec![Vec::new(); modules.len()];
        let scopes = (0..modules.len())
            .map(|index| {
                let id = ModuleId::from_index(index);
                scope::collect(id, modules, &mut diagnostics[index])
            })
            .collect();
        // A slot for each declaration of each module, in a table of any kind.
        fn table<T: Clone>(modules: &[project::Module]) -> Vec<Vec<State<T>>> {
            modules
                .iter()
                .map(|module| vec![State::NotStarted; module.ast.top_level.len()])
                .collect()
        }
        Checker {
            modules,
            scopes,
            types: Types::new(),
            pointer_bits,
            declared_types: table(modules),
            function_types: table(modules),
            constants: table(modules),
            diagnostics,
            current_module: ModuleId::from_index(0),
            checking: Vec::new(),
        }
    }

    /// The type the type declaration or alias `definition` declares
    /// (`check_type_declaration`), named at `used_at`.
    pub fn declared_type(&mut self, definition: DefId, used_at: Span) -> TypeId {
        self.check_once(
            definition,
            used_at,
            |checker| &mut checker.declared_types,
            TypeId::INVALID,
            Checker::check_type_declaration,
        )
    }

    /// The type the name of the type declaration or alias `definition` stands
    /// for, named at `used_at`. A type declaration makes its type
    /// (`Type::Named`), so naming it checks nothing; an alias names its
    /// declared type (`Type::Alias`).
    pub fn named_type(&mut self, definition: DefId, used_at: Span) -> TypeId {
        let tree = &self.modules[definition.module.index()].ast;
        if let ast::Decl::Type(_) = tree.decls[tree.declaration(definition.index)] {
            return self.types.intern(Type::Named(definition));
        }
        let target = self.declared_type(definition, used_at);
        if target == TypeId::INVALID {
            return target; // its error was reported
        }
        self.types.intern(Type::Alias {
            definition,
            real: target,
        })
    }

    /// The type of the function or method `definition` (`check_function`),
    /// named at `used_at`.
    pub fn function_type(&mut self, definition: DefId, used_at: Span) -> TypeId {
        self.check_once(
            definition,
            used_at,
            |checker| &mut checker.function_types,
            TypeId::INVALID,
            Checker::check_function,
        )
    }

    /// The type and value of the constant `definition`, named at `used_at`;
    /// `None` when it had an error (`check_constant`).
    pub fn constant(&mut self, definition: DefId, used_at: Span) -> Option<Constant> {
        self.check_once(
            definition,
            used_at,
            |checker| &mut checker.constants,
            None,
            Checker::check_constant,
        )
    }

    /// Runs `work` with `module` as the module being checked, then goes back
    /// to the one before.
    pub fn in_module<T>(&mut self, module: ModuleId, work: impl FnOnce(&mut Self) -> T) -> T {
        let outer = std::mem::replace(&mut self.current_module, module);
        let result = work(self);
        self.current_module = outer;
        result
    }

    /// The result of `definition`, named at `used_at`, in `table`: the one
    /// already found, `on_cycle` when it is asked for while its check runs
    /// (a cycle, reported), or else what `check` finds in its module, kept
    /// for the next ask.
    fn check_once<T: Clone>(
        &mut self,
        definition: DefId,
        used_at: Span,
        table: fn(&mut Self) -> &mut Vec<Vec<State<T>>>,
        on_cycle: T,
        check: fn(&mut Self, DefId) -> T,
    ) -> T {
        let (module, index) = (definition.module.index(), definition.index as usize);
        if let State::Done(result) = &table(self)[module][index] {
            return result.clone();
        }
        let mut checking = self.checking.iter();
        if let Some(start) = checking.position(|&(running, _)| running == definition) {
            self.report_cycle(start, used_at);
            return on_cycle;
        }
        self.checking.push((definition, used_at));
        let result = self.in_module(definition.module, |checker| check(checker, definition));
        self.checking.pop();
        table(self)[module][index] = State::Done(result.clone());
        result
    }

    /// The type `ty` as a message names it (`Types::text`).
    pub(crate) fn type_text(&self, ty: TypeId) -> String {
        self.types.text(ty, |definition| {
            let (name, file) = project::declared_name(self.modules, definition);
            name.text(file)
        })
    }

    /// Reports the cycle that the declaration checked last closes by asking,
    /// at `closing`, for `checking[start]`: one place for each step, where a
    /// declaration names the next.
    fn report_cycle(&mut self, start: usize, closing: Span) {
        let modules = self.modules;
        let declaration = |definition: DefId| {
            let tree = &modules[definition.module.index()].ast;
            &tree.decls[tree.declaration(definition.index)]
        };
        let cycle = &self.checking[start..];
        let (first, _) = cycle[0];
        let (first_name, first_file) = project::declared_name(modules, first);
        let only_aliases = cycle
            .iter()
            .all(|&(asker, _)| matches!(declaration(asker), ast::Decl::Alias(_)));
        let what = match declaration(first) {
            ast::Decl::Alias(_) if only_aliases => "is an alias of itself",
            ast::Decl::Type(_) | ast::Decl::Alias(_) => "holds itself",
            ast::Decl::Const { .. } | ast::Decl::Function(_) => "depends on itself",
        };
        let first_text = first_name.text(first_file);
        let mut diagnostic = Diagnostic::new(
            diagnostics::CIRCULAR_DEPENDENCY,
            format!("`{first_text}` {what}"),
        )
        .label(
            first_file,
            first_name.span,
            format!("`{first_text}` is defined here"),
        );
        // Each declaration of the cycle asks for the next, the last for the
        // first again.
        let asked = cycle[1..].iter().copied().chain([(first, closing)]);
        for (step, (&(asker, _), (asked, span))) in cycle.iter().zip(asked).enumerate() {
            let verb = match declaration(asker) {
                ast::Decl::Type(_) => "holds",
                ast::Decl::Alias(_) => "is an alias of",
                ast::Decl::Const { .. } | ast::Decl::Function(_) => "uses",
            };
            let (asker_name, file) = project::declared_name(modules, asker);
            let (asked_name, asked_file) = project::declared_name(modules, asked);
            let asker_text = asker_name.text(file);
            let asked_text = asked_name.text(asked_file);
            let closes = if step + 1 == cycle.len() {
                ", closing the cycle"
            } else {
                ""
            };
            let message = format!("`{asker_text}` {verb} `{asked_text}`{closes}");
            diagnostic = diagnostic.label(file, span, message);
        }
        // `A + A` closes the same cycle twice: only the closing place differs.
        let labels = &diagnostic.labels;
        let same_path = |earlier: &Diagnostic| {
            earlier.code == diagnostics::CIRCULAR_DEPENDENCY
                && earlier.labels.len() == labels.len()
                && (earlier.labels.iter().zip(labels).rev().skip(1))
                    .all(|(a, b)| a.span == b.span && Arc::ptr_eq(&a.file, &b.file))
        };
        let found = &mut self.diagnostics[first.module.index()];
        if !found.iter().any(same_path) {
            found.push(diagnostic);
        }
    }

    /// The module being checked.
    pub(crate) fn module(&self) -> &'p project::Module {
        &self.modules[self.current_module.index()]
    }

    /// The names the module being checked declares and imports.
    pub(crate) fn scope(&self) -> &Scope {
        &self.scopes[self.current_module.index()]
    }

    /// Adds `diagnostic` to what the module being checked reported.
    fn report(&mut self, diagnostic: Diagnostic) {
        self.diagnostics[self.current_module.index()].push(diagnostic);
    }

    /// Reports the error `message` at `span` of the module, and gives the type
    /// of an error already reported, so nothing more is reported about it.
    fn error(&mut self, code: Code, message: impl Into<String>, span: Span) -> TypeId {
        let diagnostic = Diagnostic::new(code, message).label(&self.module().ast.file, span, "");
        self.report(diagnostic);
        TypeId::INVALID
    }

    /// What `name` stands for at the top level: in the module's scope, else
    /// an exported name of the prelude.
    fn global(&self, name: &str) -> Option<Symbol> {
        let in_prelude = || {
            self.prelude()?
                .get(name)
                .filter(|_| scope::is_exported(name))
        };
        self.scope().get(name).or_else(in_prelude)
    }

    /// The names the module uses unqualified, with what each stands for: its
    /// scope's, then the prelude's exported ones.
    pub(crate) fn visible_symbols(&self) -> impl Iterator<Item = (&str, Symbol)> {
        let prelude = self.prelude().into_iter().flat_map(Scope::symbols);
        let exported = prelude.filter(|&(name, _)| scope::is_exported(name));
        self.scope().symbols().chain(exported)
    }

    /// The prelude's scope; `None` while checking the prelude itself.
    fn prelude(&self) -> Option<&Scope> {
        Some(&self.scopes[self.scope().parent?.index()])
    }

    /// Reports the error `message` about the name at `span`, which names
    /// nothing, with a guess that it meant `meant`.
    pub(crate) fn report_unknown(
        &mut self,
        code: Code,
        span: Span,
        message: String,
        meant: Option<String>,
    ) {
        let file = &self.module().ast.file;
        let mut diagnostic = Diagnostic::new(code, message).label(file, span, "");
        if let Some(meant) = meant {
            let text = format!("did you mean `{meant}`?");
            let fix = Fix {
                file: file.clone(),
                span,
                new_text: meant,
            };
            diagnostic = diagnostic.guess(text, vec![fix]);
        }
        self.report(diagnostic);
    }

    /// Reports syntax the checker does not handle yet, such as `what` =
    /// "reference types".
    fn not_supported(&mut self, span: Span, what: &str) -> TypeId {
        let message = format!("{what} are not supported yet");
        self.error(diagnostics::NOT_SUPPORTED_YET, message, span)
    }
}
