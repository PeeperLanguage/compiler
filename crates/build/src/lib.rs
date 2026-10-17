//! The scheduler: checks every declaration of a program, each once, in the
//! order declarations ask for each other (docs/ir-design.md). It owns the
//! result of every query, and `check` asks for them through `check::Queries`,
//! so a declaration is checked the first time anything needs it.
//!
//! A compile can reuse what the previous one found: a module whose text and
//! imported interfaces are unchanged keeps its diagnostics, and its bodies are
//! not checked again. Only hashes and diagnostics are kept between compiles;
//! ids, types, THIR and MIR are rebuilt, since their numbering shifts.

use std::collections::HashMap;
use std::hash::DefaultHasher;
use std::hash::Hash;
use std::hash::Hasher;
use std::sync::Arc;

use arena::Id;
use check::Checker;
use check::Queries;
use check::scope::ModuleScope;
use diagnostics::Diagnostic;
use model::DefId;
use model::mir;
use model::syntax;
use model::thir;
use model::types::Type;
use model::types::TypeId;
use model::types::Types;
use model::value::Constant;
use source::SourceFile;

/// What a compile found, kept for the next compile of the same program.
#[derive(Default)]
pub struct Compiled {
    names: Vec<Diagnostic>, // from collecting each module's names; redone every compile
    modules: Vec<Module>,   // in the program's order
}

impl Compiled {
    /// Every diagnostic, in the order a compile from nothing reports them.
    pub fn diagnostics(&self) -> impl Iterator<Item = &Diagnostic> {
        let modules = self.modules.iter().flat_map(|module| &module.diagnostics);
        self.names.iter().chain(modules)
    }
}

/// What a compile found in one module.
struct Module {
    path: String,
    file: Arc<SourceFile>,
    interface: u64, // `Build::interface`
    diagnostics: Vec<Diagnostic>,
}

/// Checks every declaration of `program`, lowers each function to MIR and
/// gives what it found. A module of `previous` (a compile of the same program
/// with the same `pointer_bits`, or `Compiled::default()`) whose text and
/// whose imports' interfaces are unchanged keeps its diagnostics, and its
/// bodies are not checked again. The THIR and MIR of each checked function
/// with a complete body go to `use_function` as soon as they are built, in
/// declaration order, and are dropped after: nothing holds every function's
/// bodies at once. Integers as wide as a pointer (`isize`, `usize`) have
/// `pointer_bits` bits.
pub fn compile(
    program: &project::Program,
    pointer_bits: u32,
    previous: Compiled,
    mut use_function: impl FnMut(DefId, &thir::Body, &mir::Body, &Types),
) -> Compiled {
    let modules: Vec<&project::Module> = program
        .order
        .iter()
        .map(|path| &program.modules[path])
        .collect();
    let indexes: HashMap<&str, u32> = (0..)
        .zip(&modules)
        .map(|(index, module)| (module.path.as_str(), index))
        .collect();
    let mut names = Vec::new();
    let scopes: Vec<ModuleScope> = (0..)
        .zip(&modules)
        .map(|(index, module)| check::scope::collect(index, module, &indexes, &mut names))
        .collect();
    let program = Program {
        prelude: indexes.get(project::PRELUDE_PATH).copied(),
        modules,
        scopes,
    };
    let mut build = Build {
        program: &program,
        types: Types::new(),
        pointer_bits,
        signatures: table(&program.modules),
        constants: table(&program.modules),
        diagnostics: vec![Vec::new(); program.modules.len()],
        reused: vec![false; program.modules.len()],
    };
    let mut previous: HashMap<String, Module> = previous
        .modules
        .into_iter()
        .map(|module| (module.path.clone(), module))
        .collect();
    let mut interfaces = Vec::with_capacity(program.modules.len());
    // Whether each module's interface differs from the previous compile's.
    let mut changed: Vec<bool> = Vec::with_capacity(program.modules.len());
    for (index, module) in (0..).zip(&program.modules) {
        let tree = &module.syntax;
        // An import comes before its importer, so it is decided already.
        let imports_unchanged = module.imports.iter().all(|import| {
            let place = indexes.get(import.path.as_str());
            place.is_some_and(|&place| changed.get(place as usize) == Some(&false))
        });
        let cached = previous.remove(&module.path);
        let previous_interface = cached.as_ref().map(|cached| cached.interface);
        if let Some(cached) = cached
            && imports_unchanged
            && cached.file.text == tree.file.text
        {
            build.reused[index as usize] = true;
            build.diagnostics[index as usize] = cached.diagnostics;
            interfaces.push(cached.interface);
            changed.push(false);
            continue;
        }
        for declaration in tree.lists.items(tree.declarations) {
            let definition = DefId {
                module: index,
                index: declaration.index() as u32,
            };
            match tree.decls[declaration] {
                syntax::Decl::Function(function) => {
                    if let Some((thir, mir)) = build.function(definition, &function) {
                        use_function(definition, &thir, &mir, &build.types);
                    }
                }
                syntax::Decl::Const { .. } => {
                    build.constant(definition);
                }
                _ => {
                    build.signature(definition);
                }
            }
        }
        let interface = build.interface(index);
        changed.push(previous_interface != Some(interface));
        interfaces.push(interface);
    }
    let modules = program
        .modules
        .iter()
        .zip(build.diagnostics)
        .zip(interfaces);
    let modules = modules.map(|((module, diagnostics), interface)| Module {
        path: module.path.clone(),
        file: module.syntax.file.clone(),
        interface,
        diagnostics,
    });
    Compiled {
        names,
        modules: modules.collect(),
    }
}

