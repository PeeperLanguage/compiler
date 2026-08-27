// Recursive-descent parser for type expressions.
//
// Type tokens select dedicated routines for references, pointers, arrays,
// functions, structs, interfaces, enums, and named types. Recursive calls
// consume nested type syntax directly, while the shared Parser preserves
// source locations, diagnostics, recovery, and AST registration.

package parser

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
	"fmt"
)

func (p *Parser) parseTypeExpr() ast.TypeExpr {
	tok := p.current()
	switch tok.Kind {
	case token.AMP:
		return p.parseRefTypeExpr()
	case token.QUESTION:
		return p.parseOptionalTypeExpr()
	case token.ASTERISK:
		return p.parseOwnedPtrTypeExpr()
	case token.RAWPTR:
		return p.parseRawPtrTypeExpr()
	case token.LBRACK:
		return p.parseBracketTypeExpr()
	case token.FN:
		return p.parseFuncTypeExpr()
	case token.STRUCT:
		return p.parseStructTypeExpr()
	case token.IFACE:
		return p.parseInterfaceTypeExpr()
	case token.ENUM:
		return p.parseEnumTypeExpr()
	case token.IDENT:
		first, ok := p.parsePathSegment()
		if !ok {
			return nil
		}
		if p.at(token.DCOLON) {
			path := p.parseScopeResolution(first)
			if path == nil {
				return nil
			}
			return path
		}
		if len(first.TypeArgs) > 0 {
			return reg(p, &ast.AppliedType{Name: first.Name, TypeArgs: first.TypeArgs, Location: first.Location})
		}
		return reg(p, &ast.NamedType{Name: first.Name.Name, Location: first.Location})
	default:
		loc := source.NewLocation(p.filePath, tok.Start, tok.End)
		d := diagnostics.NewError("expected type").
			WithCode(diagnostics.ErrInvalidTypeInParser).
			WithPrimaryLabel(loc, fmt.Sprintf("found %s", tok.Kind))
		p.diag.Add(d)
		return nil
	}
}

func (p *Parser) parsePathSegment() (ast.PathSegment, bool) {
	name := p.parseIdent()
	if name == nil {
		return ast.PathSegment{}, false
	}
	segment := ast.PathSegment{Name: name, Location: name.Location}
	if p.at(token.LT) {
		args, close, ok := p.parseTypeArguments()
		if !ok {
			return ast.PathSegment{}, false
		}
		segment.TypeArgs = args
		segment.Location = source.NewLocation(p.filePath, ast.StartOf(name), close.End)
	}
	return segment, true
}

func (p *Parser) parseScopeResolution(first ast.PathSegment) *ast.ScopeResolution {
	segments := []ast.PathSegment{first}
	for p.match(token.DCOLON) {
		segment, ok := p.parsePathSegment()
		if !ok {
			return nil
		}
		segments = append(segments, segment)
	}
	return reg(p, &ast.ScopeResolution{
		Segments: segments,
		Location: source.NewLocation(p.filePath, ast.StartOf(first.Name), *segments[len(segments)-1].Location.End),
	})
}

func (p *Parser) parseTypeArguments() ([]ast.TypeExpr, *token.Token, bool) {
	open := p.consume(token.LT, "expected '<' before type arguments")
	if open == nil {
		return nil, nil, false
	}
	args := make([]ast.TypeExpr, 0, 1)
	for {
		arg := p.parseTypeExpr()
		if arg == nil {
			return nil, nil, false
		}
		args = append(args, arg)
		if !p.match(token.COMMA) {
			break
		}
	}
	close := p.consumeTypeArgumentClose(open.Start)
	return args, close, close != nil
}

func (p *Parser) consumeTypeArgumentClose(open source.Position) *token.Token {
	if p.at(token.GT) {
		return p.advance()
	}
	if p.at(token.SHR) {
		combined := p.current()
		middle := combined.Start
		middle.Advance(">")
		first := token.Token{Kind: token.GT, Literal: ">", Start: combined.Start, End: middle}
		p.stream[p.pos] = token.Token{Kind: token.GT, Literal: ">", Start: middle, End: combined.End}
		return &first
	}
	return p.expectClose(open, token.GT, "<")
}

func (p *Parser) parseRefTypeExpr() ast.TypeExpr {
	start := p.consume(token.AMP, "expected '&' in reference type")
	if start == nil {
		return nil
	}
	mutable := p.match(token.MUT)
	target := p.parseTypeExpr()
	if target == nil {
		return nil
	}
	return reg(p, &ast.RefType{
		Mutable:  mutable,
		Target:   target,
		Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(target)),
	})
}

