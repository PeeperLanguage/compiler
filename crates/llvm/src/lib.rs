//! LLVM IR text for a module (docs/ir-design.md): one walk over each
//! function's MIR, shaped like clang's output at `-O0`, with debug info from
//! the spans MIR keeps in a debug build. Run-time traps panic through the prelude's `Write`
//! and `Exit`.

mod function;
pub mod link;
pub mod runtime;
pub mod write;

use std::collections::BTreeMap;
use std::collections::HashMap;
use std::collections::HashSet;
use std::fmt;
use std::fmt::Write;
use std::path::Path;

use model::DefId;
use model::ModuleId;
use model::constant::Constant;
use model::constant::Value;
use model::semantic_types::Float;
use model::semantic_types::RANGE;
use model::semantic_types::RANGE_FIELD_NAMES;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::semantic_types::range_fields;
use source::SourceFile;

/// What a program is built for: debugging, with debug info, or release,
/// without it.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum BuildMode {
    Debug,
    Release,
}

/// The LLVM text of one module, built a function at a time.
pub struct Module<'p> {
    program: &'p project::Program,
    module: ModuleId,
    functions: String, // each `define`
    strings: String,   // each string constant's global
    /// The `define` of each array printer (`Function::array_printer`) and
    /// range filler (`Function::range_filler`).
    private_functions: String,
    /// The `declare` of each function called, by symbol; sorted, so the text
    /// is the same every run. Those the module defines are left out.
    declarations: BTreeMap<String, String>,
    written: Written,
    metadata: Option<Metadata>, // the debug info, in a debug build
    pointer_bits: u32,
}

/// What the module has written so far, so each is written once.
#[derive(Default)]
struct Written {
    strings: HashMap<Vec<u8>, usize>,       // each string's number
    array_printers: HashMap<TypeId, usize>, // each array type's printer number
    range_fillers: HashMap<TypeId, usize>,  // each element type's filler number
    functions: HashSet<String>,             // each `define`'s symbol
}

/// The module's debug info: numbered metadata nodes.
struct Metadata {
    text: String,                  // each node's line, `!N = ...`, in order
    count: usize,                  // the nodes so far; the next one's number
    file: usize,                   // the DIFile of the module's source file
    unit: usize,                   // the DICompileUnit, the scope of every function
    types: HashMap<TypeId, usize>, // each type's debug type
}

