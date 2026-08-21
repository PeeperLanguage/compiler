package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"compiler/internal/backend/llvm"
	"compiler/internal/diagnostics"
	"compiler/internal/graph"
	"compiler/internal/ir/hir/fold"
	"compiler/internal/ir/hir/lower"
	"compiler/internal/ir/mir"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/ownership"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
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
	diag := p.ctx.Diagnostics

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
	if len(cycles) > 0 && diag != nil {
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
			diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrCyclicImport))
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
	preludeInjected := prelude == nil
	for {
		if !preludeInjected && prelude != nil && prelude.ModuleScope != nil && prelude.Phase >= project.PhaseCollected {
			// Inject prelude as soon as its module scope exists. Other modules can
			// then resolve global prelude names while later binding updates the same
			// symbol objects in place.
			for _, sym := range prelude.ModuleScope.Symbols() {
				_ = p.ctx.GlobalScope.Declare(sym)
			}
			preludeInjected = true
		}

		ready := make([]*project.Module, 0, len(orderedModules))
		for _, module := range orderedModules {
			if p.moduleReadyForNextPhase(module, prelude, preludeInjected) {
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
		if !advanced {
			break
		}
	}
	if diag != nil && diag.HasErrors() {
		return nil
	}
	// A scheduler stall without user diagnostics is an internal pipeline failure,
	// not a successful partial compilation.
	if err := requireTerminalModules(orderedModules, loader.scheduled); err != nil {
		return err
	}
	mirModules := make([]*mir.Module, 0, len(orderedModules))
	for _, module := range orderedModules {
		if module != nil && module.MIR != nil {
			mirModules = append(mirModules, module.MIR)
		}
	}
	llvm.ValidateRuntimeSymbols(mirModules, diag, p.ctx.Target)
	return nil
}

// requireTerminalModules reports scheduled modules that stopped before backend
// lowering when user diagnostics did not already explain compilation stopping.
func requireTerminalModules(modules []*project.Module, scheduled map[string]struct{}) error {
	for _, module := range modules {
		if module == nil || module.Phase >= project.PhaseBackend {
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
	if p == nil || module == nil || module.AST == nil || module.Phase >= project.PhaseBackend {
		return false
	}
	next := nextModulePhase(module.Phase)
	if next == project.PhaseNone {
		return false
	}
	if !preludeReadyForPhase(module, prelude, preludeInjected, next) {
		return false
	}
	required := importPrerequisitePhase(next)
	if required == project.PhaseNone {
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

func preludeReadyForPhase(module, prelude *project.Module, preludeInjected bool, next project.ModulePhase) bool {
	if module == nil || prelude == nil || module.Key == prelude.Key {
		return true
	}
	switch next {
	case project.PhaseCollected, project.PhaseBound:
		return true
	default:
		return preludeInjected && prelude.Phase >= project.PhaseResolved
	}
}

func nextModulePhase(current project.ModulePhase) project.ModulePhase {
	switch current {
	case project.PhaseParsed:
		return project.PhaseCollected
	case project.PhaseCollected:
		return project.PhaseBound
	case project.PhaseBound:
		return project.PhaseResolved
	case project.PhaseResolved:
		return project.PhaseConstEval
	case project.PhaseConstEval:
		return project.PhaseTypechecked
	case project.PhaseTypechecked:
		return project.PhaseHIR
	case project.PhaseHIR:
		return project.PhaseCFG
	case project.PhaseCFG:
		return project.PhaseOwnership
	case project.PhaseOwnership:
		return project.PhaseUsage
	case project.PhaseUsage:
		return project.PhaseMIR
	case project.PhaseMIR:
		return project.PhaseBackend
	default:
		return project.PhaseNone
	}
}

func importPrerequisitePhase(next project.ModulePhase) project.ModulePhase {
	switch next {
	case project.PhaseCollected:
		return project.PhaseParsed
	case project.PhaseBound:
		return project.PhaseBound
	case project.PhaseConstEval, project.PhaseTypechecked:
		return project.PhaseConstEval
	case project.PhaseResolved:
		return project.PhaseCollected
	case project.PhaseHIR:
		return project.PhaseTypechecked
	case project.PhaseCFG:
		return project.PhaseHIR
	case project.PhaseOwnership:
		return project.PhaseCFG
	case project.PhaseUsage:
		return project.PhaseOwnership
	default:
		return project.PhaseNone
	}
}

// advanceModulePhase moves one module exactly one phase forward. Serial Run uses
// same kernel that future dependency-aware scheduling will reuse, so phase
// prerequisites stay centralized in one place.
func (p *Pipeline) advanceModulePhase(module *project.Module, diag *diagnostics.DiagnosticBag) bool {
	if p == nil || module == nil || module.AST == nil {
		return false
	}
	if module.Phase >= project.PhaseBackend {
		return false
	}
	if module.Phase < project.PhaseCollected {
		collector.Collect(p.ctx, module)
		module.Phase = project.PhaseCollected
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < project.PhaseBound {
		binder.Bind(p.ctx, module)
		module.Phase = project.PhaseBound
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < project.PhaseResolved {
		resolver.Resolve(p.ctx, module)
		module.Phase = project.PhaseResolved
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < project.PhaseConstEval {
		consteval.Evaluate(p.ctx, module)
		module.Phase = project.PhaseConstEval
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < project.PhaseTypechecked {
		typechecker.Check(p.ctx, module)
		module.Phase = project.PhaseTypechecked
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < project.PhaseHIR {
		modhir := lower.GenerateHIR(p.ctx, module)
		if modhir == nil {
			return false
		}
		modhir = fold.ApplyTypedExpressionFolding(modhir, diag)
		module.HIR = modhir
		module.Phase = project.PhaseHIR
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.HIR == nil {
		return false
	}
	if module.Phase < project.PhaseCFG {
		module.CFG = cfg.BuildModule(module.HIR)
		module.CFGValid = cfg.Analyze(module.CFG, diag)
		module.Phase = project.PhaseCFG
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.CFG == nil {
		return false
	}
	if !module.CFGValid {
		return false
	}
	if module.Phase < project.PhaseOwnership {
		ownership.Check(p.ctx, module)
		module.Phase = project.PhaseOwnership
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < project.PhaseUsage {
		usage.Analyze(p.ctx, module)
		module.Phase = project.PhaseUsage
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.Phase < project.PhaseMIR {
		if diag != nil && diag.HasErrors() {
			return false
		}
		module.MIR = mir.GenerateMIR(module.HIR, module.CFG, module.ModuleScope, module.Semantics.ConstValues)
		module.Phase = project.PhaseMIR
		p.ctx.Metrics.AddPhaseAdvance()
		return true
	}
	if module.MIR == nil {
		return false
	}
	if module.Phase >= project.PhaseBackend {
		return false
	}
	module.LLVMIR = llvm.GenerateLLVMIR(module.MIR, diag, p.ctx.Target, p.ctx.Config.BuildDebug)
	module.Phase = project.PhaseBackend
	p.ctx.Metrics.AddPhaseAdvance()
	return true
}
