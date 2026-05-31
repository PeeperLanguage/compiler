package parser

import (
	"compiler/core/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
	"strings"
)

const (
	precLowest = iota
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

var infixPrec = map[tokens.Kind]int{
	tokens.OROR:     precOr,
	tokens.ANDAND:   precAnd,
	tokens.EQ:       precEquality,
	tokens.NEQ:      precEquality,
	tokens.LT:       precCompare,
	tokens.GT:       precCompare,
	tokens.LE:       precCompare,
	tokens.GE:       precCompare,
	tokens.PLUS:     precSum,
	tokens.MINUS:    precSum,
	tokens.ASTERISK: precProduct,
	tokens.SLASH:    precProduct,
	tokens.PERCENT:  precProduct,
	tokens.AS:       precCast,
	tokens.LPAREN:   precCall,
}

type ledFunc func(*Parser, ast.Expr, int) ast.Expr

func (p *Parser) parseExpr(precedence int) ast.Expr {
	left := p.parsePrefix()
	if left == nil {
		return nil
	}
	for !p.at(tokens.SEMICOLON) && !p.at(tokens.COMMA) && !p.at(tokens.RPAREN) && !p.at(tokens.RBRACE) {
		nextPrec := p.peekPrecedence()
		if nextPrec <= precedence {
			break
		}
		led, ok := ledFor(p.peek().Kind)
		if !ok {
			p.errorf(p.peek(), diagnostics.ErrInvalidExpression, "expected expression operator")
			return left
		}
		left = led(p, left, nextPrec)
		if left == nil {
			return nil
		}
	}
	return left
}

func ledFor(kind tokens.Kind) (ledFunc, bool) {
	switch kind {
	case tokens.OROR, tokens.ANDAND, tokens.EQ, tokens.NEQ, tokens.LT, tokens.GT, tokens.LE, tokens.GE,
		tokens.PLUS, tokens.MINUS, tokens.ASTERISK, tokens.SLASH, tokens.PERCENT:
		return parseInfixLed, true
	case tokens.AS:
		return parseAsLed, true
	case tokens.LPAREN:
		return parseCallLed, true
	default:
		return nil, false
	}
}

func (p *Parser) parsePrefix() ast.Expr {
	tok := p.peek()
	switch tok.Kind {
	case tokens.IDENT:
		return p.parseIdentExpr()
	case tokens.NUMBER:
		p.advance()
		return &ast.NumberLit{Value: tok.Literal, Location: p.loc(tok, tok)}
	case tokens.STRING:
		p.advance()
		return &ast.StringLit{Value: tok.Literal, Location: p.loc(tok, tok)}
	case tokens.PLUS, tokens.MINUS, tokens.BANG:
		p.advance()
		expr := p.parseExpr(precPrefix)
		if expr == nil {
			return nil
		}
		return &ast.UnaryExpr{
			Op:       tok.Literal,
			Expr:     expr,
			Location: p.loc(tok, tok),
		}
	case tokens.LPAREN:
		p.advance()
		expr := p.parseExpr(precLowest)
		if p.consume(tokens.RPAREN, "expected ')'") == nil {
			if p.isStmtBoundary(p.peek().Kind) || p.peek().Kind == tokens.COMMA || p.peek().Kind == tokens.RPAREN {
				p.recoverMissingToken(tokens.RPAREN, "expected ')'", p.expectedInsertionPoint())
				return expr
			}
			return nil
		}
		return expr
	default:
		p.errorf(tok, diagnostics.ErrInvalidExpression, "expected expression")
		return nil
	}
}

func parseInfixLed(p *Parser, left ast.Expr, precedence int) ast.Expr {
	return p.parseInfix(left, precedence)
}

func parseCallLed(p *Parser, left ast.Expr, _ int) ast.Expr {
	return p.parseCall(left)
}

func parseAsLed(p *Parser, left ast.Expr, _ int) ast.Expr {
	return p.parseAsExpr(left)
}

func (p *Parser) parseInfix(left ast.Expr, precedence int) ast.Expr {
	op := p.advance()
	if op == nil {
		return nil
	}
	right := p.parseExpr(precedence)
	if right == nil {
		return nil
	}
	return &ast.BinaryExpr{
		Left:     left,
		Op:       op.Literal,
		Right:    right,
		Location: p.locFromNode(left, right),
	}
}

func (p *Parser) parseCall(callee ast.Expr) ast.Expr {
	start := p.consume(tokens.LPAREN, "expected '('")
	args := make([]ast.Expr, 0)
	if !p.at(tokens.RPAREN) {
		for {
			arg := p.parseExpr(precLowest)
			if arg != nil {
				args = append(args, arg)
			}
			if !p.match(tokens.COMMA) {
				break
			}
		}
	}
	end := p.consume(tokens.RPAREN, "expected ')' after arguments")
	if start == nil {
		return nil
	}
	if end == nil {
		if p.isStmtBoundary(p.peek().Kind) || p.peek().Kind == tokens.COMMA || p.peek().Kind == tokens.RPAREN {
			end = p.recoverMissingToken(tokens.RPAREN, "expected ')' after arguments", p.expectedInsertionPoint())
		} else {
			return nil
		}
	}
	if end == nil {
		return nil
	}
	return &ast.CallExpr{
		Callee:   callee,
		Args:     args,
		Location: p.locFromNode(callee, &ast.BadExpr{Location: p.loc(*end, *end)}),
	}
}

func (p *Parser) parseIdentExpr() ast.Expr {
	id := p.parseIdent()
	if id == nil {
		return nil
	}
	parts := []string{id.Name}
	end := id.Location
	for p.match(tokens.DCOLON) {
		next := p.parseIdent()
		if next == nil {
			return nil
		}
		parts = append(parts, next.Name)
		end = next.Location
	}
	if len(parts) == 1 {
		return id
	}
	return &ast.Ident{
		Name:     strings.Join(parts, "::"),
		Location: p.locFromNode(id, &ast.BadExpr{Location: end}),
	}
}

func (p *Parser) parseIdent() *ast.Ident {
	tok := p.peek()
	if tok.Kind != tokens.IDENT {
		p.errorf(tok, diagnostics.ErrMissingIdentifier, "expected identifier")
		return nil
	}
	p.advance()
	return &ast.Ident{Name: tok.Literal, Location: p.loc(tok, tok)}
}

// parseAsExpr parses an "as" cast expression: expr as type
func (p *Parser) parseAsExpr(left ast.Expr) ast.Expr {
	if left == nil {
		return nil
	}
	asTok := p.advance()
	if asTok == nil || asTok.Kind != tokens.AS {
		return left
	}
	// Parse the type expression after "as"
	typeExpr := p.parseTypeExpr()
	if typeExpr == nil {
		p.errorf(*asTok, diagnostics.ErrInvalidExpression, "expected type after 'as'")
		return left
	}
	return &ast.AsExpr{
		Expr:     left,
		TypeExpr: typeExpr,
		Location: p.locFromNode(left, typeExpr),
	}
}
