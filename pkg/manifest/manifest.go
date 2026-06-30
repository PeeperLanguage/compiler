package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"compiler/pkg/peeper"
	"compiler/pkg/toml"
)

const (
	FileName           = "peeper.toml"
	cacheDirName       = ".peeper"
	cacheModulesSubdir = "modules"
	reservedStdAlias   = "core"
)

func CacheModulesDir(projectRoot string) string {
	return filepath.Join(projectRoot, cacheDirName, cacheModulesSubdir)
}

type DependencyType int

const (
	DependencyRemote DependencyType = iota
	DependencyNeighbor
)

type PackageInfo struct {
	Name            string
	Version         string
	CompilerVersion string
	Build           BuildType
}

type BuildType string

const (
	BuildProgram BuildType = "program"
	BuildLib     BuildType = "lib"
)

type DevConfig struct {
	MockRemote bool
	MockPath   string
}

type Dependency struct {
	Type    DependencyType
	Version string
	Path    string
}

type File struct {
	Package      PackageInfo
	Dependencies map[string]Dependency
	Dev          DevConfig
	FilePath     string
}

type Project struct {
	RootDir      string
	ManifestPath string
	File         *File
}

type SourceFileProject struct {
	RootDir     string
	ProjectName string
}

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	versionPattern    = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
	constraintPattern = regexp.MustCompile(`^[A-Za-z0-9._+\-~^<>=*]+$`)
)

func FindManifestPath(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(dir); statErr == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		manifestPath := filepath.Join(dir, FileName)
		if _, err := os.Stat(manifestPath); err == nil {
			return manifestPath, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found", FileName)
		}
		dir = parent
	}
}

func SourceDir(root string) string {
	return filepath.Join(root, peeper.SourceDirName)
}

func ProgramEntryPath(root string) string {
	return filepath.Join(SourceDir(root), peeper.MainFileName)
}

