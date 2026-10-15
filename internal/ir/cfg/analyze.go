package cfg

import (
	"cmp"
	"fmt"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/problems"
	"compiler/internal/source"
)

// Analyze emits control-flow diagnostics without mutating finalized topology.
func Analyze(module *Module, diag *diagnostics.DiagnosticBag, constantCondition func(*Branch) (bool, bool)) {
	if module == nil {
		return
	}
	for _, fn := range module.Functions {
		analyzeFunction(fn, diag, constantCondition)
	}
}

func analyzeFunction(fn *ControlFlowGraph, diag *diagnostics.DiagnosticBag, constantCondition func(*Branch) (bool, bool)) {
	if fn == nil || fn.Entry == nil {
		return
	}
	if diag != nil {
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			if branch, ok := block.Terminator.(*Branch); ok && constantCondition != nil {
				if value, found := constantCondition(branch); found {
					reportConstantCondition(branch, value, block.Origin == BlockLoop, diag)
				}
			}
			if block.IsReachable {
				continue
			}
			for _, site := range block.Sites {
				if site != nil && (site.Kind == SiteStatement || site.Kind == SiteTerminator) {
					diag.Add(problems.UnreachableCode(site.Location))
				}
			}
		}
	}
	if fn.Exit != nil && fn.Exit.IsReachable && fn.HasReturnValue {
		reportMissingReturn(fn, diag)
	}
}

func reportConstantCondition(branch *Branch, value, isLoop bool, diag *diagnostics.DiagnosticBag) {
	if branch == nil || diag == nil {
		return
	}
	msg := "condition is always false"
	code := diagnostics.WarnConstantConditionFalse
	if value {
		msg = "condition is always true"
		code = diagnostics.WarnConstantConditionTrue
	}
	// The warning is about the condition, not the whole statement it guards.
	location := branch.ConditionLocation
	if location == nil {
		location = branch.Location
	}
	warning := diagnostics.NewWarning(msg).WithCode(code).WithPrimaryLabel(location, msg)
	if isLoop && value {
		warning.Help("write `for` with no condition for a loop that never ends")
	}
	diag.Add(warning)
}

// missingReturnCause is one path that reaches the end of a function without
// returning, with the label that names it.
type missingReturnCause struct {
	label    string
	location *source.Location
}

// maxMissingReturnCauses keeps the message short when many paths fall off.
const maxMissingReturnCauses = 3

// reportMissingReturn explains how execution reaches the end of fn. A branch
// that does not return is no error while code after it does, so only paths
// that arrive at the end are named, and only when nothing separates them from
// it: if statements come between, the end of the function is the cause, since
// one `return` there fixes every path.
func reportMissingReturn(fn *ControlFlowGraph, diag *diagnostics.DiagnosticBag) {
	if fn == nil || diag == nil {
		return
	}
	diagnostic := diagnostics.NewError("not all control paths return a value").WithCode(diagnostics.ErrMissingReturn)
	end := closingBrace(fn.Location)
	if end == nil {
		end = fn.Location
	}
	endLabel := "function ends without returning `" + fn.ReturnTypeText + "`"
	help := "add a `return` at the end of the function"
	causes, endsAfterStatements := findMissingReturnCauses(fn)
	hidden := 0
	if endsAfterStatements || len(causes) == 0 {
		diagnostic.WithPrimaryLabel(end, endLabel)
	} else {
		slices.SortFunc(causes, func(left, right missingReturnCause) int { return compareLocations(left.location, right.location) })
		hidden = max(len(causes)-maxMissingReturnCauses, 0)
		for index, cause := range causes[:len(causes)-hidden] {
			if index == 0 {
				diagnostic.WithPrimaryLabel(cause.location, cause.label)
			} else {
				diagnostic.WithSecondaryLabel(cause.location, cause.label)
			}
		}
		diagnostic.WithSecondaryLabel(end, endLabel)
		help += ", or make each path shown return a value"
	}
	diagnostic.Note("execution can reach the end of the function without a `return`")
	if hidden > 0 {
		diagnostic.Note(fmt.Sprintf("%d more path(s) like these are not shown", hidden))
	}
	diagnostic.Help(help)
	diag.Add(diagnostic)
}

