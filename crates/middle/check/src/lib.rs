//! Semantic analysis: what each name means, the type of each expression and
//! the value of each constant, built into THIR in one walk per body
//! (docs/ir-design.md). It starts with each module's scope.

mod builder;
pub mod builtin;
pub mod declaration;
mod expression;
mod function;
pub mod scope;
mod statement;
#[cfg(test)]
mod testing;

use std::sync::Arc;

use diagnostics::Code;
use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use function::Built;
use model::DefId;
use model::ModuleId;
use model::ast;
use model::constant::Constant;
use model::machine::Machine;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir::Fact;
use model::thir::FactKind;
use model::thir::Symbol;
use scope::Scope;
use source::Span;

/// Checks a program's declarations. Each module-scope declaration (`DefId`)
/// is checked once, the first time anything asks for it, and its result is
/// kept in the table of its category: types, functions or constants. A body's
/// own names (`Symbol::Local`) are checked in place with the body.
pub struct Checker<'p> {
    pub modules: &'p [project::Module], // in the program's order
    /// What checking each module found so far, by `ModuleId`.
    pub module_checks: Vec<ModuleChecks>,
    pub types: Types, // the program's types, built-in and declared
    /// The machine the program is for: the width of `isize` and `usize` is
    /// its `pointer_bits`, and a value may take its `largest_object` bytes.
    pub target: &'static Machine,
    /// Whether the facts found are recorded (`ModuleChecks::facts`), as for
    /// an editor.
    record_facts: bool,
    /// The declarations being checked, outermost first: the innermost's
    /// module is the module being checked (`current_module`). One asked for
    /// again while it is here depends on itself.
    checking: Vec<Checking>,
}

/// A declaration being checked (`Checker::checking`).
struct Checking {
    definition: DefId,
    /// Where its asker wrote its name (its own name, when checked for its
    /// own sake).
    used_at: Span,
    /// Whether it is a function's body built only to run at compile time
    /// (`evaluate_call`). Its facts are not recorded (`Checker::record`): the
    /// compile builds the body again and records them with the THIR that
    /// explains its locals. A declaration it checks is checked once, in an
    /// entry of its own, which records its facts.
    to_run: bool,
}

/// What checking one module found so far (`Checker::module_checks`). A
/// table of declarations holds a slot for each top-level declaration, by
/// its place; a slot whose declaration's kind it does not hold stays
/// `NotStarted`.
pub struct ModuleChecks {
    /// The names the module declares and imports, collected before anything
    /// is asked for: a body can use a name declared below it or in another
    /// module.
    scope: Scope,
    /// Each type declaration, alias and function: the type it declares
    /// (`check_type_declaration`), or the function's type (`check_function`).
    pub declared_types: Vec<State<TypeId>>,
    /// Each module constant: its type and value, `None` when it had an error
    /// (`check_constant`).
    pub constants: Vec<State<Option<Constant>>>,
    /// Each function: how far its body is built (`build_function`).
    bodies: Vec<Built>,
    /// The modules whose functions its constants may run at compile time
    /// (`evaluate_call`), so a constant's value depends on their bodies,
    /// which no interface holds.
    pub ran_modules: Vec<ModuleId>,
    /// The facts found in it (`Checker::record`), when they are recorded.
    /// Declarations are checked as they are asked for, in any order, so the
    /// facts wait here until the compile hands them over.
    pub facts: Vec<Fact>,
    /// What checking it reported, in order.
    pub diagnostics: Vec<Diagnostic>,
}

/// Where the check of one declaration is.
#[derive(Clone, Debug)]
pub enum State<T> {
    NotStarted, // or being checked: then it is in `Checker::checking`
    Done(T),
}

