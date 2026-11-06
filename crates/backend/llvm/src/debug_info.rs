//! Debug info: a module's numbered metadata nodes, and the debug type of
//! each type it describes.

use std::fmt;
use std::fmt::Write;
use std::path::Path;

use model::layout::layout;
use model::layout::struct_layout;
use model::machine::Machine;
use model::semantic_types::Float;
use model::semantic_types::RANGE;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::semantic_types::range_fields;
use rustc_hash::FxHashMap;

use crate::function::comma_separated;
use crate::text::quoted;

/// How many pointers and structs a debug type goes through
/// (`Metadata::debug_type`).
const DEEPEST_DEBUG_TYPE: usize = 5_000;

/// The module's debug info: numbered metadata nodes.
pub(crate) struct Metadata {
    pub(crate) text: String, // each node's line, `!N = ...`, in order
    pub(crate) count: usize, // the nodes so far; the next one's number
    pub(crate) file: usize,  // the DIFile of the module's source file
    pub(crate) unit: usize,  // the DICompileUnit, the scope of every function
    debug_types: FxHashMap<TypeId, usize>, // each type's debug type
    depth: usize,            // what `debug_type` is inside now
}
impl Metadata {
    /// The debug info of a module in the file `path`, with no function yet.
    pub(crate) fn new(path: &Path) -> Metadata {
        let name = path.file_name().unwrap_or_default().to_string_lossy();
        let folder = path.parent().unwrap_or(Path::new("")).to_string_lossy();
        let mut metadata = Metadata {
            text: String::new(),
            count: 0,
            file: 0,
            unit: 0,
            debug_types: FxHashMap::default(),
            depth: 0,
        };
        metadata.file = metadata.add(format_args!(
            "!DIFile(filename: {}, directory: {})",
            quoted(name.as_bytes()),
            quoted(folder.as_bytes())
        ));
        let file = metadata.file;
        metadata.unit = metadata.add(format_args!(
            "distinct !DICompileUnit(language: DW_LANG_C, file: !{file}, producer: \"peeper\", \
             isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)"
        ));
        metadata
    }

    /// Adds the node `node` and gives its number.
    pub(crate) fn add(&mut self, node: impl fmt::Display) -> usize {
        let id = self.reserve();
        self.define(id, node);
        id
    }

    /// The number of a node to `define` later, for a node that the nodes
    /// added before it name.
    pub(crate) fn reserve(&mut self) -> usize {
        let id = self.count;
        self.count += 1;
        id
    }

    /// Writes the node `id` as `node`.
    pub(crate) fn define(&mut self, id: usize, node: impl fmt::Display) {
        _ = writeln!(self.text, "!{id} = {node}");
    }

