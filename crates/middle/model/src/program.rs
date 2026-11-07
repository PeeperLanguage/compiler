//! A loaded program: its modules, each a parsed file with its resolved
//! imports, and what every phase asks of its declarations. `project` loads
//! one; nothing here reads a disk.

use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use diagnostics::Diagnostic;
use source::SourceFile;
use source::Span;

use crate::DefId;
use crate::ModuleId;
use crate::ast;
use crate::ast::Ident;
use crate::ast::Tree;
use crate::semantic_types::TypeId;
use crate::semantic_types::Types;

/// One source file, read and parsed. Its source file is `ast.file`. What
/// `load` read never changes; what a compile finds, its scope included, is
/// kept by the compile (`check::Checker`).
pub struct Module {
    pub path: String, // import path, such as "myapp:utils/file" or "core:global"
    pub ast: Tree,
    pub imports: Vec<Import>, // in source order; the implicit prelude import last, in every module but the prelude
}

/// The declaration `definition` of `modules` names.
pub fn declaration(modules: &[Module], definition: DefId) -> &ast::Decl {
    let tree = &modules[definition.module.index()].ast;
    &tree.decls[tree.declaration(definition.index)]
}

/// The text of the name the declaration `definition` of `modules` declares.
pub fn declared_text(modules: &[Module], definition: DefId) -> &str {
    let (name, file) = declared_name(modules, definition);
    name.text(file)
}

/// Whether other modules can use `name`: it starts with an upper case letter.
pub fn is_exported(name: &str) -> bool {
    name.starts_with(char::is_uppercase)
}

/// The name of the declaration `definition` of `modules` as a program calls
/// it: its own name, or `Owner::name` for a function of the type `Owner`.
pub fn declared_path(modules: &[Module], definition: DefId) -> String {
    let name = declared_text(modules, definition);
    match declaration(modules, definition) {
        ast::Decl::Function(ast::Function {
            owner: Some(owner), ..
        }) => {
            let file = &modules[definition.module.index()].ast.file;
            format!("{}::{name}", owner.text(file))
        }
        _ => name.to_string(),
    }
}

/// The type `ty` of `types` as a message names it, each declared type by the
/// name its declaration in `modules` gives it (`Types::text`).
pub fn type_text(modules: &[Module], types: &Types, ty: TypeId) -> String {
    types.text(ty, |definition| declared_text(modules, definition))
}

/// The name the declaration `definition` of `modules` declares, and the file
/// it is in.
pub fn declared_name(modules: &[Module], definition: DefId) -> (Ident, &Arc<SourceFile>) {
    (
        declaration(modules, definition).name(),
        &modules[definition.module.index()].ast.file,
    )
}

/// The name of the attribute that makes a function a C function.
pub const EXTERN_ATTRIBUTE: &str = "extern";

/// The name in `#[extern("name")]` on the function `definition` of
/// `modules`, if it has one: it is a C function, linked by that name. The
/// error is where an `extern` attribute of another shape, or a second one,
/// is written, which the checker reports.
pub fn extern_symbol(modules: &[Module], definition: DefId) -> Result<Option<String>, Span> {
    let tree = &modules[definition.module.index()].ast;
    let ast::Decl::Function(function) = declaration(modules, definition) else {
        return Ok(None);
    };
    let mut symbol = None;
    for attribute in tree.children.lists.items(function.metadata.attributes) {
        let span = tree.children.attributes.span(attribute);
        let attribute = &tree.children.attributes[attribute];
        if attribute.name.text(&tree.file) != EXTERN_ATTRIBUTE {
            continue;
        }
        let mut arguments = tree.children.lists.items(attribute.arguments);
        let (None, Some(argument), None) = (&symbol, arguments.next(), arguments.next()) else {
            return Err(span);
        };
        let ast::Expr::String { .. } = tree.exprs[argument] else {
            return Err(span);
        };
        let text = tree.file.slice(tree.exprs.span(argument));
        symbol = Some(String::from_utf8_lossy(&literal::value(text)).into_owned());
    }
    Ok(symbol)
}

/// Whether the function `definition` of `modules` is a C function: it has
/// an `extern`, well formed or not, so the checker gives it C's rules.
pub fn is_extern(modules: &[Module], definition: DefId) -> bool {
    extern_symbol(modules, definition) != Ok(None)
}

