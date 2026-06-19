package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type benchFixture struct {
	root        string
	entry       string
	leaf        string
	unrelated   string
	entryBody   string
	leafBody    string
	entryImport string
}

func BenchmarkIncrementalWorkspace(b *testing.B) {
	for _, fixtureName := range []string{"small", "medium", "large"} {
		fixture := createBenchFixture(b, fixtureName)
		b.Run(fixtureName, func(b *testing.B) {
			runBenchCase(b, "cold_compile", fixture, func(state *ServerState) string {
				state.Cache = map[string]string{}
				return fixture.entry
			})
			runBenchCase(b, "warm_no_change_open", fixture, func(state *ServerState) string {
				state.Cache = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				return fixture.entry
			})
			runBenchCase(b, "body_only_edit", fixture, func(state *ServerState) string {
				state.Cache = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				state.Cache[fixture.leaf] = fixture.leafBody + "\nfn local_detail() -> i32 { let x = 1; return x; }\n"
				return fixture.leaf
			})
			runBenchCase(b, "export_shape_edit", fixture, func(state *ServerState) string {
				state.Cache = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				state.Cache[fixture.leaf] = "fn LeafValue(v: i32) -> i32 { return v; }\n"
				return fixture.leaf
			})
			runBenchCase(b, "import_set_edit", fixture, func(state *ServerState) string {
				state.Cache = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				state.Cache[fixture.entry] = fixture.entryImport + "import \"extra\";\n" + fixture.entryBody
				return fixture.entry
			})
			runBenchCase(b, "unrelated_component_edit", fixture, func(state *ServerState) string {
				state.Cache = map[string]string{}
				_, _ = state.recompile(fixture.entry)
				_, _ = state.recompile(fixture.unrelated)
				state.Cache[fixture.unrelated] = "fn main() -> i32 { return 2; }\n"
				return fixture.unrelated
			})
			runBenchCase(b, "multi_main_first_root", fixture, func(state *ServerState) string {
				state.Cache = map[string]string{}
				return fixture.entry
			})
			runBenchCase(b, "multi_main_second_root", fixture, func(state *ServerState) string {
				state.Cache = map[string]string{}
				return fixture.unrelated
			})
		})
	}
}

func runBenchCase(b *testing.B, name string, fixture benchFixture, prepare func(*ServerState) string) {
	b.Run(name, func(b *testing.B) {
		var totalParsed, totalReused, totalDowngraded, totalAdvances float64
		for i := 0; i < b.N; i++ {
			state := NewServerState()
			state.RootDir = fixture.root
			target := prepare(state)
			b.StartTimer()
			_, _ = state.recompile(target)
			b.StopTimer()
			metrics := state.LastMetrics
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
	depth := map[string]int{
		"small":  4,
		"medium": 12,
		"large":  24,
	}[size]
	if depth == 0 {
		tb.Fatalf("unknown fixture size %q", size)
	}

	writeBenchWorkspaceFile(tb, filepath.Join(root, "extra.peep"), "fn Extra() -> i32 { return 9; }\n")
	unrelated := filepath.Join(root, "other.peep")
	writeBenchWorkspaceFile(tb, unrelated, "fn main() -> i32 { return 1; }\n")

	leaf := filepath.Join(root, fmt.Sprintf("chain_%02d.peep", depth-1))
	writeBenchWorkspaceFile(tb, leaf, "fn LeafValue() -> i32 { return 1; }\n")
	for i := depth - 2; i >= 0; i-- {
		path := filepath.Join(root, fmt.Sprintf("chain_%02d.peep", i))
		nextImport := fmt.Sprintf("chain_%02d", i+1)
		nextCall := "LeafValue"
		if i+1 < depth-1 {
			nextCall = fmt.Sprintf("Chain%02d", i+1)
		}
		writeBenchWorkspaceFile(tb, path, fmt.Sprintf("import %q;\nfn Chain%02d() -> i32 { return %s::%s(); }\n", nextImport, i, nextImport, nextCall))
	}

	entry := filepath.Join(root, "main.peep")
	entryImport := "import \"chain_00\";\n"
	entryBody := "fn main() -> i32 {\n\treturn chain_00::Chain00();\n}\n"
	writeBenchWorkspaceFile(tb, entry, entryImport+entryBody)
	return benchFixture{
		root:        root,
		entry:       entry,
		leaf:        leaf,
		unrelated:   unrelated,
		entryBody:   entryBody,
		leafBody:    "fn LeafValue() -> i32 { return 1; }\n",
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
