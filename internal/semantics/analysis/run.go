package analysis

import "compiler/internal/diagnostics"

// Run performs the complete post-CFG semantic analysis for one module.
// Flow refinements and cleanup decisions are durable; effects and
// definite-initialization state exist only during this call.
func Run(diag *diagnostics.DiagnosticBag, input Input) *Module {
	result := newModule()
	if diag == nil || input.Source == nil || input.CFG == nil {
		return result
	}

	runFlow(diag, input, result)
	effects := buildEffects(input.Source, input.CFG)
	if !diag.HasErrors() {
		if err := effects.validate(input.CFG, input.Source); err != nil {
			diag.AddError(diagnostics.ErrInvalidEvidence,
				"derived semantic effects are malformed: "+err.Error(), nil, "")
		}
	}
	checkInitialization(input.CFG, effects, diag)
	result.cleanup = checkOwnership(diag, ownershipInput{
		Source: input.Source, CFG: input.CFG, Analysis: result, Ops: effects,
		Scope: input.Scope, SymbolIndex: input.SymbolIndex,
	})
	if !diag.HasErrors() {
		if err := result.validate(input.Source, input.SymbolIndex, input.CFG); err != nil {
			diag.AddError(diagnostics.ErrInvalidEvidence,
				"analysis evidence is inconsistent: "+err.Error(), nil, "")
		}
	}
	return result
}
