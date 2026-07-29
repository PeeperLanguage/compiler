package hir_fold

import (
	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/source"
	"maps"
)

func ApplyConstantFolding(mod *hir.Module, diag *diagnostics.DiagnosticBag) *hir.Module {
	if mod == nil {
		return nil
	}
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Body == nil {
			continue
		}
		fn.Body = foldBlock(mod.Types, fn.Body, diag, nil)
	}
	return mod
}

func foldBlock(types *ir.TypeTable, block *hir.Block, diag *diagnostics.DiagnosticBag, parentEnv map[string]constvalue.Value) *hir.Block {
	if block == nil {
		return nil
	}
	out := &hir.Block{
		Stmts:    make([]hir.Stmt, 0, len(block.Stmts)),
		NodeID:   block.NodeID,
		Location: block.Location,
	}
	env := cloneConstEnv(parentEnv)
	terminated := false
	for _, stmt := range block.Stmts {
		if stmt == nil {
			continue
		}
		if terminated {
			addUnreachableWarning(diag, hir.LocOf(stmt))
			continue
		}
		folded := foldStmt(types, stmt, diag, env)
		for _, item := range folded {
			out.Stmts = append(out.Stmts, item)
			if stmtTerminates(item) {
				terminated = true
			}
		}
	}
	return out
}

func foldStmt(types *ir.TypeTable, stmt hir.Stmt, diag *diagnostics.DiagnosticBag, env map[string]constvalue.Value) []hir.Stmt {
	switch node := stmt.(type) {
	case *hir.Block:
		return []hir.Stmt{foldBlock(types, node, diag, env)}
	case *hir.Binding:
		value := ir.FoldExpr(types, node.Value, env)
		out := &hir.Binding{Name: node.Name, Constant: node.Constant, Value: value, NodeID: node.NodeID, SymbolID: node.SymbolID, Location: node.Location}
		if node.Constant {
			if folded, ok := ir.ConstValueOf(types, value); ok {
				env[node.Name] = folded
			}
		}
		return []hir.Stmt{out}
	case *hir.ExprStmt:
		return []hir.Stmt{&hir.ExprStmt{Value: ir.FoldExpr(types, node.Value, env), NodeID: node.NodeID, Location: node.Location}}
	case *hir.Invalid:
		return []hir.Stmt{node}
	case *hir.Return:
		cleanup := make([]ir.Expr, 0, len(node.Cleanup))
		for _, expr := range node.Cleanup {
			cleanup = append(cleanup, ir.FoldExpr(types, expr, env))
		}
		if node.Value == nil {
			return []hir.Stmt{&hir.Return{Cleanup: cleanup, NodeID: node.NodeID, Location: node.Location}}
		}
		return []hir.Stmt{&hir.Return{Value: ir.FoldExpr(types, node.Value, env), Cleanup: cleanup, NodeID: node.NodeID, Location: node.Location}}
	case *hir.If:
		thenBlock := foldBlock(types, node.Then, diag, env)
		var elseStmt hir.Stmt
		if node.Else != nil {
			foldedElse := foldStmt(types, node.Else, diag, cloneConstEnv(env))
			if len(foldedElse) == 1 {
				elseStmt = foldedElse[0]
			} else if len(foldedElse) > 1 {
				elseStmt = &hir.Block{Stmts: foldedElse, NodeID: hir.NodeIDOf(node.Else), Location: hir.LocOf(node.Else)}
			}
		}
		cond := ir.FoldExpr(types, node.Cond, env)
		if value, ok := ir.ConstValueOf(types, cond); ok {
			if truthy, ok := value.Truthy(); ok && truthy {
				addConstantConditionWarning(diag, node.Location, true)
				if thenBlock == nil {
					return nil
				}
				return []hir.Stmt{thenBlock}
			}
			if _, ok := value.Truthy(); ok {
				addConstantConditionWarning(diag, node.Location, false)
				if elseStmt == nil {
					return nil
				}
				return []hir.Stmt{elseStmt}
			}
		}
		return []hir.Stmt{&hir.If{Cond: cond, Then: thenBlock, Else: elseStmt, NodeID: node.NodeID, Location: node.Location}}
	case *hir.For:
		var cond ir.Expr
		if node.Cond != nil {
			cond = ir.FoldExpr(types, node.Cond, env)
		}
		return []hir.Stmt{&hir.For{Cond: cond, Body: foldBlock(types, node.Body, diag, cloneConstEnv(env)), NodeID: node.NodeID, Location: node.Location}}
	default:
		return []hir.Stmt{stmt}
	}
}

func cloneConstEnv(src map[string]constvalue.Value) map[string]constvalue.Value {
	if len(src) == 0 {
		return make(map[string]constvalue.Value)
	}
	out := make(map[string]constvalue.Value, len(src))
	maps.Copy(out, src)
	return out
}

func stmtTerminates(stmt hir.Stmt) bool {
	switch node := stmt.(type) {
	case *hir.Return:
		return true
	case *hir.Block:
		if node == nil || len(node.Stmts) == 0 {
			return false
		}
		return stmtTerminates(node.Stmts[len(node.Stmts)-1])
	case *hir.If:
		if node == nil || node.Else == nil {
			return false
		}
		return stmtTerminates(node.Then) && stmtTerminates(node.Else)
	case *hir.For:
		return false
	default:
		return false
	}
}

func addConstantConditionWarning(diag *diagnostics.DiagnosticBag, loc *source.Location, value bool) {
	if diag == nil {
		return
	}
	msg := "condition is always false"
	code := diagnostics.WarnConstantConditionFalse
	if value {
		msg = "condition is always true"
		code = diagnostics.WarnConstantConditionTrue
	}
	diag.Add(
		diagnostics.NewWarning(msg).
			WithCode(code).
			WithPrimaryLabel(loc, msg),
	)
}

func addUnreachableWarning(diag *diagnostics.DiagnosticBag, loc *source.Location) {
	if diag == nil {
		return
	}
	diag.Add(
		diagnostics.NewWarning("unreachable code").
			WithCode(diagnostics.WarnUnreachableCode).
			WithPrimaryLabel(loc, "this code is unreachable").
			WithHelp("remove this code or restructure control flow"),
	)
}
