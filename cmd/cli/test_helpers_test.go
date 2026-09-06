package cli

import (
	"os"
	"testing"
)

// chdirForTest is the Go 1.23 equivalent of testing.T.Chdir. Keep working
// directory mutation behind one helper so tests restore process state even
// when they fail.
func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory %s: %v", previous, err)
		}
	})
}
