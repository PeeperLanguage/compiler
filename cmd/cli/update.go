package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"compiler/pkg/manifest"
)

func UpdateCommand(args []string) error {
	ctx, err := prepareUpdateScanContext(args)
	if err != nil {
		return err
	}
	if len(ctx.file.Dependencies) == 0 {
		printInfo("No dependencies to update")
		return nil
	}

	cachePath := filepath.Join(ctx.projectRoot, ".ember", "modules")
	if err := os.MkdirAll(cachePath, 0o755); err != nil {
		return err
	}

	plans, checked, err := collectUpdatePlans(ctx.file, ctx.lockfile, &ctx.devConfig, ctx.filter)
	if err != nil {
		return err
	}

	updated := 0
	for _, plan := range plans {
		printUpdate(fmt.Sprintf("%s: %s → %s", plan.RepoPath, plan.CurrentVersion, plan.TargetVersion))
		constraints := map[string][]string{
			plan.RepoPath: []string{">" + plan.CurrentVersion, "<=" + plan.TargetVersion},
		}
		if err := installPackageRecursive(cachePath, plan.RepoPath, "latest", &ctx.devConfig, ctx.lockfile, constraints, plan.Alias, "", map[string]bool{}); err != nil {
			printError(fmt.Sprintf("Failed to update %s: %v", plan.RepoPath, err))
			continue
		}
		if dep, ok := ctx.file.Dependencies[plan.Alias]; ok {
			dep.Version = plan.TargetVersion
			ctx.file.Dependencies[plan.Alias] = dep
		}
		updated++
	}
	if updated > 0 {
		pruneUnusedDependencies(ctx.lockfile, cachePath)
	}

	if err := manifest.SaveLockfile(ctx.projectRoot, ctx.lockfile); err != nil {
		return err
	}
	if updated > 0 {
		if err := manifest.Save(ctx.manifestPath, ctx.file); err != nil {
			return err
		}
	}
	if updated == 0 {
		printSuccess(fmt.Sprintf("All %d packages are up to date", checked))
		return nil
	}
	printSuccess(fmt.Sprintf("Updated %d/%d packages", updated, checked))
	return nil
}
