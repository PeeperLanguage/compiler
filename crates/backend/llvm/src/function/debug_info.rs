//! A function's debug info: its subprogram, the lexical block of each MIR
//! debug scope, and the location of each instruction.

use arena::Id;
use model::DefId;
use model::mir;
use model::semantic_types::Types;
use source::Span;

use crate::Module;
use crate::function::Function;
use crate::text::quoted;
use model::layout::in_memory;

impl Function<'_, '_, '_> {
    /// The DILocation of where `span` starts, in the MIR debug scope
    /// `debug_scope`, as the end of a line; nothing in a release build.
    pub(super) fn location(
        &mut self,
        span: Span,
        debug_scope: Id<mir::DebugScope>,
    ) -> DebugLocation {
        let file = self.module.file();
        let Some(metadata) = &mut self.module.metadata else {
            return DebugLocation(None);
        };
        let scope = self.debug.scopes[debug_scope.index()];
        // An offset has one line and column, so it is the key: they are
        // found only for a new node.
        let id = *self
            .debug
            .locations
            .entry((scope, span.start))
            .or_insert_with(|| {
                let (line, column) = file.position(span.start as usize);
                metadata.add(format_args!(
                    "!DILocation(line: {line}, column: {column}, scope: !{scope})"
                ))
            });
        DebugLocation(Some(id))
    }
}

impl Module<'_> {
    /// Adds the debug info of the function `definition`, linked as `symbol`,
    /// that has the MIR `body`: its `DISubprogram`, the scope of everything
    /// in it, and a `DILexicalBlock` for each block that declares a variable. Gives the subprogram and the
    /// metadata of each MIR debug scope: its lexical block, else its parent's;
    /// `None` in a release build, which has no debug info.
    /// The function's block takes the subprogram when there are parameters,
    /// whose scope is the function.
    pub(super) fn add_debug_info(
        &mut self,
        definition: DefId,
        symbol: &str,
        body: &mir::Body,
        types: &Types,
    ) -> Option<(usize, Vec<usize>)> {
        let target = self.target;
        let metadata = self.metadata.as_mut()?;
        let tree = &self.program.modules[definition.module.index()].ast;
        let name = tree.decls[tree.declaration(definition.index)].name();
        let (line, _) = tree.file.position(name.span.start as usize);
        let program = self.program;
        // As `add_function` passes them: a value kept in memory by its
        // address, a result kept in memory where a first parameter points,
        // so a debugger reads no result from the C ABI's registers.
        let mut debug_type = |ty| match metadata.debug_type(program, types, ty, target) {
            Some(id) if in_memory(types, ty) => {
                let address = metadata.add(format_args!(
                    "!DIDerivedType(tag: DW_TAG_pointer_type, baseType: !{id}, size: {})",
                    target.pointer_bits
                ));
                format!("!{address}")
            }
            Some(id) => format!("!{id}"),
            None => "null".to_string(),
        };
        let mut signature = if in_memory(types, body.result) {
            vec!["null".to_string(), debug_type(body.result)]
        } else {
            vec![debug_type(body.result)]
        };
        for parameter in body.variables.ids().take(body.parameters) {
            signature.push(debug_type(body.variables[parameter].ty));
        }
        let subroutine = metadata.add(format_args!(
            "!DISubroutineType(types: !{{{}}})",
            signature.join(", ")
        ));
        let (file, unit) = (metadata.file, metadata.unit);
        let subprogram = metadata.add(format_args!(
            "distinct !DISubprogram(name: {}, linkageName: {}, scope: !{file}, file: !{file}, \
             line: {line}, type: !{subroutine}, scopeLine: {line}, spFlags: DISPFlagDefinition, \
             unit: !{unit})",
            quoted(name.text(&tree.file).as_bytes()),
            quoted(symbol.as_bytes()),
        ));
        let mut declares = vec![false; body.debug_scopes.len()];
        for variable in body.variables.ids() {
            declares[body.variables[variable].debug_scope.index()] = true;
        }
        if body.parameters > 0 {
            declares[0] = false;
        }
        // A block comes after its parent, so the parent's debug scope is known.
        let mut scope_metadata: Vec<usize> = Vec::with_capacity(declares.len());
        for scope in body.debug_scopes.ids() {
            let parent = body.debug_scopes[scope]
                .parent
                .map_or(subprogram, |parent| scope_metadata[parent.index()]);
            if !declares[scope.index()] {
                scope_metadata.push(parent);
                continue;
            }
            let (line, column) = tree
                .file
                .position(body.debug_scopes.span(scope).start as usize);
            scope_metadata.push(metadata.add(format_args!(
                "!DILexicalBlock(scope: !{parent}, file: !{file}, line: {line}, column: {column})"
            )));
        }
        Some((subprogram, scope_metadata))
    }
}

/// The `, !dbg !N` that ends a line with its DILocation `N`; nothing without
/// one.
pub(super) struct DebugLocation(pub(super) Option<usize>);

impl std::fmt::Display for DebugLocation {
    fn fmt(&self, out: &mut std::fmt::Formatter) -> std::fmt::Result {
        match self.0 {
            Some(id) => write!(out, ", !dbg !{id}"),
            None => Ok(()),
        }
    }
}
