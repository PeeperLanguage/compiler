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
use eval::interpreter::Failure;
use eval::interpreter::program::ProgramFacts;
use eval::interpreter::program::Stream;
use eval::interpreter::program::Written;
use eval::messages::PANIC_EXIT_CODE;
use model::DefId;
use model::ModuleId;
use model::ast;
use model::constant::Constant;
use model::machine::Machine;
use model::mir;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir;
use source::SourceFile;
use source::Span;

/// What a compile found, kept for the next compile of the same program.
#[derive(Default)]
pub struct Results {
    modules: Vec<ModuleResults>, // in the program's order
    /// The program's `main` and whether it gives the exit code, found by
    /// `run` for an entry module with no error in the program.
    entry_point: Option<(DefId, bool)>,
}

impl Results {
    /// Every diagnostic, module by module in the program's order.
    pub fn diagnostics(&self) -> impl Iterator<Item = &Diagnostic> {
        self.modules.iter().flat_map(|module| &module.diagnostics)
    }

    /// The entry module's `main` and whether it returns the program's exit
    /// code (`fn main() -> i32`), when the compile was given an entry module
    /// and found no error.
    pub fn entry_point(&self) -> Option<(DefId, bool)> {
        self.entry_point
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

/// What a program did when `interpret` ran it.
pub struct Interpreted {
    pub output: Vec<Written>, // with the panic text last, if it stopped with one
    pub exit_code: u8,        // as a program's parent on Linux sees it: the low 8 bits
}

/// Compiles `program` as `run` does, then runs it from `entry`'s `main` with
/// no backend or system (`eval::interpreter::program`). Gives what the compile
/// found and, when that left a program to start, what it did.
pub fn interpret(
    program: &project::Program,
    target: &'static Machine,
    entry: ModuleId,
) -> (Results, Option<Interpreted>) {
    let mut bodies = HashMap::new();
    let mut last_types = None;
    let results = run(
        program,
        target,
        Results::default(),
        Some(entry),
        false,
        |output| match output {
            Output::Function(CheckedFunction {
                definition,
                mir: Some(mir),
                ..
            }) => {
                bodies.insert(definition, mir);
            }
            // After each module's last function, so the last has every type.
            Output::Facts { types, .. } => last_types = Some(types.clone()),
            _ => {}
        },
    );
    let (Some((main, _)), Some(types)) = (results.entry_point(), last_types) else {
        return (results, None);
    };
    // A syntax error the parser read past is the loader's, and leaves a whole tree.
    if program.diagnostics.iter().any(Diagnostic::is_error) {
        return (results, None);
    }
    let modules = program.modules.as_slice();
    let extern_symbol = |function| project::extern_symbol(modules, function).ok().flatten();
    let struct_name = |definition| {
        let (name, file) = project::declared_name(modules, definition);
        name.text(file).to_string()
    };
    let field_names = |definition| {
        let names = project::field_names(modules, definition);
        names.map(str::to_string).collect()
    };
    let facts = ProgramFacts {
        extern_symbol: &extern_symbol,
        struct_name: &struct_name,
        field_names: &field_names,
    };
    let outcome = eval::interpreter::program::run(
        main,
        &bodies[&main],
        &types,
        target,
        &|function| bodies.get(&function),
        &facts,
    );
    let mut output = outcome.output;
    let exit_code = match outcome.ending {
        Ok(code) => code as u8,
        Err(Failure::Stopped {
            message,
            function,
            span,
        }) => {
            let file = &modules[function.module.index()].ast.file;
            let text = project::panic_text(&program.root, file, message.as_bytes(), span);
            output.push(Written {
                stream: Stream::Stderr,
                text,
            });
            PANIC_EXIT_CODE as u8
        }
        Err(_) => unreachable!("a checked program's functions run, and it exits by `Ok`"),
    };
    let interpreted = Interpreted { output, exit_code };
    (results, Some(interpreted))
}

/// Checks every declaration of `program`, lowers each function to MIR and
/// gives what it found. A module of `previous` (a compile of the same program
/// for the same `target`, or `Results::default()`) whose text and
/// whose imports' interfaces are unchanged keeps its diagnostics, and its
/// bodies are not checked again. What it checks goes to `use_output` in
/// declaration order, module by module: each function with a body as soon as
/// it is built, with the facts found so far when `record_facts`, then
/// dropped except its MIR, which is `use_output`'s: nothing here holds every
/// function's bodies at once. Each module constant goes once checked, with
/// its type and value, and last the module's remaining facts, none unless
/// `record_facts`. Integers as wide as a pointer (`isize`, `usize`) have
/// `target`'s `pointer_bits` bits. With an `entry` module, a program with no
/// other error must declare there a `main` it can start at (`entry_point`).
pub fn run(
    program: &project::Program,
    target: &'static Machine,
    previous: Results,
    entry: Option<ModuleId>,
    record_facts: bool,
    mut use_output: impl FnMut(Output),
) -> Results {
    let modules = program.modules.as_slice();
    let mut checker = Checker::new(modules, target, record_facts);
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
        // A constant's value depends on the bodies it ran, and a diagnostic
        // on the text of each file it points into, whose modules come before
        // (each was imported, by it or by one of those).
        let others_unchanged = |cached: &ModuleResults| {
            let reused = |path: &String| {
                *path == module.ast.file.path
                    || results
                        .iter()
                        .any(|done| done.file.path == *path && done.reused)
            };
            let pointed = cached.diagnostics.iter().flat_map(|diagnostic| {
                let fixes = diagnostic.notes.iter().flat_map(|note| note.fixes());
                let labels = diagnostic.labels.iter().map(|label| &label.file);
                labels.chain(fixes.map(|fix| &fix.file))
            });
            let pointed = pointed.map(|file| &file.path);
            cached.ran_modules.iter().chain(pointed).all(reused)
        };
        if let Some(cached) = cached
            && imports_unchanged
            && cached.file.text == module.ast.file.text
            && others_unchanged(&cached)
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
    let mut entry_point = None;
    if let Some(entry) = entry
        && !results
            .iter()
            .flat_map(|found| &found.diagnostics)
            .any(Diagnostic::is_error)
    {
        match find_entry_point(&mut checker, entry) {
            Ok(found) => entry_point = Some(found),
            Err(error) => results[entry.index()].diagnostics.push(error),
        }
    }
    Results {
        modules: results,
        entry_point,
    }
}

/// The `main` of the module `entry` and whether it returns the exit code, or
/// the error that it has none a program can start at.
fn find_entry_point(checker: &mut Checker, entry: ModuleId) -> Result<(DefId, bool), Diagnostic> {
    let module = &checker.modules[entry.index()];
    let tree = &module.ast;
    let main = tree
        .declarations()
        .enumerate()
        .find_map(|(index, declaration)| match tree.decls[declaration] {
            ast::Decl::Function(function)
                if function.body.is_some() && function.name.text(&tree.file) == "main" =>
            {
                Some((
                    DefId {
                        module: entry,
                        index: index as u32,
                    },
                    function.name.span,
                ))
            }
            _ => None,
        });
    let no_entry_point = |span: Span| {
        Diagnostic::new(
            diagnostics::INVALID_ENTRY_POINT,
            "program entrypoint must be a local body-backed `fn main()` or `fn main() -> i32`",
        )
        .label(&tree.file, span, "")
    };
    let Some((main, name)) = main else {
        return Err(no_entry_point(Span::at(0)));
    };
    let signature = checker.function_type(main, name);
    let exit_code = match &checker.types[signature] {
        Type::Function {
            parameters, result, ..
        } if parameters.is_empty() => match checker.types.real(*result) {
            TypeId::VOID => Some(false),
            TypeId::I32 => Some(true),
            _ => None,
        },
        _ => None,
    };
    exit_code
        .map(|exit_code| (main, exit_code))
        .ok_or_else(|| no_entry_point(name))
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
    for (index, declaration) in tree.declarations().enumerate() {
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
            variadic,
        } => {
            parameters.len().hash(hasher);
            for &parameter in parameters {
                hash_type(checker, parameter, hasher);
            }
            hash_type(checker, *result, hasher);
            origins.hash(hasher);
            variadic.hash(hasher);
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
        Type::Range { element, .. } => hash_type(checker, *element, hasher), // `step` follows
        Type::Reference { mutable, target }
        | Type::Slice {
            mutable,
            element: target,
        } => {
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
    for (index, declaration) in tree.declarations().enumerate() {
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
            for field in tree.children.lists.items(fields) {
                tree.children.fields[field]
                    .name
                    .text(&tree.file)
                    .hash(&mut hasher);
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
mod tests;
