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
use scope::Scope;
use source::Span;

use model::Symbol;

/// What checking a program's module-scope declarations (`DefId`) found, each
/// checked once, the first time anything asks: one method per kind, as
/// `Symbol` names them. A body's own names (`Symbol::Local`) are checked in
/// place with the body, never asked for.
pub struct Queries<'p> {
    pub modules: &'p [project::Module], // in the program's order
    /// The names each module declares and imports, by `ModuleId`, collected
    /// before anything is asked for: a body can use a name declared below it
    /// or in another module.
    scopes: Vec<Scope>,
    pub types: Types, // the program's types, shared by every declaration
    /// How wide a pointer is on the target, in bits: the width of `isize` and
    /// `usize`.
    pub pointer_bits: u32,
    /// The query of each function, method and type declaration, by its place
    /// (`DefId`): a function's type, or the type a type declaration names.
    /// Another declaration's slot stays `NotStarted`.
    pub declared_types: Vec<Vec<State<TypeId>>>,
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
    /// Nothing checked yet of `modules` but their scopes, whose errors are
    /// each module's first diagnostics. Pointers have `pointer_bits` bits.
    pub fn new(modules: &'p [project::Module], pointer_bits: u32) -> Queries<'p> {
        let mut diagnostics = vec![Vec::new(); modules.len()];
        let scopes = (0..modules.len())
            .map(|index| {
                let id = ModuleId::from_index(index);
                scope::collect(id, modules, &mut diagnostics[index])
            })
            .collect();
        Queries {
            modules,
            scopes,
            types: Types::new(),
            pointer_bits,
            declared_types: modules
                .iter()
                .map(|module| vec![State::NotStarted; module.ast.top_level.len()])
                .collect(),
            constants: modules
                .iter()
                .map(|module| vec![State::NotStarted; module.ast.top_level.len()])
                .collect(),
            diagnostics,
        }
    }

    /// The function type of the function or method `definition`
    /// (`ModuleChecker::check_signature`).
    pub fn function(&mut self, definition: DefId) -> TypeId {
        self.query(
            definition,
            |queries| &mut queries.declared_types,
            TypeId::INVALID,
            |checker, declaration| checker.check_signature(declaration),
        )
    }

    /// The type the type declaration or alias `definition` names
    /// (`ModuleChecker::check_type_declaration`).
    pub fn named_type(&mut self, definition: DefId) -> TypeId {
        self.query(
            definition,
            |queries| &mut queries.declared_types,
            TypeId::INVALID,
            |checker, _| checker.check_type_declaration(definition),
        )
    }

    /// The type and value of the constant `definition`; `None` when it had
    /// an error (`ModuleChecker::check_constant`).
    pub fn constant(&mut self, definition: DefId) -> Option<Constant> {
        self.query(
            definition,
            |queries| &mut queries.constants,
            None,
            |checker, declaration| checker.check_constant(declaration),
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

    /// The type `ty` as a message names it: an alias by its name, with the
    /// type itself (`Types::text_named`).
    pub fn type_text(&self, ty: TypeId) -> String {
        self.types.text_named(ty, |definition| {
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

    /// The names the module being checked declares and imports.
    pub fn scope(&self) -> &Scope {
        &self.queries.scopes[self.current_module.index()]
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
            scope = &self.queries.scopes[scope.parent?.index()];
        }
    }

    /// Reports syntax the checker does not handle yet, such as `what` =
    /// "reference types".
    fn not_supported(&mut self, span: Span, what: &str) -> TypeId {
        let message = format!("{what} are not supported yet");
        self.error(diagnostics::NOT_SUPPORTED_YET, message, span)
    }
}
