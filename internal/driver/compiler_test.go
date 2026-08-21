package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/pkg/peeper"
)

func TestCompileFileSourceSelection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main"+peeper.SourceExt)
	disk := "fn disk() -> i32 { return 1; }\n"
	if err := os.WriteFile(path, []byte(disk), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	empty := ""
	nonempty := "fn overlay() -> i32 { return 2; }\n"
	tests := []struct {
		name    string
		overlay *string
		want    string
	}{
		{name: "disk", want: disk},
		{name: "empty overlay", overlay: &empty, want: ""},
		{name: "nonempty overlay", overlay: &nonempty, want: nonempty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := project.NewWithConfig(project.Config{RootDir: root, Extension: peeper.SourceExt}, diagnostics.NewDiagnosticBag())
			mod := CompileFile(ctx, path, tt.overlay)
			if mod == nil {
				t.Fatalf("CompileFile returned nil")
			}
			if mod.ContentHash != ast.HashText(tt.want) {
				t.Fatalf("content hash = %q, want hash for %q", mod.ContentHash, tt.want)
			}
		})
	}
}
