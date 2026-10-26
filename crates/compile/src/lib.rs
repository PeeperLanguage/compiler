//! A compile of a program: checks every declaration, each once, in the
//! order declarations ask for each other (docs/ir-design.md), and lowers each
//! function. What each declaration's check finds is kept in `check::Checker`,
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

use check::Checker;
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
    /// The files of the modules whose functions its constants ran at compile
    /// time (`Checker::ran_modules`): it is reused only if they are.
    ran_modules: Vec<String>,
}

impl ModuleResults {
    /// Nothing found yet in the module read from `file`.
    fn new(file: Arc<SourceFile>) -> ModuleResults {
        ModuleResults {
            file,
            interface: 0,
            diagnostics: Vec::new(),
            reused: false,
            ran_modules: Vec::new(),
        }
    }
}

/// What a compile hands on as it goes (`run`), each piece used once and
/// dropped after.
#[expect(
    clippy::large_enum_variant,
    reason = "each is moved once into the callback, never stored; a box would allocate per function"
)]
pub enum Output<'c> {
    Function(CheckedFunction<'c>),
    /// A module constant `definition`, of a module checked, with its type and
    /// value; `None` when it had an error.
    Constant {
        definition: DefId,
        constant: Option<&'c Constant>,
        types: &'c Types, // the program's types so far, which `constant` names
    },
    /// The facts of `module` found after its last function was handed on;
    /// none unless the compile records facts (`ModuleChecks::facts`), and
    /// no name among them stands for a local.
    Facts {
        module: ModuleId,
        facts: &'c [thir::Fact],
        types: &'c Types, // the program's types so far, which the literals' types name
    },
}

/// A function with a body, as a compile hands it on: built, used once, and
/// dropped after, except its MIR, which is handed over.
pub struct CheckedFunction<'c> {
    pub definition: DefId,
    pub signature: TypeId, // its function type
    pub thir: &'c thir::Body,
    /// The facts of its module found since the last function was handed on:
    /// those of its text, and of the declarations checked meanwhile, when
    /// the compile records facts (as for an editor); otherwise none. Only
    /// this function's locals are among their names, explained by `thir`.
    pub facts: &'c [thir::Fact],
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
/// bodies are not checked again. What it checks goes to `use_output` in
/// declaration order, module by module: each function with a body as soon as
/// it is built, with the facts found so far when `record_facts`, then
/// dropped except its MIR, which is `use_output`'s: nothing here holds every
/// function's bodies at once. Each module constant goes once checked, with
/// its type and value, and last the module's remaining facts, none unless
/// `record_facts`. Integers as wide as a pointer (`isize`, `usize`) have
/// `pointer_bits` bits.
pub fn run(
    program: &project::Program,
    pointer_bits: u32,
    previous: Results,
    record_facts: bool,
    mut use_output: impl FnMut(Output),
) -> Results {
    let modules = program.modules.as_slice();
    let mut checker = Checker::new(modules, pointer_bits, record_facts);
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
        // An import comes before its importer, so it is decided already.
        let imports_unchanged = module
            .imports
            .iter()
            .all(|import| changed.get(import.module.index()) == Some(&false));
        let cached = previous.remove(&module.ast.file.path);
        let previous_interface = cached.as_ref().map(|cached| cached.interface);
        // A constant's value depends on the bodies it ran, whose modules come
        // before (each was imported, by it or by one of those).
        let ran_unchanged = |cached: &ModuleResults| {
            let reused = |path: &String| {
                *path == module.ast.file.path
                    || results
                        .iter()
                        .any(|done| done.file.path == *path && done.reused)
            };
            cached.ran_modules.iter().all(reused)
        };
        if let Some(cached) = cached
            && imports_unchanged
            && cached.file.text == module.ast.file.text
            && ran_unchanged(&cached)
        {
            let found = &mut results[index];
            found.reused = true;
            found.ran_modules = cached.ran_modules;
            // Its scope's errors come from the same text, so are the same;
            // what asking for its declarations found again is dropped.
            found.diagnostics = cached.diagnostics;
            found.interface = cached.interface;
            changed.push(false);
            continue;
        }
        compile_module(&mut checker, id, &mut thir, &mut use_output);
        let interface = interface(&checker, id);
        changed.push(previous_interface != Some(interface));
        let found = &mut results[index];
        found.interface = interface;
        let checked = &mut checker.module_checks[index];
        found.diagnostics = std::mem::take(&mut checked.diagnostics);
        let ran = checked.ran_modules.iter();
        found.ran_modules = ran
            .map(|ran| modules[ran.index()].ast.file.path.clone())
            .collect();
    }
    Results { modules: results }
}

