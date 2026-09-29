// Package moduleid defines canonical compiler module identity.
package moduleid

import (
	"encoding/hex"
	"strconv"
	"strings"
)

// ID is stable across filesystem relocation and shared by imports, symbols,
// compiler registries, graph boundaries, and linkage naming.
type ID struct {
	Origin     string
	Namespace  string
	Dependency string
	ImportPath string
}

// FunctionID identifies a callable declaration within one logical module.
// Unlike AST NodeID, it survives edits that add or remove syntax elsewhere in
// the module. Declaration surface intentionally excludes the function body;
// callers compare body and semantic inputs separately before reusing artifacts.
type FunctionID string

func (id ID) IsValid() bool {
	return id.Origin != "" && id.ImportPath != ""
}

// String returns collision-safe deterministic encoding for string-only boundaries.
func (id ID) String() string {
	return Frame(id.Origin, id.Namespace, id.Dependency, id.ImportPath)
}

// FunctionIdentity returns a stable declaration identity for one module.
// Occurrence distinguishes same-surface redeclarations during error recovery;
// valid declarations always have occurrence zero.
func FunctionIdentity(owner ID, declarationSurface string, occurrence int) FunctionID {
	if !owner.IsValid() || declarationSurface == "" || occurrence < 0 {
		return ""
	}
	return FunctionID(Frame("function", owner.String(), declarationSurface, strconv.Itoa(occurrence)))
}

// Frame encodes ordered identity and linkage components without delimiter collisions.
func Frame(components ...string) string {
	var b strings.Builder
	for _, component := range components {
		b.WriteString(strconv.Itoa(len(component)))
		b.WriteByte('_')
		b.WriteString(hex.EncodeToString([]byte(component)))
		b.WriteByte('_')
	}
	return b.String()
}