func PathWithinSourceDir(root, path string) bool {
	root, err := filepath.Abs(SourceDir(root))
	if err != nil {
		return false
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func LoadProject(startPath string) (*Project, error) {
	manifestPath, err := FindManifestPath(startPath)
	if err != nil {
		return nil, err
	}
	file, err := Load(manifestPath)
	if err != nil {
		return nil, err
	}
	return &Project{
		RootDir:      filepath.Dir(manifestPath),
		ManifestPath: manifestPath,
		File:         file,
	}, nil
}

// ResolveSourceFileProject keeps file-to-project discovery and source-dir
// validation in one place so CLI and LSP apply the same project layout rule.
func ResolveSourceFileProject(path string) (SourceFileProject, error) {
	ctx := SourceFileProject{RootDir: filepath.Dir(path)}

	loadedProject, err := LoadProject(path)
	if err != nil {
		return ctx, nil
	}

	ctx.RootDir = loadedProject.RootDir
	ctx.ProjectName = loadedProject.File.Package.Name
	if !PathWithinSourceDir(ctx.RootDir, path) {
		return ctx, fmt.Errorf("project source files must stay under %s", SourceDir(ctx.RootDir))
	}

	return ctx, nil
}

func Load(path string) (*File, error) {
	data, err := toml.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	manifest := &File{
		Dependencies: make(map[string]Dependency),
		FilePath:     path,
	}
	pkg, ok := data.Sections["default"]
	if !ok {
		return nil, fmt.Errorf("missing top-level package config")
	}
	name, ok, err := toml.LookupKey[string](pkg, "name")
	if err != nil {
		return nil, err
	}
	if !identifierPattern.MatchString(name) {
		return nil, fmt.Errorf("invalid package.name %q", name)
	}
	manifest.Package.Name = name
	if version, ok, err := toml.LookupKey[string](pkg, "version"); err != nil {
		return nil, fmt.Errorf("package.version: %w", err)
	} else if ok {
		if version != "" && !versionPattern.MatchString(version) {
			return nil, fmt.Errorf("invalid package.version %q", version)
		}
		manifest.Package.Version = version
	}
	if compilerVersion, ok, err := toml.LookupKey[string](pkg, "compiler"); err != nil {
		return nil, fmt.Errorf("compiler: %w", err)
	} else if ok {
		manifest.Package.CompilerVersion = compilerVersion
	}
	build, ok, err := toml.LookupKey[string](pkg, "build")
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	switch BuildType(strings.TrimSpace(build)) {
	case BuildProgram, BuildLib:
		manifest.Package.Build = BuildType(strings.TrimSpace(build))
	default:
		return nil, fmt.Errorf("invalid build %q", build)
	}

	if deps, ok := data.Sections["dependencies"]; ok {
		for alias, raw := range deps {
			if !identifierPattern.MatchString(alias) {
				return nil, fmt.Errorf("invalid dependency alias %q", alias)
			}
			if alias == reservedStdAlias {
				return nil, fmt.Errorf("dependency alias %q is reserved", alias)
			}
			dep, err := ParseDependency(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid dependency %q: %w", alias, err)
			}
			if dep.Type == DependencyRemote && dep.Version != "" && !constraintPattern.MatchString(dep.Version) {
				return nil, fmt.Errorf("invalid dependency %q version %q", alias, dep.Version)
			}
			manifest.Dependencies[alias] = dep
		}
	}

	if dev, ok := data.Sections["dev"]; ok {
		if mockRemote, ok, err := toml.LookupKey[bool](dev, "mock_remote"); err != nil {
			return nil, fmt.Errorf("dev.mock_remote: %w", err)
		} else if ok {
			manifest.Dev.MockRemote = mockRemote
		}
		if mockPath, ok, err := toml.LookupKey[string](dev, "mock_path"); err != nil {
			return nil, fmt.Errorf("dev.mock_path: %w", err)
		} else if ok {
			manifest.Dev.MockPath = mockPath
		}
	}
	return manifest, nil
}

func ParseDependency(raw toml.Value) (Dependency, error) {
	switch value := raw.(type) {
	case string:
		return parseDependencyString(value)
	case toml.Table:
		return parseDependencyTable(value)
	default:
		return Dependency{}, fmt.Errorf("unsupported dependency format")
	}
}

func parseDependencyString(value string) (Dependency, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "../") || strings.HasPrefix(value, "./") {
		return Dependency{Type: DependencyNeighbor, Path: filepath.Clean(value)}, nil
	}
	if !isRemoteRepo(value) {
		return Dependency{}, fmt.Errorf("dependency must be a relative neighbor path or remote repo path")
	}
	dep := Dependency{Type: DependencyRemote, Path: value, Version: "latest"}
	if idx := strings.LastIndex(value, "@"); idx >= 0 {
		before := strings.TrimSpace(value[:idx])
		after := strings.TrimSpace(value[idx+1:])
		if before == "" || after == "" {
			return Dependency{}, fmt.Errorf("missing version after '@'")
		}
		dep.Path = before
		dep.Version = after
	}
	return dep, nil
}

func parseDependencyTable(table toml.Table) (Dependency, error) {
	typeName, _, err := toml.LookupKey[string](table, "type")
	if err != nil {
		return Dependency{}, fmt.Errorf("type: %w", err)
	}
	path, _, err := toml.LookupKey[string](table, "path")
	if err != nil {
		return Dependency{}, fmt.Errorf("path: %w", err)
	}
	repo, _, err := toml.LookupKey[string](table, "repo")
	if err != nil {
		return Dependency{}, fmt.Errorf("repo: %w", err)
	}
	version, _, err := toml.LookupKey[string](table, "version")
	if err != nil {
		return Dependency{}, fmt.Errorf("version: %w", err)
	}

	switch typeName {
	case "", "neighbor":
		switch {
		case path == "":
			return Dependency{}, fmt.Errorf("neighbor dependency requires path")
		case repo != "":
			return Dependency{}, fmt.Errorf("neighbor dependency cannot define repo")
		case version != "":
			return Dependency{}, fmt.Errorf("neighbor dependency cannot define version")
		}
		if !strings.HasPrefix(path, "../") && !strings.HasPrefix(path, "./") {
			return Dependency{}, fmt.Errorf("neighbor dependency path must be relative")
		}
		return Dependency{Type: DependencyNeighbor, Path: filepath.Clean(path)}, nil
	case "remote":
		if repo == "" {
			repo = path
		}
		if repo == "" {
			return Dependency{}, fmt.Errorf("remote dependency requires repo")
		}
		if !isRemoteRepo(repo) {
			return Dependency{}, fmt.Errorf("remote dependency repo %q is invalid", repo)
		}
		if version == "" {
			version = "latest"
		}
		return Dependency{Type: DependencyRemote, Path: repo, Version: version}, nil
	default:
		return Dependency{}, fmt.Errorf("unknown dependency type %q", typeName)
	}
}

func isRemoteRepo(path string) bool {
	return strings.HasPrefix(path, "github.com/") || strings.HasPrefix(path, "gitlab.com/") || strings.HasPrefix(path, "bitbucket.org/")
}

func Save(path string, file *File) error {
	if file == nil {
		return fmt.Errorf("nil manifest")
	}

	var builder strings.Builder
	renderPackageSection(&builder, &file.Package)
	if len(file.Dependencies) > 0 {
		renderDependenciesSection(&builder, file.Dependencies)
	}
	if file.Dev.MockRemote || file.Dev.MockPath != "" {
		renderDevSection(&builder, &file.Dev)
	}
	if !strings.HasSuffix(builder.String(), "\n") {
		builder.WriteString("\n")
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func renderPackageSection(builder *strings.Builder, pkg *PackageInfo) {
	fmt.Fprintf(builder, "name = %s\n", strconv.Quote(pkg.Name))
	if pkg.Version != "" {
		fmt.Fprintf(builder, "version = %s\n", strconv.Quote(pkg.Version))
	}
	if pkg.CompilerVersion != "" {
		fmt.Fprintf(builder, "compiler = %s\n", strconv.Quote(pkg.CompilerVersion))
	}
	fmt.Fprintf(builder, "build = %s\n", strconv.Quote(string(pkg.Build)))
}

func renderDependenciesSection(builder *strings.Builder, deps map[string]Dependency) {
	builder.WriteString("\n[dependencies]\n")
	aliases := make([]string, 0, len(deps))
	for alias := range deps {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		fmt.Fprintf(builder, "%s = %s\n", alias, strconv.Quote(renderDependency(deps[alias])))
	}
}

func renderDevSection(builder *strings.Builder, dev *DevConfig) {
	builder.WriteString("\n[dev]\n")
	fmt.Fprintf(builder, "mock_remote = %t\n", dev.MockRemote)
	if dev.MockPath != "" {
		fmt.Fprintf(builder, "mock_path = %s\n", strconv.Quote(dev.MockPath))
	}
}

func RemoveDependency(path, alias string) error {
	file, err := Load(path)
	if err != nil {
		return err
	}
	delete(file.Dependencies, alias)
	return Save(path, file)
}

func renderDependency(dep Dependency) string {
	switch dep.Type {
	case DependencyNeighbor:
		return dep.Path
	case DependencyRemote:
		version := dep.Version
		if version == "" {
			version = "latest"
		}
		return dep.Path + "@" + version
	default:
		return dep.Path
	}
}
