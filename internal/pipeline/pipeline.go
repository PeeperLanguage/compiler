package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"compiler/internal/backend/llvm"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir/fold"
	"compiler/internal/ir/hir/lower"
	"compiler/internal/ir/mir"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	preludepkg "compiler/internal/prelude"
	"compiler/internal/problems"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/definiteinit"
	"compiler/internal/semantics/ownership"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typechecker"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/semantics/usage"
)

// Run the central lex -> parse -> analyze -> HIR -> MIR -> LLVM flow.
func Run(ctx *project.CompilerContext, entry *project.Module) error {
	if ctx == nil || entry == nil {
		return errors.New("empty pipeline")
	}

	entry.IsEntry = true
	// Explicit entry content must replace any overlay stub registered for same ID.
	ctx.AddModule(entry)
	ctx.CompletedProjectPhase = phase.Load
	diag := ctx.Diagnostics
	loadDiag := diag.BeginPhase(phase.Load, "")
	finalDiag := diag.BeginPhase(phase.Finalize, "")

	loader := &moduleLoader{
		ctx:       ctx,
		scheduled: make(map[moduleid.ID]string),
	}
	preludeID := moduleid.ID{}
	if preludeMod, ok := ctx.ModuleByID(preludepkg.ModuleID(ctx)); ok {
		if err := loader.Load(preludeMod); err != nil {
			return err
		}
		preludeID = preludeMod.ID
	}
	if err := loader.Load(entry); err != nil {
		return err
	}

	// Ensure topo-sort puts prelude first by making all non-prelude modules
	// depend on it. This removes the need for any special-case ordering logic.
	if preludeID.Valid() {
		for _, mod := range ctx.Modules() {
			if mod != nil && mod.ID != preludeID {
				if ctx.Graph != nil {
					ctx.Graph.AddEdge(graph.NodeID(mod.ID.String()), graph.NodeID(preludeID.String()))
				}
			}
		}
	}

	modules := ctx.Modules()
	moduleIndex := make(map[graph.NodeID]*project.Module, len(modules))
	moduleIDs := make([]graph.NodeID, 0, len(modules))
	for _, mod := range modules {
		if mod == nil || !mod.ID.Valid() {
			continue
		}
		id := graph.NodeID(mod.ID.String())
		moduleIDs = append(moduleIDs, id)
		moduleIndex[id] = mod
	}

	var (
		orderedIDs []graph.NodeID
		cycles     [][]graph.NodeID
	)
	if ctx.Graph != nil {
		orderedIDs, cycles = ctx.Graph.TopoSort(moduleIDs)
	}
	if len(cycles) > 0 {
		for _, cycle := range cycles {
			msg := "cyclic import detected"
			if len(cycle) > 0 {
				parts := make([]string, 0, len(cycle))
				for _, id := range cycle {
					module := moduleIndex[id]
					if module == nil {
						continue
					}
					name := module.FilePath
					if name == "" {
						name = module.ID.ImportPath
					}
					parts = append(parts, name)
				}
				msg = "cyclic import detected: " + strings.Join(parts, " -> ")
			}
			loadDiag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrCyclicImport))
		}
		return nil
	}

	orderedModules := make([]*project.Module, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		module := moduleIndex[id]
		if module != nil && module.ID.Valid() {
			orderedModules = append(orderedModules, module)
		}
	}
	var prelude *project.Module
	if preludeID.Valid() {
		prelude = moduleIndex[graph.NodeID(preludeID.String())]
	}
	preludeInjected := advanceModulesThrough(ctx, orderedModules, prelude, prelude == nil, phase.Ownership, diag)
	ctx.CompletedProjectPhase = phase.Ownership
	if diag != nil && diag.HasErrors() {
		return nil
	}
	if err := requireScheduledModulesAtLeast(orderedModules, loader.scheduled, phase.Ownership); err != nil {
		return err
	}
	for _, module := range orderedModules {
		if module == nil || module.Phase < phase.Ownership || module.Phase >= phase.Usage {
			continue
		}
		usageDiag := diag.BeginPhase(phase.Usage, module.ID.String())
		usage.Analyze(ctx.WithDiagnostics(usageDiag), module)
		module.Phase = phase.Usage
		ctx.Metrics.AddPhaseAdvance()
	}
	if err := requireScheduledModulesAtLeast(orderedModules, loader.scheduled, phase.Usage); err != nil {
		return err
	}
	ctx.CompletedProjectPhase = phase.Usage
	if ctx.Config.RequireEntrypoint {
		validateProgramEntrypoint(entry, diag.AppendPhase(phase.Usage, entry.ID.String()))
		if diag.HasErrors() {
			return nil
		}
	}
	advanceModulesThrough(ctx, orderedModules, prelude, preludeInjected, phase.Backend, diag)
	if diag != nil && diag.HasErrors() {
		return nil
	}
	// A scheduler stall without user diagnostics is an internal pipeline failure,
	// not a successful partial compilation.
	if err := requireScheduledModulesAtLeast(orderedModules, loader.scheduled, phase.Backend); err != nil {
		return err
	}
	ctx.CompletedProjectPhase = phase.Backend
	mirModules := make([]*mir.Module, 0, len(orderedModules))
	for _, module := range orderedModules {
		if module != nil && module.MIR != nil {
			mirModules = append(mirModules, module.MIR)
		}
	}
	llvm.ValidateRuntimeSymbols(mirModules, finalDiag)
	ctx.CompletedProjectPhase = phase.Finalize
	return nil
}

