package typeresolution

import (
	"slices"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/types"
)

type namedTypeInstance struct {
	base       *types.DefinedType
	typ        *types.DefinedType
	ready      chan struct{}
	isComplete bool
}

type typeInstantiationFrame struct {
	declarationIdentity string
	applicationIdentity string
	applicationText     string
	node                ast.TypeExpr
}

func typeDeclaration(owner *module.Module, base *types.DefinedType) (ast.TypeDecl, bool) {
	if owner == nil || owner.ModuleScope == nil || base == nil {
		return nil, false
	}
	symbol, found := owner.ModuleScope.LookupLocal(base.Name)
	if !found || symbol.Kind != symbols.SymbolType || symbol.Type != base {
		return nil, false
	}
	declaration, ok := symbol.ASTNode.(ast.TypeDecl)
	return declaration, ok && declaration != nil
}

func (r *Resolver) instantiateType(diag *diagnostics.DiagnosticBag, base *types.DefinedType, arguments []types.Type, node ast.TypeExpr, chain []typeInstantiationFrame) types.Type {
	if r == nil {
		return &types.InvalidType{}
	}
	canonicalArguments, declarationArguments, ok := canonicalTypeApplication(base, arguments)
	if !ok {
		return &types.InvalidType{}
	}
	if declarationArguments {
		return base
	}
	identity := typeInstanceIdentity(base, canonicalArguments)
	applicationText := types.TypeText(&types.DefinedType{Name: base.Name, TypeArguments: canonicalArguments})
	for _, origin := range chain {
		if origin.declarationIdentity != base.Identity {
			continue
		}
		if origin.applicationIdentity == identity {
			r.mu.RLock()
			cached, ok := r.instances[identity]
			r.mu.RUnlock()
			if ok && cached.typ != nil {
				return cached.typ
			}
			return &types.InvalidType{}
		}
		if diag != nil {
			diagnostic := diagnostics.NewError("recursive generic applications must preserve exact type arguments").
				WithCode(diagnostics.ErrInvalidType).
				WithPrimaryLabel(ast.LocOf(node), "`"+applicationText+"` changes recursive arguments").
				WithSecondaryLabel(ast.LocOf(origin.node), "`"+origin.applicationText+"` started this instantiation").
				Help("use the same canonical type arguments at every recursive reference")
			diag.Add(diagnostic)
		}
		return &types.InvalidType{}
	}

	r.mu.Lock()
	if cached, ok := r.instances[identity]; ok && cached.typ != nil && cached.isComplete {
		r.mu.Unlock()
		return cached.typ
	}
	if cached, ok := r.instances[identity]; ok && cached.typ != nil {
		ready := cached.ready
		r.mu.Unlock()
		<-ready
		r.mu.RLock()
		cached, ok = r.instances[identity]
		r.mu.RUnlock()
		if ok && cached.typ != nil && cached.isComplete {
			return cached.typ
		}
		return &types.InvalidType{}
	}
	declarationModule := r.typeOwner(base.Identity)
	declaration, declarationOK := typeDeclaration(declarationModule, base)
	if !declarationOK {
		r.mu.Unlock()
		if diag != nil {
			diag.AddError(diagnostics.ErrInvalidType,
				"generic type declaration is unavailable for `"+types.TypeText(base)+"`", ast.LocOf(node), "recompile declaration module")
		}
		return &types.InvalidType{}
	}
	instance := &types.DefinedType{
		Name:           base.Name,
		Identity:       identity,
		Kind:           base.Kind,
		TypeParameters: base.TypeParameters,
		TypeArguments:  canonicalArguments,
	}
	// Cache provisional shell before substitution. Recursive pointer/reference
	// applications resolve back to this exact object.
	r.instances[identity] = namedTypeInstance{
		base:  base,
		typ:   instance,
		ready: make(chan struct{}),
	}
	r.mu.Unlock()

	chain = append(chain, typeInstantiationFrame{
		declarationIdentity: base.Identity,
		applicationIdentity: identity,
		applicationText:     applicationText,
		node:                node,
	})
	instance.Underlying = r.typeInstanceUnderlying(diag, declarationModule, declaration, base, instance, chain)
	valid := !types.ContainsInvalid(instance.Underlying)
	r.finishTypeInstance(identity, instance, valid)
	if !valid {
		return &types.InvalidType{}
	}
	return instance
}

func (r *Resolver) typeInstanceUnderlying(diag *diagnostics.DiagnosticBag, declarationModule *module.Module, declaration ast.TypeDecl, base, instance *types.DefinedType, chain []typeInstantiationFrame) types.Type {
	syntax := declaration.UnderlyingType()
	context := Context{
		AllowAbstractSelf: true,
		TypeParameters:    types.TypeParameterBindings(base.TypeParameters, instance.TypeArguments),
	}
	if _, ok := declaration.(*ast.InterfaceDecl); ok {
		context.NamedInterfaceRoot = syntax
	}
	// An instance is not the declaring module's own source: it gets no index, so
	// names resolve through that module's scope and nothing is read from or
	// recorded in its index. That module recorded this declaration when it bound it.
	underlying, _ := r.resolve(diag, declarationModule, nil, syntax, context, chain)
	return underlying
}

