package typechecker

import (
	"compiler/internal/ir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type variantStateFact struct {
	origins      []place.Origin
	cases        []int
	caseCount    int
	dependencies []*symbols.Symbol
}

type originStateFact struct {
	storage []place.Origin
	value   []place.Origin
}

type flowState struct {
	reachable   bool
	variants    []variantStateFact
	references  []originStateFact
	rawPointers []originStateFact
}

type edgeVariantFact struct {
	variant variantStateFact
	order   int
}

func payloadDepthForExpected(src, expected typeinfo.Type) int {
	if src == nil || expected == nil {
		return 0
	}
	if _, optional := typeinfo.Underlying(expected).(*typeinfo.OptionalType); optional {
		return 0
	}
	current := src
	for depth := 1; ; depth++ {
		optional, ok := typeinfo.Underlying(current).(*typeinfo.OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			return 0
		}
		current = optional.Inner
		if typeinfo.Assignable(expected, current) {
			return depth
		}
	}
}

func optionalLayerCount(typ typeinfo.Type) int {
	depth := 0
	for {
		optional, ok := typeinfo.Underlying(typ).(*typeinfo.OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			return depth
		}
		depth++
		typ = optional.Inner
	}
}

func unwrapOptionalLayers(typ typeinfo.Type, depth int) typeinfo.Type {
	for range depth {
		optional, ok := typeinfo.Underlying(typ).(*typeinfo.OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			break
		}
		typ = optional.Inner
	}
	return typ
}

func newFlowState() flowState {
	return flowState{reachable: true}
}

func copyFlowState(src flowState) flowState {
	dst := newFlowState()
	dst.reachable = src.reachable
	for _, fact := range src.variants {
		dst.variants = append(dst.variants, variantStateFact{
			origins: place.CloneOrigins(fact.origins), cases: append([]int(nil), fact.cases...), caseCount: fact.caseCount,
			dependencies: append([]*symbols.Symbol(nil), fact.dependencies...),
		})
	}
	dst.references = cloneOriginFacts(src.references)
	dst.rawPointers = cloneOriginFacts(src.rawPointers)
	return dst
}

func provenVariantCase(facts []variantStateFact, origins []place.Origin, caseCount int) (int, bool) {
	fact, found := variantFact(facts, origins)
	if !found || fact.caseCount != caseCount || len(fact.cases) != 1 {
		return 0, false
	}
	return fact.cases[0], true
}

func provenOptionalPayloadCases(facts []variantStateFact, origins []place.Origin) []int {
	path := make([]int, 0)
	current := place.CloneOrigins(origins)
	for {
		fact, found := variantFact(facts, current)
		if !found || !sameCaseSet(fact.cases, []int{ir.OptionalPresentCase}) {
			return path
		}
		path = append(path, ir.OptionalPresentCase)
		current = place.VariantPayloadOrigins(current, []int{ir.OptionalPresentCase})
	}
}

func restrictVariantFact(st *flowState, added variantStateFact) {
	if st == nil || !st.reachable || len(added.origins) == 0 || added.caseCount <= 0 {
		return
	}
	if len(added.cases) == 0 {
		st.reachable = false
		st.variants = nil
		return
	}
	if len(added.cases) >= added.caseCount {
		return
	}
	for index := range st.variants {
		if place.SameOrigins(st.variants[index].origins, added.origins) {
			if st.variants[index].caseCount != added.caseCount {
				st.reachable = false
				st.variants = nil
				return
			}
			st.variants[index].cases = intersectCaseSets(st.variants[index].cases, added.cases)
			if len(st.variants[index].cases) == 0 {
				st.reachable = false
				st.variants = nil
				return
			}
			st.variants[index].dependencies = mergeDependencies(st.variants[index].dependencies, added.dependencies)
			return
		}
	}
	st.variants = append(st.variants, variantStateFact{
		origins: place.CloneOrigins(added.origins), cases: append([]int(nil), added.cases...), caseCount: added.caseCount,
		dependencies: append([]*symbols.Symbol(nil), added.dependencies...),
	})
}

