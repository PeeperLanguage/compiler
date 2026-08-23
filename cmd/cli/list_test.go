package cli

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/pkg/manifest"
)

func TestListCommandPropagatesMalformedLockfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte("name = \"app\"\nbuild = \"program\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifest.LockfileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)

	if err := ListCommand(nil); err == nil {
		t.Fatal("ListCommand ignored malformed lockfile")
	}
}

func TestListCommandAllowsMissingLockfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte("name = \"app\"\nbuild = \"program\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	if err := ListCommand(nil); err != nil {
		t.Fatalf("ListCommand with no lockfile: %v", err)
	}
}
