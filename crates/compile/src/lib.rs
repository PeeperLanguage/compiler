//! A compile of a program: checks every declaration, each once, in the
//! order declarations ask for each other (docs/ir-design.md), and lowers each
//! function. The result of every query is kept in `check::Queries`, so a
//! declaration is checked the first time anything needs it.
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

use check::Queries;
use check::State;
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
use source::SourceFile;

/// What a compile found, kept for the next compile of the same program.
#[derive(Default)]
pub struct Results {
    modules: Vec<ModuleResults>, // in the program's order
}

impl Results {
    /// Every diagnostic, module by module in the program's order.
    pub fn diagnostics(&self) -> impl Iterator<Item = &Diagnostic> {
        self.modules.iter().flat_map(|module| &module.diagnostics)
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
    /// From building its scope, then checking it. Kept per module, not for
    /// the whole program, so the next compile can keep an unchanged module's
    /// and check only the others. Loading's errors are one list for the
    /// whole program (`project::Program`), since every load is done anew.
    diagnostics: Vec<Diagnostic>,
    reused: bool, // kept from the previous compile, not checked
}

impl ModuleResults {
    /// Nothing found yet in the module read from `file`.
    fn new(file: Arc<SourceFile>) -> ModuleResults {
        ModuleResults {
            file,
            interface: 0,
            diagnostics: Vec::new(),
            reused: false,
        }
    }
}

/// A function with a body, as a compile hands it on: built, used once, and
/// dropped after, except its MIR, which is handed over.
pub struct CheckedFunction<'c> {
    pub definition: DefId,
    pub signature: TypeId, // its function type
    pub thir: &'c thir::Body,
    /// The names written in it, when the compile records names (as for an
    /// editor); otherwise none.
    pub names: &'c [thir::Name],
    pub mir: Option<mir::Body>, // `None` when checking it found an error
    pub types: &'c Types,       // the program's types so far, which `signature` and the bodies name
}

/// How many bits a pointer has on the machine a program is built for: the
/// machine the compiler runs on, the only target for now. The driver and the
/// language server both compile with it, so an editor reports what a build
/// would.
pub const POINTER_BITS: u32 = usize::BITS;

/// Checks every declaration of `program`, lowers each function to MIR and
/// gives what it found. A module of `previous` (a compile of the same program
/// with the same `pointer_bits`, or `Results::default()`) whose text and
/// whose imports' interfaces are unchanged keeps its diagnostics, and its
/// bodies are not checked again. Each checked function with a body goes to
/// `use_function` as soon as it is built, in declaration order, module by
/// module, with its names when `record_names`; its THIR is dropped after and
/// its MIR is `use_function`'s: nothing here holds every function's bodies at
/// once. Integers as
/// wide as a pointer (`isize`, `usize`) have `pointer_bits` bits.
pub fn run(
    program: &project::Program,
    pointer_bits: u32,
    previous: Results,
    record_names: bool,
    mut use_function: impl FnMut(CheckedFunction),
) -> Results {
    let modules = program.modules.as_slice();
    let mut queries = Queries::new(modules, pointer_bits);
    let mut results: Vec<ModuleResults> = modules
        .iter()
        .map(|module| ModuleResults::new(module.ast.file.clone()))
        .collect();
    let mut previous: HashMap<String, ModuleResults> = previous
        .modules
        .into_iter()
        .map(|cached| (cached.file.path.clone(), cached))
        .collect();
    // Whether each module's interface differs from the previous compile's.
    let mut changed: Vec<bool> = Vec::with_capacity(modules.len());
    // One THIR body for every function, so its memory is reused.
    let mut thir = thir::Body::default();
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
            let found = &mut results[index];
            found.reused = true;
            // Its scope's errors come from the same text, so are the same;
            // what asking for its declarations found again is dropped.
            found.diagnostics = cached.diagnostics;
            found.interface = cached.interface;
            changed.push(false);
            continue;
        }
        for (index, declaration) in tree.lists.items(tree.top_level).enumerate() {
            let definition = DefId {
                module: id,
                index: index as u32,
            };
            match tree.decls[declaration] {
                ast::Decl::Function(function) => {
                    let ty = queries.function(definition);
                    let mut names = Vec::new(); // allocates only when a name is recorded
                    let recorded = record_names.then_some(&mut names);
                    let checked = queries.with_checker(id, |checker| {
                        checker.check_body(&function, ty, &mut thir, recorded)
                    });
                    if checked {
                        // Its MIR, path-checked; none when the signature had an
                        // error (reported) or it cannot be lowered (`lower::lower`).
                        let mir = match queries.types[ty] {
                            Type::Function {
                                ref parameters,
                                result,
                            } => lower::lower(&thir, parameters.len()).inspect(|mir| {
                                let file = &tree.file;
                                let diagnostics = &mut queries.diagnostics[id.index()];
                                lower::path_checks::report_initialization_errors(
                                    mir,
                                    file,
                                    diagnostics,
                                );
                                lower::path_checks::warn_unreachable_code(mir, file, diagnostics);
                                // Last: it points at the closing brace.
                                lower::path_checks::report_missing_return(
                                    mir,
                                    file,
                                    &queries.types,
                                    result,
                                    diagnostics,
                                );
                            }),
                            _ => None,
                        };
                        use_function(CheckedFunction {
                            definition,
                            signature: ty,
                            thir: &thir,
                            names: &names,
                            mir,
                            types: &queries.types,
                        });
                    }
                }
                ast::Decl::Const { .. } => {
                    queries.constant(definition);
                }
                // Nothing asks for a type declaration's type yet, so it is
                // no query: it is checked here, once.
                ast::Decl::Type(_) | ast::Decl::Alias(_) => {
                    queries.with_checker(id, |checker| checker.check_type_declaration(declaration));
                }
            }
        }
        let interface = interface(&queries, id);
        changed.push(previous_interface != Some(interface));
        let found = &mut results[index];
        found.interface = interface;
        found.diagnostics = std::mem::take(&mut queries.diagnostics[index]);
    }
    Results { modules: results }
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