func (p *Parser) parseOptionalTypeExpr() ast.TypeExpr {
	start := p.consume(token.QUESTION, "expected '?' in optional type")
	if start == nil {
		return nil
	}
	inner := p.parseTypeExpr()
	if inner == nil {
		return nil
	}
	return reg(p, &ast.OptionalType{
		Inner:    inner,
		Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(inner)),
	})
}

func (p *Parser) parseOwnedPtrTypeExpr() ast.TypeExpr {
	start := p.consume(token.ASTERISK, "expected '*' in owned pointer type")
	if start == nil {
		return nil
	}
	target := p.parseTypeExpr()
	if target == nil {
		return nil
	}
	return reg(p, &ast.OwnedPtrType{
		Target:   target,
		Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(target)),
	})
}

func (p *Parser) parseRawPtrTypeExpr() ast.TypeExpr {
	tok := p.consume(token.RAWPTR, "expected 'rawptr' in raw pointer type")
	if tok == nil {
		return nil
	}
	return reg(p, &ast.RawPtrType{
		Location: source.NewLocation(p.filePath, tok.Start, tok.End),
	})
}

func (p *Parser) parseBracketTypeExpr() ast.TypeExpr {
	start := p.consume(token.LBRACK, "expected '[' in array type")
	if start == nil {
		return nil
	}
	if p.match(token.RBRACK) {
		elem := p.parseTypeExpr()
		if elem == nil {
			return nil
		}
		return reg(p, &ast.ArrayType{
			Shape:    ast.ArrayOwner,
			Elem:     elem,
			Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(elem)),
		})
	}
	if p.match(token.DOTDOT) {
		if p.consume(token.RBRACK, "expected ']' after '..' in slice type") == nil {
			return nil
		}
		elem := p.parseTypeExpr()
		if elem == nil {
			return nil
		}
		return reg(p, &ast.ArrayType{
			Shape:    ast.ArraySlice,
			Elem:     elem,
			Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(elem)),
		})
	}
	if p.current().Kind != token.NUMBER {
		tok := p.current()
		p.diag.Add(diagnostics.NewError("expected array length").
			WithCode(diagnostics.ErrInvalidTypeInParser).
			WithPrimaryLabel(source.NewLocation(p.filePath, tok.Start, tok.End), "use `[N]T` for fixed arrays, `[]T` for dynamic arrays, or `[..]T` for slices"))
		p.synchronize(token.RBRACK)
		if p.at(token.RBRACK) {
			p.advance()
		}
		return nil
	}
	parsed := p.parseNumberLit("")
	length, ok := parsed.(*ast.NumberLit)
	if !ok {
		return nil
	}
	if p.consume(token.RBRACK, "expected ']' after array length") == nil {
		return nil
	}
	elem := p.parseTypeExpr()
	if elem == nil {
		return nil
	}
	return reg(p, &ast.ArrayType{
		Len:      length,
		Elem:     elem,
		Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(elem)),
	})
}

func (p *Parser) parseFuncTypeExpr() ast.TypeExpr {
	start := p.consume(token.FN, "expected fn in function type")
	if start == nil {
		return nil
	}
	if p.consume(token.LPAREN, "expected '(' after fn in function type") == nil {
		return nil
	}
	lparenPos := p.stream[p.pos-1].Start
	var params []ast.Param
	if !p.at(token.RPAREN) {
		for {
			var name *ast.Ident
			if p.at(token.IDENT) && p.next().Kind == token.COLON {
				name = p.parseIdent()
				p.advance()
			}
			param := p.parseTypeExpr()
			if param == nil {
				return nil
			}
			startPos := ast.StartOf(param)
			if name != nil {
				startPos = ast.StartOf(name)
			}
			params = append(params, ast.Param{Name: name, Type: param, Location: source.NewLocation(p.filePath, startPos, ast.EndOf(param))})
			if !p.match(token.COMMA) {
				break
			}
		}
	}
	p.expectClose(lparenPos, token.RPAREN, "(")
	var ret ast.TypeExpr
	if p.match(token.ARROW) {
		ret = p.parseTypeExpr()
		if ret == nil {
			return nil
		}
	}
	returnOrigins := p.parseReturnOriginClause()
	var endPos source.Position
	if returnOrigins != nil && returnOrigins.Location != nil {
		endPos = *returnOrigins.Location.End
	} else if ret != nil {
		endPos = ast.EndOf(ret)
	} else if len(params) > 0 {
		endPos = ast.EndOf(params[len(params)-1].Type)
	} else {
		endPos = start.End
	}
	return reg(p, &ast.FuncType{Params: params, Return: ret, ReturnOrigins: returnOrigins, Location: source.NewLocation(p.filePath, start.Start, endPos)})
}

