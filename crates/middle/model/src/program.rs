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
use crate::FunctionCopy;
use crate::ModuleId;
use crate::constant::Value;
use crate::semantic_types::ConstArgument;
use crate::semantic_types::Constant;
use crate::semantic_types::TypeId;
use crate::semantic_types::Types;
use syntax::ast;
use syntax::ast::Ident;
use syntax::ast::Language;
use syntax::ast::Tree;

/// One source file, read and parsed. Its source file is `ast.file`. What
/// `load` read never changes; what a compile finds, its scope included, is
/// kept by the compile (`check::Checker`).
pub struct Module<L: Language> {
    pub path: String, // import path, such as "myapp:utils/file" or "core:global"
    pub ast: Tree<L>,
    pub imports: Vec<Import>, // in source order; the implicit prelude import last, in every module but the prelude
}

/// The declaration `definition` of `modules` names.
pub fn declaration<L: Language>(modules: &[Module<L>], definition: DefId) -> &ast::Decl<L> {
    let tree = &modules[definition.module.index()].ast;
    &tree.decls[tree.declaration(definition.index)]
}

/// The text of the name the declaration `definition` of `modules` declares.
pub fn declared_text<L: Language>(modules: &[Module<L>], definition: DefId) -> &str {
    let (name, file) = declared_name(modules, definition);
    name.text(file)
}

/// Whether other modules can use `name`: it starts with an upper case letter.
pub fn is_exported(name: &str) -> bool {
    name.starts_with(char::is_uppercase)
}

/// The name of the declaration `definition` of `modules` as a program calls
/// it: its own name, or `Owner::name` for a function of the type `Owner`.
pub fn declared_path<L: Language>(modules: &[Module<L>], definition: DefId) -> String {
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
pub fn type_text<L: Language>(modules: &[Module<L>], types: &Types, ty: TypeId) -> String {
    types.text(ty, |definition| declared_text(modules, definition))
}

/// The name the declaration `definition` of `modules` declares, and the file
/// it is in.
pub fn declared_name<L: Language>(
    modules: &[Module<L>],
    definition: DefId,
) -> (Ident, &Arc<SourceFile>) {
    (
        declaration(modules, definition).name(),
        &modules[definition.module.index()].ast.file,
    )
}

/// The name of the attribute that makes a function a C function.
pub const EXTERN_ATTRIBUTE: &str = "extern";

/// The name in `#[extern("name")]` on the function `definition` of
/// `modules`, if it has one: it is a C function, linked by that name. The
/// error is where an `extern` attribute of another shape, a second one, or
/// one whose name no symbol can have is written, which the checker
/// reports.
pub fn extern_symbol<L: Language>(
    modules: &[Module<L>],
    definition: DefId,
) -> Result<Option<String>, Span> {
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
        let ast::Expr::Feature(expr) = tree.exprs[argument] else {
            return Err(span);
        };
        if L::string_kind(&expr).is_none() {
            return Err(span);
        }
        let text = tree.file.slice(tree.exprs.span(argument));
        let name = String::from_utf8_lossy(&literal::value(text)).into_owned();
        // No symbol has an empty name or one with a zero byte, which ends a
        // C name; `main` is the program's own, and `llvm.` names are LLVM's.
        if name.is_empty() || name.contains('\0') || name == "main" || name.starts_with("llvm.") {
            return Err(span);
        }
        symbol = Some(name);
    }
    Ok(symbol)
}

/// The name of the attribute on a function the compiler answers itself.
pub const BUILTIN_ATTRIBUTE: &str = "builtin";

/// Whether the declaration `definition` of `modules` is a function with a
/// `#[builtin]`: it has no body, and a call of it is a constant the checker
/// works out from the type it is given.
pub fn is_builtin<L: Language>(modules: &[Module<L>], definition: DefId) -> bool {
    let tree = &modules[definition.module.index()].ast;
    let ast::Decl::Function(function) = declaration(modules, definition) else {
        return false;
    };
    (tree.children.lists.items(function.metadata.attributes)).any(|attribute| {
        tree.children.attributes[attribute].name.text(&tree.file) == BUILTIN_ATTRIBUTE
    })
}

/// Whether the function `definition` of `modules` is a C function: it has
/// an `extern`, well formed or not, so the checker gives it C's rules.
pub fn is_extern<L: Language>(modules: &[Module<L>], definition: DefId) -> bool {
    extern_symbol(modules, definition) != Ok(None)
}