impl<'p> Module<'p> {
    /// The module `module` of `program`, with no functions yet, built in
    /// `mode`. Pointers have `pointer_bits` bits.
    fn new(
        program: &'p project::Program,
        module: ModuleId,
        pointer_bits: u32,
        mode: BuildMode,
    ) -> Module<'p> {
        let metadata = match mode {
            BuildMode::Debug => {
                let path = Path::new(&program.modules[module.index()].ast.file.path);
                Some(Metadata::new(path))
            }
            BuildMode::Release => None,
        };
        Module {
            program,
            module,
            functions: String::new(),
            strings: String::new(),
            private_functions: String::new(),
            declarations: BTreeMap::new(),
            written: Written::default(),
            metadata,
            pointer_bits,
        }
    }

    /// Adds the C `main` that starts the program at the function `main`:
    /// it gives `main`'s result as the exit code when `exit_code`, else 0.
    pub fn add_entry_point(&mut self, main: DefId, exit_code: bool) {
        let main = function::global(&symbol(self.program, main));
        let call = if exit_code {
            format!("  %code = call i32 {main}()\n  ret i32 %code")
        } else {
            format!("  call void {main}()\n  ret i32 0")
        };
        _ = writeln!(
            self.functions,
            "define i32 @main() {{\nentry:\n{call}\n}}\n"
        );
    }

    /// The source file the module is read from.
    fn file(&self) -> &'p SourceFile {
        &self.program.modules[self.module.index()].ast.file
    }

    /// Whether no function was added.
    pub fn is_empty(&self) -> bool {
        self.functions.is_empty()
    }

    /// The module's LLVM text.
    pub fn text(&self) -> String {
        let path = &self.file().path;
        let mut out = String::new();
        _ = writeln!(out, "source_filename = {}\n", quoted(path.as_bytes()));
        // Every slice type's: its elements' address and their count.
        _ = writeln!(out, "%slice = type {{ ptr, i{} }}\n", self.pointer_bits);
        out.push_str(&self.strings);
        if !self.strings.is_empty() {
            out.push('\n');
        }
        out.push_str(&self.functions);
        out.push_str(&self.private_functions);
        for (symbol, declaration) in &self.declarations {
            if !self.written.functions.contains(symbol) {
                out.push_str(declaration);
            }
        }
        let Some(metadata) = &self.metadata else {
            return out;
        };
        out.push_str("declare void @llvm.dbg.declare(metadata, metadata, metadata)\n\n");
        let version = metadata.count;
        _ = writeln!(out, "!llvm.dbg.cu = !{{!{}}}", metadata.unit);
        _ = writeln!(
            out,
            "!llvm.module.flags = !{{!{version}, !{}}}",
            version + 1
        );
        out.push_str(&metadata.text);
        _ = writeln!(out, "!{version} = !{{i32 7, !\"Dwarf Version\", i32 4}}");
        _ = writeln!(
            out,
            "!{} = !{{i32 2, !\"Debug Info Version\", i32 3}}",
            version + 1
        );
        out
    }

    /// A pointer to the string constant `bytes`, ended by a zero byte; each
    /// string gets one global.
    fn add_string(&mut self, bytes: &[u8]) -> String {
        let count = self.written.strings.len();
        let id = *self
            .written
            .strings
            .entry(bytes.to_vec())
            .or_insert_with(|| {
                let mut text = bytes.to_vec();
                text.push(0);
                _ = writeln!(
                    self.strings,
                    "@.str.{count} = private unnamed_addr constant [{} x i8] c{}",
                    text.len(),
                    quoted(&text)
                );
                count
            });
        format!("@.str.{id}")
    }

    /// Notes that the module calls `symbol`, declared by `declaration`.
    fn declare(&mut self, symbol: &str, declaration: impl FnOnce() -> String) {
        if !self.declarations.contains_key(symbol) {
            self.declarations.insert(symbol.to_string(), declaration());
        }
    }
}

