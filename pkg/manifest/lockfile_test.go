package manifest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLockfileMigratesLegacyShapes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		alias   string
	}{
		{
			name: "missing version with dependencies",
			content: `{
  "direct_deps": ["github.com/acme/json"],
  "dependencies": {
    "github.com/acme/json": {
      "version": "v1.2.3",
      "direct": true
    }
  }
}`,
			alias: "github.com/acme/json",
		},
		{
			name: "version 1 with dependencies",
			content: `{
  "version": "1.0",
  "direct_deps": ["github.com/acme/json"],
  "dependencies": {
    "github.com/acme/json": {
      "version": "v1.2.3",
      "direct": true
    }
  }
}`,
			alias: "github.com/acme/json",
		},
		{
			name: "mislabelled version 1 with packages",
			content: `{
  "version": "1.0",
  "direct_deps": {
    "json": "github.com/acme/json@v1.2.3"
  },
  "packages": {
    "github.com/acme/json@v1.2.3": {
      "version": "v1.2.3",
      "direct": true
    }
  }
}`,
			alias: "json",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, LockfileName), []byte(test.content), 0o644); err != nil {
				t.Fatal(err)
			}

			lock, err := LoadLockfile(root)
			if err != nil {
				t.Fatalf("load lockfile: %v", err)
			}
			if lock.Version != "2.0" {
				t.Fatalf("migrated version = %q, want 2.0", lock.Version)
			}
			if got := lock.DirectDeps[test.alias]; got != "github.com/acme/json@v1.2.3" {
				t.Fatalf("migrated direct dep = %q", got)
			}
			if _, ok := lock.Packages["github.com/acme/json@v1.2.3"]; !ok {
				t.Fatal("expected migrated package entry")
			}
		})
	}
}

func TestLoadLockfileReadsV2Shape(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, LockfileName)
	content := `{
  "version": "2.0",
  "direct_deps": {
    "json": "github.com/acme/json@v1.2.3"
  },
  "packages": {
    "github.com/acme/json@v1.2.3": {
      "version": "v1.2.3",
      "direct": true
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, err := LoadLockfile(root)
	if err != nil {
		t.Fatalf("load lockfile: %v", err)
	}
	if got := lock.DirectDeps["json"]; got != "github.com/acme/json@v1.2.3" {
		t.Fatalf("unexpected direct dep mapping %q", got)
	}
	if _, ok := lock.Packages["github.com/acme/json@v1.2.3"]; !ok {
		t.Fatalf("expected v2 package entry")
	}
}

func TestSaveLockfileOmitsLegacyDependenciesField(t *testing.T) {
	root := t.TempDir()
	lock := NewLockfile()
	lock.SetDirectDependency("json", "github.com/acme/json@v1.2.3")
	lock.SetDependency("github.com/acme/json@v1.2.3", LockfileEntry{
		Version:     "v1.2.3",
		ResolvedURL: "github.com/acme/json",
		Direct:      true,
	})

	if err := SaveLockfile(root, lock); err != nil {
		t.Fatalf("save lockfile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"version": "2.0"`) {
		t.Fatalf("expected v2 lockfile:\n%s", text)
	}
	if strings.Contains(text, `"dependencies"`) {
		t.Fatalf("expected saved lockfile to omit legacy dependencies field:\n%s", text)
	}
	if !strings.Contains(text, `"packages"`) {
		t.Fatalf("expected saved lockfile to include packages field:\n%s", text)
	}
}

func TestLoadLockfileRejectsUnsupportedVersionWithoutRewrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, LockfileName)
	content := []byte(`{"version":"3.0","packages":{}}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadLockfile(root); err == nil || !strings.Contains(err.Error(), "unsupported lockfile version") {
		t.Fatalf("LoadLockfile error = %v, want unsupported version", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, content) {
		t.Fatalf("unsupported lockfile changed: %q", after)
	}
}

func TestLoadLockfileRejectsInvalidSupportedSchemas(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "null document", content: `null`},
		{name: "v2 legacy fields", content: `{"version":"2.0","direct_deps":[],"dependencies":{}}`},
		{name: "v2 top-level unknown field", content: `{"version":"2.0","packages":{},"extra":true}`},
		{name: "v2 package unknown field", content: `{"version":"2.0","packages":{"repo@v1":{"version":"v1","extra":true}}}`},
		{name: "v1 top-level unknown field", content: `{"version":"1.0","dependencies":{},"extra":true}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, LockfileName), []byte(test.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadLockfile(root); err == nil {
				t.Fatal("LoadLockfile accepted invalid schema")
			}
		})
	}
}

