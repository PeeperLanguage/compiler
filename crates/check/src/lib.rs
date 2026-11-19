//! Semantic analysis: what each name means, the type of each expression and
//! the value of each constant, built into THIR in one walk per body
//! (docs/ir-design.md). It starts with each module's scope.

mod builder;
mod conformance;
mod declaration;
mod expression;
pub mod fold;
mod functions;
pub mod generics;
mod locals;
pub mod scope;
mod statement;

// One file per concept, named as the parser's file that reads it.
mod arrays;
mod chars;
mod enums;
mod interfaces;
mod optionals;
mod pipes;
mod printing;
mod ranges;
mod raw_pointers;
mod structs;
/// Why a call of `callee` with `given` arguments is wrong, when it takes
/// `fixed` of them, or at least that many when `variadic`.
pub(crate) fn argument_count_message(
    callee: &str,
    what: &str,
    fixed: usize,
    variadic: bool,
    given: usize,
) -> String {
    format!(
        "`{callee}` takes {}{fixed} {what}{}, but {given} {} given",
        if variadic { "at least " } else { "" },
        if fixed == 1 { "" } else { "s" },
        if given == 1 { "was" } else { "were" },
    )
}

#[cfg(test)]
mod testing;

use std::cell::RefCell;
use std::sync::Arc;

use arena::Id;
use diagnostics::Code;
use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use functions::bodies;
use lower::ProgramDebugTypes;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::ModuleId;
use model::machine::Machine;
use model::semantic_types::ConstArgument;
use model::semantic_types::Constant;
use model::semantic_types::ImplementationId;
use model::semantic_types::Naming;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir::Fact;
use model::thir::FactKind;
use model::thir::Symbol;
use rustc_hash::FxHashMap;
use scope::Scope;
use source::Span;
use tree::ast;

/// Checks a program's declarations. Each module-scope declaration (`DefId`)
/// is checked once, the first time anything asks for it, and its result is
/// kept in the table of its category: types, functions or constants. A body's
/// own names (`Symbol::Local`) are checked in place with the body.
pub struct Checker<'p> {
    pub modules: &'p [model::program::Module], // in the program's order
    /// What checking each module found so far, by `ModuleId`.
    pub module_checks: Vec<ModuleChecks<'p>>,
    pub types: Types, // the program's types, built-in and declared
    /// The debug type of each type the program's lowered bodies name so far
    /// (`lower::lower`); only a backend reads them.
    pub debug_types: ProgramDebugTypes,
    /// The machine the program is for: the width of `isize` and `usize` is
    /// its `pointer_bits`, and a value may take its `largest_object` bytes.
    pub target: &'static Machine,
    internals: CheckerInternals,
}

/// What checking keeps track of on its way, which no declaration of the
/// program states (`Checker::internals`).
struct CheckerInternals {
    /// Each struct and interface pair found to implement (`implementation`),
    /// so its methods are looked up once.
    known_implementations: FxHashMap<(TypeId, TypeId), ImplementationId>,
    /// Whether the facts found are recorded (`ModuleChecks::facts`), as for
    /// an editor.
    records_editor_facts: bool,
    /// The declarations being checked, outermost first: the innermost's
    /// module is the module being checked (`current_module`). One asked for
    /// again while it is here depends on itself.
    declarations_being_checked: Vec<Checking>,
    /// How far the body of each function, or each copy of one, is built
    /// (`build_function`).
    built_bodies: FxHashMap<DeclarationCopy, bodies::Built>,
    /// Each copy of a function or type with type or `const` parameters
    /// made so far (`copy_type`).
    copies: FxHashMap<DeclarationCopy, Copied>,
    /// The copies made whose bodies the compile has not built yet, in the
    /// order made; it takes them (`next_copy`).
    copies_to_build: Vec<DeclarationCopy>,
    /// The field types of each struct given an open type, which has no copy
    /// to hold them, read once (`field_types_given`).
    open_struct_fields: FxHashMap<DeclarationCopy, Box<[TypeId]>>,
    /// Whether copies nested too deep (`COPY_DEPTH_LIMIT`): then no copy's
    /// body is built any more, as a function that makes two copies inside
    /// each would make twice as many at each depth.
    copies_too_deep: bool,
}

