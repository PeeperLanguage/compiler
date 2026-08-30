package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"compiler/pkg/distribution"
)

func main() {
	source := flag.String("source", "", "staged pack root")
	output := flag.String("output", "", "archive output path")
	format := flag.String("format", "", "archive format: tar.gz or zip")
	kind := flag.String("kind", "", "pack kind: compiler or toolchain")
	id := flag.String("id", "", "immutable pack identifier")
	version := flag.String("version", "", "pack version")
	targetOS := flag.String("os", "", "host operating system")
	targetArch := flag.String("arch", "", "host architecture")
	flag.Parse()

	manifest, err := distribution.WritePack(*source, *output, distribution.Format(*format), distribution.Metadata{
		Kind: *kind, ID: *id, Version: *version, OS: *targetOS, Arch: *targetArch,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		fmt.Fprintln(os.Stderr, "encode pack result:", err)
		os.Exit(1)
	}
}
