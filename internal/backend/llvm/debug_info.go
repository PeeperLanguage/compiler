package llvm

import (
	"cmp"
	"fmt"
	"path"
	"strings"

	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/source"
	"compiler/pkg/peeper"
)

type llvmDebugEmitter struct {
	isEnabled      bool
	fileID         int
	compileUnitID  int
	emptyTupleID   int
	subroutineID   int
	debugFlagID    int
	platformFlagID int
	nextID         int
	defs           []string
	subprograms    map[*mir.Function]int
	locations      map[string]int
	types          map[ir.TypeID]int
}

func newLLVMDebugEmitter(mod *mir.Module, targetOS string, enabled bool) *llvmDebugEmitter {
	if !enabled {
		return &llvmDebugEmitter{}
	}
	filePath := ""
	if mod != nil {
		filePath = mod.FilePath
	}
	if strings.TrimSpace(filePath) == "" {
		filePath = "unknown" + peeper.SourceExt
	}
	fileName := path.Base(filePath)
	dir := path.Dir(filePath)
	if dir == "." {
		dir = ""
	}
	d := &llvmDebugEmitter{
		isEnabled:   true,
		subprograms: make(map[*mir.Function]int),
		locations:   make(map[string]int),
		types:       make(map[ir.TypeID]int),
	}
	d.fileID = d.define(`!DIFile(filename: %q, directory: %q)`, fileName, dir)
	d.compileUnitID = d.define(`distinct !DICompileUnit(language: DW_LANG_C, file: !%d, producer: %q, isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)`, d.fileID, "PeeperCompiler")
	d.emptyTupleID = d.define("!{}")
	d.subroutineID = d.define("!DISubroutineType(types: !%d)", d.emptyTupleID)
	d.debugFlagID = d.define(`!{i32 2, !"Debug Info Version", i32 3}`)
	if targetOS == "windows" {
		d.platformFlagID = d.define(`!{i32 2, !"CodeView", i32 1}`)
	} else {
		d.platformFlagID = d.define(`!{i32 7, !"Dwarf Version", i32 4}`)
	}
	return d
}

func (d *llvmDebugEmitter) define(format string, args ...any) int {
	if d == nil || !d.isEnabled {
		return -1
	}
	id := d.nextID
	d.nextID++
	text := format
	if len(args) > 0 {
		text = fmt.Sprintf(format, args...)
	}
	d.defs = append(d.defs, fmt.Sprintf("!%d = %s", id, text))
	return id
}

func (d *llvmDebugEmitter) functionID(fn *mir.Function) int {
	if d == nil || !d.isEnabled || fn == nil || fn.Location == nil {
		return -1
	}
	if id, ok := d.subprograms[fn]; ok {
		return id
	}
	line, _ := locationLineCol(fn.Location)
	id := d.define(`distinct !DISubprogram(name: %q, linkageName: %q, scope: !%d, file: !%d, line: %d, type: !%d, scopeLine: %d, spFlags: DISPFlagDefinition, unit: !%d)`,
		fn.Name,
		ir.SanitizeSymbolName(fn.Name),
		d.fileID,
		d.fileID,
		line,
		d.subroutineID,
		line,
		d.compileUnitID,
	)
	d.subprograms[fn] = id
	return id
}

func (d *llvmDebugEmitter) locationID(loc *source.Location, scopeID int) int {
	if d == nil || !d.isEnabled || scopeID < 0 || loc == nil {
		return -1
	}
	line, col := locationLineCol(loc)
	key := fmt.Sprintf("%d:%d:%d", scopeID, line, col)
	if id, ok := d.locations[key]; ok {
		return id
	}
	id := d.define("!DILocation(line: %d, column: %d, scope: !%d)", line, col, scopeID)
	d.locations[key] = id
	return id
}

func (d *llvmDebugEmitter) appendModuleMetadata(b *strings.Builder) {
	if d == nil || !d.isEnabled || b == nil {
		return
	}
	b.WriteString("\ndeclare void @llvm.dbg.declare(metadata, metadata, metadata)\n")
	b.WriteString("\n!llvm.dbg.cu = !{!")
	fmt.Fprintf(b, "%d", d.compileUnitID)
	b.WriteString("}\n")
	b.WriteString("!llvm.module.flags = !{!")
	fmt.Fprintf(b, "%d", d.platformFlagID)
	b.WriteString(", !")
	fmt.Fprintf(b, "%d", d.debugFlagID)
	b.WriteString("}\n")
	for _, def := range d.defs {
		b.WriteString(def)
		b.WriteString("\n")
	}
}

