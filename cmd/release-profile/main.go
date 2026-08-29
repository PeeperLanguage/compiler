package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"compiler/internal/target"
	"compiler/internal/toolchain"
)

func main() {
	root := flag.String("root", "", "staged installation root")
	targetOS := flag.String("os", "", "release host operating system")
	targetArch := flag.String("arch", "", "release host architecture")
	minimumOS := flag.String("minimum-os", "", "minimum supported macOS version")
	flag.Parse()
	if flag.NArg() != 0 || *root == "" {
		fmt.Fprintln(os.Stderr, "release-profile requires -root, -os, and -arch")
		os.Exit(2)
	}
	host, err := target.New(*targetOS, *targetArch)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	profile, err := toolchain.NewManagedProfile(host, *minimumOS)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	rootPath, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve staged installation root:", err)
		os.Exit(1)
	}
	profileDirectory := filepath.Join(rootPath, "toolchains", "native")
	if err := os.MkdirAll(profileDirectory, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "create profile directory:", err)
		os.Exit(1)
	}
	temporary, err := os.CreateTemp(profileDirectory, ".profile.json.tmp-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create temporary profile:", err)
		os.Exit(1)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(profile); err != nil {
		_ = temporary.Close()
		fmt.Fprintln(os.Stderr, "encode managed profile:", err)
		os.Exit(1)
	}
	if err := temporary.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "close managed profile:", err)
		os.Exit(1)
	}
	if _, err := toolchain.Load(temporaryPath, rootPath, host); err != nil {
		fmt.Fprintln(os.Stderr, "validate managed profile:", err)
		os.Exit(1)
	}
	profilePath := filepath.Join(profileDirectory, "profile.json")
	if err := os.Rename(temporaryPath, profilePath); err != nil {
		fmt.Fprintln(os.Stderr, "publish managed profile:", err)
		os.Exit(1)
	}
}
