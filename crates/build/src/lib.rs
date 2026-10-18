//! The scheduler: checks every declaration of a program, each once, in the
//! order declarations ask for each other (docs/ir-design.md). It owns the
//! result of every query, and `check` asks for them through `check::Queries`,
//! so a declaration is checked the first time anything needs it.
//!
//! A compile can reuse what the previous one found: a module whose text and
//! imported interfaces are unchanged keeps its diagnostics, and its bodies are
//! not checked again. Only each module's source file, interface hash and
//! diagnostics are kept between compiles; ids, types, THIR and MIR are
//! rebuilt, since their numbering shifts.

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
use model::ModuleId;
use model::ast;
use model::constant::Constant;
use model::mir;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir;
use project::ImportKind;
use source::SourceFile;

/// What a compile found, kept for the next compile of the same program.
#[derive(Default)]
pub struct Results {
    modules: Vec<Module>, // in the program's order
}

impl Results {
    /// Every diagnostic, in the order a compile from nothing reports them:
    /// building every module's scope comes before checking any module.
    pub fn diagnostics(&self) -> impl Iterator<Item = &Diagnostic> {
        let scopes = self
            .modules
            .iter()
            .flat_map(|module| &module.scope_diagnostics);
        let checked = self.modules.iter().flat_map(|module| &module.diagnostics);
        scopes.chain(checked)
    }

    /// The files of the modules this compile reused instead of checking them
    /// again.
    pub fn reused(&self) -> impl Iterator<Item = &SourceFile> {
        let reused = self.modules.iter().filter(|module| module.reused);
        reused.map(|module| &*module.file)
    }
}

/// What a compile found in one module.
struct Module {
    file: Arc<SourceFile>, // its name tells the module's entry in the next compile
    interface: u64,        // `Build::interface`
    scope_diagnostics: Vec<Diagnostic>, // from building its scope; redone every compile
    diagnostics: Vec<Diagnostic>, // from checking it; kept while it is reused
    reused: bool,          // kept from the previous compile, not checked
}

