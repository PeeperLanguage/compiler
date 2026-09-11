package binder

import (
	"fmt"
	"slices"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

const (
	graphEdgeTypeValueRef      graph.EdgeKind = "type_value_ref"
	graphEdgeTypeIndirectRef   graph.EdgeKind = "type_indirect_ref"
	graphEdgeTypeCompletionRef graph.EdgeKind = "type_completion_ref"
)

// typeDeclarationOrder registers dependencies once and orders construction
// before any generic applications can cache incomplete alias representations.
// Legal completion cycles get one bounded completion pass after every shell is
// populated. Only value edges are illegal cycles.
func (b *binder) typeDeclarationOrder() ([]ast.TypeDecl, []ast.TypeDecl) {
	if b == nil || b.ctx == nil || b.ctx.Graph == nil || b.ctx.Diagnostics == nil || b.module == nil || b.module.ModuleScope == nil {
		return nil, nil
	}
	var nodeIDs []graph.NodeID
	declarations := make(map[graph.NodeID]ast.TypeDecl)
	ast.ForEachDecl(b.module.AST, func(decl ast.Decl) bool {
		typeDecl, ok := decl.(ast.TypeDecl)
		if !ok || typeDecl.DeclName() == nil {
			return true
		}
		sym := b.moduleScopeSymbol(typeDecl.DeclName().Name)
		if sym == nil || sym.ASTNode != decl {
			return true
		}
		id := typeDeclNodeID(b.module.ID, sym.Name)
		nodeIDs = append(nodeIDs, id)
		declarations[id] = typeDecl
		b.addTypeDeclEdges(id, typeDecl.UnderlyingType(), false, typeDecl.DeclarationTypeParams())
		return true
	})
	// Only value-layout edges participate in illegal cycle detection.
	_, cycles := b.ctx.Graph.TopoSort(nodeIDs, graphEdgeTypeValueRef)
	illegal := make(map[graph.NodeID]bool)
	for _, cycle := range cycles {
		if len(cycle) == 0 {
			continue
		}
		for _, id := range cycle {
			illegal[id] = true
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
	order, completionCycles := b.ctx.Graph.TopoSort(nodeIDs, graphEdgeTypeCompletionRef)
	ordered := make([]ast.TypeDecl, 0, len(order))
	for _, id := range order {
		ordered = append(ordered, declarations[id])
	}
	seen := make(map[graph.NodeID]bool)
	completion := make([]ast.TypeDecl, 0)
	for _, cycle := range completionCycles {
		for _, id := range cycle {
			if illegal[id] || seen[id] || declarations[id] == nil {
				continue
			}
			seen[id] = true
			completion = append(completion, declarations[id])
		}
	}
	return ordered, completion
}

func typeDeclNodeID(moduleID moduleid.ID, name string) graph.NodeID {
	if !moduleID.Valid() || name == "" {
		return ""
	}
	return graph.NodeID("type:" + moduleID.String() + ":" + name)
}

func (b *binder) addTypeDeclEdges(owner graph.NodeID, typ ast.TypeExpr, indirect bool, parameters []ast.TypeParam) {
	if b == nil || b.ctx == nil || b.ctx.Graph == nil || b.module == nil || owner == "" || typ == nil {
		return
	}
	switch node := typ.(type) {
	case *ast.NamedType:
		target, alias := b.lookupTypeDeclNodeID(node.Name, parameters)
		b.addTypeDeclEdge(owner, target, indirect, alias)
	case *ast.AppliedType:
		if node.Name != nil {
			target, _ := b.lookupTypeDeclNodeID(node.Name.Name, parameters)
			b.addTypeDeclEdge(owner, target, indirect, true)
		}
		// Arguments must be canonical before instance keys are computed. An
		// argument occurrence alone does not establish an inline layout edge.
		for _, argument := range node.TypeArgs {
			b.addTypeDeclEdges(owner, argument, true, parameters)
		}
	case *ast.ScopeResolution:
		target, alias := b.lookupQualifiedTypeDeclNodeID(node)
		b.addTypeDeclEdge(owner, target, indirect, alias || len(node.Segments[len(node.Segments)-1].TypeArgs) > 0)
		for _, segment := range node.Segments {
			for _, argument := range segment.TypeArgs {
				b.addTypeDeclEdges(owner, argument, true, parameters)
			}
		}
	case *ast.RawPtrType:
		// Raw pointers carry no pointee layout dependency.
	case *ast.EnumType:
		for _, variant := range node.Variants {
			b.addTypeDeclEdges(owner, variant.Payload, indirect, parameters)
		}
	case *ast.OwnedPtrType:
		// Pointer target is not a layout dependency.
		b.addTypeDeclEdges(owner, node.Target, true, parameters)
	case *ast.RefType:
		// Reference target is not owned inline storage.
		b.addTypeDeclEdges(owner, node.Target, true, parameters)
	case *ast.OptionalType:
		b.addTypeDeclEdges(owner, node.Inner, indirect, parameters)
	case *ast.ArrayType:
		b.addTypeDeclEdges(owner, node.Elem, indirect || node.Shape != ast.ArrayFixed || node.Len == nil, parameters)
	case *ast.StructType:
		for _, field := range node.Fields {
			b.addTypeDeclEdges(owner, field.Type, indirect, parameters)
		}
	case *ast.FuncType:
		for _, param := range node.Params {
			b.addTypeDeclEdges(owner, param.Type, true, parameters)
		}
		b.addTypeDeclEdges(owner, node.Return, true, parameters)
	case *ast.InterfaceType:
		for _, method := range node.Methods {
			for _, param := range method.Params {
				b.addTypeDeclEdges(owner, param.Type, true, parameters)
			}
			b.addTypeDeclEdges(owner, method.ReturnType, true, parameters)
		}
	default:
		panic(fmt.Sprintf("binder type dependencies: unhandled type syntax %T", typ))
	}
}

func (b *binder) addTypeDeclEdge(owner, target graph.NodeID, indirect, complete bool) {
	if target == "" {
		return
	}
	kind := graphEdgeTypeValueRef
	if indirect {
		kind = graphEdgeTypeIndirectRef
	}
	b.ctx.Graph.AddEdge(owner, target, kind)
	// Nominal references only need their collected shell. Aliases and applied
	// declarations must finish first, even when used behind an indirection.
	if complete && owner != target {
		b.ctx.Graph.AddEdge(owner, target, graphEdgeTypeCompletionRef)
	}
}

func (b *binder) lookupTypeDeclNodeID(name string, parameters []ast.TypeParam) (graph.NodeID, bool) {
	if b == nil || b.module == nil || b.module.ModuleScope == nil || name == "" {
		return "", false
	}
	if slices.ContainsFunc(parameters, func(parameter ast.TypeParam) bool {
		return parameter.Name != nil && parameter.Name.Name == name
	}) {
		return "", false
	}
	sym, ok := b.module.ModuleScope.Lookup(name)
	if !ok || sym == nil || sym.Kind != symbols.SymbolType {
		return "", false
	}
	defined, ok := sym.Type.(*typeinfo.DefinedType)
	return typeDeclNodeID(b.module.ID, sym.Name), ok && defined.Kind == typeinfo.DefinedKindAlias
}

func (b *binder) lookupQualifiedTypeDeclNodeID(node *ast.ScopeResolution) (graph.NodeID, bool) {
	if b == nil || b.ctx == nil || b.module == nil || node == nil {
		return "", false
	}
	qualifier, member, imported := node.ImportMember()
	if !imported {
		return "", false
	}
	resolved, ok := project.LookupImportedSymbol(b.ctx, b.module, qualifier.Name, member.Name)
	if !ok || resolved.Module == nil || resolved.Symbol == nil || resolved.Symbol.Kind != symbols.SymbolType {
		return "", false
	}
	defined, ok := resolved.Symbol.Type.(*typeinfo.DefinedType)
	return typeDeclNodeID(resolved.Module.ID, resolved.Symbol.Name), ok && defined.Kind == typeinfo.DefinedKindAlias
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
