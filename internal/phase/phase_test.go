package phase

import "testing"

func TestPhaseString(t *testing.T) {
	for phase, want := range map[Phase]string{
		None: "none", Setup: "setup", Load: "load", Parsed: "parsed",
		Typechecked: "typechecked", CFG: "CFG", FlowTyped: "flow-typed", DefiniteInit: "definite-init",
		Ownership: "ownership", Usage: "usage", HIR: "HIR", MIR: "MIR",
		Backend: "backend", Finalize: "finalize",
	} {
		if got := phase.String(); got != want {
			t.Fatalf("phase %d string = %q, want %q", phase, got, want)
		}
	}
	if got := Phase(255).String(); got != "phase(255)" {
		t.Fatalf("unknown phase string = %q", got)
	}
}
