package analysis_test

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/cfg"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/analysis"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

func analyzeInitializationSource(t *testing.T, source string) *diagnostics.DiagnosticBag {
	t.Helper()
	const filePath = "definite_init_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, source)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "definite_init_test"},
		FilePath: filePath,
		Content:  source,
		AST:      parser.New(filePath, lexer.New(filePath, source, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(module)

	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	module.THIR = typechecker.Check(ctx, module)
	module.CFG = cfg.BuildModule(module.THIR)
	analysis.Run(diag, analysis.Input{
		Source:      module.THIR,
		CFG:         module.CFG,
		Scope:       module.ModuleScope,
		SymbolIndex: module.SymbolIndex,
	})
	return diag
}

func TestCallIterationDoesNotGuaranteeEntry(t *testing.T) {
	diag := analyzeInitializationSource(t, `struct Cursor {}
fn (self: &Cursor) Next() -> ?i32 { return none; }
fn choose() -> i32 {
	let cursor = Cursor.{};
	let mut result: i32;
	for item in cursor.Next() { result = item; }
	return result;
}`)
	if !diag.HasErrors() || !strings.Contains(diag.EmitAllToString(), "used before it's initialized") {
		t.Fatalf("expected zero-entry uninitialized diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationIgnoresTerminatingBranchAtJoin(t *testing.T) {
	diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	if flag {
		value = 7;
	} else {
		return 3;
	}
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationRejectsContinuingUninitializedBranch(t *testing.T) {
	diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	if flag {
		value = 7;
	}
	return value;
}`)
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized diagnostic:\n%s", diag.EmitAllToString())
	}
	if got := diag.EmitAllToString(); !strings.Contains(got, "symbol `value` used before it's initialized") || strings.Contains(got, "value$") {
		t.Fatalf("diagnostic does not use source symbol name:\n%s", got)
	}
}

func TestInitializationAcceptsAssignmentOnBothBranches(t *testing.T) {
	diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	if flag {
		value = 7;
	} else {
		value = 3;
	}
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationLoopMayExecuteZeroTimes(t *testing.T) {
	diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	for flag {
		value = 7;
	}
	return value;
}`)
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationUsesGuaranteedRangeEntry(t *testing.T) {
	for _, test := range []struct {
		name      string
		rangeText string
		wantError bool
	}{
		{name: "guaranteed entry", rangeText: "0..1"},
		{name: "empty range", rangeText: "0..0", wantError: true},
		{name: "runtime range", rangeText: "start..end", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			diag := analyzeInitializationSource(t, `fn choose(start: i32, end: i32) -> i32 {
	let mut value: i32;
	for item in `+test.rangeText+` {
		value = item;
	}
	return value;
}`)
			if got := hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable); got != test.wantError {
				t.Fatalf("uninitialized diagnostic = %v, want %v:\n%s", got, test.wantError, diag.EmitAllToString())
			}
		})
	}
}

func TestInitializationAcceptsDirectAssignment(t *testing.T) {
	diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	value = 7;
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationRejectsProjectedWriteBeforeWholeAssignment(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name: "nested field",
			source: `struct Inner { value: i32 }
struct Outer { inner: Inner }
fn choose() -> i32 {
	let mut outer: Outer;
	outer.inner.value = 7;
	return 0;
}`,
		},
		{
			name: "constant index",
			source: `fn choose() -> i32 {
	let mut values: [2]i32;
	values[0] = 7;
	return 0;
}`,
		},
		{
			name: "runtime index",
			source: `fn choose(index: i32) -> i32 {
	let mut values: [2]i32;
	values[index] = 7;
	return 0;
}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diag := analyzeInitializationSource(t, test.source)
			if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
				t.Fatalf("expected projected write diagnostic:\n%s", diag.EmitAllToString())
			}
			if output := diag.EmitAllToString(); !strings.Contains(output, "assign a complete value to this symbol before writing through a projection") {
				t.Fatalf("projected write diagnostic lacks whole-value guidance:\n%s", output)
			}
		})
	}
}

func TestInitializationAcceptsProjectedWriteAfterWholeAssignment(t *testing.T) {
	diag := analyzeInitializationSource(t, `struct Pair { left: i32, right: i32 }
fn choose() -> i32 {
	let mut pair: Pair;
	pair = .{ left = 1, right = 2 };
	pair.left = 7;
	return pair.left;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationDefinesMatchPatternBindingOnCaseEdge(t *testing.T) {
	diag := analyzeInitializationSource(t, `enum Result {
	Ok: { value: i32 },
	Pending,
}

fn choose(result: Result) -> i32 {
	match result {
		Result::Ok with { value = payload } => {
			return payload;
		}
		Result::Pending => {
			return 0;
		}
	}
}`)
	if hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("unexpected uninitialized diagnostic:\n%s", diag.EmitAllToString())
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func hasDiagnosticCode(diag *diagnostics.DiagnosticBag, code string) bool {
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}

// A match subject is a read like any other. This was previously undiagnosed:
// the analysis attached a site condition for a branch terminator only, so
// nothing checked the subject of a match. Publishing the subject as an ordinary
// read closed the gap, and this analysis learned nothing about matches to get it.
func TestInitializationChecksMatchSubject(t *testing.T) {
	diag := analyzeInitializationSource(t, `enum Outcome {
	Ok: { value: i32 },
	Pending,
}

fn choose(flag: bool) -> i32 {
	let mut outcome: Outcome;
	if flag {
		outcome = Outcome::Pending;
	}
	match outcome {
		Outcome::Ok with { value = payload } => {
			return payload;
		}
		Outcome::Pending => {
			return 0;
		}
	}
}`)
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized match subject diagnostic:\n%s", diag.EmitAllToString())
	}
	if got := diag.EmitAllToString(); !strings.Contains(got, "symbol `outcome` used before it's initialized") {
		t.Fatalf("diagnostic does not name the match subject:\n%s", got)
	}
}

// A loop's iterated sequence is a read like any other. It was previously
// unchecked here: the analysis saw only a statement's own expressions and a
// branch condition, and a for statement carries neither. Ownership always read
// it, so publishing it once closed the gap for initialization too.
func TestInitializationChecksLoopIterable(t *testing.T) {
	diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut limit: i32;
	if flag {
		limit = 3;
	}
	for i in 0..limit {
	}
	return 0;
}`)
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized loop bound diagnostic:\n%s", diag.EmitAllToString())
	}
	if got := diag.EmitAllToString(); !strings.Contains(got, "symbol `limit` used before it's initialized") {
		t.Fatalf("diagnostic does not name the loop bound:\n%s", got)
	}
}
