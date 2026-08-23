package diagnostics

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"compiler/internal/phase"
	"compiler/pkg/colors"
)

func TestBeginPhaseReplacesOnlySelectedGroup(t *testing.T) {
	bag := NewDiagnosticBag()
	bag.BeginPhase(phase.Parsed, "a").Add(NewWarning("old a parse"))
	bag.BeginPhase(phase.Parsed, "b").Add(NewWarning("b parse"))
	bag.BeginPhase(phase.Typechecked, "a").Add(NewError("a type"))
	bag.BeginPhase(phase.Parsed, "a").Add(NewWarning("new a parse"))

	got := bag.Diagnostics()
	if len(got) != 3 || got[0].Message != "new a parse" || got[1].Message != "b parse" || got[2].Message != "a type" {
		t.Fatalf("diagnostics after group replacement = %#v", got)
	}
	if bag.ErrorCount() != 1 || bag.WarningCount() != 2 {
		t.Fatalf("counts = (%d errors, %d warnings), want (1, 2)", bag.ErrorCount(), bag.WarningCount())
	}
}

func TestAppendPhaseContinuesSelectedGroup(t *testing.T) {
	bag := NewDiagnosticBag()
	bag.BeginPhase(phase.Setup, "").Add(NewWarning("config"))
	bag.AppendPhase(phase.Setup, "").Add(NewError("prelude"))

	got := bag.Diagnostics()
	if len(got) != 2 || got[0].Message != "config" || got[1].Message != "prelude" {
		t.Fatalf("continued diagnostics = %#v", got)
	}
}

func TestDiscardModuleAfterRetainsOtherModuleGroups(t *testing.T) {
	bag := NewDiagnosticBag()
	bag.BeginPhase(phase.Parsed, "a").Add(NewWarning("a parse"))
	bag.BeginPhase(phase.Typechecked, "a").Add(NewError("a type"))
	bag.BeginPhase(phase.Typechecked, "b").Add(NewError("b type"))

	bag.DiscardModuleAfter("a", phase.Parsed)

	got := bag.Diagnostics()
	if len(got) != 2 || got[0].Message != "a parse" || got[1].Message != "b type" {
		t.Fatalf("diagnostics after module reset = %#v", got)
	}
}

func TestCopyModuleRangeReplacesOnlySelectedGroups(t *testing.T) {
	source := NewDiagnosticBag()
	source.BeginPhase(phase.Parsed, "a").Add(NewWarning("a parse"))
	source.BeginPhase(phase.Ownership, "a").Add(NewWarning("a ownership"))
	source.BeginPhase(phase.Usage, "a").Add(NewWarning("a usage"))
	source.BeginPhase(phase.Backend, "a").Add(NewError("a backend"))
	source.BeginPhase(phase.Parsed, "b").Add(NewWarning("b parse"))
	destination := NewDiagnosticBag()
	destination.BeginPhase(phase.Parsed, "a").Add(NewWarning("stale parse"))
	destination.BeginPhase(phase.Usage, "a").Add(NewWarning("stale usage"))
	destination.BeginPhase(phase.Parsed, "b").Add(NewWarning("existing b parse"))

	destination.CopyModuleRange(source, "a", phase.None, phase.Ownership, true)
	destination.CopyModuleRange(source, "a", phase.None, phase.Ownership, true)

	got := destination.Diagnostics()
	want := []string{"a parse", "existing b parse", "a ownership", "stale usage"}
	if len(got) != len(want) {
		t.Fatalf("diagnostic count = %d, want %d: %#v", len(got), len(want), got)
	}
	for i, message := range want {
		if got[i].Message != message {
			t.Fatalf("diagnostic %d = %q, want %q", i, got[i].Message, message)
		}
	}

	destination.CopyModuleRange(source, "a", phase.Usage, phase.Backend, false)
	got = destination.Diagnostics()
	want = []string{"a parse", "existing b parse", "a ownership"}
	if len(got) != len(want) || destination.HasErrors() {
		t.Fatalf("inactive diagnostics affected active results: %#v", got)
	}
	for i, message := range want {
		if got[i].Message != message {
			t.Fatalf("diagnostic %d while deferred = %q, want %q", i, got[i].Message, message)
		}
	}

	destination.ActivateModuleRange("a", phase.Usage, phase.Backend)
	destination.ActivateModuleRange("a", phase.Usage, phase.Backend)
	got = destination.Diagnostics()
	want = []string{"a parse", "existing b parse", "a ownership", "a usage", "a backend"}
	if len(got) != len(want) || destination.ErrorCount() != 1 {
		t.Fatalf("diagnostic count after deferred copy = %d, want %d: %#v", len(got), len(want), got)
	}
	for i, message := range want {
		if got[i].Message != message {
			t.Fatalf("diagnostic %d after deferred copy = %q, want %q", i, got[i].Message, message)
		}
	}
}

