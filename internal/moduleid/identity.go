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

func (id ID) Valid() bool {
	return id.Origin != "" && id.ImportPath != ""
}

// String returns collision-safe deterministic encoding for string-only boundaries.
func (id ID) String() string {
	return Frame(id.Origin, id.Namespace, id.Dependency, id.ImportPath)
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
