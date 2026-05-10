package parser

import (
	"compiler/core/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
)

const (
	precLowest = iota
	precOr
	precAnd
	precEquality
	precCompare
	precSum
	precProduct
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
	tokens.LPAREN:   precCall,
}

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
		if p.peek().Kind == tokens.LPAREN {
			left = p.parseCall(left)
			continue
		}
		left = p.parseInfix(left, nextPrec)
		if left == nil {
			return nil
		}
	}
	return left
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
	case tokens.MINUS, tokens.BANG:
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
			return nil
		}
		return expr
	default:
		p.errorf(tok, diagnostics.ErrInvalidExpression, "expected expression")
		return nil
	}
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
	if start == nil || end == nil {
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
	return id
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