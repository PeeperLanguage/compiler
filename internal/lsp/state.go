package lsp

import (
	"strings"
	"sync"
	"time"

	"compiler/internal/diagnostics"
	"compiler/internal/driver"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/pkg/manifest"
)

type ServerState struct {
	mu               sync.Mutex
	publishMu        sync.Mutex
	diagWG           sync.WaitGroup
	RootDir          string
	Cache            map[string]string
	LastCtx          *project.CompilerContext
	LastMetrics      project.CompileMetrics
	workspace        *workspaceIndex
	modules          map[string]*project.Module
	diagVersion      map[string]uint64
	diagGeneration   uint64
	documentVersions map[string]int
}

func NewServerState() *ServerState {
	return &ServerState{
		Cache:            make(map[string]string),
		modules:          make(map[string]*project.Module),
		diagVersion:      make(map[string]uint64),
		documentVersions: make(map[string]int),
	}
}

type diagnosticSnapshot struct {
	ctx        *project.CompilerContext
	files      []string
	generation uint64
	versions   map[string]int
}

func (s *ServerState) applyDocumentSnapshot(filePath string, text *string, version *int) {
	if s == nil {
		return
	}
	filePath = project.CanonicalPath(filePath)
	// Publication checks generation while holding publishMu. Mutations must use
	// same boundary so a checked snapshot cannot write after this state is current.
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == nil {
		delete(s.Cache, filePath)
		delete(s.documentVersions, filePath)
	} else {
		s.Cache[filePath] = *text
		if version != nil {
			s.documentVersions[filePath] = *version
		}
	}
	s.diagGeneration++
}

func (s *ServerState) diagnosticSnapshot(entryFile string, files []string) *diagnosticSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.diagnosticSnapshotLocked(entryFile, files)
}

func (s *ServerState) diagnosticSnapshotLocked(entryFile string, files []string) *diagnosticSnapshot {
	ctx, _ := s.recompileLocked(entryFile)
	if len(files) == 0 {
		files = []string{project.CanonicalPath(entryFile)}
		if s.workspace != nil {
			if component, ok := s.workspace.componentForFile(entryFile); ok && len(component.files) > 0 {
				files = component.files
			}
		}
	}
	files = append([]string(nil), files...)
	versions := make(map[string]int, len(files))
	for _, filePath := range files {
		filePath = project.CanonicalPath(filePath)
		if version, ok := s.documentVersions[filePath]; ok {
			versions[filePath] = version
		}
	}
	return &diagnosticSnapshot{ctx: ctx, files: files, generation: s.diagGeneration, versions: versions}
}

func (s *ServerState) workspaceDiagnosticSnapshots() []*diagnosticSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RootDir == "" {
		return nil
	}
	if s.workspace == nil {
		s.workspace = newWorkspaceIndex(s.RootDir)
	}
	if err := s.workspace.rebuild(s.Cache); err != nil {
		return nil
	}
	components := append([]workspaceComponent(nil), s.workspace.components...)
	snapshots := make([]*diagnosticSnapshot, 0, len(components))
	for _, component := range components {
		if len(component.files) == 0 {
			continue
		}
		entry := component.files[0]
		if len(component.roots) > 0 {
			entry = component.roots[0]
		}
		snapshots = append(snapshots, s.diagnosticSnapshotLocked(entry, component.files))
	}
	return snapshots
}

func (s *ServerState) recompile(entryFile string) (*project.CompilerContext, *project.Module) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recompileLocked(entryFile)
}

func (s *ServerState) recompileLocked(entryFile string) (*project.CompilerContext, *project.Module) {
	canonicalEntry := project.CanonicalPath(entryFile)
	diagBag := diagnostics.NewDiagnosticBag()
	sourceProject, err := manifest.ResolveSourceFileProject(entryFile)
	rootDir := sourceProject.RootDir
	projectName := sourceProject.ProjectName
	cfg := project.Config{
		RootDir:     rootDir,
		ProjectName: projectName,
	}
	ctx := compiler.NewCompilerContext(cfg, diagBag)
	ctx.Metrics = &project.CompileMetrics{}
	if err != nil {
		ctx.Diagnostics.Add(diagnostics.NewError(
			err.Error(),
		))
		s.LastCtx = ctx
		s.LastMetrics = ctx.Metrics.Snapshot()
		return ctx, nil
	}

	rootDir = project.CanonicalPath(s.RootDir)
	if rootDir != "" {
		if s.workspace == nil || s.workspace.rootDir != rootDir {
			s.workspace = newWorkspaceIndex(rootDir)
		}
		if err := s.workspace.rebuild(s.Cache); err == nil {
			dirtyFiles := s.workspace.dirtyFiles(entryFile, s.modules)
			ctx.Metrics.AddDirtyFiles(len(dirtyFiles))
			s.seedReusableModules(ctx, dirtyFiles)
			for cachedPath, cachedContent := range s.Cache {
				compiler.AddSource(ctx, cachedPath, cachedContent)
			}
			if virtualPath, content, ok := s.workspace.syntheticEntry(entryFile); ok {
				if compiler.CompileFile(ctx, virtualPath, &content) != nil {
					s.LastCtx = ctx
					s.LastMetrics = ctx.Metrics.Snapshot()
					s.captureModules(ctx)
					if mod, ok := ctx.ModuleByFile(entryFile); ok {
						return ctx, mod
					}
				}
			}
		}
	}

	for cachedPath, cachedContent := range s.Cache {
		if project.CanonicalPath(cachedPath) == canonicalEntry {
			continue
		}
		compiler.AddSource(ctx, cachedPath, cachedContent)
	}

	var overlay *string
	if content, ok := s.Cache[canonicalEntry]; ok {
		overlay = &content
	}
	mod := compiler.CompileFile(ctx, entryFile, overlay)
	s.LastCtx = ctx
	s.LastMetrics = ctx.Metrics.Snapshot()
	s.captureModules(ctx)
	return ctx, mod
}

