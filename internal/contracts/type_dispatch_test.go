// This file owns the phase-coverage contract for semantic type kinds.
//
// node_dispatch_test.go covers AST *syntax*: statements, expressions and type
// syntax. This covers the semantic types those lower into — the typeinfo.Type
// family. Adding one and forgetting that it must answer a capability, lower to
// a runtime type, or compare equal used to compile clean and fail much later,
// or silently produce a wrong answer.
package contracts

import (
	"slices"
	"strings"
	"testing"
)

// typeDispatchSite is one function that must answer for every semantic type.
//
// Only sites that must be *total* belong here. A narrow predicate whose default
// is a legitimate answer does not: classifying kinds there would be ceremony
// around a decision that is already correct.
type typeDispatchSite struct {
	file string
	fn   string
	// why records what this site answers, so a contributor reading a failure
	// knows what decision they owe rather than only that one is missing.
	why string
	// omitted classifies kinds this site implements no case for.
	omitted map[string]classification
}

// A missed kind at each of these is silently wrong rather than loudly rejected,
// which is what earns them a contract. IsSizedType and IsLowerableType are
// deliberately absent: their default rejects, so a forgotten kind produces a
// diagnostic rather than a wrong answer.
var typeKindSites = []typeDispatchSite{
	{
		file: "semantics/typeinfo/capability_walk.go",
		fn:   "ownershipCapability",
		why:  "how the type copies and whether scope cleanup must destroy it",
		omitted: map[string]classification{
			"InvalidType": {ignore, "a recovery type makes no capability claim; invalid source never reaches ownership"},
			"UnknownType": {ignore, "an unresolved type makes no capability claim; resolution replaces it first"},
			"NamedType":   {ignore, "a bare name carries no structure to classify; it is replaced by the type it names"},
			"TypeParameterType": {ignore, "conservatively move-on-use with no drop until instantiation-aware " +
				"queries arrive with generic support, as OwnershipCapability documents"},
			"FuncType": {ignore, "a function value is a code pointer owning no storage, so the walk's default of " +
				"move-on-use with no drop is safe. Whether it should copy implicitly is an open language " +
				"question, not a missing case"},
		},
	},
	{
		file: "semantics/typeinfo/relations.go",
		fn:   "SameType",
		why:  "whether two types are the same type",
		omitted: map[string]classification{
			"DefinedType": {contextual, "nominal identity is settled before the switch by sameNominalStruct, and " +
				"Underlying peels the definition away for the structural comparison that follows"},
		},
	},
	{
		file: "ir/hir/lower/lower_types.go",
		fn:   "intern",
		why:  "which runtime type the backend materializes",
		omitted: map[string]classification{
			"EnumType":     {contextual, "a named enum is interned through internDefined, which owns variant layout"},
			"OptionalType": {contextual, "an optional is shaped by loweredRuntimeType before it reaches interning"},
			"TypeParameterType": {reject, "a type parameter has no runtime representation; instantiation must " +
				"substitute it before lowering, and reaching here yields ir.InvalidType"},
		},
	},
}

func TestEverySemanticTypeKindHasAPhaseDecision(t *testing.T) {
	kinds := declaredMarkerKinds(t, "semantics/typeinfo/types.go", "TypeNode")
	if len(kinds) < 10 {
		t.Fatalf("found %d semantic type kinds, expected the full family", len(kinds))
	}
	for _, site := range typeKindSites {
		t.Run(site.fn, func(t *testing.T) {
			handled := handledKindsIn(t, site.file, site.fn, "compiler/internal/semantics/typeinfo", "typeinfo", kinds)
			for _, kind := range kinds {
				entry, classified := site.omitted[kind]
				if slices.Contains(handled, kind) {
					if classified {
						t.Errorf("%s handles %s but still classifies it %s (%q); delete the entry",
							site.fn, kind, entry.decision, entry.reason)
					}
					continue
				}
				if !classified {
					t.Errorf("%s makes no decision about typeinfo.%s; it decides %s, so add a case or declare why the kind is inert",
						site.fn, kind, site.why)
				}
			}
		})
	}
}

func TestSemanticTypeOmissionReasonsNameRealKinds(t *testing.T) {
	kinds := declaredMarkerKinds(t, "semantics/typeinfo/types.go", "TypeNode")
	for _, site := range typeKindSites {
		for kind, entry := range site.omitted {
			if !slices.Contains(kinds, kind) {
				t.Errorf("%s classifies type kind %s that no longer exists", site.fn, kind)
			}
			if strings.TrimSpace(entry.reason) == "" {
				t.Errorf("%s classifies %s as %s without a reason", site.fn, kind, entry.decision)
			}
			if entry.decision.String() == "unknown" {
				t.Errorf("%s classifies %s with an invalid decision", site.fn, kind)
			}
		}
	}
}
