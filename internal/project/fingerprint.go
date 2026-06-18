package project

import (
	"hash/fnv"
	"sort"
	"strings"
)

// HashText gives one stable, cheap content identity shared by incremental
// cache code. FNV is enough here because collisions only cause extra rebuilds,
// never wrong code generation.
func HashText(text string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return hashHex(h.Sum64())
}

// FingerprintParts hashes unordered source-surface parts. Sorting keeps the
// fingerprint stable even when callers discover items in different orders.
func FingerprintParts(parts []string) string {
	if len(parts) == 0 {
		return HashText("")
	}
	sorted := append([]string(nil), parts...)
	sort.Strings(sorted)
	return HashText(strings.Join(sorted, "\n"))
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