/// Checks every declaration of `module`, each where it is declared, used or
/// not, compiles each function (`compile_function`) and gives each constant,
/// then the module's remaining facts, to `use_output`.
fn compile_module(
    checker: &mut Checker,
    module: ModuleId,
    thir: &mut thir::Body,
    use_output: &mut impl FnMut(Output),
) {
    let tree = &checker.modules[module.index()].ast;
    for (index, declaration) in tree.lists.items(tree.top_level).enumerate() {
        let definition = DefId {
            module,
            index: index as u32,
        };
        match tree.decls[declaration] {
            ast::Decl::Function(function) => {
                compile_function(checker, definition, &function, thir, use_output);
            }
            ast::Decl::Const { name, .. } => {
                let constant = checker.constant(definition, name.span);
                use_output(Output::Constant {
                    definition,
                    constant: constant.as_ref(),
                    types: &checker.types,
                });
            }
            ast::Decl::Type(written) | ast::Decl::Alias(written) => {
                checker.declared_type(definition, written.name.span);
            }
        }
    }
    let facts = std::mem::take(&mut checker.module_checks[module.index()].facts);
    use_output(Output::Facts {
        module,
        facts: &facts,
        types: &checker.types,
    });
}

/// Builds the function `definition`, written `function`, into `thir`
/// (`Checker::build_function`) and gives it to `use_output` with its MIR,
/// and with the facts found so far when the checker records facts. A
/// function without a body, or whose signature had an error, is not given.
fn compile_function(
    checker: &mut Checker,
    definition: DefId,
    function: &ast::Function,
    thir: &mut thir::Body,
    use_output: &mut impl FnMut(Output),
) {
    let Some(mir) = checker.build_function(definition, function.name.span, thir, false) else {
        return;
    };
    let facts = std::mem::take(&mut checker.module_checks[definition.module.index()].facts);
    use_output(Output::Function(CheckedFunction {
        definition,
        signature: checker.function_type(definition, function.name.span),
        thir,
        facts: &facts,
        mir,
        types: &checker.types,
    }));
}

/// Feeds `ty` to `hasher` by what it is, not by its id, which differs between
/// compiles: a named type by its module's path and its name, a struct also
/// by its fields' types.
fn hash_type(checker: &Checker, ty: TypeId, hasher: &mut DefaultHasher) {
    let ty = &checker.types[ty];
    std::mem::discriminant(ty).hash(hasher);
    match ty {
        Type::Integer(integer) => integer.hash(hasher),
        Type::Float(float) => float.hash(hasher),
        Type::Function {
            parameters,
            result,
            origins,
        } => {
            parameters.len().hash(hasher);
            for &parameter in parameters {
                hash_type(checker, parameter, hasher);
            }
            hash_type(checker, *result, hasher);
            origins.hash(hasher);
        }
        Type::Named(definition) | Type::Struct { definition, .. } => {
            checker.modules[definition.module.index()].path.hash(hasher);
            let (name, file) = project::declared_name(checker.modules, *definition);
            name.text(file).hash(hasher);
            if let Type::Struct { fields, .. } = ty {
                fields.len().hash(hasher);
                for &field in fields {
                    hash_type(checker, field, hasher);
                }
            }
        }
        Type::Array { element, length } => {
            length.hash(hasher);
            hash_type(checker, *element, hasher);
        }
        Type::Reference { mutable, target } => {
            mutable.hash(hasher);
            hash_type(checker, *target, hasher);
        }
        Type::RawPointer { target } => {
            target.is_some().hash(hasher);
            if let Some(target) = target {
                hash_type(checker, *target, hasher);
            }
        }
        Type::Invalid | Type::Void | Type::Bool | Type::CString => {}
        Type::Alias { .. } => unreachable!("indexing looks through aliases"),
    }
}

