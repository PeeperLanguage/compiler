package problems

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/source"
)

func TestUnreachableCode(t *testing.T) {
	loc := source.NewLocation("main.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 2})
	d := UnreachableCode(loc)

	if d.Severity != diagnostics.Warning {
		t.Fatalf("severity = %s, want warning", d.Severity)
	}
	if d.Message != "unreachable code" {
		t.Fatalf("message = %q, want %q", d.Message, "unreachable code")
	}
	if d.Code != diagnostics.WarnUnreachableCode {
		t.Fatalf("code = %q, want %q", d.Code, diagnostics.WarnUnreachableCode)
	}
	if len(d.Labels) != 1 {
		t.Fatalf("label count = %d, want 1", len(d.Labels))
	}
	label := d.Labels[0]
	if label.Location != loc || label.Message != "this code is unreachable" || label.Style != diagnostics.Primary {
		t.Fatalf("primary label = %#v", label)
	}
	if len(d.Extras) != 1 {
		t.Fatalf("extra count = %d, want 1", len(d.Extras))
	}
	extra := d.Extras[0]
	if extra.Kind != diagnostics.ExtraText || extra.Text.Kind != "help" || extra.Text.Message != "remove this code or restructure control flow" {
		t.Fatalf("help text = %#v", extra)
	}
}

func TestRedeclaration(t *testing.T) {
	current := source.NewLocation("main.peep", source.Position{Line: 2, Column: 1}, source.Position{Line: 2, Column: 2})
	previous := source.NewLocation("main.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 2})
	d := Redeclaration("symbol already declared", current, previous)

	if d.Severity != diagnostics.Error {
		t.Fatalf("severity = %s, want error", d.Severity)
	}
	if d.Message != "symbol already declared" {
		t.Fatalf("message = %q, want %q", d.Message, "symbol already declared")
	}
	if d.Code != diagnostics.ErrRedeclaredSymbol {
		t.Fatalf("code = %q, want %q", d.Code, diagnostics.ErrRedeclaredSymbol)
	}
	if len(d.Labels) != 2 {
		t.Fatalf("label count = %d, want 2", len(d.Labels))
	}
	primary := d.Labels[0]
	if primary.Location != current || primary.Message != "redeclared here" || primary.Style != diagnostics.Primary {
		t.Fatalf("primary label = %#v", primary)
	}
	secondary := d.Labels[1]
	if secondary.Location != previous || secondary.Message != "first declared here" || secondary.Style != diagnostics.Secondary {
		t.Fatalf("secondary label = %#v", secondary)
	}
}
