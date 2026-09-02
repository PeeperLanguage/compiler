package project

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/moduleid"
	"compiler/pkg/manifest"
	"compiler/pkg/remotes"
)

// Canonical file-backed import after resolver lookup.
type ResolvedImport struct {
	// Canonical imported module identity.
	ID moduleid.ID
	// Source import declaration, when resolved from parsed syntax.
	Decl *ast.ImportDecl
	// Absolute slash-separated source path.
	FilePath string
}

// ImportCandidate is one source-level import path visible from a compiler
// context. Continuing candidates are directories or roots which need more path.
type ImportCandidate struct {
	ImportPath string
	FilePath   string
	Continuing bool
}

// ImportError reports a resolved import failure with a diagnostic code.
type ImportError struct {
	Code string
	Msg  string
}

func (e *ImportError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

// ImportCandidates returns immediate import paths matching prefix. Import root
// selection stays beside resolution so editor features cannot invent different
// project, namespace, source-directory, or extension rules.
func (ctx *CompilerContext) ImportCandidates(prefix, currentFile string) []ImportCandidate {
	if ctx == nil {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	root, sourcePrefix, relativePrefix, ok := ctx.importCandidateRoot(prefix)
	if !ok {
		return ctx.importRootCandidates(prefix)
	}
	return ctx.enumerateImportDirectory(root, sourcePrefix, relativePrefix, currentFile)
}

func (ctx *CompilerContext) importRootCandidates(prefix string) []ImportCandidate {
	var candidates []ImportCandidate
	if ctx.Config.ProjectName != "" {
		root := manifest.SourceDir(ctx.Config.RootDir)
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			candidates = append(candidates, ImportCandidate{ImportPath: ctx.Config.ProjectName + "/", Continuing: true})
		}
	}

	namespaces := make(map[string]struct{}, len(ctx.Config.LibraryRoots))
	for namespace := range ctx.Config.LibraryRoots {
		namespaces[namespace] = struct{}{}
	}
	if entries, err := os.ReadDir(ctx.Config.LibraryBaseDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				namespaces[entry.Name()] = struct{}{}
			}
		}
	}
	for namespace := range namespaces {
		root, found := ctx.LibraryRoot(namespace)
		if !found {
			continue
		}
		if info, err := os.Stat(manifest.SourceDir(root)); err != nil || !info.IsDir() {
			continue
		}
		candidates = append(candidates, ImportCandidate{ImportPath: namespace + ":", Continuing: true})
	}

	slices.SortFunc(candidates, func(a, b ImportCandidate) int {
		return strings.Compare(a.ImportPath, b.ImportPath)
	})
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate.ImportPath, prefix) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func (ctx *CompilerContext) importCandidateRoot(prefix string) (root, sourcePrefix, relativePrefix string, ok bool) {
	if ctx.Config.ProjectName != "" {
		localPrefix := ctx.Config.ProjectName + "/"
		if relative, found := strings.CutPrefix(prefix, localPrefix); found {
			if hasHiddenImportSegment(relative) {
				return "", "", "", false
			}
			return manifest.SourceDir(ctx.Config.RootDir), localPrefix, relative, true
		}
	}
	namespace, relative, found := strings.Cut(prefix, ":")
	if !found || namespace == "" || strings.ContainsAny(namespace, "/.") || hasHiddenImportSegment(relative) {
		return "", "", "", false
	}
	libraryRoot, found := ctx.LibraryRoot(namespace)
	if !found {
		return "", "", "", false
	}
	root = manifest.SourceDir(libraryRoot)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", "", "", false
	}
	return root, namespace + ":", relative, true
}

