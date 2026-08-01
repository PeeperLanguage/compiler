package target

import (
	"fmt"
	"runtime"
)

// Info is immutable target metadata for one compiler context. Pointer and
// index widths derive from Arch, so generated ABI and LLVM triple agree.
type Info struct {
	OS          string
	Arch        string
	LLVMTriple  string
	PointerBits int
	IndexBits   int
}

// New resolves one supported target into metadata shared by compiler phases.
func New(targetOS, targetArch string) (Info, error) {
	targetOS = NormalizeOS(targetOS)
	targetArch = NormalizeArch(targetArch)
	triple, err := LLVMTriple(targetOS, targetArch)
	if err != nil {
		return Info{}, err
	}
	pointerBits := DefaultSizeBitsForArch(targetArch)
	return Info{
		OS:          targetOS,
		Arch:        targetArch,
		LLVMTriple:  triple,
		PointerBits: pointerBits,
		IndexBits:   pointerBits,
	}, nil
}

// Host returns metadata for the compiler host target.
func Host() Info {
	info, err := New(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		panic(fmt.Sprintf("unsupported compiler host target: %v", err))
	}
	return info
}

// Valid reports whether Info has a complete supported target identity.
func (i Info) Valid() bool {
	return i.OS != "" && i.Arch != "" && i.LLVMTriple != "" &&
		(i.PointerBits == Bits32 || i.PointerBits == Bits64) && i.IndexBits == i.PointerBits
}

// ArchFor32BitMode returns the compatible 32-bit architecture for -m32.
// Architectures without a supported LLVM 32-bit counterpart are rejected.
func ArchFor32BitMode(arch string) (string, error) {
	switch NormalizeArch(arch) {
	case "386", "arm", "mips", "mipsle", "wasm":
		return NormalizeArch(arch), nil
	case "amd64":
		return "386", nil
	case "arm64":
		return "arm", nil
	case "mips64":
		return "mips", nil
	case "mips64le":
		return "mipsle", nil
	default:
		return "", fmt.Errorf("target architecture %q has no supported 32-bit LLVM counterpart", arch)
	}
}
