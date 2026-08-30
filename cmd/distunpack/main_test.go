package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"compiler/pkg/distribution"
)

func TestUnpackUsesExpectedComponentMetadata(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	metadata := distribution.Metadata{Kind: distribution.PackKindToolchain, ID: "toolchain-linux-amd64-r1", Version: "r1", OS: "linux", Arch: "amd64"}
	archive := filepath.Join(t.TempDir(), "toolchain.tar.gz")
	if _, err := distribution.WritePack(source, archive, distribution.FormatTarGz, metadata); err != nil {
		t.Fatal(err)
	}
	arguments := []string{
		"-archive", archive,
		"-format", string(distribution.FormatTarGz),
		"-destination", t.TempDir(),
		"-kind", metadata.Kind,
		"-id", metadata.ID,
		"-version", metadata.Version,
		"-os", metadata.OS,
		"-arch", metadata.Arch,
	}
	if err := unpack(arguments, io.Discard); err != nil {
		t.Fatalf("unpack() error = %v", err)
	}
	arguments[len(arguments)-1] = "arm64"
	if err := unpack(arguments, io.Discard); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("unpack() error = %v", err)
	}
}
