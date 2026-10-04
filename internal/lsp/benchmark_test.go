package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

type benchFixture struct {
	root        string
	entry       string
	leaf        string
	unrelated   string
	entryBody   string
	entryImport string
}

func BenchmarkIncrementalWorkspace(b *testing.B) {
	for _, fixtureName := range []string{"small", "medium", "large"} {
		fixture := createBenchFixture(b, fixtureName)
		b.Run(fixtureName, func(b *testing.B) {
			runBenchCase(b, "cold_compile", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				return fixture.entry
			})
			runBenchCase(b, "warm_no_change_open", fixture, func(state *ServerState) string {
				sourceText := fixture.entryImport + fixture.entryBody
				state.applyDocumentSnapshot(fixture.entry, &sourceText, nil)
				_, _ = state.recompile(fixture.entry)
				return fixture.entry
			})
			runBenchCase(b, "function_body_edit", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				updated := "const leafLimit: i32 = 1;\nfn LeafValue() -> i32 { return 2; }\nfn StableValue() -> i32 { return 7; }\n"
				state.applyDocumentSnapshot(fixture.leaf, &updated, nil)
				return fixture.entry
			})
			runBenchCase(b, "earlier_function_growth", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				updated := "const leafLimit: i32 = 1;\nfn LeafValue() -> i32 {\n\tlet value = leafLimit;\n\treturn value;\n}\nfn StableValue() -> i32 { return 7; }\n"
				state.applyDocumentSnapshot(fixture.leaf, &updated, nil)
				return fixture.entry
			})
			runBenchCase(b, "private_constant_edit", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				updated := "const leafLimit: i32 = 2;\nfn LeafValue() -> i32 { return leafLimit; }\nfn StableValue() -> i32 { return 7; }\n"
				state.applyDocumentSnapshot(fixture.leaf, &updated, nil)
				return fixture.entry
			})
			runBenchCase(b, "export_shape_edit", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				updated := "const leafLimit: i32 = 1;\nfn LeafValue() -> i32 { return leafLimit; }\nfn StableValue() -> i32 { return 7; }\nfn AddedValue() -> i32 { return 9; }\n"
				state.applyDocumentSnapshot(fixture.leaf, &updated, nil)
				return fixture.entry
			})
			runBenchCase(b, "import_set_edit", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				updated := fixture.entryImport + "import \"bench/extra\";\n" + fixture.entryBody
				state.applyDocumentSnapshot(fixture.entry, &updated, nil)
				return fixture.entry
			})
			runBenchCase(b, "unrelated_component_edit", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				_, _ = state.recompile(fixture.unrelated)
				updated := "fn main() -> i32 { return 2; }\n"
				state.applyDocumentSnapshot(fixture.unrelated, &updated, nil)
				return fixture.unrelated
			})
			runBenchCase(b, "multi_main_first_root", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				return fixture.entry
			})
			runBenchCase(b, "multi_main_second_root", fixture, func(state *ServerState) string {
				state.SourceOverrides = map[string]string{}
				return fixture.unrelated
			})
		})
	}
}

func runBenchCase(b *testing.B, name string, fixture benchFixture, prepare func(*ServerState) string) {
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		var totalParsed, totalReused, totalDowngraded, totalAdvances float64
		for range b.N {
			state := NewServerState()
			state.RootDir = fixture.root
			target := prepare(state)
			b.StartTimer()
			ctx, _ := state.recompile(target)
			b.StopTimer()
			if ctx == nil || ctx.Diagnostics == nil {
				b.Fatal("incremental compile returned no diagnostics")
			}
			if ctx.Diagnostics.HasErrors() {
				b.Fatalf("incremental compile failed:\n%s", ctx.Diagnostics.EmitAllToString())
			}
			metrics := &state.LastMetrics
			totalParsed += float64(metrics.ModulesParsed)
			totalReused += float64(metrics.ModulesReused)
			totalDowngraded += float64(metrics.ModulesDowngraded)
			totalAdvances += float64(metrics.PhaseAdvances)
		}
		b.ReportMetric(totalParsed/float64(b.N), "modules_parsed/op")
		b.ReportMetric(totalReused/float64(b.N), "modules_reused/op")
		b.ReportMetric(totalDowngraded/float64(b.N), "modules_downgraded/op")
		b.ReportMetric(totalAdvances/float64(b.N), "phase_advances/op")
	})
}

func createBenchFixture(tb testing.TB, size string) benchFixture {
	tb.Helper()
	root := tb.TempDir()
	writeBenchWorkspaceFile(tb, filepath.Join(root, manifest.FileName), "name = \"bench\"\nbuild = \"program\"\n")
	sourceRoot := filepath.Join(root, peeper.SourceDirName)
	depth := map[string]int{
		"small":  4,
		"medium": 12,
		"large":  24,
	}[size]
	if depth == 0 {
		tb.Fatalf("unknown fixture size %q", size)
	}

	writeBenchWorkspaceFile(tb, filepath.Join(sourceRoot, "extra"+peeper.SourceExt), "fn Extra() -> i32 { return 9; }\n")
	unrelated := filepath.Join(sourceRoot, "other"+peeper.SourceExt)
	writeBenchWorkspaceFile(tb, unrelated, "fn main() -> i32 { return 1; }\n")

	leaf := filepath.Join(sourceRoot, fmt.Sprintf("chain_%02d%s", depth-1, peeper.SourceExt))
	leafBody := "const leafLimit: i32 = 1;\nfn LeafValue() -> i32 { return leafLimit; }\nfn StableValue() -> i32 { return 7; }\n"
	writeBenchWorkspaceFile(tb, leaf, leafBody)
	for i := depth - 2; i >= 0; i-- {
		path := filepath.Join(sourceRoot, fmt.Sprintf("chain_%02d%s", i, peeper.SourceExt))
		nextImport := fmt.Sprintf("chain_%02d", i+1)
		nextCall := "LeafValue"
		if i+1 < depth-1 {
			nextCall = fmt.Sprintf("Chain%02d", i+1)
		}
		writeBenchWorkspaceFile(tb, path, fmt.Sprintf("import %q;\nfn Chain%02d() -> i32 { return %s::%s(); }\n", "bench/"+nextImport, i, nextImport, nextCall))
	}

	entry := filepath.Join(sourceRoot, peeper.MainFileName)
	entryImport := "import \"bench/chain_00\";\n"
	entryBody := "fn main() -> i32 {\n\treturn chain_00::Chain00();\n}\n"
	writeBenchWorkspaceFile(tb, entry, entryImport+entryBody)
	return benchFixture{
		root:        root,
		entry:       entry,
		leaf:        leaf,
		unrelated:   unrelated,
		entryBody:   entryBody,
		entryImport: entryImport,
	}
}

func writeBenchWorkspaceFile(tb testing.TB, path, content string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatalf("write %s: %v", path, err)
	}
}