/// Feeds `ty` to `hasher` by what it is, not by its id, which differs between
/// compiles.
fn hash_type(types: &Types, ty: TypeId, hasher: &mut DefaultHasher) {
    let ty = &types[ty];
    std::mem::discriminant(ty).hash(hasher);
    match ty {
        Type::Integer(integer) => integer.hash(hasher),
        Type::Float(float) => float.hash(hasher),
        Type::Function { parameters, result } => {
            parameters.len().hash(hasher);
            for &parameter in parameters {
                hash_type(types, parameter, hasher);
            }
            hash_type(types, *result, hasher);
        }
        Type::Invalid | Type::Void | Type::Bool | Type::CString => {}
    }
}

/// A query's results for every declaration of `modules`, none started.
fn table<T: Clone>(modules: &[&project::Module]) -> Vec<Vec<State<T>>> {
    let declarations = |module: &&project::Module| module.syntax.decls.len();
    modules
        .iter()
        .map(|module| vec![State::NotStarted; declarations(module)])
        .collect()
}

/// What the scheduler reads and never changes.
struct Program<'p> {
    modules: Vec<&'p project::Module>, // in the program's order
    scopes: Vec<ModuleScope>,          // of each module
    prelude: Option<u32>,              // the place of `core:global`, if loaded
}

/// Where one query of one declaration is.
#[derive(Clone)]
enum State<T> {
    NotStarted,
    /// Asked for again while running: a cycle.
    Running,
    Done(T),
}

/// The results of the queries, by module and declaration.
struct Build<'p> {
    program: &'p Program<'p>,
    types: Types,
    pointer_bits: u32,
    signatures: Vec<Vec<State<TypeId>>>,
    constants: Vec<Vec<State<Option<Constant>>>>,
    diagnostics: Vec<Vec<Diagnostic>>, // by module
    /// By module: whether it keeps the previous compile's diagnostics, which
    /// asking for its declarations again would only repeat.
    reused: Vec<bool>,
}

