package target

import (
	"fmt"
	"runtime"
	"strings"
)

type targetKey struct {
	OS   string
	Arch string
}

var llvmTriples = map[targetKey]string{
	{OS: "aix", Arch: "ppc64"}:       "powerpc64-ibm-aix",
	{OS: "android", Arch: "386"}:     "i386-linux-android",
	{OS: "android", Arch: "amd64"}:   "x86_64-linux-android",
	{OS: "android", Arch: "arm"}:     "arm-linux-android",
	{OS: "android", Arch: "arm64"}:   "aarch64-linux-android",
	{OS: "darwin", Arch: "amd64"}:    "x86_64-apple-darwin",
	{OS: "darwin", Arch: "arm64"}:    "aarch64-apple-darwin",
	{OS: "dragonfly", Arch: "amd64"}: "x86_64-unknown-dragonfly",
	{OS: "freebsd", Arch: "386"}:     "i386-unknown-freebsd",
	{OS: "freebsd", Arch: "amd64"}:   "x86_64-unknown-freebsd",
	{OS: "freebsd", Arch: "arm"}:     "arm-unknown-freebsd",
	{OS: "freebsd", Arch: "arm64"}:   "aarch64-unknown-freebsd",
	{OS: "illumos", Arch: "amd64"}:   "x86_64-unknown-illumos",
	{OS: "ios", Arch: "amd64"}:       "x86_64-apple-ios",
	{OS: "ios", Arch: "arm64"}:       "aarch64-apple-ios",
	{OS: "linux", Arch: "386"}:       "i386-unknown-linux-gnu",
	{OS: "linux", Arch: "amd64"}:     "x86_64-unknown-linux-musl",
	{OS: "linux", Arch: "arm"}:       "arm-unknown-linux-gnu",
	{OS: "linux", Arch: "arm64"}:     "aarch64-unknown-linux-musl",
	{OS: "linux", Arch: "loong64"}:   "loongarch64-unknown-linux-gnu",
	{OS: "linux", Arch: "mips"}:      "mips-unknown-linux-gnu",
	{OS: "linux", Arch: "mips64"}:    "mips64-unknown-linux-gnu",
	{OS: "linux", Arch: "mips64le"}:  "mips64el-unknown-linux-gnu",
	{OS: "linux", Arch: "mipsle"}:    "mipsel-unknown-linux-gnu",
	{OS: "linux", Arch: "ppc64"}:     "powerpc64-unknown-linux-gnu",
	{OS: "linux", Arch: "ppc64le"}:   "powerpc64le-unknown-linux-gnu",
	{OS: "linux", Arch: "riscv64"}:   "riscv64-unknown-linux-gnu",
	{OS: "linux", Arch: "s390x"}:     "s390x-unknown-linux-gnu",
	{OS: "netbsd", Arch: "386"}:      "i386-unknown-netbsd",
	{OS: "netbsd", Arch: "amd64"}:    "x86_64-unknown-netbsd",
	{OS: "netbsd", Arch: "arm"}:      "arm-unknown-netbsd",
	{OS: "netbsd", Arch: "arm64"}:    "aarch64-unknown-netbsd",
	{OS: "openbsd", Arch: "386"}:     "i386-unknown-openbsd",
	{OS: "openbsd", Arch: "amd64"}:   "x86_64-unknown-openbsd",
	{OS: "openbsd", Arch: "arm"}:     "arm-unknown-openbsd",
	{OS: "openbsd", Arch: "arm64"}:   "aarch64-unknown-openbsd",
	{OS: "openbsd", Arch: "ppc64"}:   "powerpc64-unknown-openbsd",
	{OS: "openbsd", Arch: "riscv64"}: "riscv64-unknown-openbsd",
	{OS: "solaris", Arch: "amd64"}:   "x86_64-sun-solaris",
	{OS: "wasip1", Arch: "wasm"}:     "wasm32-unknown-wasi",
	{OS: "windows", Arch: "386"}:     "i386-pc-windows-msvc",
	{OS: "windows", Arch: "amd64"}:   "x86_64-w64-windows-gnu",
	{OS: "windows", Arch: "arm64"}:   "aarch64-w64-windows-gnu",
}

// SystemLLVMTriple returns the host ABI expected by an unmanaged system Clang.
func SystemLLVMTriple(targetOS, targetArch string) (string, error) {
	targetOS = NormalizeOS(targetOS)
	targetArch = NormalizeArch(targetArch)
	key := targetKey{OS: targetOS, Arch: targetArch}
	switch key {
	case targetKey{OS: "linux", Arch: "amd64"}:
		return "x86_64-unknown-linux-gnu", nil
	case targetKey{OS: "linux", Arch: "arm64"}:
		return "aarch64-unknown-linux-gnu", nil
	case targetKey{OS: "windows", Arch: "amd64"}:
		return "x86_64-pc-windows-msvc", nil
	case targetKey{OS: "windows", Arch: "arm64"}:
		return "aarch64-pc-windows-msvc", nil
	default:
		return LLVMTriple(targetOS, targetArch)
	}
}

// LLVMTriple returns the canonical LLVM target triple for a normalized GOOS/GOARCH pair.
func LLVMTriple(targetOS, targetArch string) (string, error) {
	targetOS = NormalizeOS(targetOS)
	targetArch = NormalizeArch(targetArch)
	if triple, ok := llvmTriples[targetKey{OS: targetOS, Arch: targetArch}]; ok {
		return triple, nil
	}
	return "", fmt.Errorf("unsupported target combination %q", targetOS+"/"+targetArch)
}

// NormalizeOS trims and lowercases a target OS, defaulting to host when empty.
func NormalizeOS(targetOS string) string {
	targetOS = strings.ToLower(strings.TrimSpace(targetOS))
	if targetOS == "" {
		return runtime.GOOS
	}
	return targetOS
}

// NormalizeArch trims and lowercases a target arch, defaulting to host when empty.
func NormalizeArch(targetArch string) string {
	targetArch = strings.ToLower(strings.TrimSpace(targetArch))
	if targetArch == "" {
		return runtime.GOARCH
	}
	return targetArch
}

// ExecutableExt returns platform executable suffix for the target OS.
func ExecutableExt(targetOS string) string {
	if NormalizeOS(targetOS) == "windows" {
		return ".exe"
	}
	return ""
}

// IsHostTarget reports whether the target matches current host OS/arch.
func IsHostTarget(targetOS, targetArch string) bool {
	return NormalizeOS(targetOS) == runtime.GOOS && NormalizeArch(targetArch) == runtime.GOARCH
}
