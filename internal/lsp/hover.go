package lsp

import (
	"fmt"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type hoverSubjectKind int

const (
	hoverSubjectSymbol hoverSubjectKind = iota
	hoverSubjectExpr
	hoverSubjectType
	hoverSubjectDecl
	hoverSubjectImport
	hoverSubjectAttribute
)

// hoverSubject is the normalized cursor target after resolution. It hides how
// the cursor was found so the renderer can stay flat and data-driven.
type hoverSubject struct {
	Kind           hoverSubjectKind
	Node           ast.Node
	Location       *source.Location
	Symbol         *symbols.Symbol
	ExprType       typeinfo.Type
	ResolvedType   typeinfo.Type
	Decl           ast.Node
	ResolvedImport *project.ResolvedImport
	Attribute      *ast.Attribute
	MethodSymbols  []*symbols.Symbol
}

func (s *ServerState) resolveHoverSubject(filePath string, position source.Position) *hoverSubject {
	ctx, mod := s.currentCompiledModule(filePath)
	cc := buildCursorContext(ctx, mod, position)
	if cc == nil {
		return nil
	}
	// Priority order: import > type > decl > selector > symbol > expr.
	for _, resolve := range []func(*cursorContext) *hoverSubject{
		resolveAttributeHoverSubject,
		resolveImportHoverSubject,
		resolveTypeHoverSubject,
		resolveDeclHoverSubject,
		resolveSelectorHoverSubject,
		resolveSymbolHoverSubject,
		resolveExprHoverSubject,
	} {
		if subject := resolve(cc); subject != nil {
			return subject
		}
	}
	return nil
}

func resolveAttributeHoverSubject(cc *cursorContext) *hoverSubject {
	if cc == nil || cc.module == nil || cc.module.AST == nil {
		return nil
	}
	for _, stmt := range cc.module.AST.Stmts {
		if subject := attributeHoverSubject(stmt, cc); subject != nil {
			return subject
		}
	}
	return nil
}

func attributeHoverSubject(node ast.Node, cc *cursorContext) *hoverSubject {
	attributed, ok := node.(ast.AttributedNode)
	if !ok || attributed == nil {
		return nil
	}
	for _, attr := range attributed.GetAttributes() {
		if !locContains(attr.Location, cc.line, cc.col) {
			continue
		}
		hoverAttr := attr
		return &hoverSubject{
			Kind:      hoverSubjectAttribute,
			Node:      node,
			Location:  attr.Location,
			Attribute: &hoverAttr,
		}
	}
	return nil
}

func resolveImportHoverSubject(cc *cursorContext) *hoverSubject {
	ident, ok := cc.node.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	parent := cc.parents[ident.ID()]
	if sr, ok := parent.(*ast.ScopeResolution); ok && sr.Module == ident {
		imp, ok := cc.module.Imports[ident.Name]
		if !ok {
			return nil
		}
		hoverImp := imp
		return &hoverSubject{
			Kind:           hoverSubjectImport,
			Node:           ident,
			Location:       ast.LocOf(ident),
			ResolvedImport: &hoverImp,
		}
	}
	return nil
}

func resolveTypeHoverSubject(cc *cursorContext) *hoverSubject {
	typeNode, ok := hoverTypeNode(cc.node, cc.parents)
	if !ok || typeNode == nil {
		return nil
	}
	selfType, allowAbstractSelf := hoverTypeSyntaxContext(typeNode, cc.parents)
	resolved := typeinfo.TypeFromSyntax(typeNode, project.TypeSyntaxOptions(cc.ctx, cc.module, selfType, allowAbstractSelf))
	if resolved == nil {
		return nil
	}
	return &hoverSubject{
		Kind:          hoverSubjectType,
		Node:          cc.node,
		Location:      ast.LocOf(cc.node),
		ResolvedType:  resolved,
		MethodSymbols: lookupMethodSet(cc.ctx, resolved, hoverMethodKeysForTypeNode(typeNode, cc.parents, resolved)),
	}
}

func hoverMethodKeysForTypeNode(typeNode ast.TypeExpr, parents map[ast.NodeID]ast.Node, resolved typeinfo.Type) []string {
	keys := []string{typeinfo.TypeText(resolved)}
	for curr := ast.Node(typeNode); curr != nil; curr = parents[curr.ID()] {
		decl, ok := curr.(ast.TypeDecl)
		if !ok {
			continue
		}
		if name := decl.DeclName(); name != nil && name.Name != "" && name.Name != keys[0] {
			keys = append(keys, name.Name)
		}
		break
	}
	return keys
}

func hoverTypeNode(node ast.Node, parents map[ast.NodeID]ast.Node) (ast.TypeExpr, bool) {
	if node == nil {
		return nil, false
	}
	if typeNode, ok := node.(ast.TypeExpr); ok {
		if isTypeExprPosition(typeNode, parents[typeNode.ID()]) {
			return typeNode, true
		}
	}
	top := node
	for top != nil {
		switch top.(type) {
		case *ast.Ident, *ast.ScopeResolution:
			parent := parents[top.ID()]
			if parent == nil {
				top = nil
				break
			}
			if _, ok := parent.(*ast.ScopeResolution); ok {
				top = parent
				continue
			}
			typeNode, ok := top.(ast.TypeExpr)
			if ok && isTypeExprPosition(typeNode, parent) {
				return typeNode, true
			}
			return nil, false
		default:
			return nil, false
		}
	}
	return nil, false
}

func hoverTypeSyntaxContext(typeNode ast.TypeExpr, parents map[ast.NodeID]ast.Node) (typeinfo.Type, bool) {
	for curr := ast.Node(typeNode); curr != nil; curr = parents[curr.ID()] {
		switch curr.(type) {
		case *ast.InterfaceDecl:
			return nil, true
		}
	}
	return nil, false
}

func isTypeExprPosition(typeNode ast.TypeExpr, parent ast.Node) bool {
	if typeNode == nil || parent == nil {
		return false
	}
	switch p := parent.(type) {
	case *ast.LetDecl:
		return p.Type == typeNode
	case *ast.ConstDecl:
		return p.Type == typeNode
	case *ast.TypeAliasDecl:
		return p.Type == typeNode
	case *ast.StructDecl:
		return p.Type == typeNode
	case *ast.InterfaceDecl:
		return p.Type == typeNode
	case *ast.EnumDecl:
		return p.Type == typeNode
	case *ast.AsExpr:
		return p.TypeExpr == typeNode
	case *ast.StructLit:
		return p.Type == typeNode
	case *ast.OwnedPtrType:
		return p.Target == typeNode
	case *ast.RefType:
		return p.Target == typeNode
	case *ast.OptionalType:
		return p.Inner == typeNode
	case *ast.ArrayType:
		return p.Elem == typeNode
	case *ast.FuncType:
		if p.Return == typeNode {
			return true
		}
		for _, param := range p.Params {
			if param.Type == typeNode {
				return true
			}
		}
	case *ast.StructType:
		for _, field := range p.Fields {
			if field.Type == typeNode {
				return true
			}
		}
	case *ast.InterfaceType:
		for _, method := range p.Methods {
			if method.ReturnType == typeNode {
				return true
			}
			if method.Receiver != nil && method.Receiver.Type == typeNode {
				return true
			}
			for _, param := range method.Params {
				if param.Type == typeNode {
					return true
				}
			}
		}
	case *ast.FnDecl:
		if p.ReturnType == typeNode {
			return true
		}
		if p.Receiver != nil && p.Receiver.Type == typeNode {
			return true
		}
		for _, param := range p.Params {
			if param.Type == typeNode {
				return true
			}
		}
	}
	return false
}

func resolveDeclHoverSubject(cc *cursorContext) *hoverSubject {
	if cc == nil || cc.node == nil {
		return nil
	}
	switch decl := cc.node.(type) {
	case *ast.FnDecl:
		return declHoverSubject(cc, decl, decl.Name)
	case *ast.LetDecl:
		return declHoverSubject(cc, decl, decl.Name)
	case *ast.ConstDecl:
		return declHoverSubject(cc, decl, decl.Name)
	case ast.TypeDecl:
		return declHoverSubject(cc, decl, decl.DeclName())
	default:
		return nil
	}
}

func declHoverSubject(cc *cursorContext, decl ast.Node, name *ast.Ident) *hoverSubject {
	subject := &hoverSubject{
		Kind:     hoverSubjectDecl,
		Node:     decl,
		Decl:     decl,
		Location: ast.LocOf(decl),
	}
	if name != nil {
		subject.Symbol = resolveIdentSymbol(name, cc.parents, cc.module, cc.ctx)
		if subject.Symbol != nil && subject.Symbol.Kind == symbols.SymbolType {
			if typ, ok := symbols.GetSymbolType(subject.Symbol); ok {
				subject.MethodSymbols = lookupMethodSet(cc.ctx, typ, []string{subject.Symbol.Name})
			}
		}
	}
	return subject
}

func resolveSelectorHoverSubject(cc *cursorContext) *hoverSubject {
	ident, ok := cc.node.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	parent := cc.parents[ident.ID()]
	sel, ok := parent.(*ast.SelectorExpr)
	if !ok || sel == nil || sel.Name != ident {
		return nil
	}
	if sym := resolveSelectorMemberSymbol(sel, ident, cc.parents, cc.module, cc.ctx); sym != nil {
		return &hoverSubject{
			Kind:     hoverSubjectSymbol,
			Node:     ident,
			Decl:     documentedDeclAncestor(ident, cc.parents),
			Location: ast.LocOf(ident),
			Symbol:   sym,
		}
	}
	if subject := resolveInterfaceSelectorMethodHoverSubject(cc, sel, ident); subject != nil {
		return subject
	}
	if exprType, ok := cc.module.Semantics.ExprTypes[sel.ID()]; ok {
		return &hoverSubject{
			Kind:     hoverSubjectExpr,
			Node:     ident,
			Location: ast.LocOf(ident),
			ExprType: exprType,
		}
	}
	return nil
}

func resolveInterfaceSelectorMethodHoverSubject(cc *cursorContext, sel *ast.SelectorExpr, ident *ast.Ident) *hoverSubject {
	baseType, ok := selectorBaseType(sel.Expr, cc.parents, cc.module, cc.ctx)
	if !ok {
		return nil
	}
	iface, ok := typeinfo.InterfaceTypeOf(baseType)
	if !ok || iface == nil {
		return nil
	}
	for i := range iface.Methods {
		method := &iface.Methods[i]
		if method.Name != ident.Name {
			continue
		}
		return &hoverSubject{
			Kind:     hoverSubjectSymbol,
			Node:     ident,
			Location: ast.LocOf(ident),
			Symbol:   interfaceMethodSymbol(ident, method),
		}
	}
	return nil
}

func resolveSymbolHoverSubject(cc *cursorContext) *hoverSubject {
	ident, ok := cc.node.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	sym := resolveDeclNameSymbol(ident, cc.parents, cc.module)
	if sym == nil {
		sym = resolveInterfaceMethodNameSymbol(ident, cc.parents, cc.ctx, cc.module)
	}
	if sym == nil {
		sym = resolveIdentSymbol(ident, cc.parents, cc.module, cc.ctx)
	}
	if sym == nil {
		return nil
	}
	subject := &hoverSubject{
		Kind:     hoverSubjectSymbol,
		Node:     ident,
		Decl:     documentedDeclAncestor(ident, cc.parents),
		Location: ast.LocOf(ident),
		Symbol:   sym,
	}
	if sym.Kind == symbols.SymbolType {
		if typ, ok := symbols.GetSymbolType(sym); ok {
			subject.MethodSymbols = lookupMethodSet(cc.ctx, typ, []string{sym.Name})
		}
	}
	return subject
}

func documentedDeclAncestor(node ast.Node, parents map[ast.NodeID]ast.Node) ast.Node {
	for current := node; current != nil; current = parents[current.ID()] {
		if decl, ok := current.(ast.Decl); ok {
			return decl
		}
	}
	return nil
}

func resolveDeclNameSymbol(ident *ast.Ident, parents map[ast.NodeID]ast.Node, module *project.Module) *symbols.Symbol {
	if ident == nil || module == nil || module.Semantics == nil {
		return nil
	}
	parent := parents[ident.ID()]
	if fn, ok := parent.(*ast.FnDecl); ok && fn != nil && fn.Name == ident && fn.Receiver != nil {
		if sym, ok := module.Semantics.MethodSymbol[fn.ID()]; ok && sym != nil {
			return sym
		}
	}
	return nil
}

func resolveInterfaceMethodNameSymbol(ident *ast.Ident, parents map[ast.NodeID]ast.Node, ctx *project.CompilerContext, module *project.Module) *symbols.Symbol {
	if ident == nil || ctx == nil || module == nil {
		return nil
	}
	iface, ok := parents[ident.ID()].(*ast.InterfaceType)
	if !ok || iface == nil {
		return nil
	}
	opts := project.TypeSyntaxOptions(ctx, module, nil, true)
	resolved, ok := typeinfo.TypeFromSyntax(iface, opts).(*typeinfo.InterfaceType)
	if !ok || resolved == nil {
		return nil
	}
	for i, method := range iface.Methods {
		if method.Name != ident || i >= len(resolved.Methods) {
			continue
		}
		return interfaceMethodSymbol(ident, &resolved.Methods[i])
	}
	return nil
}

func interfaceMethodSymbol(ident *ast.Ident, method *typeinfo.Method) *symbols.Symbol {
	if ident == nil || method == nil {
		return nil
	}
	sym := symbols.New(ident.Name, symbols.SymbolMethod, ident, ast.LocOf(ident))
	sym.Type = method.CallableType()
	return sym
}

func lookupMethodSet(ctx *project.CompilerContext, typ typeinfo.Type, keys []string) []*symbols.Symbol {
	if ctx == nil || typ == nil || len(keys) == 0 {
		return nil
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		keySet[key] = struct{}{}
	}
	if len(keySet) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var methods []*symbols.Symbol
	for _, module := range ctx.Modules() {
		if module == nil || module.Semantics == nil {
			continue
		}
		for key := range keySet {
			for _, sym := range module.Semantics.MethodSets[key] {
				if sym == nil {
					continue
				}
				signature := sym.Name
				if typ, ok := symbols.GetSymbolType(sym); ok && typ != nil {
					signature += "|" + typeinfo.TypeText(typ)
				}
				if sym.Location != nil && sym.Location.Filename != nil && sym.Location.Start != nil {
					signature += fmt.Sprintf("|%s:%d:%d", *sym.Location.Filename, sym.Location.Start.Line, sym.Location.Start.Column)
				}
				if _, ok := seen[signature]; ok {
					continue
				}
				seen[signature] = struct{}{}
				methods = append(methods, sym)
			}
		}
	}
	return methods
}

func resolveExprHoverSubject(cc *cursorContext) *hoverSubject {
	if cc == nil || cc.node == nil || cc.module == nil || cc.module.Semantics == nil {
		return nil
	}
	if _, ok := cc.node.(ast.Expr); !ok {
		return nil
	}
	exprType, ok := cc.module.Semantics.ExprTypes[cc.node.ID()]
	if !ok {
		return nil
	}
	return &hoverSubject{
		Kind:     hoverSubjectExpr,
		Node:     cc.node,
		Location: ast.LocOf(cc.node),
		ExprType: exprType,
	}
}

func renderHoverSubject(subject *hoverSubject) string {
	if subject == nil {
		return ""
	}
	var text string
	switch subject.Kind {
	case hoverSubjectSymbol, hoverSubjectDecl:
		// Decl subjects always set Symbol. Nil guard only covers symbol-only paths.
		if subject.Symbol == nil {
			return ""
		}
		text = renderSymbol(subject.Symbol, symbolRenderContext{Declaration: subject.Decl})
		if typ, ok := symbols.GetSymbolType(subject.Symbol); ok && typ != nil {
			if subject.Symbol.Kind == symbols.SymbolType {
				text += renderTypeDetails(typ, subject.MethodSymbols)
			}
		}
	case hoverSubjectExpr:
		if subject.ExprType == nil {
			return ""
		}
		text = fmt.Sprintf("(expr): %s", typeinfo.TypeText(subject.ExprType))
	case hoverSubjectType:
		if subject.ResolvedType == nil {
			return ""
		}
		text = "(type)"
		if inline, ok := hoverTypeLabel(subject.ResolvedType); ok && inline != "" {
			text += " " + inline
		}
		text += renderTypeDetails(subject.ResolvedType, subject.MethodSymbols)
	case hoverSubjectImport:
		if subject.ResolvedImport == nil {
			return ""
		}
		name := subject.ResolvedImport.ImportPath
		if ident, ok := subject.Node.(*ast.Ident); ok && ident != nil && ident.Name != "" {
			name = ident.Name
		}
		importSymbol := &symbols.Symbol{Name: name, Kind: symbols.SymbolImport}
		text = renderSymbol(importSymbol, symbolRenderContext{ImportPath: subject.ResolvedImport.ImportPath})
	case hoverSubjectAttribute:
		if subject.Attribute == nil {
			return ""
		}
		text = "(attribute) #[" + subject.Attribute.Name + "]"
	default:
		return ""
	}
	if doc := hoverDocComment(subject); doc != "" {
		// Keep docs outside code fence so markdown renders them as normal prose
		// with the separator bar users expect from other LSPs. Preserve source
		// line breaks explicitly so markdown does not collapse adjacent doc lines.
		doc = strings.ReplaceAll(doc, "\n", "  \n")
		return fmt.Sprintf("```peeper\n%s\n```\n\n---\n\n%s", text, doc)
	}
	return fmt.Sprintf("```peeper\n%s\n```", text)
}

func renderTypeDetails(typ typeinfo.Type, methods []*symbols.Symbol) string {
	body := formatHoverTypeBody(typ)
	methodText := formatHoverMethods(methods)
	switch {
	case body == "" && methodText == "":
		return ""
	case body == "" && methodText != "":
		return "\n\n// methods\n" + methodText
	case body != "" && methodText == "":
		return "\n\n// inner type\n" + body
	default:
		return "\n\n// inner type\n" + body + "\n\n// methods\n" + methodText
	}
}

func formatHoverTypeBody(typ typeinfo.Type) string {
	switch t := typ.(type) {
	case *typeinfo.DefinedType:
		if t == nil || t.Underlying == nil {
			return ""
		}
		if body := formatHoverTypeBody(t.Underlying); body != "" {
			return body
		}
		return typeinfo.TypeText(t.Underlying)
	case *typeinfo.StructType:
		if t == nil {
			return ""
		}
		var b strings.Builder
		b.WriteString("struct {\n")
		for _, field := range t.Fields {
			b.WriteString("  ")
			b.WriteString(field.Name)
			b.WriteString(": ")
			b.WriteString(typeinfo.TypeText(field.Type))
			b.WriteString(",\n")
		}
		b.WriteString("}")
		return b.String()
	case *typeinfo.InterfaceType:
		if t == nil {
			return ""
		}
		var b strings.Builder
		b.WriteString("iface {\n")
		for i := range t.Methods {
			method := &t.Methods[i]
			methodSymbol := &symbols.Symbol{Name: method.Name, Kind: symbols.SymbolMethod, Type: method.CallableType()}
			b.WriteString("  ")
			b.WriteString(renderSymbol(methodSymbol, symbolRenderContext{Embedded: true}))
			b.WriteString(",\n")
		}
		b.WriteString("}")
		return b.String()
	case *typeinfo.EnumType:
		if t == nil {
			return ""
		}
		var b strings.Builder
		b.WriteString("enum {\n")
		for _, variant := range t.Variants {
			b.WriteString("  ")
			b.WriteString(variant)
			b.WriteString(",\n")
		}
		b.WriteString("}")
		return b.String()
	default:
		return ""
	}
}

func formatHoverMethods(methods []*symbols.Symbol) string {
	if len(methods) == 0 {
		return ""
	}
	var b strings.Builder
	for _, method := range methods {
		if method == nil {
			continue
		}
		signature := renderSymbol(method, symbolRenderContext{Embedded: true})
		if signature == "" {
			continue
		}
		b.WriteString(signature)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func hoverTypeLabel(typ typeinfo.Type) (string, bool) {
	if typ == nil {
		return "", false
	}
	switch t := typ.(type) {
	case *typeinfo.NamedType:
		if t == nil {
			return "", false
		}
		return t.Name, true
	case *typeinfo.DefinedType:
		if t == nil {
			return "", false
		}
		return t.Name, true
	case *typeinfo.StructType, *typeinfo.InterfaceType, *typeinfo.EnumType:
		return "", false
	default:
		text := typeinfo.TypeText(typ)
		return text, text != ""
	}
}

func hoverDocComment(subject *hoverSubject) string {
	if subject == nil {
		return ""
	}
	if subject.Attribute != nil {
		if def, ok := ast.AttributeDefinitions[subject.Attribute.Name]; ok && def.Doc != "" {
			return def.Doc
		}
	}
	var symNode ast.Node
	if subject.Symbol != nil {
		symNode = subject.Symbol.ASTNode
	}
	for _, node := range [3]ast.Node{subject.Decl, symNode, subject.Node} {
		docNode, ok := node.(ast.DocumentedNode)
		if !ok || docNode == nil {
			continue
		}
		doc := docNode.GetDocComment()
		if doc != nil && strings.TrimSpace(doc.Text) != "" {
			return strings.TrimSpace(doc.Text)
		}
	}
	return ""
}

func (s *ServerState) HandleHover(params HoverParams) (*Hover, error) {
	path := uriToPath(string(params.TextDocument.URI))
	text, err := s.completionSource(path)
	if err != nil {
		return nil, nil
	}
	position, ok := sourcePositionAt(text, params.Position)
	if !ok {
		return nil, nil
	}
	subject := s.resolveHoverSubject(path, position)
	if subject == nil {
		return nil, nil
	}
	value := renderHoverSubject(subject)
	if value == "" {
		return nil, nil
	}

	hoverRange, ok := rangeAtLocation(text, subject.Location)
	if !ok {
		return nil, nil
	}
	return &Hover{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: value,
		},
		Range: &hoverRange,
	}, nil
}
