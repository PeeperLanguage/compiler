package llvm

import (
	backendllvm "compiler/internal/backend/llvm"
	"compiler/internal/ir/mir"
)

// Deprecated: moved to compiler/internal/backend/llvm.
func GenerateLLVMIR(mod *mir.Module) string {
	return backendllvm.GenerateLLVMIR(mod, nil)
}
