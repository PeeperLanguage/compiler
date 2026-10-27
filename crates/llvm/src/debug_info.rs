//! Debug info: a module's numbered metadata nodes, and the debug type of
//! each type it describes.

use std::collections::HashMap;
use std::fmt;
use std::fmt::Write;
use std::path::Path;

use model::semantic_types::Float;
use model::semantic_types::RANGE;
use model::semantic_types::RANGE_FIELD_NAMES;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::semantic_types::range_fields;

use crate::Target;
use crate::layout::layout;
use crate::layout::struct_layout;
use crate::text::quoted;

/// The module's debug info: numbered metadata nodes.
pub(crate) struct Metadata {
    pub(crate) text: String,       // each node's line, `!N = ...`, in order
    pub(crate) count: usize,       // the nodes so far; the next one's number
    pub(crate) file: usize,        // the DIFile of the module's source file
    pub(crate) unit: usize,        // the DICompileUnit, the scope of every function
    types: HashMap<TypeId, usize>, // each type's debug type
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
            types: HashMap::new(),
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
        target: &Target,
    ) -> Option<usize> {
        let pointer_bits = target.pointer_bits;
        // An alias here would get a second debug type for its type.
        debug_assert_eq!(types.real(ty), ty, "MIR holds no aliases");
        if ty == TypeId::VOID {
            return None;
        }
        if let Some(&id) = self.types.get(&ty) {
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
                    Some(pointee) => {
                        let base = self.debug_type(program, types, types.real(pointee), target);
                        format!("!{}", base.expect("a pointer points to a value's type"))
                    }
                    None => "null".to_string(), // `rawptr`
                };
                format!(
                    "!DIDerivedType(tag: DW_TAG_pointer_type, baseType: {base}, size: {pointer_bits})"
                )
            }
            Type::Slice { element, .. } => {
                let base = self.debug_type(program, types, types.real(element), target);
                let base = base.expect("an element has a value's type");
                let name = types.text(ty, |definition| {
                    let (name, file) = project::declared_name(&program.modules, definition);
                    name.text(file)
                });
                // Its members name it, so its number comes first.
                let composite = self.reserve();
                let pointer = self.add(format_args!(
                    "!DIDerivedType(tag: DW_TAG_pointer_type, baseType: !{base}, size: {pointer_bits})"
                ));
                let usize = self.add(basic("usize", pointer_bits, "DW_ATE_unsigned"));
                let member = |field: &str, base: usize, offset: u32| {
                    format!(
                        "!DIDerivedType(tag: DW_TAG_member, name: \"{field}\", \
                         scope: !{composite}, baseType: !{base}, offset: {offset})"
                    )
                };
                let address = self.add(member("ptr", pointer, 0));
                let length = self.add(member("len", usize, pointer_bits));
                let elements = self.add(format_args!("!{{!{address}, !{length}}}"));
                self.define(
                    composite,
                    format_args!(
                        "distinct !DICompositeType(tag: DW_TAG_structure_type, name: {}, \
                         size: {}, elements: !{elements})",
                        quoted(name.as_bytes()),
                        2 * pointer_bits
                    ),
                );
                self.types.insert(ty, composite);
                return Some(composite);
            }
            Type::Array { element, length } => {
                let base = self.debug_type(program, types, types.real(element), target);
                let base = base.expect("an element has a value's type");
                let range = self.add(format_args!("!DISubrange(count: {length})"));
                let (size, _) = layout(types, ty, target);
                format!(
                    "!DICompositeType(tag: DW_TAG_array_type, baseType: !{base}, size: {}, \
                     elements: !{{!{range}}})",
                    size * 8
                )
            }
            Type::Invalid | Type::Void | Type::Function { .. } => {
                unreachable!("MIR holds no value of this type")
            }
            Type::Struct {
                definition,
                ref fields,
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
                let fields = range_fields(element, step);
                let parts = (RANGE_FIELD_NAMES.into_iter().zip(fields)).collect::<Vec<_>>();
                let shape = (ty, name.as_str(), &parts[..]);
                return Some(self.debug_struct(program, types, shape, target));
            }
            Type::Alias { .. } => unreachable!("indexing looks through aliases"),
            Type::Named(_) => unreachable!("no value has a named type"),
        };
        let id = self.add(text);
        self.types.insert(ty, id);
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
        target: &Target,
    ) -> usize {
        // Its members name it, so its number comes first.
        let composite = self.reserve();
        let field_types = fields.iter().map(|&(_, field)| field).collect::<Vec<_>>();
        let (offsets, size, _) = struct_layout(types, &field_types, target);
        let mut members = Vec::with_capacity(fields.len());
        for (&(field_name, field), offset) in fields.iter().zip(offsets) {
            let base = self.debug_type(program, types, types.real(field), target);
            let base = base.expect("a field has a value's type");
            let member = self.add(format_args!(
                "!DIDerivedType(tag: DW_TAG_member, name: {}, scope: !{composite}, \
                 baseType: !{base}, offset: {})",
                quoted(field_name.as_bytes()),
                offset * 8
            ));
            members.push(format!("!{member}"));
        }
        let elements = self.add(format_args!("!{{{}}}", members.join(", ")));
        self.define(
            composite,
            format_args!(
                "distinct !DICompositeType(tag: DW_TAG_structure_type, name: {}, \
                 size: {}, elements: !{elements})",
                quoted(name.as_bytes()),
                size * 8
            ),
        );
        self.types.insert(ty, composite);
        composite
    }
}