// findMissingReturnCauses walks back from the exit through blocks that hold no
// statement. The first thing it meets on each path is the cause. It reports
// whether some path arrives after statements of the function body itself.
func findMissingReturnCauses(fn *ControlFlowGraph) (causes []missingReturnCause, endsAfterStatements bool) {
	if fn == nil || fn.Exit == nil {
		return nil, false
	}
	visited := map[*Block]bool{fn.Exit: true}
	add := func(label string, location *source.Location) {
		if location != nil {
			causes = append(causes, missingReturnCause{label, location})
		}
	}
	var walk func(*Block)
	walk = func(block *Block) {
		for _, pred := range predecessorBlocks(fn, block) {
			// Code after a `return` is unreachable and may still jump to the exit.
			if pred == nil || !pred.IsReachable || visited[pred] {
				continue
			}
			visited[pred] = true
			statement := lastStatement(pred)
			switch {
			case pred.Origin == BlockLoop:
				add(loopFinishLabel, conditionLocation(pred))
			case statement != nil && block.Origin == BlockLoopExit && statement.NodeID.IsGenerated():
				// A call iteration leaves its loop through a break the compiler
				// wrote, so the loop is what to point at.
				add(loopFinishLabel, statement.Location)
			case statement != nil && block.Origin == BlockLoopExit:
				add("this `break` leaves the loop without returning", statement.Location)
			case pred.Origin == BlockImplicitElse || pred.Origin == BlockElse && statement == nil:
				add(ifFalseLabel, conditionOf(fn, pred))
			case pred.Origin == BlockThen && statement == nil:
				add(ifTrueLabel, conditionOf(fn, pred))
			case statement != nil && statement.ScopeID == fn.BodyScopeID:
				endsAfterStatements = true
			case statement != nil:
				add(branchEndLabel, statement.Location)
			case pred.Origin == BlockArm:
				add(branchEndLabel, pred.Location)
			case pred == fn.Entry:
				endsAfterStatements = true
			default:
				walk(pred)
			}
		}
	}
	walk(fn.Exit)
	return causes, endsAfterStatements
}

const (
	loopFinishLabel = "when this loop finishes, nothing is returned"
	branchEndLabel  = "this path ends without returning"
	ifTrueLabel     = "if this is true, nothing is returned"
	ifFalseLabel    = "if this is false, nothing is returned"
)

func lastStatement(block *Block) *Site {
	for index := len(block.Sites) - 1; index >= 0; index-- {
		if block.Sites[index].Kind == SiteStatement {
			return block.Sites[index]
		}
	}
	return nil
}

func conditionLocation(block *Block) *source.Location {
	if branch, ok := block.Terminator.(*Branch); ok {
		return branch.ConditionLocation
	}
	return nil
}

// conditionOf is the condition of the branch whose edge enters block.
func conditionOf(fn *ControlFlowGraph, block *Block) *source.Location {
	for _, pred := range predecessorBlocks(fn, block) {
		if branch, ok := pred.Terminator.(*Branch); ok && (branch.TrueTarget == block || branch.FalseTarget == block) {
			return branch.ConditionLocation
		}
	}
	return nil
}

// closingBrace is the last character of a function's span, where the end of
// the function is.
func closingBrace(location *source.Location) *source.Location {
	if location == nil || location.Filename == nil || location.End == nil || location.End.Column <= 1 {
		return nil
	}
	end := *location.End
	start := end
	start.Column--
	start.Index--
	return source.NewLocation(*location.Filename, start, end)
}

func compareLocations(left, right *source.Location) int {
	if left == nil || left.Start == nil || right == nil || right.Start == nil {
		return 0
	}
	return cmp.Or(cmp.Compare(left.Start.Line, right.Start.Line), cmp.Compare(left.Start.Column, right.Start.Column))
}

func predecessorBlocks(fn *ControlFlowGraph, block *Block) []*Block {
	if fn == nil || fn.BlockEdges == nil || block == nil {
		return nil
	}
	ids := fn.BlockEdges.Predecessors(block.ID, nil)
	blocks := make([]*Block, 0, len(ids))
	for _, id := range ids {
		if id >= 0 && id < len(fn.Blocks) {
			blocks = append(blocks, fn.Blocks[id])
		}
	}
	return blocks
}
