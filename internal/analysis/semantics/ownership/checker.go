package ownership

import (
	"fmt"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
)

type checker struct {
	ctx         *context.CompilerContext
	module      *context.Module
	moved       map[symbols.SymbolID]struct{}
	borrowScope *borrowScope
}

type borrowScope struct {
	parent  *borrowScope
	shared  map[symbols.SymbolID]int
	mutable map[symbols.SymbolID]struct{}
}

func Check(ctx *context.CompilerContext, module *context.Module) {
	if ctx == nil || module == nil || module.AST == nil {
		return
	}
	c := &checker{
		ctx:    ctx,
		module: module,
	}
	c.checkModule()
}

func (c *checker) checkModule() {
	if c == nil || c.module == nil || c.module.AST == nil || c.module.ModuleScope == nil {
		return
	}
	for _, decl := range c.module.AST.Decls {
		fn, ok := decl.(*ast.FnDecl)
		if !ok || fn == nil || fn.Body == nil {
			continue
		}
		sym, found := c.module.ModuleScope.Lookup(fn.Name.Name)
		if !found || sym == nil || sym.Scope == nil {
			continue
		}
		c.checkFunction(sym, fn)
	}
}

func (c *checker) checkFunction(sym *symbols.Symbol, fn *ast.FnDecl) {
	if c == nil || sym == nil || fn == nil || fn.Body == nil || sym.Scope == nil {
		return
	}
	prevMoved := c.moved
	prevBorrowScope := c.borrowScope
	c.moved = make(map[symbols.SymbolID]struct{})
	c.borrowScope = &borrowScope{
		shared:  make(map[symbols.SymbolID]int),
		mutable: make(map[symbols.SymbolID]struct{}),
	}
	defer func() {
		c.moved = prevMoved
		c.borrowScope = prevBorrowScope
	}()
	c.checkBlock(sym.Scope.(*table.Scope), fn.Body)
}

func (c *checker) checkBlock(parentScope *table.Scope, block *ast.BlockStmt) {
	if c == nil || block == nil {
		return
	}
	scope := parentScope
	if s, ok := c.module.BlockScopes[block]; ok && s != nil {
		scope = s
	}
	prevBorrowScope := c.borrowScope
	if prevBorrowScope == nil {
		c.borrowScope = &borrowScope{
			shared:  make(map[symbols.SymbolID]int),
			mutable: make(map[symbols.SymbolID]struct{}),
		}
	} else {
		c.borrowScope = &borrowScope{
			parent:  prevBorrowScope,
			shared:  make(map[symbols.SymbolID]int),
			mutable: make(map[symbols.SymbolID]struct{}),
		}
	}
	defer func() {
		c.borrowScope = prevBorrowScope
	}()
	for _, stmt := range block.Stmts {
		c.checkStmt(scope, stmt)
	}
}

func (c *checker) checkStmt(scope *table.Scope, stmt ast.Stmt) {
	if c == nil || stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		c.checkBlock(scope, node)
	case *ast.LetDecl:
		c.checkBinding(scope, node.Value)
	case *ast.ConstDecl:
		c.checkBinding(scope, node.Value)
	case *ast.ReturnStmt:
		c.checkReturn(scope, node.Value)
	case *ast.IfStmt:
		c.checkExpr(scope, node.Cond)
		c.checkBlock(scope, node.Then)
		c.checkStmt(scope, node.Else)
	case *ast.ExprStmt:
		c.checkExpr(scope, node.Expr)
	default:
		// No ownership action required for this statement kind.
		// If you are adding a new ast.Stmt that carries expressions,
		// add a case above so ownership analysis is not silently skipped.
		_ = node
	}
}

func (c *checker) checkBinding(scope *table.Scope, value ast.Expr) {
	if c == nil || scope == nil || value == nil {
		return
	}
	if sym, mutable := c.borrowedSymbol(scope, value); sym != nil {
		c.addStoredBorrow(sym, mutable, value)
		return
	}
	c.consumeByValue(scope, value)
	c.checkExpr(scope, value)
}

func (c *checker) checkReturn(scope *table.Scope, value ast.Expr) {
	if c == nil || scope == nil || value == nil {
		return
	}
	if _, ok := value.(*ast.BorrowExpr); ok {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, value, diagnostics.ErrBorrowEscape,
			"borrowed reference escapes by return")
		return
	}
	c.consumeByValue(scope, value)
	c.checkExpr(scope, value)
}

