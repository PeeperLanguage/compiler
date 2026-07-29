package lsp

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"compiler/internal/diagnostics"
	driver "compiler/internal/driver"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/pkg/manifest"
)

type ServerState struct {
	mu          sync.Mutex
	diagWG      sync.WaitGroup
	RootDir     string
	Cache       map[string]string
	LastCtx     *project.CompilerContext
	LastMetrics project.CompileMetrics
	workspace   *workspaceIndex
	modules     map[string]*project.Module
	diagVersion map[string]uint64
}

func NewServerState() *ServerState {
	return &ServerState{
		Cache:       make(map[string]string),
		modules:     make(map[string]*project.Module),
		diagVersion: make(map[string]uint64),
	}
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
	diagBag := diagnostics.NewDiagnosticBag()
	sourceProject, err := manifest.ResolveSourceFileProject(entryFile)
	rootDir := sourceProject.RootDir
	projectName := sourceProject.ProjectName
	cfg := project.Config{
		RootDir:     rootDir,
		ProjectName: projectName,
	}
	ctx := driver.NewContext(cfg, diagBag)
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
				driver.AddOverlay(ctx, cachedPath, cachedContent)
			}
			if virtualPath, content, ok := s.workspace.syntheticEntry(entryFile); ok {
				if driver.ParseFileWithOverlay(ctx, virtualPath, content) != nil {
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

	absEntry, err := filepath.Abs(entryFile)
	for cachedPath, cachedContent := range s.Cache {
		absCached, err2 := filepath.Abs(cachedPath)
		if err2 != nil || (err == nil && absCached == absEntry) {
			continue
		}
		driver.AddOverlay(ctx, cachedPath, cachedContent)
	}

	content := s.Cache[entryFile]
	mod := driver.ParseFileWithOverlay(ctx, entryFile, content)
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
		cloned.Phase = phase
		ctx.Metrics.AddReusedModule()
		if phase < module.Phase {
			ctx.Metrics.AddDowngradedModule()
		}
		if phase <= project.PhaseParsed {
			cloned.ModuleScope = nil
			cloned.Semantics = nil
		}
		if phase < project.PhaseHIR {
			cloned.HIR = nil
		}
		if phase < project.PhaseCFG {
			cloned.CFG = nil
		}
		if phase < project.PhaseMIR {
			cloned.MIR = nil
		}
		if phase < project.PhaseBackend {
			cloned.LLVMIR = ""
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
