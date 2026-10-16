package project

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	sourceExt = ".peep"
	// preludePath is imported by every module except itself.
	preludePath = "core:global"
)

// Packages maps each package name to the folder holding its source files, for
// example "myapp" to <project>/src and "core" to <installation>/libs/core/src.
// An import "myapp:utils/file" names <project>/src/utils/file.peep.
type Packages map[string]string

// pathOf returns the import path of the source file at file: its package and
// its path in that package's folder, without the extension. A file in no
// package's folder has only its file name for a path, and inPackage is false.
func (p Packages) pathOf(file string) (path string, inPackage bool) {
	for name, dir := range p {
		rel, err := filepath.Rel(dir, file)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return name + ":" + filepath.ToSlash(strings.TrimSuffix(rel, sourceExt)), true
		}
	}
	return strings.TrimSuffix(filepath.Base(file), sourceExt), false
}

// resolve checks an import path as written, "package:path/in/package", and
// returns its canonical form and the file it names. The file is not checked
// to exist.
func (p Packages) resolve(path string) (canonical, file string, err error) {
	name, rest, ok := strings.Cut(path, ":")
	if !ok {
		return "", "", errors.New("import path needs a package, such as `myapp:utils/file`")
	}
	dir, known := p[name]
	if !known {
		return "", "", fmt.Errorf("unknown package `%s`", name)
	}
	if ext := filepath.Ext(rest); ext != "" {
		if ext != sourceExt {
			return "", "", fmt.Errorf("invalid import extension %q", ext)
		}
		rest = strings.TrimSuffix(rest, ext)
	}
	for _, part := range strings.Split(rest, "/") {
		if part == "" || part == "." || part == ".." {
			return "", "", errors.New("import path parts cannot be empty, `.` or `..`")
		}
	}
	return name + ":" + rest, filepath.Join(dir, filepath.FromSlash(rest)+sourceExt), nil
}
