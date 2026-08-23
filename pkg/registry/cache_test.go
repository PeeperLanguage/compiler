package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModuleCachePathAndDelete(t *testing.T) {
	cache := t.TempDir()
	repo := "github.com/acme/math"
	ver := "1.2.3"
	path, err := GetModulePath(cache, repo, ver)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "content"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DeleteModule(cache, repo, ver); err != nil {
		t.Fatalf("DeleteModule failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("module dir should be deleted, stat err=%v", err)
	}
}

func TestModuleCacheRejectsInvalidIdentity(t *testing.T) {
	tests := []struct {
		repo    string
		version string
	}{
		{repo: "github.com/acme/../../outside", version: "1.0.0"},
		{repo: "github.com/acme/pkg", version: "../../outside"},
		{repo: "github.com/acme/pkg", version: "bad\x00version"},
	}
	for _, test := range tests {
		if _, err := GetModulePath(t.TempDir(), test.repo, test.version); err == nil {
			t.Fatalf("GetModulePath(%q, %q) accepted invalid identity", test.repo, test.version)
		}
		if err := DeleteModule(t.TempDir(), test.repo, test.version); err == nil {
			t.Fatalf("DeleteModule(%q, %q) accepted invalid identity", test.repo, test.version)
		}
	}
}

func TestModulePathPreservesSafeLegacyVersion(t *testing.T) {
	cache := t.TempDir()
	path, err := GetModulePath(cache, " github.com/acme/pkg ", " v1 ")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cache, "github.com", "acme", "pkg@v1")
	if path != want {
		t.Fatalf("module path = %q, want %q", path, want)
	}
}

func TestDownloadHelpers(t *testing.T) {
	url, err := packageArchiveURL("github.com/a/b", "v1.0.0")
	if err != nil || url == "" {
		t.Fatalf("github archive URL failed: %q err=%v", url, err)
	}
	if _, err := packageArchiveURL("example.com/a/b", "v1.0.0"); err == nil {
		t.Fatalf("expected unsupported host error")
	}
}
