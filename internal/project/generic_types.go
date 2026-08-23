package project

import (
	"strconv"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/typeinfo"
)

type namedTypeDeclaration struct {
	module *Module
	syntax ast.TypeDecl
	base   *typeinfo.DefinedType
}

type namedTypeInstance struct {
	ownerModuleKey string
	typ            *typeinfo.DefinedType
}

// RegisterTypeDeclaration preserves declaration syntax beside its stable
// semantic shell so concrete applications can substitute from one source.
func (ctx *CompilerContext) RegisterTypeDeclaration(module *Module, declaration ast.TypeDecl, base *typeinfo.DefinedType) {
	if ctx == nil || module == nil || declaration == nil || base == nil || base.Identity == "" {
		return
	}
	ctx.mu.Lock()
	ctx.typeDeclarations[base.Identity] = namedTypeDeclaration{module: module, syntax: declaration, base: base}
	ctx.mu.Unlock()
}

func (ctx *CompilerContext) instantiateType(base *typeinfo.DefinedType, arguments []typeinfo.Type, node ast.TypeExpr) typeinfo.Type {
	if ctx == nil || base == nil || len(arguments) != len(base.TypeParameters) {
		return &typeinfo.InvalidType{}
	}
	declarationArguments := true
	for index, argument := range arguments {
		if argument != base.TypeParameters[index] {
			declarationArguments = false
			break
		}
	}
	if declarationArguments {
		return base
	}

	argumentKeys := make([]string, len(arguments))
	for index, argument := range arguments {
		argumentKeys[index] = typeArgumentIdentity(argument)
	}
	identity := base.Identity + "<" + strings.Join(argumentKeys, ",") + ">"

	ctx.mu.Lock()
	if cached, ok := ctx.typeInstances[identity]; ok && cached.typ != nil {
		ctx.mu.Unlock()
		return cached.typ
	}
	declaration, ok := ctx.typeDeclarations[base.Identity]
	if !ok || declaration.module == nil || declaration.syntax == nil || declaration.base != base {
		ctx.mu.Unlock()
		if ctx.Diagnostics != nil {
			ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"generic type declaration is unavailable for `"+base.Text()+"`", ast.LocOf(node), "recompile declaration module")
		}
		return &typeinfo.InvalidType{}
	}
	instance := &typeinfo.DefinedType{
		Name:           base.Name,
		Identity:       identity,
		Kind:           base.Kind,
		TypeParameters: base.TypeParameters,
		TypeArguments:  append([]typeinfo.Type(nil), arguments...),
	}
	// Cache provisional shell before substitution. Recursive pointer/reference
	// applications resolve back to this exact object.
	ctx.typeInstances[identity] = namedTypeInstance{ownerModuleKey: declaration.module.Key, typ: instance}
	ctx.mu.Unlock()

	opts := TypeSyntaxOptions(ctx, declaration.module, nil, true)
	opts.TypeParameters = typeinfo.TypeParameterBindings(base.TypeParameters, arguments)
	instance.Underlying = typeinfo.TypeFromSyntax(declaration.syntax.UnderlyingType(), opts)
	return instance
}

func typeArgumentIdentity(typ typeinfo.Type) string {
	switch value := typ.(type) {
	case *typeinfo.DefinedType:
		if value != nil && value.Identity != "" {
			return "defined:" + value.Identity
		}
	case *typeinfo.TypeParameterType:
		if value != nil {
			return "parameter:" + value.OwnerIdentity + ":" + strconv.Itoa(value.Index) + ":" + value.Text()
		}
	}
	return semanticTypeKey(typ, make(map[typeinfo.Type]bool))
}
