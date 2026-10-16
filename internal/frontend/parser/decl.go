package parser

import (
	"compiler/internal/diag"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

func (p *parser) module() {
	var importBuf, funcBuf [16]ast.Index
	imports, funcs := importBuf[:0], funcBuf[:0]
	for !p.at(token.EOF) {
		if p.at(token.IMPORT) || p.at(token.FN) {
			p.recovering = false
		}
		switch {
		case p.at(token.IMPORT):
			if imp := p.importDecl(); imp != ast.None {
				imports = append(imports, imp)
			}
		case p.at(token.FN):
			funcs = append(funcs, p.funcDecl())
		default:
			// One error for the whole stretch, then go on at the next declaration.
			p.fail(diag.New(diag.InvalidDeclaration, "module scope expects declaration").
				Label(p.t.File, p.tok().Span, "expected a declaration here"))
			for !p.at(token.EOF) && !p.at(token.FN) && !p.at(token.IMPORT) {
				p.next()
			}
		}
	}
	p.t.Root = p.add(ast.KindModule, 0, p.list(imports), p.list(funcs))
}

func (p *parser) importDecl() ast.Index {
	start := p.next().Start
	path, ok := p.expect(token.STRING)
	if !ok {
		p.sync()
		return ast.None
	}
	pathNode := p.add(ast.KindStringLiteral, path.Start, ast.None, ast.None)
	alias := ast.None
	if p.at(token.AS) {
		p.next()
		alias = p.name(ast.KindDeclaredName)
	}
	p.end()
	return p.add(ast.KindImportDecl, start, pathNode, alias)
}

// name reads a name into a node of kind; a missing one gives an empty span.
func (p *parser) name(kind ast.Kind) ast.Index {
	if t, ok := p.expect(token.IDENT); ok {
		return p.add(kind, t.Start, ast.None, ast.None)
	}
	return p.add(kind, p.lastEnd, ast.None, ast.None)
}

// funcDecl reads `fn (receiver) name<T>(parameters) -> Result from origins`
// followed by a body, or by `;` for a function defined elsewhere.
func (p *parser) funcDecl() ast.Index {
	start := p.next().Start
	receiver := ast.None
	if p.at(token.LPAREN) {
		open := p.next()
		receiver = p.parameter()
		p.close(token.RPAREN, open)
	}
	name := p.name(ast.KindDeclaredName)
	var typeBuf [4]ast.Index
	typeParams := typeBuf[:0]
	if p.at(token.LT) {
		open := p.next()
		typeParams = p.items(typeParams, token.GT, isName, func() ast.Index { return p.name(ast.KindDeclaredName) })
		p.closeAngle(open)
	}
	var buf [8]ast.Index
	params := buf[:0]
	if open, ok := p.expect(token.LPAREN); ok {
		params = p.items(params, token.RPAREN, startsParameter, p.parameter)
		p.close(token.RPAREN, open)
	}
	typeParamList, paramList := p.list(typeParams), p.list(params)
	result, origins := p.result()
	body := ast.None
	switch {
	case p.at(token.SEMICOLON):
		p.end()
	case p.at(token.LBRACE):
		body = p.block()
	default:
		p.fail(diag.New(diag.ExpectedToken, "missing function body").
			Label(p.t.File, source.Span{Start: p.lastEnd, End: p.lastEnd}, "expected `{` or `;` here"))
	}
	return p.add(ast.KindFunctionDecl, start, name, p.extra(receiver, typeParamList, paramList, result, origins, body))
}

func isName(kind token.Kind) bool { return kind == token.IDENT }

func startsParameter(kind token.Kind) bool { return kind == token.MUT || startsType(kind) }

// parameter reads a function's parameter: `mut name: Type = default`, where
// `mut`, the name and the default may be left out.
func (p *parser) parameter() ast.Index {
	start := p.tok().Start
	var op token.Kind
	var mut token.Token
	if p.at(token.MUT) {
		mut = p.next()
		op = token.MUT
		for p.at(token.MUT) {
			extra := p.next()
			removed := source.Span{Start: extra.Start, End: extra.End}
			if p.t.File.Line(extra.End) == p.t.File.Line(p.tok().Start) {
				removed.End = p.tok().Start // and the space after it
			}
			p.repair(diag.New(diag.InvalidDeclaration, "`mut` is repeated").Label(p.t.File, extra.Span, "").
				Help("remove the extra `mut`", diag.Fix{File: p.t.File, Span: removed}))
		}
	}
	name := p.parameterName()
	typ := p.typeExpr()
	if op == token.MUT && name == ast.None {
		p.repair(diag.New(diag.InvalidDeclaration, "mutable parameter requires a named binding").
			Label(p.t.File, source.Span{Start: mut.Start, End: p.lastEnd}, "add a parameter name after the modifier"))
	}
	value := ast.None
	if p.at(token.ASSIGN) {
		p.next()
		value = p.expr(precLowest)
	}
	i := p.add(ast.KindParameter, start, name, p.extra(typ, value))
	p.t.Nodes[i].Op = op
	return i
}

// parameterName reads the `name:` in front of a parameter's type, if it is there.
func (p *parser) parameterName() ast.Index {
	if !p.at(token.IDENT) || p.peek().Kind != token.COLON {
		return ast.None
	}
	name := p.name(ast.KindDeclaredName)
	p.next()
	return name
}

// result reads what follows a parameter list: `-> Type` and the origins of a
// returned reference, `from name` or `from (a, b)`. Both may be left out.
func (p *parser) result() (result, origins ast.Index) {
	result = ast.None
	switch kind := p.tok().Kind; {
	case kind == token.ARROW:
		p.next()
		result = p.typeExpr()
	case kind != token.FN && startsType(kind):
		// A type right after the parameters can only be the result; `fn` there
		// starts the next declaration.
		at := source.Span{Start: p.lastEnd, End: p.lastEnd}
		p.repair(diag.New(diag.ExpectedToken, "expected `->` before the return type").Label(p.t.File, at, "").
			Help("add missing `->`", diag.Fix{File: p.t.File, Span: at, NewText: " ->"}))
		result = p.typeExpr()
	}
	var buf [2]ast.Index
	names := buf[:0]
	if p.at(token.FROM) {
		p.next()
		reference := func() ast.Index { return p.name(ast.KindNameReference) }
		if p.at(token.LPAREN) {
			open := p.next()
			names = p.items(names, token.RPAREN, isName, reference)
			p.close(token.RPAREN, open)
		} else {
			names = append(names, reference())
		}
	}
	return result, p.list(names)
}