impl<'p> Build<'p> {
    /// Runs `work` on a checker of the module at `module`; what it reports
    /// joins that module's diagnostics.
    fn with_checker<T>(
        &mut self,
        module: u32,
        work: impl FnOnce(&mut Checker<Build<'p>>) -> T,
    ) -> T {
        let program = self.program;
        let index = module as usize;
        let mut diagnostics = Vec::new();
        let mut checker = Checker {
            module: program.modules[index],
            scopes: &program.scopes,
            scope: &program.scopes[index],
            prelude: program.prelude.filter(|&prelude| prelude != module),
            queries: self,
            diagnostics: &mut diagnostics,
        };
        let result = work(&mut checker);
        if !self.reused[index] {
            self.diagnostics[index].append(&mut diagnostics);
        }
        result
    }

    /// A hash of what other modules see of the module at `module`, once its
    /// declarations are checked: its imports, and each declaration's kind,
    /// name, and type or value. Its bodies are not part of it.
    fn interface(&self, module: u32) -> u64 {
        let index = module as usize;
        let module = self.program.modules[index];
        let tree = &module.syntax;
        let mut hasher = DefaultHasher::new();
        for import in &module.imports {
            import.path.hash(&mut hasher);
            import.alias.hash(&mut hasher);
        }
        for declaration in tree.lists.items(tree.declarations) {
            let decl = &tree.decls[declaration];
            std::mem::discriminant(decl).hash(&mut hasher);
            tree.file.slice(decl.name()).hash(&mut hasher);
            if let State::Done(ty) = self.signatures[index][declaration.index()] {
                hash_type(&self.types, ty, &mut hasher);
            }
            if let State::Done(Some((ty, value))) = &self.constants[index][declaration.index()] {
                hash_type(&self.types, *ty, &mut hasher);
                value.hash(&mut hasher);
            }
        }
        hasher.finish()
    }

    /// Checks the function `definition`, lowers its body to MIR and runs the
    /// path checks on it; gives its THIR and MIR, or `None` when it has no body
    /// or checking it found an error.
    fn function(
        &mut self,
        definition: DefId,
        function: &syntax::Function,
    ) -> Option<(thir::Body, mir::Body)> {
        let ty = self.signature(definition);
        let body = self.with_checker(definition.module, |checker| checker.body(function, ty))?;
        let Type::Function { parameters, result } = &self.types[ty] else {
            return None; // the signature's error was reported
        };
        let module = definition.module as usize;
        let tree = &self.program.modules[module].syntax;
        let span = tree.blocks.span(function.body?);
        let mir = lower::lower(&body, parameters.len(), span)?;
        let diagnostics = &mut self.diagnostics[module];
        // Only a `let` without a value can be unset or assigned twice.
        let late = |index| body.locals[Id::from_index(index)].declared_without_value;
        if (0..body.locals.len()).any(late) {
            lower::path_checks::initialization(&mir, &tree.file, diagnostics);
        }
        lower::path_checks::unreachable_code(&mir, &tree.file, diagnostics);
        if let Some(error) =
            lower::path_checks::missing_return(&mir, &tree.file, &self.types, *result)
        {
            diagnostics.push(error); // last: it points at the closing brace
        }
        Some((body, mir))
    }

    /// Reports that the declaration `definition` was asked for while it was
    /// being checked: it depends on itself.
    fn report_cycle(&mut self, definition: DefId) {
        let module = definition.module as usize;
        if self.reused[module] {
            return;
        }
        let tree = &self.program.modules[module].syntax;
        let name = tree.decls[Id::from_index(definition.index as usize)].name();
        let text = tree.file.slice(name);
        self.diagnostics[module].push(
            Diagnostic::new(
                diagnostics::CIRCULAR_DEPENDENCY,
                format!("`{text}` depends on itself"),
            )
            .label(&tree.file, name, ""),
        );
    }
}

impl Queries for Build<'_> {
    fn types(&mut self) -> &mut Types {
        &mut self.types
    }

    fn pointer_bits(&self) -> u32 {
        self.pointer_bits
    }

    fn signature(&mut self, function: DefId) -> TypeId {
        let (module, index) = (function.module as usize, function.index as usize);
        match self.signatures[module][index] {
            State::Done(ty) => return ty,
            State::Running => {
                self.report_cycle(function);
                return TypeId::INVALID;
            }
            State::NotStarted => {}
        }
        self.signatures[module][index] = State::Running;
        let ty = self.with_checker(function.module, |checker| {
            checker.signature(Id::from_index(index))
        });
        self.signatures[module][index] = State::Done(ty);
        ty
    }

