package cli

import (
	"fmt"
)

func SniffCommand(args []string) error {
	ctx, err := prepareUpdateScanContext(args)
	if err != nil {
		return err
	}
	if len(ctx.file.Dependencies) == 0 {
		printInfo("No dependencies to sniff")
		return nil
	}

	plans, checked, err := collectUpdatePlans(ctx.file, ctx.lockfile, &ctx.devConfig, ctx.filter)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		printSuccess(fmt.Sprintf("No updates available (%d checked)", checked))
		return nil
	}

	fmt.Printf("Available updates (%d/%d):\n", len(plans), checked)
	for _, plan := range plans {
		fmt.Printf("  %s (%s): %s -> %s\n", plan.Alias, plan.RepoPath, plan.CurrentVersion, plan.TargetVersion)
	}
	return nil
}