// declareLocals tells the debugger where each source variable lives. A debug
// build gives every listed variable a stack slot (see stackLocalSlots), so one
// dbg.declare per variable holds for the whole function.
func (e *llvmEmitter) declareLocals(b *llvmBuilder, fn *mir.Function) {
	if !e.debug.isEnabled || b.debugScopeID < 0 {
		return
	}
	// shortcut: a record covers the whole function, so a name declared twice
	// (two loops over `value`) would show the wrong one. Such names get no
	// record until MIR knows the lines each variable lives on.
	declared := make(map[string]int, len(fn.Locals))
	for _, local := range fn.Locals {
		declared[local.SourceName]++
	}
	for _, local := range fn.Locals {
		if local.SourceName == "" || declared[local.SourceName] > 1 {
			continue
		}
		slot, ok := b.localPtrs[local.Name]
		typeID := e.debugTypeID(local.Type)
		b.setLocation(local.Location)
		if !ok || typeID < 0 || b.debugLocationID < 0 {
			continue
		}
		arg := ""
		if local.Arg > 0 {
			arg = fmt.Sprintf("arg: %d, ", local.Arg)
		}
		line, _ := locationLineCol(local.Location)
		variable := e.debug.define(`!DILocalVariable(name: %q, %sscope: !%d, file: !%d, line: %d, type: !%d)`,
			local.SourceName, arg, b.debugScopeID, e.debug.fileID, line, typeID)
		b.line(fmt.Sprintf("call void @llvm.dbg.declare(metadata %s* %s, metadata !%d, metadata !DIExpression())", slot.Pointee.Text, slot.Text, variable))
	}
}

// debugScalars gives each scalar kind's DWARF encoding and size in bits; a
// size of 0 takes the type's own width. Only integers carry a sign.
var debugScalars = map[ir.TypeKind]struct {
	encoding string
	bits     int
}{
	ir.TypeInteger: {"DW_ATE_unsigned", 0},
	ir.TypeFloat:   {"DW_ATE_float", 0},
	ir.TypeBool:    {"DW_ATE_boolean", 8},
	ir.TypeByte:    {"DW_ATE_unsigned_char", 8},
	ir.TypeChar:    {"DW_ATE_UTF", 32},
}

// debugTypeID describes a type to the debugger, or returns -1 for one it
// cannot describe yet: structs and other aggregates need field offsets, which
// the backend leaves to LLVM.
func (e *llvmEmitter) debugTypeID(id ir.TypeID) int {
	if found, ok := e.debug.types[id]; ok {
		return found
	}
	typ, _ := e.mod.Types.Type(id)
	name := e.mod.Types.Text(id)
	result := -1
	if scalar, ok := debugScalars[typ.Kind]; ok {
		encoding := scalar.encoding
		if typ.IsSigned {
			encoding = "DW_ATE_signed"
		}
		result = e.debug.define(`!DIBasicType(name: %q, size: %d, encoding: %s)`, name, cmp.Or(scalar.bits, typ.Bits), encoding)
	} else if e.layout(id).Kind == llvmLayoutPointer {
		// A reference to a string, slice or interface is an aggregate, so it
		// does not get here.
		base := "null"
		if typ.Kind == ir.TypeReference {
			if elem := e.debugTypeID(typ.Elem); elem >= 0 {
				base = fmt.Sprintf("!%d", elem)
			}
		}
		result = e.debug.define(`!DIDerivedType(tag: DW_TAG_pointer_type, name: %q, baseType: %s, size: %d)`, name, base, e.target.PointerBits)
	}
	e.debug.types[id] = result
	return result
}

func locationLineCol(loc *source.Location) (int, int) {
	if loc == nil || loc.Start == nil {
		return 1, 1
	}
	line := loc.Start.Line
	col := loc.Start.Column
	if line < 1 {
		line = 1
	}
	if col < 1 {
		col = 1
	}
	return line, col
}
