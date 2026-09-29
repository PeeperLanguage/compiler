package source

import (
	"strconv"

	"compiler/internal/moduleid"
)

type nodeIDDomain uint8

const (
	nodeIDInvalid nodeIDDomain = iota
	nodeIDParsed
	nodeIDFunction
	nodeIDGenerated
)

type GeneratedNodeKind uint8

const (
	GeneratedNodeInvalid GeneratedNodeKind = iota
	GeneratedDefaultArgument
	GeneratedCheckedIteration
)

// NodeID identifies one source or generated syntax node. Parsed IDs are
// generation-local until the Parsed-to-Collected boundary republishes function
// nodes under their stable FunctionID and function-local preorder ordinal.
type NodeID struct {
	domain     nodeIDDomain
	function   moduleid.FunctionID
	generation string
	ordinal    uint64
}

func ParsedNodeID(ordinal uint64) NodeID {
	if ordinal == 0 {
		return NodeID{}
	}
	return NodeID{domain: nodeIDParsed, ordinal: ordinal}
}

func FunctionNodeID(function moduleid.FunctionID, ordinal uint64) NodeID {
	if function == "" || ordinal == 0 {
		return NodeID{}
	}
	return NodeID{domain: nodeIDFunction, function: function, ordinal: ordinal}
}

func GeneratedNodeID(owner NodeID, kind GeneratedNodeKind, slot, ordinal uint64) NodeID {
	function := owner.Function()
	if function == "" || !owner.IsValid() || ordinal == 0 {
		return NodeID{}
	}
	switch kind {
	case GeneratedDefaultArgument, GeneratedCheckedIteration:
	default:
		return NodeID{}
	}
	generation := owner.generation
	if generation != "" {
		generation += "/"
	}
	generation += strconv.FormatUint(owner.ordinal, 10) + "/" +
		strconv.FormatUint(uint64(kind), 10) + "/" + strconv.FormatUint(slot, 10)
	return NodeID{domain: nodeIDGenerated, function: function, generation: generation, ordinal: ordinal}
}

func (id NodeID) IsValid() bool {
	switch id.domain {
	case nodeIDParsed:
		return id.ordinal != 0
	case nodeIDFunction:
		return id.function != "" && id.generation == "" && id.ordinal != 0
	case nodeIDGenerated:
		return id.function != "" && id.generation != "" && id.ordinal != 0
	default:
		return false
	}
}

func (id NodeID) IsGenerated() bool {
	return id.domain == nodeIDGenerated && id.IsValid()
}

func (id NodeID) Function() moduleid.FunctionID {
	if id.domain != nodeIDFunction && id.domain != nodeIDGenerated {
		return ""
	}
	return id.function
}

func (id NodeID) String() string {
	switch id.domain {
	case nodeIDParsed:
		return "parsed:" + strconv.FormatUint(id.ordinal, 10)
	case nodeIDFunction:
		return "function:" + string(id.function) + ":" + strconv.FormatUint(id.ordinal, 10)
	case nodeIDGenerated:
		return "generated:" + string(id.function) + ":" + id.generation + ":" + strconv.FormatUint(id.ordinal, 10)
	default:
		return "invalid"
	}
}
