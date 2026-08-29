package toolchain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"compiler/internal/target"
	"compiler/pkg/peeper"
)

const ProfileSchemaVersion = 1

const RuntimeABIVersion = "peeper_rt_v1"

// Profile is the complete native compile/link contract selected before backend output is consumed.
type Profile struct {
	SchemaVersion  int    `json:"schema_version"`
	ProfileID      string `json:"profile_id"`
	TargetOS       string `json:"target_os"`
	TargetArch     string `json:"target_arch"`
	LLVMTriple     string `json:"llvm_triple"`
	ClangPath      string `json:"clang_path"`
	LinkerPath     string `json:"linker_path"`
	Sysroot        string `json:"sysroot,omitempty"`
	RuntimeArchive string `json:"runtime_archive,omitempty"`
	RuntimeABI     string `json:"runtime_abi,omitempty"`
	LinkMode       string `json:"link_mode"`
	DebugFormat    string `json:"debug_format"`
	MinimumOS      string `json:"minimum_os,omitempty"`
	SDKDiscovery   string `json:"sdk_discovery,omitempty"`
	Managed        bool   `json:"-"`
}

func NewManagedProfile(compilerTarget target.Info, minimumOS string) (Profile, error) {
	if !compilerTarget.Valid() || (compilerTarget.Arch != "amd64" && compilerTarget.Arch != "arm64") {
		return Profile{}, fmt.Errorf("unsupported managed release host %s/%s", compilerTarget.OS, compilerTarget.Arch)
	}
	minimumOS = strings.TrimSpace(minimumOS)
	clangPath := path.Join("toolchains", "native", "bin", "clang"+target.ExecutableExt(compilerTarget.OS))
	profile := Profile{
		SchemaVersion: ProfileSchemaVersion,
		ProfileID:     "managed-" + compilerTarget.LLVMTriple,
		TargetOS:      compilerTarget.OS,
		TargetArch:    compilerTarget.Arch,
		LLVMTriple:    compilerTarget.LLVMTriple,
		ClangPath:     clangPath,
		LinkerPath:    clangPath,
		RuntimeArchive: path.Join(
			"targets", compilerTarget.LLVMTriple, "lib", "libpeeper_rt_v1.a",
		),
		RuntimeABI:  RuntimeABIVersion,
		LinkMode:    "system",
		DebugFormat: "dwarf",
	}
	switch compilerTarget.OS {
	case "linux":
		if minimumOS != "" {
			return Profile{}, fmt.Errorf("minimum OS is only valid for managed macOS profiles")
		}
		profile.Sysroot = path.Join("toolchains", "native", "sysroot")
		profile.LinkMode = "static"
	case "windows":
		if minimumOS != "" {
			return Profile{}, fmt.Errorf("minimum OS is only valid for managed macOS profiles")
		}
		profile.DebugFormat = "codeview"
	case "darwin":
		if !validMinimumOS(minimumOS) {
			return Profile{}, fmt.Errorf("managed macOS profile requires a valid minimum macOS version")
		}
		profile.MinimumOS = minimumOS
		profile.SDKDiscovery = "xcrun"
	default:
		return Profile{}, fmt.Errorf("unsupported managed release host %s/%s", compilerTarget.OS, compilerTarget.Arch)
	}
	return profile, nil
}

