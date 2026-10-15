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
}

func (r *Resolver) Resolve(diag *diagnostics.DiagnosticBag, mod *module.Module, node ast.TypeExpr, context Context) types.Type {
	return r.resolve(diag, mod, node, context, nil)
}

// Query resolves syntax without publishing source evidence or creating generic
// instances. Loading means valid evidence may exist later; Invalid means syntax
// resolved to an invalid semantic type.
func (r *Resolver) Query(mod *module.Module, node ast.TypeExpr, context Context) QueryResult {
	result := QueryResult{Status: QueryAvailable}
	syntax := syntaxResolver{resolver: r, module: mod, query: &result}
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
	syntax := syntaxResolver{resolver: r, module: mod, diag: diag}
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
	return fnType
}

func (r *Resolver) resolve(diag *diagnostics.DiagnosticBag, mod *module.Module, node ast.TypeExpr, context Context, chain []typeInstantiationFrame) types.Type {
	syntax := syntaxResolver{resolver: r, module: mod, diag: diag, chain: chain}
	issues := make([]types.SyntaxIssue, 0, 1)
	compilerTarget := r.compilerTarget()
	typ := types.TypeFromSyntax(node, syntax.context(context, compilerTarget, &issues))
	reportSyntaxIssues(diag, mod, compilerTarget.IndexBits, issues)
	return typ
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
	if r.module.SymbolIndex != nil {
		if sym := r.module.SymbolIndex.Symbol(node); sym != nil && sym.Kind == symbols.SymbolType {
			r.record(sym)
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
	r.record(sym)
	if r.query == nil && r.module.SymbolIndex != nil {
		r.module.SymbolIndex.Bind(node, sym)
	}
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
	if r.module.SymbolIndex != nil {
		if sym := r.module.SymbolIndex.Symbol(node); sym != nil && sym.Kind == symbols.SymbolType && sym.IsPub {
			if r.query == nil {
				r.module.RecordImportedUse(qualifier.Name, sym)
			}
			return symbols.GetSymbolType(sym)
		}
	}
	resolved, ok := r.resolver.LookupImportedSymbol(r.module, qualifier.Name, member.Name)
	if !ok || resolved.Symbol == nil || resolved.Symbol.Kind != symbols.SymbolType || !resolved.Symbol.IsPub {
		return nil, false
	}
	if r.query == nil {
		r.module.RecordImportedUse(qualifier.Name, resolved.Symbol)
		if r.module.SymbolIndex != nil {
			r.module.SymbolIndex.Bind(node, resolved.Symbol)
		}
	}
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

func (r syntaxResolver) record(sym *symbols.Symbol) {
	if r.query == nil && r.module != nil && r.module.SymbolIndex != nil && sym != nil {
		r.module.SymbolIndex.MarkUsed(sym)
	}
}
