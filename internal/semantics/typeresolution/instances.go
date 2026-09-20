package typeresolution

import (
	"slices"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/typeinfo"
)

type namedTypeInstance struct {
	ownerModuleID moduleid.ID
	base          *typeinfo.DefinedType
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
func (r *Resolver) RegisterTypeDeclaration(owner *module.Module, declaration ast.TypeDecl, base *typeinfo.DefinedType) {
	if r == nil || owner == nil || declaration == nil || base == nil || base.Identity == "" {
		return
	}
	r.mu.Lock()
	owner.RecordTypeDeclaration(base.Identity, module.TypeDeclaration{Syntax: declaration, Base: base})
	r.declarations[base.Identity] = owner
	r.mu.Unlock()
}

func (r *Resolver) instantiateType(diag *diagnostics.DiagnosticBag, base *typeinfo.DefinedType, arguments []typeinfo.Type, node ast.TypeExpr, chain []typeInstantiationFrame) typeinfo.Type {
	if r == nil {
		return &typeinfo.InvalidType{}
	}
	canonicalArguments, declarationArguments, ok := canonicalTypeApplication(base, arguments)
	if !ok {
		return &typeinfo.InvalidType{}
	}
	if declarationArguments {
		return base
	}
	identity := typeInstanceIdentity(base, canonicalArguments)
	applicationText := (&typeinfo.DefinedType{Name: base.Name, TypeArguments: canonicalArguments}).Text()
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
			return &typeinfo.InvalidType{}
		}
		if diag != nil {
			diagnostic := diagnostics.NewError("recursive generic applications must preserve exact type arguments").
				WithCode(diagnostics.ErrInvalidType).
				WithPrimaryLabel(ast.LocOf(node), "`"+applicationText+"` changes recursive arguments").
				WithSecondaryLabel(ast.LocOf(origin.node), "`"+origin.applicationText+"` started this instantiation").
				WithHelp("use the same canonical type arguments at every recursive reference")
			diag.Add(diagnostic)
		}
		return &typeinfo.InvalidType{}
	}

	r.mu.Lock()
	if cached, ok := r.instances[identity]; ok && cached.typ != nil && cached.complete {
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
		if ok && cached.typ != nil && cached.complete {
			return cached.typ
		}
		return &typeinfo.InvalidType{}
	}
	declarationModule, ok := r.declarations[base.Identity]
	declaration := module.TypeDeclaration{}
	declarationOK := false
	if declarationModule != nil {
		declaration, declarationOK = declarationModule.TypeDeclaration(base.Identity)
	}
	if !ok || declarationModule == nil || !declarationOK || declaration.Syntax == nil || declaration.Base != base {
		r.mu.Unlock()
		if diag != nil {
			diag.AddError(diagnostics.ErrInvalidType,
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
	r.instances[identity] = namedTypeInstance{
		ownerModuleID: declarationModule.ID,
		base:          base,
		typ:           instance,
		ready:         make(chan struct{}),
	}
	r.mu.Unlock()

	chain = append(chain, typeInstantiationFrame{
		declarationIdentity: base.Identity,
		applicationIdentity: identity,
		applicationText:     applicationText,
		node:                node,
	})
	instance.Underlying = r.typeInstanceUnderlying(diag, declarationModule, declaration, instance, chain)
	valid := !typeinfo.ContainsInvalid(instance.Underlying)
	r.finishTypeInstance(identity, instance, valid)
	if !valid {
		return &typeinfo.InvalidType{}
	}
	return instance
}

func (r *Resolver) typeInstanceUnderlying(diag *diagnostics.DiagnosticBag, declarationModule *module.Module, declaration module.TypeDeclaration, instance *typeinfo.DefinedType, chain []typeInstantiationFrame) typeinfo.Type {
	syntax := declaration.Syntax.UnderlyingType()
	context := Context{
		AllowAbstractSelf: true,
		TypeParameters:    typeinfo.TypeParameterBindings(declaration.Base.TypeParameters, instance.TypeArguments),
	}
	if _, ok := declaration.Syntax.(*ast.InterfaceDecl); ok {
		context.NamedInterfaceRoot = syntax
	}
	return r.resolve(diag, declarationModule, syntax, context, chain)
}

// CompleteTypeInstances rebuilds cached instances in place after binder fills
// every shell in a legal declaration cycle. Pointer identity remains stable for
// recursive references and existing cache consumers.
func (r *Resolver) CompleteTypeInstances(diag *diagnostics.DiagnosticBag, bases []*typeinfo.DefinedType) {
	if r == nil || len(bases) == 0 {
		return
	}
	selected := make(map[*typeinfo.DefinedType]bool, len(bases))
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
		if cached.complete && cached.typ != nil && selected[cached.base] {
			identities = append(identities, identity)
		}
	}
	r.mu.RUnlock()
	slices.Sort(identities)

	for _, identity := range identities {
		r.mu.RLock()
		cached := r.instances[identity]
		declarationModule := r.declarations[cached.base.Identity]
		declaration := module.TypeDeclaration{}
		declarationOK := false
		if declarationModule != nil {
			declaration, declarationOK = declarationModule.TypeDeclaration(cached.base.Identity)
		}
		r.mu.RUnlock()
		if declarationModule == nil || !declarationOK || declaration.Syntax == nil || declaration.Base != cached.base {
			continue
		}
		chain := []typeInstantiationFrame{{
			declarationIdentity: cached.base.Identity,
			applicationIdentity: identity,
			applicationText:     cached.typ.Text(),
			node:                declaration.Syntax.UnderlyingType(),
		}}
		underlying := r.typeInstanceUnderlying(diag, declarationModule, declaration, cached.typ, chain)
		if !typeinfo.ContainsInvalid(underlying) {
			cached.typ.Underlying = underlying
		}
	}
}

// finishTypeInstance publishes or removes one provisional cache entry and
// wakes any concurrent application waiting on the same semantic identity.
func (r *Resolver) finishTypeInstance(identity string, instance *typeinfo.DefinedType, valid bool) {
	r.mu.Lock()
	cached, ok := r.instances[identity]
	if !ok || cached.typ != instance {
		r.mu.Unlock()
		return
	}
	if valid {
		cached.complete = true
		r.instances[identity] = cached
	} else {
		delete(r.instances, identity)
	}
	close(cached.ready)
	r.mu.Unlock()
}

func canonicalTypeApplication(base *typeinfo.DefinedType, arguments []typeinfo.Type) (canonical []typeinfo.Type, declarationArguments, ok bool) {
	if base == nil || len(arguments) != len(base.TypeParameters) {
		return nil, false, false
	}
	canonical = make([]typeinfo.Type, len(arguments))
	for index, argument := range arguments {
		canonical[index] = typeinfo.Unalias(argument)
		if typeinfo.IsInvalid(canonical[index]) {
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

func typeInstanceIdentity(base *typeinfo.DefinedType, arguments []typeinfo.Type) string {
	argumentKeys := make([]string, len(arguments))
	for index, argument := range arguments {
		argumentKeys[index] = typeArgumentIdentity(argument)
	}
	return base.Identity + "<" + strings.Join(argumentKeys, ",") + ">"
}

// lookupTypeInstance performs an observational cache lookup. Loading means no
// complete instance is available yet; unlike instantiateType, this operation
// never creates a shell, waits for completion, emits diagnostics, or mutates state.
func (r *Resolver) lookupTypeInstance(base *typeinfo.DefinedType, arguments []typeinfo.Type) QueryResult {
	if r == nil {
		return QueryResult{Type: &typeinfo.InvalidType{}, Status: QueryInvalid}
	}
	canonicalArguments, declarationArguments, ok := canonicalTypeApplication(base, arguments)
	if !ok {
		return QueryResult{Type: &typeinfo.InvalidType{}, Status: QueryInvalid}
	}
	if declarationArguments {
		return QueryResult{Type: base, Status: QueryAvailable}
	}
	identity := typeInstanceIdentity(base, canonicalArguments)
	r.mu.RLock()
	cached, found := r.instances[identity]
	r.mu.RUnlock()
	if !found || !cached.complete || cached.typ == nil {
		return QueryResult{Status: QueryLoading}
	}
	return QueryResult{Type: cached.typ, Status: QueryAvailable}
}

func typeArgumentIdentity(typ typeinfo.Type) string {
	switch value := typ.(type) {
	case *typeinfo.DefinedType:
		if value != nil && value.Identity != "" {
			return "defined:" + value.Identity
		}
	}
	return typeinfo.SemanticKey(typ)
}
