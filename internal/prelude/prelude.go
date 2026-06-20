package prelude

import (
	"fmt"
	"os"
	"path/filepath"

	"compiler/internal/project"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

// Auto-loaded Peeper prelude file within the stdlib root.
const GlobalPreludeFile = "global" + peeper.SourceExt

func globalPreludePath(ctx *project.CompilerContext) (string, bool) {
	if ctx == nil {
		return "", false
	}
	coreRoot, ok := ctx.LibraryRoot("core")
	if !ok || coreRoot == "" {
		return "", false
	}
	return filepath.Join(manifest.SourceDir(coreRoot), GlobalPreludeFile), true
}

// ModuleForFile returns the canonical prelude module identity when a file path
// points at the auto-loaded global prelude source. Direct-open and overlay
// paths must reuse this exact key/import-path so the same file does not appear
// twice in compiler and LSP caches.
func ModuleForFile(ctx *project.CompilerContext, filePath, content string) (*project.Module, bool) {
	preludePath, ok := globalPreludePath(ctx)
	if !ok || project.CanonicalPath(preludePath) != project.CanonicalPath(filePath) {
		return nil, false
	}
	return &project.Module{
		Key:        "core:prelude/global",
		ImportPath: "prelude/global",
		FilePath:   preludePath,
		Namespace:  "core",
		Origin:     project.ModuleOriginStdlib,
		Content:    content,
	}, true
}

// Register global prelude source when it exists.
func Load(ctx *project.CompilerContext) error {
	if ctx == nil {
		return fmt.Errorf("nil compiler context")
	}
	preludePath, ok := globalPreludePath(ctx)
	if !ok || preludePath == "" {
		return nil
	}
	content, err := os.ReadFile(preludePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("load prelude %s: %w", preludePath, err)
	}
	module, _ := ModuleForFile(ctx, preludePath, string(content))
	ctx.AddModule(module)
	return nil
}
