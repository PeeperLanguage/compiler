package target

import (
	"fmt"
	"runtime"
	"strings"
)

// bitsPerByte is the number of bits in a byte. Used to convert
// the configured ABI size in bits to its size in bytes.
const bitsPerByte = 8

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

var sizeBits = DefaultSizeBitsForArch(runtime.GOARCH)

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

// SizeBits returns the currently configured pointer size in bits.
// It defaults to the host architecture's native size and can be
// overridden by SetSizeBits.
func SizeBits() int {
	return sizeBits
}

// SetSizeBits configures the global pointer size. Passing 0 restores
// the default for the current GOARCH. Returns an error if bits is
// not 0, 32, or 64.
func SetSizeBits(bits int) error {
	if bits == 0 {
		bits = DefaultSizeBitsForArch(runtime.GOARCH)
	}
	if bits != Bits32 && bits != Bits64 {
		return fmt.Errorf("invalid target ABI size %d (expected %d or %d)", bits, Bits32, Bits64)
	}
	sizeBits = bits
	return nil
}

// PointerBytes returns the pointer size in bytes for the current
// configuration. The result is always either 4 or 8.
func PointerBytes() int64 {
	return int64(SizeBits() / bitsPerByte)
}
