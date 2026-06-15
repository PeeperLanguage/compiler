package project

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/typeinfo"
)

func TypeSyntaxOptions(ctx *CompilerContext, module *Module, selfType typeinfo.Type, allowAbstractSelf bool) typeinfo.SyntaxOptions {
	return typeinfo.SyntaxOptions{
		SelfType:          selfType,
		AllowAbstractSelf: allowAbstractSelf,
		ResolveNamed: func(name string) (typeinfo.Type, bool) {
			return LookupTypeInCurrentModule(module, name)
		},
		ResolveQualified: func(moduleName, memberName string) (typeinfo.Type, bool) {
			return LookupTypeInImportedModule(ctx, module, moduleName, memberName)
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
