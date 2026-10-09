// Package moduleid defines canonical compiler module identity.
package moduleid

import (
	"encoding/hex"
	"fmt"
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

// TypeIdentity anchors nominal type identity at its declaring module.
func TypeIdentity(owner ID, name string) string {
	return owner.String() + "::" + name
}

// ParseTypeIdentity reverses TypeIdentity. Generic instance identities append
// "<...>" to the name; the returned name stops before it.
func ParseTypeIdentity(identity string) (ID, string, bool) {
	var parts [4]string
	rest := identity
	for index := range parts {
		var ok bool
		if parts[index], rest, ok = unframe(rest); !ok {
			return ID{}, "", false
		}
	}
	name, ok := strings.CutPrefix(rest, "::")
	if !ok || name == "" {
		return ID{}, "", false
	}
	if end := strings.IndexByte(name, '<'); end >= 0 {
		name = name[:end]
	}
	return ID{Origin: parts[0], Namespace: parts[1], Dependency: parts[2], ImportPath: parts[3]}, name, true
}

// DisplayText renders identity for people reading compiler output:
// origin ':' [namespace '@'] [dependency '+'] import path. Escaping keeps the
// separators unambiguous, so distinct identities never share text.
func (id ID) DisplayText() string {
	var b strings.Builder
	b.WriteString(EscapeComponent(id.Origin))
	b.WriteByte(':')
	if id.Namespace != "" {
		b.WriteString(EscapeComponent(id.Namespace))
		b.WriteByte('@')
	}
	if id.Dependency != "" {
		b.WriteString(EscapeComponent(id.Dependency))
		b.WriteByte('+')
	}
	for index, segment := range strings.Split(id.ImportPath, "/") {
		if index > 0 {
			b.WriteByte('/')
		}
		b.WriteString(EscapeComponent(segment))
	}
	return b.String()
}

// EscapeComponent percent-encodes every byte outside [A-Za-z0-9-_.], so
// separators used around identity components cannot appear inside one.
func EscapeComponent(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
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

// unframe reads one Frame component from the front of s.
func unframe(s string) (string, string, bool) {
	lengthText, rest, ok := strings.Cut(s, "_")
	if !ok {
		return "", "", false
	}
	length, err := strconv.Atoi(lengthText)
	if err != nil || length < 0 || len(rest) < 2*length+1 || rest[2*length] != '_' {
		return "", "", false
	}
	value, err := hex.DecodeString(rest[:2*length])
	if err != nil {
		return "", "", false
	}
	return string(value), rest[2*length+1:], true
}
