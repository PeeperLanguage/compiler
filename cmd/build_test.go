package main

import (
	"reflect"
	"testing"

	"compiler/internal/project"
)

func TestClangArgsForBuildRelease(t *testing.T) {
	args := clangArgsForBuild(project.Config{TargetOS: "linux"}, "x86_64-unknown-linux-gnu", []string{"a.ll", "b.ll"}, "demo")
	want := []string{"-target", "x86_64-unknown-linux-gnu", "-x", "ir", "a.ll", "-x", "ir", "b.ll", "-o", "demo"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("clang args = %#v, want %#v", args, want)
	}
}

func TestClangArgsForBuildDebugUnix(t *testing.T) {
	args := clangArgsForBuild(project.Config{TargetOS: "linux", BuildDebug: true}, "x86_64-unknown-linux-gnu", []string{"a.ll"}, "demo")
	want := []string{"-target", "x86_64-unknown-linux-gnu", "-O0", "-g", "-x", "ir", "a.ll", "-o", "demo"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("clang args = %#v, want %#v", args, want)
	}
}

func TestClangArgsForBuildDebugWindows(t *testing.T) {
	args := clangArgsForBuild(project.Config{TargetOS: "windows", BuildDebug: true}, "x86_64-pc-windows-msvc", []string{"a.ll"}, "demo.exe")
	want := []string{"-target", "x86_64-pc-windows-msvc", "-O0", "-gcodeview", "-x", "ir", "a.ll", "-o", "demo.exe"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("clang args = %#v, want %#v", args, want)
	}
}
