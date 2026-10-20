//! The scheduler: checks every declaration of a program, each once, in the
//! order declarations ask for each other (docs/ir-design.md). The result of
//! every query is kept in the `Compilation`, and `check` asks for them
//! through `check::Queries`, so a declaration is checked the first time
//! anything needs it.
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
use diagnostics::Diagnostic;
use model::Declared;
use model::DefId;
use model::ModuleId;
use model::State;
use model::ast;
use model::constant::Constant;
use model::mir;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir;
use source::SourceFile;

/// What a compile found, kept for the next compile of the same program.
#[derive(Default)]
pub struct Results {
    modules: Vec<ModuleResults>, // in the program's order
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

/// What a compile found in one module, kept for the next compile.
struct ModuleResults {
    file: Arc<SourceFile>, // its path tells the module's entry in the next compile
    interface: u64,        // a hash of what other modules see; set when the module is done
    scope_diagnostics: Vec<Diagnostic>, // from building its scope; redone every compile
    diagnostics: Vec<Diagnostic>, // from checking it; kept while it is reused
    reused: bool,          // kept from the previous compile, not checked
}

impl ModuleResults {
    /// Nothing found yet in the module read from `file`.
    fn new(file: Arc<SourceFile>) -> ModuleResults {
        ModuleResults {
            file,
            interface: 0,
            scope_diagnostics: Vec::new(),
            diagnostics: Vec::new(),
            reused: false,
        }
    }
}

/// Checks every declaration of `program`, lowers each function to MIR and
/// gives what it found. A module of `previous` (a compile of the same program
/// with the same `pointer_bits`, or `Results::default()`) whose text and
/// whose imports' interfaces are unchanged keeps its diagnostics, and its
/// bodies are not checked again. The signature of each checked function with
/// a body, its THIR, the names written in it (only when `record_names`, as for an
/// editor; otherwise none), and its MIR unless checking it found an error, go
/// to `use_function` as soon as they are built, in declaration order, and are
/// dropped after: nothing holds every function's bodies at once. Integers as
/// wide as a pointer (`isize`, `usize`) have `pointer_bits` bits.
pub fn compile(
    program: &project::Program,
    pointer_bits: u32,
    previous: Results,
    record_names: bool,
    mut use_function: impl FnMut(DefId, TypeId, &thir::Body, &[thir::Name], Option<&mir::Body>, &Types),
) -> Results {
    let modules = program.modules.as_slice();
    let mut compilation = Compilation {
        modules,
        types: Types::new(),
        pointer_bits,
        declarations: modules
            .iter()
            .map(|module| vec![State::NotStarted; module.ast.declarations.len()])
            .collect(),
        results: modules
            .iter()
            .map(|module| ModuleResults::new(module.ast.file.clone()))
            .collect(),
    };
    for (index, module) in modules.iter().enumerate() {
        let id = ModuleId::from_index(index);
        let diagnostics = &mut compilation.results[index].scope_diagnostics;
        let set = module
            .scope
            .set(check::scope::collect(id, modules, diagnostics));
        assert!(set.is_ok(), "a program is compiled once");
    }
    let mut previous: HashMap<String, ModuleResults> = previous
        .modules
        .into_iter()
        .map(|cached| (cached.file.path.clone(), cached))
        .collect();
    // Whether each module's interface differs from the previous compile's.
    let mut changed: Vec<bool> = Vec::with_capacity(modules.len());
    for (index, module) in modules.iter().enumerate() {
        let id = ModuleId::from_index(index);
        let tree = &module.ast;
        // An import comes before its importer, so it is decided already.
        let imports_unchanged = module
            .imports
            .iter()
            .all(|import| changed.get(import.module.index()) == Some(&false));
        let cached = previous.remove(&module.ast.file.path);
        let previous_interface = cached.as_ref().map(|cached| cached.interface);
        if let Some(cached) = cached
            && imports_unchanged
            && cached.file.text == module.ast.file.text
        {
            let found = &mut compilation.results[index];
            found.reused = true;
            found.diagnostics = cached.diagnostics;
            found.interface = cached.interface;
            changed.push(false);
            continue;
        }
        for (index, declaration) in tree.lists.items(tree.declarations).enumerate() {
            let definition = DefId {
                module: id,
                index: index as u32,
            };
            match tree.decls[declaration] {
                ast::Decl::Function(function) => {
                    let ty = compilation.declaration(definition).signature();
                    let mut names = Vec::new(); // allocates only when a name is recorded
                    let recorded = record_names.then_some(&mut names);
                    let thir = compilation
                        .with_checker(id, |checker| checker.body(&function, ty, recorded));
                    if let Some(thir) = thir {
                        let mir = compilation.lower(id, ty, &thir);
                        let types = &compilation.types;
                        use_function(definition, ty, &thir, &names, mir.as_ref(), types);
                    }
                }
                _ => {
                    compilation.declaration(definition);
                }
            }
        }
        let interface = compilation.interface(id);
        changed.push(previous_interface != Some(interface));
        compilation.results[index].interface = interface;
    }
    Results {
        modules: compilation.results,
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

/// One compile of a program: what it finds, beside the modules it reads.
/// Its tables are indexed by `ModuleId`. The query results die with the
/// compile; `results` is what the next compile is given.
struct Compilation<'p> {
    modules: &'p [project::Module], // in the program's order
    types: Types,
    pointer_bits: u32,
    /// The query of each top-level declaration, by its place (`DefId`).
    declarations: Vec<Vec<State<Declared>>>,
    results: Vec<ModuleResults>,
}

impl<'p> Compilation<'p> {
    /// Runs `work` on a checker of `module`; what it reports joins that
    /// module's diagnostics, unless the module was reused: it keeps the
    /// previous compile's, which asking for its declarations again would only
    /// repeat.
    fn with_checker<T>(
        &mut self,
        module: ModuleId,
        work: impl FnOnce(&mut Checker<Compilation<'p>>) -> T,
    ) -> T {
        let mut diagnostics = Vec::new();
        let mut checker = Checker {
            modules: self.modules,
            current_module: module,
            queries: self,
            diagnostics: &mut diagnostics,
        };
        let result = work(&mut checker);
        let found = &mut self.results[module.index()];
        if !found.reused {
            found.diagnostics.append(&mut diagnostics);
        }
        result
    }

    /// A hash of what other modules see of `module`, once its declarations
    /// are checked: its imports, and each declaration's kind, name, and type
    /// or value. Its bodies are not part of it.
    fn interface(&self, module: ModuleId) -> u64 {
        let declarations = &self.declarations[module.index()];
        let module = &self.modules[module.index()];
        let tree = &module.ast;
        let types = &self.types;
        let mut hasher = DefaultHasher::new();
        for import in &module.imports {
            self.modules[import.module.index()].path.hash(&mut hasher);
            import
                .name(module, self.modules)
                .map(|(name, _)| name)
                .hash(&mut hasher);
        }
        for (index, declaration) in tree.lists.items(tree.declarations).enumerate() {
            let decl = &tree.decls[declaration];
            std::mem::discriminant(decl).hash(&mut hasher);
            decl.name().text(&tree.file).hash(&mut hasher);
            match &declarations[index] {
                State::Done(Declared::Signature(ty)) => hash_type(types, *ty, &mut hasher),
                State::Done(Declared::Constant(Some(Constant { ty, value }))) => {
                    hash_type(types, *ty, &mut hasher);
                    value.hash(&mut hasher);
                }
                _ => {}
            }
        }
        hasher.finish()
    }

    /// Lowers `body`, the THIR of a function of `module` with the signature
    /// `ty`, to MIR and runs the path checks on it; `None` when it cannot be
    /// lowered (`lower::lower`).
    fn lower(&mut self, module: ModuleId, ty: TypeId, body: &thir::Body) -> Option<mir::Body> {
        let types = &self.types;
        let Type::Function { parameters, result } = &types[ty] else {
            return None; // the signature's error was reported
        };
        let tree = &self.modules[module.index()].ast;
        let mir = lower::lower(body, parameters.len())?;
        let diagnostics = &mut self.results[module.index()].diagnostics;
        lower::path_checks::initialization(&mir, &tree.file, diagnostics);
        lower::path_checks::unreachable_code(&mir, &tree.file, diagnostics);
        if let Some(error) = lower::path_checks::missing_return(&mir, &tree.file, types, *result) {
            diagnostics.push(error); // last: it points at the closing brace
        }
        Some(mir)
    }

    /// The result of the query of `definition`: the result already found,
    /// `on_cycle` when the query is asked for while it runs (a cycle,
    /// reported), or else what `work` finds with a checker of its module,
    /// kept for the next ask.
    fn query(
        &mut self,
        definition: DefId,
        on_cycle: Declared,
        work: impl FnOnce(&mut Checker<Compilation<'p>>, Id<ast::Decl>) -> Declared,
    ) -> Declared {
        let (module, index) = (definition.module.index(), definition.index as usize);
        // Cloned, so no borrow is held while the query runs and asks others.
        let state = self.declarations[module][index].clone();
        match state {
            State::Done(result) => return result,
            State::Running => {
                self.report_cycle(definition);
                return on_cycle;
            }
            State::NotStarted => {}
        }
        self.declarations[module][index] = State::Running;
        let declaration = self.modules[module].ast.declaration(definition.index);
        let result = self.with_checker(definition.module, |checker| work(checker, declaration));
        self.declarations[module][index] = State::Done(result.clone());
        result
    }

    /// Reports that the declaration `definition` was asked for while it was
    /// being checked: it depends on itself.
    fn report_cycle(&mut self, definition: DefId) {
        let found = &mut self.results[definition.module.index()];
        if found.reused {
            return;
        }
        let tree = &self.modules[definition.module.index()].ast;
        let name = tree.decls[tree.declaration(definition.index)].name();
        let text = name.text(&tree.file);
        // `A + A` asks for `A` twice while it runs; the cycle is reported once.
        let reported = |diagnostic: &Diagnostic| {
            diagnostic.code == diagnostics::CIRCULAR_DEPENDENCY
                && diagnostic.labels[0].span == name.span
        };
        if found.diagnostics.iter().any(reported) {
            return;
        }
        found.diagnostics.push(
            Diagnostic::new(
                diagnostics::CIRCULAR_DEPENDENCY,
                format!("`{text}` depends on itself"),
            )
            .label(&tree.file, name.span, ""),
        );
    }
}

impl Queries for Compilation<'_> {
    fn types(&mut self) -> &mut Types {
        &mut self.types
    }

    fn pointer_bits(&self) -> u32 {
        self.pointer_bits
    }

    fn declaration(&mut self, definition: DefId) -> Declared {
        let tree = &self.modules[definition.module.index()].ast;
        if let ast::Decl::Const { .. } = tree.decls[tree.declaration(definition.index)] {
            let work = |checker: &mut Checker<_>, declaration| {
                Declared::Constant(checker.constant(declaration))
            };
            self.query(definition, Declared::Constant(None), work)
        } else {
            let work = |checker: &mut Checker<_>, declaration| {
                Declared::Signature(checker.signature(declaration))
            };
            self.query(definition, Declared::Signature(TypeId::INVALID), work)
        }
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
        let project = project::Project {
            packages,
            entry: folder("app").join("main.peep"),
            root: PathBuf::from("/peeper-build-test"),
            name: "app".to_string(),
        };
        let program = project::load(&project, &open_files);
        let reported: Vec<&str> = program
            .load_diagnostics()
            .map(|diagnostic| diagnostic.message.as_str())
            .collect();
        assert!(program.checkable(), "{reported:?}");
        program
    }

    /// Checks the program of `modules` (see `program`) and gives each
    /// diagnostic as its code, message and the text of its first label.
    fn diagnostics(modules: &[(&str, &str)]) -> Vec<(&'static str, String, String)> {
        let found = compile(
            &program(modules),
            64,
            Results::default(),
            false,
            |_, _, _, _, _, _| {},
        );
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
        let results = compile(
            &program,
            64,
            previous,
            false,
            |definition, _, _, _, _, _| {
                let path = &program.modules[definition.module.index()].path;
                if !checked.contains(path) {
                    checked.push(path.clone());
                }
            },
        );
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
        let reused: Vec<&str> = second.reused().map(|file| file.path.as_str()).collect();
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