func (s *ServerState) currentCompiledModule(filePath string) (*project.CompilerContext, *project.Module) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LastCtx != nil {
		canonical := project.CanonicalPath(filePath)
		if canonical != "" {
			if mod, ok := s.LastCtx.ModuleByFile(canonical); ok && mod != nil {
				// Overlays for other open files register placeholder modules in the
				// context before they are parsed. Hover/definition/rename must not
				// reuse those stubs even if their content hash matches the buffer.
				if mod.AST == nil || mod.Phase < project.PhaseParsed {
					return s.recompileLocked(filePath)
				}
				// Reuse the last compiled snapshot only when the current buffer text
				// still matches it. Otherwise hover/definition/rename would keep
				// reading a frozen AST after edits until some later path recompiles.
				if content, err := workspaceContent(canonical, s.Cache); err == nil && mod.ContentHash == ast.HashText(content) {
					return s.LastCtx, mod
				}
			}
		}
	}
	return s.recompileLocked(filePath)
}

func (s *ServerState) scheduleDiagnosticRefresh(filePath string, delay time.Duration, publish func()) {
	if s == nil || publish == nil {
		return
	}
	filePath = project.CanonicalPath(filePath)
	s.mu.Lock()
	s.diagVersion[filePath]++
	version := s.diagVersion[filePath]
	s.mu.Unlock()

	s.diagWG.Go(func() {
		// Full-sync edits arrive as whole-file snapshots. Delay diagnostics so a
		// burst of keystrokes collapses into one recompile instead of one per edit.
		time.Sleep(delay)
		s.mu.Lock()
		if s.diagVersion[filePath] != version {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		publish()
	})
}

func (s *ServerState) waitForScheduledDiagnostics() {
	if s == nil {
		return
	}
	s.diagWG.Wait()
}

func (s *ServerState) seedReusableModules(ctx *project.CompilerContext, dirtyFiles map[string]struct{}) {
	if s == nil || ctx == nil || len(s.modules) == 0 {
		return
	}
	for _, module := range s.modules {
		if module != nil {
			ctx.SetSemanticExportBaseline(module.Key, module.SemanticExportFingerprint)
		}
	}
	reusePhases := map[string]project.ModulePhase{}
	if s.workspace != nil {
		reusePhases = s.workspace.reusePhases(firstDirtyFile(dirtyFiles), s.modules)
	}
	for filePath, module := range s.modules {
		if module == nil || module.FilePath == "" {
			continue
		}
		phase, ok := reusePhases[filePath]
		if !ok {
			continue
		}
		if strings.Contains(filePath, "/.peeper-lsp/") {
			continue
		}
		if phase == module.Phase {
			ctx.Metrics.AddReusedModule()
			ctx.AddModule(module)
			continue
		}
		cloned := *module
		cloned.ResetToPhase(phase)
		ctx.Metrics.AddReusedModule()
		if phase < module.Phase {
			ctx.Metrics.AddDowngradedModule()
		}
		ctx.AddModule(&cloned)
	}
}

func firstDirtyFile(dirtyFiles map[string]struct{}) string {
	for filePath := range dirtyFiles {
		return filePath
	}
	return ""
}

func (s *ServerState) captureModules(ctx *project.CompilerContext) {
	if s == nil || ctx == nil {
		return
	}
	if s.modules == nil {
		s.modules = make(map[string]*project.Module)
	}
	for _, module := range ctx.Modules() {
		if module == nil || module.FilePath == "" || module.AST == nil || module.Phase < project.PhaseParsed {
			continue
		}
		if strings.Contains(module.FilePath, "/.peeper-lsp/") {
			continue
		}
		if s.workspace != nil {
			if current := s.workspace.modules[module.FilePath]; current != nil {
				module.ContentHash = current.contentHash
				module.ImportFingerprint = current.importFingerprint
				module.ExportFingerprint = current.exportFingerprint
			}
		}
		if existing := s.modules[module.FilePath]; existing != nil &&
			existing.Origin == project.ModuleOriginStdlib &&
			module.Origin == project.ModuleOriginLocal {
			continue
		}
		s.modules[module.FilePath] = module
	}
}
