package hir_fold

import (
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
		fn.Body = foldBlock(fn.Body, diag, nil)
	}
	return mod
}

func foldBlock(block *hir.Block, diag *diagnostics.DiagnosticBag, parentEnv map[string]ir.ConstValue) *hir.Block {
	if block == nil {
		return nil
	}
	out := &hir.Block{
		Stmts:    make([]hir.Stmt, 0, len(block.Stmts)),
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
		folded := foldStmt(stmt, diag, env)
		for _, item := range folded {
			out.Stmts = append(out.Stmts, item)
			if stmtTerminates(item) {
				terminated = true
			}
		}
	}
	return out
}

func foldStmt(stmt hir.Stmt, diag *diagnostics.DiagnosticBag, env map[string]ir.ConstValue) []hir.Stmt {
	switch node := stmt.(type) {
	case *hir.Block:
		return []hir.Stmt{foldBlock(node, diag, env)}
	case *hir.Binding:
		value := ir.FoldExpr(node.Value, env)
		out := &hir.Binding{Name: node.Name, Constant: node.Constant, Value: value, Location: node.Location}
		if node.Constant {
			if folded, ok := ir.ConstValueOf(value); ok {
				env[node.Name] = folded
			}
		}
		return []hir.Stmt{out}
	case *hir.ExprStmt:
		return []hir.Stmt{&hir.ExprStmt{Value: ir.FoldExpr(node.Value, env), Location: node.Location}}
	case *hir.Invalid:
		return []hir.Stmt{node}
	case *hir.Return:
		if node.Value == nil {
			return []hir.Stmt{&hir.Return{Location: node.Location}}
		}
		return []hir.Stmt{&hir.Return{Value: ir.FoldExpr(node.Value, env), Location: node.Location}}
	case *hir.If:
		thenBlock := foldBlock(node.Then, diag, env)
		var elseStmt hir.Stmt
		if node.Else != nil {
			foldedElse := foldStmt(node.Else, diag, cloneConstEnv(env))
			if len(foldedElse) == 1 {
				elseStmt = foldedElse[0]
			} else if len(foldedElse) > 1 {
				elseStmt = &hir.Block{Stmts: foldedElse, Location: hir.LocOf(node.Else)}
			}
		}
		cond := ir.FoldExpr(node.Cond, env)
		if value, ok := ir.ConstValueOf(cond); ok {
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
		return []hir.Stmt{&hir.If{Cond: cond, Then: thenBlock, Else: elseStmt, Location: node.Location}}
	default:
		return []hir.Stmt{stmt}
	}
}

func cloneConstEnv(src map[string]ir.ConstValue) map[string]ir.ConstValue {
	if len(src) == 0 {
		return make(map[string]ir.ConstValue)
	}
	out := make(map[string]ir.ConstValue, len(src))
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