func validateProgramEntrypoint(entry *project.Module, diag *diagnostics.DiagnosticBag) {
	const message = "program entrypoint must be a local body-backed `fn main()` or `fn main() -> i32`"
	if entry == nil || entry.ModuleScope == nil {
		diag.AddError(diagnostics.ErrInvalidEntrypoint, message, nil, "")
		return
	}

	sym, found := entry.ModuleScope.LookupLocal("main")
	if !found || sym == nil || sym.Kind != symbols.SymbolFunc {
		diag.AddError(diagnostics.ErrInvalidEntrypoint, message, nil, "")
		return
	}
	decl, declOK := sym.ASTNode.(*ast.FnDecl)
	fnType, typeOK := sym.Type.(*typeinfo.FuncType)
	validReturn := typeOK && fnType.Return == nil
	if typeOK && fnType.Return != nil {
		integer, ok := fnType.Return.(*typeinfo.IntegerType)
		validReturn = ok && integer.Signed && integer.Bits == 32
	}
	if !declOK || decl == nil || decl.Receiver != nil || decl.Body == nil || len(decl.TypeParams) != 0 || !typeOK || len(fnType.Params) != 0 || !validReturn {
		diag.AddError(diagnostics.ErrInvalidEntrypoint, message, sym.Location, "invalid program entrypoint")
	}
}

func advanceModulesThrough(ctx *project.CompilerContext, orderedModules []*project.Module, prelude *project.Module, preludeInjected bool, lastPhase phase.Phase, diag *diagnostics.DiagnosticBag) bool {
	for {
		if !preludeInjected && prelude != nil && prelude.ModuleScope != nil && prelude.Phase >= phase.Collected {
			// Inject prelude as soon as its module scope exists. Other modules can
			// then resolve global prelude names while later binding updates the same
			// symbol objects in place.
			injectPreludeSymbols(ctx, prelude, diag)
			preludeInjected = true
		}

		ready := make([]*project.Module, 0, len(orderedModules))
		for _, module := range orderedModules {
			if module != nil && module.Phase < lastPhase && nextModulePhase(module.Phase) <= lastPhase && moduleReadyForNextPhase(ctx, module, prelude, preludeInjected) {
				ready = append(ready, module)
			}
		}
		if len(ready) == 0 {
			break
		}

		var wg sync.WaitGroup
		progress := make(chan bool, len(ready))
		for _, module := range ready {
			wg.Add(1)
			go func(module *project.Module) {
				defer wg.Done()
				progress <- advanceModulePhase(ctx, module, diag)
			}(module)
		}
		wg.Wait()
		close(progress)

		advanced := false
		for ok := range progress {
			advanced = advanced || ok
		}
		invalidateSemanticDependents(ctx, ready)
		if !advanced {
			break
		}
	}
	return preludeInjected
}

// injectPreludeSymbols keeps repeated pipeline runs idempotent while exposing
// a real collision between compiler-owned globals and prelude declarations.
func injectPreludeSymbols(ctx *project.CompilerContext, prelude *project.Module, diag *diagnostics.DiagnosticBag) {
	if ctx == nil || ctx.GlobalScope == nil || prelude == nil || prelude.ModuleScope == nil {
		return
	}
	preludeDiag := diag.AppendPhase(phase.Collected, prelude.ID.String())
	for _, sym := range prelude.ModuleScope.Symbols() {
		if err := ctx.GlobalScope.Declare(sym); err == nil {
			continue
		}
		existing, found := ctx.GlobalScope.LookupLocal(sym.Name)
		if found && existing != nil && existing.ID == sym.ID {
			continue
		}
		problems.ReportRedeclaration(preludeDiag, ctx.GlobalScope, fmt.Sprintf("prelude declaration %q conflicts with an existing global", sym.Name), sym.Name, sym.Location)
	}
}

