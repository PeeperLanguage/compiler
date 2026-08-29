package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"compiler/internal/target"
	"compiler/internal/toolchain"
)

func TestInspectInstallationReportsManagedToolchain(t *testing.T) {
	root := t.TempDir()
	host := target.Host()
	executable := filepath.Join(root, "bin", "peeper"+target.ExecutableExt(host.OS))
	for _, directory := range []string{filepath.Dir(executable), filepath.Join(root, "libs", "core"), filepath.Join(root, "toolchains", "native", "bin"), filepath.Join(root, "runtime")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	clang := filepath.Join("toolchains", "native", "bin", "clang"+target.ExecutableExt(host.OS))
	for _, path := range []string{executable, filepath.Join(root, clang), filepath.Join(root, "runtime", "libpeeper_rt_v1.a")} {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	debugFormat := "dwarf"
	if runtime.GOOS == "windows" {
		debugFormat = "codeview"
	}
	profile := map[string]any{
		"schema_version": toolchain.ProfileSchemaVersion, "profile_id": "managed-host", "target_os": host.OS, "target_arch": host.Arch, "llvm_triple": host.LLVMTriple,
		"clang_path": clang, "linker_path": clang, "runtime_archive": "runtime/libpeeper_rt_v1.a", "runtime_abi": toolchain.RuntimeABIVersion,
		"link_mode": "system", "debug_format": debugFormat,
	}
	if host.OS == "darwin" {
		profile["sdk_discovery"] = "xcrun"
		profile["minimum_os"] = "14.0"
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(root, "toolchains", "native", "profile.json")
	if err := os.WriteFile(profilePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	report := inspectInstallation(executable, host)
	if !report.OK || !report.ManagedToolchain || report.ProfileID != "managed-host" {
		t.Fatalf("inspectInstallation() = %#v", report)
	}
}

func TestInspectInstallationReportsMissingCore(t *testing.T) {
	root := t.TempDir()
	report := inspectInstallation(filepath.Join(root, "bin", "peeper"), target.Host())
	if report.OK || report.Error != "core library is missing" {
		t.Fatalf("inspectInstallation() = %#v", report)
	}
}
