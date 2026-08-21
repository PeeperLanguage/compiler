package xtest_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"compiler/pkg/toml"
)

type fixtureExpectation struct {
	Name           string
	Dir            string
	Mode           string
	Outcome        string
	ExitCode       int
	CompilerArgs   []string
	ProgramArgs    []string
	StdoutContains []string
	StderrContains []string
}

func TestFixtureContracts(t *testing.T) {
	manifests, err := filepath.Glob(filepath.Join("*", "peeper.toml"))
	if err != nil {
		t.Fatalf("discover fixtures: %v", err)
	}
	slices.Sort(manifests)
	if len(manifests) == 0 {
		t.Fatal("no fixture manifests found")
	}

	expectations := make([]fixtureExpectation, 0, len(manifests))
	for _, manifestPath := range manifests {
		expectations = append(expectations, readFixtureExpectation(t, manifestPath))
	}

	binary := os.Getenv("PEEPER_BIN")
	if binary == "" {
		t.Skip("PEEPER_BIN not set; fixture manifests validated without execution")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatalf("resolve PEEPER_BIN: %v", err)
	}
	for _, expectation := range expectations {
		t.Run(expectation.Name, func(t *testing.T) {
			runFixture(t, binary, expectation)
		})
	}
}

func readFixtureExpectation(t *testing.T, manifestPath string) fixtureExpectation {
	t.Helper()
	data, err := toml.ParseFile(manifestPath)
	if err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}
	section, ok := data.Section("test")
	if !ok {
		t.Fatalf("%s has no [test] section", manifestPath)
	}
	name := filepath.Base(filepath.Dir(manifestPath))
	expectation := fixtureExpectation{Name: name, Dir: filepath.Dir(manifestPath)}
	expectation.Mode = requiredFixtureValue[string](t, section, manifestPath, "mode")
	expectation.Outcome = requiredFixtureValue[string](t, section, manifestPath, "outcome")
	expectation.ExitCode = optionalFixtureValue[int](t, section, manifestPath, "exit_code")
	expectation.CompilerArgs = optionalFixtureValue[[]string](t, section, manifestPath, "compiler_args")
	expectation.ProgramArgs = optionalFixtureValue[[]string](t, section, manifestPath, "program_args")
	expectation.StdoutContains = optionalFixtureValue[[]string](t, section, manifestPath, "stdout_contains")
	expectation.StderrContains = optionalFixtureValue[[]string](t, section, manifestPath, "stderr_contains")
	if !slices.Contains([]string{"check", "build", "run"}, expectation.Mode) {
		t.Fatalf("%s has invalid mode %q", manifestPath, expectation.Mode)
	}
	if !slices.Contains([]string{"success", "failure", "exit_code"}, expectation.Outcome) {
		t.Fatalf("%s has invalid outcome %q", manifestPath, expectation.Outcome)
	}
	if expectation.Outcome == "exit_code" && expectation.Mode != "run" {
		t.Fatalf("%s uses exit_code outside run mode", manifestPath)
	}
	if expectation.Outcome != "exit_code" && expectation.ExitCode != 0 {
		t.Fatalf("%s sets exit_code without exit_code outcome", manifestPath)
	}
	return expectation
}

func requiredFixtureValue[T any](t *testing.T, section toml.Table, path, key string) T {
	t.Helper()
	value, found, err := toml.LookupKey[T](section, key)
	if err != nil || !found {
		t.Fatalf("%s invalid or missing %s: %v", path, key, err)
	}
	return value
}

func optionalFixtureValue[T any](t *testing.T, section toml.Table, path, key string) T {
	t.Helper()
	value, _, err := toml.LookupKey[T](section, key)
	if err != nil {
		t.Fatalf("%s invalid %s: %v", path, key, err)
	}
	return value
}

func runFixture(t *testing.T, binary string, expectation fixtureExpectation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	compilerArgs := append([]string{"-logformat", "normal"}, expectation.CompilerArgs...)
	if expectation.Mode == "check" {
		checkArgs := append([]string{"check"}, compilerArgs...)
		checkArgs = append(checkArgs, expectation.Dir)
		stdout, stderr, exitCode := executeFixtureProcess(t, ctx, binary, checkArgs...)
		checkFixtureOutcome(t, expectation, stdout, stderr, exitCode)
		return
	}

	executable := filepath.Join(t.TempDir(), "fixture")
	buildArgs := append([]string{"build"}, compilerArgs...)
	buildArgs = append(buildArgs, "-o", executable, expectation.Dir)
	stdout, stderr, exitCode := executeFixtureProcess(t, ctx, binary, buildArgs...)
	if expectation.Mode == "build" {
		checkFixtureOutcome(t, expectation, stdout, stderr, exitCode)
		return
	}
	if exitCode != 0 {
		t.Fatalf("fixture build failed with exit %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	}
	stdout, stderr, exitCode = executeFixtureProcess(t, ctx, executable, expectation.ProgramArgs...)
	checkFixtureOutcome(t, expectation, stdout, stderr, exitCode)
}

func executeFixtureProcess(t *testing.T, ctx context.Context, command string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.CommandContext(ctx, command, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("execute %s: %v", command, err)
	return "", "", -1
}

func checkFixtureOutcome(t *testing.T, expectation fixtureExpectation, stdout, stderr string, exitCode int) {
	t.Helper()
	switch expectation.Outcome {
	case "success":
		if exitCode != 0 {
			t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
		}
	case "failure":
		if exitCode == 0 {
			t.Fatalf("exit = 0, want failure\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
	case "exit_code":
		if exitCode != expectation.ExitCode {
			t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", exitCode, expectation.ExitCode, stdout, stderr)
		}
	}
	for _, text := range expectation.StdoutContains {
		if !strings.Contains(stdout, text) {
			t.Fatalf("stdout missing %q:\n%s", text, stdout)
		}
	}
	for _, text := range expectation.StderrContains {
		if !strings.Contains(stderr, text) {
			t.Fatalf("stderr missing %q:\n%s", text, stderr)
		}
	}
}
