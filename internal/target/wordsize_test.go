package target

import (
	"runtime"
	"strings"
	"testing"
)

// withSizeBits sets the global sizeBits for the duration of the test.
func withSizeBits(t *testing.T, bits int) {
	t.Helper()
	prev := SizeBits()
	if err := SetSizeBits(bits); err != nil {
		t.Fatalf("set abi size: %v", err)
	}
	t.Cleanup(func() {
		if err := SetSizeBits(prev); err != nil {
			t.Fatalf("restore abi size: %v", err)
		}
	})
}

func TestDefaultSizeBitsForArch(t *testing.T) {
	tests := []struct {
		arch string
		want int
	}{
		{"386", Bits32},
		{"arm", Bits32},
		{"mips", Bits32},
		{"mipsle", Bits32},
		{"mips64p32", Bits32},
		{"mips64p32le", Bits32},
		{"ppc", Bits32},
		{"riscv", Bits32},
		{"s390", Bits32},
		{"sparc", Bits32},
		{"wasm", Bits32},
		{"amd64", Bits64},
		{"arm64", Bits64},
		{"mips64", Bits64},
		{"mips64le", Bits64},
		{"ppc64", Bits64},
		{"ppc64le", Bits64},
		{"riscv64", Bits64},
		{"s390x", Bits64},
		{"sparc64", Bits64},
		{"wasm64", Bits64},
		// Case and whitespace handling.
		{"  ARM  ", Bits32},
		{"AmD64", Bits64},
		// Unknown arches default to 64.
		{"unknown", Bits64},
		{"", Bits64},
	}
	for _, tt := range tests {
		t.Run(tt.arch, func(t *testing.T) {
			if got := DefaultSizeBitsForArch(tt.arch); got != tt.want {
				t.Errorf("DefaultSizeBitsForArch(%q) = %d, want %d", tt.arch, got, tt.want)
			}
		})
	}
}

func TestDefaultSizeBitsForHost(t *testing.T) {
	want := DefaultSizeBitsForArch(runtime.GOARCH)
	if got := SizeBits(); got != want {
		t.Errorf("default SizeBits() = %d, want %d (matches %s)", got, want, runtime.GOARCH)
	}
}

func TestSetSizeBitsValid(t *testing.T) {
	for _, bits := range []int{0, Bits32, Bits64} {
		t.Run("", func(t *testing.T) {
			withSizeBits(t, bits)
			if bits == 0 {
				if SizeBits() != DefaultSizeBitsForArch(runtime.GOARCH) {
					t.Errorf("SetSizeBits(0) did not restore default")
				}
			} else if SizeBits() != bits {
				t.Errorf("SizeBits() = %d, want %d", SizeBits(), bits)
			}
		})
	}
}

func TestSetSizeBitsInvalid(t *testing.T) {
	for _, bits := range []int{1, 8, 16, 24, 128, -1, 33, 65} {
		t.Run("", func(t *testing.T) {
			withSizeBits(t, Bits64)
			err := SetSizeBits(bits)
			if err == nil {
				t.Fatalf("SetSizeBits(%d) returned nil, want error", bits)
			}
			if !strings.Contains(err.Error(), "invalid target ABI size") {
				t.Errorf("unexpected error message: %v", err)
			}
			if SizeBits() != Bits64 {
				t.Errorf("SizeBits() changed after invalid SetSizeBits: got %d", SizeBits())
			}
		})
	}
}

func TestThirtyTwoBitArchsCoverage(t *testing.T) {
	// Ensure the table is non-empty so we don't accidentally drop support
	// for every 32-bit arch during a refactor.
	if len(arch32) == 0 {
		t.Fatal("thirtyTwoBitArchs is empty")
	}
	// amd64 must never be classified as 32-bit.
	if _, ok := arch32["amd64"]; ok {
		t.Error("amd64 incorrectly classified as 32-bit")
	}
}
