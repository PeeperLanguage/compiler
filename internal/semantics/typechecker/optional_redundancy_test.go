package typechecker

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/semantics/typeinfo"
)

func TestOptionalForwardGenericField(t *testing.T) {
	declarations := []string{
		"type Early = Box<i32>;",
		"struct Box<T> { value: ?Later }",
		"type Later = ?i32;",
	}
	for first := range len(declarations) {
		for second := range len(declarations) {
			if first == second {
				continue
			}
			t.Run(fmt.Sprintf("order_%d%d", first, second), func(t *testing.T) {
				source := strings.Join([]string{declarations[first], declarations[second], declarations[3-first-second]}, "\n")
				_, diag := checkFlowSource(t, source+`
fn Read(box: &Early) -> i32 {
 if box.value == none { return 0; }
 return box.value;
}`)
				if diag.HasErrors() {
					t.Fatalf("forward generic field must need one proof:\n%s", diag.EmitAllToString())
				}
			})
		}
	}
}

func TestOptionalRedundancyCanonicalTypes(t *testing.T) {
	for _, test := range []struct {
		name         string
		declarations string
		spelling     string
		notes        int
	}{
		{"explicit double", "", "??i32", 1},
		{"explicit triple", "", "???i32", 2},
		{"explicit spaced", "", "? ?i32", 1},
		{"alias", "type Maybe = ?i32;", "?Maybe", 0},
		{"forward alias", "type Maybe = ?Later; type Later = ?i32;", "Maybe", 0},
		{"generic", "type Maybe<T> = ?T;", "Maybe<?i32>", 0},
		{"wrapped generic", "type Maybe<T> = ?T;", "?Maybe<?i32>", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			module, diag := checkFlowSource(t, test.declarations+`
fn Some() -> ?i32 { return 42; }
fn Wrap() -> `+test.spelling+` { return Some(); }
fn Read() -> i32 {
 let value = Wrap();
 if value == none { return 0; }
 return value;
}`)
			if diag.HasErrors() {
				t.Fatalf("unexpected errors:\n%s", diag.EmitAllToString())
			}
			notes := 0
			for _, item := range diag.Diagnostics() {
				if item.Code == diagnostics.InfoRedundantOptional {
					notes++
					if item.Severity != diagnostics.Info {
						t.Fatalf("severity = %v", item.Severity)
					}
				}
			}
			if notes != test.notes {
				t.Fatalf("notes = %d, want %d:\n%s", notes, test.notes, diag.EmitAllToString())
			}
			wrap, ok := module.ModuleScope.LookupLocal("Wrap")
			if !ok {
				t.Fatal("missing Wrap symbol")
			}
			fn, ok := typeinfo.Unalias(wrap.Type).(*typeinfo.FuncType)
			if !ok {
				t.Fatalf("Wrap type = %T", wrap.Type)
			}
			optional, ok := typeinfo.Unalias(fn.Return).(*typeinfo.OptionalType)
			if !ok || typeinfo.TypeText(optional.Inner) != "i32" {
				t.Fatalf("return type = %s, want one optional i32 carrier", typeinfo.TypeText(fn.Return))
			}
			descriptor, ok := typeinfo.VariantDescriptorOf(fn.Return)
			if !ok || len(descriptor.Cases) != 2 || typeinfo.TypeText(descriptor.Cases[1].Payload) != "i32" {
				t.Fatalf("optional representation = %#v", descriptor)
			}
		})
	}
}

func TestOptionalRedundancyFixtureSemantics(t *testing.T) {
	source, err := os.ReadFile("../../../x_test/runtime_optional_redundancy/src/main.peep")
	if err != nil {
		t.Fatal(err)
	}
	_, diag := checkFlowSource(t, string(source))
	if diag.HasErrors() {
		t.Fatalf("fixture semantic errors:\n%s", diag.EmitAllToString())
	}
}
