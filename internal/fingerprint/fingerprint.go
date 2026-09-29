// Package fingerprint provides content and unordered-part digests used for
// incremental compiler invalidation.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
)

// Text identifies source content for incremental reuse. A false match would
// reuse stale compiler artifacts, so this uses a collision-resistant hash.
func Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Parts hashes unordered surface parts. Length prefixes distinguish an
// embedded newline from a boundary between two parts.
func Parts(parts []string) string {
	if len(parts) == 0 {
		return Text("")
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
