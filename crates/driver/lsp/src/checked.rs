//! What the last check found, kept for the next one and for requests: the
//! compile results, and each module's facts by name.

use features::Peeper;
use std::collections::HashMap;
use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;

use lsp_types::CodeAction;
use model::FunctionCopy;
use model::ModuleId;
use model::semantic_types::ConstArgument;
use model::semantic_types::Constant;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::semantic_types::value_text;
use model::thir;
use model::thir::FactKind;
use model::thir::Local;
use model::thir::Symbol;
use source::Span;
use std::fmt::Write;
use syntax::ast;

use crate::host;

/// What the server keeps of a program between checks.
#[derive(Default)]
pub(crate) struct Checked {
    pub(crate) results: compile::Results, // reused by the next compile
    pub(crate) published: HashSet<PathBuf>, // the files last given diagnostics
    /// The program as last compiled, and the facts of each of its modules'
    /// text and the names of its constants, by file: what definition and
    /// hover read.
    pub(crate) program: Option<model::program::Program<Peeper>>,
    pub(crate) facts: HashMap<PathBuf, Vec<CachedFact>>,
    pub(crate) fix_all: HashMap<PathBuf, CodeAction>, // each file's sure fixes, as of the last check
}
impl Checked {
    /// Compiles `program` again, reusing what the last compile found, and
    /// keeps the facts of each module's text and the names of its
    /// constants: a module the compile reused gave no facts, and the facts
    /// found before still fit it; a module checked again gets its new facts.
    pub(crate) fn recompile(&mut self, program: &model::program::Program<Peeper>) {
        let mut found: HashMap<ModuleId, Vec<CachedFact>> = HashMap::new();
        let previous = std::mem::take(&mut self.results);
        let mut cache_facts = |output: compile::Output| match output {
            compile::Output::Function(compile::CheckedFunction {
                module,
                thir,
                facts,
                types,
                ..
            }) => {
                let facts = facts
                    .iter()
                    .map(|fact| CachedFact::new(program, fact, Some(thir), types));
                found.entry(module).or_default().extend(facts);
            }
            compile::Output::Constant {
                definition,
                constant,
                types,
            } => {
                let declared = model::program::declared_name(&program.modules, definition)
                    .0
                    .span;
                let text = constant.map(|constant| ConstantText::new(program, constant, types));
                let kind = CachedKind::Constant { text };
                let name = CachedFact {
                    span: declared,
                    kind,
                };
                found.entry(definition.module).or_default().push(name);
            }
            compile::Output::Facts {
                module,
                facts,
                types,
            } => {
                let facts = facts
                    .iter()
                    .map(|fact| CachedFact::new(program, fact, None, types));
                found.entry(module).or_default().extend(facts);
            }
        };
        // shortcut: the editor checks for the host, so a `usize` constant that
        // fits it but not a `-target i686-linux` build is reported only by that
        // build; check for a project's target when a project can name one.
        self.results = compile::run(
            program,
            host(),
            previous,
            compile::Options {
                record_facts: true,
                ..compile::Options::default()
            },
            &mut cache_facts,
        );
        let reused: HashSet<&Path> = self
            .results
            .reused()
            .map(|file| Path::new(&file.path))
            .collect();
        self.facts.retain(|file, _| reused.contains(file.as_path()));
        self.facts.extend(found.into_iter().map(|(module, found)| {
            let file = &program.modules[module.index()].ast.file.path;
            (PathBuf::from(file), found)
        }));
    }
}
/// What the check found about the text at `span` of a module: a name and
/// what it stands for, a module constant's name, or a literal's type. Kept as
/// long as its module is reused: by file and place, not by the ids of one
/// compile.
#[derive(Clone)]
pub(crate) struct CachedFact {
    pub(crate) span: Span,
    pub(crate) kind: CachedKind,
}
#[derive(Clone)]
pub(crate) enum CachedKind {
    /// A variable of a body, this name being its declaration: whether it is
    /// `mut`, and its type's text; `None` for a type the check did not
    /// find, as after an error, or in a function's own body for a type of
    /// its `const T: type`, which only a copy knows.
    Variable { mutable: bool, ty: Option<String> },
    /// A `const` of a body or of the module, this name being its
    /// declaration, with its checked type and value; `None` when it had an
    /// error.
    Constant { text: Option<ConstantText> },
    /// A use of a local declared at `declared`, whose name holds what is
    /// known of it, kept once.
    Local { declared: Span },
    /// The declaration at `position` among the top-level declarations of the
    /// module read from `file`. Its importer is reused only while those keep
    /// their kinds and names (`compile::run`), so the position stays right;
    /// an arena index would not, as a body's `const` shifts it.
    /// `copy`: at a call that made a copy of a function with type or
    /// `const` parameters, that copy's signature (`copy_signature`).
    Declaration {
        file: PathBuf,
        position: u32,
        copy: Option<String>,
    },
    /// The module read from this file.
    Module(PathBuf),
    /// A built-in type, named as written.
    Builtin,
    /// A number or string literal, with its type's text.
    Literal { ty: String },
}
impl CachedFact {
    /// `fact`, about the text of a module of `program`, whose types are of
    /// `types`; a local's name comes with the `body` it belongs to.
    pub(crate) fn new(
        program: &model::program::Program<Peeper>,
        fact: &thir::Fact,
        body: Option<&thir::Body>,
        types: &Types,
    ) -> CachedFact {
        let file = |module: ModuleId| PathBuf::from(&program.modules[module.index()].ast.file.path);
        let kind = match fact.kind {
            FactKind::Literal(ty) => CachedKind::Literal {
                ty: model::program::type_text(&program.modules, types, ty),
            },
            FactKind::Name(Symbol::Local(local)) => {
                let body = body
                    .expect("a local's name comes with its function (`CheckedFunction::facts`)");
                let declared = body.declared_at(local);
                match local {
                    _ if fact.span != declared => CachedKind::Local { declared },
                    Local::Variable(variable) => {
                        let variable = body.variables[variable];
                        CachedKind::Variable {
                            mutable: variable.mutable,
                            ty: (variable.ty != TypeId::INVALID).then(|| {
                                model::program::type_text(&program.modules, types, variable.ty)
                            }),
                        }
                    }
                    Local::Constant(constant) => CachedKind::Constant {
                        text: (body.constants[constant].as_ref())
                            .map(|constant| ConstantText::new(program, constant, types)),
                    },
                }
            }
            FactKind::Name(Symbol::Module(module)) => CachedKind::Module(file(module)),
            FactKind::Name(
                Symbol::Function(definition)
                | Symbol::Constant(definition)
                | Symbol::Type(definition),
            ) => CachedKind::Declaration {
                file: file(definition.module),
                position: definition.index,
                copy: None,
            },
            FactKind::Copy { function, ty } => CachedKind::Declaration {
                file: file(function.definition.module),
                position: function.definition.index,
                copy: Some(copy_signature(program, function, ty, types)),
            },
            FactKind::Name(Symbol::Builtin(_) | Symbol::TypeArgument(_)) => CachedKind::Builtin,
        };
        CachedFact {
            span: fact.span,
            kind,
        }
    }
}
/// The signature of `copy`, a function of `program` whose function type is
/// `ty` of `types`, as its declaration writes it, but each parameter with
/// the type the copy takes, a `const` one with what it is given, and no
/// type parameters.
fn copy_signature(
    program: &model::program::Program<Peeper>,
    copy: FunctionCopy,
    ty: TypeId,
    types: &Types,
) -> String {
    let modules = &program.modules;
    let tree = &modules[copy.definition.module.index()].ast;
    let ast::Decl::Function(function) = model::program::declaration(modules, copy.definition)
    else {
        unreachable!("a copy is of a function")
    };
    let Type::Function {
        parameters, result, ..
    } = &types[ty]
    else {
        unreachable!("a copy has a function type")
    };
    let text = |ty: TypeId| model::program::type_text(modules, types, ty);
    // The types its type parameters, `<T>`, are given come first (`copy_parameters`).
    let mut given =
        types.arguments(copy.arguments)[function.signature.type_parameters.len()..].iter();
    let mut taken = parameters.iter();
    let mut signature = String::from("fn ");
    if let Some(owner) = function.owner {
        _ = write!(signature, "{}::", owner.text(&tree.file));
    }
    signature.push_str(function.name.text(&tree.file));
    signature.push('(');
    for (place, parameter) in tree
        .children
        .lists
        .items(function.signature.parameters)
        .enumerate()
    {
        if place > 0 {
            signature.push_str(", ");
        }
        let written = tree.file.slice(tree.children.params.span(parameter));
        let parameter = tree.children.params[parameter];
        let name = parameter.name.map_or("_", |name| name.text(&tree.file));
        if parameter.constant {
            match given
                .next()
                .expect("a copy is given each `const` parameter")
            {
                ConstArgument::Type(ty) => {
                    _ = write!(signature, "const {name}: type = {}", text(*ty));
                }
                ConstArgument::Value(constant) => {
                    // Its type as written: `usize`, not the integer it is here.
                    let ty = tree.file.slice(tree.types.span(parameter.ty));
                    let value = value_text(&constant.value, &types[constant.ty]);
                    _ = write!(signature, "const {name}: {ty} = {value}");
                }
            }
            continue;
        }
        let ty = *taken.next().expect("a copy's type has each parameter");
        match (&tree.types[parameter.ty], &types[ty]) {
            _ if tree.is_self(parameter) => signature.push_str(written),
            (ast::Type::Variadic { .. }, Type::Slice { mutable, element }) => {
                let mutable = if *mutable { "mut " } else { "" };
                _ = write!(signature, "{name}: ...{mutable}{}", text(*element));
            }
            _ => {
                let mutable = if parameter.mutable { "mut " } else { "" };
                _ = write!(signature, "{mutable}{name}: {}", text(ty));
            }
        }
    }
    signature.push(')');
    if *result != TypeId::VOID {
        _ = write!(signature, " -> {}", text(*result));
    }
    let mut separator = " in ";
    for origin in tree.children.lists.items(function.signature.origins) {
        signature.push_str(separator);
        signature.push_str(tree.file.slice(tree.children.names.span(origin)));
        separator = ", ";
    }
    signature
}
/// A constant's checked type and value, as text, which outlives the compile
/// that found them.
#[derive(Clone)]
pub(crate) struct ConstantText {
    pub(crate) ty: String,
    pub(crate) value: String,
}
impl ConstantText {
    /// `constant`, of `program`, whose type is in `types`.
    pub(crate) fn new(
        program: &model::program::Program<Peeper>,
        constant: &Constant,
        types: &Types,
    ) -> ConstantText {
        ConstantText {
            ty: model::program::type_text(&program.modules, types, constant.ty),
            value: value_text(&constant.value, &types[constant.ty]).to_string(),
        }
    }
}
