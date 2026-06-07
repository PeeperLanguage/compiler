package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"compiler/pkg/manifest"
)

func CleanupCommand(args []string) error {
	manifestPath, err := manifest.FindManifestPath(".")
	if err != nil {
		return err
	}
	projectRoot := filepath.Dir(manifestPath)
	cachePath := manifest.CacheModulesDir(projectRoot)
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		printInfo("No cache directory found")
		return nil
	}

	lockfile, err := manifest.LoadLockfile(projectRoot)
	if err != nil {
		return err
	}
	candidates, err := listOrphanCandidates(cachePath, lockfile)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		printInfo("No unused dependencies to clean up")
		return nil
	}

	removed := map[string]struct{}{}
	fmt.Printf("Found %d orphaned dependencies:\n", len(candidates))
	for _, candidate := range candidates {
		fmt.Printf("  → Removing %s\n", candidate.PackageID)
		_ = os.RemoveAll(candidate.Path)
		removed[candidate.PackageID] = struct{}{}
		if candidate.InLock {
			lockfile.RemoveDependency(candidate.PackageID)
		}
	}
	for _, packageID := range pruneUnusedDependencies(lockfile, cachePath) {
		removed[packageID] = struct{}{}
	}

	if err := manifest.SaveLockfile(projectRoot, lockfile); err != nil {
		return err
	}
	printSuccess(fmt.Sprintf("Cleaned up %d orphaned dependencies", len(removed)))
	return nil
}
