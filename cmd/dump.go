package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	
	"compiler/internal/backend"
	"compiler/internal/context"
)

// Write -keep-gen artifacts for each module.
func saveIRs(ctx *context.CompilerContext, backendName, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if ctx == nil {
		return fmt.Errorf("missing compiler context")
	}
	for _, module := range ctx.Modules() {
		base := strings.TrimSuffix(filepath.Base(module.FilePath), filepath.Ext(module.FilePath))
		hirText := ""
		if module.HIR != nil {
			hirText = module.HIR.Text()
		}
		if err := os.WriteFile(filepath.Join(dir, base+".hir"), []byte(hirText), 0o644); err != nil {
			return err
		}
		mirText := ""
		if module.MIR != nil {
			mirText = module.MIR.Text()
		}
		if err := os.WriteFile(filepath.Join(dir, base+".mir"), []byte(mirText), 0o644); err != nil {
			return err
		}
		if backendName == string(backend.LLVM) {
			if err := os.WriteFile(filepath.Join(dir, base+".ll"), []byte(module.LLVMIR), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}