package symbols

import (
	"strconv"

	"compiler/internal/moduleid"
	"compiler/internal/source"
)

type symbolIDDomain uint8

const (
	symbolIDInvalid symbolIDDomain = iota
	symbolIDSource
	symbolIDModule
	symbolIDGenerated
	symbolIDCompiler
	symbolIDProjected
)

type GeneratedSymbolRole uint8

const (
	GeneratedSymbolInvalid GeneratedSymbolRole = iota
	GeneratedForCursor
	GeneratedForRangeLimit
	GeneratedForRangeOrdinal
	GeneratedForSequenceCarrier
)

// SymbolID identifies one declared, generated, compiler-owned, or projected
// symbol without depending on allocation order or symbol-object lifetime.
type SymbolID struct {
	domain     symbolIDDomain
	source     source.NodeID
	module     moduleid.ID
	kind       Kind
	key        string
	occurrence uint64
	role       GeneratedSymbolRole
}

func SourceSymbolID(owner source.NodeID) SymbolID {
	if !owner.IsValid() {
		return SymbolID{}
	}
	return SymbolID{domain: symbolIDSource, source: owner}
}

func ModuleSymbolID(owner moduleid.ID, kind Kind, key string, occurrence uint64) SymbolID {
	if !owner.IsValid() || kind == "" || key == "" {
		return SymbolID{}
	}
	return SymbolID{domain: symbolIDModule, module: owner, kind: kind, key: key, occurrence: occurrence}
}

func GeneratedSymbolID(owner source.NodeID, role GeneratedSymbolRole) SymbolID {
	if owner.Function() == "" || !owner.IsValid() {
		return SymbolID{}
	}
	switch role {
	case GeneratedForCursor, GeneratedForRangeLimit, GeneratedForRangeOrdinal, GeneratedForSequenceCarrier:
	default:
		return SymbolID{}
	}
	return SymbolID{domain: symbolIDGenerated, source: owner, role: role}
}

func CompilerSymbolID(kind Kind, key string) SymbolID {
	if kind == "" || key == "" {
		return SymbolID{}
	}
	return SymbolID{domain: symbolIDCompiler, kind: kind, key: key}
}

// ProjectedSymbolID identifies a tooling-only symbol synthesized from semantic
// type information rather than published into compiler evidence.
func ProjectedSymbolID(kind Kind, key string) SymbolID {
	if kind == "" || key == "" {
		return SymbolID{}
	}
	return SymbolID{domain: symbolIDProjected, kind: kind, key: key}
}

func (id SymbolID) IsValid() bool {
	switch id.domain {
	case symbolIDSource:
		return id.source.IsValid()
	case symbolIDModule:
		return id.module.IsValid() && id.kind != "" && id.key != ""
	case symbolIDGenerated:
		return id.source.Function() != "" && id.role != GeneratedSymbolInvalid
	case symbolIDCompiler, symbolIDProjected:
		return id.kind != "" && id.key != ""
	default:
		return false
	}
}

func (id SymbolID) String() string {
	switch id.domain {
	case symbolIDSource:
		return moduleid.Frame("source", id.source.String())
	case symbolIDModule:
		return moduleid.Frame("module", id.module.String(), string(id.kind), id.key, strconv.FormatUint(id.occurrence, 10))
	case symbolIDGenerated:
		return moduleid.Frame("generated", id.source.String(), strconv.FormatUint(uint64(id.role), 10))
	case symbolIDCompiler:
		return moduleid.Frame("compiler", string(id.kind), id.key)
	case symbolIDProjected:
		return moduleid.Frame("projected", string(id.kind), id.key)
	default:
		return "invalid"
	}
}
