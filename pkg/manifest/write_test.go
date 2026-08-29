package manifest

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveDependencyStateRestoresManifestWhenLockPublishFails(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, FileName)
	original := []byte("name = \"app\"\nbuild = \"program\"\n")
	if err := os.WriteFile(manifestPath, original, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, LockfileName), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	file.Dependencies["local"] = Dependency{Type: DependencyNeighbor, Path: "../local"}
	if err := SaveDependencyState(root, file, NewLockfile()); err == nil {
		t.Fatal("lock publish failure was ignored")
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("manifest changed after failed coordinated publish:\n%s", after)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("restored manifest mode = %o, want 640", info.Mode().Perm())
	}
}

func TestSaveDependencyStatePublishesBothFiles(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, FileName)
	if err := os.WriteFile(manifestPath, []byte("name = \"app\"\nbuild = \"program\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	file.Dependencies["local"] = Dependency{Type: DependencyNeighbor, Path: "../local"}
	lock := NewLockfile()
	if err := SaveDependencyState(root, file, lock); err != nil {
		t.Fatalf("SaveDependencyState: %v", err)
	}
	loaded, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Dependencies["local"]; !ok {
		t.Fatal("manifest dependency missing after coordinated publish")
	}
	if _, err := LoadLockfile(root); err != nil {
		t.Fatalf("load published lock: %v", err)
	}
}
