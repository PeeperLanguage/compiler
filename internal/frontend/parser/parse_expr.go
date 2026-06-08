package parser

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
	"fmt"
)

const (
	precLowest uint8 = iota
	precOr
	precAnd
	precEquality
	precCompare
	precSum
	precProduct
	precCast
	precPrefix
	precCall
)

// nudFunc parses a prefix (null-denotation) expression.
type nudFunc func(p *Parser) ast.Expr

// ledFunc parses an infix (left-denotation) expression.
type ledFunc func(p *Parser, left ast.Expr, prec uint8) ast.Expr

var (
	nudLookup = map[token.Kind]nudFunc{}
	ledLookup = map[token.Kind]ledFunc{}
	precTable = map[token.Kind]uint8{}
)

func nud(kind token.Kind, handler nudFunc) {
	nudLookup[kind] = handler
}

func led(kind token.Kind, prec uint8, handler ledFunc) {
	precTable[kind] = prec
	ledLookup[kind] = handler
}

func init() {
	// literals & identifiers
	nud(token.NUMBER, func(p *Parser) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.NumberLit{Value: tok.Literal, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.STRING, func(p *Parser) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.StringLit{Value: tok.Literal, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.IDENT, func(p *Parser) ast.Expr { return p.parseIdentExpr() })

	// grouping
	nud(token.LPAREN, func(p *Parser) ast.Expr {
		p.advance()
		inner := p.parseExpr(precLowest)
		if p.consume(token.RPAREN, "expected ')'") == nil {
			if p.isStmtBoundary(p.peek().Kind) || p.peek().Kind == token.COMMA || p.peek().Kind == token.RPAREN {
				p.recoverMissingToken(token.RPAREN, "expected ')'", p.expectedInsertionPoint())
				return inner
			}
			return nil
		}
		return inner
	})

	// prefix / unary
	nud(token.PLUS, func(p *Parser) ast.Expr { return p.parseUnaryExpr() })
	nud(token.MINUS, func(p *Parser) ast.Expr { return p.parseUnaryExpr() })
	nud(token.BANG, func(p *Parser) ast.Expr { return p.parseUnaryExpr() })

	// struct literal
	nud(token.DOT, func(p *Parser) ast.Expr { return p.parseStructLiteral() })

	// logical
	led(token.OROR, precOr, parseBinaryExpr)
	led(token.ANDAND, precAnd, parseBinaryExpr)

	// equality
	led(token.EQ, precEquality, parseBinaryExpr)
	led(token.NEQ, precEquality, parseBinaryExpr)

	// relational
	led(token.LT, precCompare, parseBinaryExpr)
	led(token.GT, precCompare, parseBinaryExpr)
	led(token.LE, precCompare, parseBinaryExpr)
	led(token.GE, precCompare, parseBinaryExpr)

	// additive
	led(token.PLUS, precSum, parseBinaryExpr)
	led(token.MINUS, precSum, parseBinaryExpr)

	// multiplicative
	led(token.ASTERISK, precProduct, parseBinaryExpr)
	led(token.SLASH, precProduct, parseBinaryExpr)
	led(token.PERCENT, precProduct, parseBinaryExpr)

	// cast
	led(token.AS, precCast, func(p *Parser, left ast.Expr, _ uint8) ast.Expr {
		return p.parseAsExpr(left)
	})

	// call & member
	led(token.LPAREN, precCall, func(p *Parser, left ast.Expr, _ uint8) ast.Expr {
		return p.parseCall(left)
	})
	led(token.DOT, precCall, func(p *Parser, left ast.Expr, _ uint8) ast.Expr {
		return p.parseSelector(left)
	})
}

func (p *Parser) parseExpr(precedence uint8) ast.Expr {
	nudHandler, ok := nudLookup[p.peek().Kind]
	if !ok {
		p.diag.Add(diagnostics.NewError("expected expression").WithCode(diagnostics.ErrInvalidExpression).WithPrimaryLabel(source.NewLocation(p.filePath, p.peek().Start, p.peek().End), fmt.Sprintf("found %s", p.peek().Kind)))
		return nil
	}
	left := nudHandler(p)
	if left == nil {
		return nil
	}
	for !p.at(token.SEMICOLON) && !p.at(token.COMMA) && !p.at(token.RPAREN) && !p.at(token.RBRACE) {
		prec, ok := precTable[p.peek().Kind]
		if !ok || prec <= precedence {
			break
		}
		left = ledLookup[p.peek().Kind](p, left, prec)
		if left == nil {
			return nil
		}
	}
	return left
}

func (p *Parser) parseUnaryExpr() ast.Expr {
	tok := p.advance()
	expr := p.parseExpr(precPrefix)
	if expr == nil {
		return nil
	}
	return reg(p, &ast.UnaryExpr{
		Op:       tok.Literal,
		Expr:     expr,
		Location: source.NewLocation(p.filePath, tok.Start, tok.End),
	})
}

func parseBinaryExpr(p *Parser, left ast.Expr, prec uint8) ast.Expr {
	op := p.advance()
	if op == nil {
		return nil
	}
	right := p.parseExpr(prec)
	if right == nil {
		return nil
	}
	return reg(p, &ast.BinaryExpr{
		Left:     left,
		Op:       op.Literal,
		Right:    right,
		Location: source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(right)),
	})
}

func (p *Parser) parseCall(callee ast.Expr) ast.Expr {
	start := p.consume(token.LPAREN, "expected '('")
	if start == nil {
		return nil
	}
	var args []ast.Expr
	if !p.at(token.RPAREN) {
		for {
			arg := p.parseExpr(precLowest)
			if arg != nil {
				args = append(args, arg)
			}
			if !p.match(token.COMMA) {
				break
			}
		}
	}
	end := p.consume(token.RPAREN, "expected ')' after arguments")
	if end == nil {
		if !p.isStmtBoundary(p.peek().Kind) && p.peek().Kind != token.COMMA && p.peek().Kind != token.RPAREN {
			return nil
		}
		end = p.recoverMissingToken(token.RPAREN, "expected ')' after arguments", p.expectedInsertionPoint())
	}
	return reg(p, &ast.CallExpr{
		Callee:   callee,
		Args:     args,
		Location: source.NewLocation(p.filePath, ast.StartOf(callee), ast.EndOf(reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, end.Start, end.End)}))),
	})
}

