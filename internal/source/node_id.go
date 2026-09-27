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
	nodeIDSynthetic
)

// NodeID identifies one source or generated syntax node. Parsed IDs are
// generation-local until the Parsed-to-Collected boundary republishes function
// nodes under their stable FunctionID and function-local preorder ordinal.
type NodeID struct {
	domain   nodeIDDomain
	function moduleid.FunctionID
	ordinal  uint64
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

func SyntheticNodeID(ordinal uint64) NodeID {
	if ordinal == 0 {
		return NodeID{}
	}
	return NodeID{domain: nodeIDSynthetic, ordinal: ordinal}
}

func (id NodeID) IsValid() bool {
	return id.domain != nodeIDInvalid && id.ordinal != 0 && (id.domain != nodeIDFunction || id.function != "")
}

func (id NodeID) IsSynthetic() bool {
	return id.domain == nodeIDSynthetic && id.ordinal != 0
}

func (id NodeID) Function() moduleid.FunctionID {
	if id.domain != nodeIDFunction {
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
	case nodeIDSynthetic:
		return "synthetic:" + strconv.FormatUint(id.ordinal, 10)
	default:
		return "invalid"
	}
}
