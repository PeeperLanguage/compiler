package ast

import (
	"compiler/internal/moduleid"
	"compiler/internal/source"
)

// PublishFunctionIdentities replaces parser-local IDs inside callable
// declarations with stable function-owned source identities. It runs after
// logical module identity is known and before semantic collection.
func PublishFunctionIdentities(owner moduleid.ID, module *Module) {
	if !owner.IsValid() || module == nil {
		return
	}
	occurrences := make(map[string]int)
	ForEachDecl(module, func(declaration Decl) bool {
		function, ok := declaration.(*FnDecl)
		if !ok || function == nil {
			return true
		}
		surface := function.GetDeclSurface()
		occurrence := occurrences[surface]
		occurrences[surface] = occurrence + 1
		functionID := moduleid.FunctionIdentity(owner, surface, occurrence)
		if functionID == "" {
			return true
		}
		var ordinal uint64
		Inspect(function, func(node Node) bool {
			if node != nil {
				ordinal++
				node.SetID(source.FunctionNodeID(functionID, ordinal))
			}
			return true
		})
		return true
	})
}
