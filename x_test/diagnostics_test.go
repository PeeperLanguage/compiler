package xtest_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var updateDiagnostics = flag.Bool("update", false, "rewrite testdata/diagnostics.golden from the current compiler")

const diagnosticsGolden = "testdata/diagnostics.golden"

// TestDiagnosticSnapshot pins the full `peeper check` output of every fixture,
// so a change to any message text, position, or count is seen in review.
// Rewrite the golden file with `go test ./x_test -run TestDiagnosticSnapshot -update`
// only when the change is intended.
func TestDiagnosticSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("golden paths use / separators")
	}
	manifests := fixtureManifests(t)
	binary := fixtureBinary(t)
	if binary == "" {
		t.Skip("PEEPER_BIN not set; diagnostic snapshot requires bundled compiler")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Bundled libraries live beside the binary's bin directory, so messages that
	// point into them carry the install root.
	installRoot := filepath.Dir(filepath.Dir(binary))
	paths := strings.NewReplacer(
		root+string(filepath.Separator), "",
		installRoot+string(filepath.Separator), "$PEEPER/",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var snapshot strings.Builder
	for _, manifestPath := range manifests {
		dir := filepath.Dir(manifestPath)
		stdout, stderr, exitCode := executeFixtureProcess(t, ctx, binary, "check", "-logformat", "normal", dir)
		fmt.Fprintf(&snapshot, "=== %s (exit %d)\n", dir, exitCode)
		output := strings.TrimSuffix(paths.Replace(stdout+stderr), "\n")
		if output == "" {
			continue
		}
		// Trailing spaces on gutter lines are not part of what a message says,
		// and keeping them would fail `git diff --check` on the golden file.
		for _, line := range strings.Split(output, "\n") {
			snapshot.WriteString(strings.TrimRight(line, " "))
			snapshot.WriteByte('\n')
		}
	}
	got := snapshot.String()

	if *updateDiagnostics {
		if err := os.MkdirAll(filepath.Dir(diagnosticsGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(diagnosticsGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(diagnosticsGolden)
	if err != nil {
		t.Fatalf("read %s: %v (create it with -update)", diagnosticsGolden, err)
	}
	if got != string(want) {
		t.Fatalf("diagnostics differ from %s; first difference:\n%s", diagnosticsGolden, firstDifference(string(want), got))
	}
}

// firstDifference shows the first differing line with its fixture header, so a
// failure names the fixture without dumping the whole snapshot.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	fixture := ""
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if strings.HasPrefix(w, "=== ") {
			fixture = w
		}
		if w != g {
			return fmt.Sprintf("%s\nline %d\nwant: %q\ngot:  %q", fixture, i+1, w, g)
		}
	}
	return ""
}
