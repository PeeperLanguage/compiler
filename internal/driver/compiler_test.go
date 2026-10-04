package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/fingerprint"
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
	nonempty := "fn sourceOverride() -> i32 { return 2; }\n"
	tests := []struct {
		name           string
		sourceOverride *string
		want           string
	}{
		{name: "disk", want: disk},
		{name: "empty source override", sourceOverride: &empty, want: ""},
		{name: "nonempty source override", sourceOverride: &nonempty, want: nonempty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := project.NewWithConfig(project.Config{RootDir: root, Extension: peeper.SourceExt}, diagnostics.NewDiagnosticBag())
			mod := CompileFile(ctx, path, tt.sourceOverride)
			if mod == nil {
				t.Fatalf("CompileFile returned nil")
			}
			if mod.ContentHash != fingerprint.Text(tt.want) {
				t.Fatalf("content hash = %q, want hash for %q", mod.ContentHash, tt.want)
			}
		})
	}
}
