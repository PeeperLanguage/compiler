package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"

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

	cachePath := manifest.CacheModulesDir(ctx.projectRoot)
	if err := os.MkdirAll(cachePath, 0o755); err != nil {
		return err
	}

	plans, checked, err := collectUpdatePlans(http.DefaultClient, ctx.file, ctx.lockfile, &ctx.devConfig, ctx.filter)
	if err != nil {
		return err
	}

	updated := 0
	installErrors := make([]error, 0)
	for _, plan := range plans {
		printUpdate(fmt.Sprintf("%s: %s → %s", plan.RepoPath, plan.CurrentVersion, plan.TargetVersion))
		constraints := map[string][]string{
			plan.RepoPath: []string{">" + plan.CurrentVersion, "<=" + plan.TargetVersion},
		}
		if err := installPackageRecursive(http.DefaultClient, cachePath, plan.RepoPath, "latest", &ctx.devConfig, ctx.lockfile, constraints, plan.Alias, "", map[string]bool{}); err != nil {
			printError(fmt.Sprintf("Failed to update %s: %v", plan.RepoPath, err))
			installErrors = append(installErrors, fmt.Errorf("update %s: %w", plan.RepoPath, err))
			continue
		}
		if dep, ok := ctx.file.Dependencies[plan.Alias]; ok {
			dep.Version = plan.TargetVersion
			ctx.file.Dependencies[plan.Alias] = dep
		}
		updated++
	}
	if err := errors.Join(installErrors...); err != nil {
		return err
	}
	if updated > 0 {
		pruned, err := pruneUnusedDependencies(ctx.lockfile)
		if err != nil {
			return err
		}
		if err := manifest.SaveDependencyState(ctx.projectRoot, ctx.file, ctx.lockfile); err != nil {
			return err
		}
		if err := deletePrunedDependencies(cachePath, pruned); err != nil {
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
