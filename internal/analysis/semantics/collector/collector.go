package collector

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"fmt"
)

type collector struct {
	ctx    *context.CompilerContext
	module *context.Module
	diag   *diagnostics.DiagnosticBag
}

func (c *collector) collectModule(mod *ast.Module) bool {
	if c == nil || c.ctx == nil || c.module == nil || mod == nil {
		return false
	}
	c.module.ModuleScope = table.New(c.ctx.GlobalScope)
	c.module.Decls = &declinfo.ModuleInfo{
		Functions: make([]*declinfo.Function, 0),
		Externs:   make([]declinfo.ExternDecl, 0),
		NameIndex: make(map[string][]*symbols.Symbol),
	}
	c.module.Bindings = nil
	c.module.Types = nil
	for _, decl := range mod.Decls {
		if !c.collectNode(decl) {
			return false
		}
	}
	return true
}

func (c *collector) collectNode(node ast.Node) bool {
	switch n := node.(type) {
	case *ast.FnDecl:
		return c.collectFnDecl(n)
	default:
		return true
	}
}

func (c *collector) collectFnDecl(fn *ast.FnDecl) bool {
	if c == nil || c.module == nil || fn == nil {
		return false
	}
	if fn.Name == nil || fn.Name.Name == "" {
		common.AddError(c.diag, c.module.FilePath, fn, diagnostics.ErrMissingIdentifier, "function name required")
		return false
	}
	kind := symbols.SymbolFunc
	if fn.Body == nil {
		kind = symbols.SymbolUnknown
	}
	sym := symbols.New(fn.Name.Name, kind, fn)
	if !c.module.ModuleScope.Declare(sym) {
		common.AddError(c.diag, c.module.FilePath, fn, diagnostics.ErrRedeclaredSymbol, "duplicate function `"+fn.Name.Name+"`")
		return false
	}
	c.module.Decls.NameIndex[sym.Name] = append(c.module.Decls.NameIndex[sym.Name], sym)
	if fn.Body == nil {
		c.module.Decls.Externs = append(c.module.Decls.Externs, declinfo.ExternDecl{Symbol: sym, Decl: fn})
		return true
	}
	collected := &declinfo.Function{
		Symbol:     sym,
		Decl:       fn,
		Scope:      table.New(c.module.ModuleScope),
		LocalDecls: make([]declinfo.LocalDecl, 0),
		LocalNames: make(map[string][]declinfo.LocalDecl),
	}
	c.collectBlock(fn.Body, collected)
	c.module.Decls.Functions = append(c.module.Decls.Functions, collected)
	return true
}

func (c *collector) collectBlock(block *ast.BlockStmt, fn *declinfo.Function) {
	if c == nil || block == nil || fn == nil {
		return
	}
	for _, stmt := range block.Stmts {
		c.collectStmt(stmt, fn)
	}
}

func (c *collector) collectStmt(stmt ast.Stmt, fn *declinfo.Function) {
	switch n := stmt.(type) {
	case *ast.BlockStmt:
		c.collectBlock(n, fn)
	case *ast.LetDecl:
		fmt.Printf("declaring %v, %v\n", n, fn)
		c.addLocalDecl(n.Name, fn)
	case *ast.ConstDecl:
		c.addLocalDecl(n.Name, fn)
	case *ast.IfStmt:
		c.collectBlock(n.Then, fn)
		if elseBlock, ok := n.Else.(*ast.BlockStmt); ok {
			c.collectBlock(elseBlock, fn)
		} else if elseIf, ok := n.Else.(*ast.IfStmt); ok {
			c.collectBlock(elseIf.Then, fn)
		}
	}
}

func (c *collector) addLocalDecl(name *ast.Ident, fn *declinfo.Function) {
	if c == nil || fn == nil || name == nil || name.Name == "" {
		return
	}
	local := declinfo.LocalDecl{Name: name.Name, Loc: name.Loc()}
	fn.LocalDecls = append(fn.LocalDecls, local)
	fn.LocalNames[name.Name] = append(fn.LocalNames[name.Name], local)
}

func Collect(ctx *context.CompilerContext, module *context.Module, diag *diagnostics.DiagnosticBag) bool {
	if ctx == nil || module == nil || module.AST == nil || diag == nil {
		return false
	}
	c := &collector{ctx: ctx, module: module, diag: diag}
	return c.collectModule(module.AST)
}