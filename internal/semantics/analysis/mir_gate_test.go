package analysis_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	compiler "compiler/internal/driver"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/project"
	"compiler/internal/semantics/analysis"
	"compiler/internal/semantics/symbols"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

// TestMIREffectGate measures whether MIR says what the effect stream says:
// for every passing fixture it describes each block's effects from THIR and
// from MIR, and reports where they differ. It is the Stage 4 measurement of
// checks-on-mir and changes no production code. Run with PEEPER_GATE=1 after
// `go run ./scripts/bundle.go`.
func TestMIREffectGate(t *testing.T) {
	if os.Getenv("PEEPER_GATE") == "" {
		t.Skip("set PEEPER_GATE=1 to measure MIR against the effect stream")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := filepath.Glob(filepath.Join(repo, "x_test", "*", "peeper.toml"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	sort.Strings(manifests)

	var functions, equal, blocks, equalBlocks int
	differences := make(map[string][]string)
	for _, manifestPath := range manifests {
		dir := filepath.Dir(manifestPath)
		data, _ := os.ReadFile(manifestPath)
		if strings.Contains(string(data), `outcome = "failure"`) {
			continue
		}
		entry := filepath.Join(dir, peeper.SourceDirName, peeper.MainFileName)
		sourceProject, err := manifest.ResolveSourceFileProject(entry)
		if err != nil {
			continue
		}
		ctx := compiler.NewCompilerContext(project.Config{
			RootDir: sourceProject.RootDir, ProjectName: sourceProject.ProjectName,
			Extension: peeper.SourceExt, LibraryBaseDir: filepath.Join(repo, "build", "libs"),
		}, diagnostics.NewDiagnosticBag())
		compiler.CompileFile(ctx, entry, nil)
		if ctx.Diagnostics.HasErrors() {
			continue
		}
		for _, module := range ctx.Modules() {
			if module.MIR == nil || module.THIR == nil || module.CFG == nil {
				continue
			}
			streams := analysis.BuildEffectsForTest(module.THIR, module.CFG)
			mirFunctions := 0
			for _, source := range module.THIR.Functions {
				if source == nil || source.Symbol == nil {
					continue
				}
				fn := module.MIR.Funcs[mirFunctions]
				mirFunctions++
				graph := module.CFG.FunctionByID(source.Identity)
				if source.Body == nil || graph == nil || fn.Blocks == nil {
					continue
				}
				functions++
				same := true
				fromMIR := mirBlockOps(fn, module.MIR.Types)
				for _, block := range graph.Blocks {
					if block == nil || !block.IsReachable || block == graph.Exit {
						continue
					}
					want := make([]string, 0)
					if block == graph.Entry {
						for _, parameter := range source.Params {
							if parameter.Symbol != nil {
								want = append(want, "define "+parameter.Symbol.Name)
							}
						}
					}
					for _, site := range block.Sites {
						for _, op := range streams[graph.FunctionID][site.ID] {
							if define, isDefine := op.(analysis.DefineForTest); isDefine && define.IsOnEntry {
								continue
							}
							// A function name is not a variable; MIR reads it as a callee.
							if use, isUse := op.(analysis.UseForTest); isUse && use.Place.Root != nil && (use.Place.Root.Kind == symbols.SymbolFunc || use.Place.Root.Kind == symbols.SymbolMethod) {
								continue
							}
							want = append(want, describe(op))
						}
					}
					got := fromMIR[block.ID]
					if os.Getenv("PEEPER_GATE_NO_CALLS") != "" {
						want, got = withoutCallBrackets(want), withoutCallBrackets(got)
					}
					blocks++
					if strings.Join(want, "; ") == strings.Join(got, "; ") {
						equalBlocks++
						continue
					}
					same = false
					key := filepath.Base(dir)
					differences[key] = append(differences[key], fmt.Sprintf("%s b%d\n    effects: %s\n    mir:     %s",
						fn.Name, block.ID, strings.Join(want, "; "), strings.Join(got, "; ")))
				}
				if same {
					equal++
				}
			}
		}
	}

	keys := make([]string, 0, len(differences))
	for key := range differences {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var report strings.Builder
	fmt.Fprintf(&report, "functions: %d compared, %d equal; blocks: %d compared, %d equal\n", functions, equal, blocks, equalBlocks)
	for _, key := range keys {
		fmt.Fprintf(&report, "== %s\n", key)
		for _, difference := range differences[key] {
			fmt.Fprintf(&report, "  %s\n", difference)
		}
	}
	if out := os.Getenv("PEEPER_GATE_REPORT"); out != "" {
		if err := os.WriteFile(out, []byte(report.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(strings.SplitN(report.String(), "\n", 2)[0])
}

var temporaryName = regexp.MustCompile(`^t[0-9]+$`)

func withoutCallBrackets(ops []string) []string {
	kept := make([]string, 0, len(ops))
	for _, op := range ops {
		if op != "call" && op != "end" {
			kept = append(kept, op)
		}
	}
	return kept
}

// sourceName is the variable a MIR name stores, read from the name itself:
// `item$3` is `item`, `$orelse.8$8_3_0_4` is `$orelse.8`. Temporaries, statics
// and callables are not variables. Guessing from the name is acceptable only
// in this measurement; a production reader would carry the symbol.
func sourceName(name string) (string, bool) {
	cut := strings.LastIndex(name, "$")
	if temporaryName.MatchString(name) || strings.HasPrefix(name, "@") || cut <= 0 {
		return "", false
	}
	return name[:cut], true
}

// mirBlockOps describes each MIR block in the words describe() uses for
// effects, in instruction order. A call begins at the first instruction that
// feeds one of its arguments, found by following temporaries back.
func mirBlockOps(fn *mir.Function, types *ir.TypeTable) map[int][]string {
	ops := make(map[int][]string, len(fn.Blocks))
	defined := make(map[string]bool)
	for _, parameter := range fn.Params {
		if name, ok := sourceName(parameter.Name); ok {
			ops[fn.EntryID] = append(ops[fn.EntryID], "define "+name)
			defined[parameter.Name] = true
		}
	}
	used := readTemporaries(fn, types)
	for _, block := range fn.Blocks {
		at := make([][]string, len(block.Instrs))
		begins := make([]int, len(block.Instrs))
		producer := make(map[string]int)
		// ZeroValue has one producer: a `let` without a value.
		zero := make(map[string]bool)
		for index, instruction := range block.Instrs {
			emit := func(text string) { at[index] = append(at[index], text) }
			root := func(verb string, place *mir.Place) {
				if place == nil {
					return
				}
				if ref, isName := place.Root.(*mir.RefName); isName {
					if name, ok := sourceName(ref.Name); ok {
						emit(verb + " " + name)
						return
					}
				}
				emit(verb + " temporary")
			}
			call := func() {
				begins[firstFeeding(block.Instrs, producer, index, types)]++
				reborrow(at, block.Instrs, producer, instruction, types)
				emit("end")
			}
			switch node := instruction.(type) {
			case *mir.Assign:
				switch value := node.Value.(type) {
				case *mir.Move:
					if ref, isName := value.Src.(*mir.RefName); isName {
						if name, ok := sourceName(ref.Name); ok {
							emit("use " + name)
						}
					}
				case *mir.Load:
					root("use", value.Place)
				case *mir.AddrOf:
					root("borrow", value.Place)
				case *mir.SliceView:
					root("borrow", value.Source)
				case *mir.Call, *mir.InterfaceCall:
					call()
				// These come only from built-in calls (alloc, len, as_chars,
				// from_bytes, dynamic-array functions).
				case *mir.Alloc, *mir.Len, *mir.StringChars, *mir.StringFromBytes, *mir.DynamicArrayAlloc:
					call()
				}
				if temporaryName.MatchString(node.Name) {
					producer[node.Name] = index
					_, zero[node.Name] = node.Value.(*mir.ZeroValue)
					// A temporary nothing reads but a Drop is a statement's
					// thrown-away value.
					if !used[node.Name] {
						emit("discard")
					}
				}
				if name, ok := sourceName(node.Name); ok {
					var source *mir.RefName
					if move, isMove := node.Value.(*mir.Move); isMove {
						source, _ = move.Src.(*mir.RefName)
					}
					switch {
					case defined[node.Name]:
						emit("write " + name)
					case source != nil && zero[source.Name]:
						emit("declare " + name)
					default:
						emit("define " + name)
					}
					defined[node.Name] = true
				}
			case *mir.Store:
				root("write", node.Place)
			case *mir.Call, *mir.InterfaceCall, *mir.DynamicArrayOp:
				call()
				emit("discard")
			case *mir.Print:
				emit("discard")
			}
		}
		for index := range block.Instrs {
			for range begins[index] {
				ops[block.ID] = append(ops[block.ID], "call")
			}
			ops[block.ID] = append(ops[block.ID], at[index]...)
		}
	}
	return ops
}

// reborrow rewrites `use r` to `borrow r` for each argument read from a
// variable into a reference parameter: a call borrows such an argument, as the
// type checker records for every reference parameter.
func reborrow(at [][]string, instructions []mir.Instr, producer map[string]int, instruction mir.Instr, types *ir.TypeTable) {
	var value any = instruction
	if assign, ok := instruction.(*mir.Assign); ok {
		value = assign.Value
	}
	var borrowed []mir.ValueRef
	switch call := value.(type) {
	case *mir.Call:
		callee, _ := types.Type(call.Callee.TypeID())
		for index, arg := range call.Args {
			if param, _ := types.Type(callee.Params[index]); param.Kind == ir.TypeReference {
				borrowed = append(borrowed, arg)
			}
		}
	case *mir.InterfaceCall:
		// The slot's own receiver is a raw data pointer; ConsumesBase says
		// whether the method takes self by value.
		if !call.ConsumesBase {
			borrowed = append(borrowed, call.Base)
		}
	// Built-ins that take their operand by reference.
	case *mir.Len:
		borrowed = append(borrowed, call.Value)
	case *mir.StringChars:
		borrowed = append(borrowed, call.Value)
	}
	for _, arg := range borrowed {
		ref, isName := arg.(*mir.RefName)
		if !isName {
			continue
		}
		from, produced := producer[ref.Name]
		// A reference narrowed from `&mut` to `&` passes through a cast.
		for produced {
			cast, isCast := instructions[from].(*mir.Assign).Value.(*mir.Cast)
			if !isCast {
				break
			}
			source, isName := cast.Arg.(*mir.RefName)
			if !isName {
				break
			}
			from, produced = producer[source.Name]
		}
		if !produced {
			continue
		}
		for i, op := range at[from] {
			if strings.HasPrefix(op, "use ") {
				at[from][i] = "borrow " + strings.TrimPrefix(op, "use ")
			}
		}
	}
}

// readTemporaries lists the temporaries some instruction other than a Drop,
// or a terminator, reads.
func readTemporaries(fn *mir.Function, types *ir.TypeTable) map[string]bool {
	used := make(map[string]bool)
	read := func(text string) {
		for _, temporary := range temporaryOperand.FindAllString(text, -1) {
			used[temporary] = true
		}
	}
	for _, block := range fn.Blocks {
		for _, instruction := range block.Instrs {
			if _, isDrop := instruction.(*mir.Drop); isDrop {
				continue
			}
			text := instruction.Text(types)
			if assign, ok := instruction.(*mir.Assign); ok {
				text = strings.TrimPrefix(text, assign.Name)
			}
			read(text)
		}
		if block.Term != nil {
			read(block.Term.Text(types))
		}
	}
	return used
}

var temporaryOperand = regexp.MustCompile(`\bt[0-9]+\b`)

// firstFeeding is the earliest instruction before index whose temporary
// reaches instruction index, or index itself when none does. Operands are read
// from the instruction's text: good enough for this measurement only.
func firstFeeding(instructions []mir.Instr, producer map[string]int, index int, types *ir.TypeTable) int {
	first := index
	pending := []int{index}
	seen := map[int]bool{index: true}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		text := instructions[current].Text(types)
		if assign, ok := instructions[current].(*mir.Assign); ok {
			text = strings.TrimPrefix(text, assign.Name)
		}
		for _, temporary := range temporaryOperand.FindAllString(text, -1) {
			if from, ok := producer[temporary]; ok && from < current && !seen[from] {
				seen[from] = true
				first = min(first, from)
				pending = append(pending, from)
			}
		}
	}
	return first
}