    /// The debug type of `ty`, a type of `program` laid out for `target`;
    /// `None` for `void`.
    pub(crate) fn debug_type(
        &mut self,
        program: &project::Program,
        types: &Types,
        ty: TypeId,
        target: &Machine,
    ) -> Option<usize> {
        let pointer_bits = target.pointer_bits;
        // An alias here would get a second debug type for its type.
        debug_assert_eq!(types.real(ty), ty, "MIR holds no aliases");
        if ty == TypeId::VOID {
            return None;
        }
        if let Some(&id) = self.debug_types.get(&ty) {
            return Some(id);
        }
        let basic = |name: &str, bits: u32, encoding: &str| {
            format!(
                "!DIBasicType(name: {}, size: {bits}, encoding: {encoding})",
                quoted(name.as_bytes())
            )
        };
        let text = match types[ty] {
            Type::Bool => basic("bool", 8, "DW_ATE_boolean"),
            Type::Integer(integer) => {
                let encoding = if integer.signed {
                    "DW_ATE_signed"
                } else {
                    "DW_ATE_unsigned"
                };
                basic(&integer.to_string(), integer.bits, encoding)
            }
            Type::Float(Float::F32) => basic("f32", 32, "DW_ATE_float"),
            Type::Float(Float::F64) => basic("f64", 64, "DW_ATE_float"),
            Type::CString => {
                let char = self.add(basic("char", 8, "DW_ATE_signed_char"));
                format!(
                    "!DIDerivedType(tag: DW_TAG_pointer_type, name: \"cstr\", baseType: !{char}, size: {pointer_bits})"
                )
            }
            Type::Reference { .. } | Type::RawPointer { .. } => {
                let base = match types[ty].pointee() {
                    // A struct with an error: this output is never linked.
                    Some(pointee) if matches!(types[pointee], Type::Named(_)) => "null".to_string(),
                    // shortcut: clang runs out of stack on debug types tens
                    // of thousands of nodes deep, and peeper cannot raise its
                    // limit (no `unsafe`); past this depth a debugger sees an
                    // untyped pointer, or a struct with no fields. A type
                    // keeps what it was first written as, so one first reached
                    // past this depth stays cut at a shallow use, and one
                    // entered from its deep end can chain past the depth.
                    Some(_) if self.depth == DEEPEST_DEBUG_TYPE => "null".to_string(),
                    Some(pointee) => {
                        let pointee = types.real(pointee);
                        self.depth += 1;
                        // Pointers break a flat array, so they nest as deep
                        // as the source does.
                        let base =
                            source::grow_stack(|| self.debug_type(program, types, pointee, target));
                        self.depth -= 1;
                        format!("!{}", base.expect("a pointer points to a value's type"))
                    }
                    None => "null".to_string(), // `rawptr`
                };
                format!(
                    "!DIDerivedType(tag: DW_TAG_pointer_type, baseType: {base}, size: {pointer_bits})"
                )
            }
            Type::Slice { .. } | Type::Str => {
                // A `&str` views bytes.
                let element = types[ty]
                    .elements()
                    .map_or(TypeId::U8, |(element, _)| element);
                let base = self.debug_type(program, types, types.real(element), target);
                let base = base.expect("an element has a value's type");
                let name = project::type_text(&program.modules, types, ty);
                // Its members name it, so its number comes first.
                let composite = self.reserve();
                let pointer = self.add(format_args!(
                    "!DIDerivedType(tag: DW_TAG_pointer_type, baseType: !{base}, size: {pointer_bits})"
                ));
                let usize = self.add(basic("usize", pointer_bits, "DW_ATE_unsigned"));
                let pointer_bits = u64::from(pointer_bits);
                let members = [
                    self.add_member(composite, "ptr", pointer, 0),
                    self.add_member(composite, "len", usize, pointer_bits),
                ];
                self.define_struct(composite, &name, 2 * pointer_bits, &members);
                self.debug_types.insert(ty, composite);
                return Some(composite);
            }
            // One node with a range per level, as C's debug info has.
            Type::Array { internals, .. } => {
                let base = self.debug_type(program, types, internals.base, target);
                let base = base.expect("an element has a value's type");
                let ranges: Vec<usize> = (types.array_lengths(ty))
                    .map(|length| self.add(format_args!("!DISubrange(count: {length})")))
                    .collect();
                let ranges = ranges
                    .iter()
                    .map(|range| fmt::from_fn(move |out| write!(out, "!{range}")));
                let (size, _) = layout(types, ty, target);
                format!(
                    "!DICompositeType(tag: DW_TAG_array_type, baseType: !{base}, size: {}, \
                     elements: !{{{}}})",
                    size * 8,
                    comma_separated(ranges)
                )
            }
            Type::Invalid | Type::Void | Type::Function { .. } => {
                unreachable!("MIR holds no value of this type")
            }
            Type::Struct {
                definition,
                ref fields,
                ..
            } => {
                let (name, file) = project::declared_name(&program.modules, definition);
                let names = project::field_names(&program.modules, definition);
                let parts = (names.zip(fields.iter().copied())).collect::<Vec<_>>();
                let shape = (ty, name.text(file), &parts[..]);
                return Some(self.debug_struct(program, types, shape, target));
            }
            Type::Range { element, step } => {
                let Type::Integer(integer) = types[element] else {
                    unreachable!("a range is of integers")
                };
                let name = format!("{RANGE}[{integer}]");
                let parts = range_fields(element, step);
                let shape = (ty, name.as_str(), &parts[..]);
                return Some(self.debug_struct(program, types, shape, target));
            }
            Type::Alias { .. } => unreachable!("indexing looks through aliases"),
            Type::Named(_) => unreachable!("no value has a named type"),
        };
        let id = self.add(text);
        self.debug_types.insert(ty, id);
        Some(id)
    }

    /// The debug type of `ty`, a struct named `name` with `fields` (each
    /// field's name and type, in order): a struct declaration's, or a
    /// range's (`range_fields`).
    pub(crate) fn debug_struct(
        &mut self,
        program: &project::Program,
        types: &Types,
        (ty, name, fields): (TypeId, &str, &[(&str, TypeId)]),
        target: &Machine,
    ) -> usize {
        // Its members name it, and a `*T` field reaches it again, so its
        // number comes first.
        let composite = self.reserve();
        self.debug_types.insert(ty, composite);
        let field_types = fields.iter().map(|&(_, field)| field).collect::<Vec<_>>();
        let (offsets, size, _) = struct_layout(types, &field_types, target);
        let mut members = Vec::with_capacity(fields.len());
        if self.depth < DEEPEST_DEBUG_TYPE {
            self.depth += 1;
            for (&(field_name, field), offset) in fields.iter().zip(offsets) {
                let field = types.real(field);
                // Structs hold structs as deep as the declarations nest.
                let base = source::grow_stack(|| self.debug_type(program, types, field, target));
                let base = base.expect("a field has a value's type");
                members.push(self.add_member(composite, field_name, base, offset * 8));
            }
            self.depth -= 1;
        }
        self.define_struct(composite, name, size * 8, &members);
        composite
    }

    /// Adds a member of the struct `composite`: the field `field`, of the
    /// debug type `base`, `offset` bits from the struct's start.
    fn add_member(&mut self, composite: usize, field: &str, base: usize, offset: u64) -> usize {
        self.add(format_args!(
            "!DIDerivedType(tag: DW_TAG_member, name: {}, scope: !{composite}, \
             baseType: !{base}, offset: {offset})",
            quoted(field.as_bytes())
        ))
    }

    /// Defines `composite`, reserved before its members, as the struct `name`
    /// of `size` bits with `members`, in order.
    fn define_struct(&mut self, composite: usize, name: &str, size: u64, members: &[usize]) {
        let members = members
            .iter()
            .map(|member| fmt::from_fn(move |out| write!(out, "!{member}")));
        let elements = self.add(format_args!("!{{{}}}", comma_separated(members)));
        self.define(
            composite,
            format_args!(
                "distinct !DICompositeType(tag: DW_TAG_structure_type, name: {}, \
                 size: {size}, elements: !{elements})",
                quoted(name.as_bytes())
            ),
        );
    }
}
