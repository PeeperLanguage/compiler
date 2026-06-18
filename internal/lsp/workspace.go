package lsp

import (
	"compiler/internal/diagnostics"
	driver "compiler/internal/driver"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/graph"
	"compiler/internal/project"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type workspaceModule struct {
	filePath          string
	importPath        string
	contentHash       string
	importFingerprint string
	exportFingerprint string
	localDeps         []string
}

type workspaceComponent struct {
	files []string
	roots []string
}

type workspaceIndex struct {
	rootDir    string
	modules    map[string]*workspaceModule
	components []workspaceComponent
}

func newWorkspaceIndex(rootDir string) *workspaceIndex {
	return &workspaceIndex{rootDir: project.CanonicalPath(rootDir)}
}

func (w *workspaceIndex) rebuild(cache map[string]string) error {
	if w == nil || w.rootDir == "" {
		return nil
	}

	ctx := project.New(w.rootDir, driver.SOURCE_EXT, diagnostics.NewDiagnosticBag())
	files, err := workspaceFiles(w.rootDir, cache)
	if err != nil {
		return err
	}

	fileSet := make(map[string]struct{}, len(files))
	modules := make(map[string]*workspaceModule, len(files))
	g := graph.New()
	for _, filePath := range files {
		fileSet[filePath] = struct{}{}
		importPath, err := ctx.ImportPathForFile(project.ModuleOriginLocal, "", filePath)
		if err != nil {
			continue
		}
		modules[filePath] = &workspaceModule{
			filePath:   filePath,
			importPath: importPath,
		}
		g.AddNode(graph.NodeID(filePath), graph.Node{Kind: graph.NodeModule})
	}

	for _, module := range modules {
		content, err := workspaceContent(module.filePath, cache)
		if err != nil {
			continue
		}
		module.contentHash = project.HashText(content)
		diag := diagnostics.NewDiagnosticBag()
		parsed := parser.New(module.filePath, lexer.New(module.filePath, content, diag).Tokenize(), diag).ParseModule()
		module.exportFingerprint = parsed.ExportFingerprint
		module.importFingerprint = parsed.ImportFingerprint
		from := &project.Module{
			FilePath:   module.filePath,
			ImportPath: module.importPath,
			Origin:     project.ModuleOriginLocal,
		}
		seen := make(map[string]struct{})
		for _, imp := range parsed.Imports {
			rawPath, ok := project.ImportPathFromDecl(imp)
			if !ok {
				continue
			}
			resolved, err := ctx.ResolveImportPath(from, rawPath)
			if err != nil || resolved == nil || resolved.Origin != project.ModuleOriginLocal {
				continue
			}
			target := project.CanonicalPath(resolved.FilePath)
			if _, ok := fileSet[target]; !ok {
				continue
			}
			if _, dup := seen[target]; dup {
				continue
			}
			seen[target] = struct{}{}
			module.localDeps = append(module.localDeps, target)
			g.AddEdge(graph.NodeID(module.filePath), graph.NodeID(target), graph.EdgeImport)
		}
		sort.Strings(module.localDeps)
	}

	w.modules = modules
	w.components = buildWorkspaceComponents(modules, g)
	return nil
}

func (w *workspaceIndex) syntheticEntry() (string, string, bool) {
	if w == nil || len(w.components) == 0 || !w.hasDiskBackedFiles() {
		return "", "", false
	}

	var roots []string
	for _, component := range w.components {
		roots = append(roots, component.roots...)
	}
	if len(roots) == 0 {
		return "", "", false
	}
	sort.Strings(roots)

	var builder strings.Builder
	for i, root := range roots {
		module := w.modules[root]
		if module == nil || module.importPath == "" {
			continue
		}
		fmt.Fprintf(&builder, "import %q as ws%d;\n", module.importPath, i)
	}
	builder.WriteString("fn WorkspaceEntry() {}\n")

	virtualPath := filepath.Join(w.rootDir, ".peeper-lsp", "__workspace__.peep")
	return virtualPath, builder.String(), true
}

func (w *workspaceIndex) componentFiles(filePath string) map[string]struct{} {
	out := make(map[string]struct{})
	if w == nil {
		return out
	}
	filePath = project.CanonicalPath(filePath)
	if filePath == "" {
		return out
	}
	for _, component := range w.components {
		for _, candidate := range component.files {
			if candidate != filePath {
				continue
			}
			for _, member := range component.files {
				out[member] = struct{}{}
			}
			return out
		}
	}
	out[filePath] = struct{}{}
	return out
}

func (w *workspaceIndex) dirtyFiles(filePath string, cached map[string]*project.Module) map[string]struct{} {
	dirty := make(map[string]struct{})
	if w == nil {
		return dirty
	}
	component := w.componentFiles(filePath)
	propagate := make([]string, 0)
	for member := range component {
		current := w.modules[member]
		if current == nil {
			continue
		}
		cachedModule := cached[member]
		if cachedModule == nil {
			dirty[member] = struct{}{}
			propagate = append(propagate, member)
			continue
		}
		if cachedModule.ContentHash == current.contentHash {
			continue
		}
		dirty[member] = struct{}{}
		if cachedModule.ImportFingerprint != current.importFingerprint || cachedModule.ExportFingerprint != current.exportFingerprint {
			propagate = append(propagate, member)
		}
	}
	seen := make(map[string]struct{}, len(propagate))
	for len(propagate) > 0 {
		current := propagate[0]
		propagate = propagate[1:]
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		for dependent := range w.reverseDependents(current) {
			if _, ok := component[dependent]; !ok {
				continue
			}
			if _, ok := dirty[dependent]; ok {
				continue
			}
			dirty[dependent] = struct{}{}
			propagate = append(propagate, dependent)
		}
	}
	if len(dirty) == 0 {
		filePath = project.CanonicalPath(filePath)
		if filePath != "" {
			dirty[filePath] = struct{}{}
		}
	}
	return dirty
}

func (w *workspaceIndex) hasDiskBackedFiles() bool {
	if w == nil || len(w.modules) == 0 {
		return false
	}
	for filePath := range w.modules {
		if _, err := os.Stat(filePath); err != nil {
			return false
		}
	}
	return true
}

func buildWorkspaceComponents(modules map[string]*workspaceModule, g *graph.Graph) []workspaceComponent {
	if len(modules) == 0 {
		return nil
	}

	files := make([]string, 0, len(modules))
	for filePath := range modules {
		files = append(files, filePath)
	}
	sort.Strings(files)

	nodeIDs := make([]graph.NodeID, 0, len(files))
	for _, filePath := range files {
		nodeIDs = append(nodeIDs, graph.NodeID(filePath))
	}

	rawComponents := g.WeaklyConnectedComponents(nodeIDs, graph.EdgeImport)
	components := make([]workspaceComponent, 0, len(rawComponents))
	for _, raw := range rawComponents {
		component := workspaceComponent{}
		for _, nodeID := range raw {
			component.files = append(component.files, string(nodeID))
		}
		sort.Strings(component.files)
		for _, filePath := range component.files {
			if g.InDegree(graph.NodeID(filePath), graph.EdgeImport) == 0 {
				component.roots = append(component.roots, filePath)
			}
		}
		if len(component.roots) == 0 && len(component.files) > 0 {
			component.roots = append(component.roots, component.files[0])
		}
		sort.Strings(component.roots)
		components = append(components, component)
	}

	return components
}

func (w *workspaceIndex) reverseDependents(filePath string) map[string]struct{} {
	out := make(map[string]struct{})
	if w == nil {
		return out
	}
	filePath = project.CanonicalPath(filePath)
	if filePath == "" {
		return out
	}
	queue := []string{filePath}
	seen := map[string]struct{}{filePath: {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, module := range w.modules {
			if module == nil {
				continue
			}
			found := false
			for _, dep := range module.localDeps {
				if dep == current {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			if _, ok := seen[module.filePath]; ok {
				continue
			}
			seen[module.filePath] = struct{}{}
			out[module.filePath] = struct{}{}
			queue = append(queue, module.filePath)
		}
	}
	return out
}

func workspaceFiles(rootDir string, cache map[string]string) ([]string, error) {
	fileSet := make(map[string]struct{})
	if rootDir != "" {
		err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "build" || strings.HasPrefix(name, ".tmp") {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != driver.SOURCE_EXT {
				return nil
			}
			fileSet[project.CanonicalPath(path)] = struct{}{}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for path := range cache {
		if filepath.Ext(path) != driver.SOURCE_EXT {
			continue
		}
		canonical := project.CanonicalPath(path)
		if rootDir != "" && !project.PathWithinRoot(rootDir, canonical) {
			continue
		}
		fileSet[canonical] = struct{}{}
	}

	files := make([]string, 0, len(fileSet))
	for path := range fileSet {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func workspaceContent(filePath string, cache map[string]string) (string, error) {
	if content, ok := cache[filePath]; ok {
		return content, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
