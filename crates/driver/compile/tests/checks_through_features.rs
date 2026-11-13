//! The checker's tests that need a feature to reach core behaviour. The
//! checker's own tests are written in a language with no feature, as the
//! `features` crate depends on `check`; these are written in Peeper, through the
//! checker's public calls.

use std::sync::Arc;

use check::Checker;
use check::scope::collect;
use features::Peeper;
use model::ConstArguments;
use model::DefId;
use model::FunctionCopy;
use model::ModuleId;
use model::machine::Machine;
use model::program::Import;
use model::program::ImportKind;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir::Body;
use model::thir::Symbol;
use source::SourceFile;
use syntax::ast;

/// The module `path`, written as `text`, with `imports`; its text must parse
/// without errors.
fn module(path: &str, text: &str, imports: Vec<Import>) -> model::program::Module<Peeper> {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (ast, parse) = parser::parse(&file);
    assert!(parse.is_empty(), "{parse:?}");
    model::program::Module {
        path: path.to_string(),
        ast,
        imports,
    }
}

/// A diagnostic as its code, message and the text of its first label.
type Reported = (&'static str, String, String);

/// Runs `work` on a checker of the module `app:main` written as `text`, given
/// its id, which imports `app:utils` as `utils` (an import added after
/// `text`, so the places in `text` stay as written), which declares `Shown`
/// and the private `hidden`. Gives what `work` gives, the program's types and
/// the diagnostics.
fn with_checker<T>(
    text: &str,
    work: impl FnOnce(&mut Checker<Peeper>, ModuleId) -> T,
) -> (T, Types, Vec<Reported>) {
    let (utils_id, main_id) = (ModuleId::from_index(0), ModuleId::from_index(1));
    let imports = vec![Import {
        kind: ImportKind::Declared(0),
        module: utils_id,
    }];
    let text = format!("{text}\nimport \"app:utils\";\n");
    let modules = [
        module("app:utils", "fn Shown() {}\nfn hidden() {}", Vec::new()),
        module("app:main", &text, imports),
    ];
    let file = modules[1].ast.file.clone();
    let mut checker = Checker::new(&modules, Machine::named("x86_64-linux").unwrap(), false);
    let result = work(&mut checker, main_id);
    let reported = checker
        .module_checks
        .iter()
        .flat_map(|module| &module.diagnostics)
        .map(|diagnostic| {
            let text = file.slice(diagnostic.labels[0].span).to_string();
            (diagnostic.code.id, diagnostic.message.clone(), text)
        })
        .collect();
    (result, checker.types, reported)
}

/// `expected` as `Reported`s, to compare with what was reported.
fn reported(expected: &[(&'static str, &str, &str)]) -> Vec<Reported> {
    let owned = |&(code, message, text): &(&'static str, &str, &str)| {
        (code, message.to_string(), text.to_string())
    };
    expected.iter().map(owned).collect()
}

/// The type of every function and type declaration of `text`, as type
/// text, and the diagnostics.
fn declared_types(text: &str) -> (Vec<String>, Vec<Reported>) {
    let (found, types, reported) = with_checker(text, |checker, main| {
        let tree = &checker.modules[main.index()].ast;
        let found: Vec<TypeId> = tree
            .declarations()
            .enumerate()
            .map(|(index, declaration)| {
                let definition = DefId {
                    module: main,
                    index: index as u32,
                };
                // As a compile asks, so a use does not report it again.
                let written = tree.decls[declaration];
                let name_span = written.name().span;
                match written {
                    ast::Decl::Function(_) => checker.function_type(definition, name_span),
                    _ => checker.declared_type(definition, name_span),
                }
            })
            .collect();
        found
    });
    let text_of = |ty: TypeId| match &types[ty] {
        Type::Function {
            parameters, result, ..
        } => format!("fn{parameters:?} -> {result:?}"),
        other => format!("{other:?}"),
    };
    (found.into_iter().map(text_of).collect(), reported)
}

/// The declaration `index` of the module at place `module`.
fn definition(module: usize, index: u32) -> DefId {
    DefId {
        module: ModuleId::from_index(module),
        index,
    }
}

// From `check::declaration`.

#[test]
fn errors_are_located() {
    let (found, found_reported) = declared_types(
        "fn a(x: Missing) {}\nfn b(m: utils) {}\nfn c(r: &i32) -> &i32 in q {}\nfn d<T>(x: T) {}\nfn e(x: i32 = 1) {}\nstruct P { x: i32 }\n#[extern(\"f\")]\nfn f(p: P);\nfn ok(p: P) -> P { return p; }\nfn g(x: a) {}\nfn h(s: string) {}\nfn i(&self) {}\nfn P::j(a: i32, mut self) {}\n",
    );
    let expected = reported(&[
        ("T0002", "unknown type `Missing`", "Missing"),
        ("T0021", "`utils` is a module, not a type", "utils"),
        ("T0002", "`q` is not a parameter", "q"),
        ("T0043", "generic functions are not supported yet", "T"),
        ("T0043", "default parameters are not supported yet", "1"),
        (
            "T0043",
            "struct, array, range, slice and `&str` parameters and results of `extern` functions are not supported yet",
            "P",
        ),
        ("T0021", "`a` is a value, not a type", "a"),
        ("T0043", "string types are not supported yet", "string"),
        (
            "P0005",
            "only a type's function takes `self`: `fn Point::name(&self)`",
            "&self",
        ),
        ("P0005", "`self` must be the first parameter", "self"),
    ]);
    assert_eq!(found_reported, expected);
    let invalid = format!("{:?}", TypeId::INVALID);
    assert_eq!(
        found[0],
        format!("fn[{invalid}] -> {:?}", TypeId::VOID),
        "an error stays inside the signature"
    );
    assert_eq!(found[3], "Invalid");
}

/// `extern` on a function is the one attribute there is.
#[test]
fn other_attributes_are_reported() {
    let (_, found_reported) = declared_types("#[inline]\nfn f();\n#[extern(\"s\")]\nstruct S {}\n");
    assert_eq!(
        found_reported,
        reported(&[
            ("P0005", "unknown attribute `inline`", "#[inline]"),
            (
                "P0005",
                "a function needs a body, or `#[extern(\"name\")]` when C defines it",
                "f"
            ),
            (
                "P0005",
                "`extern` names a C function, so only a function has it",
                "#[extern(\"s\")]"
            ),
        ])
    );
}

/// An `extern` of another shape is reported, and its function still gets
/// the C rules.
#[test]
fn a_malformed_extern_is_reported() {
    let (_, found_reported) = declared_types(
        "struct P { x: i32 }\n#[extern(1)]\nfn a();\n#[extern(\"b\", 2)]\nfn b();\n#[extern(\"c\")]\n#[extern(\"d\")]\nfn c(p: P);\n",
    );
    let shape = "write `#[extern(\"name\")]` once, with the C function's name";
    let expected = reported(&[
        ("P0005", shape, "#[extern(1)]"),
        ("P0005", shape, "#[extern(\"b\", 2)]"),
        ("P0005", shape, "#[extern(\"d\")]"),
        (
            "T0043",
            "struct, array, range, slice and `&str` parameters and results of `extern` functions are not supported yet",
            "P",
        ),
    ]);
    assert_eq!(found_reported, expected);
}

// From `check::scope`.

#[test]
fn declarations_and_imports() {
    let text = "import \"app:utils\";\nfn main() { const K: i32 = 1; later(); }\nfn later() {}\nconst Limit: i32 = 4;\nstruct Point { x: i32 }\nfn Point::length(&self) -> i32 { return 0; }\nfn Point::origin() -> Point { return .{ x = 0 }; }\n";
    let (utils, global) = (ModuleId::from_index(0), ModuleId::from_index(1));
    let imports = vec![
        Import {
            kind: ImportKind::Declared(0),
            module: utils,
        },
        Import {
            kind: ImportKind::Prelude,
            module: global,
        },
    ];
    let modules = [
        module("app:utils", "", Vec::new()),
        module("core:global", "fn global() {}", Vec::new()),
        module("app:main", text, imports),
    ];
    let mut diagnostics = Vec::new();
    let scope = collect(ModuleId::from_index(2), &modules, &mut diagnostics);
    assert!(diagnostics.is_empty());
    // A declaration's place counts top-level declarations only: `main`'s
    // body `const` does not move `later`.
    assert_eq!(
        scope.get(None, "main"),
        Some(Symbol::Function(definition(2, 0)))
    );
    assert_eq!(
        scope.get(None, "later"),
        Some(Symbol::Function(definition(2, 1)))
    );
    assert_eq!(
        scope.get(None, "Limit"),
        Some(Symbol::Constant(definition(2, 2)))
    );
    assert_eq!(
        scope.get(None, "Point"),
        Some(Symbol::Type(definition(2, 3)))
    );
    assert_eq!(scope.get(None, "utils"), Some(Symbol::Module(utils)));
    assert_eq!(
        scope.get(None, "length"),
        None,
        "a method is not a module name"
    );
    let point = Some(definition(2, 3));
    assert_eq!(
        scope.get(point, "origin"),
        Some(Symbol::Function(definition(2, 5)))
    );
    assert_eq!(
        scope.get(None, "origin"),
        None,
        "a type's function is not a module name"
    );
    assert_eq!(
        scope.get(None, "global"),
        None,
        "the prelude is not copied in"
    );
    assert_eq!(scope.parent, Some(global), "the prelude's scope comes next");
}

#[test]
fn a_type_function_needs_a_struct_of_its_module() {
    // The owner may be declared below; anything but a struct of this
    // module is refused, and a function declared twice in a type is
    // reported; the module's own `a` is another name.
    let text = "fn Point::a() {}\nstruct Point { x: i32 }\nfn Point::a() {}\nconst Limit: i32 = 1;\nfn Limit::b() {}\nfn Missing::c() {}\ntype Other = Point;\nfn Other::d() {}\nfn i32::e() {}\nfn a() {}\n";
    let modules = [module("app:main", text, Vec::new())];
    let mut diagnostics = Vec::new();
    let scope = collect(ModuleId::from_index(0), &modules, &mut diagnostics);
    let point = Some(definition(0, 1));
    assert_eq!(
        scope.get(point, "a"),
        Some(Symbol::Function(definition(0, 0)))
    );
    assert_eq!(
        scope.get(None, "a"),
        Some(Symbol::Function(definition(0, 9)))
    );
    let messages: Vec<&str> = diagnostics.iter().map(|d| d.message.as_str()).collect();
    assert_eq!(
        messages,
        [
            "`a` is already declared in this type",
            "`Limit` is not a struct declared in this module",
            "`Missing` is not a struct declared in this module",
            "`Other` is not a struct declared in this module",
            "`i32` is not a struct declared in this module",
        ]
    );
}

// From `check::statement`.

/// The diagnostics of checking every function body of `text`.
fn body_reports(text: &str) -> Vec<Reported> {
    let ((), _, reported) = with_checker(text, |checker, main| {
        let tree = &checker.modules[main.index()].ast;
        for (index, declaration) in tree.declarations().enumerate() {
            let ast::Decl::Function(function) = tree.decls[declaration] else {
                continue;
            };
            let definition = DefId {
                module: main,
                index: index as u32,
            };
            checker.check_body(
                FunctionCopy {
                    definition,
                    arguments: ConstArguments::NONE,
                },
                function.name.span,
                &mut Body::default(),
                true,
            );
        }
    });
    reported
}

#[test]
fn temporaries_cannot_be_written_or_addressed() {
    let found = body_reports(
        "fn make() -> [2]i32 { return [2]i32{1, 2}; }
            fn f() {
                make()[0] = 5;
                let p = @make()[0];
                let q = @5;
            }",
    );
    let expected = reported(&[
        ("T0007", "cannot assign to a temporary value", "make()[0]"),
        (
            "T0004",
            "cannot take address of a temporary value",
            "@make()[0]",
        ),
        ("T0004", "cannot take address of a temporary value", "@5"),
    ]);
    assert_eq!(found, expected);
}
