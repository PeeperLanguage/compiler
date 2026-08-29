package target

import (
	"runtime"
	"strings"
	"testing"
)

func TestLLVMTriple(t *testing.T) {
	tests := []struct {
		targetOS   string
		targetArch string
		want       string
	}{
		{targetOS: "linux", targetArch: "amd64", want: "x86_64-unknown-linux-musl"},
		{targetOS: "linux", targetArch: "arm64", want: "aarch64-unknown-linux-musl"},
		{targetOS: "windows", targetArch: "amd64", want: "x86_64-w64-windows-gnu"},
		{targetOS: "darwin", targetArch: "arm64", want: "aarch64-apple-darwin"},
		{targetOS: "freebsd", targetArch: "386", want: "i386-unknown-freebsd"},
		{targetOS: "wasip1", targetArch: "wasm", want: "wasm32-unknown-wasi"},
		{targetOS: "  Linux ", targetArch: " AmD64 ", want: "x86_64-unknown-linux-musl"},
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

func TestSystemLLVMTripleUsesHostABI(t *testing.T) {
	for _, test := range []struct{ targetOS, targetArch, want string }{
		{"linux", "amd64", "x86_64-unknown-linux-gnu"},
		{"linux", "arm64", "aarch64-unknown-linux-gnu"},
		{"windows", "amd64", "x86_64-pc-windows-msvc"},
		{"windows", "arm64", "aarch64-pc-windows-msvc"},
		{"darwin", "arm64", "aarch64-apple-darwin"},
	} {
		if got, err := SystemLLVMTriple(test.targetOS, test.targetArch); err != nil || got != test.want {
			t.Fatalf("SystemLLVMTriple(%q, %q) = %q, %v; want %q", test.targetOS, test.targetArch, got, err, test.want)
		}
	}
}

func TestLLVMTripleRejectsUnknownTarget(t *testing.T) {
	for _, pair := range [][2]string{{"linux", "mystery"}, {"mystery", "amd64"}, {"linux", "wasm"}, {"wasip1", "amd64"}, {"darwin", "386"}, {"aix", "amd64"}, {"windows", "mips"}} {
		_, err := LLVMTriple(pair[0], pair[1])
		want := "unsupported target combination \"" + pair[0] + "/" + pair[1] + "\""
		if err == nil || err.Error() != want {
			t.Fatalf("LLVMTriple(%q, %q) error = %v, want %q", pair[0], pair[1], err, want)
		}
	}
}

func TestLLVMTripleAcceptsGoTargetIntersection(t *testing.T) {
	allowed := map[string][]string{
		"aix": {"ppc64"}, "android": {"386", "amd64", "arm", "arm64"}, "darwin": {"amd64", "arm64"},
		"dragonfly": {"amd64"}, "freebsd": {"386", "amd64", "arm", "arm64"}, "illumos": {"amd64"},
		"ios": {"amd64", "arm64"}, "linux": {"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le", "mipsle", "ppc64", "ppc64le", "riscv64", "s390x"},
		"netbsd": {"386", "amd64", "arm", "arm64"}, "openbsd": {"386", "amd64", "arm", "arm64", "ppc64", "riscv64"},
		"solaris": {"amd64"}, "wasip1": {"wasm"}, "windows": {"386", "amd64", "arm64"},
	}
	count := 0
	for targetOS, architectures := range allowed {
		for _, targetArch := range architectures {
			count++
			if triple, err := LLVMTriple(targetOS, targetArch); err != nil || triple == "" {
				t.Fatalf("LLVMTriple(%q, %q) = %q, %v", targetOS, targetArch, triple, err)
			}
		}
	}
	if count != len(llvmTriples) {
		t.Fatalf("accepted pair count = %d, implementation count = %d", count, len(llvmTriples))
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

func TestIsHostTargetUsesNormalizedTarget(t *testing.T) {
	if !IsHostTarget("  "+strings.ToUpper(runtime.GOOS)+" ", " "+strings.ToUpper(runtime.GOARCH)+"  ") {
		t.Fatal("normalized host target reported as non-host")
	}
	if IsHostTarget("unsupported-host-os", runtime.GOARCH) {
		t.Fatal("non-host target reported as host")
	}
}
