package project

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// TypeSyntaxOptions bridges project/module state into generic type parsing.
// ASTTypeWithOptions stays reusable because this adapter injects the current
// module's name lookup, import lookup, and `Self` rules without hard-wiring
// project state into the typeinfo package.
func TypeSyntaxOptions(ctx *CompilerContext, module *Module, selfType typeinfo.Type, allowAbstractSelf bool) typeinfo.SyntaxOptions {
	return typeinfo.SyntaxOptions{
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
		InvalidSelf: func(node *ast.NamedType) typeinfo.Type {
			if ctx != nil && ctx.Diagnostics != nil {
				ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
					"`Self` can only be used in interface methods and impl blocks", ast.LocOf(node), "")
			}
			return &typeinfo.InvalidType{}
		},
	}
}
