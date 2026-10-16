package parser

import (
	"compiler/internal/diag"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

// declarations holds, by token kind, the reader of each declaration a keyword
// begins; each takes the declaration's metadata node or None. It is filled in
// init: a function's body calls back into the parser, which reads this table.
var declarations [256]func(p *parser, metadata ast.Index) ast.Index

func init() {
	declarations[token.FN] = funcDecl
	declarations[token.CONST] = constDecl
	declarations[token.STRUCT] = typeDecl
	declarations[token.ENUM] = typeDecl
	declarations[token.IFACE] = typeDecl
	declarations[token.TYPE] = typeDecl
}

// startsDeclaration reports whether kind begins a declaration or an import.
func startsDeclaration(kind token.Kind) bool {
	return declarations[kind] != nil || kind == token.IMPORT
}

func (p *parser) module() {
	var importBuf, declBuf [16]ast.Index
	imports, decls := importBuf[:0], declBuf[:0]
	for {
		metadata := p.metadata()
		kind := p.tok().Kind
		if startsDeclaration(kind) {
			p.recovering = false
		}
		switch {
		case kind == token.EOF:
			p.t.Root = p.add(ast.KindModule, 0, p.t.PackModule(ast.Module{Imports: p.list(imports), Declarations: p.list(decls)}))
			return
		case kind == token.IMPORT:
			if imp := p.importDecl(); imp != ast.None {
				imports = append(imports, imp)
			}
		case declarations[kind] != nil:
			decls = append(decls, declarations[kind](p, metadata))
		case kind == token.SEMICOLON:
			p.next()
		case kind == token.LET:
			// Read it to go on in step, but keep no module-level variable.
			let := p.next()
			p.fail(diag.New(diag.InvalidDeclaration, "top-level `let` not allowed").
				Label(p.t.File, let.Span, "").
				Help("use `const` for module-scope values"))
			p.binding()
		default:
			// One error for the whole stretch, then go on at the next declaration.
			p.fail(diag.New(diag.InvalidDeclaration, "module scope expects declaration").
				Label(p.t.File, p.tok().Span, "expected a declaration here").
				Help("move this statement into a function"))
			for !p.at(token.EOF) && !startsDeclaration(p.tok().Kind) {
				p.next()
			}
		}
	}
}

// metadata reads the doc comment and attributes in front of a declaration into
// one node, or returns None when there are none.
func (p *parser) metadata() ast.Index {
	start := p.tok().Start
	doc := p.docComment()
	var buf [2]ast.Index
	attrs := buf[:0]
	for p.at(token.HASH) {
		attrs = append(attrs, p.attribute())
	}
	if doc == ast.None && len(attrs) == 0 {
		return ast.None
	}
	if doc != ast.None {
		start = p.t.Nodes[doc].Span.Start
	}
	return p.add(ast.KindMetadata, start, p.t.PackMetadata(ast.Metadata{DocComment: doc, Attributes: p.list(attrs)}))
}

// docComment reads the `///` lines in front of a declaration into one node, or
// returns None. A `///` on the line of earlier code belongs to that code, not
// to what follows.
func (p *parser) docComment() ast.Index {
	start := -1
	for ; p.tokens[p.pos].Kind == token.DOC_COMMENT; p.pos++ {
		t := p.tokens[p.pos]
		if p.pos > 0 && p.tokens[p.pos-1].Kind != token.DOC_COMMENT &&
			p.t.File.Line(p.tokens[p.pos-1].End) == p.t.File.Line(t.Start) {
			start = -1
			continue
		}
		if start < 0 {
			start = t.Start
		}
		p.lastEnd = t.End
	}
	if start < 0 {
		return ast.None
	}
	return p.leaf(ast.KindDocComment, start)
}

// attribute reads `#[name]` or `#[name(arguments)]`; one attribute per block.
func (p *parser) attribute() ast.Index {
	start := p.next().Start
	open, ok := p.expect(token.LBRACK)
	if !ok {
		name := p.leaf(ast.KindNameReference, p.lastEnd)
		return p.add(ast.KindAttribute, start, p.t.PackAttribute(ast.Attribute{Name: name, Arguments: ast.EmptyList}))
	}
	name := p.name(ast.KindNameReference)
	var buf [4]ast.Index
	args := buf[:0]
	if p.at(token.LPAREN) {
		argsOpen := p.next()
		args = p.items(args, token.RPAREN, startsExpression, func() ast.Index { return p.expr(precLowest) })
		p.close(token.RPAREN, argsOpen)
	}
	if t := p.tok(); t.Kind == token.COMMA {
		p.fail(diag.New(diag.ExpectedToken, "only one attribute is allowed per `#[...]` block").
			Label(p.t.File, t.Span, "split attributes into separate `#[...]` blocks"))
		for !p.at(token.RBRACK) && !p.at(token.EOF) && !startsDeclaration(p.tok().Kind) {
			p.next()
		}
	}
	p.close(token.RBRACK, open)
	return p.add(ast.KindAttribute, start, p.t.PackAttribute(ast.Attribute{Name: name, Arguments: p.list(args)}))
}

func (p *parser) importDecl() ast.Index {
	start := p.next().Start
	path, ok := p.expect(token.STRING)
	if !ok {
		p.sync()
		return ast.None
	}
	pathNode := p.leaf(ast.KindStringLiteral, path.Start)
	alias := ast.None
	if p.at(token.AS) {
		p.next()
		alias = p.name(ast.KindDeclaredName)
	}
	p.end()
	return p.add(ast.KindImportDecl, start, p.t.PackImportDecl(ast.ImportDecl{Path: pathNode, Alias: alias}))
}

// name reads a name into a node of kind; a missing one gives an empty span.
func (p *parser) name(kind ast.Kind) ast.Index {
	if t, ok := p.expect(token.IDENT); ok {
		return p.leaf(kind, t.Start)
	}
	return p.leaf(kind, p.lastEnd)
}

// funcDecl reads `fn (receiver) name<T>(parameters) -> Result from origins`
// followed by a body, or by `;` for a function defined elsewhere.
func funcDecl(p *parser, metadata ast.Index) ast.Index {
	start := p.next().Start
	f := p.signature()
	f.Metadata = metadata
	switch {
	case p.at(token.SEMICOLON):
		p.end()
	case p.at(token.LBRACE):
		f.Body = p.block()
	default:
		p.fail(diag.New(diag.ExpectedToken, "missing function body").
			Label(p.t.File, source.Span{Start: p.lastEnd, End: p.lastEnd}, "expected `{` or `;` here"))
	}
	return p.function(start, f)
}

// signature reads a function's signature after its `fn`; its body and
// metadata are None.
func (p *parser) signature() ast.FunctionDecl {
	f := ast.FunctionDecl{Receiver: ast.None, Result: ast.None, Body: ast.None, Metadata: ast.None}
	if p.at(token.LPAREN) {
		open := p.next()
		f.Receiver = p.parameter()
		p.close(token.RPAREN, open)
	}
	f.Name = p.name(ast.KindDeclaredName)
	f.TypeParameters = p.typeParameters()
	var buf [8]ast.Index
	params := buf[:0]
	if open, ok := p.expect(token.LPAREN); ok {
		params = p.items(params, token.RPAREN, startsParameter, p.parameter)
		p.close(token.RPAREN, open)
	}
	f.Parameters = p.list(params)
	f.Result, f.Origins = p.result()
	return f
}

// function makes the node of function f, which starts at start.
func (p *parser) function(start int, f ast.FunctionDecl) ast.Index {
	return p.add(ast.KindFunctionDecl, start, p.t.PackFunctionDecl(f))
}

// typeParameters reads `<T, U>` and returns its list; it may be left out.
func (p *parser) typeParameters() ast.List {
	if !p.at(token.LT) {
		return ast.EmptyList
	}
	open := p.next()
	var buf [4]ast.Index
	names := p.items(buf[:0], token.GT, isName, func() ast.Index { return p.name(ast.KindDeclaredName) })
	p.closeAngle(open)
	return p.list(names)
}

// constDecl reads `const name: Type = value;`.
func constDecl(p *parser, metadata ast.Index) ast.Index {
	start := p.next().Start
	name, typ, value := p.binding()
	return p.add(ast.KindConstDecl, start, p.t.PackConstDecl(ast.ConstDecl{Metadata: metadata, Name: name, Type: typ, Value: value}))
}

// binding reads `name: Type = value;` after `let` or `const`; the type and the
// value may be left out.
func (p *parser) binding() (name, typ, value ast.Index) {
	name, typ, value = p.name(ast.KindDeclaredName), ast.None, ast.None
	if p.at(token.COLON) {
		p.next()
		typ = p.typeExpr()
	}
	if p.at(token.ASSIGN) {
		p.next()
		value = p.expr(precLowest)
	}
	p.end()
	return name, typ, value
}

// typeDecl reads `struct Name<T> { fields }`, `enum Name<T> { variants }`,
// `iface Name<T> { methods }` or `type Name<T> = Type;`. A `;` after the `}`
// is allowed.
func typeDecl(p *parser, metadata ast.Index) ast.Index {
	keyword := p.next()
	name := p.name(ast.KindDeclaredName)
	typeParams := p.typeParameters()
	var kind ast.Kind
	var typ ast.Index
	switch keyword.Kind {
	case token.STRUCT:
		kind, typ = ast.KindStructDecl, p.structType(p.tok().Start)
	case token.ENUM:
		kind, typ = ast.KindEnumDecl, p.enumType(p.tok().Start)
	case token.IFACE:
		kind, typ = ast.KindInterfaceDecl, p.interfaceType(p.tok().Start)
	default:
		p.expect(token.ASSIGN)
		kind, typ = ast.KindTypeAliasDecl, p.typeExpr()
		p.end()
	}
	if keyword.Kind != token.TYPE && p.at(token.SEMICOLON) {
		p.next()
	}
	return p.add(kind, keyword.Start, p.t.PackTypeDecl(ast.TypeDecl{Metadata: metadata, Name: name, TypeParameters: typeParams, Type: typ}))
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
	i := p.add(ast.KindParameter, start, p.t.PackParameter(ast.Parameter{Name: name, Type: typ, Default: value}))
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
func (p *parser) result() (result ast.Index, origins ast.List) {
	result = ast.None
	switch kind := p.tok().Kind; {
	case kind == token.ARROW:
		p.next()
		result = p.typeExpr()
	case startsType(kind) && !startsDeclaration(kind):
		// A type right after the parameters can only be the result; `fn`,
		// `struct`, `enum` or `iface` there starts the next declaration.
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