/// Checks every declaration of `program`, lowers each function to MIR and
/// gives what it found. A module of `previous` (a compile of the same program
/// with the same `pointer_bits`, or `Results::default()`) whose text and
/// whose imports' interfaces are unchanged keeps its diagnostics, and its
/// bodies are not checked again. The THIR of each checked function with a
/// body, and its MIR unless checking it found an error, go to `use_function`
/// as soon as they are built, in declaration order, and are dropped after:
/// nothing holds every function's bodies at once. Integers as wide as a
/// pointer (`isize`, `usize`) have `pointer_bits` bits.
pub fn compile(
    program: &project::Program,
    pointer_bits: u32,
    previous: Results,
    mut use_function: impl FnMut(DefId, &thir::Body, Option<&mir::Body>, &Types),
) -> Results {
    let modules = program.modules.as_slice();
    let mut scope_diagnostics = vec![Vec::new(); modules.len()];
    let scopes: Vec<ModuleScope> = (0..modules.len())
        .zip(&mut scope_diagnostics)
        .map(|(index, diagnostics)| {
            check::scope::collect(ModuleId::from_index(index), modules, diagnostics)
        })
        .collect();
    let program = Program { modules, scopes };
    let mut build = Build {
        program: &program,
        types: Types::new(),
        pointer_bits,
        signatures: table(program.modules),
        constants: table(program.modules),
        diagnostics: vec![Vec::new(); program.modules.len()],
        reused: vec![false; program.modules.len()],
    };
    let mut previous: HashMap<String, Module> = previous
        .modules
        .into_iter()
        .map(|cached| (cached.file.name.clone(), cached))
        .collect();
    let mut interfaces = Vec::with_capacity(program.modules.len());
    // Whether each module's interface differs from the previous compile's.
    let mut changed: Vec<bool> = Vec::with_capacity(program.modules.len());
    for (index, module) in program.modules.iter().enumerate() {
        let id = ModuleId::from_index(index);
        let tree = &module.ast;
        // An import comes before its importer, so it is decided already.
        let imports_unchanged = module
            .imports
            .iter()
            .all(|import| changed.get(import.module.index()) == Some(&false));
        let cached = previous.remove(&module.ast.file.name);
        let previous_interface = cached.as_ref().map(|cached| cached.interface);
        if let Some(cached) = cached
            && imports_unchanged
            && cached.file.text == module.ast.file.text
        {
            build.reused[index] = true;
            build.diagnostics[index] = cached.diagnostics;
            interfaces.push(cached.interface);
            changed.push(false);
            continue;
        }
        for declaration in tree.lists.items(tree.declarations) {
            let definition = DefId {
                module: id,
                index: declaration.index() as u32,
            };
            match tree.decls[declaration] {
                ast::Decl::Function(function) => {
                    let ty = build.signature(definition);
                    let thir = build.with_checker(id, |checker| checker.body(&function, ty));
                    if let Some(thir) = thir {
                        let mir = build.lower(id, ty, &thir);
                        use_function(definition, &thir, mir.as_ref(), &build.types);
                    }
                }
                ast::Decl::Const { .. } => {
                    build.constant(definition);
                }
                _ => {
                    build.signature(definition);
                }
            }
        }
        let interface = build.interface(id);
        changed.push(previous_interface != Some(interface));
        interfaces.push(interface);
    }
    let modules = program
        .modules
        .iter()
        .zip(scope_diagnostics)
        .zip(build.diagnostics)
        .zip(interfaces)
        .zip(build.reused);
    let modules = modules.map(
        |((((module, scope_diagnostics), diagnostics), interface), reused)| Module {
            file: module.ast.file.clone(),
            interface,
            scope_diagnostics,
            diagnostics,
            reused,
        },
    );
    Results {
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
fn table<T: Clone>(modules: &[project::Module]) -> Vec<Vec<State<T>>> {
    modules
        .iter()
        .map(|module| vec![State::NotStarted; module.ast.decls.len()])
        .collect()
}

/// What the scheduler reads and never changes.
struct Program<'p> {
    modules: &'p [project::Module], // in the program's order
    scopes: Vec<ModuleScope>,       // of each module
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
    /// Runs `work` on a checker of `module`; what it reports joins that
    /// module's diagnostics.
    fn with_checker<T>(
        &mut self,
        module: ModuleId,
        work: impl FnOnce(&mut Checker<Build<'p>>) -> T,
    ) -> T {
        let program = self.program;
        let index = module.index();
        let mut diagnostics = Vec::new();
        let mut checker = Checker {
            module: &program.modules[index],
            scopes: &program.scopes,
            scope: &program.scopes[index],
            prelude: program.modules[index]
                .imports
                .iter()
                .find(|import| import.kind == ImportKind::Prelude)
                .map(|import| import.module),
            queries: self,
            diagnostics: &mut diagnostics,
        };
        let result = work(&mut checker);
        if !self.reused[index] {
            self.diagnostics[index].append(&mut diagnostics);
        }
        result
    }

    /// A hash of what other modules see of `module`, once its declarations
    /// are checked: its imports, and each declaration's kind, name, and type
    /// or value. Its bodies are not part of it.
    fn interface(&self, module: ModuleId) -> u64 {
        let index = module.index();
        let module = &self.program.modules[index];
        let tree = &module.ast;
        let mut hasher = DefaultHasher::new();
        for import in &module.imports {
            let modules = self.program.modules;
            modules[import.module.index()].path.hash(&mut hasher);
            import
                .name(module, modules)
                .map(|(name, _)| name)
                .hash(&mut hasher);
        }
        for declaration in tree.lists.items(tree.declarations) {
            let decl = &tree.decls[declaration];
            std::mem::discriminant(decl).hash(&mut hasher);
            decl.name().text(&tree.file).hash(&mut hasher);
            if let State::Done(ty) = self.signatures[index][declaration.index()] {
                hash_type(&self.types, ty, &mut hasher);
            }
            if let State::Done(Some(Constant { ty, value })) =
                &self.constants[index][declaration.index()]
            {
                hash_type(&self.types, *ty, &mut hasher);
                value.hash(&mut hasher);
            }
        }
        hasher.finish()
    }

    /// Lowers `body`, the THIR of a function of `module` with the signature
    /// `ty`, to MIR and runs the path checks on it; `None` when it cannot be
    /// lowered (`lower::lower`).
    fn lower(&mut self, module: ModuleId, ty: TypeId, body: &thir::Body) -> Option<mir::Body> {
        let Type::Function { parameters, result } = &self.types[ty] else {
            return None; // the signature's error was reported
        };
        let module = module.index();
        let tree = &self.program.modules[module].ast;
        let mir = lower::lower(body, parameters.len())?;
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
        Some(mir)
    }

    /// The result of one query of `definition`, kept in `table`: the result
    /// already found, `on_cycle` when the query is asked for while it runs (a
    /// cycle, reported), or else what `work` finds with a checker of its
    /// module, kept for the next ask.
    fn query<T: Clone>(
        &mut self,
        definition: DefId,
        table: fn(&mut Self) -> &mut Vec<Vec<State<T>>>,
        on_cycle: T,
        work: impl FnOnce(&mut Checker<Build<'p>>, Id<ast::Decl>) -> T,
    ) -> T {
        let (module, index) = (definition.module.index(), definition.index as usize);
        match &table(self)[module][index] {
            State::Done(result) => return result.clone(),
            State::Running => {
                self.report_cycle(definition);
                return on_cycle;
            }
            State::NotStarted => {}
        }
        table(self)[module][index] = State::Running;
        let result = self.with_checker(definition.module, |checker| {
            work(checker, Id::from_index(index))
        });
        table(self)[module][index] = State::Done(result.clone());
        result
    }

    /// Reports that the declaration `definition` was asked for while it was
    /// being checked: it depends on itself.
    fn report_cycle(&mut self, definition: DefId) {
        let module = definition.module.index();
        if self.reused[module] {
            return;
        }
        let tree = &self.program.modules[module].ast;
        let name = tree.decls[Id::from_index(definition.index as usize)].name();
        let text = name.text(&tree.file);
        self.diagnostics[module].push(
            Diagnostic::new(
                diagnostics::CIRCULAR_DEPENDENCY,
                format!("`{text}` depends on itself"),
            )
            .label(&tree.file, name.span, ""),
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
        let work = |checker: &mut Checker<_>, declaration| checker.signature(declaration);
        self.query(
            function,
            |build| &mut build.signatures,
            TypeId::INVALID,
            work,
        )
    }

    fn constant(&mut self, constant: DefId) -> Option<Constant> {
        let work = |checker: &mut Checker<_>, declaration| checker.constant(declaration);
        self.query(constant, |build| &mut build.constants, None, work)
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::path::PathBuf;

    use super::Results;
    use super::compile;

    /// The program of `modules`, each an import path and its text, loaded as
    /// `peeper check` loads it from `app:main`, with every file given as open
    /// editor text so nothing is read from disk. Loading must find no error.
    fn program(modules: &[(&str, &str)]) -> project::Program {
        let folder = |package: &str| match package {
            "core" => PathBuf::from("/peeper-build-test/libs/core/src"),
            _ => PathBuf::from("/peeper-build-test/src"),
        };
        let packages = HashMap::from([
            ("app".to_string(), folder("app")),
            ("core".to_string(), folder("core")),
        ]);
        let open_files = modules
            .iter()
            .map(|&(path, text)| {
                let (package, file) = path.split_once(':').unwrap();
                (
                    folder(package).join(format!("{file}.peep")),
                    text.to_string(),
                )
            })
            .collect();
        let program = project::load(&packages, &folder("app").join("main.peep"), &open_files);
        let reported: Vec<&str> = program
            .load_diagnostics()
            .map(|diagnostic| diagnostic.message.as_str())
            .collect();
        assert!(!program.load_failed(), "{reported:?}");
        program
    }

    /// Checks the program of `modules` (see `program`) and gives each
    /// diagnostic as its code, message and the text of its first label.
    fn diagnostics(modules: &[(&str, &str)]) -> Vec<(&'static str, String, String)> {
        let found = compile(&program(modules), 64, Results::default(), |_, _, _, _| {});
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
    fn recompile(modules: &[(&str, &str)], previous: Results) -> (Results, Vec<String>) {
        let program = program(modules);
        let mut checked: Vec<String> = Vec::new();
        let results = compile(&program, 64, previous, |definition, _, _, _| {
            let path = &program.modules[definition.module.index()].path;
            if !checked.contains(path) {
                checked.push(path.clone());
            }
        });
        (results, checked)
    }

    /// The message of each diagnostic of `results`, in order.
    fn messages(results: &Results) -> Vec<String> {
        let message = |diagnostic: &diagnostics::Diagnostic| diagnostic.message.clone();
        results.diagnostics().map(message).collect()
    }

    const UTILS: (&str, &str) = (
        "app:utils",
        "fn Add(a: i32, b: i32) -> i32 { return a + b; }",
    );
    const OTHER: (&str, &str) = ("app:other", "fn Other() {}");
    const MAIN: (&str, &str) = (
        "app:main",
        "import \"app:utils\";
        import \"app:other\";
        fn main() { let sum = utils::Add(1, 2); }
        fn wrong() { let flag: bool = 1; }",
    );

    #[test]
    fn an_unchanged_program_is_not_checked_again() {
        let (first, checked) = recompile(&[UTILS, OTHER, MAIN], Results::default());
        assert_eq!(checked, ["app:other", "app:utils", "app:main"]);
        let found = messages(&first);
        assert_eq!(found, ["mismatched types: expected `bool`, found `i32`"]);
        let (second, checked) = recompile(&[UTILS, OTHER, MAIN], first);
        assert!(checked.is_empty(), "{checked:?}");
        assert_eq!(messages(&second), found);
    }

    #[test]
    fn a_body_edit_checks_only_its_module() {
        let (first, _) = recompile(&[UTILS, OTHER, MAIN], Results::default());
        let utils = (
            "app:utils",
            "fn Add(a: i32, b: i32) -> i32 { return b + a; }",
        );
        let (second, checked) = recompile(&[utils, OTHER, MAIN], first);
        assert_eq!(checked, ["app:utils"]);
        let reused: Vec<&str> = second.reused().map(|file| file.name.as_str()).collect();
        assert_eq!(
            reused,
            [
                "/peeper-build-test/src/other.peep",
                "/peeper-build-test/src/main.peep"
            ]
        );
    }

    #[test]
    fn a_signature_edit_checks_its_importers() {
        let (first, _) = recompile(&[UTILS, OTHER, MAIN], Results::default());
        let utils = (
            "app:utils",
            "fn Add(a: i64, b: i64) -> i64 { return a + b; }",
        );
        let (_, checked) = recompile(&[utils, OTHER, MAIN], first);
        assert_eq!(checked, ["app:utils", "app:main"]);
    }

    #[test]
    fn errors_come_and_go_with_edits() {
        let (first, _) = recompile(&[UTILS, OTHER, MAIN], Results::default());
        let broken = (
            "app:utils",
            "fn Add(a: i32, b: i32) -> i32 { let c: bool = a; return a + b; }",
        );
        // `utils` is checked and `Add` has an error; its interface is
        // unchanged, so `main` is reused and keeps its own error.
        let (second, checked) = recompile(&[broken, OTHER, MAIN], first);
        assert_eq!(checked, ["app:utils"]);
        let mismatch = |found: &str| format!("mismatched types: expected `bool`, found `{found}`");
        assert_eq!(messages(&second), [mismatch("i32"), mismatch("i32")]);
        let (third, checked) = recompile(&[UTILS, OTHER, MAIN], second);
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
        let (first, _) = recompile(&[utils, main], Results::default());
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
        let (first, _) = recompile(&[prelude, main], Results::default());
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
