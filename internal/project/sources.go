package project

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"compiler/pkg/peeper"
)

// DiscoverSourceFiles resolves explicit files and recursively expands
// directories using one canonical skip and dedup policy for project clients.
func DiscoverSourceFiles(paths []string) ([]string, error) {
	files := make(map[string]struct{})
	for _, requested := range paths {
		if strings.TrimSpace(requested) == "" {
			continue
		}
		path, err := filepath.Abs(requested)
		if err != nil {
			return nil, fmt.Errorf("resolve source path %q: %w", requested, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect source path %q: %w", requested, err)
		}
		if !info.IsDir() {
			if filepath.Ext(path) != peeper.SourceExt {
				return nil, fmt.Errorf("unsupported source file extension %q (expected %s)", filepath.Ext(path), peeper.SourceExt)
			}
			files[CanonicalPath(path)] = struct{}{}
			continue
		}
		if err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				name := entry.Name()
				if name == ".git" || name == "build" || name == "_builtin_library" || strings.HasPrefix(name, ".tmp") {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) == peeper.SourceExt {
				files[CanonicalPath(path)] = struct{}{}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("discover sources under %q: %w", requested, err)
		}
	}

	result := make([]string, 0, len(files))
	for path := range files {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}
