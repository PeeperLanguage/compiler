// Package manifest reads peeper.toml, the file that makes a folder a project.
package manifest

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"

	"compiler/pkg/toml"
)

// FileName is the name of the project file.
const FileName = "peeper.toml"

// ErrNotFound means the folder has no project file.
var ErrNotFound = errors.New("no " + FileName + " in this folder")

// Manifest is what a project file says.
type Manifest struct {
	Name string // the project's package name: imports of its modules start with "Name:"
	Root string // the folder holding the project file
}

// namePattern is the rule for package names.
var namePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// ValidName reports whether name can be a package name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Read reads the project file in dir. Folders above dir are not searched.
func Read(dir string) (Manifest, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Manifest{}, err
	}
	file := filepath.Join(dir, FileName)
	data, err := toml.ParseFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, ErrNotFound
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", file, err)
	}
	name, _, err := toml.Lookup[string](data, "default", "name")
	if err != nil || !ValidName(name) {
		return Manifest{}, fmt.Errorf("%s: name must start with a letter and hold only letters, digits and `_`", file)
	}
	return Manifest{Name: name, Root: dir}, nil
}
