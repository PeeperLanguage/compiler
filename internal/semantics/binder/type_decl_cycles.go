package binder

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
)

const (
	graphEdgeTypeValueRef    graph.EdgeKind = "type_value_ref"
	graphEdgeTypeIndirectRef graph.EdgeKind = "type_indirect_ref"
)

func (b *binder) registerTypeDecl(name string, typ ast.TypeExpr) {
	if b == nil || b.ctx == nil || b.ctx.Graph == nil || b.module == nil || name == "" {
		return
	}
	owner := typeDeclNodeID(b.module.Key, name)
	// Value edges require full layout; indirect references do not force target expansion.
	b.addTypeDeclEdges(owner, typ, false)
}

func (b *binder) validateTypeDeclCycles() {
	if b == nil || b.ctx == nil || b.ctx.Graph == nil || b.ctx.Diagnostics == nil || b.module == nil || b.module.ModuleScope == nil {
		return
	}
	nodeIDs := make([]graph.NodeID, 0)
	for _, sym := range b.module.ModuleScope.Symbols() {
		if sym == nil || sym.Kind != symbols.SymbolType {
			continue
		}
		nodeIDs = append(nodeIDs, typeDeclNodeID(b.module.Key, sym.Name))
	}
	if len(nodeIDs) == 0 {
		return
	}
	// Only value-layout edges participate in illegal cycle detection.
	_, cycles := b.ctx.Graph.TopoSort(nodeIDs, graphEdgeTypeValueRef)
	for _, cycle := range cycles {
		if len(cycle) == 0 {
			continue
		}
		firstName := typeDeclNameFromNodeID(cycle[0])
		firstSym, ok := b.module.ModuleScope.LookupLocal(firstName)
		if !ok || firstSym == nil {
			continue
		}
		parts := make([]string, 0, len(cycle))
		for _, id := range cycle {
			name := typeDeclNameFromNodeID(id)
			if name != "" {
				parts = append(parts, name)
			}
		}
		if len(parts) == 0 {
			continue
		}
		b.ctx.Diagnostics.AddError(
			diagnostics.ErrCircularDependency,
			fmt.Sprintf("type declaration cycle: %s", strings.Join(parts, " -> ")),
			firstSym.Location,
			"break the cycle with indirection such as a pointer",
		)
	}
}

func typeDeclNodeID(moduleKey, name string) graph.NodeID {
	if moduleKey == "" || name == "" {
		return ""
	}
	return graph.NodeID("type:" + moduleKey + ":" + name)
}

func (b *binder) addTypeDeclEdges(owner graph.NodeID, typ ast.TypeExpr, indirect bool) {
	if b == nil || b.ctx == nil || b.ctx.Graph == nil || b.module == nil || owner == "" || typ == nil {
		return
	}
	switch node := typ.(type) {
	case *ast.NamedType:
		b.addTypeDeclEdge(owner, b.lookupTypeDeclNodeID(node.Name), indirect)
	case *ast.ScopeResolution:
		b.addTypeDeclEdge(owner, b.lookupQualifiedTypeDeclNodeID(node), indirect)
	case *ast.RawPtrType, *ast.EnumType:
		// These types contain no named storage dependency.
	case *ast.OwnedPtrType:
		// Pointer target is not a layout dependency.
		b.addTypeDeclEdges(owner, node.Target, true)
	case *ast.RefType:
		// Reference target is not owned inline storage.
		b.addTypeDeclEdges(owner, node.Target, true)
	case *ast.OptionalType:
		b.addTypeDeclEdges(owner, node.Inner, indirect)
	case *ast.ArrayType:
		b.addTypeDeclEdges(owner, node.Elem, indirect || node.Shape != ast.ArrayFixed || node.Len == nil)
	case *ast.StructType:
		for _, field := range node.Fields {
			b.addTypeDeclEdges(owner, field.Type, indirect)
		}
	case *ast.FuncType:
		for _, param := range node.Params {
			b.addTypeDeclEdges(owner, param.Type, true)
		}
		b.addTypeDeclEdges(owner, node.Return, true)
	case *ast.InterfaceType:
		for _, method := range node.Methods {
			for _, param := range method.Params {
				b.addTypeDeclEdges(owner, param.Type, true)
			}
			b.addTypeDeclEdges(owner, method.ReturnType, true)
		}
	default:
		panic(fmt.Sprintf("binder type dependencies: unhandled type syntax %T", typ))
	}
}

func (b *binder) addTypeDeclEdge(owner, target graph.NodeID, indirect bool) {
	if target == "" {
		return
	}
	kind := graphEdgeTypeValueRef
	if indirect {
		kind = graphEdgeTypeIndirectRef
	}
	b.ctx.Graph.AddEdge(owner, target, kind)
}

func (b *binder) lookupTypeDeclNodeID(name string) graph.NodeID {
	if b == nil || b.module == nil || b.module.ModuleScope == nil || name == "" {
		return ""
	}
	sym, ok := b.module.ModuleScope.Lookup(name)
	if !ok || sym == nil || sym.Kind != symbols.SymbolType {
		return ""
	}
	return typeDeclNodeID(b.module.Key, sym.Name)
}

func (b *binder) lookupQualifiedTypeDeclNodeID(node *ast.ScopeResolution) graph.NodeID {
	if b == nil || b.ctx == nil || b.module == nil || node == nil || node.Module == nil || node.Name == nil {
		return ""
	}
	resolved, ok := project.LookupImportedSymbol(b.ctx, b.module, node.Module.Name, node.Name.Name)
	if !ok || resolved.Module == nil || resolved.Symbol == nil || resolved.Symbol.Kind != symbols.SymbolType {
		return ""
	}
	return typeDeclNodeID(resolved.Module.Key, resolved.Symbol.Name)
}

func typeDeclNameFromNodeID(id graph.NodeID) string {
	value := string(id)
	const prefix = "type:"
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	last := strings.LastIndexByte(value, ':')
	if last < len(prefix) || last == len(value)-1 {
		return ""
	}
	return value[last+1:]
}
