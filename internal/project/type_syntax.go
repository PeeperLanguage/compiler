package project

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

// TypeSyntaxOptions bridges project/module state into generic type parsing.
// TypeFromSyntax stays reusable because this adapter injects the current
// module's name lookup, import lookup, and `Self` rules without hard-wiring
// project state into the typeinfo package.
func TypeSyntaxOptions(ctx *CompilerContext, module *Module, selfType typeinfo.Type, allowAbstractSelf bool) typeinfo.SyntaxOptions {
	compilerTarget := target.Host()
	if ctx != nil && ctx.Target.Valid() {
		compilerTarget = ctx.Target
	}
	return typeinfo.SyntaxOptions{
		Target:            compilerTarget,
		SelfType:          selfType,
		AllowAbstractSelf: allowAbstractSelf,
		ResolveNamed: func(name string) (typeinfo.Type, bool) {
			if module == nil || module.ModuleScope == nil || name == "" {
				return nil, false
			}
			sym, found := module.ModuleScope.Lookup(name)
			if !found || sym == nil || sym.Kind != symbols.SymbolType {
				return nil, false
			}
			sym.Used = true
			return symbols.GetSymbolType(sym)
		},
		ResolveQualified: func(moduleName, memberName string) (typeinfo.Type, bool) {
			resolved, ok := LookupImportedSymbol(ctx, module, moduleName, memberName)
			if !ok || resolved.Symbol == nil || resolved.Symbol.Kind != symbols.SymbolType {
				return nil, false
			}
			return symbols.GetSymbolType(resolved.Symbol)
		},
		Instantiate: func(base *typeinfo.DefinedType, arguments []typeinfo.Type, node ast.TypeExpr) typeinfo.Type {
			return ctx.instantiateType(base, arguments, node)
		},
		InvalidSelf: func(node *ast.NamedType) typeinfo.Type {
			if ctx != nil && ctx.Diagnostics != nil {
				ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
					"`Self` can only be used as an iface method receiver", ast.LocOf(node), "")
			}
			return &typeinfo.InvalidType{}
		},
		InvalidArrayLen: func(node *ast.NumberLit) typeinfo.Type {
			if ctx != nil && ctx.Diagnostics != nil {
				ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
					fmt.Sprintf("array length must be an integer literal that fits its explicit type and target usize (u%d)", compilerTarget.IndexBits),
					ast.LocOf(node), "invalid array length")
			}
			return &typeinfo.InvalidType{}
		},
		InvalidApplication: func(node ast.TypeExpr, name string, want, got int) typeinfo.Type {
			if ctx != nil && ctx.Diagnostics != nil {
				word := "arguments"
				if want == 1 {
					word = "argument"
				}
				ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
					fmt.Sprintf("type `%s` expects %d type %s, got %d", name, want, word, got),
					ast.LocOf(node), "use exact explicit type arguments")
			}
			return &typeinfo.InvalidType{}
		},
	}
}
