package main

import (
	"bytes"
	"os"
	"testing"
)

// TestGeneratedFileIsCurrent fails when nodes.go changed but nodes_gen.go was
// not written again.
func TestGeneratedFileIsCurrent(t *testing.T) {
	current, err := os.ReadFile("../nodes_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, generate(read("../nodes.go"))) {
		t.Fatal("nodes_gen.go is out of date: run go generate ./internal/frontend/ast")
	}
}
