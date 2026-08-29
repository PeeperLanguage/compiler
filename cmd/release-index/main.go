package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"compiler/internal/distribution"
)

func main() {
	version := flag.String("version", "", "release version")
	baseURL := flag.String("base-url", "", "HTTPS release artifact base URL")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "release index requires pack result files")
		os.Exit(2)
	}
	artifacts := make([]distribution.ReleaseArtifact, 0, flag.NArg())
	for _, resultPath := range flag.Args() {
		if !strings.HasSuffix(resultPath, ".json") {
			fmt.Fprintf(os.Stderr, "pack result %q must end in .json\n", resultPath)
			os.Exit(1)
		}
		file, err := os.Open(resultPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open pack result %q: %v\n", resultPath, err)
			os.Exit(1)
		}
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		var manifest distribution.Manifest
		if err := decoder.Decode(&manifest); err != nil {
			_ = file.Close()
			fmt.Fprintf(os.Stderr, "decode pack result %q: %v\n", resultPath, err)
			os.Exit(1)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			_ = file.Close()
			fmt.Fprintf(os.Stderr, "decode pack result %q: trailing JSON data\n", resultPath)
			os.Exit(1)
		}
		if err := file.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "close pack result %q: %v\n", resultPath, err)
			os.Exit(1)
		}
		artifacts = append(artifacts, distribution.ReleaseArtifact{
			FileName: strings.TrimSuffix(filepath.Base(resultPath), ".json"),
			Manifest: manifest,
		})
	}
	manifest, err := distribution.BuildReleaseManifest(*version, *baseURL, artifacts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		fmt.Fprintln(os.Stderr, "encode release manifest:", err)
		os.Exit(1)
	}
}