func TestScopedPhasesAddConcurrently(t *testing.T) {
	const workers = 32
	bag := NewDiagnosticBag()
	writers := make([]*DiagnosticBag, workers)
	for i := range workers {
		writers[i] = bag.BeginPhase(phase.Typechecked, strconv.Itoa(i))
	}

	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writers[i].Add(NewWarning(strconv.Itoa(i)))
		}(i)
	}
	wg.Wait()

	if got := bag.WarningCount(); got != workers {
		t.Fatalf("warning count = %d, want %d", got, workers)
	}
}

func TestEmitErrorsOmitsWarningsAndWarningSummary(t *testing.T) {
	bag := NewDiagnosticBag()
	bag.Add(NewWarning("unused local"))
	bag.Add(NewError("broken"))

	out := captureEmitErrors(bag)
	if strings.Contains(out, "unused local") {
		t.Fatalf("expected warning to be omitted, got %q", out)
	}
	if !strings.Contains(out, "broken") {
		t.Fatalf("expected error to be emitted, got %q", out)
	}
	if strings.Contains(out, "warning(s)") {
		t.Fatalf("expected warning summary to be omitted, got %q", out)
	}
	if !strings.Contains(out, "Compilation failed with 1 error(s)") {
		t.Fatalf("expected error summary, got %q", out)
	}
}

func TestEmitErrorsSkipsWarningsWhenNoErrors(t *testing.T) {
	bag := NewDiagnosticBag()
	bag.Add(NewWarning("unused local"))

	out := captureEmitErrors(bag)
	if out != "" {
		t.Fatalf("expected no output for warnings-only bag, got %q", out)
	}
}

func TestEmitAllToHTMLRendersDirectHTML(t *testing.T) {
	bag := NewDiagnosticBag()
	bag.Add(NewError("<broken>"))

	out := bag.EmitAllToHTML()
	if !strings.Contains(out, "<span style=") {
		t.Fatalf("expected html spans, got %q", out)
	}
	if !strings.Contains(out, "&lt;broken&gt;") {
		t.Fatalf("expected escaped html message, got %q", out)
	}
	if strings.Contains(out, "\033[") {
		t.Fatalf("expected no ansi sequences, got %q", out)
	}
}

func TestConcurrentStringFormatsRemainIsolated(t *testing.T) {
	bag := NewDiagnosticBag()
	bag.Add(NewError("<broken>"))

	const iterations = 500
	start := make(chan struct{})
	errs := make(chan string, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for range iterations {
			out := bag.EmitAllToString()
			if !strings.Contains(out, "\033[") || strings.Contains(out, "<span") || strings.Contains(out, "&lt;broken&gt;") {
				errs <- "ANSI output contaminated: " + out
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for range iterations {
			out := bag.EmitAllToHTML()
			if strings.Contains(out, "\033[") || !strings.Contains(out, "<span") || !strings.Contains(out, "&lt;broken&gt;") {
				errs <- "HTML output contaminated: " + out
				return
			}
		}
	}()
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func captureEmitErrors(bag *DiagnosticBag) string {
	var sb strings.Builder
	logger := colors.NewLogger(colors.CurrentLogFormat())
	emitter := &Emitter{
		cache:       bag.sourceCache,
		writer:      &sb,
		logger:      logger,
		highlighter: NewSyntaxHighlighter(true, logger),
	}
	bag.emitFiltered(emitter, func(diag *Diagnostic) bool {
		return diag != nil && diag.Severity == Error
	})
	return sb.String()
}
