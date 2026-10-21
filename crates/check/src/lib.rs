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
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use project::Scope;
use source::Span;

use model::Symbol;

/// What checking a program's module-scope declarations (`DefId`) found, each
/// checked once, the first time anything asks: one method per kind, as
/// `Symbol` names them. A body's own names (`Symbol::Local`) are checked in
/// place with the body, never asked for.
pub struct Queries<'p> {
    pub modules: &'p [project::Module], // in the program's order, with their scopes
    pub types: Types,                   // the program's types, shared by every declaration
    /// How wide a pointer is on the target, in bits: the width of `isize` and
    /// `usize`.
    pub pointer_bits: u32,
    /// The query of each function and method, by its place (`DefId`): its
    /// type. Another declaration's slot stays `NotStarted`.
    pub functions: Vec<Vec<State<TypeId>>>,
    /// The query of each module constant, by its place: its type and value,
    /// `None` when it had an error. Another declaration's slot stays
    /// `NotStarted`.
    pub constants: Vec<Vec<State<Option<Constant>>>>,
    /// What checking each module reported, by `ModuleId`, in order.
    pub diagnostics: Vec<Vec<Diagnostic>>,
}

/// Where the query of one declaration is.
#[derive(Clone, Debug)]
pub enum State<T> {
    NotStarted,
    /// Asked for again while running: a cycle.
    Running,
    Done(T),
}

impl<'p> Queries<'p> {
    /// Nothing checked yet of `modules`, whose scopes are set. Pointers have
    /// `pointer_bits` bits.
    pub fn new(modules: &'p [project::Module], pointer_bits: u32) -> Queries<'p> {
        Queries {
            modules,
            types: Types::new(),
            pointer_bits,
            functions: modules
                .iter()
                .map(|module| vec![State::NotStarted; module.ast.declarations.len()])
                .collect(),
            constants: modules
                .iter()
                .map(|module| vec![State::NotStarted; module.ast.declarations.len()])
                .collect(),
            diagnostics: vec![Vec::new(); modules.len()],
        }
    }

    /// The function type of the function or method `definition`
    /// (`ModuleChecker::function`).
    pub fn function(&mut self, definition: DefId) -> TypeId {
        self.query(
            definition,
            |queries| &mut queries.functions,
            TypeId::INVALID,
            |checker, declaration| checker.function(declaration),
        )
    }

    /// The type and value of the constant `definition`; `None` when it had
    /// an error (`ModuleChecker::constant`).
    pub fn constant(&mut self, definition: DefId) -> Option<Constant> {
        self.query(
            definition,
            |queries| &mut queries.constants,
            None,
            |checker, declaration| checker.constant(declaration),
        )
    }

    /// Runs `work` on a checker of `module`; what it reports joins that
    /// module's diagnostics.
    pub fn with_checker<T>(
        &mut self,
        module: ModuleId,
        work: impl FnOnce(&mut ModuleChecker<'_, 'p>) -> T,
    ) -> T {
        let mut diagnostics = Vec::new();
        let mut checker = ModuleChecker {
            current_module: module,
            queries: self,
            diagnostics: &mut diagnostics,
        };
        let result = work(&mut checker);
        self.diagnostics[module.index()].append(&mut diagnostics);
        result
    }

    /// The result of the query of `definition`: the result already found,
    /// `on_cycle` when the query is asked for while it runs (a cycle,
    /// reported), or else what `work` finds with a checker of its module,
    /// kept for the next ask.
    fn query<T: Clone>(
        &mut self,
        definition: DefId,
        table: fn(&mut Self) -> &mut Vec<Vec<State<T>>>,
        on_cycle: T,
        work: impl FnOnce(&mut ModuleChecker<'_, 'p>, Id<ast::Decl>) -> T,
    ) -> T {
        let (module, index) = (definition.module.index(), definition.index as usize);
        // Cloned, so no borrow is held while the query runs and asks others.
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
        let result = self.with_checker(definition.module, |checker| work(checker, declaration));
        table(self)[module][index] = State::Done(result.clone());
        result
    }

    /// Reports that the declaration `definition` was asked for while it was
    /// being checked: it depends on itself.
    fn report_cycle(&mut self, definition: DefId) {
        let tree = &self.modules[definition.module.index()].ast;
        let name = tree.decls[tree.declaration(definition.index)].name();
        let text = name.text(&tree.file);
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
            .label(&tree.file, name.span, ""),
        );
    }
}

/// Checking the declarations of one module: their headers and the names
/// they use. Code inside them is built by a `ThirBuilder`.
pub struct ModuleChecker<'a, 'p> {
    pub current_module: ModuleId, // the module being checked
    pub queries: &'a mut Queries<'p>,
    pub diagnostics: &'a mut Vec<Diagnostic>,
}

impl<'p> ModuleChecker<'_, 'p> {
    /// The module being checked.
    pub fn module(&self) -> &'p project::Module {
        &self.queries.modules[self.current_module.index()]
    }

    /// Where a name the module does not declare is looked up next:
    /// `core:global`, unless the module is it or the program has none.
    fn prelude(&self) -> Option<ModuleId> {
        self.scope().parent
    }

    /// The names the module being checked declares and imports.
    pub fn scope(&self) -> &'p Scope {
        self.module().scope()
    }

    /// Reports the error `message` at `span` of the module, and gives the type
    /// of an error already reported, so nothing more is reported about it.
    fn error(&mut self, code: Code, message: impl Into<String>, span: Span) -> TypeId {
        self.diagnostics.push(Diagnostic::new(code, message).label(
            &self.module().ast.file,
            span,
            "",
        ));
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
            scope = self.queries.modules[scope.parent?.index()].scope();
        }
    }

    /// Reports syntax the checker does not handle yet, such as `what` =
    /// "reference types".
    fn not_supported(&mut self, span: Span, what: &str) -> TypeId {
        let message = format!("{what} are not supported yet");
        self.error(diagnostics::NOT_SUPPORTED_YET, message, span)
    }
}
