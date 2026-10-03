package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/target"
	"compiler/internal/toolchain"
)

// Run the test executable as a controlled compiler subprocess without shell or
// production hooks. Normal test invocations still run through testing.M.
func TestMain(m *testing.M) {
	if os.Getenv("PEEPER_TEST_OBJECT_TOOL") == "1" && len(os.Args) > 1 && os.Args[1] == "-target" {
		if err := objectCompilerFixture(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(23)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func objectCompilerFixture() error {
	inputPath, outputPath := os.Args[len(os.Args)-3], os.Args[len(os.Args)-1]
	ir, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, []byte("partial"), 0o640); err != nil {
		return err
	}
	if err := os.WriteFile(inputPath+".trace", []byte(outputPath), 0o600); err != nil {
		return err
	}
	cachePath := os.Getenv("PEEPER_TEST_OBJECT_CACHE")
	mode := os.Getenv("PEEPER_TEST_OBJECT_MODE")
	switch mode {
	case "compiler-failure":
		return fmt.Errorf("fixture compiler failed after partial output")
	case "wait":
		if err := waitForObjectFile(inputPath + ".release"); err != nil {
			return err
		}
	case "directory-collision":
		if err := os.Mkdir(cachePath, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cachePath, "keep"), []byte("keep"), 0o600); err != nil {
			return err
		}
	case "winner", "winner-missing-output":
		// Another compiler publishes the same key after our initial cache miss.
		winner := outputPath + ".winner"
		if err := os.WriteFile(winner, append([]byte("object:"), ir...), 0o640); err != nil {
			return err
		}
		if err := os.Rename(winner, cachePath); err != nil {
			return err
		}
	}
	if mode == "missing-output" || mode == "winner-missing-output" {
		return os.Remove(outputPath)
	}
	return os.WriteFile(outputPath, append([]byte("object:"), ir...), 0o640)
}

func waitForObjectFile(path string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", path)
}

func objectCompileFixture(t *testing.T, root string) (*project.CompilerContext, toolchain.Profile, *module.Module, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := target.Host()
	ctx := &project.CompilerContext{Config: project.Config{RootDir: root, IsDebugBuild: true}}
	profile := toolchain.Profile{IsManaged: true, ClangPath: executable, TargetOS: host.OS, TargetArch: host.Arch, LLVMTriple: host.LLVMTriple, DebugFormat: "dwarf"}
	mod := &module.Module{ID: moduleid.ID{Origin: "local", ImportPath: "cache_test"}, LLVMIR: "\n define i32 @main() { ret i32 0 } \n"}
	t.Setenv("PEEPER_TEST_OBJECT_TOOL", "1")
	t.Setenv("PEEPER_TEST_OBJECT_MODE", "")
	t.Setenv("PEEPER_TEST_OBJECT_CACHE", objectCachePath(ctx, profile, strings.TrimSpace(mod.LLVMIR)))
	return ctx, profile, mod, filepath.Join(t.TempDir(), "mod_0")
}

func TestValidateNativeLinkTarget(t *testing.T) {
	if err := validateNativeLinkTarget(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("host target rejected: %v", err)
	}

	targetOS := "linux"
	if runtime.GOOS == targetOS {
		targetOS = "windows"
	}
	if err := validateNativeLinkTarget(targetOS, runtime.GOARCH); err == nil {
		t.Fatalf("non-host target %s/%s accepted", targetOS, runtime.GOARCH)
	}
}

func TestReplacePathReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, "staged")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replacePath(staged, target); err != nil {
		t.Fatalf("replacePath() error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("replaced file = %q", data)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged path still exists: %v", err)
	}
}

func TestCompileObjectLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "unmanaged", "cache-hit", "compiler-failure", "missing-output", "directory-collision", "winner", "winner-missing-output"} {
		t.Run(mode, func(t *testing.T) {
			ctx, profile, mod, base := objectCompileFixture(t, t.TempDir())
			if mode == "unmanaged" {
				profile.IsManaged = false
			}
			cachePath := objectCachePath(ctx, profile, strings.TrimSpace(mod.LLVMIR))
			want := "object:" + strings.TrimSpace(mod.LLVMIR)
			t.Setenv("PEEPER_TEST_OBJECT_MODE", mode)
			if mode == "cache-hit" {
				if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cachePath, []byte(want), 0o640); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PEEPER_TEST_OBJECT_MODE", "compiler-failure")
			}
			objectPath, err := compileObject(ctx, profile, mod, base)
			switch mode {
			case "compiler-failure":
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 || !strings.Contains(err.Error(), "compile LLVM module cache_test") || !strings.Contains(err.Error(), "fixture compiler failed") {
					t.Fatalf("compiler error = %v, want wrapped exit 23 with module and tool output", err)
				}
			case "missing-output", "directory-collision":
				var linkErr *os.LinkError
				if !errors.As(err, &linkErr) || !strings.Contains(err.Error(), "publish cached object") {
					t.Fatalf("publish error = %v, want wrapped LinkError", err)
				}
				if mode == "missing-output" && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("publish error lost missing-file identity: %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				wantPath := cachePath
				if mode == "unmanaged" {
					wantPath = base + ".o"
				}
				if objectPath != wantPath {
					t.Fatalf("object path = %q, want %q", objectPath, wantPath)
				}
				data, err := os.ReadFile(objectPath)
				if err != nil || string(data) != want {
					t.Fatalf("object = %q, %v; want %q", data, err, want)
				}
				info, err := os.Stat(objectPath)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
					t.Fatalf("object mode = %o, want 640", info.Mode().Perm())
				}
			}
			if mode == "cache-hit" {
				if _, err := os.Stat(base + ".ll"); !os.IsNotExist(err) {
					t.Fatalf("cache hit wrote LLVM input: %v", err)
				}
				return
			}
			trace, err := os.ReadFile(base + ".ll.trace")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unmanaged" {
				if string(trace) != base+".o" {
					t.Fatalf("uncached compiler output = %q", trace)
				}
				return
			}
			stageDir := filepath.Dir(string(trace))
			if filepath.Dir(stageDir) != filepath.Dir(cachePath) {
				t.Fatalf("stage %q not on cache filesystem beside %q", stageDir, cachePath)
			}
			if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
				t.Fatalf("compileObject left stage directory: %v", err)
			}
			entries, err := os.ReadDir(filepath.Dir(cachePath))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "compiler-failure" || mode == "missing-output" {
				if len(entries) != 0 {
					t.Fatalf("failed compilation/publication left cache entries: %v", entries)
				}
			} else if len(entries) != 1 || entries[0].Name() != filepath.Base(cachePath) {
				t.Fatalf("unexpected cache entries: %v", entries)
			}
			if mode == "directory-collision" {
				if data, err := os.ReadFile(filepath.Join(cachePath, "keep")); err != nil || string(data) != "keep" {
					t.Fatalf("publish failure damaged destination: %q, %v", data, err)
				}
			}
		})
	}
}

func TestCompileObjectRejectsPreexistingDirectory(t *testing.T) {
	ctx, profile, mod, base := objectCompileFixture(t, t.TempDir())
	cachePath := objectCachePath(ctx, profile, strings.TrimSpace(mod.LLVMIR))
	// Unlike directory-collision, this entry exists before the initial lookup.
	if err := os.MkdirAll(cachePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cachePath, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	objectPath, err := compileObject(ctx, profile, mod, base)
	if err == nil || objectPath != "" {
		t.Fatalf("compileObject() = %q, %v; want empty path and invalid-cache error", objectPath, err)
	}
	for _, text := range []string{"inspect cached object for " + mod.ID.ImportPath, cachePath, "not a regular file"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("cache error = %v, want %q", err, text)
		}
	}
	if data, err := os.ReadFile(filepath.Join(cachePath, "keep")); err != nil || string(data) != "keep" {
		t.Fatalf("cache inspection damaged destination: %q, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(cachePath))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(cachePath) || !entries[0].IsDir() {
		t.Fatalf("cache inspection changed cache entries: %v, %v", entries, err)
	}
	entries, err = os.ReadDir(filepath.Dir(base))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache inspection wrote compiler artifacts: %v, %v", entries, err)
	}
}

func TestCompileObjectRejectsPreexistingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows CI does not guarantee symlink privileges")
	}
	ctx, profile, mod, base := objectCompileFixture(t, t.TempDir())
	cachePath := objectCachePath(ctx, profile, strings.TrimSpace(mod.LLVMIR))
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(t.TempDir(), "object.o")
	if err := os.WriteFile(targetPath, []byte("not cached"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, cachePath); err != nil {
		t.Fatal(err)
	}

	objectPath, err := compileObject(ctx, profile, mod, base)
	if err == nil || objectPath != "" {
		t.Fatalf("compileObject() = %q, %v; want empty path and invalid-cache error", objectPath, err)
	}
	for _, text := range []string{"inspect cached object for " + mod.ID.ImportPath, cachePath, "not a regular file"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("cache error = %v, want %q", err, text)
		}
	}
	info, err := os.Lstat(cachePath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("cache inspection changed symlink: %v, %v", info, err)
	}
	if data, err := os.ReadFile(targetPath); err != nil || string(data) != "not cached" {
		t.Fatalf("cache inspection changed symlink target: %q, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(base))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache inspection wrote compiler artifacts: %v, %v", entries, err)
	}
}

func TestCompileObjectConcurrentReaders(t *testing.T) {
	ctx, profile, mod, base := objectCompileFixture(t, t.TempDir())
	t.Setenv("PEEPER_TEST_OBJECT_MODE", "wait")
	cachePath := objectCachePath(ctx, profile, strings.TrimSpace(mod.LLVMIR))
	var compilers sync.WaitGroup
	results := []chan error{make(chan error, 1), make(chan error, 1)}
	for i := range results {
		compilers.Add(1)
		go func() {
			defer compilers.Done()
			_, err := compileObject(ctx, profile, mod, fmt.Sprintf("%s_%d", base, i))
			results[i] <- err
		}()
	}
	t.Cleanup(func() {
		for i := range results {
			_ = os.WriteFile(fmt.Sprintf("%s_%d.ll.release", base, i), nil, 0o600)
		}
		compilers.Wait()
	})
	for i := range results {
		if err := waitForObjectFile(fmt.Sprintf("%s_%d.ll.trace", base, i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("partial compiler output visible in cache: %v", err)
	}
	if err := os.WriteFile(base+"_0.ll.release", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-results[0]; err != nil {
		t.Fatal(err)
	}
	publishedInfo, err := os.Lstat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	stopReader := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		for {
			data, err := os.ReadFile(cachePath)
			if err != nil || string(data) != "object:"+strings.TrimSpace(mod.LLVMIR) {
				readerDone <- fmt.Errorf("cache reader observed %q, %v", data, err)
				return
			}
			select {
			case <-stopReader:
				readerDone <- nil
				return
			default:
			}
		}
	}()
	t.Cleanup(func() {
		close(stopReader)
		if err := <-readerDone; err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(base+"_1.ll.release", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-results[1]; err != nil {
		t.Fatal(err)
	}
	currentInfo, err := os.Lstat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(publishedInfo, currentInfo) {
		t.Fatal("second publisher replaced the published cache object")
	}
	entries, err := os.ReadDir(filepath.Dir(cachePath))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(cachePath) || entries[0].IsDir() {
		t.Fatalf("duplicate publisher left cache entries: %v, %v", entries, err)
	}
}

func TestCompileObjectAcrossFilesystems(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cross-filesystem fixture requires Linux /dev/shm")
	}
	cacheRoot, err := os.MkdirTemp("/dev/shm", "peeper-object-cache-")
	if err != nil {
		t.Skipf("/dev/shm unavailable: %v", err)
	}
	defer os.RemoveAll(cacheRoot)

	ctx, profile, mod, base := objectCompileFixture(t, cacheRoot)
	sourcePath := base + ".probe"
	if err := os.WriteFile(sourcePath, []byte("object"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sourcePath, filepath.Join(cacheRoot, "probe")); err == nil {
		t.Skip("test temporary directory and /dev/shm share a filesystem")
	} else if !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("cross-filesystem probe = %v, want EXDEV", err)
	}
	objectPath, err := compileObject(ctx, profile, mod, base)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(objectPath); err != nil || string(data) != "object:"+strings.TrimSpace(mod.LLVMIR) {
		t.Fatalf("cached object = %q, %v", data, err)
	}
}
