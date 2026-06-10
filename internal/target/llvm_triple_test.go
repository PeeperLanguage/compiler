package target

import (
	"strings"
	"testing"
)

func TestLLVMTriple(t *testing.T) {
	tests := []struct {
		targetOS   string
		targetArch string
		want       string
	}{
		{targetOS: "linux", targetArch: "amd64", want: "x86_64-unknown-linux-gnu"},
		{targetOS: "linux", targetArch: "arm64", want: "aarch64-unknown-linux-gnu"},
		{targetOS: "windows", targetArch: "amd64", want: "x86_64-pc-windows-msvc"},
		{targetOS: "darwin", targetArch: "arm64", want: "aarch64-apple-darwin"},
		{targetOS: "freebsd", targetArch: "386", want: "i386-unknown-freebsd"},
		{targetOS: "wasip1", targetArch: "wasm", want: "wasm32-unknown-wasi"},
		{targetOS: "  Linux ", targetArch: " AmD64 ", want: "x86_64-unknown-linux-gnu"},
	}

	for _, tt := range tests {
		t.Run(tt.targetOS+"-"+tt.targetArch, func(t *testing.T) {
			got, err := LLVMTriple(tt.targetOS, tt.targetArch)
			if err != nil {
				t.Fatalf("LLVMTriple(%q, %q) error = %v", tt.targetOS, tt.targetArch, err)
			}
			if got != tt.want {
				t.Fatalf("LLVMTriple(%q, %q) = %q, want %q", tt.targetOS, tt.targetArch, got, tt.want)
			}
		})
	}
}

func TestLLVMTripleRejectsUnknownTarget(t *testing.T) {
	_, err := LLVMTriple("linux", "mystery")
	if err == nil {
		t.Fatal("LLVMTriple returned nil error for unknown arch")
	}
	if !strings.Contains(err.Error(), "unsupported target architecture") {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = LLVMTriple("mystery", "amd64")
	if err == nil {
		t.Fatal("LLVMTriple returned nil error for unknown os")
	}
	if !strings.Contains(err.Error(), "unsupported target operating system") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecutableExt(t *testing.T) {
	if got := ExecutableExt("windows"); got != ".exe" {
		t.Fatalf("ExecutableExt(windows) = %q, want .exe", got)
	}
	if got := ExecutableExt("linux"); got != "" {
		t.Fatalf("ExecutableExt(linux) = %q, want empty", got)
	}
}
