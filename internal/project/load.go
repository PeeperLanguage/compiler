// Package project reads the source files of a program in parallel, resolves their
// imports, and orders the modules so each comes after the modules it imports.
// It knows packages only as names mapped to folders; finding them is the
// caller's job.
package project

import (
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"compiler/internal/diag"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/literal"
	"compiler/internal/frontend/parser"
	"compiler/internal/source"
)

// Module is one source file, read and parsed. It never changes once Load has
// returned it, so it may be shared freely.
type Module struct {
	Path        string // import path, such as "myapp:utils/file" or "core:global"
	Source      *source.File
	Syntax      *ast.Tree
	Imports     []Import // in source order; the implicit prelude import last
	Diagnostics []diag.Diagnostic
}

// Import is one resolved import of a module.
type Import struct {
	Path  string      // import path of the imported module
	Alias string      // name it has in the importing file; "" for the implicit prelude import
	Span  source.Span // where the import is written; empty for the implicit prelude import
}

// Program is every module reachable from an entry file.
type Program struct {
	Modules     map[string]*Module
	Order       []string          // each module after the modules it imports; outside cycles, the same order every run
	Diagnostics []diag.Diagnostic // about the program as a whole: import cycles
}

type job struct {
	path, file string
	standalone bool // a file in no package, checked on its own: it cannot import
}

// Load reads entry and every module it imports, directly or not. Files are read
// and parsed by a pool of workers; only this goroutine touches the module map,
// and each module is touched only by its worker until it is handed over. An
// entry in no package's folder is checked on its own and cannot import.
func Load(p Packages, entry string) *Program {
	entry, _ = filepath.Abs(entry)
	_, preludeFile, err := p.resolve(preludePath)
	withPrelude := err == nil && isFile(preludeFile)

	jobs := make(chan job)
	done := make(chan *Module)
	for range runtime.GOMAXPROCS(0) {
		go func() {
			for j := range jobs {
				done <- loadFile(p, j, withPrelude)
			}
		}()
	}

	modules := map[string]*Module{}
	path, inPackage := p.pathOf(entry)
	start := job{path: path, file: entry, standalone: !inPackage}
	seen := map[string]bool{start.path: true}
	queue := []job{start}
	for pending := 1; pending > 0; {
		var send chan job // nil, so the send is not chosen, while the queue is empty
		var next job
		if len(queue) > 0 {
			send, next = jobs, queue[0]
		}
		select {
		case send <- next:
			queue = queue[1:]
		case m := <-done:
			pending--
			modules[m.Path] = m
			for _, imp := range m.Imports {
				if !seen[imp.Path] {
					seen[imp.Path] = true
					_, file, _ := p.resolve(imp.Path)
					queue = append(queue, job{path: imp.Path, file: file})
					pending++
				}
			}
		}
	}
	close(jobs)

	order, cycles := sortModules(modules)
	return &Program{Modules: modules, Order: order, Diagnostics: cycles}
}

func isFile(file string) bool {
	info, err := os.Stat(file)
	return err == nil && info.Mode().IsRegular()
}

// loadFile reads, parses and resolves the imports of one module.
func loadFile(p Packages, j job, withPrelude bool) *Module {
	text, err := os.ReadFile(j.file)
	m := &Module{Path: j.path, Source: source.NewFile(j.file, string(text))}
	m.Syntax, m.Diagnostics = parser.Parse(m.Source)
	if err != nil {
		m.Diagnostics = append(m.Diagnostics, *diag.New(diag.ModuleNotFound, "read module: "+err.Error()))
	}
	m.resolveImports(p, j.standalone, withPrelude)
	return m
}

func (m *Module) resolveImports(p Packages, standalone, withPrelude bool) {
	t := m.Syntax
	aliases := map[string]string{} // alias -> import path
	for _, i := range t.List(t.Nodes[t.Root].A) {
		imp := t.Nodes[i]
		written := literal.Value(t.Text(imp.A))
		fail := func(code diag.Code, message string) {
			m.Diagnostics = append(m.Diagnostics, *diag.New(code, message).Label(m.Source, imp.Span, message))
		}
		if standalone {
			fail(diag.InvalidImportPath, "a single file cannot import; check its project folder instead")
			continue
		}
		path, file, err := p.resolve(written)
		if err != nil {
			fail(diag.InvalidImportPath, err.Error())
			continue
		}
		if !isFile(file) {
			fail(diag.ModuleNotFound, "module not found: "+file)
			continue
		}
		alias := path[strings.LastIndexAny(path, "/:")+1:]
		if imp.B != ast.None {
			alias = t.Text(imp.B)
		}
		if previous, used := aliases[alias]; used {
			if previous != path {
				fail(diag.AliasInUse, "import alias already in use")
			}
			continue
		}
		aliases[alias] = path
		if path == preludePath {
			m.Diagnostics = append(m.Diagnostics, *diag.New(diag.RedundantPreludeImport, "`"+written+"` is imported automatically").
				Label(m.Source, imp.Span, "remove this import").
				Note("global symbols are always in scope without an import"))
		}
		m.Imports = append(m.Imports, Import{Path: path, Alias: alias, Span: imp.Span})
	}
	if withPrelude && m.Path != preludePath && !slices.ContainsFunc(m.Imports, func(i Import) bool { return i.Path == preludePath }) {
		m.Imports = append(m.Imports, Import{Path: preludePath})
	}
}

// sortModules orders the modules so each follows the modules it imports, and
// reports every import cycle. It visits modules and imports in path order, so
// the result does not depend on how the workers were scheduled.
func sortModules(modules map[string]*Module) ([]string, []diag.Diagnostic) {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(modules))
	order := make([]string, 0, len(modules))
	var stack []string
	var cycles []diag.Diagnostic
	var visit func(path string)
	visit = func(path string) {
		state[path] = visiting
		stack = append(stack, path)
		m := modules[path]
		imports := slices.SortedFunc(slices.Values(m.Imports), func(a, b Import) int { return strings.Compare(a.Path, b.Path) })
		for _, imp := range imports {
			switch state[imp.Path] {
			case unvisited:
				visit(imp.Path)
			case visiting:
				cycle := append(slices.Clone(stack[slices.Index(stack, imp.Path):]), imp.Path)
				d := diag.New(diag.CyclicImport, "cyclic import detected: "+strings.Join(cycle, " -> "))
				if imp.Alias == "" {
					d.Note("every module imports `" + preludePath + "` implicitly")
				} else {
					d.Label(m.Source, imp.Span, "this import closes the cycle")
				}
				cycles = append(cycles, *d)
			}
		}
		stack = stack[:len(stack)-1]
		state[path] = visited
		order = append(order, path)
	}
	for _, path := range slices.Sorted(maps.Keys(modules)) {
		if state[path] == unvisited {
			visit(path)
		}
	}
	return order, cycles
}
