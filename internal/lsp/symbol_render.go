package lsp

import (
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type symbolRenderContext struct {
	Name        string
	Type        typeinfo.Type
	ImportPath  string
	Declaration ast.Node
	Embedded    bool
}

func renderSymbol(sym *symbols.Symbol, context symbolRenderContext) string {
	if sym == nil {
		return ""
	}
	name := sym.Name
	if context.Name != "" {
		name = context.Name
	}
	typ := context.Type
	if typ == nil {
		typ, _ = symbols.GetSymbolType(sym)
	}

	var b strings.Builder
	if !context.Embedded {
		b.WriteString("(")
		b.WriteString(string(sym.Kind))
		b.WriteString(") ")
	}
	if sym.Kind == symbols.SymbolImport {
		b.WriteString(name)
		if context.ImportPath != "" {
			b.WriteString(" -> ")
			b.WriteString(context.ImportPath)
		}
		return b.String()
	}

	callable, isCallable := typ.(*typeinfo.FuncType)
	if (sym.Kind == symbols.SymbolFunc || sym.Kind == symbols.SymbolMethod) && isCallable && callable != nil {
		declaration := sym.ASTNode
		if _, interfaceDeclaration := context.Declaration.(*ast.InterfaceDecl); interfaceDeclaration || declaration == nil {
			declaration = context.Declaration
		}
		var receiver *ast.Param
		var params []ast.Param
		var typeParams []ast.TypeParam
		switch decl := declaration.(type) {
		case *ast.FnDecl:
			if decl != nil {
				receiver = decl.Receiver
				params = decl.Params
				typeParams = decl.TypeParams
			}
		case *ast.InterfaceDecl:
			if decl != nil {
				if iface, ok := decl.Type.(*ast.InterfaceType); ok && iface != nil {
					for i := range iface.Methods {
						method := &iface.Methods[i]
						if method.Name != nil && method.Name.Name == sym.Name {
							receiver = method.Receiver
							params = method.Params
							typeParams = method.TypeParams
							break
						}
					}
				}
			}
		}

		writeParam := func(index int, source *ast.Param) {
			if source != nil && source.IsMutable {
				b.WriteString("mut ")
			}
			paramName := ""
			if source == nil {
				if index < len(callable.ParamNames) {
					paramName = callable.ParamNames[index]
				}
			} else if source.Name != nil {
				paramName = source.Name.Name
				if index < len(callable.ParamNames) && callable.ParamNames[index] != "" {
					paramName = callable.ParamNames[index]
				}
			}
			if paramName != "" {
				b.WriteString(paramName)
				b.WriteString(": ")
			}
			if index < len(callable.Params) {
				b.WriteString(typeinfo.TypeText(callable.Params[index]))
			}
			if source != nil && source.Default != nil {
				b.WriteString(" = ")
				b.WriteString(ast.ExprText(source.Default))
			}
		}

		b.WriteString("fn ")
		start := 0
		if sym.Kind == symbols.SymbolMethod && len(callable.Params) > 0 {
			b.WriteString("(")
			writeParam(0, receiver)
			b.WriteString(") ")
			start = 1
		}
		b.WriteString(name)
		if len(typeParams) > 0 {
			b.WriteString("<")
			written := 0
			for _, typeParam := range typeParams {
				if typeParam.Name == nil {
					continue
				}
				if written > 0 {
					b.WriteString(", ")
				}
				b.WriteString(typeParam.Name.Name)
				written++
			}
			b.WriteString(">")
		}
		b.WriteString("(")
		for i := start; i < len(callable.Params); i++ {
			if i > start {
				b.WriteString(", ")
			}
			var source *ast.Param
			sourceIndex := i - start
			if sourceIndex < len(params) {
				source = &params[sourceIndex]
			}
			writeParam(i, source)
		}
		b.WriteString(")")
		if result := typeinfo.TypeText(callable.Return); result != "" {
			b.WriteString(" -> ")
			b.WriteString(result)
		}
		b.WriteString(callable.ReturnOriginText())
		return b.String()
	}

	if sym.IsMutable() {
		b.WriteString("mut ")
	}
	b.WriteString(name)
	if sym.Kind != symbols.SymbolType && typ != nil {
		b.WriteString(": ")
		b.WriteString(typeinfo.TypeText(typ))
	}
	return b.String()
}
