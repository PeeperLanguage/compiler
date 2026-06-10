package target

import (
	"fmt"
	"runtime"
	"strings"
)

// LLVMTriple returns the canonical LLVM target triple for a normalized GOOS/GOARCH pair.
func LLVMTriple(targetOS, targetArch string) (string, error) {
	arch, err := llvmArch(targetArch)
	if err != nil {
		return "", err
	}
	platform, err := llvmPlatform(targetOS)
	if err != nil {
		return "", err
	}
	return arch + "-" + platform, nil
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

func llvmArch(targetArch string) (string, error) {
	switch arch := NormalizeArch(targetArch); arch {
	case "386":
		return "i386", nil
	case "amd64":
		return "x86_64", nil
	case "arm":
		return "arm", nil
	case "arm64":
		return "aarch64", nil
	case "loong64":
		return "loongarch64", nil
	case "mips":
		return "mips", nil
	case "mips64":
		return "mips64", nil
	case "mips64le":
		return "mips64el", nil
	case "mipsle":
		return "mipsel", nil
	case "ppc64":
		return "powerpc64", nil
	case "ppc64le":
		return "powerpc64le", nil
	case "riscv64":
		return "riscv64", nil
	case "s390x":
		return "s390x", nil
	case "wasm":
		return "wasm32", nil
	default:
		return "", fmt.Errorf("unsupported target architecture %q", targetArch)
	}
}

func llvmPlatform(targetOS string) (string, error) {
	switch os := NormalizeOS(targetOS); os {
	case "aix":
		return "ibm-aix", nil
	case "android":
		return "linux-android", nil
	case "darwin":
		return "apple-darwin", nil
	case "dragonfly":
		return "unknown-dragonfly", nil
	case "freebsd":
		return "unknown-freebsd", nil
	case "illumos":
		return "unknown-illumos", nil
	case "ios":
		return "apple-ios", nil
	case "linux":
		return "unknown-linux-gnu", nil
	case "netbsd":
		return "unknown-netbsd", nil
	case "openbsd":
		return "unknown-openbsd", nil
	case "solaris":
		return "sun-solaris", nil
	case "wasip1":
		return "unknown-wasi", nil
	case "windows":
		return "pc-windows-msvc", nil
	default:
		return "", fmt.Errorf("unsupported target operating system %q", targetOS)
	}
}
