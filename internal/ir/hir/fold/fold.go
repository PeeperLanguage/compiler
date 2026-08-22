package fold

import (
	"fmt"

	"compiler/internal/constvalue"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"maps"
)

// ApplyTypedExpressionFolding folds typed HIR expressions without simplifying
// source-written control-flow structure.
func ApplyTypedExpressionFolding(mod *hir.Module) *hir.Module {
	if mod == nil {
		return nil
	}
	for _, fn := range mod.Funcs {
		if fn == nil || fn.Body == nil {
			continue
		}
		fn.Body = foldBlock(mod.Types, fn.Body, nil)
	}
	return mod
}

func foldBlock(types *ir.TypeTable, block *hir.Block, parentEnv map[string]constvalue.Value) *hir.Block {
	if block == nil {
		return nil
	}
	out := &hir.Block{
		Stmts:    make([]hir.Stmt, 0, len(block.Stmts)),
		NodeID:   block.NodeID,
		Location: block.Location,
	}
	env := cloneConstEnv(parentEnv)
	for _, stmt := range block.Stmts {
		if stmt == nil {
			continue
		}
		folded := foldStmt(types, stmt, env)
		out.Stmts = append(out.Stmts, folded...)
	}
	return out
}

func foldStmt(types *ir.TypeTable, stmt hir.Stmt, env map[string]constvalue.Value) []hir.Stmt {
	switch node := stmt.(type) {
	case *hir.Block:
		return []hir.Stmt{foldBlock(types, node, env)}
	case *hir.Binding:
		value := ir.FoldExpr(types, node.Value, env)
		out := &hir.Binding{Name: node.Name, Constant: node.Constant, Type: node.Type, Value: value, NodeID: node.NodeID, SymbolID: node.SymbolID, Location: node.Location}
		if node.Constant {
			if folded, ok := ir.ConstValueOf(types, value); ok {
				env[node.Name] = folded
			}
		}
		return []hir.Stmt{out}
	case *hir.ExprStmt:
		return []hir.Stmt{&hir.ExprStmt{Value: ir.FoldExpr(types, node.Value, env), NodeID: node.NodeID, ValueNodeID: node.ValueNodeID, Location: node.Location}}
	case *hir.Assign:
		return []hir.Stmt{&hir.Assign{
			Target:     ir.FoldPlace(types, node.Target, env),
			Value:      ir.FoldExpr(types, node.Value, env),
			DropTarget: node.DropTarget,
			NodeID:     node.NodeID,
			Location:   node.Location,
		}}
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
		thenBlock := foldBlock(types, node.Then, env)
		var elseStmt hir.Stmt
		if node.Else != nil {
			foldedElse := foldStmt(types, node.Else, cloneConstEnv(env))
			if len(foldedElse) == 1 {
				elseStmt = foldedElse[0]
			} else if len(foldedElse) > 1 {
				elseStmt = &hir.Block{Stmts: foldedElse, NodeID: hir.NodeIDOf(node.Else), Location: hir.LocOf(node.Else)}
			}
		}
		cond := ir.FoldExpr(types, node.Cond, env)
		return []hir.Stmt{&hir.If{Cond: cond, Then: thenBlock, Else: elseStmt, NodeID: node.NodeID, Location: node.Location}}
	case *hir.For:
		var cond ir.Expr
		if node.Cond != nil {
			cond = ir.FoldExpr(types, node.Cond, env)
		}
		return []hir.Stmt{&hir.For{Cond: cond, Body: foldBlock(types, node.Body, cloneConstEnv(env)), NodeID: node.NodeID, Location: node.Location}}
	default:
		panic(fmt.Sprintf("unhandled HIR statement %T in typed folding", stmt))
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