    fn constant(&mut self, constant: DefId) -> Option<Constant> {
        let (module, index) = (constant.module as usize, constant.index as usize);
        match &self.constants[module][index] {
            State::Done(value) => return value.clone(),
            State::Running => {
                self.report_cycle(constant);
                return None;
            }
            State::NotStarted => {}
        }
        self.constants[module][index] = State::Running;
        let value = self.with_checker(constant.module, |checker| {
            checker.constant(Id::from_index(index))
        });
        self.constants[module][index] = State::Done(value.clone());
        value
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::sync::Arc;

    use source::SourceFile;

    use super::Compiled;
    use super::compile;

    /// The program of `modules`, each an import path and its text, in the
    /// order given (each after those it imports). An import written as
    /// `import "app:utils";` is named after its last part.
    fn program(modules: &[(&str, &str)]) -> project::Program {
        let mut program = project::Program {
            modules: HashMap::new(),
            order: Vec::new(),
            diagnostics: Vec::new(),
        };
        for &(path, text) in modules {
            let file = Arc::new(SourceFile::new(format!("{path}.peep"), text));
            let (syntax, parse) = parser::parse(&file);
            assert!(parse.is_empty(), "{parse:?}");
            let mut imports: Vec<project::Import> = syntax
                .imports
                .iter()
                .map(|import| {
                    let written = file.slice(import.path).trim_matches('"').to_string();
                    let alias = match import.alias {
                        Some(alias) => file.slice(alias).to_string(),
                        None => written.rsplit([':', '/']).next().unwrap().to_string(),
                    };
                    project::Import {
                        path: written,
                        alias: Some(alias),
                        span: import.span,
                    }
                })
                .collect();
            let has_prelude = modules
                .iter()
                .any(|&(path, _)| path == project::PRELUDE_PATH);
            if has_prelude && path != project::PRELUDE_PATH {
                imports.push(project::Import {
                    path: project::PRELUDE_PATH.to_string(),
                    alias: None,
                    span: source::Span::at(0),
                });
            }
            let module = project::Module {
                path: path.to_string(),
                syntax,
                imports,
                diagnostics: Vec::new(),
            };
            program.modules.insert(path.to_string(), module);
            program.order.push(path.to_string());
        }
        program
    }

    /// Checks the program of `modules` (see `program`) and gives each
    /// diagnostic as its code, message and the text of its first label.
    fn diagnostics(modules: &[(&str, &str)]) -> Vec<(&'static str, String, String)> {
        let found = compile(&program(modules), 64, Compiled::default(), |_, _, _, _| {});
        found
            .diagnostics()
            .map(|diagnostic| {
                let label = &diagnostic.labels[0];
                let text = label.file.slice(label.span).to_string();
                (diagnostic.code.id, diagnostic.message.clone(), text)
            })
            .collect()
    }

    fn expected(expected: &[(&'static str, &str, &str)]) -> Vec<(&'static str, String, String)> {
        let owned = |&(code, message, text): &(&'static str, &str, &str)| {
            (code, message.to_string(), text.to_string())
        };
        expected.iter().map(owned).collect()
    }

    #[test]
    fn calls() {
        let found = diagnostics(&[(
            "app:main",
            "fn main() {
                let sum: i64 = add(1, 2u8);
                let wrong = add(1);
                let extra = add(1, 2, true);
                let mismatched = add(true, 2);
                let nothing = main();
                let local = 1;
                local();
                Point(1);
                add(missing, 2);
            }
            fn add(a: i64, b: i16) -> i64 { return a + b; }
            struct Point { x: i32 }",
        )]);
        assert_eq!(
            found,
            expected(&[
                (
                    "T0006",
                    "`add` takes 2 arguments, but 1 was given",
                    "add(1)"
                ),
                (
                    "T0006",
                    "`add` takes 2 arguments, but 3 were given",
                    "add(1, 2, true)"
                ),
                (
                    "T0001",
                    "mismatched types: expected `i64`, found `bool`",
                    "true"
                ),
                ("T0004", "this expression has no value to bind", "main()"),
                ("T0005", "`local` is not a function", "local"),
                ("T0005", "`Point` is not a function", "Point"),
                ("T0002", "unknown name `missing`", "missing"),
                (
                    "T0043",
                    "struct declarations are not supported yet",
                    "Point"
                ),
            ])
        );
    }

    #[test]
    fn other_modules() {
        let found = diagnostics(&[
            (
                "app:utils",
                "fn Add(a: i32, b: i32) -> i32 { return a + b; }
                fn hidden() {}
                const Limit: u8 = 200;",
            ),
            (
                "app:main",
                "import \"app:utils\";
                fn main() {
                    let sum = utils::Add(1, 2);
                    let wide: i16 = utils::Limit;
                    utils::hidden();
                    utils::Missing();
                    let bad: u8 = utils::Add(1, 2);
                    utils();
                }",
            ),
        ]);
        assert_eq!(
            found,
            expected(&[
                ("M0004", "`hidden` is not exported from `utils`", "hidden"),
                (
                    "T0002",
                    "unknown name `Missing` in module `utils`",
                    "Missing"
                ),
                (
                    "T0001",
                    "mismatched types: expected `u8`, found `i32`",
                    "utils::Add(1, 2)"
                ),
                ("T0005", "`utils` is not a function", "utils"),
            ])
        );
    }

    #[test]
    fn module_constants() {
        let found = diagnostics(&[(
            "app:main",
            "const Three = One + 2;
            const One = 1;
            const Wide: i64 = Three;
            const Narrow: u8 = Three;
            const Zero = Three - 3;
            fn main() {
                let a = 10 / Zero;
                let b: i64 = Wide;
                let c = Missing;
            }",
        )]);
        assert_eq!(
            found,
            expected(&[
                (
                    "T0001",
                    "mismatched types: expected `u8`, found `i32`",
                    "Three"
                ),
                ("T0004", "division by zero", "Zero"),
                ("T0002", "unknown name `Missing`", "Missing"),
            ])
        );
    }

    #[test]
    fn a_cycle_is_reported_once() {
        let found = diagnostics(&[(
            "app:main",
            "const A = A + 1;
            const B = C;
            const C = B;
            fn main() { let x = A + B; }",
        )]);
        assert_eq!(
            found,
            expected(&[
                ("T0013", "`A` depends on itself", "A"),
                ("T0013", "`B` depends on itself", "B"),
            ])
        );
    }

    #[test]
    fn module_constants_cannot_be_assigned() {
        let found = diagnostics(&[
            ("core:global", "const stdout: i32 = 1;"),
            (
                "app:main",
                "const Limit: i32 = 1;\nfn main() { Limit = 2; stdout = 3; main = 4; }",
            ),
        ]);
        assert_eq!(
            found,
            expected(&[
                ("T0018", "cannot assign to constant `Limit`", "Limit"),
                ("T0018", "cannot assign to constant `stdout`", "stdout"),
                (
                    "T0007",
                    "cannot assign to `main`, which is not a variable",
                    "main"
                ),
            ])
        );
    }

    #[test]
    fn the_prelude_is_in_scope() {
        let found = diagnostics(&[
            (
                "core:global",
                "const stdout: i32 = 1;
                #[extern(\"peeper_rt_v1_write\")]
                fn write(fd: i32, buf: cstr, n: i32) -> i32;
                #[extern(\"peeper_rt_v1_exit\")]
                fn exit(code: i32);",
            ),
            (
                "app:main",
                "import \"core:global\";
                fn main() { exit(stdout); global::exit(global::stdout); let exit = 2; }",
            ),
        ]);
        assert!(found.is_empty(), "{found:?}");
    }

    /// Compiles the program of `modules` (see `program`) after `previous`;
    /// gives what it found and the paths of the modules whose bodies were
    /// checked, which every module of these tests has.
    fn recompile(modules: &[(&str, &str)], previous: Compiled) -> (Compiled, Vec<String>) {
        let program = program(modules);
        let mut checked: Vec<String> = Vec::new();
        let compiled = compile(&program, 64, previous, |definition, _, _, _| {
            let path = &program.order[definition.module as usize];
            if !checked.contains(path) {
                checked.push(path.clone());
            }
        });
        (compiled, checked)
    }

    /// The message of each diagnostic of `compiled`, in order.
    fn messages(compiled: &Compiled) -> Vec<String> {
        let message = |diagnostic: &diagnostics::Diagnostic| diagnostic.message.clone();
        compiled.diagnostics().map(message).collect()
    }

    const UTILS: (&str, &str) = (
        "app:utils",
        "fn Add(a: i32, b: i32) -> i32 { return a + b; }",
    );
    const OTHER: (&str, &str) = ("app:other", "fn Other() {}");
    const MAIN: (&str, &str) = (
        "app:main",
        "import \"app:utils\";
        fn main() { let sum = utils::Add(1, 2); }
        fn wrong() { let flag: bool = 1; }",
    );

    #[test]
    fn an_unchanged_program_is_not_checked_again() {
        let (first, checked) = recompile(&[UTILS, OTHER, MAIN], Compiled::default());
        assert_eq!(checked, ["app:utils", "app:other", "app:main"]);
        let found = messages(&first);
        assert_eq!(found, ["mismatched types: expected `bool`, found `i32`"]);
        let (second, checked) = recompile(&[UTILS, OTHER, MAIN], first);
        assert!(checked.is_empty(), "{checked:?}");
        assert_eq!(messages(&second), found);
    }

    #[test]
    fn a_body_edit_checks_only_its_module() {
        let (first, _) = recompile(&[UTILS, OTHER, MAIN], Compiled::default());
        let utils = (
            "app:utils",
            "fn Add(a: i32, b: i32) -> i32 { return b + a; }",
        );
        let (_, checked) = recompile(&[utils, OTHER, MAIN], first);
        assert_eq!(checked, ["app:utils"]);
    }

    #[test]
    fn a_signature_edit_checks_its_importers() {
        let (first, _) = recompile(&[UTILS, OTHER, MAIN], Compiled::default());
        let utils = (
            "app:utils",
            "fn Add(a: i64, b: i64) -> i64 { return a + b; }",
        );
        let (_, checked) = recompile(&[utils, OTHER, MAIN], first);
        assert_eq!(checked, ["app:utils", "app:main"]);
    }

    #[test]
    fn errors_come_and_go_with_edits() {
        let (first, _) = recompile(&[UTILS, MAIN], Compiled::default());
        let broken = (
            "app:utils",
            "fn Add(a: i32, b: i32) -> i32 { let c: bool = a; return a + b; }",
        );
        // `utils` is checked, but `Add` has an error and gives no MIR; its
        // interface is unchanged, so `main` keeps its own error.
        let (second, checked) = recompile(&[broken, MAIN], first);
        assert!(checked.is_empty(), "{checked:?}");
        let mismatch = |found: &str| format!("mismatched types: expected `bool`, found `{found}`");
        assert_eq!(messages(&second), [mismatch("i32"), mismatch("i32")]);
        let (third, checked) = recompile(&[UTILS, MAIN], second);
        assert_eq!(checked, ["app:utils"]);
        assert_eq!(messages(&third), [mismatch("i32")]);
    }

    #[test]
    fn a_reused_module_reports_its_errors_once() {
        let utils = (
            "app:utils",
            "fn Bad(x: Missing) {}
            fn Good() {}",
        );
        let main = (
            "app:main",
            "import \"app:utils\";\nfn main() { utils::Bad(1); }\nfn clean() {}",
        );
        let (first, _) = recompile(&[utils, main], Compiled::default());
        let found = messages(&first);
        assert_eq!(found, ["unknown type `Missing`"]);
        let edited = (
            "app:main",
            "import \"app:utils\";\nfn main() { utils::Bad(2); }\nfn clean() {}",
        );
        let (second, checked) = recompile(&[utils, edited], first);
        assert_eq!(checked, ["app:main"]);
        assert_eq!(messages(&second), found);
    }

    #[test]
    fn a_prelude_edit_checks_what_it_changes() {
        let prelude = ("core:global", "const stdout: i32 = 1;\nfn Helper() {}");
        let main = ("app:main", "fn main() { let out = stdout; }");
        let (first, _) = recompile(&[prelude, main], Compiled::default());
        let body = (
            "core:global",
            "const stdout: i32 = 1;\nfn Helper() { let a = 1; }",
        );
        let (second, checked) = recompile(&[body, main], first);
        assert_eq!(checked, ["core:global"]);
        let value = (
            "core:global",
            "const stdout: i32 = 2;\nfn Helper() { let a = 1; }",
        );
        let (_, checked) = recompile(&[value, main], second);
        assert_eq!(checked, ["core:global", "app:main"]);
    }
}
