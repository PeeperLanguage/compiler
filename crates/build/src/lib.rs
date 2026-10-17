//! The scheduler: checks every declaration of a program, each once, in the
//! order declarations ask for each other (docs/ir-design.md). It owns the
//! result of every query, and `check` asks for them through `check::Queries`,
//! so a declaration is checked the first time anything needs it.

use std::collections::HashMap;

use arena::Id;
use check::Checker;
use check::Queries;
use check::scope::ModuleScope;
use diagnostics::Diagnostic;
use model::DefId;
use model::mir;
use model::syntax;
use model::types::Type;
use model::types::TypeId;
use model::types::Types;
use model::value::Constant;

/// Checks every declaration of `program`, lowers each function to MIR and
/// gives the diagnostics. The MIR of each function with a complete body goes
/// to `use_mir` as soon as it is built, in declaration order, and is dropped
/// after: nothing holds every function's MIR at once. Integers as wide as a
/// pointer (`isize`, `usize`) have `pointer_bits` bits.
pub fn compile(
    program: &project::Program,
    pointer_bits: u32,
    mut use_mir: impl FnMut(DefId, &mir::Body, &Types),
) -> Vec<Diagnostic> {
    let modules: Vec<&project::Module> = program
        .order
        .iter()
        .map(|path| &program.modules[path])
        .collect();
    let indexes: HashMap<&str, u32> = (0..)
        .zip(&modules)
        .map(|(index, module)| (module.path.as_str(), index))
        .collect();
    let mut diagnostics = Vec::new();
    let scopes: Vec<ModuleScope> = (0..)
        .zip(&modules)
        .map(|(index, module)| check::scope::collect(index, module, &indexes, &mut diagnostics))
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
        diagnostics,
    };
    for (index, module) in (0..).zip(&program.modules) {
        let tree = &module.syntax;
        for declaration in tree.lists.items(tree.declarations) {
            let definition = DefId {
                module: index,
                index: declaration.index() as u32,
            };
            match tree.decls[declaration] {
                syntax::Decl::Function(function) => {
                    if let Some(body) = build.function(definition, &function) {
                        use_mir(definition, &body, &build.types);
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
    }
    build.diagnostics
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
    diagnostics: Vec<Diagnostic>,
}

impl<'p> Build<'p> {
    /// Runs `work` on a checker of the module at `module`; what it reports
    /// joins the program's diagnostics.
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
        self.diagnostics.append(&mut diagnostics);
        result
    }

    /// Checks the function `definition`, lowers its body to MIR and runs the
    /// path checks on it; `None` when it has no body or checking it found an
    /// error.
    fn function(&mut self, definition: DefId, function: &syntax::Function) -> Option<mir::Body> {
        let ty = self.signature(definition);
        let body = self.with_checker(definition.module, |checker| checker.body(function, ty))?;
        let Type::Function { parameters, result } = &self.types[ty] else {
            return None; // the signature's error was reported
        };
        let tree = &self.program.modules[definition.module as usize].syntax;
        let span = tree.blocks.span(function.body?);
        let mir = lower::lower(&body, parameters.len(), *result, span)?;
        // Only a `let` without a value can be unset or assigned twice.
        let late = |index| body.locals[Id::from_index(index)].declared_without_value;
        if (0..body.locals.len()).any(late) {
            lower::path_checks::initialization(&mir, &tree.file, &mut self.diagnostics);
        }
        if let Some(error) =
            lower::path_checks::missing_return(&mir, &tree.file, &self.types, *result)
        {
            self.diagnostics.push(error); // last: it points at the closing brace
        }
        Some(mir)
    }

    /// Reports that the declaration `definition` was asked for while it was
    /// being checked: it depends on itself.
    fn report_cycle(&mut self, definition: DefId) {
        let tree = &self.program.modules[definition.module as usize].syntax;
        let name = tree.decls[Id::from_index(definition.index as usize)].name();
        let text = tree.file.slice(name);
        self.diagnostics.push(
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
        let found = compile(&program(modules), 64, |_, _, _| {});
        found
            .iter()
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
}