/// The C name of the function `definition` of a program the checker
/// accepted, if it is a C function (`extern_symbol`).
pub fn c_symbol<L: Language>(modules: &[Module<L>], definition: DefId) -> Option<String> {
    extern_symbol(modules, definition).expect("the checker reported a malformed `extern`")
}

/// The name the linker knows the function `definition` by: its `#[extern]`
/// name, else its module's path and its name (`myapp:util.Parse`).
pub fn symbol<L: Language>(modules: &[Module<L>], definition: DefId) -> String {
    c_symbol(modules, definition).unwrap_or_else(|| {
        let path = &modules[definition.module.index()].path;
        let name = declared_path(modules, definition);
        format!("{path}.{name}")
    })
}

/// The name the linker knows the copy `copy` by: its function's `symbol`,
/// with its `const` arguments, each type by its module's path and name
/// (`myapp:main.New<myapp:main.Node>`), so two modules' types of one name
/// make two names, and each float value by its bits, as two NaNs print
/// alike.
pub fn copy_symbol<L: Language>(
    modules: &[Module<L>],
    types: &Types,
    copy: FunctionCopy,
) -> String {
    let qualified = |definition: DefId| {
        let path = &modules[definition.module.index()].path;
        format!("{path}.{}", declared_text(modules, definition))
    };
    let name = symbol(modules, copy.definition);
    let given = types.arguments(copy.arguments);
    if given.is_empty() {
        return name;
    }
    let text = |argument: &ConstArgument| match argument {
        ConstArgument::Value(Constant {
            value: Value::Float(bits),
            ..
        }) => format!("{bits:#x}"),
        _ => types.argument_text(argument, qualified),
    };
    let texts: Vec<String> = given.iter().map(text).collect();
    format!("{name}<{}>", texts.join(", "))
}

/// The copy `copy` as a program names it: its declared path, with its
/// `const` arguments as Peeper text (`Stack.New<Node>`); what messages,
/// dumps and a debugger show.
pub fn copy_name<L: Language>(modules: &[Module<L>], types: &Types, copy: FunctionCopy) -> String {
    let declared = declared_path(modules, copy.definition);
    types.copy_text(declared, copy.arguments, |definition| {
        declared_text(modules, definition)
    })
}

/// The names of the fields of the struct declaration `definition` of
/// `modules`, in order.
pub fn field_names<L: Language>(
    modules: &[Module<L>],
    definition: DefId,
) -> impl Iterator<Item = &str> {
    let tree = &modules[definition.module.index()].ast;
    let &ast::Decl::Type(written) = declaration(modules, definition) else {
        unreachable!("`{definition:?}` is a type declaration")
    };
    let fields = match tree.types[written.ty] {
        ast::Type::Feature(ty) => L::fields(&ty),
        _ => None,
    };
    let fields = fields.unwrap_or_else(|| unreachable!("`{definition:?}` declares a struct"));
    let names = tree.children.lists.items(fields);
    names.map(move |field| tree.children.fields[field].name.text(&tree.file))
}

/// The method declarations of the interface `definition`, in order; `None`
/// when it declares something else. The parser puts only functions in one.
pub fn interface_methods<L: Language>(
    modules: &[Module<L>],
    definition: DefId,
) -> Option<impl ExactSizeIterator<Item = &ast::Function<L>>> {
    let tree = &modules[definition.module.index()].ast;
    let &ast::Decl::Type(written) = declaration(modules, definition) else {
        return None;
    };
    let ast::Type::Feature(ty) = tree.types[written.ty] else {
        return None;
    };
    let methods = L::methods(&ty)?;
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
    pub fn declaration<L: Language>(self, importer: &Tree<L>) -> Option<&ast::Import> {
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
    pub fn name<'a, L: Language>(
        &self,
        importer: &'a Module<L>,
        modules: &'a [Module<L>],
    ) -> Option<(&'a str, Span)> {
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
pub struct Program<L: Language> {
    /// Each module after the modules it imports; outside cycles, the same
    /// order every run.
    pub modules: Vec<Module<L>>,
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

impl<L: Language> Program<L> {
    /// The module read from `file`.
    pub fn module_of(&self, file: &Path) -> Option<&Module<L>> {
        self.modules
            .iter()
            .find(|module| Path::new(&module.ast.file.path) == file)
    }
}
