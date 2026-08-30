package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"compiler/pkg/distribution"
)

func main() {
	lockPath := flag.String("lock", "pkg/distribution/toolchains.lock.json", "finished toolchain lock file")
	targetOS := flag.String("os", "", "toolchain operating system")
	targetArch := flag.String("arch", "", "toolchain architecture")
	flag.Parse()
	if flag.NArg() != 0 || *targetOS == "" || *targetArch == "" {
		fmt.Fprintln(os.Stderr, "toolchain-lock requires -os and -arch")
		os.Exit(2)
	}
	file, err := os.Open(*lockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open toolchain lock:", err)
		os.Exit(1)
	}
	lock, err := distribution.ReadToolchainLock(file)
	closeErr := file.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if closeErr != nil {
		fmt.Fprintln(os.Stderr, "close toolchain lock:", closeErr)
		os.Exit(1)
	}
	component, err := lock.Component(*targetOS, *targetArch)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(component); err != nil {
		fmt.Fprintln(os.Stderr, "encode toolchain component:", err)
		os.Exit(1)
	}
}
