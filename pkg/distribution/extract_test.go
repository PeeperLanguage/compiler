package distribution

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtractPackVerifiesAndRestoresInventory(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "peeper"), []byte("compiler"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Kind: PackKindCompiler, ID: "compiler-linux-amd64", Version: "0.2.0", OS: "linux", Arch: "amd64"}
	for _, format := range []Format{FormatTarGz, FormatZip} {
		t.Run(string(format), func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "pack"+format.Extension())
			if _, err := WritePack(source, archivePath, format, metadata); err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			manifest, err := ExtractPack(archivePath, format, destination, metadata)
			if err != nil {
				t.Fatalf("ExtractPack() error = %v", err)
			}
			data, err := os.ReadFile(filepath.Join(destination, "bin", "peeper"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "compiler" || len(manifest.Files) != 2 {
				t.Fatalf("extracted data = %q, manifest = %#v", data, manifest)
			}
		})
	}
}

func TestExtractPackRejectsMetadataMismatch(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Kind: PackKindCompiler, ID: "compiler-linux-amd64", Version: "0.2.0", OS: "linux", Arch: "amd64"}
	archivePath := filepath.Join(t.TempDir(), "pack.tar.gz")
	if _, err := WritePack(source, archivePath, FormatTarGz, metadata); err != nil {
		t.Fatal(err)
	}
	metadata.Arch = "arm64"
	if _, err := ExtractPack(archivePath, FormatTarGz, t.TempDir(), metadata); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("ExtractPack() error = %v", err)
	}
}

func TestExtractPackRejectsTraversalBeforeWriting(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "bad.tar.gz")
	manifest := Manifest{
		SchemaVersion: PackManifestVersion,
		Metadata:      Metadata{Kind: PackKindCompiler, ID: "bad", Version: "1", OS: "linux", Arch: "amd64"},
		Format:        FormatTarGz,
		Files:         []FileRecord{{Path: "../escape", Type: FileTypeRegular, Mode: 0o644, Size: 1, SHA256: strings.Repeat("0", 64)}},
	}
	writeTestTarPack(t, archivePath, manifest, map[string]string{"../escape": "x"})
	destination := t.TempDir()
	if _, err := ExtractPack(archivePath, FormatTarGz, destination, manifest.Metadata); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("ExtractPack() error = %v", err)
	}
	if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
		t.Fatalf("destination changed: entries=%v err=%v", entries, err)
	}
}

func TestExtractPackRejectsSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional Windows privileges")
	}
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "peeper"), []byte("compiler"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{Kind: PackKindCompiler, ID: "compiler-linux-amd64", Version: "0.2.0", OS: "linux", Arch: "amd64"}
	archivePath := filepath.Join(t.TempDir(), "pack.tar.gz")
	if _, err := WritePack(source, archivePath, FormatTarGz, metadata); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(destination, "bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractPack(archivePath, FormatTarGz, destination, metadata); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ExtractPack() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "peeper")); !os.IsNotExist(err) {
		t.Fatalf("archive wrote through symlink: %v", err)
	}
}

func writeTestTarPack(t *testing.T, archivePath string, manifest Manifest, files map[string]string) {
	t.Helper()
	output, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: ManifestName, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