/// A hash of what other modules see of `module`, once its declarations are
/// checked: its imports, and each declaration's kind, name, and type, value
/// or struct fields. Its bodies are not part of it.
fn interface(checker: &Checker, module: ModuleId) -> u64 {
    let modules = checker.modules;
    let checked = &checker.module_checks[module.index()];
    let (declared_types, constants) = (&checked.declared_types, &checked.constants);
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
        // A declaration's result is in one table; the other slots are
        // `NotStarted`.
        if let State::Done(ty) = declared_types[index] {
            hash_type(checker, ty, &mut hasher);
        }
        // `Type::Struct` holds only the fields' types; their names are in the tree.
        if let ast::Decl::Type(declaration) = decl
            && let ast::Type::Struct(fields) = tree.types[declaration.ty]
        {
            for field in tree.lists.items(fields) {
                tree.fields[field].name.text(&tree.file).hash(&mut hasher);
            }
        }
        if let State::Done(Some(Constant { ty, value })) = &constants[index] {
            hash_type(checker, *ty, &mut hasher);
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
    use super::Output;
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

    /// A cycle gets one error with a place for each step, however often it
    /// is closed (`A + A` closes it twice); a figure 8 is two cycles.
    #[test]
    fn a_cycle_is_reported_once_with_its_path() {
        let program = load(&[(
            "app:main",
            "const A = A + A;
            const B = C + D;
            const C = B;
            const D = B;
            fn main() { let x = A + B; }",
        )]);
        let found = run(&program, 64, Results::default(), false, |_| {});
        let errors: Vec<(String, Vec<(String, String)>)> = found
            .diagnostics()
            .map(|diagnostic| {
                let labels = diagnostic.labels.iter();
                let label = |label: &diagnostics::Label| {
                    (
                        label.file.slice(label.span).to_string(),
                        label.message.clone(),
                    )
                };
                (diagnostic.message.clone(), labels.map(label).collect())
            })
            .collect();
        let error = |message: &str, labels: &[(&str, &str)]| {
            let labels = labels
                .iter()
                .map(|&(text, label)| (text.to_string(), label.to_string()));
            (message.to_string(), labels.collect::<Vec<_>>())
        };
        assert_eq!(
            errors,
            [
                error(
                    "`A` depends on itself",
                    &[
                        ("A", "`A` is defined here"),
                        ("A", "`A` uses `A`, closing the cycle"),
                    ]
                ),
                error(
                    "`B` depends on itself",
                    &[
                        ("B", "`B` is defined here"),
                        ("C", "`B` uses `C`"),
                        ("B", "`C` uses `B`, closing the cycle"),
                    ]
                ),
                error(
                    "`B` depends on itself",
                    &[
                        ("B", "`B` is defined here"),
                        ("D", "`B` uses `D`"),
                        ("B", "`D` uses `B`, closing the cycle"),
                    ]
                ),
            ]
        );
    }

    #[test]
    fn module_constants_cannot_be_assigned() {
        let found = diagnostics(&[
            ("core:global", "const Stdout: i32 = 1;"),
            (
                "app:main",
                "const Limit: i32 = 1;\nfn main() { Limit = 2; Stdout = 3; main = 4; }",
            ),
        ]);
        assert_eq!(
            found,
            expected(&[
                ("T0018", "cannot assign to constant `Limit`", "Limit"),
                ("T0018", "cannot assign to constant `Stdout`", "Stdout"),
                (
                    "T0007",
                    "cannot assign to `main`, which is not a variable",
                    "main"
                ),
            ])
        );
    }

    /// The prelude's exported names are in every module's scope; its private
    /// names are not, unqualified or through an import.
    #[test]
    fn the_prelude_exports_only_upper_case_names() {
        let found = diagnostics(&[
            (
                "core:global",
                "const Stdout: i32 = 1;
                const hidden: i32 = 2;
                #[extern(\"peeper_rt_v1_exit\")]
                fn Exit(code: i32);",
            ),
            (
                "app:main",
                "import \"core:global\";
                fn main() { Exit(Stdout); global::Exit(global::Stdout); let Exit = hidden; global::hidden; }",
            ),
        ]);
        assert_eq!(
            found,
            expected(&[
                ("T0002", "unknown name `hidden`", "hidden"),
                ("M0004", "`hidden` is not exported from `global`", "hidden"),
            ])
        );
    }

    /// A struct declared in another module, here the prelude, has the field
    /// names of its own module's tree.
    #[test]
    fn a_prelude_struct_has_its_own_fields() {
        let found = diagnostics(&[
            (
                "core:global",
                "const Stdout: i32 = 1;\nstruct Pair { first: i32, second: i64 }",
            ),
            (
                "app:main",
                "fn main() {
                    let pair = Pair.{ second = 2, first = 1 };
                    let second: i64 = pair.second;
                    let wrong = pair.secnd;
                }",
            ),
        ]);
        assert_eq!(
            found,
            expected(&[("T0010", "`Pair` has no field `secnd`", "secnd")])
        );
    }

    /// Compiles the program of `modules` (see `load`) after `previous`;
    /// gives what it found and the paths of the modules whose bodies were
    /// checked, which every module of these tests has.
    fn recompile(modules: &[(&str, &str)], previous: Results) -> (Results, Vec<String>) {
        let loaded = load(modules);
        let mut checked: Vec<String> = Vec::new();
        let results = run(&loaded, 64, previous, false, |output| {
            let Output::Function(CheckedFunction { definition, .. }) = output else {
                return;
            };
            let path = &loaded.modules[definition.module.index()].path;
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
    fn a_struct_field_edit_checks_its_importers() {
        let utils =
            |point: &str| format!("fn Add(a: i32, b: i32) -> i32 {{ return a + b; }}\n{point}");
        let (start, retyped, renamed) = (
            utils("struct Point { x: i32 }"),
            utils("struct Point { x: i64 }"),
            utils("struct Point { y: i64 }"),
        );
        let (first, _) = recompile(&[("app:utils", &start), OTHER, MAIN], Results::default());
        let (second, checked) = recompile(&[("app:utils", &retyped), OTHER, MAIN], first);
        assert_eq!(checked, ["app:utils", "app:main"], "a field's type");
        let (_, checked) = recompile(&[("app:utils", &renamed), OTHER, MAIN], second);
        assert_eq!(checked, ["app:utils", "app:main"], "a field's name");
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

    /// A constant's value from a call depends on the called body, which no
    /// interface holds: an edit of it checks the constant's module again.
    #[test]
    fn a_body_edit_checks_the_modules_whose_constants_ran_it() {
        let main = (
            "app:main",
            "import \"app:utils\";
            const DIVISOR = utils::Divisor();
            fn main() { let half = 1 / DIVISOR; }",
        );
        let zero = ("app:utils", "fn Divisor() -> i32 { return 0; }");
        let (first, _) = recompile(&[zero, main], Results::default());
        assert_eq!(messages(&first), ["division by zero"]);
        let two = ("app:utils", "fn Divisor() -> i32 { return 2; }");
        let (second, checked) = recompile(&[two, main], first);
        assert_eq!(checked, ["app:utils", "app:main"]);
        assert!(messages(&second).is_empty(), "{:?}", messages(&second));
    }

    /// The same through a function of another module that the call reaches.
    #[test]
    fn a_body_edit_checks_constants_that_reach_it_through_others() {
        let utils = (
            "app:utils",
            "import \"app:other\";\nfn Divisor() -> i32 { return other::Value(); }",
        );
        let main = (
            "app:main",
            "import \"app:utils\";
            const DIVISOR = utils::Divisor();
            fn main() { let half = 1 / DIVISOR; }",
        );
        let zero = ("app:other", "fn Value() -> i32 { return 0; }");
        let (first, _) = recompile(&[zero, utils, main], Results::default());
        assert_eq!(messages(&first), ["division by zero"]);
        let two = ("app:other", "fn Value() -> i32 { return 2; }");
        let (second, checked) = recompile(&[two, utils, main], first);
        assert_eq!(checked, ["app:other", "app:main"]);
        assert!(messages(&second).is_empty(), "{:?}", messages(&second));
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

    /// A function run at compile time and then compiled records its facts
    /// once, each with the function that explains its locals; a declaration
    /// first checked while such a function is built (`inner`'s signature)
    /// records its facts too.
    #[test]
    fn a_function_run_at_compile_time_records_its_facts_once() {
        let program = load(&[(
            "app:main",
            "const BIG = twice(2);
            fn twice(n: i32) -> i32 { let doubled = inner(n) * 2; return doubled; }
            fn inner(n: i32) -> i32 { return n; }",
        )]);
        let tree = &program.modules[0].ast;
        let mut found: Vec<(&str, &str)> = Vec::new();
        run(&program, 64, Results::default(), true, |output| {
            let (owner, facts) = match output {
                Output::Function(CheckedFunction {
                    definition, facts, ..
                }) => {
                    let declaration = &tree.decls[tree.declaration(definition.index)];
                    (declaration.name().text(&tree.file), facts)
                }
                Output::Facts { facts, .. } => ("module", facts),
                Output::Constant { .. } => return,
            };
            found.extend(facts.iter().map(|fact| (tree.file.slice(fact.span), owner)));
        });
        // `twice` gets the facts found until it is handed on: `BIG`'s value,
        // both signatures, its body; `inner` its body.
        let expected = [
            ("twice", "twice"),
            ("i32", "twice"),
            ("i32", "twice"),
            ("2", "twice"),
            ("i32", "twice"),
            ("i32", "twice"),
            ("n", "twice"),
            ("inner", "twice"),
            ("n", "twice"),
            ("2", "twice"),
            ("doubled", "twice"),
            ("doubled", "twice"),
            ("n", "inner"),
            ("n", "inner"),
        ];
        assert_eq!(found, expected);
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
