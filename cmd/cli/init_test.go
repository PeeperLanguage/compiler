package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func TestInitCommandRejectsInvalidNamesWithoutArtifacts(t *testing.T) {
	for _, name := range []string{"1app", "bad/name", "_app"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			chdirForTest(t, root)
			if err := InitCommand([]string{name}); err == nil {
				t.Fatalf("InitCommand(%q) succeeded", name)
			}
			for _, path := range []string{manifest.FileName, peeper.SourceDirName} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("invalid name created %s: %v", path, err)
				}
			}
		})
	}
}

func TestInitCommandPreflightsPathConflictsBeforeWriting(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T)
	}{
		{
			name: "src is regular file",
			setup: func(t *testing.T) {
				if err := os.WriteFile(peeper.SourceDirName, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "main is directory",
			setup: func(t *testing.T) {
				if err := os.MkdirAll(filepath.Join(peeper.SourceDirName, peeper.MainFileName), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "main is symlink",
			setup: func(t *testing.T) {
				if err := os.Mkdir(peeper.SourceDirName, 0o755); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(peeper.SourceDirName, "existing"+peeper.SourceExt)
				if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(target), filepath.Join(peeper.SourceDirName, peeper.MainFileName)); err != nil {
					t.Skipf("create symlink: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			chdirForTest(t, root)
			test.setup(t)
			if err := InitCommand([]string{"app"}); err == nil {
				t.Fatal("InitCommand succeeded")
			}
			if _, err := os.Lstat(manifest.FileName); !os.IsNotExist(err) {
				t.Fatalf("path conflict left manifest: %v", err)
			}
		})
	}
}

func TestInitCommandPreservesExistingRegularMain(t *testing.T) {
	root := t.TempDir()
	chdirForTest(t, root)
	if err := os.Mkdir(peeper.SourceDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(peeper.SourceDirName, peeper.MainFileName)
	original := []byte("fn main() {\n\tprintln(\"custom\");\n}\n")
	if err := os.WriteFile(mainPath, original, 0o640); err != nil {
		t.Fatal(err)
	}

	if err := InitCommand([]string{"app"}); err != nil {
		t.Fatalf("InitCommand: %v", err)
	}
	after, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("existing main changed:\n%s", after)
	}
	info, err := os.Stat(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("existing main mode = %o, want 640", info.Mode().Perm())
	}
}

func TestInitCommandNormalizesWhitespace(t *testing.T) {
	root := t.TempDir()
	chdirForTest(t, root)
	if err := InitCommand([]string{"Hello Peeper"}); err != nil {
		t.Fatalf("InitCommand: %v", err)
	}
	file, err := manifest.Load(manifest.FileName)
	if err != nil {
		t.Fatalf("load generated manifest: %v", err)
	}
	if file.Package.Name != "hello_peeper" {
		t.Fatalf("package name = %q, want hello_peeper", file.Package.Name)
	}
}
