package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"compiler/pkg/remotes"
)

func GetModulePath(cachePath, repoName, version string) (string, error) {
	provider, repoPath, ok := remotes.Parse(repoName)
	if !ok {
		return "", fmt.Errorf("invalid remote package path %q", repoName)
	}
	version = strings.TrimSpace(version)
	if version == "" || version == "." || version == ".." || strings.ContainsAny(version, `/\\`) || strings.ContainsRune(version, '\x00') {
		return "", fmt.Errorf("invalid package version %q", version)
	}
	moduleID := string(provider) + "/" + repoPath + "@" + version
	return filepath.Join(cachePath, filepath.FromSlash(moduleID)), nil
}

func DeleteModule(cachePath, repoName, version string) error {
	modulePath, err := GetModulePath(cachePath, repoName, version)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(modulePath); err != nil {
		return fmt.Errorf("delete module %s: %w", modulePath, err)
	}
	return nil
}
