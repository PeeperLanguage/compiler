package typeresolution

import (
	"fmt"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/types"
	"compiler/internal/target"
)

// Context carries type construction facts that vary per conversion.
type Context struct {
	SelfType           types.Type
	AllowAbstractSelf  bool
	TypeParameters     map[string]types.Type
	NamedInterfaceRoot ast.TypeExpr
}

// QueryStatus distinguishes resolved semantic evidence from temporary cache
// unavailability and genuine invalidity.
type QueryStatus uint8

const (
	QueryAvailable QueryStatus = iota
	QueryLoading
	QueryInvalid
)

// QueryResult is the observational result of type syntax lookup. Type is nil
// while loading and when no syntax was supplied.
type QueryResult struct {
	Type   types.Type
	Status QueryStatus
}

type syntaxResolver struct {
	resolver *Resolver
	module   *module.Module
	query    *QueryResult
	diag     *diagnostics.DiagnosticBag
	chain    []typeInstantiationFrame
	// index is the symbol index this resolver may read: the caller's own module's
	// index, set only by the package's entry points. It is nil while building an
	// instance from another module's declaration, whose goroutine may be writing
	// that index; names then resolve through that module's scope.
	index *symbols.Index
	// uses collects the symbols the syntax names; nil collects nothing.
	uses *[]typeUse
}

// typeUse is one symbol a type's syntax names. An import alias is set when the
// name went through one.
type typeUse struct {
	node   ast.Node
	symbol *symbols.Symbol
	alias  string
}

// Resolve resolves mod's own type syntax and records what it names in mod's
// index.
func (r *Resolver) Resolve(diag *diagnostics.DiagnosticBag, mod *module.Module, node ast.TypeExpr, context Context) types.Type {
	typ, uses := r.resolve(diag, mod, ownIndex(mod), node, context, nil)
	publishTypeUses(mod, uses)
	return typ
}

// ownIndex is the index of the module whose source an entry point resolves.
func ownIndex(mod *module.Module) *symbols.Index {
	if mod == nil {
		return nil
	}
	return mod.SymbolIndex
}

// publishTypeUses records in mod's own index the symbols its source names. Only
// an entry point that compiles mod's source calls it: a module writes only its
// own index, and it may be running its phases on another goroutine.
func publishTypeUses(mod *module.Module, uses []typeUse) {
	if mod == nil || mod.SymbolIndex == nil {
		return
	}
	for _, use := range uses {
		if use.alias != "" {
			mod.RecordImportedUse(use.alias, use.symbol)
		} else {
			mod.SymbolIndex.MarkUsed(use.symbol)
		}
		mod.SymbolIndex.Bind(use.node, use.symbol)
	}
}

// Query resolves syntax without publishing source evidence or creating generic
// instances. Loading means valid evidence may exist later; Invalid means syntax
// resolved to an invalid semantic type.
func (r *Resolver) Query(mod *module.Module, node ast.TypeExpr, context Context) QueryResult {
	result := QueryResult{Status: QueryAvailable}
	syntax := syntaxResolver{resolver: r, module: mod, index: ownIndex(mod), query: &result}
	result.Type = types.TypeFromSyntax(node, syntax.context(context, r.compilerTarget(), nil))
	if types.ContainsMalformed(result.Type) {
		result.Status = QueryInvalid
	}
	if result.Status == QueryLoading {
		result.Type = nil
	}
	return result
}

func (r *Resolver) ResolveFunction(diag *diagnostics.DiagnosticBag, mod *module.Module, fn *ast.FnDecl, context Context) *types.FuncType {
	if fn == nil {
		return nil
	}
	issues := make([]types.SyntaxIssue, 0, 1)
	var uses []typeUse
	syntax := syntaxResolver{resolver: r, module: mod, index: ownIndex(mod), diag: diag, uses: &uses}
	compilerTarget := r.compilerTarget()
	fnType := types.FuncTypeFromDecl(fn, syntax.context(context, compilerTarget, &issues))
	// A function's own type parameters are not bound to types yet. Their
	// uses keep the name placeholder, which the signature checks reject.
	issues = slices.DeleteFunc(issues, func(issue types.SyntaxIssue) bool {
		return issue.Kind == types.SyntaxUnknownName && slices.ContainsFunc(fn.TypeParams, func(param ast.TypeParam) bool {
			return param.Name != nil && param.Name.Name == issue.Name
		})
	})
	reportSyntaxIssues(diag, mod, compilerTarget.IndexBits, issues)
	publishTypeUses(mod, uses)
	return fnType
}

// resolve builds the type mod's syntax describes and returns the symbols it
// names. It reads index when given one and writes no module state.
func (r *Resolver) resolve(diag *diagnostics.DiagnosticBag, mod *module.Module, index *symbols.Index, node ast.TypeExpr, context Context, chain []typeInstantiationFrame) (types.Type, []typeUse) {
	var uses []typeUse
	syntax := syntaxResolver{resolver: r, module: mod, index: index, diag: diag, chain: chain, uses: &uses}
	issues := make([]types.SyntaxIssue, 0, 1)
	compilerTarget := r.compilerTarget()
	typ := types.TypeFromSyntax(node, syntax.context(context, compilerTarget, &issues))
	reportSyntaxIssues(diag, mod, compilerTarget.IndexBits, issues)
	return typ, uses
}

func (r *Resolver) compilerTarget() target.Info {
	if r != nil && r.target.IsValid() {
		return r.target
	}
	return target.Host()
}

