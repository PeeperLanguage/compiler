package toolchains

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxToolchainValidationRejectsIncompleteTrees(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		want  string
	}{
		{
			name: "missing executable",
			want: "required managed Linux tool missing",
		},
		{
			name: "missing sysroot object",
			setup: func(t *testing.T, root string) {
				writeToolchainTestTools(t, root, "#!/bin/sh\nexit 0\n")
			},
			want: "required static sysroot file missing",
		},
		{
			name: "missing resource headers",
			setup: func(t *testing.T, root string) {
				resourceDir := filepath.Join(root, "lib", "clang", "23")
				writeToolchainTestTools(t, root, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", resourceDir))
				for _, object := range []string{"libc.a", "crt1.o", "crti.o", "crtn.o", "libclang_rt.builtins.a"} {
					path := filepath.Join(root, "sysroot", "lib", object)
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "clang resource headers missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.setup != nil {
				test.setup(t, root)
			}
			command := exec.Command("bash", "build-linux.sh", "validate", "amd64", root)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("validation output = %q, error = %v; want %q", output, err, test.want)
			}
		})
	}
}

func writeToolchainTestTools(t *testing.T, root, clangScript string) {
	t.Helper()
	tools := map[string]string{
		"clang":   clangScript,
		"ld.lld":  "#!/bin/sh\nexit 0\n",
		"llvm-ar": "#!/bin/sh\nexit 0\n",
	}
	for name, contents := range tools {
		path := filepath.Join(root, "bin", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}