/// A hash of what other modules see of `module`, once its declarations are
/// checked: its imports, and each declaration's kind, name, and type or
/// value. Its bodies are not part of it.
fn interface(queries: &Queries, module: ModuleId) -> u64 {
    let modules = queries.modules;
    let functions = &queries.functions[module.index()];
    let constants = &queries.constants[module.index()];
    let module = &modules[module.index()];
    let tree = &module.ast;
    let mut hasher = DefaultHasher::new();
    for import in &module.imports {
        modules[import.module.index()].path.hash(&mut hasher);
        import
            .name(module, modules)
            .map(|(name, _)| name)
            .hash(&mut hasher);
    }
    for (index, declaration) in tree.lists.items(tree.top_level).enumerate() {
        let decl = &tree.decls[declaration];
        std::mem::discriminant(decl).hash(&mut hasher);
        decl.name().text(&tree.file).hash(&mut hasher);
        // A declaration's result is in one table; the other slot is
        // `NotStarted`.
        if let State::Done(ty) = functions[index] {
            hash_type(&queries.types, ty, &mut hasher);
        }
        if let State::Done(Some(Constant { ty, value })) = &constants[index] {
            hash_type(&queries.types, *ty, &mut hasher);
            value.hash(&mut hasher);
        }
    }
    hasher.finish()
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::path::PathBuf;

    use super::CheckedFunction;
    use super::Results;
    use super::run;

    /// The program of `modules`, each an import path and its text, loaded as
    /// `peeper check` loads it from `app:main`, with every file given as open
    /// editor text so nothing is read from disk. Loading must find no error.
    fn load(modules: &[(&str, &str)]) -> project::Program {
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
            .diagnostics
            .iter()
            .map(|diagnostic| diagnostic.message.as_str())
            .collect();
        assert!(program.checkable, "{reported:?}");
        program
    }

    /// Checks the program of `modules` (see `load`) and gives each
    /// diagnostic as its code, message and the text of its first label.
    fn diagnostics(modules: &[(&str, &str)]) -> Vec<(&'static str, String, String)> {
        let found = run(&load(modules), 64, Results::default(), false, |_| {});
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

    /// Compiles the program of `modules` (see `load`) after `previous`;
    /// gives what it found and the paths of the modules whose bodies were
    /// checked, which every module of these tests has.
    fn recompile(modules: &[(&str, &str)], previous: Results) -> (Results, Vec<String>) {
        let loaded = load(modules);
        let mut checked: Vec<String> = Vec::new();
        let results = run(
            &loaded,
            64,
            previous,
            false,
            |CheckedFunction { definition, .. }| {
                let path = &loaded.modules[definition.module.index()].path;
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
