package parser

import (
	"compiler/internal/diag"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

// startsType reports whether a type can begin with kind.
func startsType(kind token.Kind) bool {
	switch kind {
	case token.IDENT, token.AMP, token.QUESTION, token.QQ, token.ASTERISK, token.RAWPTR, token.LBRACK, token.FN,
		token.STRUCT, token.ENUM, token.IFACE:
		return true
	}
	return false
}

// typeExpr reads a type.
func (p *parser) typeExpr() ast.Index {
	t := p.tok()
	switch t.Kind {
	case token.IDENT:
		return p.namedType()
	case token.AMP:
		p.next()
		var op token.Kind
		if p.at(token.MUT) {
			op = p.next().Kind
		}
		i := p.add(ast.KindReferenceType, t.Start, p.typeExpr(), ast.None)
		p.t.Nodes[i].Op = op
		return i
	case token.QUESTION, token.QQ:
		return p.optionalType()
	case token.ASTERISK:
		p.next()
		return p.add(ast.KindOwnedPointerType, t.Start, p.typeExpr(), ast.None)
	case token.RAWPTR:
		p.next()
		return p.add(ast.KindRawPointerType, t.Start, ast.None, ast.None)
	case token.LBRACK:
		return p.arrayType()
	case token.FN:
		return p.functionType()
	case token.STRUCT:
		p.next()
		return p.structType(t.Start)
	case token.ENUM:
		p.next()
		return p.enumType(t.Start)
	case token.IFACE:
		p.next()
		return p.interfaceType(t.Start)
	}
	p.fail(diag.New(diag.InvalidType, "expected type, found `"+t.Kind.String()+"`").Label(p.t.File, t.Span, "expected a type here"))
	return p.add(ast.KindInvalidType, p.lastEnd, ast.None, ast.None)
}

// namedType reads a type name with its type arguments, if any (`Box<T>`), or a
// path of them (`a::B<T>`).
func (p *parser) namedType() ast.Index {
	start := p.tok().Start
	first := p.typeSegment()
	if !p.at(token.DCOLON) {
		return first
	}
	var buf [4]ast.Index
	segments := append(buf[:0], first)
	for p.at(token.DCOLON) {
		p.next()
		segments = append(segments, p.typeSegment())
	}
	return p.add(ast.KindPath, start, p.list(segments), ast.None)
}

func (p *parser) typeSegment() ast.Index {
	start := p.tok().Start
	name := p.name(ast.KindTypeName)
	if !p.at(token.LT) {
		return name
	}
	open := p.next()
	var buf [4]ast.Index
	args := p.items(buf[:0], token.GT, startsType, p.typeExpr)
	p.closeAngle(open)
	return p.add(ast.KindGenericType, start, name, p.list(args))
}

// closeAngle reads the `>` closing type arguments. The lexer reads `>>` as one
// token; in `Box<Box<T>>` it closes two lists, so its first `>` is read here
// and the token, owned by the parser, is cut down to the second.
func (p *parser) closeAngle(open token.Token) {
	if t := p.tok(); t.Kind == token.SHR {
		p.lastEnd = t.Start + 1
		p.tokens[p.current()] = token.Token{Kind: token.GT, Span: source.Span{Start: t.Start + 1, End: t.End}}
		return
	}
	p.close(token.GT, open)
}

// optionalType reads `?T`. A second marker changes nothing (`??T` is `?T`), so
// each extra one is reported as redundant and still kept in the tree.
func (p *parser) optionalType() ast.Index {
	var buf [2]int
	markers := buf[:0] // where each `?` starts; `??` is one token holding two
	for p.at(token.QUESTION) || p.at(token.QQ) {
		t := p.next()
		markers = append(markers, t.Start)
		if t.Kind == token.QQ {
			markers = append(markers, t.Start+1)
		}
	}
	for _, at := range markers[1:] {
		marker := source.Span{Start: at, End: at + 1}
		p.repair(diag.New(diag.RedundantOptional, "redundant optional marker").Label(p.t.File, marker, "").
			Help("remove redundant `?`", diag.Fix{File: p.t.File, Span: marker}).
			Note("nested optional types are the same as a single optional type"))
	}
	inner := p.typeExpr()
	for i := len(markers) - 1; i >= 0; i-- {
		inner = p.add(ast.KindOptionalType, markers[i], inner, ast.None)
	}
	return inner
}

// arrayType reads `[N]T`, `[]T` or `[..]T`.
func (p *parser) arrayType() ast.Index {
	open := p.next()
	switch t := p.tok(); t.Kind {
	case token.RBRACK:
		p.next()
		return p.add(ast.KindDynamicArrayType, open.Start, p.typeExpr(), ast.None)
	case token.DOTDOT:
		p.next()
		p.close(token.RBRACK, open)
		return p.add(ast.KindSliceType, open.Start, p.typeExpr(), ast.None)
	case token.NUMBER:
		p.next()
		p.checkNumber(t)
		length := p.add(ast.KindNumberLiteral, t.Start, ast.None, ast.None)
		p.close(token.RBRACK, open)
		return p.add(ast.KindFixedArrayType, open.Start, length, p.typeExpr())
	default:
		p.fail(diag.New(diag.InvalidType, "expected array length").
			Label(p.t.File, t.Span, "use `[N]T` for fixed arrays, `[]T` for dynamic arrays, or `[..]T` for slices"))
		return p.add(ast.KindInvalidType, open.Start, ast.None, ast.None)
	}
}

// functionType reads `fn(name: T, U) -> R from name`; parameter names are
// optional.
func (p *parser) functionType() ast.Index {
	start := p.next().Start
	var buf [4]ast.Index
	params := buf[:0]
	if open, ok := p.expect(token.LPAREN); ok {
		params = p.items(params, token.RPAREN, startsType, func() ast.Index {
			paramStart := p.tok().Start
			name := p.parameterName()
			return p.add(ast.KindParameter, paramStart, name, p.extra(p.typeExpr(), ast.None))
		})
		p.close(token.RPAREN, open)
	}
	paramList := p.list(params)
	result, origins := p.result()
	return p.add(ast.KindFunctionType, start, paramList, p.extra(result, origins))
}

// structType reads `{ name: Type, ... }`, the fields of a struct type that
// starts at start.
func (p *parser) structType(start int) ast.Index {
	return p.members(start, ast.KindStructType, isName, func() ast.Index {
		fieldStart := p.tok().Start
		name := p.name(ast.KindDeclaredName)
		p.expect(token.COLON)
		return p.add(ast.KindField, fieldStart, name, p.typeExpr())
	})
}

// enumType reads `{ Name, Name: Payload, ... }`, the variants of an enum type
// that starts at start.
func (p *parser) enumType(start int) ast.Index {
	return p.members(start, ast.KindEnumType, isName, func() ast.Index {
		variantStart := p.tok().Start
		name := p.name(ast.KindDeclaredName)
		payload := ast.None
		if p.at(token.COLON) {
			p.next()
			payload = p.typeExpr()
		}
		return p.add(ast.KindVariant, variantStart, name, payload)
	})
}

// interfaceType reads `{ fn (receiver) name(parameters) -> Result, ... }`, the
// methods of an interface type that starts at start: function signatures
// without bodies. A method needs a receiver and takes no default values.
func (p *parser) interfaceType(start int) ast.Index {
	return p.members(start, ast.KindInterfaceType, func(kind token.Kind) bool { return kind == token.FN }, func() ast.Index {
		methodStart := p.next().Start
		if !p.at(token.LPAREN) {
			p.expect(token.LPAREN)
		}
		f := p.signature()
		noDefault := func(param ast.Index) {
			if value := p.t.Parameter(param).Default; value != ast.None {
				p.repair(diag.New(diag.InvalidDeclaration, "interface method defaults are not supported").
					Label(p.t.File, p.t.Nodes[value].Span, ""))
			}
		}
		if f.Receiver != ast.None {
			noDefault(f.Receiver)
		}
		for _, param := range p.t.List(f.Parameters) {
			noDefault(param)
		}
		return p.function(methodStart, f)
	})
}

// members reads the `{ ... }` list of a struct, enum or interface type that
// starts at start, and makes its node of kind.
func (p *parser) members(start int, kind ast.Kind, startsMember func(token.Kind) bool, member func() ast.Index) ast.Index {
	var buf [8]ast.Index
	items := buf[:0]
	if open, ok := p.expect(token.LBRACE); ok {
		items = p.items(items, token.RBRACE, startsMember, member)
		p.close(token.RBRACE, open)
	}
	return p.add(kind, start, p.list(items), ast.None)
}
