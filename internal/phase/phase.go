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
	// Typechecked includes final module const values and semantic API identity.
	Typechecked
	// CFG includes finalized topology and CFG diagnostics.
	CFG
	// Analyzed includes CFG-refined semantic facts and ownership cleanup decisions.
	Analyzed
	// Usage records completion of usage diagnostics at project barrier.
	Usage
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
	case Typechecked:
		return "typechecked"
	case CFG:
		return "CFG"
	case Analyzed:
		return "analyzed"
	case Usage:
		return "usage"
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
