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
	"compiler/internal/phase"
	"compiler/internal/problems"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/definiteinit"
	"compiler/internal/semantics/ownership"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/semantics/usage"
)

// Ordered phase execution for one compiler project.
type Pipeline struct {
	ctx *project.CompilerContext
}

// Bind a pipeline to shared compiler state.
func New(ctx *project.CompilerContext) *Pipeline {
	return &Pipeline{ctx: ctx}
}

// Run the central lex -> parse -> analyze -> HIR -> MIR -> LLVM flow.
func (p *Pipeline) Run(entry *project.Module) error {
	if p == nil || p.ctx == nil || entry == nil {
		return errors.New("empty pipeline")
	}

	p.ctx.AddModule(entry)
	p.ctx.CompletedProjectPhase = phase.Load
	diag := p.ctx.Diagnostics
	loadDiag := diag.BeginPhase(phase.Load, "")
	finalDiag := diag.BeginPhase(phase.Finalize, "")

	loader := &moduleLoader{
		ctx:       p.ctx,
		scheduled: make(map[string]struct{}),
	}
	preludeKey := ""
	if preludeMod, ok := p.ctx.ModuleByKey("core:prelude/global"); ok {
		if err := loader.Load(preludeMod); err != nil {
			return err
		}
		preludeKey = preludeMod.Key
	}
	if err := loader.Load(entry); err != nil {
		return err
	}

	// Ensure topo-sort puts prelude first by making all non-prelude modules
	// depend on it. This removes the need for any special-case ordering logic.
	if preludeKey != "" {
		for _, mod := range p.ctx.Modules() {
			if mod != nil && mod.Key != preludeKey {
				if p.ctx.Graph != nil {
					p.ctx.Graph.AddEdge(graph.NodeID(mod.Key), graph.NodeID(preludeKey))
				}
			}
		}
	}

	modules := p.ctx.Modules()
	moduleIndex := make(map[graph.NodeID]*project.Module, len(modules))
	moduleIDs := make([]graph.NodeID, 0, len(modules))
	for _, mod := range modules {
		if mod == nil || mod.Key == "" {
			continue
		}
		id := graph.NodeID(mod.Key)
		moduleIDs = append(moduleIDs, id)
		moduleIndex[id] = mod
	}

	var (
		orderedIDs []graph.NodeID
		cycles     [][]graph.NodeID
	)
	if p.ctx.Graph != nil {
		orderedIDs, cycles = p.ctx.Graph.TopoSort(moduleIDs)
	}
	if len(cycles) > 0 {
		for _, cycle := range cycles {
			msg := "cyclic import detected"
			if len(cycle) > 0 {
				parts := make([]string, 0, len(cycle))
				for _, id := range cycle {
					if id != "" {
						parts = append(parts, string(id))
					}
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
		if module != nil && module.Key != "" {
			orderedModules = append(orderedModules, module)
		}
	}
	var prelude *project.Module
	if preludeKey != "" {
		prelude = moduleIndex[graph.NodeID(preludeKey)]
	}
	preludeInjected := p.advanceModulesThrough(orderedModules, prelude, prelude == nil, phase.Ownership, diag)
	p.ctx.CompletedProjectPhase = phase.Ownership
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
		usageDiag := diag.BeginPhase(phase.Usage, module.Key)
		usage.Analyze(p.ctx.WithDiagnostics(usageDiag), module)
		module.Phase = phase.Usage
		p.ctx.Metrics.AddPhaseAdvance()
	}
	if err := requireScheduledModulesAtLeast(orderedModules, loader.scheduled, phase.Usage); err != nil {
		return err
	}
	p.ctx.CompletedProjectPhase = phase.Usage
	p.advanceModulesThrough(orderedModules, prelude, preludeInjected, phase.Backend, diag)
	if diag != nil && diag.HasErrors() {
		return nil
	}
	// A scheduler stall without user diagnostics is an internal pipeline failure,
	// not a successful partial compilation.
	if err := requireScheduledModulesAtLeast(orderedModules, loader.scheduled, phase.Backend); err != nil {
		return err
	}
	p.ctx.CompletedProjectPhase = phase.Backend
	mirModules := make([]*mir.Module, 0, len(orderedModules))
	for _, module := range orderedModules {
		if module != nil && module.MIR != nil {
			mirModules = append(mirModules, module.MIR)
		}
	}
	llvm.ValidateRuntimeSymbols(mirModules, finalDiag, p.ctx.Target)
	p.ctx.CompletedProjectPhase = phase.Finalize
	return nil
}

func (p *Pipeline) advanceModulesThrough(orderedModules []*project.Module, prelude *project.Module, preludeInjected bool, lastPhase phase.Phase, diag *diagnostics.DiagnosticBag) bool {
	for {
		if !preludeInjected && prelude != nil && prelude.ModuleScope != nil && prelude.Phase >= phase.Collected {
			// Inject prelude as soon as its module scope exists. Other modules can
			// then resolve global prelude names while later binding updates the same
			// symbol objects in place.
			p.injectPreludeSymbols(prelude, diag)
			preludeInjected = true
		}

		ready := make([]*project.Module, 0, len(orderedModules))
		for _, module := range orderedModules {
			if module != nil && module.Phase < lastPhase && nextModulePhase(module.Phase) <= lastPhase && p.moduleReadyForNextPhase(module, prelude, preludeInjected) {
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
				progress <- p.advanceModulePhase(module, diag)
			}(module)
		}
		wg.Wait()
		close(progress)

		advanced := false
		for ok := range progress {
			advanced = advanced || ok
		}
		p.invalidateSemanticDependents(ready)
		if !advanced {
			break
		}
	}
	return preludeInjected
}

// injectPreludeSymbols keeps repeated pipeline runs idempotent while exposing
// a real collision between compiler-owned globals and prelude declarations.
func (p *Pipeline) injectPreludeSymbols(prelude *project.Module, diag *diagnostics.DiagnosticBag) {
	if p == nil || p.ctx == nil || p.ctx.GlobalScope == nil || prelude == nil || prelude.ModuleScope == nil {
		return
	}
	preludeDiag := diag.AppendPhase(phase.Collected, prelude.Key)
	for _, sym := range prelude.ModuleScope.Symbols() {
		if err := p.ctx.GlobalScope.Declare(sym); err == nil {
			continue
		}
		existing, found := p.ctx.GlobalScope.LookupLocal(sym.Name)
		if found && existing != nil && existing.ID == sym.ID {
			continue
		}
		problems.ReportRedeclaration(preludeDiag, p.ctx.GlobalScope, fmt.Sprintf("prelude declaration %q conflicts with an existing global", sym.Name), sym.Name, sym.Location)
	}
}

// requireScheduledModulesAtLeast reports scheduled modules that stalled before
// a required project-wide phase barrier without user diagnostics.
func requireScheduledModulesAtLeast(modules []*project.Module, scheduled map[string]struct{}, phase phase.Phase) error {
	for _, module := range modules {
		if module == nil || module.Phase >= phase {
			continue
		}
		if _, ok := scheduled[module.Key]; !ok {
			continue
		}
		name := module.Key
		if name == "" {
			name = module.FilePath
		}
		return fmt.Errorf("pipeline stopped: module %q at %s phase", name, module.Phase)
	}
	return nil
}

func (p *Pipeline) moduleReadyForNextPhase(module, prelude *project.Module, preludeInjected bool) bool {
	if p == nil || module == nil || module.AST == nil || module.Phase >= phase.Backend {
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
		imported, ok := p.ctx.ModuleByKey(imp.Key)
		if !ok || imported == nil || imported.Phase < required {
			return false
		}
	}
	return true
}

func preludeReadyForPhase(module, prelude *project.Module, preludeInjected bool, next phase.Phase) bool {
	if module == nil || prelude == nil || module.Key == prelude.Key {
		return true
	}
	switch next {
	case phase.Collected, phase.Bound:
		return true
	default:
		return preludeInjected && prelude.Phase >= phase.Resolved
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
		return phase.ConstEval
	case phase.Typechecked:
		return phase.Typechecked
	case phase.Resolved:
		return phase.Collected
	case phase.CFG:
		return phase.Typechecked
	case phase.DefiniteInit:
		return phase.CFG
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
func (p *Pipeline) advanceModulePhase(module *project.Module, diag *diagnostics.DiagnosticBag) bool {
	if p == nil || module == nil || module.AST == nil {
		return false
	}
	if module.Phase >= phase.Backend {
		return false
	}
	next := nextModulePhase(module.Phase)
	if next == phase.None || next == phase.Usage {
		return false
	}
	phaseDiag := diag.BeginPhase(next, module.Key)
	phaseCtx := p.ctx.WithDiagnostics(phaseDiag)
	if module.Phase < phase.Collected {
		collector.Collect(phaseCtx, module)
		module.Phase = phase.Collected
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Bound {
		binder.Bind(phaseCtx, module)
		module.Phase = phase.Bound
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Resolved {
		resolver.Resolve(phaseCtx, module)
		module.Phase = phase.Resolved
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.ConstEval {
		consteval.Evaluate(phaseCtx, module)
		module.Phase = phase.ConstEval
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Typechecked {
		typechecker.Check(phaseCtx, module)
		consteval.FinalizeValues(phaseCtx, module)
		module.TypedASTNodes = ast.Index(module.AST)
		module.SemanticExportFingerprint = project.SemanticExportFingerprint(module)
		module.Phase = phase.Typechecked
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.CFG {
		module.CFG = cfg.BuildModule(module.AST)
		cfg.Analyze(module.CFG, phaseDiag, func(conditionID, scopeID ir.NodeID) (bool, bool) {
			node := module.TypedASTNodes[ast.NodeID(conditionID)]
			expr, ok := node.(ast.Expr)
			if !ok {
				return false, false
			}
			value, ok := consteval.EvaluateExpr(
				phaseCtx,
				module,
				module.Semantics.BlockScopes[ast.NodeID(scopeID)],
				expr,
				&typeinfo.BoolType{},
			)
			return value != nil && value.Truthy(), ok
		})
		module.Phase = phase.CFG
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.CFG == nil {
		return false
	}
	if module.Phase < phase.DefiniteInit {
		definiteinit.Check(
			module.CFG,
			module.TypedASTNodes,
			module.Semantics.BlockScopes,
			module.Semantics.ResolvedSymbols,
			phaseDiag,
		)
		module.Phase = phase.DefiniteInit
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < phase.Ownership {
		module.Ownership = ownership.Check(phaseCtx, module)
		module.Phase = phase.Ownership
		p.ctx.Metrics.AddPhaseAdvance()
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
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.HIR == nil {
		return false
	}
	if module.Phase < phase.MIR {
		if diag != nil && diag.HasErrors() {
			return false
		}
		module.MIR = mir.GenerateMIR(module.HIR, module.CFG, module.Ownership, module.ModuleScope, module.Semantics.ConstValues)
		module.Phase = phase.MIR
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.MIR == nil {
		return false
	}
	if module.Phase >= phase.Backend {
		return false
	}
	module.LLVMIR = llvm.GenerateLLVMIR(module.MIR, phaseDiag, p.ctx.Target, p.ctx.Config.BuildDebug)
	module.Phase = phase.Backend
	p.ctx.Metrics.AddPhaseAdvance()
	return true
}

// invalidateSemanticDependents applies semantic API changes only between
// parallel scheduler batches, after dependency type information is final.
func (p *Pipeline) invalidateSemanticDependents(advanced []*project.Module) {
	if p == nil || p.ctx == nil || p.ctx.Graph == nil {
		return
	}
	queue := make([]graph.NodeID, 0)
	seen := make(map[graph.NodeID]struct{})
	for _, module := range advanced {
		if module == nil || module.Phase != phase.Typechecked {
			continue
		}
		baseline, ok := p.ctx.SemanticExportBaseline(module.Key)
		if !ok || baseline == module.SemanticExportFingerprint {
			continue
		}
		id := graph.NodeID(module.Key)
		queue = append(queue, id)
		seen[id] = struct{}{}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependentID := range p.ctx.Graph.Predecessors(current) {
			if _, found := seen[dependentID]; found {
				continue
			}
			seen[dependentID] = struct{}{}
			queue = append(queue, dependentID)
			dependent, found := p.ctx.ModuleByKey(string(dependentID))
			if !found || dependent == nil || dependent.Phase < phase.Typechecked {
				continue
			}
			p.ctx.ResetModule(dependent, phase.Parsed)
			p.ctx.Metrics.AddDowngradedModule()
		}
	}
}
