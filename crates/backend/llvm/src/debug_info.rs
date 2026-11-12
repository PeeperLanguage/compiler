//! Debug info: a module's numbered metadata nodes, the debug type of each
//! type its functions name, and each function's subprogram and scopes.

use std::fmt;
use std::fmt::Write;
use std::path::Path;

use arena::Id;
use model::FunctionCopy;
use model::mir::Body;
use model::mir::DebugScope;
use model::mir::DebugType;
use model::mir::DebugTypeId;
use model::mir::DebugTypes;
use model::mir::Passing;
use rustc_hash::FxHashMap;
use source::SourceFile;

use crate::Module;
use crate::text::comma_separated;
use crate::text::quoted;

/// The module's debug info: numbered metadata nodes.
pub(crate) struct Metadata {
    pub(crate) text: String, // each node's line, `!N = ...`; a reserved one comes later
    pub(crate) count: usize, // the nodes so far; the next one's number
    files: FxHashMap<String, usize>, // the DIFile of each source file, by path
    pub(crate) unit: usize,  // the DICompileUnit, the scope of every function
    debug_types: FxHashMap<DebugTypeId, usize>, // each debug type's node
}
impl Metadata {
    /// The debug info of a module in the file `path`, with no function yet.
    pub(crate) fn new(path: &str) -> Metadata {
        let mut metadata = Metadata {
            text: String::new(),
            count: 0,
            files: FxHashMap::default(),
            unit: 0,
            debug_types: FxHashMap::default(),
        };
        let file = metadata.file_node(path);
        metadata.unit = metadata.add(format_args!(
            "distinct !DICompileUnit(language: DW_LANG_C, file: !{file}, producer: \"peeper\", \
             isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)"
        ));
        metadata
    }

    /// The DIFile of the source file at `path`, added the first time: the
    /// module's own, or that of a copy of another module's function.
    pub(crate) fn file_node(&mut self, path: &str) -> usize {
        if let Some(&id) = self.files.get(path) {
            return id;
        }
        let key = path.to_string();
        let path = Path::new(path);
        let name = path.file_name().unwrap_or_default().to_string_lossy();
        let folder = path.parent().unwrap_or(Path::new("")).to_string_lossy();
        let id = self.add(format_args!(
            "!DIFile(filename: {}, directory: {})",
            quoted(name.as_bytes()),
            quoted(folder.as_bytes())
        ));
        self.files.insert(key, id);
        id
    }

    pub(crate) fn add(&mut self, node: impl fmt::Display) -> usize {
        let id = self.reserve();
        self.define(id, node);
        id
    }

    /// The number of a node to `define` later, for a node that the nodes
    /// added before it name.
    fn reserve(&mut self) -> usize {
        let id = self.count;
        self.count += 1;
        id
    }

    fn define(&mut self, id: usize, node: impl fmt::Display) {
        _ = writeln!(self.text, "!{id} = {node}");
    }

    /// The node of the debug type `id` of `debug_types`, on a target whose
    /// pointers have `pointer_bits` bits, with the nodes it names. Each is
    /// numbered when first named and written from a list, not by
    /// recursion: pointers and structs nest as deep as the source does.
    pub(crate) fn debug_type(
        &mut self,
        debug_types: &DebugTypes,
        id: DebugTypeId,
        pointer_bits: u32,
    ) -> usize {
        let mut pending = Vec::new();
        let node = self.debug_type_node(id, &mut pending);
        while let Some((id, node)) = pending.pop() {
            match debug_types.get(id) {
                DebugType::Basic {
                    name,
                    bits,
                    encoding,
                } => self.define(
                    node,
                    format_args!(
                        "!DIBasicType(name: {}, size: {bits}, encoding: {encoding})",
                        quoted(name.as_bytes())
                    ),
                ),
                DebugType::Pointer { name, to } => {
                    let name = if name.is_empty() {
                        String::new()
                    } else {
                        format!("name: {}, ", quoted(name.as_bytes()))
                    };
                    let base = match to {
                        Some(to) => format!("!{}", self.debug_type_node(*to, &mut pending)),
                        None => "null".to_string(),
                    };
                    self.define(
                        node,
                        format_args!(
                            "!DIDerivedType(tag: DW_TAG_pointer_type, {name}baseType: {base}, size: {pointer_bits})"
                        ),
                    );
                }
                DebugType::Composite {
                    name,
                    size,
                    members,
                } => {
                    let members: Vec<usize> = (members.iter())
                        .map(|member| {
                            let base = self.debug_type_node(member.ty, &mut pending);
                            self.add(format_args!(
                                "!DIDerivedType(tag: DW_TAG_member, name: {}, scope: !{node}, \
                                 baseType: !{base}, offset: {})",
                                quoted(member.name.as_bytes()),
                                member.offset * 8
                            ))
                        })
                        .collect();
                    let elements = self.add(nodes(&members));
                    self.define(
                        node,
                        format_args!(
                            "distinct !DICompositeType(tag: DW_TAG_structure_type, name: {}, \
                             size: {}, elements: !{elements})",
                            quoted(name.as_bytes()),
                            size * 8
                        ),
                    );
                }
                // One node with a range per level, as C's debug info has.
                DebugType::Array {
                    element,
                    size,
                    lengths,
                } => {
                    let base = self.debug_type_node(*element, &mut pending);
                    let ranges: Vec<usize> = (lengths.iter())
                        .map(|length| self.add(format_args!("!DISubrange(count: {length})")))
                        .collect();
                    self.define(
                        node,
                        format_args!(
                            "!DICompositeType(tag: DW_TAG_array_type, baseType: !{base}, size: {}, \
                             elements: {})",
                            size * 8,
                            nodes(&ranges)
                        ),
                    );
                }
            }
        }
        node
    }

