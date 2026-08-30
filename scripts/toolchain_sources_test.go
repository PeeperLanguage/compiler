package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateToolchainSourcesUsesVerifiedStableAssets(t *testing.T) {
	temporary := t.TempDir()
	llvmRelease := filepath.Join(temporary, "llvm.json")
	mingwRelease := filepath.Join(temporary, "mingw.json")
	muslArchive := filepath.Join(temporary, "musl.tar.gz")
	output := filepath.Join(temporary, "sources.lock.json")

	writeReleaseFixture(t, llvmRelease, "llvmorg-24.1.0", []releaseAssetFixture{
		{name: "llvm-project-24.1.0.src.tar.xz", size: 101, digest: strings.Repeat("a", 64)},
		{name: "llvm-project-24.1.0.src.tar.xz.sig", size: 1, digest: strings.Repeat("b", 64)},
		{name: "LLVM-24.1.0-Linux-X64.tar.xz", size: 102, digest: strings.Repeat("c", 64)},
		{name: "LLVM-24.1.0-Linux-X64.tar.xz.sig", size: 1, digest: strings.Repeat("d", 64)},
		{name: "LLVM-24.1.0-Linux-ARM64.tar.xz", size: 103, digest: strings.Repeat("e", 64)},
		{name: "LLVM-24.1.0-Linux-ARM64.tar.xz.sig", size: 1, digest: strings.Repeat("f", 64)},
	})
	writeReleaseFixture(t, mingwRelease, "20270102", []releaseAssetFixture{
		{name: "llvm-mingw-20270102-ucrt-x86_64.zip", size: 201, digest: strings.Repeat("1", 64)},
		{name: "llvm-mingw-20270102-ucrt-aarch64.zip", size: 202, digest: strings.Repeat("2", 64)},
	})
	if err := os.WriteFile(muslArchive, []byte("musl fixture"), 0o644); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("bash", "update-toolchain-sources.sh", "../toolchains/toolchain-sources.lock.json", output)
	command.Env = append(os.Environ(),
		"LLVM_RELEASE_JSON="+llvmRelease,
		"LLVM_MINGW_RELEASE_JSON="+mingwRelease,
		"MUSL_VERSION=1.2.6",
		"MUSL_ARCHIVE="+muslArchive,
	)
	if outputBytes, err := command.CombinedOutput(); err != nil {
		t.Fatalf("update source lock: %v\n%s", err, outputBytes)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Assets []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
			Size    int64  `json:"size"`
			SHA256  string `json:"sha256"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	versions := make(map[string]string, len(lock.Assets))
	for _, asset := range lock.Assets {
		versions[asset.ID] = asset.Version
	}
	for id, version := range map[string]string{
		"llvm-source": "24.1.0", "llvm-linux-amd64": "24.1.0", "llvm-linux-arm64": "24.1.0",
		"musl-source": "1.2.6", "llvm-mingw-windows-amd64": "20270102", "llvm-mingw-windows-arm64": "20270102",
	} {
		if versions[id] != version {
			t.Fatalf("asset %s version = %q, want %q", id, versions[id], version)
		}
	}
}

func TestUpdateToolchainSourcesRejectsIncompleteRelease(t *testing.T) {
	temporary := t.TempDir()
	llvmRelease := filepath.Join(temporary, "llvm.json")
	writeReleaseFixture(t, llvmRelease, "llvmorg-24.1.0", nil)
	command := exec.Command("bash", "update-toolchain-sources.sh", "../toolchains/toolchain-sources.lock.json", filepath.Join(temporary, "output.json"))
	command.Env = append(os.Environ(), "LLVM_RELEASE_JSON="+llvmRelease)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "release requires exactly one asset") {
		t.Fatalf("update output = %q, error = %v", output, err)
	}
}

func TestUpdateToolchainLockReadsSeparateArtifactDirectories(t *testing.T) {
	temporary := t.TempDir()
	lock := filepath.Join(temporary, "toolchains.lock.json")
	data, err := os.ReadFile("../toolchains/toolchains.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var current struct {
		Toolchains []json.RawMessage `json:"toolchains"`
	}
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatal(err)
	}
	records := filepath.Join(temporary, "records")
	for _, record := range current.Toolchains {
		var target struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
		}
		if err := json.Unmarshal(record, &target); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(records, "toolchain-record-"+target.OS+"-"+target.Arch, "toolchain-record.json"), string(record))
	}

	command := exec.Command("bash", "scripts/update-toolchain-lock.sh", lock, records)
	command.Dir = ".."
	command.Env = append(os.Environ(),
		"SELECTED_linux_amd64=true", "RESULT_linux_amd64=success",
		"SELECTED_linux_arm64=true", "RESULT_linux_arm64=success",
		"SELECTED_darwin_amd64=true", "RESULT_darwin_amd64=success",
		"SELECTED_darwin_arm64=true", "RESULT_darwin_arm64=success",
		"SELECTED_windows_amd64=true", "RESULT_windows_amd64=success",
		"SELECTED_windows_arm64=true", "RESULT_windows_arm64=success",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("update toolchain lock: %v\n%s", err, output)
	}
}

type releaseAssetFixture struct {
	name   string
	size   int64
	digest string
}

func writeReleaseFixture(t *testing.T, path, tag string, assets []releaseAssetFixture) {
	t.Helper()
	payload := struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name               string `json:"name"`
			Size               int64  `json:"size"`
			Digest             string `json:"digest"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}{TagName: tag, Assets: make([]struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}, 0, len(assets))}
	repository := "mstorsjo/llvm-mingw"
	if strings.HasPrefix(tag, "llvmorg-") {
		repository = "llvm/llvm-project"
	}
	for _, asset := range assets {
		payload.Assets = append(payload.Assets, struct {
			Name               string `json:"name"`
			Size               int64  `json:"size"`
			Digest             string `json:"digest"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			Name: asset.name, Size: asset.size, Digest: "sha256:" + asset.digest,
			BrowserDownloadURL: "https://github.com/" + repository + "/releases/download/" + tag + "/" + asset.name,
		})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}
