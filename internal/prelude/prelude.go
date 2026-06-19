package prelude

import (
	"fmt"
	"os"
	"path/filepath"

	"compiler/internal/project"
	"compiler/pkg/peeper"
)

// Auto-loaded Peeper prelude file within the stdlib root.
const GlobalPreludeFile = "global" + peeper.SourceExt

// Register global prelude source when it exists.
func Load(ctx *project.CompilerContext) error {
	if ctx == nil {
		return fmt.Errorf("nil compiler context")
	}
	coreRoot, ok := ctx.LibraryRoot("core")
	if !ok || coreRoot == "" {
		return nil
	}
	preludePath := filepath.Join(coreRoot, GlobalPreludeFile)
	content, err := os.ReadFile(preludePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("load prelude %s: %w", preludePath, err)
	}
	module := &project.Module{
		Key:        "core:prelude/global",
		ImportPath: "prelude/global",
		FilePath:   preludePath,
		Namespace:  "core",
		Origin:     project.ModuleOriginStdlib,
		Content:    string(content),
	}
	ctx.AddModule(module)
	return nil
}
