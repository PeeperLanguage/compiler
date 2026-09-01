package project

import (
	"strconv"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/typeinfo"
)

type namedTypeDeclaration struct {
	syntax ast.TypeDecl
	base   *typeinfo.DefinedType
}

type namedTypeInstance struct {
	ownerModuleID moduleid.ID
	typ           *typeinfo.DefinedType
	ready         chan struct{}
	complete      bool
}

type typeInstantiationFrame struct {
	declarationIdentity string
	applicationIdentity string
	applicationText     string
	node                ast.TypeExpr
}

// RegisterTypeDeclaration records collection's reusable declaration artifact
// and indexes it for concrete substitution in the current context.
func (ctx *CompilerContext) RegisterTypeDeclaration(module *Module, declaration ast.TypeDecl, base *typeinfo.DefinedType) {
	if ctx == nil || module == nil || declaration == nil || base == nil || base.Identity == "" {
		return
	}
	artifact := namedTypeDeclaration{syntax: declaration, base: base}
	ctx.mu.Lock()
	if module.namedTypeDeclarations == nil {
		module.namedTypeDeclarations = make(map[string]namedTypeDeclaration)
	}
	module.namedTypeDeclarations[base.Identity] = artifact
	ctx.typeDeclarations[base.Identity] = module
	ctx.mu.Unlock()
}

func (ctx *CompilerContext) instantiateType(base *typeinfo.DefinedType, arguments []typeinfo.Type, node ast.TypeExpr, chain []typeInstantiationFrame) typeinfo.Type {
	if ctx == nil || base == nil || len(arguments) != len(base.TypeParameters) {
		return &typeinfo.InvalidType{}
	}
	canonicalArguments := make([]typeinfo.Type, len(arguments))
	for index, argument := range arguments {
		canonicalArguments[index] = typeinfo.Unalias(argument)
		if typeinfo.IsInvalid(canonicalArguments[index]) {
			return &typeinfo.InvalidType{}
		}
	}
	declarationArguments := true
	for index, argument := range canonicalArguments {
		if argument != base.TypeParameters[index] {
			declarationArguments = false
			break
		}
	}
	if declarationArguments {
		return base
	}

	argumentKeys := make([]string, len(canonicalArguments))
	for index, argument := range canonicalArguments {
		argumentKeys[index] = typeArgumentIdentity(argument)
	}
	identity := base.Identity + "<" + strings.Join(argumentKeys, ",") + ">"
	applicationText := (&typeinfo.DefinedType{Name: base.Name, TypeArguments: canonicalArguments}).Text()
	for _, origin := range chain {
		if origin.declarationIdentity != base.Identity {
			continue
		}
		if origin.applicationIdentity == identity {
			ctx.mu.RLock()
			cached, ok := ctx.typeInstances[identity]
			ctx.mu.RUnlock()
			if ok && cached.typ != nil {
				return cached.typ
			}
			return &typeinfo.InvalidType{}
		}
		if ctx.Diagnostics != nil {
			diagnostic := diagnostics.NewError("recursive generic applications must preserve exact type arguments").
				WithCode(diagnostics.ErrInvalidType).
				WithPrimaryLabel(ast.LocOf(node), "`"+applicationText+"` changes recursive arguments").
				WithSecondaryLabel(ast.LocOf(origin.node), "`"+origin.applicationText+"` started this instantiation").
				WithHelp("use the same canonical type arguments at every recursive reference")
			ctx.Diagnostics.Add(diagnostic)
		}
		return &typeinfo.InvalidType{}
	}

	ctx.mu.Lock()
	if cached, ok := ctx.typeInstances[identity]; ok && cached.typ != nil && cached.complete {
		ctx.mu.Unlock()
		return cached.typ
	}
	if cached, ok := ctx.typeInstances[identity]; ok && cached.typ != nil {
		ready := cached.ready
		ctx.mu.Unlock()
		<-ready
		ctx.mu.RLock()
		cached, ok = ctx.typeInstances[identity]
		ctx.mu.RUnlock()
		if ok && cached.typ != nil && cached.complete {
			return cached.typ
		}
		return &typeinfo.InvalidType{}
	}
	declarationModule, ok := ctx.typeDeclarations[base.Identity]
	declaration := namedTypeDeclaration{}
	if ok && declarationModule != nil {
		declaration = declarationModule.namedTypeDeclarations[base.Identity]
	}
	if !ok || declarationModule == nil || declaration.syntax == nil || declaration.base != base {
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
		TypeArguments:  canonicalArguments,
	}
	// Cache provisional shell before substitution. Recursive pointer/reference
	// applications resolve back to this exact object.
	ctx.typeInstances[identity] = namedTypeInstance{
		ownerModuleID: declarationModule.ID,
		typ:           instance,
		ready:         make(chan struct{}),
	}
	ctx.mu.Unlock()

	chain = append(chain, typeInstantiationFrame{
		declarationIdentity: base.Identity,
		applicationIdentity: identity,
		applicationText:     applicationText,
		node:                node,
	})
	opts := TypeSyntaxOptions(ctx, declarationModule, nil, true)
	opts.TypeParameters = typeinfo.TypeParameterBindings(base.TypeParameters, canonicalArguments)
	opts.Instantiate = func(nestedBase *typeinfo.DefinedType, nestedArguments []typeinfo.Type, nestedNode ast.TypeExpr) typeinfo.Type {
		return ctx.instantiateType(nestedBase, nestedArguments, nestedNode, chain)
	}
	instance.Underlying = typeinfo.TypeFromSyntax(declaration.syntax.UnderlyingType(), opts)
	valid := !typeinfo.ContainsInvalid(instance.Underlying)
	ctx.finishTypeInstance(identity, instance, valid)
	if !valid {
		return &typeinfo.InvalidType{}
	}
	return instance
}

// finishTypeInstance publishes or removes one provisional cache entry and
// wakes any concurrent application waiting on the same semantic identity.
func (ctx *CompilerContext) finishTypeInstance(identity string, instance *typeinfo.DefinedType, valid bool) {
	ctx.mu.Lock()
	cached, ok := ctx.typeInstances[identity]
	if !ok || cached.typ != instance {
		ctx.mu.Unlock()
		return
	}
	if valid {
		cached.complete = true
		ctx.typeInstances[identity] = cached
	} else {
		delete(ctx.typeInstances, identity)
	}
	close(cached.ready)
	ctx.mu.Unlock()
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
