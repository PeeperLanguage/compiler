package main

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCompileObjectNeverMovesPublishedCache(t *testing.T) {
	ctx, profile, mod, base := objectCompileFixture(t, t.TempDir())
	cachePath := objectCachePath(ctx, profile, strings.TrimSpace(mod.LLVMIR))
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	watch, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(watch)
	if _, err := syscall.InotifyAddWatch(watch, filepath.Dir(cachePath), syscall.IN_MOVED_FROM|syscall.IN_DELETE); err != nil {
		t.Fatal(err)
	}
	// The fixture publishes a winner after our cache miss, before our publication.
	// Watching directory events catches even a gap too short for polling readers.
	t.Setenv("PEEPER_TEST_OBJECT_MODE", "winner")
	if _, err := compileObject(ctx, profile, mod, base); err != nil {
		t.Fatal(err)
	}
	var events [65536]byte
	n, err := syscall.Read(watch, events[:])
	if errors.Is(err, syscall.EAGAIN) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < n; {
		nameLen := int(binary.NativeEndian.Uint32(events[offset+12 : offset+16]))
		name := strings.TrimRight(string(events[offset+16:offset+16+nameLen]), "\x00")
		if name == filepath.Base(cachePath) {
			t.Fatalf("published cache object was moved away or deleted: %s", name)
		}
		offset += syscall.SizeofInotifyEvent + nameLen
	}
}
