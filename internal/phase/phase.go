// Package phase defines compiler pipeline phase identity shared by artifacts,
// diagnostics, scheduling, and incremental reuse.
package phase

import "fmt"

type Phase uint8

const (
	None Phase = iota
	Setup
	Load
	Parsed
	Collected
	Bound
	Resolved
	// ConstEval completes eager semantic evaluation. Expected-type queries may
	// refine facts while typechecking.
	ConstEval
	// Typechecked includes final module const values and semantic API identity.
	Typechecked
	// CFG includes finalized topology and CFG diagnostics.
	CFG
	// FlowTyped includes CFG-refined expression types and place origins.
	FlowTyped
	// DefiniteInit records completion of diagnostic-only initialization checks.
	DefiniteInit
	// Ownership includes ownership cleanup results.
	Ownership
	// Usage records completion of usage diagnostics at project barrier.
	Usage
	HIR
	MIR
	Backend
	// Finalize contains checks spanning completed module backends.
	Finalize
)

func (phase Phase) String() string {
	switch phase {
	case None:
		return "none"
	case Setup:
		return "setup"
	case Load:
		return "load"
	case Parsed:
		return "parsed"
	case Collected:
		return "collected"
	case Bound:
		return "bound"
	case Resolved:
		return "resolved"
	case ConstEval:
		return "const-eval"
	case Typechecked:
		return "typechecked"
	case CFG:
		return "CFG"
	case FlowTyped:
		return "flow-typed"
	case DefiniteInit:
		return "definite-init"
	case Ownership:
		return "ownership"
	case Usage:
		return "usage"
	case HIR:
		return "HIR"
	case MIR:
		return "MIR"
	case Backend:
		return "backend"
	case Finalize:
		return "finalize"
	default:
		return fmt.Sprintf("phase(%d)", uint8(phase))
	}
}
