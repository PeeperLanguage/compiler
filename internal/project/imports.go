package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
)

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

// ModuleKeyFor builds a stable module key for a file path and origin.
func ModuleKeyFor(origin ModuleOrigin, filePath string) string {
	if filePath == "" {
		return ""
	}
	prefix := string(origin)
	if prefix == "" {
		prefix = string(ModuleOriginLocal)
	}
	return prefix + ":" + CanonicalPath(filePath)
}

func ImportPathFromDecl(imp *ast.ImportDecl) (string, bool) {
	if imp == nil || imp.Path == nil {
		return "", false
	}
	switch node := imp.Path.(type) {
	case *ast.StringLit:
		return node.Value, true
	case *ast.Ident:
		return node.Name, true
	default:
		return "", false
	}
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
	case ModuleOriginStdlib:
		libraryRoot, ok := ctx.LibraryRoot(namespace)
		if !ok {
			return "", fmt.Errorf("missing library root for namespace %q", namespace)
		}
		root = libraryRoot
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
	return rel, nil
}

// ResolveImportPath resolves an import path to a module file.
func (ctx *CompilerContext) ResolveImportPath(from *Module, rawPath string) (*ResolvedImport, error) {
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
		basePath = filepath.Join(rootDir, filepath.FromSlash(logicalPath))
	} else {
		if err := validateImportPath(importPath); err != nil {
			return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: err.Error()}
		}
		if isRemoteImport(importPath) {
			return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: "remote imports are not supported yet"}
		}
		basePath = filepath.Join(ctx.Config.RootDir, filepath.FromSlash(importPath))
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

	resolvedImportPath, err := ctx.ImportPathForFile(origin, namespace, absPath)
	if err != nil {
		return nil, &ImportError{Code: diagnostics.ErrInvalidImportPath, Msg: err.Error()}
	}

	return &ResolvedImport{
		Key:        ModuleKeyFor(origin, absPath),
		ImportPath: resolvedImportPath,
		FilePath:   absPath,
		Origin:     origin,
		Namespace:  namespace,
	}, nil
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

func isRemoteImport(path string) bool {
	return strings.HasPrefix(path, "github.com/") ||
		strings.HasPrefix(path, "gitlab.com/") ||
		strings.HasPrefix(path, "bitbucket.org/")
}
