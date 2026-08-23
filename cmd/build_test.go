package main

import (
	"reflect"
	"runtime"
	"testing"

	"compiler/internal/project"
)

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