func hasHiddenImportSegment(path string) bool {
	for segment := range strings.SplitSeq(path, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func (ctx *CompilerContext) enumerateImportDirectory(root, sourcePrefix, relativePrefix, currentFile string) []ImportCandidate {
	directoryPart, namePrefix := filepath.ToSlash(filepath.Dir(relativePrefix)), filepath.Base(relativePrefix)
	if strings.HasSuffix(relativePrefix, "/") || relativePrefix == "" {
		directoryPart = strings.TrimSuffix(relativePrefix, "/")
		namePrefix = ""
	}
	if directoryPart == "." {
		directoryPart = ""
	}
	directory := filepath.Join(root, filepath.FromSlash(directoryPart))
	if !PathWithinRoot(root, directory) {
		return nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}

	currentFile = CanonicalPath(currentFile)
	var directories, modules []ImportCandidate
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.HasPrefix(name, namePrefix) {
			continue
		}
		relative := name
		if directoryPart != "" {
			relative = directoryPart + "/" + name
		}
		if entry.IsDir() {
			directories = append(directories, ImportCandidate{ImportPath: sourcePrefix + relative + "/", Continuing: true})
			continue
		}
		if !strings.EqualFold(filepath.Ext(name), ctx.Config.Extension) {
			continue
		}
		target := CanonicalPath(filepath.Join(directory, name))
		if target == currentFile {
			continue
		}
		modules = append(modules, ImportCandidate{
			ImportPath: sourcePrefix + strings.TrimSuffix(relative, filepath.Ext(relative)),
			FilePath:   target,
		})
	}
	slices.SortFunc(directories, func(a, b ImportCandidate) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	slices.SortFunc(modules, func(a, b ImportCandidate) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	return append(directories, modules...)
}

// ImportPathForFile computes the import path for a file within the project roots.
func (ctx *CompilerContext) ImportPathForFile(origin ModuleOrigin, namespace, filePath string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("nil compiler context")
	}
	if filePath == "" {
		return "", fmt.Errorf("empty file path")
	}
	root := ""
	switch origin {
	case ModuleOriginLocal:
		root = ctx.Config.RootDir
		if ctx.Config.ProjectName != "" {
			root = manifest.SourceDir(root)
		}
	case ModuleOriginStdlib:
		libraryRoot, ok := ctx.LibraryRoot(namespace)
		if !ok {
			return "", fmt.Errorf("missing library root for namespace %q", namespace)
		}
		root = manifest.SourceDir(libraryRoot)
	}
	if root == "" {
		base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
		if base == "" {
			return "", fmt.Errorf("invalid file path")
		}
		return base, nil
	}
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file is outside the module root")
	}
	rel = filepath.ToSlash(rel)
	rel = strings.TrimSuffix(rel, filepath.Ext(rel))
	if origin == ModuleOriginLocal && ctx.Config.ProjectName != "" {
		return ctx.Config.ProjectName + "/" + rel, nil
	}
	return rel, nil
}

// ResolveImportPath resolves an import path to a module file.
func (ctx *CompilerContext) ResolveImportPath(rawPath string) (*ResolvedImport, error) {
	if ctx == nil {
		return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: "nil compiler context"}
	}
	importPath := strings.TrimSpace(rawPath)
	if importPath == "" {
		return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: "empty import path"}
	}
	origin := ModuleOriginLocal
	namespace := ""
	var basePath string

	if importNamespace, logicalPath, ok := splitNamespacedImportPath(importPath); ok {
		namespace = importNamespace
		origin = ModuleOriginStdlib
		if err := validateImportPath(logicalPath); err != nil {
			return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: err.Error()}
		}
		rootDir, found := ctx.LibraryRoot(namespace)
		if !found {
			return nil, &ImportError{Code: diagnostics.ErrModuleNotFound, Msg: fmt.Sprintf("invalid library prefix: %s", namespace)}
		}
		basePath = filepath.Join(manifest.SourceDir(rootDir), filepath.FromSlash(logicalPath))
	} else {
		if err := validateImportPath(importPath); err != nil {
			return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: err.Error()}
		}
		if remotes.IsRemotePath(importPath) {
			return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: "remote imports are not supported yet"}
		}
		if ctx.Config.ProjectName == "" {
			return nil, &ImportError{
				Code: diagnostics.ErrInvalidImportPath,
				Msg:  fmt.Sprintf("local imports require %s; run `peeper init` to create project config", manifest.FileName),
			}
		}
		prefix := ctx.Config.ProjectName + "/"
		if !strings.HasPrefix(importPath, prefix) {
			return nil, &ImportError{
				Code: diagnostics.ErrInvalidImportPath,
				Msg:  fmt.Sprintf("local import must start with %q", prefix),
			}
		}
		// Local imports stay inside nearest project source root. Prefix is
		// package boundary, not path segment on disk.
		basePath = filepath.Join(manifest.SourceDir(ctx.Config.RootDir), filepath.FromSlash(strings.TrimPrefix(importPath, prefix)))
	}

	if basePath == "" {
		return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: "invalid import path"}
	}

	if ext := filepath.Ext(basePath); ext == "" {
		basePath += ctx.Config.Extension
	} else if !strings.EqualFold(ext, ctx.Config.Extension) {
		return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: fmt.Sprintf("invalid import extension %q", ext)}
	}

	absPath := basePath
	if !filepath.IsAbs(absPath) {
		resolved, err := filepath.Abs(absPath)
		if err != nil {
			return nil, err
		}
		absPath = resolved
	}
	absPath = filepath.Clean(absPath)

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, &ImportError{Code: diagnostics.ErrModuleNotFound, Msg: fmt.Sprintf("module not found: %s", absPath)}
	}
	if info.IsDir() {
		return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: "import path points to a directory"}
	}
	absPath = CanonicalPath(absPath)

	id, err := ctx.IdentityForFile(origin, namespace, absPath)
	if err != nil {
		return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: err.Error()}
	}

	return &ResolvedImport{ID: id, FilePath: absPath}, nil
}

func splitNamespacedImportPath(importPath string) (string, string, bool) {
	namespace, logicalPath, ok := strings.Cut(importPath, ":")
	if !ok {
		return "", "", false
	}
	namespace = strings.TrimSpace(namespace)
	logicalPath = strings.TrimSpace(logicalPath)
	if namespace == "" || logicalPath == "" {
		return "", "", false
	}
	if strings.Contains(namespace, "/") || strings.Contains(namespace, ".") {
		return "", "", false
	}
	return namespace, logicalPath, true
}

func validateImportPath(importPath string) error {
	if importPath == "." || importPath == ".." {
		return fmt.Errorf("import path must be root-relative")
	}
	if filepath.IsAbs(importPath) || strings.HasPrefix(importPath, "./") || strings.HasPrefix(importPath, "../") {
		return fmt.Errorf("import path must be root-relative")
	}
	parts := strings.SplitSeq(importPath, "/")
	for part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("import path must be root-relative")
		}
	}
	return nil
}
