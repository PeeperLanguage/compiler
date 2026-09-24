package project

import (
	"fmt"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// SemanticExportFingerprint identifies compiler-visible semantic facts exported by module.
// Constant values resolve through their defining module, so an exported default that
// references an imported constant still changes when that constant changes.
func SemanticExportFingerprint(ctx *CompilerContext, module *module.Module) string {
	if module == nil || module.ModuleScope == nil {
		return ast.FingerprintParts(nil)
	}
	parts := make([]string, 0)
	for _, sym := range module.ModuleScope.Symbols() {
		if sym == nil || !sym.IsPub {
			continue
		}
		part := string(sym.Kind) + ":" + sym.Name + ":" + typeinfo.SemanticKey(sym.Type)
		if sym.Kind == symbols.SymbolVar {
			part += fmt.Sprintf(":mutable=%t", sym.IsMutable())
		}
		part += semanticExportMetadata(ctx, module, sym)
		if sym.Kind == symbols.SymbolConst {
			part += ":value=" + constvalue.SemanticKey(ctx.PublishedConstant(module, sym))
		}
		parts = append(parts, part)
	}
	if module.Bindings != nil {
		module.Bindings.ForEachMethod(func(receiver string, method *symbols.Symbol) {
			if method == nil || !method.IsPub {
				return
			}
			parts = append(parts, "method:"+receiver+":"+method.Name+":"+
				typeinfo.SemanticKey(method.Type)+semanticExportMetadata(ctx, module, method))
		})
	}
	return ast.FingerprintParts(parts)
}

func semanticExportMetadata(ctx *CompilerContext, module *module.Module, sym *symbols.Symbol) string {
	decl, ok := sym.ASTNode.(ast.Decl)
	if !ok || decl == nil {
		return ""
	}
	metadata := ":syntax=" + decl.GetDeclSurface()
	if attributed, ok := decl.(ast.AttributedNode); ok {
		attributes := make([]string, 0)
		for _, attribute := range attributed.GetAttributes() {
			args := make([]string, len(attribute.Args))
			for index, arg := range attribute.Args {
				args[index] = ast.ExprText(arg)
			}
			attributes = append(attributes, attribute.Name+"("+strings.Join(args, ",")+")")
		}
		metadata += ":attributes=" + ast.FingerprintParts(attributes)
	}
	fn, ok := decl.(*ast.FnDecl)
	if !ok || fn == nil {
		return metadata
	}
	if linkName, isExternal := ast.FunctionLinkName(fn, sym.Name); isExternal {
		metadata += ":link=" + linkName
	}
	for index, param := range fn.Params {
		if param.Default == nil {
			continue
		}
		metadata += fmt.Sprintf(":default[%d]=%s", index, ast.ExprText(param.Default))
		facts := make([]string, 0)
		ast.Inspect(param.Default, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || ident == nil || module.Bindings == nil {
				return true
			}
			resolved := module.Bindings.Symbol(ident)
			if resolved == nil {
				return true
			}
			fact := resolved.Name + ":" + typeinfo.SemanticKey(resolved.Type)
			if resolved.Kind == symbols.SymbolConst {
				fact += "=" + constvalue.SemanticKey(ctx.PublishedConstant(module, resolved))
			}
			facts = append(facts, fact)
			return true
		})
		metadata += ":facts=" + ast.FingerprintParts(facts)
	}
	return metadata
}
