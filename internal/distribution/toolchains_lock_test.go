package distribution

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestToolchainSourceLockPinsVerifiableAssets(t *testing.T) {
	data, err := os.ReadFile("../../distribution/toolchain-sources.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Assets        []struct {
			ID           string `json:"id"`
			Version      string `json:"version"`
			URL          string `json:"url"`
			Size         int64  `json:"size"`
			SHA256       string `json:"sha256"`
			SignatureURL string `json:"signature_url"`
			License      string `json:"license"`
			LicenseURL   string `json:"license_url"`
		} `json:"assets"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		t.Fatalf("decode toolchain lock: %v", err)
	}
	if lock.SchemaVersion != 1 {
		t.Fatalf("schema version = %d", lock.SchemaVersion)
	}
	if lock.Kind != "upstream-sources" {
		t.Fatalf("lock kind = %q", lock.Kind)
	}
	seen := make(map[string]bool)
	for _, asset := range lock.Assets {
		if asset.ID == "" || seen[asset.ID] {
			t.Fatalf("invalid or duplicate asset id %q", asset.ID)
		}
		seen[asset.ID] = true
		if asset.Version == "" || asset.Size <= 0 || asset.License == "" || !strings.HasPrefix(asset.URL, "https://") {
			t.Fatalf("incomplete asset %#v", asset)
		}
		digest, err := hex.DecodeString(asset.SHA256)
		if err != nil || len(digest) != 32 {
			t.Fatalf("asset %q has invalid SHA-256 %q", asset.ID, asset.SHA256)
		}
	}
	for _, required := range []string{"llvm-source", "llvm-linux-amd64", "llvm-linux-arm64", "musl-source", "llvm-mingw-windows-amd64", "llvm-mingw-windows-arm64"} {
		if !seen[required] {
			t.Fatalf("toolchain lock lacks %q", required)
		}
	}
}
