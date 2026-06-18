package ast

import (
	"hash/fnv"
	"sort"
	"strings"
)

// These helpers stay in syntax layer so parser can compute stable module
// surfaces without depending on project/LSP orchestration packages.

// HashText gives one stable, cheap content identity for incremental cache
// decisions. Collisions only cause extra rebuilds, never wrong codegen.
func HashText(text string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return hashHex(h.Sum64())
}

// FingerprintParts hashes unordered syntax-surface parts. Sorting keeps the
// fingerprint stable even when callers discover items in different orders.
func FingerprintParts(parts []string) string {
	if len(parts) == 0 {
		return HashText("")
	}
	sorted := append([]string(nil), parts...)
	sort.Strings(sorted)
	return HashText(strings.Join(sorted, "\n"))
}

func ImportPathFromDecl(imp *ImportDecl) (string, bool) {
	if imp == nil || imp.Path == nil {
		return "", false
	}
	switch node := imp.Path.(type) {
	case *StringLit:
		return node.Value, true
	case *Ident:
		return node.Name, true
	default:
		return "", false
	}
}

func hashHex(v uint64) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = digits[v&0xf]
		v >>= 4
	}
	return string(out)
}