func TestLoadLockfileRejectsInvalidChecksum(t *testing.T) {
	root := t.TempDir()
	content := `{"version":"2.0","packages":{"repo@v1":{"version":"v1","checksum":"sha256:not-hex"}}}`
	if err := os.WriteFile(filepath.Join(root, LockfileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLockfile(root); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("LoadLockfile checksum error = %v", err)
	}
}

func TestLoadLockfileNormalizesUppercaseChecksumHex(t *testing.T) {
	root := t.TempDir()
	checksum := "sha256:" + strings.Repeat("A", 64)
	content := `{"version":"2.0","packages":{"repo@v1":{"version":"v1","checksum":"` + checksum + `"}}}`
	if err := os.WriteFile(filepath.Join(root, LockfileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := lock.GetDependency("repo@v1")
	if !ok || entry.Checksum != strings.ToLower(checksum) {
		t.Fatalf("normalized checksum = %q", entry.Checksum)
	}
}

func TestSetDirectDependencyDemotesPreviousVersion(t *testing.T) {
	lock := NewLockfile()
	lock.SetDependency("github.com/acme/json@v1.0.0", LockfileEntry{
		Version:     "v1.0.0",
		ResolvedURL: "github.com/acme/json",
		Direct:      true,
	})
	lock.SetDependency("github.com/acme/json@v1.1.0", LockfileEntry{
		Version:     "v1.1.0",
		ResolvedURL: "github.com/acme/json",
		Direct:      true,
	})

	lock.SetDirectDependency("json", "github.com/acme/json@v1.0.0")
	lock.SetDirectDependency("json", "github.com/acme/json@v1.1.0")

	oldEntry, ok := lock.GetDependency("github.com/acme/json@v1.0.0")
	if !ok {
		t.Fatal("expected old package entry")
	}
	if oldEntry.Direct {
		t.Fatalf("expected old direct package to be demoted")
	}
	newEntry, ok := lock.GetDependency("github.com/acme/json@v1.1.0")
	if !ok {
		t.Fatal("expected new package entry")
	}
	if !newEntry.Direct {
		t.Fatalf("expected new package entry to stay direct")
	}
}

func TestLoadLockfileReconcilesDirectFlagsFromDirectDeps(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, LockfileName)
	content := `{
  "version": "2.0",
  "direct_deps": {
    "json": "github.com/acme/json@v1.1.0"
  },
  "packages": {
    "github.com/acme/json@v1.0.0": {
      "version": "v1.0.0",
      "resolved_url": "github.com/acme/json",
      "direct": true
    },
    "github.com/acme/json@v1.1.0": {
      "version": "v1.1.0",
      "resolved_url": "github.com/acme/json",
      "direct": true
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, err := LoadLockfile(root)
	if err != nil {
		t.Fatalf("load lockfile: %v", err)
	}
	oldEntry, ok := lock.GetDependency("github.com/acme/json@v1.0.0")
	if !ok {
		t.Fatal("expected old package")
	}
	if oldEntry.Direct {
		t.Fatalf("expected old package to be non-direct after reconcile")
	}
	newEntry, ok := lock.GetDependency("github.com/acme/json@v1.1.0")
	if !ok {
		t.Fatal("expected new package")
	}
	if !newEntry.Direct {
		t.Fatalf("expected mapped direct package to be direct")
	}
}

func TestUpdateDependencyEdgesRewiresUsedBy(t *testing.T) {
	lock := NewLockfile()
	lock.SetDependency("A@v1", LockfileEntry{Version: "v1", ResolvedURL: "A", Dependencies: []string{"B@v1"}})
	lock.SetDependency("B@v1", LockfileEntry{Version: "v1", ResolvedURL: "B", UsedBy: []string{"A@v1"}})
	lock.SetDependency("C@v1", LockfileEntry{Version: "v1", ResolvedURL: "C"})

	lock.UpdateDependencyEdges("A@v1", []string{"C@v1"})

	b, ok := lock.GetDependency("B@v1")
	if !ok {
		t.Fatal("expected B entry")
	}
	if len(b.UsedBy) != 0 {
		t.Fatalf("expected B to drop used_by, got %#v", b.UsedBy)
	}
	c, ok := lock.GetDependency("C@v1")
	if !ok {
		t.Fatal("expected C entry")
	}
	if len(c.UsedBy) != 1 || c.UsedBy[0] != "A@v1" {
		t.Fatalf("expected C used_by to include A@v1, got %#v", c.UsedBy)
	}
	a, ok := lock.GetDependency("A@v1")
	if !ok {
		t.Fatal("expected A entry")
	}
	if len(a.Dependencies) != 1 || a.Dependencies[0] != "C@v1" {
		t.Fatalf("expected A dependencies rewired to C@v1, got %#v", a.Dependencies)
	}
}

func TestRemoveDependencyDetachesGraphReferences(t *testing.T) {
	lock := NewLockfile()
	lock.SetDependency("A@v1", LockfileEntry{
		Version:      "v1",
		ResolvedURL:  "A",
		Dependencies: []string{"B@v1"},
	})
	lock.SetDependency("B@v1", LockfileEntry{
		Version:      "v1",
		ResolvedURL:  "B",
		Dependencies: []string{"C@v1"},
		UsedBy:       []string{"A@v1"},
	})
	lock.SetDependency("C@v1", LockfileEntry{
		Version:     "v1",
		ResolvedURL: "C",
		UsedBy:      []string{"B@v1"},
	})

	lock.RemoveDependency("B@v1")

	if _, ok := lock.GetDependency("B@v1"); ok {
		t.Fatal("expected B to be removed")
	}
	a, ok := lock.GetDependency("A@v1")
	if !ok {
		t.Fatal("expected A entry")
	}
	if len(a.Dependencies) != 0 {
		t.Fatalf("expected A dependencies to drop B, got %#v", a.Dependencies)
	}
	c, ok := lock.GetDependency("C@v1")
	if !ok {
		t.Fatal("expected C entry")
	}
	if len(c.UsedBy) != 0 {
		t.Fatalf("expected C used_by to drop B, got %#v", c.UsedBy)
	}
}

func TestEntryRepoOfPrefersResolvedURL(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		entry LockfileEntry
		want  string
	}{
		{
			name:  "uses ResolvedURL when present",
			key:   "github.com/acme/json@v1.2.3",
			entry: LockfileEntry{ResolvedURL: "github.com/acme/json"},
			want:  "github.com/acme/json",
		},
		{
			name:  "falls back to key parse",
			key:   "github.com/acme/json@v1.2.3",
			entry: LockfileEntry{},
			want:  "github.com/acme/json",
		},
		{
			name:  "unparseable key returns key itself",
			key:   "rawkey",
			entry: LockfileEntry{},
			want:  "rawkey",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := entryRepoOf(tt.key, tt.entry); got != tt.want {
				t.Errorf("entryRepoOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalPackageID(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		entry LockfileEntry
		want  string
	}{
		{
			name:  "key already canonical",
			key:   "github.com/acme/json@v1.2.3",
			entry: LockfileEntry{},
			want:  "github.com/acme/json@v1.2.3",
		},
		{
			name:  "constructs from version and ResolvedURL",
			key:   "github.com/acme/json",
			entry: LockfileEntry{Version: "v1.2.3", ResolvedURL: "github.com/acme/json"},
			want:  "github.com/acme/json@v1.2.3",
		},
		{
			name:  "returns empty when no version",
			key:   "github.com/acme/json",
			entry: LockfileEntry{},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalPackageID(tt.key, tt.entry); got != tt.want {
				t.Errorf("canonicalPackageID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUniqueStrings(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"empty", nil, nil},
		{"deduplicates and sorts", []string{"c", "a", "b", "a", ""}, []string{"a", "b", "c"}},
		{"skips empty", []string{"", "", "x"}, []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uniqueStrings(tt.input)
			if !stringSlicesEqual(got, tt.want) {
				t.Errorf("uniqueStrings() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterOut(t *testing.T) {
	changed := false
	got := filterOut([]string{"a", "b", "c"}, "b", &changed)
	if !stringSlicesEqual(got, []string{"a", "c"}) {
		t.Errorf("filterOut() = %v, want [a c]", got)
	}
	if !changed {
		t.Errorf("filterOut() did not flag changed")
	}
}

func TestFilterOutNoMatch(t *testing.T) {
	changed := false
	got := filterOut([]string{"a", "b"}, "z", &changed)
	if !stringSlicesEqual(got, []string{"a", "b"}) {
		t.Errorf("filterOut() = %v, want [a b]", got)
	}
	if changed {
		t.Errorf("filterOut() flagged changed when nothing was removed")
	}
}

func TestMarshalLockfileOrdersMapKeysDeterministically(t *testing.T) {
	lock := NewLockfile()
	lock.DirectDeps = map[string]string{"z": "z@v1", "a": "a@v1"}
	lock.SetDependency("z@v1", LockfileEntry{Version: "v1"})
	lock.SetDependency("a@v1", LockfileEntry{Version: "v1"})

	data, err := marshalLockfile(lock)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	directA := strings.Index(text, `"a": "a@v1"`)
	directZ := strings.Index(text, `"z": "z@v1"`)
	if directA < 0 || directZ < 0 || directA > directZ {
		t.Fatalf("direct dependency keys are not sorted:\n%s", text)
	}
	packageA := strings.Index(text, `"a@v1": {`)
	packageZ := strings.Index(text, `"z@v1": {`)
	if packageA < 0 || packageZ < 0 || packageA > packageZ {
		t.Fatalf("package keys are not sorted:\n%s", text)
	}
}

func TestStringSetFromSlice(t *testing.T) {
	set := stringSetFromSlice([]string{"a", "b", "a"})
	if _, ok := set["a"]; !ok {
		t.Error("expected a in set")
	}
	if _, ok := set["b"]; !ok {
		t.Error("expected b in set")
	}
	if _, ok := set["c"]; ok {
		t.Error("did not expect c in set")
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