func alternateEdgeVariantFacts(left, right []edgeVariantFact) []edgeVariantFact {
	out := make([]edgeVariantFact, 0)
	for _, leftFact := range left {
		for _, rightFact := range right {
			if !place.SameOrigins(leftFact.variant.origins, rightFact.variant.origins) || leftFact.variant.caseCount != rightFact.variant.caseCount {
				continue
			}
			cases := unionCaseSets(leftFact.variant.cases, rightFact.variant.cases)
			if len(cases) >= leftFact.variant.caseCount {
				break
			}
			out = append(out, edgeVariantFact{
				variant: variantStateFact{
					origins: place.CloneOrigins(leftFact.variant.origins), cases: cases, caseCount: leftFact.variant.caseCount,
					dependencies: mergeDependencies(
						leftFact.variant.dependencies, rightFact.variant.dependencies,
					),
				},
				order: min(leftFact.order, rightFact.order),
			})
			break
		}
	}
	return out
}

func mergeVariantFacts(left, right []variantStateFact) []variantStateFact {
	out := make([]variantStateFact, 0)
	for _, leftFact := range left {
		for _, rightFact := range right {
			if !place.SameOrigins(leftFact.origins, rightFact.origins) || leftFact.caseCount != rightFact.caseCount {
				continue
			}
			cases := unionCaseSets(leftFact.cases, rightFact.cases)
			if len(cases) >= leftFact.caseCount {
				break
			}
			out = append(out, variantStateFact{
				origins: place.CloneOrigins(leftFact.origins), cases: cases, caseCount: leftFact.caseCount,
				dependencies: mergeDependencies(leftFact.dependencies, rightFact.dependencies),
			})
			break
		}
	}
	return out
}

func mergeFlowStates(left, right flowState) flowState {
	if !left.reachable {
		return copyFlowState(right)
	}
	if !right.reachable {
		return copyFlowState(left)
	}
	merged := newFlowState()
	merged.variants = mergeVariantFacts(left.variants, right.variants)
	merged.references = mergeOriginFacts(left.references, right.references)
	merged.rawPointers = mergeOriginFacts(left.rawPointers, right.rawPointers)
	return merged
}

func sameFlowState(left, right flowState) bool {
	if left.reachable != right.reachable || len(left.variants) != len(right.variants) || len(left.references) != len(right.references) ||
		len(left.rawPointers) != len(right.rawPointers) {
		return false
	}
	for _, fact := range left.variants {
		rightFact, found := variantFact(right.variants, fact.origins)
		if !found || rightFact.caseCount != fact.caseCount || !sameCaseSet(rightFact.cases, fact.cases) {
			return false
		}
	}
	if !sameOriginFacts(left.references, right.references) || !sameOriginFacts(left.rawPointers, right.rawPointers) {
		return false
	}
	return true
}

func variantFact(facts []variantStateFact, origins []place.Origin) (variantStateFact, bool) {
	for _, fact := range facts {
		if place.SameOrigins(fact.origins, origins) {
			return fact, true
		}
	}
	return variantStateFact{}, false
}

func cloneOriginFacts(facts []originStateFact) []originStateFact {
	cloned := make([]originStateFact, len(facts))
	for index, fact := range facts {
		cloned[index] = originStateFact{
			storage: place.CloneOrigins(fact.storage), value: place.CloneOrigins(fact.value),
		}
	}
	return cloned
}

func originValues(facts []originStateFact, storage []place.Origin) []place.Origin {
	for _, fact := range facts {
		if place.SameOrigins(fact.storage, storage) {
			return place.CloneOrigins(fact.value)
		}
	}
	return nil
}

func setOriginFact(facts []originStateFact, storage, value []place.Origin) []originStateFact {
	if len(storage) == 0 || len(value) == 0 {
		return facts
	}
	for index := range facts {
		if place.SameOrigins(facts[index].storage, storage) {
			facts[index].value = place.CloneOrigins(value)
			return facts
		}
	}
	return append(facts, originStateFact{storage: place.CloneOrigins(storage), value: place.CloneOrigins(value)})
}

