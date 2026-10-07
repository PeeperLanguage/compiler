package parser

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

// recoverBracedLiteral owns missing literal introducers, not ordinary body braces.
// Scan before committing: a header's assignment statements must remain its body.
func (p *Parser) recoverBracedLiteral(start source.Position, boundary token.Kind, scoped bool) ast.Expr {
	open := p.current()
	if open.Kind != token.LBRACE {
		return nil
	}
	if boundary == token.LBRACE && !p.literalFieldFollows(p.pos+1, false) {
		return nil
	}
	closers := []token.Kind{token.RBRACE}
	end := p.pos + 1
scan:
	for end < len(p.stream) {
		kind := p.stream[end].Kind
		if len(closers) == 1 {
			switch kind {
			case token.COMMA:
				if (boundary == token.RPAREN || boundary == token.RBRACK) && !p.literalFieldFollows(end+1, true) {
					break scan
				}
			case token.SEMICOLON, token.LET, token.CONST, token.FN, token.RETURN,
				token.IF, token.FOR, token.MATCH, token.ELSE, token.STRUCT, token.ENUM, token.IFACE, token.TYPE:
				break scan
			}
		}
		switch kind {
		case token.LBRACE:
			closers = append(closers, token.RBRACE)
		case token.LPAREN:
			closers = append(closers, token.RPAREN)
		case token.LBRACK:
			closers = append(closers, token.RBRACK)
		case token.RBRACE, token.RPAREN, token.RBRACK:
			if kind != closers[len(closers)-1] {
				break scan
			}
			closers = closers[:len(closers)-1]
			if len(closers) == 0 {
				end++
				break scan
			}
		case token.EOF:
			break scan
		}
		end++
	}
	if boundary == token.LBRACE && (len(closers) != 0 || end >= len(p.stream) ||
		(p.stream[end].Kind != token.LBRACE && precTable[p.stream[end].Kind] == 0)) {
		return nil
	}
	message := "struct literal requires '.' before '{'"
	help := "write `Type.{ ... }` for a named struct or `.{ ... }` for an anonymous struct"
	if scoped {
		message = "literal requires '.' or 'with' before '{'"
		help = "write `Path.{ ... }` for a struct literal or `Enum::Variant with .{ ... }` for an enum payload"
	}
	p.diag.Add(diagnostics.NewError(message).
		WithCode(diagnostics.ErrInvalidExpression).
		WithPrimaryLabel(source.NewLocation(p.filePath, open.Start, open.End), "missing literal introducer").
		WithHelp(help))
	p.pos = end
	return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, start, p.prev().End)})
}

func (p *Parser) literalFieldFollows(index int, allowClose bool) bool {
	for index < len(p.stream) && p.stream[index].Kind == token.DOC_COMMENT {
		index++
	}
	if allowClose && index < len(p.stream) && p.stream[index].Kind == token.RBRACE {
		return true
	}
	return index+1 < len(p.stream) && p.stream[index].Kind == token.IDENT && p.stream[index+1].Kind == token.ASSIGN
}
