package distribution

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestToolchainSourceLockPinsVerifiableAssets(t *testing.T) {
	data, err := os.ReadFile("../../toolchains/toolchain-sources.lock.json")
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
		if asset.Version == "" || asset.Size <= 0 || asset.License == "" || !strings.HasPrefix(asset.URL, "https://") || !strings.HasPrefix(asset.LicenseURL, "https://") || (asset.SignatureURL != "" && !strings.HasPrefix(asset.SignatureURL, "https://")) {
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

func TestReadToolchainLockRequiresOneValidComponentPerHost(t *testing.T) {
	lock := fixtureToolchainLock()
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadToolchainLock(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("ReadToolchainLock() error = %v", err)
	}
	component, err := parsed.Component("linux", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if component.Version != "llvm23.1.0-musl1.2.5-rfixture" {
		t.Fatalf("component = %#v", component)
	}
}

func TestReadToolchainLockRejectsInvalidRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ToolchainLock)
		want   string
	}{
		{name: "duplicate target", mutate: func(lock *ToolchainLock) {
			lock.Toolchains[1].OS, lock.Toolchains[1].Arch = lock.Toolchains[0].OS, lock.Toolchains[0].Arch
		}, want: "repeats target"},
		{name: "duplicate ID", mutate: func(lock *ToolchainLock) { lock.Toolchains[1].ID = lock.Toolchains[0].ID }, want: "repeats component"},
		{name: "missing target", mutate: func(lock *ToolchainLock) { lock.Toolchains = lock.Toolchains[:5] }, want: "lacks target"},
		{name: "invalid URL", mutate: func(lock *ToolchainLock) { lock.Toolchains[0].URL = "http://example.com/toolchain.tar.gz" }, want: "unsafe URL"},
		{name: "invalid SHA", mutate: func(lock *ToolchainLock) { lock.Toolchains[0].SHA256 = "bad" }, want: "invalid SHA-256"},
		{name: "invalid size", mutate: func(lock *ToolchainLock) { lock.Toolchains[0].Size = 0 }, want: "invalid size"},
		{name: "invalid format", mutate: func(lock *ToolchainLock) { lock.Toolchains[0].Format = "tar" }, want: "unsupported format"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lock := fixtureToolchainLock()
			test.mutate(&lock)
			data, err := json.Marshal(lock)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ReadToolchainLock(strings.NewReader(string(data)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadToolchainLock() error = %v, want %q", err, test.want)
			}
		})
	}
}

func fixtureToolchainLock() ToolchainLock {
	lock := ToolchainLock{SchemaVersion: ToolchainLockVersion, Kind: "peeper-toolchains"}
	for _, host := range supportedReleaseHosts {
		format := FormatTarGz
		if host.os == "windows" {
			format = FormatZip
		}
		id := "toolchain-" + host.os + "-" + host.arch + "-fixture"
		lock.Toolchains = append(lock.Toolchains, ReleaseComponent{
			ID: id, Kind: PackKindToolchain, Version: "llvm23.1.0-musl1.2.5-rfixture",
			OS: host.os, Arch: host.arch, URL: "https://example.com/" + id + format.Extension(),
			Size: 123, SHA256: strings.Repeat("a", 64), Format: format,
		})
	}
	return lock
}
