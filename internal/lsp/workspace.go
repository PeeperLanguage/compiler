package lsp

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/graph"
	"compiler/internal/project"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

type workspaceModule struct {
	filePath          string
	importPath        string
	projectName       string
	rootDir           string
	contentHash       string
	importFingerprint string
	exportFingerprint string
	importTargets     []string
}

type workspaceComponent struct {
	files []string
	roots []string
}

type workspaceIndex struct {
	rootDir     string
	modules     map[string]*workspaceModule
	components  []workspaceComponent
	imports     *graph.Graph
	parsedFiles int
}

func newWorkspaceIndex(rootDir string) *workspaceIndex {
	return &workspaceIndex{
		rootDir: project.CanonicalPath(rootDir),
		modules: make(map[string]*workspaceModule),
	}
}

func (w *workspaceIndex) rebuild(cache map[string]string) error {
	if w == nil || w.rootDir == "" {
		return nil
	}

	files, err := workspaceFiles(w.rootDir, cache)
	if err != nil {
		return err
	}

	type workspaceFileContext struct {
		rootDir     string
		projectName string
		importPath  string
	}

	fileSet := make(map[string]struct{}, len(files))
	contexts := make(map[string]workspaceFileContext, len(files))
	w.parsedFiles = 0
	for _, filePath := range files {
		rootDir := filepath.Dir(filePath)
		projectName := ""
		if loadedProject, err := manifest.LoadProject(filePath); err == nil {
			if !manifest.PathWithinSourceDir(loadedProject.RootDir, filePath) {
				delete(w.modules, filePath)
				continue
			}
			rootDir = loadedProject.RootDir
			projectName = loadedProject.File.Package.Name
		}
		ctx := project.NewWithConfig(project.Config{
			RootDir:     rootDir,
			ProjectName: projectName,
			Extension:   peeper.SourceExt,
		}, diagnostics.NewDiagnosticBag())
		importPath, err := ctx.ImportPathForFile(project.ModuleOriginLocal, "", filePath)
		if err != nil {
			importPath = ""
		}
		fileSet[filePath] = struct{}{}
		contexts[filePath] = workspaceFileContext{
			rootDir:     rootDir,
			projectName: projectName,
			importPath:  importPath,
		}
	}
	fileMembershipChanged := len(w.modules) != len(fileSet)
	if !fileMembershipChanged {
		for filePath := range w.modules {
			if _, ok := fileSet[filePath]; !ok {
				fileMembershipChanged = true
				break
			}
		}
	}

	for _, filePath := range files {
		fileCtx, ok := contexts[filePath]
		if !ok {
			continue
		}
		content, err := workspaceContent(filePath, cache)
		if err != nil {
			continue
		}
		contentHash := ast.HashText(content)

		module := w.modules[filePath]
		if module == nil {
			module = &workspaceModule{filePath: filePath}
			w.modules[filePath] = module
		}

		contextChanged := module.rootDir != fileCtx.rootDir ||
			module.projectName != fileCtx.projectName ||
			module.importPath != fileCtx.importPath
		if !fileMembershipChanged && !contextChanged && module.contentHash == contentHash {
			continue
		}

		module.rootDir = fileCtx.rootDir
		module.projectName = fileCtx.projectName
		module.importPath = fileCtx.importPath
		module.contentHash = contentHash
		diag := diagnostics.NewDiagnosticBag()
		parsed := parser.New(filePath, lexer.New(filePath, content, diag).Tokenize(), diag).ParseModule()
		module.exportFingerprint = parsed.ExportFingerprint
		module.importFingerprint = parsed.ImportFingerprint
		module.importTargets = module.importTargets[:0]
		ctx := project.NewWithConfig(project.Config{
			RootDir:     module.rootDir,
			ProjectName: module.projectName,
			Extension:   peeper.SourceExt,
		}, diagnostics.NewDiagnosticBag())
		from := &project.Module{
			FilePath:   filePath,
			ImportPath: module.importPath,
			Origin:     project.ModuleOriginLocal,
		}
		seen := make(map[string]struct{})
		for _, imp := range parsed.Imports {
			rawPath, ok := ast.ImportPathFromDecl(imp)
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
			module.importTargets = append(module.importTargets, target)
		}
		w.parsedFiles++
	}

	for filePath := range w.modules {
		if _, ok := fileSet[filePath]; ok {
			continue
		}
		delete(w.modules, filePath)
	}

	g := graph.New(project.GraphNodeModule, project.GraphEdgeImport)
	for filePath := range w.modules {
		g.AddNode(graph.NodeID(filePath))
	}
	for _, module := range w.modules {
		for _, target := range module.importTargets {
			if _, ok := w.modules[target]; !ok {
				continue
			}
			g.AddEdge(graph.NodeID(module.filePath), graph.NodeID(target))
		}
	}

	w.components = buildWorkspaceComponents(w.modules, g)
	w.imports = g
	return nil
}

