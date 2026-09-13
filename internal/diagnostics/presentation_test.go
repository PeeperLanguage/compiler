package diagnostics

import (
	"bytes"
	"strings"
	"testing"

	"compiler/internal/source"
	"compiler/pkg/colors"
)

func TestForPresentationHidesInternalDetailWithoutMutatingStoredDiagnostic(t *testing.T) {
	filename := "/tmp/main.peep"
	loc := &source.Location{Filename: &filename}
	diag := NewError("lowered MIR is malformed: value %7 has type#11, want type#4").
		WithCode(ErrInvalidEvidence).
		WithPrimaryLabel(loc, "malformed MIR value").
		WithText("internal", "validator detail", colors.RED)

	presented := ForPresentation(diag, PresentationOptions{})
	if presented == diag {
		t.Fatal("presentation must not mutate or reuse hidden ICE diagnostic")
	}
	if presented.Code != ErrInvalidEvidence || presented.Severity != Error {
		t.Fatalf("presented identity = (%q, %v), want (%q, error)", presented.Code, presented.Severity, ErrInvalidEvidence)
	}
	if presented.Message != internalCompilerFailureMessage {
		t.Fatalf("presented message = %q", presented.Message)
	}
	if len(presented.Labels) != 0 {
		t.Fatalf("presented labels = %#v, want hidden", presented.Labels)
	}
	if len(presented.Extras) != 1 || presented.Extras[0].Text.Kind != "help" {
		t.Fatalf("presented extras = %#v, want report guidance", presented.Extras)
	}
	if diag.Message == internalCompilerFailureMessage || len(diag.Labels) != 1 || len(diag.Extras) != 1 {
		t.Fatalf("stored diagnostic was mutated: %#v", diag)
	}
}

func TestForPresentationShowsFullInternalDetailWhenRequested(t *testing.T) {
	diag := NewError("ownership evidence is inconsistent: detail").WithCode(ErrInvalidEvidence)
	presented := ForPresentation(diag, PresentationOptions{ShowInternalErrors: true})
	if presented != diag {
		t.Fatal("full internal presentation should preserve original diagnostic")
	}
	if presented.Message != diag.Message {
		t.Fatalf("message = %q, want %q", presented.Message, diag.Message)
	}
}

func TestForPresentationLeavesSourceDiagnosticUnchanged(t *testing.T) {
	diag := NewError("type mismatch").WithCode(ErrTypeMismatch)
	presented := ForPresentation(diag, PresentationOptions{})
	if presented != diag {
		t.Fatal("ordinary source diagnostic should not be copied")
	}
}

func TestEmitFilteredInternalDetailPresentation(t *testing.T) {
	bag := NewDiagnosticBag()
	bag.Add(NewError("lowered MIR is malformed: secret validator detail").WithCode(ErrInvalidEvidence))

	render := func(options PresentationOptions) string {
		var buf bytes.Buffer
		emitter := NewEmitter(&buf)
		bag.emitFiltered(emitter, func(*Diagnostic) bool { return true }, &options)
		return buf.String()
	}

	hidden := render(PresentationOptions{})
	if strings.Contains(hidden, "secret validator detail") {
		t.Fatalf("default output leaked internal detail: %q", hidden)
	}
	if !strings.Contains(hidden, ErrInvalidEvidence) || !strings.Contains(hidden, internalCompilerFailureMessage) {
		t.Fatalf("default output = %q, want ICE identity and generic message", hidden)
	}

	shown := render(PresentationOptions{ShowInternalErrors: true})
	if !strings.Contains(shown, "secret validator detail") || !strings.Contains(shown, ErrInvalidEvidence) {
		t.Fatalf("opt-in output = %q, want full ICE detail", shown)
	}
}