func (p *Parser) parseIdentExpr() ast.Expr {
	id := p.parseIdent()
	if id == nil {
		return nil
	}
	if p.match(token.DCOLON) {
		member := p.parseIdent()
		if member == nil {
			return nil
		}
		return reg(p, &ast.ScopeResolution{
			Module:   id,
			Name:     member,
			Location: source.NewLocation(p.filePath, ast.StartOf(id), ast.EndOf(member)),
		})
	}
	return id
}

func (p *Parser) parseSelector(left ast.Expr) ast.Expr {
	dot := p.consume(token.DOT, "expected '.'")
	if dot == nil {
		return left
	}
	name := p.parseIdent()
	if name == nil {
		return nil
	}
	return reg(p, &ast.SelectorExpr{
		Expr:     left,
		Name:     name,
		Location: source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(name)),
	})
}

func (p *Parser) parseStructLiteral() ast.Expr {
	start := p.consume(token.DOT, "expected '.'")
	if start == nil {
		return nil
	}
	fields, end, ok := parseBracedItemList(p, "expected '{' after '.'", "expected '}' after struct literal",
		func() (ast.StructLitField, bool) {
			name := p.parseIdent()
			if name == nil {
				return ast.StructLitField{}, false
			}
			if p.consume(token.ASSIGN, "expected '=' after struct literal field name") == nil {
				return ast.StructLitField{}, false
			}
			value := p.parseExpr(precLowest)
			if value == nil {
				return ast.StructLitField{}, false
			}
			return ast.StructLitField{
				Name:     name,
				Value:    value,
				Location: source.NewLocation(p.filePath, ast.StartOf(name), ast.EndOf(value)),
			}, true
		})
	if !ok {
		return nil
	}
	return reg(p, &ast.StructLit{
		Fields:   fields,
		Location: source.NewLocation(p.filePath, start.Start, end.End),
	})
}

func (p *Parser) parseIdent() *ast.Ident {
	tok := p.peek()
	if tok.Kind != token.IDENT {
		p.diag.Add(diagnostics.NewError("expected identifier").WithCode(diagnostics.ErrMissingIdentifier).WithPrimaryLabel(source.NewLocation(p.filePath, tok.Start, tok.End), fmt.Sprintf("found %s", tok.Kind)))
		return nil
	}
	p.advance()
	return reg(p, &ast.Ident{Name: tok.Literal, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
}

func (p *Parser) parseAsExpr(left ast.Expr) ast.Expr {
	asTok := p.advance() // consume 'as'
	typeExpr := p.parseTypeExpr()
	if typeExpr == nil {
		p.diag.Add(diagnostics.NewError("expected type after 'as'").WithCode(diagnostics.ErrInvalidExpression).WithPrimaryLabel(source.NewLocation(p.filePath, asTok.Start, asTok.End), fmt.Sprintf("found %s", asTok.Kind)))
		return left
	}
	return reg(p, &ast.AsExpr{
		Expr:     left,
		TypeExpr: typeExpr,
		Location: source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(typeExpr)),
	})
}
