package deps

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
)

func RegisterTypeDecl(ctx *project.CompilerContext, module *project.Module, name string, typ ast.TypeExpr) {
	if ctx == nil || ctx.Graph == nil || module == nil || name == "" {
		return
	}
	owner := typeDeclNodeID(module.Key, name)
	ctx.Graph.AddNode(owner, graph.Node{Kind: graph.NodeTypeDecl})
	// Value edges mean "must know full layout". Pointer edges are recorded as
	// indirect and do not force full expansion of target type.
	addTypeDeclEdges(ctx, module, owner, typ, false)
}

func ValidateTypeDeclCycles(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || ctx.Graph == nil || module == nil || module.ModuleScope == nil || ctx.Diagnostics == nil {
		return
	}
	nodeIDs := make([]graph.NodeID, 0)
	for _, sym := range module.ModuleScope.Symbols() {
		if sym == nil || sym.Kind != symbols.SymbolType {
			continue
		}
		nodeIDs = append(nodeIDs, typeDeclNodeID(module.Key, sym.Name))
	}
	if len(nodeIDs) == 0 {
		return
	}
	// Only value-layout edges participate in illegal cycle detection.
	_, cycles := ctx.Graph.TopoSort(nodeIDs, graph.EdgeTypeValueRef)
	for _, cycle := range cycles {
		if len(cycle) == 0 {
			continue
		}
		firstName := typeDeclNameFromNodeID(cycle[0])
		firstSym, ok := module.ModuleScope.LookupLocal(firstName)
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
		ctx.Diagnostics.AddError(
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

func addTypeDeclEdges(ctx *project.CompilerContext, module *project.Module, owner graph.NodeID, typ ast.TypeExpr, indirect bool) {
	if ctx == nil || ctx.Graph == nil || module == nil || owner == "" || typ == nil {
		return
	}
	switch node := typ.(type) {
	case *ast.NamedType:
		target := lookupTypeDeclNodeID(module, node.Name)
		if target == "" {
			return
		}
		if indirect {
			ctx.Graph.AddEdge(owner, target, graph.EdgeTypeIndirectRef)
			return
		}
		ctx.Graph.AddEdge(owner, target, graph.EdgeTypeValueRef)
	case *ast.ScopeResolution:
		target := lookupQualifiedTypeDeclNodeID(ctx, module, node)
		if target == "" {
			return
		}
		if indirect {
			ctx.Graph.AddEdge(owner, target, graph.EdgeTypeIndirectRef)
			return
		}
		ctx.Graph.AddEdge(owner, target, graph.EdgeTypeValueRef)
	case *ast.RawPtrType:
		// Pointer target is not a layout dependency.
		addTypeDeclEdges(ctx, module, owner, node.Target, true)
	case *ast.OptionalType:
		addTypeDeclEdges(ctx, module, owner, node.Inner, indirect)
	case *ast.ArrayType:
		addTypeDeclEdges(ctx, module, owner, node.Elem, indirect)
	case *ast.SliceType:
		addTypeDeclEdges(ctx, module, owner, node.Elem, indirect)
	case *ast.StructType:
		for _, field := range node.Fields {
			addTypeDeclEdges(ctx, module, owner, field.Type, indirect)
		}
	case *ast.FuncType:
		for _, param := range node.Params {
			addTypeDeclEdges(ctx, module, owner, param, true)
		}
		addTypeDeclEdges(ctx, module, owner, node.Return, true)
	case *ast.InterfaceType:
		for _, method := range node.Methods {
			for _, param := range method.Params {
				addTypeDeclEdges(ctx, module, owner, param.Type, true)
			}
			addTypeDeclEdges(ctx, module, owner, method.ReturnType, true)
		}
	}
}

func lookupTypeDeclNodeID(module *project.Module, name string) graph.NodeID {
	if module == nil || module.ModuleScope == nil || name == "" {
		return ""
	}
	sym, ok := module.ModuleScope.Lookup(name)
	if !ok || sym == nil || sym.Kind != symbols.SymbolType {
		return ""
	}
	return typeDeclNodeID(module.Key, sym.Name)
}

func lookupQualifiedTypeDeclNodeID(ctx *project.CompilerContext, module *project.Module, node *ast.ScopeResolution) graph.NodeID {
	if ctx == nil || module == nil || node == nil || node.Module == nil || node.Name == nil {
		return ""
	}
	imp, ok := module.Imports[node.Module.Name]
	if !ok {
		return ""
	}
	imported, ok := ctx.ModuleByKey(imp.Key)
	if !ok || imported == nil || imported.ModuleScope == nil {
		return ""
	}
	sym, ok := imported.ModuleScope.LookupLocal(node.Name.Name)
	if !ok || sym == nil || sym.Kind != symbols.SymbolType {
		return ""
	}
	return typeDeclNodeID(imported.Key, sym.Name)
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
