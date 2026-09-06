package mir

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"compiler/pkg/typednil"
)

const maxReportedProblems = 10

// Validate checks the shape of a lowered module: that every block ends, that
// every transfer names a block that exists, and that identity is unambiguous.
//
// It deliberately does not re-derive meaning. Whether the right instruction was
// emitted for a construct is lowering's decision, and re-deciding it here would
// be a second implementation of the thing being validated. That a node kind is
// classified at all is held by the dispatch contract in internal/contracts, not
// here.
//
// A failure is a compiler bug rather than a source error: MIR is built from
// evidence that earlier phases already accepted.
func (m *Module) Validate() error {
	if m == nil || len(m.Funcs) == 0 {
		return nil
	}
	problems := make([]string, 0)
	for _, fn := range m.Funcs {
		if fn == nil {
			problems = append(problems, "module holds a nil function")
			continue
		}
		problems = append(problems, validateFunction(fn)...)
	}
	if len(problems) == 0 {
		return nil
	}
	// Function and block order is stable, but a report that grows from map
	// iteration elsewhere would not be; sorting keeps a broken artifact
	// reproducible between runs.
	sort.Strings(problems)
	if len(problems) > maxReportedProblems {
		return fmt.Errorf("%s (%d more)", strings.Join(problems[:maxReportedProblems], "; "), len(problems)-maxReportedProblems)
	}
	return errors.New(strings.Join(problems, "; "))
}

func validateFunction(fn *Function) []string {
	problems := make([]string, 0)
	blocks := make(map[int]bool, len(fn.Blocks))
	for _, block := range fn.Blocks {
		if block == nil {
			problems = append(problems, fmt.Sprintf("function %s holds a nil block", fn.Name))
			continue
		}
		if blocks[block.ID] {
			problems = append(problems, fmt.Sprintf("function %s declares block b%d twice", fn.Name, block.ID))
			continue
		}
		blocks[block.ID] = true
	}
	// Identity has to hold before transfers can be checked against it.
	if len(problems) > 0 {
		return problems
	}
	if len(fn.Blocks) > 0 && !blocks[fn.EntryID] {
		problems = append(problems, fmt.Sprintf("function %s enters at b%d, which it does not contain", fn.Name, fn.EntryID))
	}
	for _, block := range fn.Blocks {
		for index, instr := range block.Instrs {
			if typednil.IsNil(instr) {
				problems = append(problems, fmt.Sprintf("function %s block b%d holds a nil instruction at %d", fn.Name, block.ID, index))
			}
		}
		if typednil.IsNil(block.Term) {
			// Emission would otherwise fall off the end of a block.
			problems = append(problems, fmt.Sprintf("function %s block b%d has no terminator", fn.Name, block.ID))
			continue
		}
		problems = append(problems, validateTransfers(fn, block, blocks)...)
	}
	return problems
}

// validateTransfers checks that every block a terminator names exists. A
// transfer to a block that was never emitted becomes a label the backend cannot
// resolve.
func validateTransfers(fn *Function, block *Block, blocks map[int]bool) []string {
	problems := make([]string, 0)
	report := func(target int, role string) {
		if !blocks[target] {
			problems = append(problems, fmt.Sprintf("function %s block b%d transfers to b%d as its %s, which it does not contain",
				fn.Name, block.ID, target, role))
		}
	}
	switch term := block.Term.(type) {
	case *Jump:
		report(term.TargetID, "jump target")
	case *Branch:
		report(term.ThenID, "true target")
		report(term.ElseID, "false target")
	case *SwitchVariant:
		seen := make(map[int]bool, len(term.Targets))
		for _, target := range term.Targets {
			report(target.TargetID, fmt.Sprintf("case %d target", target.Case))
			if seen[target.Case] {
				problems = append(problems, fmt.Sprintf("function %s block b%d selects case %d twice", fn.Name, block.ID, target.Case))
			}
			seen[target.Case] = true
		}
	case *Ret:
		// A return leaves the function and names no block.
	default:
		problems = append(problems, fmt.Sprintf("function %s block b%d ends with unknown terminator %T", fn.Name, block.ID, block.Term))
	}
	return problems
}
