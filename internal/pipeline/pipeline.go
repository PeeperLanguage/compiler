package pipeline

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/cfg"
	"compiler/internal/analysis/semantics/collector"
	"compiler/internal/analysis/semantics/resolver"
	"compiler/internal/analysis/semantics/typechecher"
	"compiler/internal/backend/llvm"
	"compiler/internal/context"
	"compiler/internal/ir/hir_fold"
	"compiler/internal/ir/hir_lower"
	"compiler/internal/ir/mir"
	"errors"
	"strings"
)

// Ordered phase execution for one compiler context.
type Pipeline struct {
	ctx *context.CompilerContext
}

// Bind a pipeline to shared compiler state.
func New(ctx *context.CompilerContext) *Pipeline {
	return &Pipeline{ctx: ctx}
}

// Run the central lex -> parse -> analyze -> HIR -> MIR -> LLVM flow.
func (p *Pipeline) Run(entry *context.Module) error {
	if p == nil || p.ctx == nil || entry == nil {
		return errors.New("empty pipeline")
	}

	p.ctx.AddModule(entry)
	var diag *diagnostics.DiagnosticBag
	if p.ctx != nil {
		diag = p.ctx.Diagnostics
	}

	loader := newModuleLoader(p.ctx)
	if preludeMod, ok := p.ctx.ModuleByKey("stdlib:prelude/global"); ok {
		if err := loader.Load(preludeMod); err != nil {
			return err
		}
	}
	if err := loader.Load(entry); err != nil {
		return err
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
		if module == nil || module.AST == nil {
			continue
		}
		collector.Collect(p.ctx, module)
		resolver.Resolve(p.ctx, module)
		typechecher.Check(p.ctx, module)

		modhir := hir_lower.GenerateHIR(p.ctx, module)
		if modhir == nil {
			continue
		}
		modhir = hir_fold.ApplyConstantFolding(modhir, diag)
		module.HIR = modhir
		cfg.AnalyzeModule(modhir, diag)
		if diag != nil && diag.HasErrors() {
			continue
		}

		modmir := mir.GenerateMIR(module.HIR, module.ModuleScope)
		module.MIR = modmir
		module.LLVMIR = llvm.GenerateLLVMIR(modmir, diag)

		if module.Key == "stdlib:prelude/global" {
			if module.ModuleScope != nil {
				for _, sym := range module.ModuleScope.Symbols() {
					_ = p.ctx.GlobalScope.Declare(sym)
				}
			}
		}
	}
	return nil
}