// requireScheduledModulesAtLeast reports scheduled modules that stalled before
// a required project-wide phase barrier without user diagnostics.
func requireScheduledModulesAtLeast(modules []*project.Module, scheduled map[moduleid.ID]string, phase phase.Phase) error {
	for _, module := range modules {
		if module == nil || module.Phase >= phase {
			continue
		}
		if _, ok := scheduled[module.ID]; !ok {
			continue
		}
		name := module.ID.ImportPath
		if name == "" {
			name = module.FilePath
		}
		return fmt.Errorf("pipeline stopped: module %q at %s phase", name, module.Phase)
	}
	return nil
}

func moduleReadyForNextPhase(ctx *project.CompilerContext, module, prelude *project.Module, preludeInjected bool) bool {
	if ctx == nil || module == nil || module.AST == nil || module.Phase >= phase.Backend {
		return false
	}
	next := nextModulePhase(module.Phase)
	if next == phase.None {
		return false
	}
	if !preludeReadyForPhase(module, prelude, preludeInjected, next) {
		return false
	}
	required := importPrerequisitePhase(next)
	if required == phase.None {
		return true
	}
	for _, imp := range module.Imports {
		imported, ok := ctx.ModuleByID(imp.ID)
		if !ok || imported == nil || imported.Phase < required {
			return false
		}
	}
	return true
}

func preludeReadyForPhase(module, prelude *project.Module, preludeInjected bool, next phase.Phase) bool {
	if module == nil || prelude == nil || module.ID == prelude.ID {
		return true
	}
	switch next {
	case phase.Collected, phase.Bound:
		return true
	case phase.Resolved:
		return preludeInjected && prelude.Phase >= phase.Resolved
	default:
		return preludeInjected && prelude.Phase >= phase.Typechecked
	}
}

func nextModulePhase(current phase.Phase) phase.Phase {
	switch current {
	case phase.Parsed:
		return phase.Collected
	case phase.Collected:
		return phase.Bound
	case phase.Bound:
		return phase.Resolved
	case phase.Resolved:
		return phase.ConstEval
	case phase.ConstEval:
		return phase.Typechecked
	case phase.Typechecked:
		return phase.CFG
	case phase.CFG:
		return phase.FlowTyped
	case phase.FlowTyped:
		return phase.DefiniteInit
	case phase.DefiniteInit:
		return phase.Ownership
	case phase.Ownership:
		return phase.Usage
	case phase.Usage:
		return phase.HIR
	case phase.HIR:
		return phase.MIR
	case phase.MIR:
		return phase.Backend
	default:
		return phase.None
	}
}

func importPrerequisitePhase(next phase.Phase) phase.Phase {
	switch next {
	case phase.Collected:
		return phase.Parsed
	case phase.Bound:
		return phase.Bound
	case phase.ConstEval:
		return phase.Typechecked
	case phase.Typechecked:
		return phase.Typechecked
	case phase.Resolved:
		return phase.Collected
	case phase.CFG:
		return phase.Typechecked
	case phase.FlowTyped:
		return phase.CFG
	case phase.DefiniteInit:
		return phase.FlowTyped
	case phase.Ownership:
		return phase.DefiniteInit
	case phase.Usage:
		return phase.Ownership
	case phase.HIR:
		return phase.Usage
	default:
		return phase.None
	}
}

