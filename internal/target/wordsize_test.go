package target

import (
	"strings"
	"testing"
)

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

func TestInfoUsesArchitectureWidthAndTriple(t *testing.T) {
	tests := []struct {
		os   string
		arch string
		bits int
		want string
	}{
		{os: "linux", arch: "386", bits: Bits32, want: "i386-unknown-linux-gnu"},
		{os: "linux", arch: "amd64", bits: Bits64, want: "x86_64-unknown-linux-gnu"},
	}
	for _, tt := range tests {
		t.Run(tt.arch, func(t *testing.T) {
			info, err := New(tt.os, tt.arch)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Valid() || info.PointerBits != tt.bits || info.IndexBits != tt.bits || info.LLVMTriple != tt.want {
				t.Fatalf("Info = %#v", info)
			}
		})
	}
}

func TestInfoRejectsUnsupportedTarget(t *testing.T) {
	_, err := New("linux", "mystery")
	if err == nil || !strings.Contains(err.Error(), "unsupported target combination") {
		t.Fatalf("New returned %v, want unsupported target combination", err)
	}
}

func TestArchFor32BitMode(t *testing.T) {
	tests := map[string]string{
		"amd64": "386",
		"arm64": "arm",
		"386":   "386",
	}
	for arch, want := range tests {
		got, err := ArchFor32BitMode(arch)
		if err != nil || got != want {
			t.Fatalf("ArchFor32BitMode(%q) = (%q, %v), want (%q, nil)", arch, got, err, want)
		}
	}
	if _, err := ArchFor32BitMode("riscv64"); err == nil {
		t.Fatal("expected unsupported 32-bit counterpart")
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
