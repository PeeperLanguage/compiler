package pipeline

import (
	"compiler/internal/analysis/cfg"
	"compiler/internal/backend/llvm"
	"compiler/internal/diagnostics"
	"compiler/internal/graph"
	"compiler/internal/ir/hir_fold"
	"compiler/internal/ir/hir_lower"
	"compiler/internal/ir/mir"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/internal/semantics/usage"
	"compiler/internal/target"
	"errors"
	"strings"
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
					p.ctx.Graph.AddEdge(graph.NodeID(mod.Key), graph.NodeID(preludeKey), graph.EdgeImport)
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
		orderedIDs, cycles = p.ctx.Graph.TopoSort(moduleIDs, graph.EdgeImport)
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

	for _, id := range orderedIDs {
		module := moduleIndex[id]
		if module == nil || module.Key == "" {
			continue
		}
		p.processModule(module, diag)
		// Inject prelude symbols into GlobalScope immediately after prelude is
		// compiled so subsequent modules can resolve them.
		if module.Key == preludeKey && module.ModuleScope != nil {
			for _, sym := range module.ModuleScope.Symbols() {
				_ = p.ctx.GlobalScope.Declare(sym)
			}
		}
	}
	return nil
}

func (p *Pipeline) processModule(module *project.Module, diag *diagnostics.DiagnosticBag) {
	if p == nil || module == nil || module.AST == nil {
		return
	}
	if module.Phase >= project.PhaseBackend {
		return
	}
	collector.Collect(p.ctx, module)
	module.Phase = project.PhaseCollected
	binder.Bind(p.ctx, module)
	module.Phase = project.PhaseBound
	resolver.Resolve(p.ctx, module)
	module.Phase = project.PhaseResolved
	typechecker.Check(p.ctx, module)
	module.Phase = project.PhaseTypechecked
	usage.Analyze(p.ctx, module)
	module.Phase = project.PhaseUsage

	modhir := hir_lower.GenerateHIR(p.ctx, module)
	if modhir == nil {
		return
	}
	modhir = hir_fold.ApplyConstantFolding(modhir, diag)
	module.HIR = modhir
	module.Phase = project.PhaseHIR
	cfg.AnalyzeModule(modhir, diag)
	if diag != nil && diag.HasErrors() {
		return
	}

	modmir := mir.GenerateMIR(module.HIR, module.ModuleScope)
	module.MIR = modmir
	module.Phase = project.PhaseMIR
	targetTriple, err := target.LLVMTriple(p.ctx.Config.TargetOS, p.ctx.Config.TargetArch)
	if err != nil {
		if diag != nil {
			diag.Add(diagnostics.NewError("resolve llvm target triple: " + err.Error()))
		}
		return
	}
	module.LLVMIR = llvm.GenerateLLVMIR(modmir, diag, targetTriple, p.ctx.Config.BuildDebug, p.ctx.Config.TargetOS)
	module.Phase = project.PhaseBackend
}
