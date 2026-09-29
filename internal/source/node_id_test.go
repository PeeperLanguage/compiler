package source

import (
	"testing"

	"compiler/internal/moduleid"
)

func TestGeneratedNodeIDIsDeterministicAndContextOwned(t *testing.T) {
	function := moduleid.FunctionID("test-function")
	owner := FunctionNodeID(function, 7)
	first := GeneratedNodeID(owner, GeneratedDefaultArgument, 2, 1)
	second := GeneratedNodeID(owner, GeneratedDefaultArgument, 2, 1)
	if !first.IsGenerated() || first != second || first.Function() != function {
		t.Fatalf("generated identities = %v and %v", first, second)
	}
	for name, other := range map[string]NodeID{
		"owner":   GeneratedNodeID(FunctionNodeID(function, 8), GeneratedDefaultArgument, 2, 1),
		"kind":    GeneratedNodeID(owner, GeneratedCheckedIteration, 2, 1),
		"slot":    GeneratedNodeID(owner, GeneratedDefaultArgument, 3, 1),
		"ordinal": GeneratedNodeID(owner, GeneratedDefaultArgument, 2, 2),
	} {
		if other == first {
			t.Fatalf("%s context produced duplicate identity %v", name, other)
		}
	}
}

func TestGeneratedNodeIDSupportsGeneratedOwners(t *testing.T) {
	function := moduleid.FunctionID("test-function")
	owner := GeneratedNodeID(FunctionNodeID(function, 4), GeneratedDefaultArgument, 1, 3)
	first := GeneratedNodeID(owner, GeneratedDefaultArgument, 2, 1)
	second := GeneratedNodeID(owner, GeneratedDefaultArgument, 2, 1)
	if !first.IsValid() || first != second || first.Function() != function {
		t.Fatalf("nested generated identities = %v and %v", first, second)
	}
	otherOwner := GeneratedNodeID(FunctionNodeID(function, 4), GeneratedDefaultArgument, 1, 4)
	if first == GeneratedNodeID(otherOwner, GeneratedDefaultArgument, 2, 1) {
		t.Fatalf("nested owners collided at %v", first)
	}
}

func TestGeneratedNodeIDRejectsUnownedContext(t *testing.T) {
	if id := GeneratedNodeID(ParsedNodeID(1), GeneratedDefaultArgument, 0, 1); id.IsValid() {
		t.Fatalf("parsed owner produced generated identity %v", id)
	}
	if id := GeneratedNodeID(FunctionNodeID("", 1), GeneratedDefaultArgument, 0, 1); id.IsValid() {
		t.Fatalf("invalid function owner produced generated identity %v", id)
	}
	if id := GeneratedNodeID(FunctionNodeID("test-function", 1), GeneratedNodeKind(255), 0, 1); id.IsValid() {
		t.Fatalf("unknown generation kind produced identity %v", id)
	}
}
