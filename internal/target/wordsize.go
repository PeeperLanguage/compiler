package target

import "strings"

// Pointer size constants (in bits) for the supported ABIs.
const (
	Bits32 = 32
	Bits64 = 64
)

// arch32 is the set of GOARCH values whose default
// pointer size is 32 bits. Keep in sync with the Go toolchain.
var arch32 = map[string]struct{}{
	"386":         {},
	"arm":         {},
	"mips":        {},
	"mipsle":      {},
	"mips64p32":   {},
	"mips64p32le": {},
	"ppc":         {},
	"riscv":       {},
	"s390":        {},
	"sparc":       {},
	"wasm":        {},
}

// DefaultSizeBitsForArch returns the default pointer size in bits
// for the given Go architecture string. Unknown architectures
// default to 64 bits, matching the behaviour of the Go runtime.
func DefaultSizeBitsForArch(arch string) int {
	arch = strings.ToLower(strings.TrimSpace(arch))
	if _, ok := arch32[arch]; ok {
		return Bits32
	}
	return Bits64
}