func (w *workspaceIndex) syntheticEntry(filePath string) (string, string, bool) {
	if w == nil || len(w.components) == 0 || !w.hasDiskBackedFiles() {
		return "", "", false
	}
	component, ok := w.componentForFile(filePath)
	if !ok || len(component.roots) == 0 {
		return "", "", false
	}
	roots := append([]string(nil), component.roots...)
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

	virtualPath := filepath.Join(w.rootDir, ".peeper-lsp", "__workspace__"+peeper.SourceExt)
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
	component, ok := w.componentForFile(filePath)
	if ok {
		for _, member := range component.files {
			out[member] = struct{}{}
		}
		return out
	}
	out[filePath] = struct{}{}
	return out
}

func (w *workspaceIndex) componentForFile(filePath string) (workspaceComponent, bool) {
	if w == nil {
		return workspaceComponent{}, false
	}
	filePath = project.CanonicalPath(filePath)
	if filePath == "" {
		return workspaceComponent{}, false
	}
	for _, component := range w.components {
		if slices.Contains(component.files, filePath) {
			return component, true
		}
	}
	return workspaceComponent{}, false
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

func (w *workspaceIndex) reusePhases(filePath string, cached map[string]*project.Module) map[string]project.ModulePhase {
	phases := make(map[string]project.ModulePhase)
	if w == nil || len(cached) == 0 {
		return phases
	}

	// This policy stays separate from seedReusableModules: workspace owns the
	// "what is still reusable after this edit?" decision, while handlers only
	// clone/reset and seed whichever phase this function authorizes.
	component := w.componentFiles(filePath)
	propagate := make([]string, 0)
	for cachedPath, cachedModule := range cached {
		if cachedModule == nil || cachedModule.FilePath == "" {
			continue
		}
		current := w.modules[cachedPath]
		if current == nil {
			continue
		}
		if _, inComponent := component[cachedPath]; !inComponent {
			continue
		}
		// Byte-identical files can keep whatever completed phase they already had.
		if cachedModule.ContentHash == current.contentHash {
			phases[cachedPath] = cachedModule.Phase
			continue
		}
		// Import/export surface changes force dependents back to parse-only reuse.
		// Body-only edits stay local to changed modules and do not downgrade
		// importers inside same component.
		if cachedModule.ImportFingerprint != current.importFingerprint || cachedModule.ExportFingerprint != current.exportFingerprint {
			propagate = append(propagate, cachedPath)
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
			cachedModule := cached[dependent]
			currentModule := w.modules[dependent]
			if cachedModule == nil || currentModule == nil {
				continue
			}
			if cachedModule.ContentHash != currentModule.contentHash {
				continue
			}
			// Dependents with unchanged text can skip reparsing, but they must
			// rerun semantic/lowering phases because upstream module surface moved.
			phases[dependent] = project.PhaseParsed
			propagate = append(propagate, dependent)
		}
	}

	return phases
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

	rawComponents := g.WeaklyConnectedComponents(nodeIDs)
	components := make([]workspaceComponent, 0, len(rawComponents))
	for _, raw := range rawComponents {
		component := workspaceComponent{}
		for _, nodeID := range raw {
			component.files = append(component.files, string(nodeID))
		}
		sort.Strings(component.files)
		for _, filePath := range component.files {
			if g.InDegree(graph.NodeID(filePath)) == 0 {
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
	if w == nil || w.imports == nil {
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
		for _, dependent := range w.imports.Predecessors(graph.NodeID(current)) {
			file := string(dependent)
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			out[file] = struct{}{}
			queue = append(queue, file)
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
				if name == ".git" || name == "build" || name == "_builtin_library" || strings.HasPrefix(name, ".tmp") {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != peeper.SourceExt {
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
		if filepath.Ext(path) != peeper.SourceExt {
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