/// The C name of the function `definition` of a program the checker
/// accepted, if it is a C function (`extern_symbol`).
pub fn c_symbol(modules: &[Module], definition: DefId) -> Option<String> {
    extern_symbol(modules, definition).expect("the checker reported a malformed `extern`")
}

/// The names of the fields of the struct declaration `definition` of
/// `modules`, in order.
pub fn field_names(modules: &[Module], definition: DefId) -> impl Iterator<Item = &str> {
    let tree = &modules[definition.module.index()].ast;
    let &ast::Decl::Type(written) = declaration(modules, definition) else {
        unreachable!("`{definition:?}` is a type declaration")
    };
    let ast::Type::Struct(fields) = tree.types[written.ty] else {
        unreachable!("`{definition:?}` declares a struct")
    };
    let names = tree.children.lists.items(fields);
    names.map(move |field| tree.children.fields[field].name.text(&tree.file))
}

/// The method declarations of the interface `definition`, in order; `None`
/// when it declares something else. The parser puts only functions in one.
pub fn interface_methods(
    modules: &[Module],
    definition: DefId,
) -> Option<impl ExactSizeIterator<Item = &ast::Function>> {
    let tree = &modules[definition.module.index()].ast;
    let &ast::Decl::Type(written) = declaration(modules, definition) else {
        return None;
    };
    let ast::Type::Interface(methods) = tree.types[written.ty] else {
        return None;
    };
    Some(
        tree.children
            .lists
            .items(methods)
            .map(|method| match &tree.decls[method] {
                ast::Decl::Function(function) => function,
                _ => unreachable!("an interface holds functions"),
            }),
    )
}

/// One resolved import of a module.
pub struct Import {
    pub kind: ImportKind,
    pub module: ModuleId, // the imported module
}

/// Where an import comes from.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ImportKind {
    /// The importer's `ast.imports[i]`, written in its file.
    Declared(u32),
    /// The implicit import of `core:global`.
    Prelude,
}

impl ImportKind {
    /// The import as written in `importer`; `None` for the prelude's.
    pub fn declaration(self, importer: &Tree) -> Option<&ast::Import> {
        match self {
            ImportKind::Declared(index) => Some(&importer.imports[index as usize]),
            ImportKind::Prelude => None,
        }
    }
}

impl Import {
    /// The name the import brings into `importer`, and where it is written:
    /// its alias, else the last segment of the imported module's path (in
    /// `modules`), at the import. `None` for the prelude, whose names are
    /// looked up, not brought in.
    pub fn name<'a>(&self, importer: &'a Module, modules: &'a [Module]) -> Option<(&'a str, Span)> {
        let declaration = self.kind.declaration(&importer.ast)?;
        let name = match declaration.alias {
            Some(alias) => alias.text(&importer.ast.file),
            None => default_alias(&modules[self.module.index()].path),
        };
        Some((name, declaration.span))
    }
}

/// The name an import without `as` gets: the last segment of the imported
/// module's path, such as "file" for "myapp:utils/file".
pub fn default_alias(path: &str) -> &str {
    &path[path.rfind(['/', ':']).map_or(0, |i| i + 1)..]
}

/// Every module reachable from an entry file.
pub struct Program {
    /// Each module after the modules it imports; outside cycles, the same
    /// order every run.
    pub modules: Vec<Module>,
    /// What loading found: import cycles first, then each module's errors,
    /// in the program's order. One list for the whole program, because every
    /// load reads every module again; what a compile finds is kept per
    /// module instead (`compile::Results`), so an unchanged module can reuse it.
    pub diagnostics: Vec<Diagnostic>,
    /// Whether the program can be checked: every module's text is all in its
    /// tree, every import brings in its module, and no import cycle was found.
    /// Checking with a hole in a tree or a missing import would only add
    /// reports of it. A syntax error the parser read past, such as a missing
    /// `;` at a line end, leaves no hole.
    pub checkable: bool,
    /// The project folder the program was loaded from (`Project::root`),
    /// which panic messages name its files from, so a program is the same
    /// wherever its project is.
    pub root: PathBuf,
    /// The module of the project's entry file, which must declare `main`;
    /// `None` for a library.
    pub entry: Option<ModuleId>,
}

impl Program {
    /// The module read from `file`.
    pub fn module_of(&self, file: &Path) -> Option<&Module> {
        self.modules
            .iter()
            .find(|module| Path::new(&module.ast.file.path) == file)
    }
}
