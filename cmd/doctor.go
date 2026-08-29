package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"compiler/internal/target"
	"compiler/internal/toolchain"
	"compiler/pkg/peeper"
)

type doctorReport struct {
	OK               bool   `json:"ok"`
	CompilerVersion  string `json:"compiler_version"`
	HostOS           string `json:"host_os"`
	HostArch         string `json:"host_arch"`
	LLVMTriple       string `json:"llvm_triple"`
	InstallationRoot string `json:"installation_root"`
	CoreLibrary      string `json:"core_library"`
	ProfileID        string `json:"profile_id,omitempty"`
	ManagedToolchain bool   `json:"managed_toolchain"`
	ClangPath        string `json:"clang_path,omitempty"`
	LinkerPath       string `json:"linker_path,omitempty"`
	RuntimeArchive   string `json:"runtime_archive,omitempty"`
	RuntimeABI       string `json:"runtime_abi,omitempty"`
	Error            string `json:"error,omitempty"`
}

func doctorCommand(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emit JSON report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("doctor accepts flags only")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve compiler executable: %w", err)
	}
	report := inspectInstallation(executable, target.Host())
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("encode doctor report: %w", err)
		}
	} else {
		fmt.Printf("Peeper %s %s/%s\n", report.CompilerVersion, report.HostOS, report.HostArch)
		fmt.Printf("Installation: %s\n", report.InstallationRoot)
		fmt.Printf("Toolchain: %s\n", report.ProfileID)
		if report.Error != "" {
			fmt.Printf("Status: failed: %s\n", report.Error)
		} else {
			fmt.Println("Status: ready")
		}
	}
	if !report.OK {
		return programExitStatus(exitCodeError)
	}
	return nil
}

func inspectInstallation(executable string, host target.Info) doctorReport {
	root := peeper.InstallationRootForExecutable(executable)
	report := doctorReport{
		CompilerVersion: peeper.CompilerVersion,
		HostOS:          host.OS, HostArch: host.Arch, LLVMTriple: host.LLVMTriple,
		InstallationRoot: root, CoreLibrary: filepath.Join(root, "libs", "core"),
	}
	if info, err := os.Stat(report.CoreLibrary); err != nil || !info.IsDir() {
		report.Error = "core library is missing"
		return report
	}
	profile, err := toolchain.Resolve(executable, host)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.ProfileID = profile.ProfileID
	report.ManagedToolchain = profile.Managed
	report.ClangPath = profile.ClangPath
	report.LinkerPath = profile.LinkerPath
	report.RuntimeArchive = profile.RuntimeArchive
	report.RuntimeABI = profile.RuntimeABI
	for _, required := range [][2]string{{"compiler", profile.ClangPath}, {"linker", profile.LinkerPath}} {
		if info, err := os.Stat(required[1]); err != nil || !info.Mode().IsRegular() {
			report.Error = required[0] + " is missing"
			return report
		}
	}
	if profile.Managed {
		if profile.RuntimeABI != toolchain.RuntimeABIVersion || profile.RuntimeArchive == "" {
			report.Error = "managed runtime is missing or incompatible"
			return report
		}
		if info, err := os.Stat(profile.RuntimeArchive); err != nil || !info.Mode().IsRegular() {
			report.Error = "runtime archive is missing"
			return report
		}
	}
	report.OK = true
	return report
}