/// A copy of a declaration with type or `const` parameters
/// (`CheckerInternals::copies`).
struct Copied {
    /// A function copy's function type, or a type copy's type; `None`
    /// while it is checked. One with an error is removed (`copy_type`).
    ty: Option<TypeId>,
    /// The copies it is made inside, each in the one before, counting
    /// itself: 1 for one a declaration given nothing makes.
    depth: u32,
    /// The module whose check made it, or the copy it is made inside: what
    /// the check of a function copy's body finds is reported there. Its own
    /// check's errors go to the use's module, as the use's.
    reported_in: ModuleId,
    /// The use it was made for, a call or a type, in the module `used_in`.
    used_in: ModuleId,
    used_at: Span,
}

/// How deep copies may be made inside each other (`Copied::depth`): a
/// function whose copy calls a copy for a new type each time would make
/// copies without end. A type cannot: one that holds a new copy of itself,
/// `struct S<T> { next: S<[2]T> }`, holds itself, found in its own check
/// (`open_struct`).
const COPY_DEPTH_LIMIT: u32 = 64;

/// A declaration being checked (`CheckerInternals::declarations_being_checked`).
struct Checking {
    definition: DefId,
    /// What its type and `const` parameters are given, when it is a
    /// function or a struct that has them: a copy's, or each type
    /// parameter itself in its own check (`own_arguments`); else
    /// `ConstArguments::NONE`.
    arguments: ConstArguments,
    /// Where its asker wrote its name (its own name, when checked for its
    /// own sake).
    used_at: Span,
    /// Whether its facts are recorded (`Checker::record`). Not those of a
    /// function's body built only to run at compile time
    /// (`evaluate_call`): the compile builds the body again and records them
    /// with the THIR that explains its locals; nor a `const` value
    /// parameter's type read again for a call (`const_value_type`), whose
    /// facts are the signature's. A declaration it checks is checked once,
    /// in an entry of its own, which records its facts.
    records_facts: bool,
}

impl Checking {
    /// The declaration, or copy of one, it is.
    fn copy(&self) -> DeclarationCopy {
        DeclarationCopy {
            definition: self.definition,
            arguments: self.arguments,
        }
    }
}

/// What checking one module found so far (`Checker::module_checks`). A
/// table of declarations holds a slot for each top-level declaration, by
/// its place; a slot whose declaration's kind it does not hold stays
/// `NotStarted`.
pub struct ModuleChecks<'p> {
    /// The names the module declares and imports, collected before anything
    /// is asked for: a body can use a name declared below it or in another
    /// module.
    scope: Scope<'p>,
    /// Each type declaration, alias and function: the type it declares
    /// (`check_type_declaration`), or the function's type (`check_function`).
    pub declared_types: Vec<State<TypeId>>,
    /// Each module constant: its type and value, `None` when it had an error
    /// (`check_constant`).
    pub constants: Vec<State<Option<Constant>>>,
    /// The modules whose functions its constants may run at compile time
    /// (`evaluate_call`), so a constant's value depends on their bodies,
    /// which no interface holds.
    pub ran_modules: Vec<ModuleId>,
    /// The facts found in it (`Checker::record`), by the declaration being
    /// checked.
    facts: FxHashMap<DefId, Vec<Fact>>,
    /// What checking it reported, in order.
    pub diagnostics: Vec<Diagnostic>,
}

/// Where the check of one declaration is.
#[derive(Clone, Debug)]
pub enum State<T> {
    NotStarted, // or being checked: then it is in `CheckerInternals::declarations_being_checked`
    Done(T),
}

