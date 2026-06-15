package llvm

import (
	"fmt"
	"path/filepath"
	"strings"

	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/source"
)

type llvmDebugEmitter struct {
	enabled        bool
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
		filePath = "unknown.peep"
	}
	fileName := filepath.Base(filePath)
	dir := filepath.Dir(filePath)
	if dir == "." {
		dir = ""
	}
	d := &llvmDebugEmitter{
		enabled:     true,
		subprograms: make(map[*mir.Function]int),
		locations:   make(map[string]int),
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
	if d == nil || !d.enabled {
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
	if d == nil || !d.enabled || fn == nil || fn.Location == nil {
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
	if d == nil || !d.enabled || scopeID < 0 || loc == nil {
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
	if d == nil || !d.enabled || b == nil {
		return
	}
	b.WriteString("\n!llvm.dbg.cu = !{!")
	b.WriteString(fmt.Sprintf("%d", d.compileUnitID))
	b.WriteString("}\n")
	b.WriteString("!llvm.module.flags = !{!")
	b.WriteString(fmt.Sprintf("%d", d.platformFlagID))
	b.WriteString(", !")
	b.WriteString(fmt.Sprintf("%d", d.debugFlagID))
	b.WriteString("}\n")
	for _, def := range d.defs {
		b.WriteString(def)
		b.WriteString("\n")
	}
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
