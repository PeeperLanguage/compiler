package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"compiler/pkg/distribution"
)

func main() {
	if err := unpack(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func unpack(arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("distunpack", flag.ContinueOnError)
	flags.SetOutput(stderr)
	archive := flags.String("archive", "", "component archive path")
	format := flags.String("format", "", "archive format: tar.gz or zip")
	destination := flags.String("destination", "", "extraction destination")
	kind := flags.String("kind", "", "component kind")
	id := flags.String("id", "", "component identifier")
	version := flags.String("version", "", "component version")
	targetOS := flags.String("os", "", "component operating system")
	targetArch := flags.String("arch", "", "component architecture")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	values := []string{*archive, *format, *destination, *kind, *id, *version, *targetOS, *targetArch}
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("distunpack requires archive, format, destination, kind, id, version, os, and arch")
		}
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("distunpack accepts no positional arguments")
	}
	_, err := distribution.ExtractPack(*archive, distribution.Format(*format), *destination, distribution.Metadata{
		Kind: *kind, ID: *id, Version: *version, OS: *targetOS, Arch: *targetArch,
	})
	if err != nil {
		return err
	}
	return nil
}