func reportSyntaxIssues(diag *diagnostics.DiagnosticBag, mod *module.Module, indexBits int, issues []types.SyntaxIssue) {
	if diag == nil {
		return
	}
	for _, issue := range issues {
		switch issue.Kind {
		case types.SyntaxInvalidSelf:
			diag.AddError(diagnostics.ErrInvalidType,
				"`Self` can only be used as an iface method receiver", ast.LocOf(issue.Node), "")
		case types.SyntaxInvalidArrayLength:
			diag.AddError(diagnostics.ErrInvalidType,
				fmt.Sprintf("array length must be an integer literal that fits its explicit type and target usize (u%d)", indexBits),
				ast.LocOf(issue.Node), "invalid array length")
		case types.SyntaxInvalidApplication:
			word := "arguments"
			if issue.Want == 1 {
				word = "argument"
			}
			diag.AddError(diagnostics.ErrInvalidType,
				fmt.Sprintf("type `%s` expects %d type %s, got %d", issue.Name, issue.Want, word, issue.Got),
				ast.LocOf(issue.Node), "use exact explicit type arguments")
		case types.SyntaxUnknownName:
			unknown := diag.AddError(diagnostics.ErrUndefinedSymbol, "unknown type `"+issue.Name+"`", ast.LocOf(issue.Node), "")
			if match, ok := diagnostics.NearestName(issue.Name, declaredTypeNames(mod)); ok {
				unknown.Help("did you mean `" + match + "`?")
			}
		case types.SyntaxAnonymousInterface:
			diag.AddError(diagnostics.ErrInvalidType,
				"anonymous interface types are not supported yet", ast.LocOf(issue.Node),
				"declare a named interface and use &Name, &mut Name, or *Name")
		}
	}
}

// declaredTypeNames lists the type names an unqualified name in mod can
// resolve to, for suggesting the one a misspelled name was meant to be.
func declaredTypeNames(mod *module.Module) []string {
	if mod == nil || mod.ModuleScope == nil {
		return nil
	}
	var names []string
	for _, sym := range mod.ModuleScope.Symbols() {
		if sym != nil && sym.Kind == symbols.SymbolType {
			names = append(names, sym.Name)
		}
	}
	return names
}

func (r syntaxResolver) context(context Context, compilerTarget target.Info, issues *[]types.SyntaxIssue) types.SyntaxContext {
	return types.SyntaxContext{
		Target:             compilerTarget,
		SelfType:           context.SelfType,
		AllowAbstractSelf:  context.AllowAbstractSelf,
		TypeParameters:     context.TypeParameters,
		NamedInterfaceRoot: context.NamedInterfaceRoot,
		Resolver:           r,
		Issues:             issues,
	}
}

func (r syntaxResolver) ResolveNamed(node ast.TypeExpr) (types.Type, bool) {
	if r.module == nil || r.module.ModuleScope == nil || node == nil {
		return nil, false
	}
	// A name a binding phase already resolved is bound in the index; default
	// arguments cloned from another module rely on it.
	if r.index != nil {
		if sym := r.index.Symbol(node); sym != nil && sym.Kind == symbols.SymbolType {
			r.record(node, sym, "")
			return symbols.GetSymbolType(sym)
		}
	}
	name := node.TypeText()
	if applied, ok := node.(*ast.AppliedType); ok && applied.Name != nil {
		name = applied.Name.Name
	}
	sym, found := r.module.ModuleScope.Lookup(name)
	if !found || sym == nil || sym.Kind != symbols.SymbolType {
		return nil, false
	}
	r.record(node, sym, "")
	return symbols.GetSymbolType(sym)
}

func (r syntaxResolver) ResolveQualified(node *ast.ScopeResolution) (types.Type, bool) {
	if node == nil || r.module == nil || r.resolver == nil {
		return nil, false
	}
	qualifier, member, imported := node.ImportMember()
	if !imported {
		return nil, false
	}
	if r.index != nil {
		if sym := r.index.Symbol(node); sym != nil && sym.Kind == symbols.SymbolType && sym.IsPub {
			r.record(node, sym, qualifier.Name)
			return symbols.GetSymbolType(sym)
		}
	}
	resolved, ok := r.resolver.LookupImportedSymbol(r.module, qualifier.Name, member.Name)
	if !ok || resolved.Symbol == nil || resolved.Symbol.Kind != symbols.SymbolType || !resolved.Symbol.IsPub {
		return nil, false
	}
	r.record(node, resolved.Symbol, qualifier.Name)
	return symbols.GetSymbolType(resolved.Symbol)
}

func (r syntaxResolver) Instantiate(base *types.DefinedType, arguments []types.Type, node ast.TypeExpr) types.Type {
	if r.query != nil {
		instance := r.resolver.lookupTypeInstance(base, arguments)
		switch instance.Status {
		case QueryInvalid:
			r.query.Status = QueryInvalid
		case QueryLoading:
			if r.query.Status == QueryAvailable {
				r.query.Status = QueryLoading
			}
		}
		if instance.Type != nil {
			return instance.Type
		}
		if instance.Status == QueryLoading {
			return &types.UnknownType{}
		}
		return &types.InvalidType{}
	}
	if r.resolver == nil {
		return &types.InvalidType{}
	}
	return r.resolver.instantiateType(r.diag, base, arguments, node, r.chain)
}

func (r syntaxResolver) record(node ast.Node, sym *symbols.Symbol, alias string) {
	if r.uses != nil && sym != nil {
		*r.uses = append(*r.uses, typeUse{node: node, symbol: sym, alias: alias})
	}
}
