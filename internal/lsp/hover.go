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
	Kind            hoverSubjectKind
	Node            ast.Node
	Range           Range
	Symbol          *symbols.Symbol
	ExprType        typeinfo.Type
	ResolvedType    typeinfo.Type
	Decl            ast.Node
	ResolvedImport  *project.ResolvedImport
	Attribute       *ast.Attribute
	MethodSymbols   []*symbols.Symbol
	InterfaceMethod *typeinfo.Method
}

func hoverRange(node ast.Node) Range {
	loc := ast.LocOf(node)
	return locationRange(loc)
}

func locationRange(loc *source.Location) Range {
	if loc == nil || loc.Start == nil || loc.End == nil {
		return Range{}
	}
	return Range{
		Start: Position{Line: loc.Start.Line - 1, Character: loc.Start.Column - 1},
		End:   Position{Line: loc.End.Line - 1, Character: loc.End.Column - 1},
	}
}

func (s *ServerState) resolveHoverSubject(filePath string, line, col int) *hoverSubject {
	ctx, mod := s.currentCompiledModule(filePath)
	cc := buildCursorContext(ctx, mod, line, col)
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
			Range:     locationRange(attr.Location),
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
			Range:          hoverRange(ident),
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
		Range:         hoverRange(cc.node),
		ResolvedType:  resolved,
		MethodSymbols: lookupMethodSet(cc.ctx, hoverMethodKeysForTypeNode(typeNode, cc.parents, resolved)),
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
		Kind:  hoverSubjectDecl,
		Node:  decl,
		Decl:  decl,
		Range: hoverRange(decl),
	}
	if name != nil {
		subject.Symbol = resolveIdentSymbol(name, cc.parents, cc.module, cc.ctx)
		if subject.Symbol != nil && subject.Symbol.Kind == symbols.SymbolType {
			subject.MethodSymbols = lookupMethodSet(cc.ctx, []string{subject.Symbol.Name})
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
			Kind:   hoverSubjectSymbol,
			Node:   ident,
			Decl:   documentedDeclAncestor(ident, cc.parents),
			Range:  hoverRange(ident),
			Symbol: sym,
		}
	}
	if subject := resolveInterfaceSelectorMethodHoverSubject(cc, sel, ident); subject != nil {
		return subject
	}
	if exprType, ok := cc.module.Semantics.ExprTypes[sel.ID()]; ok {
		return &hoverSubject{
			Kind:     hoverSubjectExpr,
			Node:     ident,
			Range:    hoverRange(ident),
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
			Kind:            hoverSubjectSymbol,
			Node:            ident,
			Range:           hoverRange(ident),
			Symbol:          interfaceMethodSymbol(ident, method),
			InterfaceMethod: method,
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
		Kind:   hoverSubjectSymbol,
		Node:   ident,
		Decl:   documentedDeclAncestor(ident, cc.parents),
		Range:  hoverRange(ident),
		Symbol: sym,
	}
	if sym.Kind == symbols.SymbolType {
		subject.MethodSymbols = lookupMethodSet(cc.ctx, []string{sym.Name})
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

func lookupMethodSet(ctx *project.CompilerContext, keys []string) []*symbols.Symbol {
	if ctx == nil || len(keys) == 0 {
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
		Range:    hoverRange(cc.node),
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
		if subject.Symbol.Kind == symbols.SymbolFunc || subject.Symbol.Kind == symbols.SymbolMethod {
			if signature := hoverSubjectFunctionSignature(subject).text(); signature != "" {
				text = fmt.Sprintf("(%s) %s", subject.Symbol.Kind, signature)
				break
			}
		}
		text = fmt.Sprintf("(%s) %s", subject.Symbol.Kind, subject.Symbol.Name)
		if typ, ok := symbols.GetSymbolType(subject.Symbol); ok && typ != nil {
			if subject.Symbol.Kind == symbols.SymbolType {
				text += renderTypeDetails(typ, subject.MethodSymbols)
			} else {
				text += ": " + typeinfo.TypeText(typ)
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
		text = fmt.Sprintf("(import) %s -> %s", name, subject.ResolvedImport.ImportPath)
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

type hoverFunctionSignature struct {
	receiver      string
	name          string
	typeParams    []string
	params        []string
	result        string
	returnOrigins string
}

func (s hoverFunctionSignature) text() string {
	if s.name == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("fn ")
	if s.receiver != "" {
		b.WriteString("(")
		b.WriteString(s.receiver)
		b.WriteString(") ")
	}
	b.WriteString(s.name)
	if len(s.typeParams) > 0 {
		b.WriteString("<")
		b.WriteString(strings.Join(s.typeParams, ", "))
		b.WriteString(">")
	}
	b.WriteString("(")
	b.WriteString(strings.Join(s.params, ", "))
	b.WriteString(")")
	if s.result != "" {
		b.WriteString(" -> ")
		b.WriteString(s.result)
	}
	b.WriteString(s.returnOrigins)
	return b.String()
}

func hoverSubjectFunctionSignature(subject *hoverSubject) hoverFunctionSignature {
	if subject == nil || subject.Symbol == nil {
		return hoverFunctionSignature{}
	}
	if decl, ok := subject.Decl.(*ast.InterfaceDecl); ok && decl != nil {
		if iface, ok := decl.Type.(*ast.InterfaceType); ok && iface != nil {
			for i := range iface.Methods {
				method := &iface.Methods[i]
				if method.Name == subject.Node {
					return hoverASTFunctionSignature(method.Receiver, method.Name, method.TypeParams, method.Params, method.ReturnType, method.ReturnOrigins)
				}
			}
		}
	}
	if subject.InterfaceMethod != nil {
		return hoverSemanticMethodSignature(subject.InterfaceMethod)
	}
	return hoverSymbolFunctionSignature(subject.Symbol)
}

func hoverASTFunctionSignature(receiver *ast.Param, name *ast.Ident, typeParams []ast.TypeParam, params []ast.Param, result ast.TypeExpr, origins *ast.ReturnOriginClause) hoverFunctionSignature {
	if name == nil {
		return hoverFunctionSignature{}
	}
	signature := hoverFunctionSignature{name: name.Name, result: ast.TypeText(result)}
	if origins != nil {
		signature.returnOrigins = origins.Text()
	}
	if receiver != nil {
		signature.receiver = hoverASTParamText(*receiver)
	}
	for _, typeParam := range typeParams {
		if typeParam.Name != nil {
			signature.typeParams = append(signature.typeParams, typeParam.Name.Name)
		}
	}
	for _, param := range params {
		signature.params = append(signature.params, hoverASTParamText(param))
	}
	return signature
}

func hoverASTParamText(param ast.Param) string {
	var b strings.Builder
	if param.IsMutable {
		b.WriteString("mut ")
	}
	if param.Name != nil {
		b.WriteString(param.Name.Name)
		b.WriteString(": ")
	}
	b.WriteString(ast.TypeText(param.Type))
	if param.Default != nil {
		b.WriteString(" = ")
		b.WriteString(ast.ExprText(param.Default))
	}
	return b.String()
}

func hoverSemanticMethodSignature(method *typeinfo.Method) hoverFunctionSignature {
	if method == nil {
		return hoverFunctionSignature{}
	}
	callable := method.CallableType()
	signature := hoverFunctionSignature{
		name:          method.Name,
		result:        typeinfo.TypeText(method.Return),
		returnOrigins: callable.ReturnOriginText(),
	}
	params := method.Params
	if len(params) > 0 {
		signature.receiver = typeinfo.TypeText(params[0].Type)
		params = params[1:]
	}
	for _, param := range params {
		text := ""
		if param.Name != "" {
			text = param.Name + ": "
		}
		signature.params = append(signature.params, text+typeinfo.TypeText(param.Type))
	}
	return signature
}

func hoverSymbolFunctionSignature(sym *symbols.Symbol) hoverFunctionSignature {
	if sym == nil {
		return hoverFunctionSignature{}
	}
	if decl, ok := sym.ASTNode.(*ast.FnDecl); ok && decl != nil {
		return hoverASTFunctionSignature(decl.Receiver, decl.Name, decl.TypeParams, decl.Params, decl.ReturnType, decl.ReturnOrigins)
	}
	typ, ok := symbols.GetSymbolType(sym)
	if !ok {
		return hoverFunctionSignature{}
	}
	fn, ok := typ.(*typeinfo.FuncType)
	if !ok || fn == nil {
		return hoverFunctionSignature{}
	}
	signature := hoverFunctionSignature{
		name:          sym.Name,
		result:        typeinfo.TypeText(fn.Return),
		returnOrigins: fn.ReturnOriginText(),
	}
	start := 0
	if sym.Kind == symbols.SymbolMethod {
		if len(fn.Params) == 0 {
			return hoverFunctionSignature{}
		}
		signature.receiver = typeinfo.TypeText(fn.Params[0])
		start = 1
	}
	for _, param := range fn.Params[start:] {
		signature.params = append(signature.params, typeinfo.TypeText(param))
	}
	return signature
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
			b.WriteString("  ")
			b.WriteString(hoverSemanticMethodSignature(&t.Methods[i]).text())
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
		signature := hoverSymbolFunctionSignature(method).text()
		if signature == "" {
			continue
		}
		b.WriteString("  ")
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
	subject := s.resolveHoverSubject(path, params.Position.Line+1, params.Position.Character+1)
	if subject == nil {
		return nil, nil
	}
	value := renderHoverSubject(subject)
	if value == "" {
		return nil, nil
	}

	return &Hover{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: value,
		},
		Range: &subject.Range,
	}, nil
}
