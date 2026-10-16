// Command peeper is the Peeper compiler.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"compiler/internal/diag"
	"compiler/internal/manifest"
	"compiler/internal/project"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] != "check" || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: peeper check [project folder | file.peep]")
		os.Exit(2)
	}
	path := "."
	if len(args) == 2 {
		path = args[1]
	}
	info, _ := os.Stderr.Stat()
	color := info != nil && info.Mode()&os.ModeCharDevice != 0
	os.Exit(check(path, librariesDir(), os.Stderr, color))
}

// check loads the program at path, prints its diagnostics to w and returns the
// exit code: 1 when there are errors. A folder must hold peeper.toml; its project
// starts at src/main.peep. A file is checked on its own, with no project and no
// imports.
func check(path, libraries string, w io.Writer, color bool) int {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(w, err)
		return 1
	}
	packages := bundledLibraries(libraries)
	entry := path
	if info.IsDir() {
		m, err := manifest.Read(path)
		if err != nil {
			fmt.Fprintf(w, "%s: %v\n", path, err)
			return 1
		}
		if _, taken := packages[m.Name]; taken {
			fmt.Fprintf(w, "%s: project name `%s` is taken by a bundled library\n", filepath.Join(m.Root, manifest.FileName), m.Name)
			return 1
		}
		packages[m.Name] = filepath.Join(m.Root, "src")
		entry = filepath.Join(m.Root, "src", "main.peep")
	}
	prog := project.Load(packages, entry)
	all := prog.Diagnostics
	for _, module := range prog.Order {
		all = append(all, prog.Modules[module].Diagnostics...)
	}
	diag.Print(w, all, color)
	for _, d := range all {
		if d.Code.Severity == diag.Error {
			return 1
		}
	}
	return 0
}

// bundledLibraries maps each library folder in libraries to its src folder.
func bundledLibraries(libraries string) project.Packages {
	packages := project.Packages{}
	entries, _ := os.ReadDir(libraries) // no libraries folder: no bundled libraries
	for _, entry := range entries {
		if entry.IsDir() && manifest.ValidName(entry.Name()) {
			packages[entry.Name()] = filepath.Join(libraries, entry.Name(), "src")
		}
	}
	return packages
}

// librariesDir is the libs folder of the installation peeper runs from: the
// executable is in <root>/bin and the libraries in <root>/libs.
func librariesDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "libs")
}