impl Metadata {
    /// The debug info of a module in the file `path`, with no function yet.
    fn new(path: &Path) -> Metadata {
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
    fn add(&mut self, node: impl fmt::Display) -> usize {
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

    /// Writes the node `id` as `node`.
    fn define(&mut self, id: usize, node: impl fmt::Display) {
        _ = writeln!(self.text, "!{id} = {node}");
    }

    /// The debug type of `ty`, a type of `program` whose pointers have
    /// `pointer_bits` bits; `None` for `void`.
    fn debug_type(
        &mut self,
        program: &project::Program,
        types: &Types,
        ty: TypeId,
        pointer_bits: u32,
    ) -> Option<usize> {
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
                    Some(target) => {
                        let target =
                            self.debug_type(program, types, types.real(target), pointer_bits);
                        format!("!{}", target.expect("a pointer points to a value's type"))
                    }
                    None => "null".to_string(), // `rawptr`
                };
                format!(
                    "!DIDerivedType(tag: DW_TAG_pointer_type, baseType: {base}, size: {pointer_bits})"
                )
            }
            Type::Slice { element, .. } => {
                let base = self.debug_type(program, types, types.real(element), pointer_bits);
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
                let base = self.debug_type(program, types, types.real(element), pointer_bits);
                let base = base.expect("an element has a value's type");
                let range = self.add(format_args!("!DISubrange(count: {length})"));
                let (size, _) = layout(types, ty, pointer_bits);
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
                return Some(self.debug_struct(program, types, shape, pointer_bits));
            }
            Type::Range { element, step } => {
                let Type::Integer(integer) = types[element] else {
                    unreachable!("a range is of integers")
                };
                let name = format!("{RANGE}[{integer}]");
                let fields = range_fields(element, step);
                let parts = (RANGE_FIELD_NAMES.into_iter().zip(fields)).collect::<Vec<_>>();
                let shape = (ty, name.as_str(), &parts[..]);
                return Some(self.debug_struct(program, types, shape, pointer_bits));
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
    fn debug_struct(
        &mut self,
        program: &project::Program,
        types: &Types,
        (ty, name, fields): (TypeId, &str, &[(&str, TypeId)]),
        pointer_bits: u32,
    ) -> usize {
        // Its members name it, so its number comes first.
        let composite = self.reserve();
        let field_types = fields.iter().map(|&(_, field)| field).collect::<Vec<_>>();
        let (offsets, size, _) = struct_layout(types, &field_types, pointer_bits);
        let mut members = Vec::with_capacity(fields.len());
        for (&(field_name, field), offset) in fields.iter().zip(offsets) {
            let base = self.debug_type(program, types, types.real(field), pointer_bits);
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

/// The name the linker knows the function `definition` by: its `#[extern]`
/// name, else its module's path and its name (`myapp:util.Parse`).
fn symbol(program: &project::Program, definition: DefId) -> String {
    project::extern_symbol(&program.modules, definition).unwrap_or_else(|| {
        let module = &program.modules[definition.module.index()];
        let declaration = &module.ast.decls[module.ast.declaration(definition.index)];
        format!(
            "{}.{}",
            module.path,
            declaration.name().text(&module.ast.file)
        )
    })
}

/// The LLVM type of a value of type `ty`, written where it is printed.
fn type_text(types: &Types, ty: TypeId) -> impl fmt::Display {
    fmt::from_fn(move |out| match types[ty] {
        Type::Void => out.write_str("void"),
        Type::Bool => out.write_str("i1"),
        Type::Integer(integer) => write!(out, "i{}", integer.bits),
        Type::Float(Float::F32) => out.write_str("float"),
        Type::Float(Float::F64) => out.write_str("double"),
        Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => out.write_str("ptr"),
        Type::Slice { .. } => out.write_str("%slice"),
        Type::Struct { ref fields, .. } => struct_type_text(types, fields, out),
        Type::Range { element, step } => struct_type_text(types, &range_fields(element, step), out),
        Type::Array { element, length } => {
            write!(out, "[{length} x {}]", type_text(types, element))
        }
        Type::Invalid | Type::Function { .. } => unreachable!("MIR holds no value of this type"),
        Type::Alias { .. } => unreachable!("indexing looks through aliases"),
        Type::Named(_) => unreachable!("no value has a named type"),
    })
}

/// Whether a value of type `ty` is kept in memory, its LLVM value the
/// address there: a struct, an array or a range. LLVM splits a value of such a type
/// into one operation per part, so a big array as one value made clang slow,
/// then crash.
fn in_memory(types: &Types, ty: TypeId) -> bool {
    matches!(
        types[ty],
        Type::Struct { .. } | Type::Array { .. } | Type::Range { .. }
    )
}

/// A struct's LLVM type, `{ i32, i64 }`, of its `fields`' types. Out of
/// line, so `type_text`, which writes every operand's type, stays small
/// (measured: 11 M fewer instructions per compile of the benchmark); its
/// array arm recursing in line measured flat.
#[cold]
#[inline(never)]
fn struct_type_text(types: &Types, fields: &[TypeId], out: &mut fmt::Formatter) -> fmt::Result {
    out.write_str("{")?;
    for (index, &field) in fields.iter().enumerate() {
        let separator = if index > 0 { ", " } else { " " };
        write!(out, "{separator}{}", type_text(types, field))?;
    }
    out.write_str(if fields.is_empty() { "}" } else { " }" })
}

/// The size and alignment in bytes of a value of type `ty` in memory, as LLVM
/// lays it out: an integer is aligned as the next of 8, 16, 32, 64 and 128
/// bits that holds it, and wider ones as 128; its size is rounded up to that.
/// Debug info places each field with it, and a copy of a value kept in
/// memory (`in_memory`) copies its size, so it must match LLVM's.
///
/// shortcut: the alignments of 64-bit targets (x86-64, AArch64), the only
/// ones built; 32-bit x86 aligns `i64` and `f64` to 4. Read them from the
/// target's data layout when a 32-bit target is built.
fn layout(types: &Types, ty: TypeId, pointer_bits: u32) -> (u64, u64) {
    match types[ty] {
        Type::Bool => (1, 1),
        Type::Integer(integer) => {
            let align: u64 = match integer.bits {
                1..=8 => 1,
                9..=16 => 2,
                17..=32 => 4,
                33..=64 => 8,
                _ => 16,
            };
            let bytes = u64::from(integer.bits.div_ceil(8));
            (bytes.next_multiple_of(align), align)
        }
        Type::Float(Float::F32) => (4, 4),
        Type::Float(Float::F64) => (8, 8),
        Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => {
            let bytes = u64::from(pointer_bits / 8);
            (bytes, bytes)
        }
        Type::Slice { .. } => {
            let bytes = u64::from(pointer_bits / 8);
            (2 * bytes, bytes)
        }
        Type::Struct { ref fields, .. } => {
            let (_, size, align) = struct_layout(types, fields, pointer_bits);
            (size, align)
        }
        Type::Range { element, step } => {
            let fields = range_fields(element, step);
            let (_, size, align) = struct_layout(types, &fields, pointer_bits);
            (size, align)
        }
        Type::Array { element, length } => {
            let (size, align) = layout(types, element, pointer_bits);
            // shortcut: an array over 2^64 bytes overflows here; reject one
            // past the target's address space at check when one is written.
            (size * length, align)
        }
        Type::Invalid | Type::Void | Type::Function { .. } | Type::Named(_) => {
            unreachable!("no value is laid out with this type")
        }
        Type::Alias { .. } => unreachable!("indexing looks through aliases"),
    }
}

/// The offset of each of `fields` in a struct holding them in order, and the
/// struct's size and alignment, in bytes (`layout`): each field at the next
/// multiple of its alignment, the size rounded up to the largest.
fn struct_layout(types: &Types, fields: &[TypeId], pointer_bits: u32) -> (Vec<u64>, u64, u64) {
    let (mut offsets, mut end, mut align) = (Vec::with_capacity(fields.len()), 0u64, 1);
    for &field in fields {
        let (size, field_align) = layout(types, field, pointer_bits);
        let offset = end.next_multiple_of(field_align);
        offsets.push(offset);
        end = offset + size;
        align = align.max(field_align);
    }
    (offsets, end.next_multiple_of(align), align)
}

/// The attribute that extends a value of type `ty` to C's `int` when it
/// passes to or from C code, as the C ABI requires of a narrower type; `None`
/// for any other type.
fn c_extension(types: &Types, ty: TypeId) -> Option<&'static str> {
    match types[ty] {
        Type::Bool => Some("zeroext"),
        Type::Integer(integer) if integer.bits < 32 && integer.signed => Some("signext"),
        Type::Integer(integer) if integer.bits < 32 => Some("zeroext"),
        _ => None,
    }
}

/// `constant` as an LLVM constant of its type, written from its bits
/// (docs/ir-design.md, "Integer values must match LLVM"): an integer up to
/// 64 bits in decimal, a wider one in hexadecimal, a float as its `f64` bits.
fn constant_text(types: &Types, constant: &Constant) -> impl fmt::Display {
    fmt::from_fn(move |out| match (&constant.value, &types[constant.ty]) {
        (Value::Bool(value), _) => write!(out, "{value}"),
        (Value::Integer(limbs), Type::Integer(integer)) if integer.bits <= 64 => {
            let low = limbs.as_slice()[0];
            if integer.signed {
                let unused = 64 - integer.bits;
                write!(out, "{}", ((low << unused) as i64) >> unused)
            } else {
                write!(out, "{low}")
            }
        }
        (Value::Integer(limbs), _) => {
            out.write_str("u0x")?;
            for limb in limbs.as_slice().iter().rev() {
                write!(out, "{limb:016X}")?;
            }
            Ok(())
        }
        (Value::Float(bits), _) => write!(out, "0x{bits:016X}"),
        (Value::Null, _) => out.write_str("null"),
        (Value::Bytes(_), _) => {
            unreachable!("a string constant is a global (`Module::add_string`)")
        }
    })
}

/// `bytes` in double quotes, as LLVM reads a string: a quote, a backslash
/// and each byte outside printable ASCII as `\XX`.
fn quoted(bytes: &[u8]) -> String {
    let mut text = String::with_capacity(bytes.len() + 2);
    text.push('"');
    for &byte in bytes {
        if byte.is_ascii_graphic() && byte != b'"' && byte != b'\\' || byte == b' ' {
            text.push(byte as char);
        } else {
            _ = write!(text, "\\{byte:02X}");
        }
    }
    text.push('"');
    text
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::path::PathBuf;

    use bigint::Limbs;
    use model::constant::Constant;
    use model::constant::Value;
    use model::semantic_types::TypeId;
    use model::semantic_types::Types;

    /// The LLVM text of the module `app:main` holding `text`, with a prelude
    /// declaring the runtime's `Exit`.
    fn ll(text: &str) -> String {
        let folder = PathBuf::from("/peeper-llvm-test");
        let prelude = "#[extern(\"peeper_rt_v1_exit\")]\nfn Exit(code: i32);\n";
        let project = project::Project {
            packages: HashMap::from([
                ("app".to_string(), folder.join("src")),
                ("core".to_string(), folder.join("core")),
            ]),
            entry: folder.join("src/main.peep"),
            root: folder.clone(),
            name: "app".to_string(),
        };
        let open_files = HashMap::from([
            (folder.join("src/main.peep"), text.to_string()),
            (folder.join("core/global.peep"), prelude.to_string()),
        ]);
        let program = project::load(&project, &open_files);
        assert!(program.checkable);
        let (modules, results) =
            super::write::write_modules(&program, 64, super::BuildMode::Debug, |queue| {
                compile::run(&program, 64, compile::Results::default(), false, |output| {
                    if let compile::Output::Function(compile::CheckedFunction {
                        definition,
                        mir,
                        types,
                        ..
                    }) = output
                    {
                        queue.add(definition, mir.unwrap(), types);
                    }
                })
            });
        assert_eq!(results.diagnostics().count(), 0);
        modules.last().unwrap().text()
    }

    /// A division not known at compile time checks first and continues in
    /// a new block, which the `phi` after `&&` names.
    #[test]
    fn traps_continue_in_a_new_block() {
        let text = ll("fn both(a: i32, b: i32) -> bool { return a > 0 && a / b > 1; }");
        for line in [
            "br i1 %i6.z, label %bb1.1.panic, label %bb1.1\nbb1.1:",
            "%i6.d = select i1 %i6.n, i32 1, i32 %i5",
            "%i8 = phi i1 [ false, %bb0 ], [ %i7, %bb1.1 ]",
            "call i32 @\"peeper_rt_v1_write\"(i32 2, ptr @.str.0, i32 67)",
            "call void @\"peeper_rt_v1_exit\"(i32 101)",
            "c\"panic: division by zero\\0A  --> /peeper-llvm-test/src/main.peep:1:51\\0A\\00\"",
        ] {
            assert!(text.contains(line), "{line}\n{text}");
        }
        let constant = ll("fn f(a: u8) -> u8 { return a / 3u8 << 2u8; }");
        assert!(!constant.contains("panic"), "{constant}");
    }

    /// A `c"..."` is a global string; a call of an extern function uses
    /// its `#[extern]` name.
    #[test]
    fn strings_and_extern_calls() {
        let text = ll("fn f(a: u8, b: f32) { let s = c\"hi\\n\"; Exit(3); }");
        for line in [
            "@.str.0 = private unnamed_addr constant [4 x i8] c\"hi\\0A\\00\"",
            "store ptr @.str.0, ptr %v2",
            "call void @\"peeper_rt_v1_exit\"(i32 3)",
            "declare void @\"peeper_rt_v1_exit\"(i32)",
            "define void @\"app:main.f\"(i8 %arg0, float %arg1)",
        ] {
            assert!(text.contains(line), "{line}\n{text}");
        }
    }

    /// C code gets a narrow integer extended to `int` by its signedness; a
    /// Peeper function gets it as it is.
    #[test]
    fn extern_calls_extend_narrow_integers() {
        let text = ll(
            "#[extern(\"c_narrow\")]\nfn narrow(a: i8, b: u8, c: bool, d: i32) -> i16;\n\
             fn local(a: i8) {}\n\
             fn f() { local(1i8); let x = narrow(1i8, 2u8, true, 4); }",
        );
        for line in [
            "declare signext i16 @\"c_narrow\"(i8 signext, i8 zeroext, i1 zeroext, i32)",
            "call signext i16 @\"c_narrow\"(i8 signext 1, i8 zeroext 2, i1 zeroext true, i32 4)",
            "call void @\"app:main.local\"(i8 1)",
        ] {
            assert!(text.contains(line), "{line}\n{text}");
        }
    }

    /// A variable of an inner block is in that block's scope, so a debugger
    /// shows the outer variable of the same name outside it. A block that
    /// declares nothing gets no scope: the inner one is in the function's.
    #[test]
    fn variables_have_the_scope_of_their_block() {
        let text = ll("fn f(p: i32) { let x = 1; { { let x = 2; } } }");
        let blocks: Vec<&str> = text
            .lines()
            .filter(|line| line.contains("!DILexicalBlock("))
            .collect();
        let variables: Vec<&str> = text
            .lines()
            .filter(|line| line.contains("!DILocalVariable(name: \"x\""))
            .collect();
        assert_eq!((blocks.len(), variables.len()), (1, 2), "{text}");
        let block = blocks[0]
            .split(" = ")
            .next()
            .unwrap()
            .trim_start_matches('!');
        assert_eq!(scope(variables[1]), block, "{text}");
        assert_eq!(scope(blocks[0]), scope(variables[0]), "{text}");
    }

    /// The metadata id after `scope: !` in the metadata `line`.
    fn scope(line: &str) -> &str {
        line.split("scope: !")
            .nth(1)
            .unwrap()
            .split(',')
            .next()
            .unwrap()
    }

    /// Each struct's field offsets and size match what LLVM gives on x86-64
    /// and AArch64 (measured with `getelementptr` folded by clang `-O1`).
    #[test]
    fn struct_layout_matches_llvm() {
        let mut types = Types::new();
        let mut integer = |name: &str| types.builtin(name, 64).unwrap();
        let (i8, i24, i40, i128, i200) = (
            integer("i8"),
            integer("i24"),
            integer("i40"),
            integer("i128"),
            integer("i200"),
        );
        let layout = |fields: &[TypeId]| {
            let (offsets, size, _) = super::struct_layout(&types, fields, 64);
            (offsets, size)
        };
        assert_eq!(layout(&[TypeId::I32, TypeId::I64]), (vec![0, 8], 16));
        assert_eq!(
            layout(&[TypeId::BOOL, i128, TypeId::I32]),
            (vec![0, 16, 32], 48)
        );
        assert_eq!(layout(&[i8, i24, i8]), (vec![0, 4, 8], 12));
        assert_eq!(layout(&[i8, i200, i8]), (vec![0, 16, 48], 64));
        assert_eq!(layout(&[i8, i40]), (vec![0, 8], 16));
        assert_eq!(
            layout(&[i8, TypeId::F64, TypeId::CSTR]),
            (vec![0, 8, 16], 24)
        );
        assert_eq!(layout(&[]), (vec![], 0));
    }

    #[test]
    fn constants_from_bits() {
        let mut types = Types::new();
        let u200 = types.builtin("u200", 64).unwrap();
        let i3 = types.builtin("i3", 64).unwrap();
        let constant = |ty: TypeId, value: Value| {
            super::constant_text(&types, &Constant { ty, value }).to_string()
        };
        assert_eq!(
            constant(TypeId::I8, Value::Integer(Limbs::new(8, &[0xff]))),
            "-1"
        );
        assert_eq!(constant(i3, Value::Integer(Limbs::new(3, &[4]))), "-4");
        assert_eq!(
            constant(TypeId::U8, Value::Integer(Limbs::new(8, &[0xff]))),
            "255"
        );
        assert_eq!(
            constant(u200, Value::Integer(Limbs::new(200, &[5, 0, 0, 1]))),
            "u0x0000000000000001000000000000000000000000000000000000000000000005"
        );
        assert_eq!(constant(TypeId::BOOL, Value::Bool(true)), "true");
        assert_eq!(
            constant(TypeId::F32, Value::Float(0.5f64.to_bits())),
            "0x3FE0000000000000"
        );
    }
}
