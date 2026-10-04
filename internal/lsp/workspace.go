package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/fingerprint"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/graph"
	"compiler/internal/module"
	"compiler/internal/phase"
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
	diskSize          int64
	diskModTime       int64
	diskStampValid    bool
	importFingerprint string
	exportFingerprint string
	// Source import paths survive unresolved imports, so membership changes can
	// be re-resolved without reparsing unchanged files.
	sourceImportPaths []string
	// Resolved file paths currently inside this workspace; these form graph edges.
	resolvedLocalImportFiles []string
}

type workspaceComponent struct {
	files []string
	roots []string
}

type workspaceParse struct {
	content     string
	syntax      *ast.Module
	diagnostics []*diagnostics.Diagnostic
}

type workspaceIndex struct {
	rootDir     string
	modules     map[string]*workspaceModule
	components  []workspaceComponent
	imports     *graph.DependencyGraph
	parsedFiles int
}

func newWorkspaceIndex(rootDir string) *workspaceIndex {
	return &workspaceIndex{
		rootDir: project.CanonicalPath(rootDir),
		modules: make(map[string]*workspaceModule),
	}
}

func (w *workspaceIndex) rebuild(sourceOverrides map[string]string) (map[string]workspaceParse, error) {
	if w == nil || w.rootDir == "" {
		return nil, nil
	}

	files, err := workspaceFiles(w.rootDir, sourceOverrides)
	if err != nil {
		return nil, err
	}

	type workspaceFileContext struct {
		rootDir     string
		projectName string
		importPath  string
	}
	contexts := make(map[string]workspaceFileContext, len(files))
	projectContexts := make(map[[2]string]*project.CompilerContext)
	contextForProject := func(rootDir, projectName string) *project.CompilerContext {
		key := [2]string{rootDir, projectName}
		if ctx := projectContexts[key]; ctx != nil {
			return ctx
		}
		ctx := project.NewWithConfig(project.Config{
			RootDir:     rootDir,
			ProjectName: projectName,
			Extension:   peeper.SourceExt,
		}, diagnostics.NewDiagnosticBag())
		projectContexts[key] = ctx
		return ctx
	}
	projectsByDir := make(map[string]*manifest.Project)
	projectsByManifest := make(map[string]*manifest.Project)
	w.parsedFiles = 0
	parsedModules := make(map[string]workspaceParse)
	for _, filePath := range files {
		fileDir := filepath.Dir(filePath)
		rootDir := fileDir
		projectName := ""
		loadedProject, checked := projectsByDir[fileDir]
		if !checked {
			if manifestPath, err := manifest.FindManifestPath(filePath); err == nil {
				manifestKey := project.CanonicalPath(manifestPath)
				loadedProject, checked = projectsByManifest[manifestKey]
				if !checked {
					if configuredProject, loadErr := manifest.LoadProjectFromManifest(manifestPath); loadErr == nil {
						loadedProject = configuredProject
					}
					projectsByManifest[manifestKey] = loadedProject
				}
			}
			projectsByDir[fileDir] = loadedProject
		}
		if loadedProject != nil {
			if !manifest.IsPathWithinSourceDir(loadedProject.RootDir, filePath) {
				continue
			}
			rootDir = loadedProject.RootDir
			projectName = loadedProject.File.Package.Name
		}
		importPath := ""
		previous := w.modules[filePath]
		if previous != nil && previous.rootDir == rootDir && previous.projectName == projectName {
			importPath = previous.importPath
		} else {
			ctx := contextForProject(rootDir, projectName)
			if resolved, resolveErr := ctx.ImportPathForFile(project.ModuleOriginLocal, "", filePath); resolveErr == nil {
				importPath = resolved
			}
		}
		contexts[filePath] = workspaceFileContext{
			rootDir:     rootDir,
			projectName: projectName,
			importPath:  importPath,
		}
	}
	fileMembershipChanged := len(w.modules) != len(contexts)
	if !fileMembershipChanged {
		for filePath := range w.modules {
			if _, ok := contexts[filePath]; !ok {
				fileMembershipChanged = true
				break
			}
		}
	}
	// Membership changes invalidate the graph even when replacement content cannot be read.
	workspaceChanged := fileMembershipChanged

	for _, filePath := range files {
		fileCtx, ok := contexts[filePath]
		if !ok {
			continue
		}
		module := w.modules[filePath]
		if module == nil {
			module = &workspaceModule{filePath: filePath}
			w.modules[filePath] = module
		}

		contextChanged := module.rootDir != fileCtx.rootDir ||
			module.projectName != fileCtx.projectName ||
			module.importPath != fileCtx.importPath
		content := ""
		contentHash := module.contentHash
		contentUnchanged := false
		var diskInfo os.FileInfo
		if _, hasSourceOverride := sourceOverrides[filePath]; !hasSourceOverride {
			if info, statErr := os.Stat(filePath); statErr == nil {
				diskInfo = info
				if !contextChanged && module.diskStampValid &&
					module.diskSize == info.Size() &&
					module.diskModTime == info.ModTime().UnixNano() {
					contentUnchanged = true
				}
			}
		}
		if !contentUnchanged {
			var err error
			content, err = workspaceContent(filePath, sourceOverrides)
			if err != nil {
				continue
			}
			contentHash = fingerprint.Text(content)
		}

		parseChanged := contextChanged || !contentUnchanged && module.contentHash != contentHash
		if diskInfo != nil {
			module.diskSize = diskInfo.Size()
			module.diskModTime = diskInfo.ModTime().UnixNano()
			module.diskStampValid = true
		} else {
			module.diskStampValid = false
		}
		if parseChanged {
			workspaceChanged = true
			module.rootDir = fileCtx.rootDir
			module.projectName = fileCtx.projectName
			module.importPath = fileCtx.importPath
			module.contentHash = contentHash
			diag := diagnostics.NewDiagnosticBag()
			parsed := parser.New(filePath, lexer.New(filePath, content, diag).Tokenize(), diag).ParseModule()
			parsedModules[filePath] = workspaceParse{content: content, syntax: parsed, diagnostics: diag.Diagnostics()}
			module.exportFingerprint = parsed.ExportFingerprint
			module.importFingerprint = parsed.ImportFingerprint
			module.sourceImportPaths = module.sourceImportPaths[:0]
			for _, imp := range parsed.Imports {
				if rawPath, ok := ast.ImportPathFromDecl(imp); ok {
					module.sourceImportPaths = append(module.sourceImportPaths, rawPath)
				}
			}
			w.parsedFiles++
		}
		if !fileMembershipChanged && !parseChanged {
			continue
		}

		module.resolvedLocalImportFiles = module.resolvedLocalImportFiles[:0]
		if len(module.sourceImportPaths) == 0 {
			continue
		}
		ctx := contextForProject(fileCtx.rootDir, fileCtx.projectName)
		seen := make(map[string]struct{})
		for _, rawPath := range module.sourceImportPaths {
			resolved, resolveErr := ctx.ResolveImportPath(rawPath)
			if resolveErr != nil || resolved == nil || resolved.ID.Origin != string(project.ModuleOriginLocal) {
				continue
			}
			target := resolved.FilePath
			if _, ok := contexts[target]; !ok {
				continue
			}
			if _, dup := seen[target]; dup {
				continue
			}
			seen[target] = struct{}{}
			module.resolvedLocalImportFiles = append(module.resolvedLocalImportFiles, target)
		}
	}

	for filePath := range w.modules {
		if _, ok := contexts[filePath]; ok {
			continue
		}
		delete(w.modules, filePath)
	}

	if !workspaceChanged && w.imports != nil {
		return parsedModules, nil
	}

	g := graph.NewDependencyGraph(project.GraphEdgeImport)
	for _, module := range w.modules {
		for _, target := range module.resolvedLocalImportFiles {
			if _, ok := w.modules[target]; !ok {
				continue
			}
			g.AddEdge(graph.NodeID(module.filePath), graph.NodeID(target))
		}
	}

	w.components = buildWorkspaceComponents(w.modules, g)
	w.imports = g
	return parsedModules, nil
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

	virtualPath := filepath.Join(manifest.SourceDir(w.rootDir), ".peeper-lsp", "__workspace__"+peeper.SourceExt)
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

func (w *workspaceIndex) dirtyFiles(filePath string, cached map[string]*module.Module) map[string]struct{} {
	dirty := make(map[string]struct{})
	if w == nil {
		return dirty
	}
	component := w.componentFiles(filePath)
	changedSurfaces := make([]graph.NodeID, 0)
	for member := range component {
		current := w.modules[member]
		if current == nil {
			continue
		}
		cachedModule := cached[member]
		if cachedModule == nil || cachedModule.AST == nil {
			dirty[member] = struct{}{}
			changedSurfaces = append(changedSurfaces, graph.NodeID(member))
			continue
		}
		importsChanged := w.resolvedLocalImportsChanged(current, cachedModule)
		if cachedModule.ContentHash == current.contentHash && !importsChanged {
			continue
		}
		dirty[member] = struct{}{}
		if importsChanged || cachedModule.AST.ImportFingerprint != current.importFingerprint || cachedModule.AST.ExportFingerprint != current.exportFingerprint {
			changedSurfaces = append(changedSurfaces, graph.NodeID(member))
		}
	}
	for _, dependentID := range w.imports.TransitiveDependents(changedSurfaces) {
		dependent := string(dependentID)
		if _, ok := component[dependent]; !ok {
			continue
		}
		dirty[dependent] = struct{}{}
	}
	if len(dirty) == 0 {
		filePath = project.CanonicalPath(filePath)
		if filePath != "" {
			dirty[filePath] = struct{}{}
		}
	}
	return dirty
}

func (w *workspaceIndex) reusePhases(filePath string, cached map[string]*module.Module) map[string]phase.Phase {
	phases := make(map[string]phase.Phase)
	if w == nil || len(cached) == 0 {
		return phases
	}

	// This policy stays separate from seedReusableModules: workspace owns the
	// "what is still reusable after this edit?" decision, while handlers only
	// clone/reset and seed whichever phase this function authorizes.
	component := w.componentFiles(filePath)
	changedSurfaces := make([]graph.NodeID, 0)
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
		if cachedModule.AST == nil {
			changedSurfaces = append(changedSurfaces, graph.NodeID(cachedPath))
			continue
		}
		importsChanged := w.resolvedLocalImportsChanged(current, cachedModule)
		// Syntax survives a resolution change; semantic artifacts must be rebuilt.
		if cachedModule.ContentHash == current.contentHash {
			if importsChanged {
				phases[cachedPath] = phase.Parsed
				changedSurfaces = append(changedSurfaces, graph.NodeID(cachedPath))
			} else {
				phases[cachedPath] = cachedModule.Phase
			}
			continue
		}
		// Import/export surface changes force dependents back to parse-only reuse.
		// Body-only edits stay local to changed modules and do not downgrade
		// importers inside same component.
		if importsChanged || cachedModule.AST.ImportFingerprint != current.importFingerprint || cachedModule.AST.ExportFingerprint != current.exportFingerprint {
			changedSurfaces = append(changedSurfaces, graph.NodeID(cachedPath))
		}
	}

	for _, dependentID := range w.imports.TransitiveDependents(changedSurfaces) {
		dependent := string(dependentID)
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
		phases[dependent] = phase.Parsed
	}

	return phases
}