func (p *Parser) parseStructTypeExpr() ast.TypeExpr {
	start := p.consume(token.STRUCT, "expected struct")
	if start == nil {
		return nil
	}
	fields, end, _ := p.parseTypeFields("expected '{' after struct", "expected '}' after struct fields")
	return reg(p, &ast.StructType{Fields: fields, Location: source.NewLocation(p.filePath, start.Start, end.End)})
}

func (p *Parser) parseInterfaceTypeExpr() ast.TypeExpr {
	start := p.consume(token.IFACE, "expected iface")
	if start == nil {
		return nil
	}
	methods, end, _ := p.parseInterfaceMethods()
	return reg(p, &ast.InterfaceType{Methods: methods, Location: source.NewLocation(p.filePath, start.Start, end.End)})
}

func (p *Parser) parseEnumTypeExpr() ast.TypeExpr {
	start := p.consume(token.ENUM, "expected enum")
	if start == nil {
		return nil
	}
	variants, end, _ := p.parseEnumVariants()
	return reg(p, &ast.EnumType{Variants: variants, Location: source.NewLocation(p.filePath, start.Start, end.End)})
}

// --- Shared type body parsers ---

func (p *Parser) parseTypeFields(openerMsg, itemMsg string) ([]ast.TypeField, *token.Token, bool) {
	return parseBracedItemList(p, openerMsg, itemMsg,
		func() (ast.TypeField, bool) {
			name := p.parseIdent()
			if name == nil {
				return ast.TypeField{}, false
			}
			if p.consume(token.COLON, "expected ':' after field name") == nil {
				return ast.TypeField{}, false
			}
			ty := p.parseTypeExpr()
			if ty == nil {
				return ast.TypeField{}, false
			}
			return ast.TypeField{Name: name, Type: ty, Location: source.NewLocation(p.filePath, ast.StartOf(name), ast.EndOf(ty))}, true
		})
}

func (p *Parser) parseInterfaceMethods() ([]ast.TypeMethod, *token.Token, bool) {
	return parseBracedItemList(p, "expected '{' after iface", "expected '}' after iface methods",
		func() (ast.TypeMethod, bool) {
			if p.consume(token.FN, "expected 'fn' before interface method") == nil {
				return ast.TypeMethod{}, false
			}
			receiver := p.parseReceiver()
			if receiver == nil {
				return ast.TypeMethod{}, false
			}
			name := p.parseIdent()
			if name == nil {
				return ast.TypeMethod{}, false
			}
			typeParams := p.parseOptionalTypeParams()
			if p.consume(token.LPAREN, "expected '(' after method name") == nil {
				return ast.TypeMethod{}, false
			}
			lparenPos := p.stream[p.pos-1].Start
			params := p.parseParams()
			p.expectClose(lparenPos, token.RPAREN, "(")
			if receiver.Default != nil {
				p.diag.AddError(diagnostics.ErrInvalidDeclaration,
					"interface method defaults are not supported", ast.LocOf(receiver.Default), "")
			}
			for _, param := range params {
				if param.Default == nil {
					continue
				}
				p.diag.AddError(diagnostics.ErrInvalidDeclaration,
					"interface method defaults are not supported", ast.LocOf(param.Default), "")
			}
			var ret ast.TypeExpr
			if p.match(token.ARROW) {
				ret = p.parseTypeExpr()
				// nil is OK - type-checker validates return types
			}
			returnOrigins := p.parseReturnOriginClause()
			endPos := ast.EndOf(ret)
			if returnOrigins != nil && returnOrigins.Location != nil {
				endPos = *returnOrigins.Location.End
			}
			if endPos.IsZero() && len(params) > 0 {
				endPos = ast.EndOf(params[len(params)-1].Type)
			}
			if endPos.IsZero() {
				endPos = ast.EndOf(name)
			}
			return ast.TypeMethod{
				Name:          name,
				Receiver:      receiver,
				TypeParams:    typeParams,
				Params:        params,
				ReturnType:    ret,
				ReturnOrigins: returnOrigins,
				Location:      source.NewLocation(p.filePath, ast.StartOf(name), endPos),
			}, true
		})
}

