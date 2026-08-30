package toolchain

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"compiler/internal/target"
)

func TestLoadProfileResolvesInstalledPaths(t *testing.T) {
	root := t.TempDir()
	profilePath := filepath.Join(root, "toolchains", "native", "profile.json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{
		"schema_version": 1,
		"profile_id": "native-linux-amd64",
		"target_os": "linux",
		"target_arch": "amd64",
		"llvm_triple": "x86_64-unknown-linux-musl",
		"clang_path": "toolchains/native/bin/clang",
		"linker_path": "toolchains/native/bin/clang",
		"sysroot": "toolchains/native/sysroot",
		"runtime_archive": "targets/x86_64-unknown-linux-musl/lib/libpeeper_rt_v1.a",
		"runtime_abi": "peeper_rt_v1",
		"link_mode": "static",
		"debug_format": "dwarf"
	}`
	if err := os.WriteFile(profilePath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	compilerTarget := target.Info{OS: "linux", Arch: "amd64", LLVMTriple: "x86_64-unknown-linux-musl", PointerBits: 64, IndexBits: 64}
	profile, err := Load(profilePath, root, compilerTarget)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !profile.Managed {
		t.Fatal("installed profile reported as unmanaged")
	}
	if profile.ClangPath != filepath.Join(root, "toolchains", "native", "bin", "clang") {
		t.Fatalf("clang path = %q", profile.ClangPath)
	}
	if profile.RuntimeArchive != filepath.Join(root, "targets", "x86_64-unknown-linux-musl", "lib", "libpeeper_rt_v1.a") {
		t.Fatalf("runtime archive = %q", profile.RuntimeArchive)
	}
}

func TestLoadProfileRejectsTargetMismatchAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	compilerTarget := target.Info{OS: "linux", Arch: "amd64", LLVMTriple: "x86_64-unknown-linux-musl", PointerBits: 64, IndexBits: 64}
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "target mismatch",
			json: `{"schema_version":1,"profile_id":"wrong","target_os":"linux","target_arch":"arm64","llvm_triple":"aarch64-unknown-linux-musl","clang_path":"bin/clang","linker_path":"bin/clang","link_mode":"static","debug_format":"dwarf"}`,
			want: "does not match compiler target",
		},
		{
			name: "missing target os",
			json: `{"schema_version":1,"profile_id":"missing-os","target_arch":"amd64","llvm_triple":"x86_64-unknown-linux-musl","clang_path":"bin/clang","linker_path":"bin/clang","link_mode":"static","debug_format":"dwarf"}`,
			want: "has no target_os",
		},
		{
			name: "missing target arch",
			json: `{"schema_version":1,"profile_id":"missing-arch","target_os":"linux","llvm_triple":"x86_64-unknown-linux-musl","clang_path":"bin/clang","linker_path":"bin/clang","link_mode":"static","debug_format":"dwarf"}`,
			want: "has no target_arch",
		},
		{
			name: "path escape",
			json: `{"schema_version":1,"profile_id":"escape","target_os":"linux","target_arch":"amd64","llvm_triple":"x86_64-unknown-linux-musl","clang_path":"../clang","linker_path":"bin/clang","link_mode":"static","debug_format":"dwarf"}`,
			want: "escapes installation root",
		},
		{
			name: "unknown field",
			json: `{"schema_version":1,"profile_id":"extra","target_os":"linux","target_arch":"amd64","llvm_triple":"x86_64-unknown-linux-musl","clang_path":"bin/clang","linker_path":"bin/clang","link_mode":"static","debug_format":"dwarf","surprise":true}`,
			want: "unknown field",
		},
		{
			name: "trailing data",
			json: `{"schema_version":1,"profile_id":"first","target_os":"linux","target_arch":"amd64","llvm_triple":"x86_64-unknown-linux-musl","clang_path":"bin/clang","linker_path":"bin/clang","link_mode":"static","debug_format":"dwarf"} {}`,
			want: "trailing JSON data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(tt.json), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path, root, compilerTarget); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestResolvePrefersInstalledProfile(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "peeper"+target.ExecutableExt(runtime.GOOS))
	profilePath := filepath.Join(root, "toolchains", "native", "profile.json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	compilerTarget := target.Host()
	data := `{"schema_version":1,"profile_id":"managed","target_os":"` + compilerTarget.OS + `","target_arch":"` + compilerTarget.Arch + `","llvm_triple":"` + compilerTarget.LLVMTriple + `","clang_path":"toolchains/native/bin/clang","linker_path":"toolchains/native/bin/clang","link_mode":"system","debug_format":"dwarf"}`
	if compilerTarget.OS == "windows" {
		data = strings.Replace(data, `"debug_format":"dwarf"`, `"debug_format":"codeview"`, 1)
	} else if compilerTarget.OS == "darwin" {
		data = strings.Replace(data, `"debug_format":"dwarf"`, `"debug_format":"dwarf","sdk_discovery":"xcrun"`, 1)
	}
	if err := os.WriteFile(profilePath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	profile, err := Resolve(executable, compilerTarget)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !profile.Managed || profile.ProfileID != "managed" {
		t.Fatalf("Resolve() profile = %#v", profile)
	}
}

func TestProfileMacOSArgumentsIncludeSDKAndMinimumVersion(t *testing.T) {
	profile := Profile{TargetOS: "darwin", LLVMTriple: "aarch64-apple-darwin", Sysroot: "/SDK", MinimumOS: "14.0", LinkMode: "system", DebugFormat: "dwarf"}
	wantObject := []string{"-target", "aarch64-apple-darwin", "--sysroot", "/SDK", "-mmacosx-version-min=14.0", "-c", "-x", "ir", "module.ll", "-o", "module.o"}
	if got := profile.ObjectArgs("module.ll", "module.o", false); !reflect.DeepEqual(got, wantObject) {
		t.Fatalf("ObjectArgs() = %#v, want %#v", got, wantObject)
	}
	wantLink := []string{"-target", "aarch64-apple-darwin", "--sysroot", "/SDK", "-mmacosx-version-min=14.0", "@objects.rsp", "-o", "demo"}
	if got := profile.LinkArgs("objects.rsp", "demo"); !reflect.DeepEqual(got, wantLink) {
		t.Fatalf("LinkArgs() = %#v, want %#v", got, wantLink)
	}
}

func TestNewManagedProfileDefinesReleaseLayout(t *testing.T) {
	for _, test := range []struct {
		name         string
		os           string
		arch         string
		minimumOS    string
		clang        string
		sysroot      string
		linkMode     string
		debugFormat  string
		sdkDiscovery string
	}{
		{name: "linux", os: "linux", arch: "amd64", clang: "toolchains/native/bin/clang", sysroot: "toolchains/native/sysroot", linkMode: "static", debugFormat: "dwarf"},
		{name: "windows", os: "windows", arch: "arm64", clang: "toolchains/native/bin/clang.exe", linkMode: "system", debugFormat: "codeview"},
		{name: "darwin", os: "darwin", arch: "arm64", minimumOS: "13.0", clang: "toolchains/native/bin/clang", linkMode: "system", debugFormat: "dwarf", sdkDiscovery: "xcrun"},
	} {
		t.Run(test.name, func(t *testing.T) {
			host, err := target.New(test.os, test.arch)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := NewManagedProfile(host, test.minimumOS)
			if err != nil {
				t.Fatalf("NewManagedProfile() error = %v", err)
			}
			if profile.ClangPath != test.clang || profile.LinkerPath != test.clang || profile.Sysroot != test.sysroot || profile.LinkMode != test.linkMode || profile.DebugFormat != test.debugFormat || profile.SDKDiscovery != test.sdkDiscovery {
				t.Fatalf("NewManagedProfile() = %#v", profile)
			}
			wantRuntime := "targets/" + host.LLVMTriple + "/lib/libpeeper_rt_v1.a"
			if profile.RuntimeArchive != wantRuntime || profile.RuntimeABI != RuntimeABIVersion {
				t.Fatalf("runtime = %q (%q), want %q", profile.RuntimeArchive, profile.RuntimeABI, wantRuntime)
			}
		})
	}
}

func TestNewManagedProfileRejectsUnsupportedReleasePolicy(t *testing.T) {
	linux386, err := target.New("linux", "386")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewManagedProfile(linux386, ""); err == nil || !strings.Contains(err.Error(), "unsupported managed release host") {
		t.Fatalf("NewManagedProfile(linux/386) error = %v", err)
	}
	darwin, err := target.New("darwin", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewManagedProfile(darwin, ""); err == nil || !strings.Contains(err.Error(), "minimum macOS") {
		t.Fatalf("NewManagedProfile(darwin) error = %v", err)
	}
}

func TestResolveDoesNotHideInvalidInstalledProfile(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "peeper"+target.ExecutableExt(runtime.GOOS))
	profilePath := filepath.Join(root, "toolchains", "native", "profile.json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, []byte(`{"schema_version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(executable, target.Host()); err == nil || !strings.Contains(err.Error(), "unsupported toolchain profile schema") {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestResolveUsesBundledRuntimeWithSystemClang(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "peeper"+target.ExecutableExt(runtime.GOOS))
	runtimeArchive := filepath.Join(root, "runtime", "libpeeper_rt_v1.a")
	if err := os.MkdirAll(filepath.Dir(runtimeArchive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeArchive, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	profile, err := Resolve(executable, target.Host())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if profile.RuntimeArchive != runtimeArchive || profile.RuntimeABI != RuntimeABIVersion {
		t.Fatalf("Resolve() runtime = %q (%q)", profile.RuntimeArchive, profile.RuntimeABI)
	}
}

func TestResolveDoesNotClaimMissingBundledRuntime(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "peeper"+target.ExecutableExt(runtime.GOOS))
	profile, err := Resolve(executable, target.Host())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if profile.RuntimeArchive != "" || profile.RuntimeABI != "" {
		t.Fatalf("Resolve() runtime = %q (%q)", profile.RuntimeArchive, profile.RuntimeABI)
	}
}

func TestProfileBuildArguments(t *testing.T) {
	profile := Profile{
		TargetOS:       "windows",
		LLVMTriple:     "x86_64-w64-windows-gnu",
		ClangPath:      "clang",
		LinkerPath:     "clang",
		Sysroot:        "C:/Peeper/toolchain",
		RuntimeArchive: "C:/Peeper/runtime/libpeeper_rt_v1.a",
		DebugFormat:    "codeview",
	}
	objectArgs := profile.ObjectArgs("module.ll", "module.o", true)
	wantObject := []string{"-target", "x86_64-w64-windows-gnu", "--sysroot", "C:/Peeper/toolchain", "-O0", "-gcodeview", "-c", "-x", "ir", "module.ll", "-o", "module.o"}
	if !reflect.DeepEqual(objectArgs, wantObject) {
		t.Fatalf("ObjectArgs() = %#v, want %#v", objectArgs, wantObject)
	}
	linkArgs := profile.LinkArgs("objects.rsp", "demo.exe")
	wantLink := []string{"-target", "x86_64-w64-windows-gnu", "--sysroot", "C:/Peeper/toolchain", "@objects.rsp", "-o", "demo.exe"}
	if !reflect.DeepEqual(linkArgs, wantLink) {
		t.Fatalf("LinkArgs() = %#v, want %#v", linkArgs, wantLink)
	}
}

func TestStaticProfileRequestsStaticLink(t *testing.T) {
	profile := Profile{LLVMTriple: "x86_64-unknown-linux-musl", Sysroot: "toolchains/native/sysroot", LinkMode: "static"}
	want := []string{
		"-target", "x86_64-unknown-linux-musl",
		"--sysroot", "toolchains/native/sysroot",
		"-static", "-nostdlib",
		"toolchains/native/sysroot/lib/crt1.o",
		"toolchains/native/sysroot/lib/crti.o",
		"@objects.rsp",
		"-lc",
		"-lclang_rt.builtins",
		"toolchains/native/sysroot/lib/crtn.o",
		"-o", "demo",
	}
	if got := profile.LinkArgs("objects.rsp", "demo"); !reflect.DeepEqual(got, want) {
		t.Fatalf("LinkArgs() = %#v, want %#v", got, want)
	}
}

func TestWriteResponseFileIncludesRuntimeArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.rsp")
	profile := Profile{RuntimeArchive: filepath.Join("runtime dir", "libpeeper.a")}
	if err := profile.WriteResponseFile(path, []string{"one.o", filepath.Join("object dir", "two.o")}); err != nil {
		t.Fatalf("WriteResponseFile() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "one.o\n\"" + filepath.ToSlash(filepath.Join("object dir", "two.o")) + "\"\n\"" + filepath.ToSlash(filepath.Join("runtime dir", "libpeeper.a")) + "\"\n"
	if string(data) != want {
		t.Fatalf("response file = %q, want %q", data, want)
	}
}
