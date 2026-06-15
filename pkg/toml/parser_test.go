package toml

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFileSupportsInlineTablesAndArrays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peeper")
	src := `
[package]
name = "app"

[dependencies]
json = { type = "remote", repo = "github.com/acme/json", version = "v1.2.0" }
flags = [true, 1, "x"]
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := ParseFile(path)
	if err != nil {
		t.Fatalf("parse file: %v", err)
	}
	deps := data.Sections["dependencies"]
	jsonTable, ok := deps["json"].(Table)
	if !ok {
		t.Fatalf("expected inline table, got %#v", deps["json"])
	}
	if jsonTable["repo"] != "github.com/acme/json" {
		t.Fatalf("unexpected repo: %#v", jsonTable["repo"])
	}
	flags, ok := deps["flags"].(Array)
	if !ok || len(flags) != 3 {
		t.Fatalf("unexpected array: %#v", deps["flags"])
	}
}

func TestParseFileRejectsDuplicateKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peeper")
	src := `
[package]
name = "app"
name = "other"
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ParseFile(path); err == nil {
		t.Fatal("expected duplicate key error")
	}
}

func TestLookupAndDecodeProvideTypedAccess(t *testing.T) {
	src := `
title = "peeper"

[package]
name = "app"
version = "1.2.3"

[dependencies]
flags = [true, 1, "x"]
json = { type = "remote", repo = "github.com/acme/json", version = "v1.2.0" }
`

	data, err := ParseString(src)
	if err != nil {
		t.Fatalf("parse string: %v", err)
	}

	title, ok, err := Lookup[string](data, "default", "title")
	if err != nil || !ok {
		t.Fatalf("lookup title: ok=%v err=%v", ok, err)
	}
	if title != "peeper" {
		t.Fatalf("unexpected title: %q", title)
	}

	flags, ok, err := Lookup[[]any](data, "dependencies", "flags")
	if err != nil || !ok {
		t.Fatalf("lookup flags: ok=%v err=%v", ok, err)
	}
	if len(flags) != 3 || flags[1] != 1 {
		t.Fatalf("unexpected flags: %#v", flags)
	}

	type dependency struct {
		Type    string `toml:"type"`
		Repo    string `toml:"repo"`
		Version string `toml:"version"`
	}

	dep, ok, err := Lookup[dependency](data, "dependencies", "json")
	if err != nil || !ok {
		t.Fatalf("lookup dependency: ok=%v err=%v", ok, err)
	}
	if dep.Repo != "github.com/acme/json" || dep.Type != "remote" {
		t.Fatalf("unexpected dependency: %#v", dep)
	}
}

func TestDataDecodeMapsSectionsIntoStruct(t *testing.T) {
	src := `
title = "peeper"

[package]
name = "app"
version = "1.2.3"

[dev]
mock_remote = true
mock_path = "./local"
`

	data, err := ParseString(src)
	if err != nil {
		t.Fatalf("parse string: %v", err)
	}

	var cfg struct {
		Default struct {
			Title string `toml:"title"`
		} `toml:",default"`
		Package struct {
			Name    string `toml:"name"`
			Version string `toml:"version"`
		} `toml:"package"`
		Dev struct {
			MockRemote bool   `toml:"mock_remote"`
			MockPath   string `toml:"mock_path"`
		} `toml:"dev"`
	}

	if err := data.Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Default.Title != "peeper" {
		t.Fatalf("unexpected default section decode: %#v", cfg.Default)
	}
	if cfg.Package.Name != "app" || cfg.Package.Version != "1.2.3" {
		t.Fatalf("unexpected package decode: %#v", cfg.Package)
	}
	if !cfg.Dev.MockRemote || cfg.Dev.MockPath != "./local" {
		t.Fatalf("unexpected dev decode: %#v", cfg.Dev)
	}
}