func (p *Parser) parseEnumVariants() ([]ast.EnumVariant, *token.Token, bool) {
	return parseBracedItemList(p, "expected '{' after enum", "expected '}' after enum variants",
		func() (ast.EnumVariant, bool) {
			name := p.parseIdent()
			if name == nil {
				return ast.EnumVariant{}, false
			}
			if !p.match(token.COLON) {
				return ast.EnumVariant{Name: name, Location: name.Location}, true
			}
			colon := p.prev()
			if p.at(token.LBRACE) {
				fields, end, ok := p.parseTypeFields("expected '{' after enum variant ':'", "expected '}' after enum variant fields")
				if !ok {
					return ast.EnumVariant{}, false
				}
				if len(fields) == 0 {
					p.diag.Add(diagnostics.NewError("variant data requires at least one field").
						WithCode(diagnostics.ErrInvalidTypeInParser).
						WithPrimaryLabel(source.NewLocation(p.filePath, colon.Start, end.End), "remove the data block or add a field"))
				}
				payload := reg(p, &ast.StructType{Fields: fields, Location: source.NewLocation(p.filePath, colon.Start, end.End)})
				return ast.EnumVariant{Name: name, Payload: payload, Location: source.NewLocation(p.filePath, ast.StartOf(name), end.End)}, true
			}
			payload := p.parseTypeExpr()
			if payload == nil {
				return ast.EnumVariant{}, false
			}
			return ast.EnumVariant{Name: name, Payload: payload, Location: source.NewLocation(p.filePath, ast.StartOf(name), ast.EndOf(payload))}, true
		})
}

// --- Params ---

func (p *Parser) parseOptionalTypeParams() []ast.TypeParam {
	if !p.match(token.LT) {
		if p.at(token.LBRACK) {
			start := p.advance()
			for !p.at(token.RBRACK) && !p.at(token.EOF) {
				p.advance()
			}
			p.expectClose(start.Start, token.RBRACK, "[")
			p.diag.Add(diagnostics.NewError("expected '<' to start type parameter list").
				WithCode(diagnostics.ErrInvalidTypeInParser).
				WithPrimaryLabel(source.NewLocation(p.filePath, start.Start, start.End), "type parameter list starts with '<'"))
		}
		return nil
	}
	langlePos := p.stream[p.pos-1].Start
	var params []ast.TypeParam
	for {
		name := p.parseIdent()
		if name == nil {
			break
		}
		params = append(params, ast.TypeParam{Name: name, Location: name.Location})
		if !p.match(token.COMMA) {
			break
		}
	}
	p.expectClose(langlePos, token.GT, "<")
	return params
}

func (p *Parser) parseParams() []ast.Param {
	var params []ast.Param
	if p.at(token.RPAREN) {
		return params
	}
	for {
		param, ok := p.parseParam()
		if !ok {
			break
		}
		params = append(params, param)
		if !p.match(token.COMMA) {
			break
		}
	}
	return params
}

func (p *Parser) parseParam() (ast.Param, bool) {
	var (
		mutableLocation *source.Location
		modifierStart   source.Position
	)
	if p.at(token.MUT) {
		tok := p.advance()
		mutableLocation = source.NewLocation(p.filePath, tok.Start, tok.End)
		modifierStart = tok.Start
	}
	if p.at(token.IDENT) && p.pos+1 < len(p.stream) && p.stream[p.pos+1].Kind == token.COLON {
		name := p.parseIdent()
		if name == nil {
			return ast.Param{}, false
		}
		if p.consume(token.COLON, "expected ':' after parameter name") == nil {
			return ast.Param{}, false
		}
		ty := p.parseTypeExpr()
		// ty may be nil if type parsing failed; continue with name
		endPos := ast.EndOf(name)
		if ty != nil {
			endPos = ast.EndOf(ty)
		}
		var defaultValue ast.Expr
		if p.match(token.ASSIGN) {
			defaultValue = p.parseExpr(precLowest)
			if defaultValue != nil {
				endPos = ast.EndOf(defaultValue)
			}
		}
		startPos := ast.StartOf(name)
		if mutableLocation != nil {
			startPos = modifierStart
		}
		return ast.Param{IsMutable: mutableLocation != nil, MutableLocation: mutableLocation, Name: name, Type: ty, Default: defaultValue, Location: source.NewLocation(p.filePath, startPos, endPos)}, true
	}
	ty := p.parseTypeExpr()
	if ty == nil {
		return ast.Param{}, false
	}
	if mutableLocation != nil {
		p.diag.Add(diagnostics.NewError("mutable parameter requires a named binding").
			WithCode(diagnostics.ErrInvalidDeclaration).
			WithPrimaryLabel(source.NewLocation(p.filePath, modifierStart, p.prev().End), "add a parameter name after the modifier"))
		return ast.Param{}, false
	}
	return ast.Param{Type: ty, Location: ast.LocOf(ty)}, true
}

// --- Helpers ---