// resolvedLocalImportsChanged compares the workspace's current import targets
// with the compiler's last published resolution, including targets that vanished.
func (w *workspaceIndex) resolvedLocalImportsChanged(current *workspaceModule, cached *module.Module) bool {
	if w == nil || current == nil || cached == nil {
		return false
	}
	previous := make(map[string]struct{})
	for _, imp := range cached.Imports {
		if imp.ID.Origin == string(project.ModuleOriginLocal) && project.IsPathWithinRoot(w.rootDir, imp.FilePath) {
			previous[project.CanonicalPath(imp.FilePath)] = struct{}{}
		}
	}
	if len(previous) != len(current.resolvedLocalImportFiles) {
		return true
	}
	for _, target := range current.resolvedLocalImportFiles {
		if _, found := previous[target]; !found {
			return true
		}
	}
	return false
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

func buildWorkspaceComponents(modules map[string]*workspaceModule, g *graph.DependencyGraph) []workspaceComponent {
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

func workspaceFiles(rootDir string, sourceOverrides map[string]string) ([]string, error) {
	fileSet := make(map[string]struct{})
	if rootDir != "" {
		files, err := project.DiscoverSourceFiles([]string{rootDir})
		if err != nil {
			return nil, err
		}
		if len(sourceOverrides) == 0 {
			return files, nil
		}
		for _, path := range files {
			fileSet[path] = struct{}{}
		}
	}
	for path := range sourceOverrides {
		if filepath.Ext(path) != peeper.SourceExt {
			continue
		}
		if rootDir != "" && !project.IsPathWithinRoot(rootDir, path) {
			continue
		}
		fileSet[path] = struct{}{}
	}

	files := make([]string, 0, len(fileSet))
	for path := range fileSet {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func workspaceContent(filePath string, sourceOverrides map[string]string) (string, error) {
	if sourceText, ok := sourceOverrides[filePath]; ok {
		return sourceText, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}