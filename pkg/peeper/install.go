package peeper

import "path/filepath"

// InstallationRootForExecutable returns the root containing bin, libs, toolchains, and targets.
func InstallationRootForExecutable(executablePath string) string {
	if executablePath == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executablePath); err == nil && resolved != "" {
		executablePath = resolved
	}
	return filepath.Clean(filepath.Join(filepath.Dir(executablePath), ".."))
}
