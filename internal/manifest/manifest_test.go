package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(`name = "myapp"`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Read(root)
	if err != nil || m.Name != "myapp" || m.Root != root {
		t.Fatalf("Read = %+v, %v", m, err)
	}
	sub := filepath.Join(root, "src")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(sub); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read of a folder without the file: err = %v (folders above must not be searched)", err)
	}
}

func TestReadRejectsBadName(t *testing.T) {
	for _, name := range []string{`"my-app"`, `"_app"`, `"1app"`, `""`} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, FileName), []byte("name = "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(root); err == nil {
			t.Errorf("name %s was accepted", name)
		}
	}
}
