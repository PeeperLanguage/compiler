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

use arena::Id;
use diagnostics::Code;
use diagnostics::Diagnostic;
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
    /// Each type declaration, by its place: the type written after its name
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
}

/// Where the check of one declaration is.
#[derive(Clone, Debug)]
pub enum State<T> {
    NotStarted,
    /// Being checked: asked for again now, it depends on itself.
    Running,
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
        }
    }

    /// The type written after the name of the type declaration or alias
    /// `definition` (`check_type_declaration`).
    pub fn declared_type(&mut self, definition: DefId) -> TypeId {
        self.check_once(
            definition,
            |checker| &mut checker.declared_types,
            TypeId::INVALID,
            Checker::check_type_declaration,
        )
    }

    /// The type the name of the type declaration or alias `definition` stands
    /// for. A type declaration makes its type (`Type::Named`), so naming it
    /// checks nothing; an alias names its declared type (`Type::Alias`).
    pub fn named_type(&mut self, definition: DefId) -> TypeId {
        let tree = &self.modules[definition.module.index()].ast;
        if let ast::Decl::Type(_) = tree.decls[tree.declaration(definition.index)] {
            return self.types.intern(Type::Named(definition));
        }
        let target = self.declared_type(definition);
        if target == TypeId::INVALID {
            return target; // its error was reported
        }
        self.types.intern(Type::Alias {
            definition,
            real: target,
        })
    }

    /// The type of the function or method `definition` (`check_function`).
    pub fn function_type(&mut self, definition: DefId) -> TypeId {
        self.check_once(
            definition,
            |checker| &mut checker.function_types,
            TypeId::INVALID,
            Checker::check_function,
        )
    }

    /// The type and value of the constant `definition`; `None` when it had
    /// an error (`check_constant`).
    pub fn constant(&mut self, definition: DefId) -> Option<Constant> {
        self.check_once(
            definition,
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

    /// The result of `definition` in `table`: the one already found,
    /// `on_cycle` when it is asked for while its check runs (a cycle,
    /// reported), or else what `check` finds in its module, kept for the next
    /// ask.
    fn check_once<T: Clone>(
        &mut self,
        definition: DefId,
        table: fn(&mut Self) -> &mut Vec<Vec<State<T>>>,
        on_cycle: T,
        check: fn(&mut Self, Id<ast::Decl>) -> T,
    ) -> T {
        let (module, index) = (definition.module.index(), definition.index as usize);
        // Cloned, so no borrow is held while the check runs and asks others.
        let state = table(self)[module][index].clone();
        match state {
            State::Done(result) => return result,
            State::Running => {
                self.report_cycle(definition);
                return on_cycle;
            }
            State::NotStarted => {}
        }
        table(self)[module][index] = State::Running;
        let declaration = self.modules[module].ast.declaration(definition.index);
        let result = self.in_module(definition.module, |checker| check(checker, declaration));
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

    /// Reports that the declaration `definition` was asked for while it was
    /// being checked: it depends on itself.
    fn report_cycle(&mut self, definition: DefId) {
        let (name, file) = project::declared_name(self.modules, definition);
        let text = name.text(file);
        let found = &mut self.diagnostics[definition.module.index()];
        // `A + A` asks for `A` twice while it runs; the cycle is reported once.
        let reported = |diagnostic: &Diagnostic| {
            diagnostic.code == diagnostics::CIRCULAR_DEPENDENCY
                && diagnostic.labels[0].span == name.span
        };
        if found.iter().any(reported) {
            return;
        }
        found.push(
            Diagnostic::new(
                diagnostics::CIRCULAR_DEPENDENCY,
                format!("`{text}` depends on itself"),
            )
            .label(file, name.span, ""),
        );
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
    /// in its parent's, and so on out.
    fn global(&self, name: &str) -> Option<Symbol> {
        let mut scope = self.scope();
        loop {
            if let Some(symbol) = scope.get(name) {
                return Some(symbol);
            }
            scope = &self.scopes[scope.parent?.index()];
        }
    }

    /// Reports syntax the checker does not handle yet, such as `what` =
    /// "reference types".
    fn not_supported(&mut self, span: Span, what: &str) -> TypeId {
        let message = format!("{what} are not supported yet");
        self.error(diagnostics::NOT_SUPPORTED_YET, message, span)
    }
}
