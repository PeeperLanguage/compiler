package project

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

// TypeContext carries type construction facts that vary per conversion.
type TypeContext struct {
	SelfType           typeinfo.Type
	AllowAbstractSelf  bool
	TypeParameters     map[string]typeinfo.Type
	NamedInterfaceRoot ast.TypeExpr
}

// TypeQueryStatus distinguishes resolved semantic evidence from temporary cache
// unavailability and genuine invalidity.
type TypeQueryStatus uint8

const (
	TypeQueryAvailable TypeQueryStatus = iota
	TypeQueryLoading
	TypeQueryInvalid
)

// TypeQueryResult is the observational result of type syntax lookup. Type is
// nil while loading and when no syntax was supplied.
type TypeQueryResult struct {
	Type   typeinfo.Type
	Status TypeQueryStatus
}

type syntaxResolver struct {
	ctx    *CompilerContext
	module *Module
	query  *TypeQueryResult
	chain  []typeInstantiationFrame
}

func ResolveType(ctx *CompilerContext, module *Module, node ast.TypeExpr, context TypeContext) typeinfo.Type {
	return resolveType(ctx, module, node, context, nil)
}

// QueryType resolves syntax without publishing source evidence or creating
// generic instances. Loading means valid semantic evidence may exist later;
// Invalid means the syntax resolved to an invalid semantic type.
func QueryType(ctx *CompilerContext, module *Module, node ast.TypeExpr, context TypeContext) TypeQueryResult {
	result := TypeQueryResult{Status: TypeQueryAvailable}
	resolver := syntaxResolver{ctx: ctx, module: module, query: &result}
	result.Type = typeinfo.TypeFromSyntax(node, resolver.context(context, syntaxTarget(ctx), nil))
	if typeinfo.ContainsInvalid(result.Type) {
		result.Status = TypeQueryInvalid
	}
	if result.Status == TypeQueryLoading {
		result.Type = nil
	}
	return result
}

func ResolveFunctionType(ctx *CompilerContext, module *Module, fn *ast.FnDecl, context TypeContext) *typeinfo.FuncType {
	if fn == nil {
		return nil
	}
	issues := make([]typeinfo.SyntaxIssue, 0, 1)
	resolver := syntaxResolver{ctx: ctx, module: module}
	compilerTarget := syntaxTarget(ctx)
	fnType := typeinfo.FuncTypeFromDecl(fn, resolver.context(context, compilerTarget, &issues))
	reportSyntaxIssues(ctx, compilerTarget, issues)
	return fnType
}

func resolveType(ctx *CompilerContext, module *Module, node ast.TypeExpr, context TypeContext, chain []typeInstantiationFrame) typeinfo.Type {
	resolver := syntaxResolver{ctx: ctx, module: module, chain: chain}
	issues := make([]typeinfo.SyntaxIssue, 0, 1)
	typ := typeinfo.TypeFromSyntax(node, resolver.context(context, syntaxTarget(ctx), &issues))
	reportSyntaxIssues(ctx, syntaxTarget(ctx), issues)
	return typ
}

func syntaxTarget(ctx *CompilerContext) target.Info {
	if ctx != nil && ctx.Target.Valid() {
		return ctx.Target
	}
	return target.Host()
}

func reportSyntaxIssues(ctx *CompilerContext, compilerTarget target.Info, issues []typeinfo.SyntaxIssue) {
	if ctx == nil || ctx.Diagnostics == nil {
		return
	}
	for _, issue := range issues {
		switch issue.Kind {
		case typeinfo.SyntaxInvalidSelf:
			ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"`Self` can only be used as an iface method receiver", ast.LocOf(issue.Node), "")
		case typeinfo.SyntaxInvalidArrayLength:
			ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				fmt.Sprintf("array length must be an integer literal that fits its explicit type and target usize (u%d)", compilerTarget.IndexBits),
				ast.LocOf(issue.Node), "invalid array length")
		case typeinfo.SyntaxInvalidApplication:
			word := "arguments"
			if issue.Want == 1 {
				word = "argument"
			}
			ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				fmt.Sprintf("type `%s` expects %d type %s, got %d", issue.Name, issue.Want, word, issue.Got),
				ast.LocOf(issue.Node), "use exact explicit type arguments")
		case typeinfo.SyntaxAnonymousInterface:
			ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"anonymous interface types are not supported yet", ast.LocOf(issue.Node),
				"declare a named interface and use &Name, &mut Name, or *Name")
		}
	}
}

func (r syntaxResolver) context(context TypeContext, compilerTarget target.Info, issues *[]typeinfo.SyntaxIssue) typeinfo.SyntaxContext {
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
	if node == nil || r.module == nil {
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
	resolved, ok := LookupImportedSymbol(r.ctx, r.module, qualifier.Name, member.Name)
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
		instance := r.ctx.lookupTypeInstance(base, arguments)
		// Invalid dominates loading when nested applications report both states.
		switch instance.Status {
		case TypeQueryInvalid:
			r.query.Status = TypeQueryInvalid
		case TypeQueryLoading:
			if r.query.Status == TypeQueryAvailable {
				r.query.Status = TypeQueryLoading
			}
		}
		if instance.Type != nil {
			return instance.Type
		}
		if instance.Status == TypeQueryLoading {
			return &typeinfo.UnknownType{}
		}
		return &typeinfo.InvalidType{}
	}
	if r.ctx == nil {
		return &typeinfo.InvalidType{}
	}
	return r.ctx.instantiateType(base, arguments, node, r.chain)
}

func (r syntaxResolver) record(sym *symbols.Symbol) {
	if r.query == nil && sym != nil {
		sym.MarkUsed()
	}
}
