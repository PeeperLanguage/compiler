package prelude

import (
	"fmt"
	"os"
	"path/filepath"

	"compiler/internal/context"
)

// Auto-loaded Ember prelude file.
const GlobalPreludeFile = "global.em"

// Register global prelude source when it exists.
func Load(ctx *context.CompilerContext) error {
	if ctx == nil {
		return fmt.Errorf("nil compiler context")
	}
	preludePath := filepath.Join(ctx.Config.StdlibRoot, GlobalPreludeFile)
	content, err := os.ReadFile(preludePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("load prelude %s: %w", preludePath, err)
	}
	module := &context.Module{
		Key:        "stdlib:prelude/global",
		ImportPath: "prelude/global",
		FilePath:   preludePath,
		Origin:     context.ModuleOriginStdlib,
		Content:    string(content),
	}
	ctx.AddModule(module)
	return nil
}
