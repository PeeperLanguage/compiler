// Command peeper checks Peeper source files.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/phase"
	"compiler/pkg/peeper"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "usage: peeper check <path ...>")
		os.Exit(2)
	}
	files, err := sourceFiles(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	diag := diagnostics.NewDiagnosticBag()
	check(files, diag)
	diag.EmitAll(diagnostics.PresentationOptions{})
	if diag.HasErrors() {
		os.Exit(1)
	}
}

// check parses every file on its own goroutine. Each goroutine writes only its
// file's diagnostics group, created here in file order so the output order does
// not depend on scheduling.
func check(files []string, diag *diagnostics.DiagnosticBag) {
	var wg sync.WaitGroup
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			diag.AppendPhase(phase.Load, file).Add(diagnostics.NewError(err.Error()))
			continue
		}
		diag.AddSourceContent(file, string(content))
		fileDiag := diag.BeginPhase(phase.Parsed, file)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens := lexer.New(file, string(content), fileDiag).Tokenize()
			parser.New(file, tokens, fileDiag).ParseModule()
		}()
	}
	wg.Wait()
}

// sourceFiles expands each path: a file stays as given, a folder yields its
// source files in lexical order.
func sourceFiles(paths []string) ([]string, error) {
	var files []string
	for _, path := range paths {
		err := filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if file == path && !entry.IsDir() || !entry.IsDir() && filepath.Ext(file) == peeper.SourceExt {
				files = append(files, file)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}