func (c *checker) checkExpr(scope *table.Scope, expr ast.Expr) {
	if c == nil || scope == nil || expr == nil {
		return
	}
	switch node := expr.(type) {
	case *ast.NumberLit, *ast.StringLit:
		return
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			return
		}
		if c.isMoved(sym) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrUseAfterMove,
				fmt.Sprintf("use of moved value `%s`", node.Name))
			return
		}
		if c.hasMutableBorrow(sym) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrBorrowConflict,
				fmt.Sprintf("cannot use `%s` while it is mutably borrowed", node.Name))
		}
	case *ast.ScopeResolution:
		return
	case *ast.UnaryExpr:
		c.checkExpr(scope, node.Expr)
	case *ast.BinaryExpr:
		c.checkExpr(scope, node.Left)
		c.checkExpr(scope, node.Right)
	case *ast.AsExpr:
		c.checkExpr(scope, node.Expr)
	case *ast.BorrowExpr:
		if sym, mutable := c.borrowedSymbol(scope, node); sym != nil {
			c.validateBorrow(sym, mutable, node)
		} else {
			c.checkExpr(scope, node.Expr)
		}
	case *ast.CallExpr:
		c.checkExpr(scope, node.Callee)
		c.checkCall(scope, node)
	}
}

func (c *checker) checkCall(scope *table.Scope, call *ast.CallExpr) {
	if c == nil || scope == nil || call == nil {
		return
	}
	calleeType := c.exprType(scope, call.Callee)
	fnType, ok := calleeType.(*typeinfo.FuncType)
	if !ok || fnType == nil {
		for _, arg := range call.Args {
			c.checkExpr(scope, arg)
		}
		return
	}
	for i, arg := range call.Args {
		if i >= len(fnType.Params) {
			c.checkExpr(scope, arg)
			continue
		}
		paramType := fnType.Params[i]
		if _, ok := paramType.(*typeinfo.RefType); ok {
			if sym, mutable := c.borrowedSymbol(scope, arg); sym != nil {
				c.validateBorrow(sym, mutable, arg)
			} else {
				c.checkExpr(scope, arg)
			}
			continue
		}
		c.consumeByValue(scope, arg)
		c.checkExpr(scope, arg)
	}
}

func (c *checker) exprType(scope *table.Scope, expr ast.Expr) symbols.Type {
	if c == nil || scope == nil || expr == nil {
		return nil
	}
	switch node := expr.(type) {
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			return nil
		}
		typ, _ := symbols.GetSymbolType(sym)
		return typ
	case *ast.ScopeResolution:
		return c.qualifiedScopeType(node)
	case *ast.AsExpr:
		return typeinfo.TypeFromSyntax(node.TypeExpr)
	case *ast.BorrowExpr:
		return &typeinfo.RefType{
			Mutable: node.Mutable,
			Target:  c.exprType(scope, node.Expr),
		}
	case *ast.CallExpr:
		if fnType, ok := c.exprType(scope, node.Callee).(*typeinfo.FuncType); ok && fnType != nil {
			return fnType.Return
		}
	}
	return nil
}

func (c *checker) qualifiedScopeType(node *ast.ScopeResolution) typeinfo.Type {
	if c == nil || c.module == nil || c.ctx == nil || node == nil {
		return nil
	}
	imp, ok := c.module.Imports[node.Module.Name]
	if !ok {
		return nil
	}
	mod, ok := c.ctx.ModuleByKey(imp.Key)
	if !ok || mod == nil || mod.ModuleScope == nil {
		return nil
	}
	sym, ok := mod.ModuleScope.LookupLocal(node.Name.Name)
	if !ok || sym == nil {
		return nil
	}
	typ, _ := symbols.GetSymbolType(sym)
	return typ
}

func (c *checker) isMoved(sym *symbols.Symbol) bool {
	if c == nil || sym == nil || c.moved == nil {
		return false
	}
	_, ok := c.moved[sym.ID]
	return ok
}

func (c *checker) moveSymbol(sym *symbols.Symbol, site ast.Node) {
	if c == nil || sym == nil {
		return
	}
	if typeinfo.IsCopyType(c.symbolType(sym)) {
		return
	}
	if c.hasAnyBorrow(sym) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, site, diagnostics.ErrBorrowConflict,
			fmt.Sprintf("cannot move `%s` while it is borrowed", sym.Name))
		return
	}
	if c.moved == nil {
		c.moved = make(map[symbols.SymbolID]struct{})
	}
	c.moved[sym.ID] = struct{}{}
}

