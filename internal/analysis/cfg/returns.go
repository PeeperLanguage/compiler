package cfg

import (
	"compiler/core/diagnostics"
	"compiler/internal/ir/hir"
)

// CheckReturns validates that non-void functions return on all control paths.
// Runs after HIR fold so constant branches are simplified first.
func CheckReturns(mod *hir.Module, diag *diagnostics.DiagnosticBag) bool {
	if mod == nil || diag == nil {
		return true
	}
	ok := true
	for _, fn := range mod.Funcs {
		if fn == nil {
			continue
		}
		if fn.ReturnType == "" || fn.ReturnType == "void" {
			continue
		}
		if mustReturnBlock(fn.Body) {
			continue
		}
		ok = false
		reportMissingReturn(fn, diag)
	}
	return ok
}