impl<'p> Checker<'p> {
    /// Nothing checked yet of `modules` but their scopes, whose errors are
    /// each module's first diagnostics, for `target`.
    /// Facts are recorded when `record_facts`, as for an editor.
    pub fn new(
        modules: &'p [project::Module],
        target: &'static Machine,
        record_facts: bool,
    ) -> Checker<'p> {
        let module_checks = (0..modules.len())
            .map(|index| {
                let mut diagnostics = Vec::new();
                let id = ModuleId::from_index(index);
                let scope = scope::collect(id, modules, &mut diagnostics);
                let declarations = modules[index].ast.top_level.len();
                ModuleChecks {
                    scope,
                    declared_types: vec![State::NotStarted; declarations],
                    constants: vec![State::NotStarted; declarations],
                    bodies: (0..declarations).map(|_| Built::NotYet).collect(),
                    ran_modules: Vec::new(),
                    facts: Vec::new(),
                    diagnostics,
                }
            })
            .collect();
        Checker {
            modules,
            module_checks,
            types: Types::default(),
            target,
            record_facts,
            checking: Vec::new(),
        }
    }

    /// The type the type declaration or alias `definition` declares
    /// (`check_type_declaration`), named at `used_at`.
    pub fn declared_type(&mut self, definition: DefId, used_at: Span) -> TypeId {
        self.check_once(
            definition,
            used_at,
            |module| &mut module.declared_types,
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
            |module| &mut module.declared_types,
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
            |module| &mut module.constants,
            None,
            Checker::check_constant,
        )
    }

    /// The result of `definition`, named at `used_at`, in `table`: the one
    /// already found, `on_cycle` when it is asked for while its check runs
    /// (a cycle, reported), or else what `check` finds in its module, kept
    /// for the next ask, after its attributes are checked.
    fn check_once<T: Clone>(
        &mut self,
        definition: DefId,
        used_at: Span,
        table: fn(&mut ModuleChecks) -> &mut Vec<State<T>>,
        on_cycle: T,
        check: fn(&mut Self, DefId) -> T,
    ) -> T {
        let (module, index) = (definition.module.index(), definition.index as usize);
        if let State::Done(result) = &table(&mut self.module_checks[module])[index] {
            return result.clone();
        }
        let mut checking = self.checking.iter();
        if let Some(start) = checking.position(|checking| checking.definition == definition) {
            self.report_cycle(start, used_at);
            return on_cycle;
        }
        self.checking.push(Checking {
            definition,
            used_at,
            to_run: false,
        });
        // A long chain of declarations, each asking for the next, is a deep
        // recursion here.
        const RED_ZONE: usize = 128 * 1024;
        const SEGMENT: usize = 1024 * 1024;
        let result = stacker::maybe_grow(RED_ZONE, SEGMENT, || {
            self.check_attributes(definition);
            check(self, definition)
        });
        self.checking.pop();
        table(&mut self.module_checks[module])[index] = State::Done(result.clone());
        result
    }

    /// `usize`, as wide as a pointer of the target.
    pub(crate) fn usize_type(&mut self) -> TypeId {
        let usize = self.types.builtin("usize", self.target.pointer_bits);
        usize.expect("`usize` is built in")
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
        let first = cycle[0].definition;
        let (first_name, first_file) = project::declared_name(modules, first);
        let only_aliases = (cycle.iter())
            .all(|asker| matches!(declaration(asker.definition), ast::Decl::Alias(_)));
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
        let asked = (cycle[1..].iter())
            .map(|asked| (asked.definition, asked.used_at))
            .chain([(first, closing)]);
        for (step, (asker, (asked, span))) in cycle.iter().zip(asked).enumerate() {
            let asker = asker.definition;
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
        let found = &mut self.module_checks[first.module.index()].diagnostics;
        if !found.iter().any(same_path) {
            found.push(diagnostic);
        }
    }

    /// The module being checked: the innermost declaration's (`checking`).
    pub(crate) fn current_module(&self) -> ModuleId {
        let checking = self.checking.last();
        checking
            .expect("a declaration is being checked")
            .definition
            .module
    }

    /// The module being checked.
    pub(crate) fn module(&self) -> &'p project::Module {
        &self.modules[self.current_module().index()]
    }

    /// The names the module being checked declares and imports.
    pub(crate) fn scope(&self) -> &Scope {
        &self.module_checks[self.current_module().index()].scope
    }

    /// Adds `diagnostic` to what the module being checked reported.
    fn report(&mut self, diagnostic: Diagnostic) {
        let module = self.current_module().index();
        self.module_checks[module].diagnostics.push(diagnostic);
    }

    /// Reports the error `message` at `span` of the module, and gives the type
    /// of an error already reported, so nothing more is reported about it.
    fn error(&mut self, code: Code, message: impl Into<String>, span: Span) -> TypeId {
        let diagnostic = Diagnostic::new(code, message).label(&self.module().ast.file, span, "");
        self.report(diagnostic);
        TypeId::INVALID
    }

    /// Records the fact `kind` about the text at `span` in the module being
    /// checked, when facts are recorded (`new`), unless it is in a body
    /// built only to run (`Checking::to_run`).
    pub(crate) fn record(&mut self, span: Span, kind: FactKind) {
        let Some(checking) = self.checking.last().filter(|_| self.record_facts) else {
            return;
        };
        if !checking.to_run {
            let module = checking.definition.module.index();
            self.module_checks[module].facts.push(Fact { span, kind });
        }
    }

    /// What `name` stands for at the top level: a built-in type, which no
    /// declaration hides; else a name in the module's scope; else an
    /// exported name of the prelude.
    pub(crate) fn global(&mut self, name: &str) -> Option<Symbol> {
        if let Some(builtin) = self.types.builtin(name, self.target.pointer_bits) {
            return Some(Symbol::Builtin(builtin));
        }
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
        Some(&self.module_checks[self.scope().parent?.index()].scope)
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
