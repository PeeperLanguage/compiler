package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCompileObjectReusesLockedWinner(t *testing.T) {
	ctx, profile, mod, base := objectCompileFixture(t, t.TempDir())
	t.Setenv("PEEPER_TEST_OBJECT_MODE", "wait")
	cachePath := objectCachePath(ctx, profile, strings.TrimSpace(mod.LLVMIR))
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := compileObject(ctx, profile, mod, base)
		result <- err
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(base+".ll.release", nil, 0o600)
		<-done
	})
	if err := waitForObjectFile(base + ".ll.trace"); err != nil {
		t.Fatal(err)
	}
	// Model a linker holding a competing publisher's complete object without
	// FILE_SHARE_DELETE. Windows must reject replacing this open destination.
	want := "object:" + strings.TrimSpace(mod.LLVMIR)
	if err := os.WriteFile(cachePath, []byte(want), 0o640); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	probe := filepath.Join(filepath.Dir(cachePath), "replacement-probe.o")
	if err := os.WriteFile(probe, []byte(want), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(probe, cachePath); err == nil {
		t.Fatal("replacement probe succeeded despite locked destination")
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".ll.release", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("failed to reuse locked winner: %v", err)
	}
	if data, err := os.ReadFile(cachePath); err != nil || string(data) != want {
		t.Fatalf("winner changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(cachePath))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(cachePath) {
		t.Fatalf("duplicate stage not cleaned: %v, %v", entries, err)
	}
}
