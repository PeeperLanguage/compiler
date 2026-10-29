//! What the last check found, kept for the next one and for requests: the
//! compile results, and each module's facts by name.

use std::collections::HashMap;
use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;

use lsp_types::CodeAction;
use model::ModuleId;
use model::constant::Constant;
use model::constant::value_text;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::thir;
use model::thir::FactKind;
use model::thir::Local;
use model::thir::Symbol;
use source::Span;

use crate::host;

/// What the server keeps of a program between checks.
#[derive(Default)]
pub(crate) struct Checked {
    pub(crate) results: compile::Results, // reused by the next compile
    pub(crate) published: HashSet<PathBuf>, // the files last given diagnostics
    /// The program as last compiled, and the facts of each of its modules'
    /// text and the names of its constants, by file: what definition and
    /// hover read.
    pub(crate) program: Option<project::Program>,
    pub(crate) facts: HashMap<PathBuf, Vec<CachedFact>>,
    pub(crate) fix_all: HashMap<PathBuf, CodeAction>, // each file's sure fixes, as of the last check
}
impl Checked {
    /// Compiles `program` again, reusing what the last compile found, and
    /// keeps the facts of each module's text and the names of its
    /// constants: a module the compile reused gave no facts, and the facts
    /// found before still fit it; a module checked again gets its new facts.
    pub(crate) fn update(&mut self, program: &project::Program) {
        let mut found: HashMap<ModuleId, Vec<CachedFact>> = HashMap::new();
        let previous = std::mem::take(&mut self.results);
        let cache_facts = |output: compile::Output| match output {
            compile::Output::Function(compile::CheckedFunction {
                definition,
                thir,
                facts,
                types,
                ..
            }) => {
                let facts = facts
                    .iter()
                    .map(|fact| CachedFact::new(program, fact, Some(thir), types));
                found.entry(definition.module).or_default().extend(facts);
            }
            compile::Output::Constant {
                definition,
                constant,
                types,
            } => {
                let tree = &program.modules[definition.module.index()].ast;
                let declared = tree.decls[tree.declaration(definition.index)].name().span;
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
        self.results = compile::run(program, host(), previous, true, cache_facts);
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
    /// `mut`, and its type's text.
    Variable { mutable: bool, ty: String },
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
    Declaration { file: PathBuf, position: u32 },
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
        program: &project::Program,
        fact: &thir::Fact,
        body: Option<&thir::Body>,
        types: &Types,
    ) -> CachedFact {
        let file = |module: ModuleId| PathBuf::from(&program.modules[module.index()].ast.file.path);
        let kind = match fact.kind {
            FactKind::Literal(ty) => CachedKind::Literal {
                ty: type_text(program, types, ty),
            },
            FactKind::Name(Symbol::Local(local)) => {
                let body = body
                    .expect("a local's name comes with its function (`CheckedFunction::facts`)");
                let declared = body.declared(local);
                match local {
                    _ if fact.span != declared => CachedKind::Local { declared },
                    Local::Variable(variable) => {
                        let variable = body.variables[variable];
                        CachedKind::Variable {
                            mutable: variable.mutable,
                            ty: type_text(program, types, variable.ty),
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
            },
            FactKind::Name(Symbol::Builtin(_)) => CachedKind::Builtin,
        };
        CachedFact {
            span: fact.span,
            kind,
        }
    }
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
        program: &project::Program,
        constant: &Constant,
        types: &Types,
    ) -> ConstantText {
        ConstantText {
            ty: type_text(program, types, constant.ty),
            value: value_text(&constant.value, &types[constant.ty]).to_string(),
        }
    }
}
/// The text of `ty`, of `types`, with the names `program` declares.
pub(crate) fn type_text(program: &project::Program, types: &Types, ty: TypeId) -> String {
    types.text(ty, |definition| {
        let (name, source) = project::declared_name(&program.modules, definition);
        name.text(source)
    })
}