func Load(profilePath, installationRoot string, compilerTarget target.Info) (Profile, error) {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return Profile{}, fmt.Errorf("read toolchain profile: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("decode toolchain profile: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Profile{}, fmt.Errorf("decode toolchain profile: trailing JSON data")
	}
	profile.ProfileID = strings.TrimSpace(profile.ProfileID)
	if profile.SchemaVersion != ProfileSchemaVersion {
		return Profile{}, fmt.Errorf("unsupported toolchain profile schema %d", profile.SchemaVersion)
	}
	if profile.ProfileID == "" {
		return Profile{}, fmt.Errorf("toolchain profile has no profile_id")
	}
	if strings.TrimSpace(profile.TargetOS) == "" {
		return Profile{}, fmt.Errorf("toolchain profile %q has no target_os", profile.ProfileID)
	}
	if strings.TrimSpace(profile.TargetArch) == "" {
		return Profile{}, fmt.Errorf("toolchain profile %q has no target_arch", profile.ProfileID)
	}
	profile.TargetOS = target.NormalizeOS(profile.TargetOS)
	profile.TargetArch = target.NormalizeArch(profile.TargetArch)
	profile.LLVMTriple = strings.TrimSpace(profile.LLVMTriple)
	profile.RuntimeABI = strings.TrimSpace(profile.RuntimeABI)
	profile.LinkMode = strings.TrimSpace(profile.LinkMode)
	profile.DebugFormat = strings.TrimSpace(profile.DebugFormat)
	profile.MinimumOS = strings.TrimSpace(profile.MinimumOS)
	profile.SDKDiscovery = strings.TrimSpace(profile.SDKDiscovery)
	if profile.TargetOS != compilerTarget.OS || profile.TargetArch != compilerTarget.Arch || profile.LLVMTriple != compilerTarget.LLVMTriple {
		return Profile{}, fmt.Errorf("toolchain profile %q target %s/%s (%s) does not match compiler target %s/%s (%s)", profile.ProfileID, profile.TargetOS, profile.TargetArch, profile.LLVMTriple, compilerTarget.OS, compilerTarget.Arch, compilerTarget.LLVMTriple)
	}
	if profile.LinkMode != "static" && profile.LinkMode != "system" {
		return Profile{}, fmt.Errorf("toolchain profile %q has invalid link_mode %q", profile.ProfileID, profile.LinkMode)
	}
	if profile.DebugFormat != "dwarf" && profile.DebugFormat != "codeview" {
		return Profile{}, fmt.Errorf("toolchain profile %q has invalid debug_format %q", profile.ProfileID, profile.DebugFormat)
	}
	if profile.MinimumOS != "" && !validMinimumOS(profile.MinimumOS) {
		return Profile{}, fmt.Errorf("toolchain profile %q has invalid minimum_os %q", profile.ProfileID, profile.MinimumOS)
	}
	if profile.SDKDiscovery != "" && (profile.TargetOS != "darwin" || profile.SDKDiscovery != "xcrun") {
		return Profile{}, fmt.Errorf("toolchain profile %q has invalid sdk_discovery %q", profile.ProfileID, profile.SDKDiscovery)
	}
	if profile.SDKDiscovery != "" && strings.TrimSpace(profile.Sysroot) != "" {
		return Profile{}, fmt.Errorf("toolchain profile %q cannot set both sysroot and sdk_discovery", profile.ProfileID)
	}
	if profile.TargetOS == "darwin" && profile.SDKDiscovery == "" {
		return Profile{}, fmt.Errorf("toolchain profile %q must discover Apple SDK with xcrun", profile.ProfileID)
	}
	profile.ClangPath, err = resolveInstalledPath(installationRoot, profile.ClangPath, "clang_path", true)
	if err != nil {
		return Profile{}, err
	}
	profile.LinkerPath, err = resolveInstalledPath(installationRoot, profile.LinkerPath, "linker_path", true)
	if err != nil {
		return Profile{}, err
	}
	profile.Sysroot, err = resolveInstalledPath(installationRoot, profile.Sysroot, "sysroot", false)
	if err != nil {
		return Profile{}, err
	}
	if profile.SDKDiscovery == "xcrun" {
		profile.Sysroot, err = discoverAppleSDK()
		if err != nil {
			return Profile{}, err
		}
	}
	profile.RuntimeArchive, err = resolveInstalledPath(installationRoot, profile.RuntimeArchive, "runtime_archive", false)
	if err != nil {
		return Profile{}, err
	}
	profile.Managed = true
	return profile, nil
}

func Resolve(executablePath string, compilerTarget target.Info) (Profile, error) {
	installationRoot := peeper.InstallationRootForExecutable(executablePath)
	if installationRoot == "" {
		return Profile{}, fmt.Errorf("resolve toolchain: compiler executable path is unavailable")
	}
	profilePath := filepath.Join(installationRoot, "toolchains", "native", "profile.json")
	if _, err := os.Stat(profilePath); err == nil {
		return Load(profilePath, installationRoot, compilerTarget)
	} else if !os.IsNotExist(err) {
		return Profile{}, fmt.Errorf("inspect installed toolchain profile: %w", err)
	}
	clangPath, err := exec.LookPath("clang")
	if err != nil {
		return Profile{}, fmt.Errorf("no managed toolchain profile and clang not found in PATH: %w", err)
	}
	debugFormat := "dwarf"
	if compilerTarget.OS == "windows" {
		debugFormat = "codeview"
	}
	sysroot := ""
	if compilerTarget.OS == "darwin" {
		sysroot, err = discoverAppleSDK()
		if err != nil {
			return Profile{}, err
		}
	}
	systemTriple, err := target.SystemLLVMTriple(compilerTarget.OS, compilerTarget.Arch)
	if err != nil {
		return Profile{}, fmt.Errorf("resolve system Clang target: %w", err)
	}
	runtimeArchive := filepath.Join(installationRoot, "runtime", "libpeeper_rt_v1.a")
	runtimeABI := RuntimeABIVersion
	if _, err := os.Stat(runtimeArchive); err != nil {
		runtimeArchive = ""
		runtimeABI = ""
	}
	return Profile{
		SchemaVersion:  ProfileSchemaVersion,
		ProfileID:      "system-clang",
		TargetOS:       compilerTarget.OS,
		TargetArch:     compilerTarget.Arch,
		LLVMTriple:     systemTriple,
		ClangPath:      clangPath,
		LinkerPath:     clangPath,
		RuntimeArchive: runtimeArchive,
		RuntimeABI:     runtimeABI,
		Sysroot:        sysroot,
		LinkMode:       "system",
		DebugFormat:    debugFormat,
	}, nil
}

