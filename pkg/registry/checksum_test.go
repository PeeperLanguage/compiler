package registry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModuleChecksumUsesPathsAndContentsOnly(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(first, "src", "b.peep"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "a.peep"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "a.peep"), []byte("a"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "src", "b.peep"), []byte("b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(second, "a.peep"), time.Unix(1, 0), time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}

	firstChecksum, err := ModuleChecksum(first)
	if err != nil {
		t.Fatal(err)
	}
	secondChecksum, err := ModuleChecksum(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstChecksum != secondChecksum {
		t.Fatalf("metadata changed checksum: %q != %q", firstChecksum, secondChecksum)
	}
	if len(firstChecksum) != len("sha256:")+64 {
		t.Fatalf("checksum = %q", firstChecksum)
	}

	if err := os.WriteFile(filepath.Join(second, "src", "b.peep"), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	changed, err := ModuleChecksum(second)
	if err != nil {
		t.Fatal(err)
	}
	if changed == firstChecksum {
		t.Fatal("content change preserved checksum")
	}
}

func TestModuleChecksumRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source", filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ModuleChecksum(root); err == nil {
		t.Fatal("ModuleChecksum accepted symlink")
	}
}

func TestModuleChecksumLengthFramesPathsAndContents(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(first, "a"), []byte("bc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "ab"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	firstChecksum, err := ModuleChecksum(first)
	if err != nil {
		t.Fatal(err)
	}
	secondChecksum, err := ModuleChecksum(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstChecksum == secondChecksum {
		t.Fatal("path/content boundary collision")
	}
}
