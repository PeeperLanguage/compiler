package typeresolution

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

// Context carries type construction facts that vary per conversion.
type Context struct {
	SelfType           typeinfo.Type
	AllowAbstractSelf  bool
	TypeParameters     map[string]typeinfo.Type
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
	Type   typeinfo.Type
	Status QueryStatus
}

type syntaxResolver struct {
	resolver *Resolver
	module   *module.Module
	query    *QueryResult
	diag     *diagnostics.DiagnosticBag
	chain    []typeInstantiationFrame
}

func (r *Resolver) Resolve(diag *diagnostics.DiagnosticBag, mod *module.Module, node ast.TypeExpr, context Context) typeinfo.Type {
	return r.resolve(diag, mod, node, context, nil)
}

// Query resolves syntax without publishing source evidence or creating generic
// instances. Loading means valid evidence may exist later; Invalid means syntax
// resolved to an invalid semantic type.
func (r *Resolver) Query(mod *module.Module, node ast.TypeExpr, context Context) QueryResult {
	result := QueryResult{Status: QueryAvailable}
	syntax := syntaxResolver{resolver: r, module: mod, query: &result}
	result.Type = typeinfo.TypeFromSyntax(node, syntax.context(context, r.compilerTarget(), nil))
	if typeinfo.ContainsInvalid(result.Type) {
		result.Status = QueryInvalid
	}
	if result.Status == QueryLoading {
		result.Type = nil
	}
	return result
}

func (r *Resolver) ResolveFunction(diag *diagnostics.DiagnosticBag, mod *module.Module, fn *ast.FnDecl, context Context) *typeinfo.FuncType {
	if fn == nil {
		return nil
	}
	issues := make([]typeinfo.SyntaxIssue, 0, 1)
	syntax := syntaxResolver{resolver: r, module: mod, diag: diag}
	compilerTarget := r.compilerTarget()
	fnType := typeinfo.FuncTypeFromDecl(fn, syntax.context(context, compilerTarget, &issues))
	reportSyntaxIssues(diag, compilerTarget.IndexBits, issues)
	return fnType
}

func (r *Resolver) resolve(diag *diagnostics.DiagnosticBag, mod *module.Module, node ast.TypeExpr, context Context, chain []typeInstantiationFrame) typeinfo.Type {
	syntax := syntaxResolver{resolver: r, module: mod, diag: diag, chain: chain}
	issues := make([]typeinfo.SyntaxIssue, 0, 1)
	compilerTarget := r.compilerTarget()
	typ := typeinfo.TypeFromSyntax(node, syntax.context(context, compilerTarget, &issues))
	reportSyntaxIssues(diag, compilerTarget.IndexBits, issues)
	return typ
}

func (r *Resolver) compilerTarget() target.Info {
	if r != nil && r.target.Valid() {
		return r.target
	}
	return target.Host()
}

func reportSyntaxIssues(diag *diagnostics.DiagnosticBag, indexBits int, issues []typeinfo.SyntaxIssue) {
	if diag == nil {
		return
	}
	for _, issue := range issues {
		switch issue.Kind {
		case typeinfo.SyntaxInvalidSelf:
			diag.AddError(diagnostics.ErrInvalidType,
				"`Self` can only be used as an iface method receiver", ast.LocOf(issue.Node), "")
		case typeinfo.SyntaxInvalidArrayLength:
			diag.AddError(diagnostics.ErrInvalidType,
				fmt.Sprintf("array length must be an integer literal that fits its explicit type and target usize (u%d)", indexBits),
				ast.LocOf(issue.Node), "invalid array length")
		case typeinfo.SyntaxInvalidApplication:
			word := "arguments"
			if issue.Want == 1 {
				word = "argument"
			}
			diag.AddError(diagnostics.ErrInvalidType,
				fmt.Sprintf("type `%s` expects %d type %s, got %d", issue.Name, issue.Want, word, issue.Got),
				ast.LocOf(issue.Node), "use exact explicit type arguments")
		case typeinfo.SyntaxAnonymousInterface:
			diag.AddError(diagnostics.ErrInvalidType,
				"anonymous interface types are not supported yet", ast.LocOf(issue.Node),
				"declare a named interface and use &Name, &mut Name, or *Name")
		}
	}
}

func (r syntaxResolver) context(context Context, compilerTarget target.Info, issues *[]typeinfo.SyntaxIssue) typeinfo.SyntaxContext {
	return typeinfo.SyntaxContext{
		Target:             compilerTarget,
		SelfType:           context.SelfType,
		AllowAbstractSelf:  context.AllowAbstractSelf,
		TypeParameters:     context.TypeParameters,
		NamedInterfaceRoot: context.NamedInterfaceRoot,
		Resolver:           r,
		Issues:             issues,
	}
}

func (r syntaxResolver) ResolveNamed(node ast.TypeExpr) (typeinfo.Type, bool) {
	if r.module == nil || r.module.ModuleScope == nil || node == nil {
		return nil, false
	}
	if r.module.Bindings != nil {
		if sym := r.module.Bindings.Symbol(node); sym != nil && sym.Kind == symbols.SymbolType {
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
	if r.query == nil && r.module.Bindings != nil {
		r.module.Bindings.Bind(node, sym)
	}
	return symbols.GetSymbolType(sym)
}

func (r syntaxResolver) ResolveQualified(node *ast.ScopeResolution) (typeinfo.Type, bool) {
	if node == nil || r.module == nil || r.resolver == nil {
		return nil, false
	}
	qualifier, member, imported := node.ImportMember()
	if !imported {
		return nil, false
	}
	if r.module.Bindings != nil {
		if sym := r.module.Bindings.Symbol(node); sym != nil && sym.Kind == symbols.SymbolType && sym.IsPub {
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
		if r.module.Bindings != nil {
			r.module.Bindings.Bind(node, resolved.Symbol)
		}
	}
	return symbols.GetSymbolType(resolved.Symbol)
}

func (r syntaxResolver) Instantiate(base *typeinfo.DefinedType, arguments []typeinfo.Type, node ast.TypeExpr) typeinfo.Type {
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
			return &typeinfo.UnknownType{}
		}
		return &typeinfo.InvalidType{}
	}
	if r.resolver == nil {
		return &typeinfo.InvalidType{}
	}
	return r.resolver.instantiateType(r.diag, base, arguments, node, r.chain)
}

func (r syntaxResolver) record(sym *symbols.Symbol) {
	if r.query == nil && sym != nil {
		sym.MarkUsed()
	}
}