    /// The number of the debug type `id`'s node, reserved the first time
    /// and put on `pending` to be written.
    fn debug_type_node(
        &mut self,
        id: DebugTypeId,
        pending: &mut Vec<(DebugTypeId, usize)>,
    ) -> usize {
        if let Some(&node) = self.debug_types.get(&id) {
            return node;
        }
        let node = self.reserve();
        self.debug_types.insert(id, node);
        pending.push((id, node));
        node
    }
}

/// The metadata tuple of the nodes `ids`: `!{!3, !4}`.
fn nodes(ids: &[usize]) -> impl fmt::Display {
    let ids = ids
        .iter()
        .map(|id| fmt::from_fn(move |out| write!(out, "!{id}")));
    fmt::from_fn(move |out| write!(out, "!{{{}}}", comma_separated(ids.clone())))
}

/// The line and column of `offset`, as debug info writes them. LLVM keeps a
/// column in 16 bits and refuses a larger one, so a column past them is the
/// last it can hold: the location stays on the right line.
pub(crate) fn debug_position(file: &SourceFile, offset: usize) -> (usize, usize) {
    let (line, column) = file.position(offset);
    (line, column.min(usize::from(u16::MAX)))
}

/// The `, !dbg !N` that ends a line with its DILocation `N`; nothing without
/// one.
pub(crate) struct DebugLocation(pub(crate) Option<usize>);

impl fmt::Display for DebugLocation {
    fn fmt(&self, out: &mut fmt::Formatter) -> fmt::Result {
        match self.0 {
            Some(id) => write!(out, ", !dbg !{id}"),
            None => Ok(()),
        }
    }
}

impl Module<'_> {
    /// Adds the debug info of the function `copy`, written in `source`,
    /// linked as `symbol` and shown as `shown_name`, whose MIR is `body`:
    /// its `DISubprogram`, and a `DILexicalBlock` for each block that
    /// declares a variable a debugger shows. Gives the subprogram and the
    /// metadata of each debug scope: its lexical block, else its parent's;
    /// `None` in a release build, which has no debug info. The function's
    /// block always takes the subprogram, which is its parameters' scope
    /// too.
    pub(crate) fn add_debug_info(
        &mut self,
        copy: FunctionCopy,
        source: &SourceFile,
        symbol: &str,
        shown_name: &str,
        body: &Body,
        debug_types: &DebugTypes,
    ) -> Option<(usize, Vec<usize>)> {
        let pointer_bits = self.target.pointer_bits;
        let metadata = self.metadata.as_mut()?;
        let modules = &self.program.modules;
        let (name, _) = model::program::declared_name(modules, copy.definition);
        let (line, _) = source.position(name.span.start as usize);
        // As the function is passed them: a value kept in memory by its
        // address, a result kept in memory where a first parameter points,
        // so a debugger reads no result from the C ABI's registers.
        let mut debug_type = |id: Option<DebugTypeId>, passing| match id {
            Some(id) => {
                let node = metadata.debug_type(debug_types, id, pointer_bits);
                if let Some(Passing::Memory { .. }) = passing {
                    let address = metadata.add(format_args!(
                        "!DIDerivedType(tag: DW_TAG_pointer_type, baseType: !{node}, size: {pointer_bits})"
                    ));
                    format!("!{address}")
                } else {
                    format!("!{node}")
                }
            }
            None => "null".to_string(),
        };
        let result = body.signature.result;
        let mut signature = match result {
            Some(Passing::Memory { .. }) => vec!["null".to_string()],
            _ => Vec::new(),
        };
        signature.push(debug_type(body.result_debug_type, result));
        for (index, &passing) in body.signature.parameters.iter().enumerate() {
            let slot = body.slots[Id::from_index(index)].debug_type;
            signature.push(debug_type(slot, Some(passing)));
        }
        let subroutine = metadata.add(format_args!(
            "!DISubroutineType(types: !{{{}}})",
            signature.join(", ")
        ));
        let (file, unit) = (metadata.file_node(&source.path), metadata.unit);
        let subprogram = metadata.add(format_args!(
            "distinct !DISubprogram(name: {}, linkageName: {}, scope: !{file}, file: !{file}, \
             line: {line}, type: !{subroutine}, scopeLine: {line}, spFlags: DISPFlagDefinition, \
             unit: !{unit})",
            quoted(shown_name.as_bytes()),
            quoted(symbol.as_bytes()),
        ));
        let mut declares = vec![false; body.debug_scopes.len()];
        for slot in body.slots.ids() {
            let slot = &body.slots[slot];
            if slot.debug_type.is_some() {
                declares[slot.debug_scope.index()] = true;
            }
        }
        // The function's own block is its subprogram, as its parameters' scope.
        declares[0] = false;
        // A block comes after its parent, so the parent's metadata is known.
        let mut scopes: Vec<usize> = Vec::with_capacity(declares.len());
        for scope in body.debug_scopes.ids() {
            let parent = body.debug_scopes[scope]
                .parent
                .map_or(subprogram, |parent: Id<DebugScope>| scopes[parent.index()]);
            if !declares[scope.index()] {
                scopes.push(parent);
                continue;
            }
            let start = body.debug_scopes.span(scope).start as usize;
            let (line, column) = debug_position(source, start);
            scopes.push(metadata.add(format_args!(
                "!DILexicalBlock(scope: !{parent}, file: !{file}, line: {line}, column: {column})"
            )));
        }
        Some((subprogram, scopes))
    }
}