func (c *checker) symbolType(sym *symbols.Symbol) typeinfo.Type {
	if sym == nil {
		return nil
	}
	typ, _ := symbols.GetSymbolType(sym)
	return typ
}

func (c *checker) hasMutableBorrow(sym *symbols.Symbol) bool {
	if c == nil || sym == nil {
		return false
	}
	for scope := c.borrowScope; scope != nil; scope = scope.parent {
		if _, ok := scope.mutable[sym.ID]; ok {
			return true
		}
	}
	return false
}

func (c *checker) sharedBorrowCount(sym *symbols.Symbol) int {
	if c == nil || sym == nil {
		return 0
	}
	total := 0
	for scope := c.borrowScope; scope != nil; scope = scope.parent {
		total += scope.shared[sym.ID]
	}
	return total
}

func (c *checker) hasAnyBorrow(sym *symbols.Symbol) bool {
	return c.hasMutableBorrow(sym) || c.sharedBorrowCount(sym) > 0
}

// validateBorrow performs the pre-conditions shared by both stored and
// ephemeral borrow checks:
//  1. The source must not have been moved.
//  2. A mutable borrow requires a `let mut` binding and no existing borrows.
//  3. A shared borrow requires no active mutable borrow.
//
// Returns true when the borrow is valid, false when an error was emitted.
func (c *checker) validateBorrow(sym *symbols.Symbol, mutable bool, site ast.Node) bool {
	if c == nil || sym == nil || site == nil {
		return false
	}
	if c.isMoved(sym) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, site, diagnostics.ErrUseAfterMove,
			fmt.Sprintf("cannot borrow moved value `%s`", sym.Name))
		return false
	}
	if mutable {
		if !c.isMutableBorrowSource(sym) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, site, diagnostics.ErrBorrowConflict,
				fmt.Sprintf("cannot mutably borrow immutable value `%s`", sym.Name))
			return false
		}
		if c.hasAnyBorrow(sym) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, site, diagnostics.ErrBorrowConflict,
				fmt.Sprintf("cannot mutably borrow `%s` because it is already borrowed", sym.Name))
			return false
		}
		return true
	}
	if c.hasMutableBorrow(sym) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, site, diagnostics.ErrBorrowConflict,
			fmt.Sprintf("cannot shared-borrow `%s` while it is mutably borrowed", sym.Name))
		return false
	}
	return true
}

// addStoredBorrow validates and records a borrow that outlives the expression
// (i.e. the reference is stored in a binding).
func (c *checker) addStoredBorrow(sym *symbols.Symbol, mutable bool, site ast.Node) {
	if !c.validateBorrow(sym, mutable, site) {
		return
	}
	if mutable {
		c.borrowScope.mutable[sym.ID] = struct{}{}
	} else {
		c.borrowScope.shared[sym.ID]++
	}
}

// isMutableBorrowSource returns true if sym was declared as a mutable binding.
func (c *checker) isMutableBorrowSource(sym *symbols.Symbol) bool {
	if sym == nil {
		return false
	}
	switch node := sym.ASTNode.(type) {
	case *ast.LetDecl:
		return node != nil && node.IsMutable
	default:
		return false
	}
}

// borrowedSymbol extracts the symbol being borrowed from a BorrowExpr,
// returning (sym, isMutable). Returns (nil, false) for any other expression.
func (c *checker) borrowedSymbol(scope *table.Scope, expr ast.Expr) (*symbols.Symbol, bool) {
	borrow, ok := expr.(*ast.BorrowExpr)
	if !ok || borrow == nil {
		return nil, false
	}
	id, ok := borrow.Expr.(*ast.Ident)
	if !ok || id == nil {
		return nil, false
	}
	sym, found := scope.Lookup(id.Name)
	if !found || sym == nil {
		return nil, false
	}
	return sym, borrow.Mutable
}

func (c *checker) consumeByValue(scope *table.Scope, expr ast.Expr) {
	if c == nil || scope == nil || expr == nil {
		return
	}
	switch node := expr.(type) {
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			return
		}
		c.moveSymbol(sym, node)
	case *ast.AsExpr:
		c.consumeByValue(scope, node.Expr)
	}
}
