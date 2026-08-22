package project

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/phase"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func TestWithDiagnosticsSharesCompilerStateAndLock(t *testing.T) {
	ctx := New(".", ".peep", diagnostics.NewDiagnosticBag())
	scopedBag := ctx.Diagnostics.BeginPhase(phase.Typechecked, "main")
	scoped := ctx.WithDiagnostics(scopedBag)
	module := &Module{Key: "main"}
	scoped.AddModule(module)
	scoped.Diagnostics.Add(diagnostics.NewError("typed"))

	if got, ok := ctx.ModuleByKey("main"); !ok || got != module {
		t.Fatal("scoped context did not share module index")
	}
	if got := ctx.Diagnostics.Diagnostics(); len(got) != 1 || got[0].Message != "typed" {
		t.Fatalf("shared diagnostics = %#v", got)
	}
}

func TestPackagedLibraryBaseForExecutableUsesSiblingLibsDir(t *testing.T) {
	exePath := filepath.Join("/tmp", "peeper", "build", "bin", "peeper")
	got := packagedLibraryBaseForExecutable(exePath)
	want := filepath.Join("/tmp", "peeper", "build", "libs")
	if got != want {
		t.Fatalf("packaged library base = %q, want %q", got, want)
	}
}

func TestContextsKeepIndependentTargetSizedPredeclaredTypes(t *testing.T) {
	ctx32 := NewWithConfig(Config{RootDir: t.TempDir(), Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: "386"}, nil)
	ctx64 := NewWithConfig(Config{RootDir: t.TempDir(), Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: "amd64"}, nil)
	if ctx32.Target.PointerBits != 32 || ctx64.Target.PointerBits != 64 {
		t.Fatalf("target widths = %d, %d", ctx32.Target.PointerBits, ctx64.Target.PointerBits)
	}
	for _, tt := range []struct {
		ctx  *CompilerContext
		bits int
	}{
		{ctx: ctx32, bits: 32},
		{ctx: ctx64, bits: 64},
	} {
		sym, ok := tt.ctx.GlobalScope.Lookup("reserve")
		if !ok {
			t.Fatal("missing reserve predeclared symbol")
		}
		symType, found := symbols.GetSymbolType(sym)
		fn, ok := symType.(*typeinfo.FuncType)
		if !found || !ok || len(fn.Params) != 2 {
			t.Fatalf("reserve type = %#v", symType)
		}
		_, bits, ok := typeinfo.NumericInfo(fn.Params[1])
		if !ok || bits != tt.bits {
			t.Fatalf("reserve usize width = %d, want %d", bits, tt.bits)
		}
	}
}

func TestLibraryRootUsesNamespaceSubdirectory(t *testing.T) {
	ctx := NewWithConfig(Config{
		RootDir:        t.TempDir(),
		Extension:      peeper.SourceExt,
		LibraryBaseDir: filepath.Join("/tmp", "peeper", "build", "libs"),
	}, nil)

	got, ok := ctx.LibraryRoot("vendor")
	if !ok {
		t.Fatal("LibraryRoot() returned no root")
	}
	want := filepath.Join("/tmp", "peeper", "build", "libs", "vendor")
	if got != want {
		t.Fatalf("LibraryRoot(vendor) = %q, want %q", got, want)
	}
}

func TestModuleOriginForFileDetectsBundledLibrarySource(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	libraryFile := filepath.Join(libraryBase, "core", peeper.SourceDirName, "global"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(libraryFile), 0o755); err != nil {
		t.Fatalf("mkdir library dir: %v", err)
	}
	if err := os.WriteFile(libraryFile, []byte("const stdout: i32 = 1;\n"), 0o644); err != nil {
		t.Fatalf("write library file: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:        root,
		Extension:      peeper.SourceExt,
		LibraryBaseDir: libraryBase,
	}, nil)

	origin, namespace := ctx.ModuleOriginForFile(libraryFile)
	if origin != ModuleOriginStdlib {
		t.Fatalf("origin = %q, want %q", origin, ModuleOriginStdlib)
	}
	if namespace != "core" {
		t.Fatalf("namespace = %q, want %q", namespace, "core")
	}
}

func TestModuleOriginForFileLeavesProjectSourceLocal(t *testing.T) {
	root := t.TempDir()
	mainFile := filepath.Join(root, peeper.SourceDirName, "main"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(mainFile), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	if err := os.WriteFile(mainFile, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:        root,
		ProjectName:    "app",
		Extension:      peeper.SourceExt,
		LibraryBaseDir: filepath.Join(root, "libs"),
	}, nil)

	origin, namespace := ctx.ModuleOriginForFile(mainFile)
	if origin != ModuleOriginLocal {
		t.Fatalf("origin = %q, want %q", origin, ModuleOriginLocal)
	}
	if namespace != "" {
		t.Fatalf("namespace = %q, want empty", namespace)
	}
}