// advanceModulePhase moves one module exactly one phase forward. Serial Run uses
// same kernel that future dependency-aware scheduling will reuse, so phase
// prerequisites stay centralized in one place.
func advanceModulePhase(ctx *project.CompilerContext, module *project.Module, diag *diagnostics.DiagnosticBag) bool {
	if ctx == nil || module == nil || module.AST == nil {
		return false
	}
	if module.Phase >= phase.Backend {
		return false
	}
	next := nextModulePhase(module.Phase)
	if next == phase.None || next == phase.Usage {
		return false
	}
	phaseDiag := diag.BeginPhase(next, module.ID.String())
	phaseCtx := ctx.WithDiagnostics(phaseDiag)
	if module.Phase < phase.Collected {
		collector.Collect(phaseCtx, module)
		module.Phase = phase.Collected
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Bound {
		binder.Bind(phaseCtx, module)
		module.Phase = phase.Bound
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Resolved {
		resolver.Resolve(phaseCtx, module)
		module.Phase = phase.Resolved
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.ConstEval {
		consteval.Evaluate(phaseCtx, module)
		module.Phase = phase.ConstEval
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Typechecked {
		typechecker.Check(phaseCtx, module)
		consteval.FinalizeValues(phaseCtx, module)
		module.RebuildTypedASTIndex()
		module.SemanticExportFingerprint = project.SemanticExportFingerprint(ctx, module)
		module.Phase = phase.Typechecked
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.CFG {
		module.CFG = cfg.BuildModule(module.AST, cfg.BuildQueries{
			MatchCases:          module.Typechecking.MatchCases,
			LoopGuaranteedEntry: module.Typechecking.ForLoopGuaranteedEntry,
		})
		cfg.Analyze(module.CFG, phaseDiag, func(conditionID, scopeID ir.NodeID) (bool, bool) {
			node := module.TypedASTNodes[ast.NodeID(conditionID)]
			expr, ok := node.(ast.Expr)
			if !ok {
				return false, false
			}
			value, ok := consteval.EvaluateExpr(
				phaseCtx,
				module,
				module.Bindings.BlockScopes[ast.NodeID(scopeID)],
				expr,
				&typeinfo.BoolType{},
			)
			return value != nil && value.Truthy(), ok
		})
		module.Phase = phase.CFG
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.CFG == nil {
		return false
	}
	if module.Phase < phase.FlowTyped {
		module.Flow = typechecker.CheckFlow(phaseCtx, module)
		module.Phase = phase.FlowTyped
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.DefiniteInit {
		definiteinit.Check(
			module.CFG,
			module.TypedASTNodes,
			module.Bindings.BlockScopes,
			module.Bindings.NodeSymbols,
			module.Typechecking.Matches,
			phaseDiag,
		)
		module.Phase = phase.DefiniteInit
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Ownership {
		module.Ownership = ownership.Check(phaseCtx, module)
		module.Phase = phase.Ownership
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Usage {
		return false
	}
	if module.Phase < phase.HIR {
		if diag != nil && diag.HasErrors() {
			return false
		}
		modhir := lower.GenerateHIR(phaseCtx, module)
		if modhir == nil {
			return false
		}
		module.HIR = fold.ApplyTypedExpressionFolding(modhir)
		module.Phase = phase.HIR
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.HIR == nil {
		return false
	}
	if module.Phase < phase.MIR {
		if diag != nil && diag.HasErrors() {
			return false
		}
		module.MIR = mir.GenerateMIR(module.HIR, module.CFG, module.Ownership, module.ModuleScope, module.Constants.ModuleValues)
		module.Phase = phase.MIR
		ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.MIR == nil {
		return false
	}
	if module.Phase >= phase.Backend {
		return false
	}
	module.LLVMIR = llvm.GenerateLLVMIR(module.MIR, phaseDiag, ctx.Target, ctx.Config.BuildDebug)
	module.Phase = phase.Backend
	ctx.Metrics.AddPhaseAdvance()
	return true
}

// invalidateSemanticDependents applies semantic API changes only between
// parallel scheduler batches, after dependency type information is final.
func invalidateSemanticDependents(ctx *project.CompilerContext, advanced []*project.Module) {
	if ctx == nil || ctx.Graph == nil {
		return
	}
	queue := make([]graph.NodeID, 0)
	seen := make(map[graph.NodeID]struct{})
	modules := make(map[graph.NodeID]*project.Module)
	for _, module := range ctx.Modules() {
		if module != nil && module.ID.Valid() {
			modules[graph.NodeID(module.ID.String())] = module
		}
	}
	for _, module := range advanced {
		if module == nil || module.Phase != phase.Typechecked {
			continue
		}
		baseline, ok := ctx.SemanticExportBaseline(module.ID)
		if !ok || baseline == module.SemanticExportFingerprint {
			continue
		}
		id := graph.NodeID(module.ID.String())
		queue = append(queue, id)
		seen[id] = struct{}{}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependentID := range ctx.Graph.Predecessors(current) {
			if _, found := seen[dependentID]; found {
				continue
			}
			seen[dependentID] = struct{}{}
			queue = append(queue, dependentID)
			dependent, found := modules[dependentID]
			if !found || dependent == nil || dependent.Phase < phase.Typechecked {
				continue
			}
			ctx.ResetModule(dependent, phase.Parsed)
			ctx.Metrics.AddDowngradedModule()
		}
	}
}
