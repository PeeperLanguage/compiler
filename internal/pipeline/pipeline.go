package pipeline

import (
	"compiler/internal/analysis/cfg"
	"compiler/internal/backend/llvm"
	"compiler/internal/diagnostics"
	"compiler/internal/ir/hir_fold"
	"compiler/internal/ir/hir_lower"
	"compiler/internal/ir/mir"
	"compiler/internal/project"
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

	loader := newModuleLoader(p.ctx)
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
				p.ctx.AddDependency(mod.Key, preludeKey)
			}
		}
	}

	ordered, cycles := topoSort(p.ctx.Modules(), p.ctx.DependenciesOf)
	if len(cycles) > 0 && diag != nil {
		for _, cycle := range cycles {
			msg := "cyclic import detected"
			if len(cycle) > 0 {
				msg = "cyclic import detected: " + strings.Join(cycle, " -> ")
			}
			diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrCyclicImport))
		}
		return nil
	}

	for _, module := range ordered {
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
	collector.Collect(p.ctx, module)
	resolver.Resolve(p.ctx, module)
	typechecker.Check(p.ctx, module)
	usage.Analyze(p.ctx, module)

	modhir := hir_lower.GenerateHIR(p.ctx, module)
	if modhir == nil {
		return
	}
	modhir = hir_fold.ApplyConstantFolding(modhir, diag)
	module.HIR = modhir
	cfg.AnalyzeModule(modhir, diag)
	if diag != nil && diag.HasErrors() {
		return
	}

	modmir := mir.GenerateMIR(module.HIR, module.ModuleScope)
	module.MIR = modmir
	targetTriple, err := target.LLVMTriple(p.ctx.Config.TargetOS, p.ctx.Config.TargetArch)
	if err != nil {
		if diag != nil {
			diag.Add(diagnostics.NewError("resolve llvm target triple: " + err.Error()))
		}
		return
	}
	module.LLVMIR = llvm.GenerateLLVMIR(modmir, diag, targetTriple, p.ctx.Config.BuildDebug, p.ctx.Config.TargetOS)
}
