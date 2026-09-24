package ast

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
)

// These helpers stay in syntax layer so parser can compute stable module
// surfaces without depending on project/LSP orchestration packages.

// HashText identifies source content for incremental reuse. A false match
// would reuse stale compiler artifacts, so this uses a collision-resistant hash.
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// FingerprintParts hashes unordered syntax-surface parts. Length prefixes
// distinguish an embedded newline from a boundary between two parts.
func FingerprintParts(parts []string) string {
	if len(parts) == 0 {
		return HashText("")
	}
	sorted := append([]string(nil), parts...)
	sort.Strings(sorted)
	h := sha256.New()
	var length [8]byte
	for _, part := range sorted {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
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
