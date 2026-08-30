package distribution

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWritePackIsDeterministic(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "peeper"), []byte("compiler"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "LICENSE"), []byte("license\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	metadata := Metadata{Kind: "compiler", ID: "compiler-linux-amd64", Version: "0.1.0", OS: "linux", Arch: "amd64"}
	for _, format := range []Format{FormatTarGz, FormatZip} {
		t.Run(string(format), func(t *testing.T) {
			first := filepath.Join(t.TempDir(), "first"+format.Extension())
			second := filepath.Join(t.TempDir(), "second"+format.Extension())
			firstManifest, err := WritePack(source, first, format, metadata)
			if err != nil {
				t.Fatalf("WritePack() first error = %v", err)
			}
			secondManifest, err := WritePack(source, second, format, metadata)
			if err != nil {
				t.Fatalf("WritePack() second error = %v", err)
			}
			firstBytes, err := os.ReadFile(first)
			if err != nil {
				t.Fatal(err)
			}
			secondBytes, err := os.ReadFile(second)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstBytes, secondBytes) {
				t.Fatal("same source produced different archive bytes")
			}
			if firstManifest.SHA256 != secondManifest.SHA256 || firstManifest.Size != secondManifest.Size {
				t.Fatalf("manifest archive identity differs: %#v != %#v", firstManifest, secondManifest)
			}
			if len(firstManifest.Files) != 3 || firstManifest.Files[0].Path != "LICENSE" || firstManifest.Files[1].Path != "bin" || firstManifest.Files[2].Path != "bin/peeper" {
				t.Fatalf("manifest files = %#v", firstManifest.Files)
			}
		})
	}
}

func TestWritePackPreservesContainedRelativeSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional Windows privileges")
	}
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "clang-23"), []byte("clang"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("clang-23", filepath.Join(source, "bin", "clang")); err != nil {
		t.Fatal(err)
	}

	manifest, err := WritePack(source, filepath.Join(t.TempDir(), "toolchain.tar.gz"), FormatTarGz, Metadata{Kind: "toolchain", ID: "llvm-linux-amd64", Version: "23.1.0", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("WritePack() error = %v", err)
	}
	if len(manifest.Files) != 3 || manifest.Files[1].Type != FileTypeSymlink || manifest.Files[1].LinkTarget != "clang-23" {
		t.Fatalf("manifest files = %#v", manifest.Files)
	}
}

func TestWritePackRejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional Windows privileges")
	}
	source := t.TempDir()
	if err := os.Symlink("../outside", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	_, err := WritePack(source, filepath.Join(t.TempDir(), "bad.tar.gz"), FormatTarGz, Metadata{Kind: "toolchain", ID: "bad", Version: "1", OS: "linux", Arch: "amd64"})
	if err == nil {
		t.Fatal("WritePack() accepted escaping symlink")
	}
}

func TestWritePackRejectsReservedManifestPath(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, ManifestName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := WritePack(source, filepath.Join(t.TempDir(), "bad.zip"), FormatZip, Metadata{Kind: "compiler", ID: "bad", Version: "1", OS: "windows", Arch: "amd64"})
	if err == nil {
		t.Fatal("WritePack() accepted source containing reserved manifest path")
	}
}