// CompleteTypeInstances rebuilds cached instances in place after binder fills
// every shell in a legal declaration cycle. Pointer identity remains stable for
// recursive references and existing cache consumers.
func (r *Resolver) CompleteTypeInstances(diag *diagnostics.DiagnosticBag, bases []*types.DefinedType) {
	if r == nil || len(bases) == 0 {
		return
	}
	selected := make(map[*types.DefinedType]bool, len(bases))
	for _, base := range bases {
		if base != nil && len(base.TypeParameters) > 0 {
			selected[base] = true
		}
	}
	if len(selected) == 0 {
		return
	}

	r.mu.RLock()
	identities := make([]string, 0)
	for identity, cached := range r.instances {
		if cached.isComplete && cached.typ != nil && selected[cached.base] {
			identities = append(identities, identity)
		}
	}
	r.mu.RUnlock()
	slices.Sort(identities)

	for _, identity := range identities {
		r.mu.RLock()
		cached := r.instances[identity]
		declarationModule := r.typeOwner(cached.base.Identity)
		declaration, declarationOK := typeDeclaration(declarationModule, cached.base)
		r.mu.RUnlock()
		if !declarationOK {
			continue
		}
		chain := []typeInstantiationFrame{{
			declarationIdentity: cached.base.Identity,
			applicationIdentity: identity,
			applicationText:     types.TypeText(cached.typ),
			node:                declaration.UnderlyingType(),
		}}
		underlying := r.typeInstanceUnderlying(diag, declarationModule, declaration, cached.base, cached.typ, chain)
		if !types.ContainsInvalid(underlying) {
			cached.typ.Underlying = underlying
		}
	}
}

// finishTypeInstance publishes or removes one provisional cache entry and
// wakes any concurrent application waiting on the same semantic identity.
func (r *Resolver) finishTypeInstance(identity string, instance *types.DefinedType, valid bool) {
	r.mu.Lock()
	cached, ok := r.instances[identity]
	if !ok || cached.typ != instance {
		r.mu.Unlock()
		return
	}
	if valid {
		cached.isComplete = true
		r.instances[identity] = cached
	} else {
		delete(r.instances, identity)
	}
	close(cached.ready)
	r.mu.Unlock()
}

func canonicalTypeApplication(base *types.DefinedType, arguments []types.Type) (canonical []types.Type, declarationArguments, ok bool) {
	if base == nil || len(arguments) != len(base.TypeParameters) {
		return nil, false, false
	}
	canonical = make([]types.Type, len(arguments))
	for index, argument := range arguments {
		canonical[index] = types.Unalias(argument)
		if types.IsInvalid(canonical[index]) {
			return nil, false, false
		}
	}
	declarationArguments = true
	for index, argument := range canonical {
		if argument != base.TypeParameters[index] {
			declarationArguments = false
			break
		}
	}
	return canonical, declarationArguments, true
}

func typeInstanceIdentity(base *types.DefinedType, arguments []types.Type) string {
	argumentKeys := make([]string, len(arguments))
	for index, argument := range arguments {
		argumentKeys[index] = typeArgumentIdentity(argument)
	}
	return base.Identity + "<" + strings.Join(argumentKeys, ",") + ">"
}

// lookupTypeInstance performs an observational cache lookup. Loading means no
// complete instance is available yet; unlike instantiateType, this operation
// never creates a shell, waits for completion, emits diagnostics, or mutates state.
func (r *Resolver) lookupTypeInstance(base *types.DefinedType, arguments []types.Type) QueryResult {
	if r == nil {
		return QueryResult{Type: &types.InvalidType{}, Status: QueryInvalid}
	}
	canonicalArguments, declarationArguments, ok := canonicalTypeApplication(base, arguments)
	if !ok {
		return QueryResult{Type: &types.InvalidType{}, Status: QueryInvalid}
	}
	if declarationArguments {
		return QueryResult{Type: base, Status: QueryAvailable}
	}
	identity := typeInstanceIdentity(base, canonicalArguments)
	r.mu.RLock()
	cached, found := r.instances[identity]
	r.mu.RUnlock()
	if !found || !cached.isComplete || cached.typ == nil {
		return QueryResult{Status: QueryLoading}
	}
	return QueryResult{Type: cached.typ, Status: QueryAvailable}
}

func typeArgumentIdentity(typ types.Type) string {
	switch value := typ.(type) {
	case *types.DefinedType:
		if value != nil && value.Identity != "" {
			return "defined:" + value.Identity
		}
	}
	return types.SemanticKey(typ)
}
