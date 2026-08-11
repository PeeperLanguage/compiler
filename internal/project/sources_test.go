package project

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/pkg/peeper"
)

func TestDiscoverSourceFilesRecursesDeduplicatesAndSkipsBuild(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first"+peeper.SourceExt)
	second := filepath.Join(root, "nested", "second"+peeper.SourceExt)
	skipped := filepath.Join(root, "build", "skip"+peeper.SourceExt)
	for _, path := range []string{first, second, skipped} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fn Value() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := DiscoverSourceFiles([]string{root, first})
	if err != nil {
		t.Fatalf("DiscoverSourceFiles: %v", err)
	}
	if len(files) != 2 || files[0] != CanonicalPath(first) || files[1] != CanonicalPath(second) {
		t.Fatalf("discovered files = %v, want first and nested second", files)
	}
}