func mergeOriginFacts(left, right []originStateFact) []originStateFact {
	merged := make([]originStateFact, 0, min(len(left), len(right)))
	for _, leftFact := range left {
		rightValue := originValues(right, leftFact.storage)
		if len(rightValue) == 0 {
			continue
		}
		merged = append(merged, originStateFact{
			storage: place.CloneOrigins(leftFact.storage),
			value:   place.MergeOrigins(leftFact.value, rightValue),
		})
	}
	return merged
}

func sameOriginFacts(left, right []originStateFact) bool {
	if len(left) != len(right) {
		return false
	}
	for _, fact := range left {
		if !place.SameOrigins(fact.value, originValues(right, fact.storage)) {
			return false
		}
	}
	return true
}

func invalidateOriginFacts(facts []originStateFact, mutated []place.Origin) []originStateFact {
	kept := facts[:0]
	for _, fact := range facts {
		if !place.OriginsOverlap(fact.storage, mutated) {
			kept = append(kept, fact)
		}
	}
	return kept
}

func variantCasesExcept(caseCount, excluded int) []int {
	cases := make([]int, 0, max(caseCount-1, 0))
	for caseIndex := range caseCount {
		if caseIndex != excluded {
			cases = append(cases, caseIndex)
		}
	}
	return cases
}

func sameCaseSet(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for _, candidate := range left {
		if !containsCase(right, candidate) {
			return false
		}
	}
	return true
}

func intersectCaseSets(left, right []int) []int {
	out := make([]int, 0, min(len(left), len(right)))
	for _, candidate := range left {
		if containsCase(right, candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func unionCaseSets(left, right []int) []int {
	out := append([]int(nil), left...)
	for _, candidate := range right {
		if !containsCase(out, candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func containsCase(cases []int, candidate int) bool {
	for _, caseIndex := range cases {
		if caseIndex == candidate {
			return true
		}
	}
	return false
}

func mergeDependencies(left, right []*symbols.Symbol) []*symbols.Symbol {
	merged := append([]*symbols.Symbol(nil), left...)
	for _, candidate := range right {
		found := false
		for _, existing := range merged {
			if existing == candidate {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, candidate)
		}
	}
	return merged
}

func invalidateVariantOrigins(st *flowState, mutated []place.Origin) {
	if st == nil || len(mutated) == 0 {
		return
	}
	kept := st.variants[:0]
	for _, fact := range st.variants {
		dependencyMutated := false
		for _, dependency := range fact.dependencies {
			if dependency != nil && place.OriginsOverlap([]place.Origin{{Root: dependency}}, mutated) {
				dependencyMutated = true
				break
			}
		}
		if dependencyMutated {
			continue
		}
		if !place.OriginsOverlap(fact.origins, mutated) {
			kept = append(kept, fact)
			continue
		}
		preserved := true
		for _, mutation := range mutated {
			for _, carrier := range fact.origins {
				if !place.OriginsOverlap([]place.Origin{carrier}, []place.Origin{mutation}) {
					continue
				}
				if !mutationPreservesVariantCase(carrier, mutation) {
					preserved = false
				}
			}
		}
		if preserved {
			kept = append(kept, fact)
		}
	}
	st.variants = kept
}

func mutationPreservesVariantCase(carrier, mutation place.Origin) bool {
	if carrier.Root == nil || carrier.Root != mutation.Root || len(mutation.Projections) <= len(carrier.Projections) {
		return false
	}
	for index := range carrier.Projections {
		if carrier.Projections[index] != mutation.Projections[index] {
			return false
		}
	}
	return mutation.Projections[len(carrier.Projections)].Kind == place.OriginVariantPayload
}

func clearFlowScope(scope *symbols.Scope, st *flowState) {
	if scope == nil || st == nil {
		return
	}
	for _, sym := range scope.Symbols() {
		root := []place.Origin{{Root: sym}}
		st.references = clearOriginRoot(st.references, root)
		st.rawPointers = clearOriginRoot(st.rawPointers, root)
		invalidateVariantOrigins(st, root)
	}
}

func clearOriginRoot(facts []originStateFact, root []place.Origin) []originStateFact {
	kept := facts[:0]
	for _, fact := range facts {
		if !place.OriginsOverlap(fact.storage, root) && !place.OriginsOverlap(fact.value, root) {
			kept = append(kept, fact)
		}
	}
	return kept
}