func (profile Profile) ObjectArgs(inputPath, outputPath string, debug bool) []string {
	args := []string{"-target", profile.LLVMTriple}
	if profile.Sysroot != "" {
		args = append(args, "--sysroot", profile.Sysroot)
	}
	if profile.TargetOS == "darwin" && profile.MinimumOS != "" {
		args = append(args, "-mmacosx-version-min="+profile.MinimumOS)
	}
	if debug {
		args = append(args, "-O0")
		if profile.DebugFormat == "codeview" {
			args = append(args, "-gcodeview")
		} else {
			args = append(args, "-g")
		}
	}
	return append(args, "-c", "-x", "ir", inputPath, "-o", outputPath)
}

func (profile Profile) LinkArgs(responsePath, outputPath string) []string {
	args := []string{"-target", profile.LLVMTriple}
	if profile.Sysroot != "" {
		args = append(args, "--sysroot", profile.Sysroot)
	}
	if profile.TargetOS == "darwin" && profile.MinimumOS != "" {
		args = append(args, "-mmacosx-version-min="+profile.MinimumOS)
	}
	if profile.LinkMode == "static" {
		// The managed musl sysroot has no GCC installation, so clang's default
		// GNU-style static link searches for crtbeginT.o/-lgcc/-lgcc_eh that
		// don't exist here. Bypass that entirely with -nostdlib and supply
		// musl's own CRT objects and libc directly.
		args = append(args, "-static", "-nostdlib",
			path.Join(profile.Sysroot, "lib", "crt1.o"),
			path.Join(profile.Sysroot, "lib", "crti.o"),
		)
	}
	args = append(args, "@"+responsePath)
	if profile.LinkMode == "static" {
		args = append(args, "-lc", "-lclang_rt.builtins", path.Join(profile.Sysroot, "lib", "crtn.o"))
	}
	return append(args, "-o", outputPath)
}

func discoverAppleSDK() (string, error) {
	output, err := exec.Command("xcrun", "--sdk", "macosx", "--show-sdk-path").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("discover macOS SDK with xcrun: %w: %s", err, strings.TrimSpace(string(output)))
	}
	sdkPath := strings.TrimSpace(string(output))
	info, err := os.Stat(sdkPath)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("xcrun returned unavailable macOS SDK %q", sdkPath)
	}
	return sdkPath, nil
}

func validMinimumOS(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func (profile Profile) WriteResponseFile(path string, objectPaths []string) error {
	entries := make([]string, 0, len(objectPaths)+1)
	entries = append(entries, objectPaths...)
	if profile.RuntimeArchive != "" {
		entries = append(entries, profile.RuntimeArchive)
	}
	var response strings.Builder
	for _, entry := range entries {
		argument, err := responseArgument(entry)
		if err != nil {
			return err
		}
		response.WriteString(argument)
		response.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(response.String()), 0o600); err != nil {
		return fmt.Errorf("write linker response file: %w", err)
	}
	return nil
}

func resolveInstalledPath(root, value, field string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("toolchain profile has no %s", field)
		}
		return "", nil
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("toolchain profile %s must be relative to installation root", field)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve installation root: %w", err)
	}
	resolved := filepath.Join(root, filepath.Clean(value))
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", fmt.Errorf("resolve toolchain profile %s: %w", field, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("toolchain profile %s escapes installation root", field)
	}
	return resolved, nil
}

func responseArgument(value string) (string, error) {
	value = filepath.ToSlash(value)
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("response file path contains a line break")
	}
	if strings.ContainsAny(value, " \t\"") {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`, nil
	}
	return value, nil
}