impl<'p> Checker<'p> {
    /// Nothing checked yet of `modules` but their scopes, whose errors are
    /// each module's first diagnostics, for `target`.
    /// Facts are recorded when `record_facts`, as for an editor.
    pub fn new(
        modules: &'p [model::program::Module],
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
                    ran_modules: Vec::new(),
                    facts: FxHashMap::default(),
                    diagnostics,
                }
            })
            .collect();
        Checker {
            modules,
            module_checks,
            types: Types::new(target.pointer_bits),
            debug_types: ProgramDebugTypes::default(),
            target,
            internals: CheckerInternals {
                known_implementations: FxHashMap::default(),
                records_editor_facts: record_facts,
                declarations_being_checked: Vec::new(),
                built_bodies: FxHashMap::default(),
                copies: FxHashMap::default(),
                copies_to_build: Vec::new(),
                open_struct_fields: FxHashMap::default(),
                copies_too_deep: false,
            },
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

    /// The name the type declaration `definition`, being checked, gives its
    /// type: with what its copy is given, for a type with type parameters
    /// (`copy_type`).
    pub(crate) fn declared_name(&mut self, definition: DefId) -> TypeId {
        let checking = (self.internals.declarations_being_checked.last())
            .expect("a declaration is being checked");
        debug_assert_eq!(checking.definition, definition);
        let arguments = checking.arguments;
        self.types.intern(Type::Named {
            definition,
            arguments,
        })
    }

    /// The type the name of the type declaration or alias `definition` stands
    /// for, named at `used_at`. A type declaration makes its type
    /// (`Type::Named`, a struct's once checked), so naming it checks nothing;
    /// an alias names its declared type (`Type::Alias`).
    pub(crate) fn named_type(&mut self, definition: DefId, used_at: Span) -> TypeId {
        if let ast::Decl::Type(_) = model::program::declaration(self.modules, definition) {
            return self.types.intern(Type::Named {
                definition,
                arguments: ConstArguments::NONE,
            });
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
        table: for<'m> fn(&'m mut ModuleChecks<'p>) -> &'m mut Vec<State<T>>,
        on_cycle: T,
        check: fn(&mut Self, DefId) -> T,
    ) -> T {
        let (module, index) = (definition.module.index(), definition.index as usize);
        if let State::Done(result) = &table(&mut self.module_checks[module])[index] {
            return result.clone();
        }
        let mut checking = self.internals.declarations_being_checked.iter();
        if let Some(start) = checking.position(|checking| checking.definition == definition) {
            self.report_cycle(start, used_at);
            return on_cycle;
        }
        let checking = Checking {
            definition,
            arguments: self.own_arguments(definition),
            used_at,
            records_facts: true,
        };
        // A long chain of declarations, each asking for the next, is a deep
        // recursion here.
        let result = source::grow_stack(|| {
            self.checking_in(checking, |checker| {
                checker.check_attributes(definition);
                check(checker, definition)
            })
        });
        table(&mut self.module_checks[module])[index] = State::Done(result.clone());
        result
    }

    /// What `check` finds with `checking` the innermost declaration being
    /// checked (`CheckerInternals::declarations_being_checked`).
    fn checking_in<T>(&mut self, checking: Checking, check: impl FnOnce(&mut Self) -> T) -> T {
        self.internals.declarations_being_checked.push(checking);
        let result = check(self);
        self.internals.declarations_being_checked.pop();
        result
    }

    /// Whether the declaration being checked is a copy (`copy_type`). A
    /// declaration's own check (`own_arguments`) is none.
    pub(crate) fn in_copy(&self) -> bool {
        let checking = self
            .internals
            .declarations_being_checked
            .last()
            .expect("a declaration is being checked");
        self.internals.copies.contains_key(&checking.copy())
    }

    /// Whether the declaration being checked is a generic declaration's own
    /// check (`own_arguments`): neither a copy nor a declaration without
    /// type or `const` parameters.
    pub(crate) fn in_own_check(&self) -> bool {
        let checking = (self.internals.declarations_being_checked.last())
            .expect("a declaration is being checked");
        checking.arguments != ConstArguments::NONE && !self.in_copy()
    }

    /// The type `ty` as a message names it (`Types::text`).
    pub(crate) fn type_text(&self, ty: TypeId) -> String {
        model::program::type_text(self.modules, &self.types, ty)
    }

    /// Both types as a message names them, with each one's module when two
    /// different declarations in them read the same: `a::Point`, `[1]b::Point`.
    pub(crate) fn type_texts(&self, first: TypeId, second: TypeId) -> (String, String) {
        let name = |naming: Naming| model::program::named_text(self.modules, naming);
        // `Types::text` needs a closure it can copy, so this one cannot own the list.
        let written = RefCell::new(Vec::new());
        let plain = |naming: Naming| {
            if let Naming::Declaration(definition) = naming {
                written.borrow_mut().push(definition);
            }
            name(naming)
        };
        let texts = (
            self.types.text(first, plain),
            self.types.text(second, plain),
        );
        let written = written.into_inner();
        let clash = |&one: &DefId| {
            let same_name = |&other: &DefId| {
                one != other && name(Naming::Declaration(one)) == name(Naming::Declaration(other))
            };
            written.iter().any(same_name)
        };
        if !written.iter().any(clash) {
            return texts;
        }
        // A type parameter is the one of the declaration being checked.
        let with_module = |naming: Naming| match naming {
            Naming::Declaration(definition) => {
                let module = &self.modules[definition.module.index()];
                format!("{}::{}", module.path, name(naming))
            }
            Naming::Parameter { .. } => name(naming).to_owned(),
        };
        (
            self.types.text(first, with_module),
            self.types.text(second, with_module),
        )
    }

    /// Reports the cycle that the declaration checked last closes by asking,
    /// at `closing`, for `checking[start]`: one place for each step, where a
    /// declaration names the next.
    fn report_cycle(&mut self, start: usize, closing: Span) {
        let modules = self.modules;
        let cycle = &self.internals.declarations_being_checked[start..];
        let first = cycle[0].definition;
        let (first_name, first_file) = model::program::declared_name(modules, first);
        let only_aliases = (cycle.iter()).all(|asker| {
            matches!(
                model::program::declaration(modules, asker.definition),
                ast::Decl::Alias(_)
            )
        });
        let what = match model::program::declaration(modules, first) {
            ast::Decl::Alias(_) if only_aliases => "is an alias of itself",
            ast::Decl::Type(_) | ast::Decl::Alias(_) => "holds itself",
            ast::Decl::Const { .. } | ast::Decl::Function(_) => "depends on itself",
        };
        let first_text = model::program::declared_path(modules, first);
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
            let verb = match model::program::declaration(modules, asker) {
                ast::Decl::Type(_) => "holds",
                ast::Decl::Alias(_) => "is an alias of",
                ast::Decl::Const { .. } | ast::Decl::Function(_) => "uses",
            };
            let file = &modules[asker.module.index()].ast.file;
            let asker_text = model::program::declared_path(modules, asker);
            let asked_text = model::program::declared_path(modules, asked);
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
        let checking = self.internals.declarations_being_checked.last();
        checking
            .expect("a declaration is being checked")
            .definition
            .module
    }

    /// The module being checked.
    pub(crate) fn module(&self) -> &'p model::program::Module {
        &self.modules[self.current_module().index()]
    }

    /// The names the module being checked declares and imports.
    pub(crate) fn scope(&self) -> &Scope<'p> {
        &self.module_checks[self.current_module().index()].scope
    }

    /// Adds `diagnostic` to what the module being checked reported.
    pub(crate) fn report(&mut self, diagnostic: Diagnostic) {
        let module = self.current_module().index();
        self.module_checks[module].diagnostics.push(diagnostic);
    }

    /// Reports the error `message` at `span` of the module, and gives the type
    /// of an error already reported, so nothing more is reported about it.
    pub(crate) fn error(&mut self, code: Code, message: impl Into<String>, span: Span) -> TypeId {
        let diagnostic = Diagnostic::new(code, message).label(&self.module().ast.file, span, "");
        self.report(diagnostic);
        TypeId::INVALID
    }

    /// Records the fact `kind` about the text at `span` in the module being
    /// checked, when facts are recorded (`new`), unless its declaration's
    /// are not (`Checking::records_facts`) or it is a copy (`in_copy`),
    /// whose facts are of what it is given only.
    pub(crate) fn record(&mut self, span: Span, kind: FactKind) {
        let Some(checking) = self
            .internals
            .declarations_being_checked
            .last()
            .filter(|_| self.internals.records_editor_facts)
        else {
            return;
        };
        if checking.records_facts && !self.in_copy() {
            let definition = checking.definition;
            let facts = &mut self.module_checks[definition.module.index()].facts;
            facts
                .entry(definition)
                .or_default()
                .push(Fact { span, kind });
        }
    }

    /// The facts found in the text of `function`, its signature's and its
    /// body's, which declares each local they name, as its `where` does
    /// (`constraints_hold`); none for a copy, which records none and whose
    /// body is not the one they name.
    pub fn function_facts(&mut self, function: DeclarationCopy) -> Vec<Fact> {
        let definition = function.definition;
        let facts = &mut self.module_checks[definition.module.index()].facts;
        (function.arguments == ConstArguments::NONE)
            .then(|| facts.remove(&definition))
            .flatten()
            .unwrap_or_default()
    }

    /// The facts of `module` no function took (`function_facts`), by
    /// declaration: of its types and constants, and of functions with no
    /// body or an error in their signature, whose locals no body declares,
    /// so no name among them stands for a local.
    pub fn other_facts(&mut self, module: ModuleId) -> Vec<Fact> {
        let mut rest: Vec<(DefId, Vec<Fact>)> =
            std::mem::take(&mut self.module_checks[module.index()].facts)
                .into_iter()
                .collect();
        rest.sort_unstable_by_key(|(definition, _)| definition.index);
        (rest.into_iter())
            .flat_map(|(_, facts)| facts)
            .filter(|fact| !matches!(fact.kind, FactKind::Name(Symbol::Local(_))))
            .collect()
    }

    /// What `name` stands for at the top level: a type parameter of the
    /// function or struct being checked, `<T>` or `const T: type`, the type
    /// it is given (`Checking::arguments`; a `const` value is a local of the
    /// body); else a built-in type, which no declaration
    /// hides; else a name in the module's scope; else an exported name of
    /// the prelude.
    pub(crate) fn resolve_global(&mut self, name: &str) -> Option<Symbol> {
        let checking = self
            .internals
            .declarations_being_checked
            .last()
            .expect("a declaration is being checked");
        if checking.arguments != ConstArguments::NONE {
            let file = &self.module().ast.file;
            let found =
                model::program::declaration_copy_parameters(self.modules, checking.definition)
                    .enumerate()
                    .find(|(_, parameter)| {
                        parameter.is_some_and(|written| file.slice(written) == name)
                    });
            if let Some((place, Some(declared))) = found
                && let ConstArgument::Type(ty) = self.types.arguments(checking.arguments)[place]
            {
                return Some(Symbol::TypeArgument { ty, declared });
            }
        }
        if let Some(builtin) = self.types.builtin(name) {
            return Some(Symbol::Builtin(builtin));
        }
        let in_prelude = || {
            self.prelude()?
                .get(None, name)
                .filter(|&symbol| self.prelude_shares(name, symbol))
        };
        self.scope().get(None, name).or_else(in_prelude)
    }

    /// What the path written as the type expression `path` stands for:
    /// `module::name`, `Type::name` for a function of `Type`, or
    /// `module::Type::name`. A path that names nothing or is not supported is
    /// reported. Another module's name is reachable only if it starts with an
    /// upper case letter.
    pub(crate) fn resolve_path(&mut self, path: Id<ast::Type>) -> Option<Symbol> {
        let tree = &self.module().ast;
        let span = tree.types.span(path);
        // A `Type::Generic` alone, as `Box<T>`, is one segment.
        let segments: Vec<Id<ast::Type>> = match tree.types[path] {
            ast::Type::Path(segments) => tree.children.lists.items(segments).collect(),
            _ => vec![path],
        };
        if !segments
            .iter()
            .all(|&segment| matches!(tree.types[segment], ast::Type::Name))
        {
            self.not_supported(span, "paths with type arguments");
            return None;
        }
        // Each name after the first is looked up in what the one before it
        // names: a module's own names, or a type's functions, which the
        // module that declares the type holds under the type's id.
        let (&first, rest) = segments.split_first().expect("a path has a name");
        let (mut head, mut head_span) = (
            tree.file.slice(tree.types.span(first)),
            tree.types.span(first),
        );
        let Some(mut symbol) = self.resolve_global(head) else {
            self.report_unknown_name(head, head_span, std::iter::empty());
            return None;
        };
        self.record(head_span, FactKind::Name(symbol));
        for &member in rest {
            let owner = match symbol {
                Symbol::Type(definition) => {
                    let named = self.named_type(definition, head_span);
                    let owner = self.types[self.types.real(named)].definition();
                    owner.filter(|&owner| scope::has_functions(self.modules, owner))
                }
                _ => None,
            };
            let (place, module) = match (symbol, owner) {
                (Symbol::Module(imported), _) => ("module", imported),
                (_, Some(owner)) => ("type", owner.module),
                (Symbol::Type(_) | Symbol::Builtin(_) | Symbol::TypeArgument { .. }, None) => {
                    let message = format!("`{head}` has no functions");
                    self.error(diagnostics::INVALID_OPERATION, message, head_span);
                    return None;
                }
                _ => {
                    let message = format!("`{head}` is not a module or a type");
                    self.error(diagnostics::INVALID_OPERATION, message, head_span);
                    return None;
                }
            };
            let member_span = tree.types.span(member);
            let found = self.member(module, owner, place, head, member_span)?;
            (symbol, head, head_span) = (found, tree.file.slice(member_span), member_span);
        }
        Some(symbol)
    }

    /// What the name written at `span` stands for among the names of `owner`
    /// in `module`: the module's own names when `owner` is `None`, else the
    /// functions of the type `owner`. `place` and `head` name where, for
    /// messages: "module `utils`", "type `Point`". An unknown name is
    /// reported with the nearest one; another module's name is reachable
    /// only if it starts with an upper case letter.
    pub(crate) fn member(
        &mut self,
        module: ModuleId,
        owner: Option<DefId>,
        place: &str,
        head: &str,
        span: Span,
    ) -> Option<Symbol> {
        let file = &self.module().ast.file;
        let name = file.slice(span);
        let elsewhere = module != self.current_module();
        let members = &self.module_checks[module.index()].scope;
        let Some(found) = members.get(owner, name) else {
            let declared = members.symbols(owner).map(|(declared, _)| declared);
            let meant = scope::nearest(
                name,
                std::iter::empty(),
                declared.filter(|declared| !elsewhere || model::program::is_exported(declared)),
            );
            let message = format!("unknown name `{name}` in {place} `{head}`");
            self.report_unknown(diagnostics::UNDEFINED_NAME, span, message, meant);
            return None;
        };
        self.record(span, FactKind::Name(found));
        if elsewhere && !model::program::is_exported(name) {
            self.report(
                Diagnostic::new(
                    diagnostics::NOT_EXPORTED,
                    format!("`{name}` is not exported from `{head}`"),
                )
                .label(file, span, "")
                .note(
                    "names that start with an upper case letter are exported; others are private",
                ),
            );
            return None;
        }
        Some(found)
    }

    /// Reports `name`, written at `span`, which nothing in scope declares,
    /// with the nearest of `locals` and the top-level names.
    pub(crate) fn report_unknown_name<'a>(
        &mut self,
        name: &str,
        span: Span,
        locals: impl Iterator<Item = &'a str>,
    ) {
        let globals = self.visible_symbols().map(|(declared, _)| declared);
        let meant = scope::nearest(name, locals, globals);
        let message = format!("unknown name `{name}`");
        self.report_unknown(diagnostics::UNDEFINED_NAME, span, message, meant);
    }

    /// The names the module uses unqualified, with what each stands for: its
    /// scope's, then the prelude's exported ones.
    pub(crate) fn visible_symbols(&self) -> impl Iterator<Item = (&str, Symbol)> {
        let prelude = self
            .prelude()
            .into_iter()
            .flat_map(|scope| scope.symbols(None));
        let exported = prelude.filter(|&(name, symbol)| self.prelude_shares(name, symbol));
        self.scope().symbols(None).chain(exported)
    }

    /// Whether every module sees the prelude's `name`, which stands for
    /// `symbol`: it is exported, or a `#[builtin]` function, which is part
    /// of the language.
    fn prelude_shares(&self, name: &str, symbol: Symbol) -> bool {
        let builtin = |definition| model::program::is_builtin(self.modules, definition);
        model::program::is_exported(name)
            || matches!(symbol, Symbol::Function(definition) if builtin(definition))
    }

    /// The prelude's scope; `None` while checking the prelude itself.
    pub(crate) fn prelude(&self) -> Option<&Scope<'p>> {
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
    pub(crate) fn not_supported(&mut self, span: Span, what: &str) -> TypeId {
        let message = format!("{what} are not supported yet");
        self.error(diagnostics::NOT_SUPPORTED_YET, message, span)
    }
}
