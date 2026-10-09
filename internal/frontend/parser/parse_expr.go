// Pratt parser for expressions.
//
// Prefix handlers parse values that can begin an expression. Infix and
// postfix handlers parse operators, calls, selectors, indexes, and casts.
// Binding power decides whether the current operator belongs to the current
// expression or to an outer expression, which keeps precedence and
// associativity in one table instead of nesting grammar-specific functions.

package parser

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
	"compiler/pkg/colors"
	"compiler/pkg/numeric"
	"fmt"
)

const (
	precLowest uint8 = iota
	precPipe
	precLogicalOr
	precLogicalAnd
	precBitOr
	precBitXor
	precBitAnd
	precEquality
	precCompare
	precShift
	precSum
	precProduct
	precCast
	precPrefix
	precCall
)

// nudFunc parses a prefix (null-denotation) expression.
type nudFunc func(p *Parser, boundary token.Kind) ast.Expr

// ledFunc parses an infix (left-denotation) expression.
type ledFunc func(p *Parser, left ast.Expr, prec uint8, boundary token.Kind) ast.Expr

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
	nud(token.NUMBER, func(p *Parser, _ token.Kind) ast.Expr {
		return p.parseNumberLit("")
	})
	nud(token.STRING, func(p *Parser, _ token.Kind) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.StringLit{Value: tok.Literal, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.CSTRING, func(p *Parser, _ token.Kind) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.StringLit{Value: tok.Literal, IsCString: true, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.BYTE_CHAR, func(p *Parser, _ token.Kind) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.ByteLit{Value: tok.Literal, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.CHAR, func(p *Parser, _ token.Kind) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.CharLit{Value: tok.Literal, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.NONE, func(p *Parser, _ token.Kind) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.NoneLit{Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.TRUE, func(p *Parser, _ token.Kind) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.BoolLit{Value: true, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.FALSE, func(p *Parser, _ token.Kind) ast.Expr {
		tok := p.advance()
		return reg(p, &ast.BoolLit{Value: false, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	})
	nud(token.AT, func(p *Parser, boundary token.Kind) ast.Expr { return p.parseAddressExpr(ast.AddressRaw, boundary) })
	nud(token.AMP, func(p *Parser, boundary token.Kind) ast.Expr { return p.parseAddressExpr(ast.AddressShared, boundary) })
	nud(token.FREE, func(p *Parser, _ token.Kind) ast.Expr { return p.parseFreeExpr() })
	nud(token.PRINT, func(p *Parser, _ token.Kind) ast.Expr { return p.parsePrintExpr() })
	nud(token.PRINTLN, func(p *Parser, _ token.Kind) ast.Expr { return p.parsePrintExpr() })
	nud(token.IDENT, func(p *Parser, boundary token.Kind) ast.Expr { return p.parseIdentExpr(boundary) })

	// grouping
	nud(token.LPAREN, func(p *Parser, _ token.Kind) ast.Expr {
		p.advance()
		inner := p.parseExpr(precLowest, token.RPAREN)
		p.consume(token.RPAREN, "expected ')'")
		return inner
	})

	// prefix / unary
	nud(token.PLUS, func(p *Parser, boundary token.Kind) ast.Expr { return p.parseUnaryExpr(boundary) })
	nud(token.MINUS, func(p *Parser, boundary token.Kind) ast.Expr { return p.parseUnaryExpr(boundary) })
	nud(token.BANG, func(p *Parser, boundary token.Kind) ast.Expr { return p.parseUnaryExpr(boundary) })
	nud(token.TILDE, func(p *Parser, boundary token.Kind) ast.Expr { return p.parseUnaryExpr(boundary) })

	// composite literal
	nud(token.DOT, func(p *Parser, _ token.Kind) ast.Expr { return p.parseCompositeLiteral(nil) })
	nud(token.LBRACK, func(p *Parser, _ token.Kind) ast.Expr { return p.parseArrayLiteral() })

	// logical
	led(token.PIPE_ARROW, precPipe, parsePipeExpr)
	led(token.OROR, precLogicalOr, parseBinaryExpr)
	led(token.ANDAND, precLogicalAnd, parseBinaryExpr)

	// bitwise
	led(token.BAR, precBitOr, parseBinaryExpr)
	led(token.CARET, precBitXor, parseBinaryExpr)
	led(token.AMP, precBitAnd, parseBinaryExpr)

	// equality
	led(token.EQ, precEquality, parseBinaryExpr)
	led(token.NEQ, precEquality, parseBinaryExpr)
	led(token.IS, precEquality, parseIsExpr)

	// relational
	led(token.LT, precCompare, parseBinaryExpr)
	led(token.GT, precCompare, parseBinaryExpr)
	led(token.LE, precCompare, parseBinaryExpr)
	led(token.GE, precCompare, parseBinaryExpr)

	// shift
	led(token.SHL, precShift, parseBinaryExpr)
	led(token.SHR, precShift, parseBinaryExpr)

	// additive
	led(token.PLUS, precSum, parseBinaryExpr)
	led(token.MINUS, precSum, parseBinaryExpr)

	// multiplicative
	led(token.ASTERISK, precProduct, parseBinaryExpr)
	led(token.SLASH, precProduct, parseBinaryExpr)
	led(token.PERCENT, precProduct, parseBinaryExpr)

	// cast
	led(token.AS, precCast, func(p *Parser, left ast.Expr, _ uint8, _ token.Kind) ast.Expr {
		return p.parseAsExpr(left)
	})

	// call & member
	led(token.LPAREN, precCall, func(p *Parser, left ast.Expr, _ uint8, _ token.Kind) ast.Expr {
		return p.parseCall(left)
	})
	led(token.LBRACK, precCall, func(p *Parser, left ast.Expr, _ uint8, _ token.Kind) ast.Expr {
		return p.parseIndexExpr(left)
	})
	led(token.DOT, precCall, func(p *Parser, left ast.Expr, _ uint8, _ token.Kind) ast.Expr {
		return p.parseSelector(left)
	})
}

func parsePipeExpr(p *Parser, left ast.Expr, _ uint8, boundary token.Kind) ast.Expr {
	op := p.advance()
	right := p.parseExpr(precPrefix, boundary)
	call, ok := right.(*ast.CallExpr)
	if !ok || call == nil || call.IsPiped {
		loc := source.NewLocation(p.filePath, op.Start, ast.EndOf(right))
		p.diag.Add(diagnostics.NewError("pipe target must be a free-function call").
			WithCode(diagnostics.ErrInvalidExpression).
			WithPrimaryLabel(loc, "write `value |> function(...)`"))
		return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(right))})
	}
	if _, isMethod := call.Callee.(*ast.SelectorExpr); isMethod {
		loc := ast.LocOf(call.Callee)
		p.diag.Add(diagnostics.NewError("pipe cannot call a method").
			WithCode(diagnostics.ErrInvalidExpression).
			WithPrimaryLabel(loc, "use `value.method(...)`"))
		return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(right))})
	}
	call.Args = append([]ast.Expr{left}, call.Args...)
	call.IsPiped = true
	call.Location = source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(call))
	return call
}

func (p *Parser) parseExpr(precedence uint8, boundary token.Kind) ast.Expr {
	var left ast.Expr
	if p.at(token.LBRACE) {
		left = p.recoverBracedLiteral(p.current().Start, boundary, false)
	}
	if left == nil {
		nudHandler, ok := nudLookup[p.current().Kind]
		if !ok {
			loc := source.NewLocation(p.filePath, p.current().Start, p.current().End)
			p.diag.Add(diagnostics.NewError("expected expression").WithCode(diagnostics.ErrInvalidExpression).WithPrimaryLabel(loc, fmt.Sprintf("found %s", p.current().Kind)))
			return reg(p, &ast.BadExpr{Location: loc})
		}
		left = nudHandler(p, boundary)
		if left == nil {
			loc := source.NewLocation(p.filePath, p.current().Start, p.current().End)
			return reg(p, &ast.BadExpr{Location: loc})
		}
	}
	for !p.at(token.SEMICOLON) && !p.at(token.COMMA) && !p.at(token.RPAREN) && !p.at(token.RBRACE) {
		if p.at(token.LBRACE) {
			switch left.(type) {
			case *ast.Ident, *ast.ScopeResolution:
				_, scoped := left.(*ast.ScopeResolution)
				if bad := p.recoverBracedLiteral(ast.StartOf(left), boundary, scoped); bad != nil {
					left = bad
					continue
				}
			}
		}
		prec, ok := precTable[p.current().Kind]
		if !ok || prec <= precedence {
			break
		}
		left = ledLookup[p.current().Kind](p, left, prec, boundary)
		if left == nil {
			break
		}
	}
	return left
}

func (p *Parser) parseUnaryExpr(boundary token.Kind) ast.Expr {
	tok := p.advance()
	if (tok.Kind == token.PLUS || tok.Kind == token.MINUS) && p.at(token.NUMBER) {
		if literal, err := numeric.ParseLiteral(p.current().Literal); err == nil && literal.ExplicitType != "" {
			return p.parseNumberLit(tok.Literal)
		}
	}
	expr := p.parseExpr(precPrefix, boundary)
	if expr == nil {
		expr = reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
	}
	return reg(p, &ast.UnaryExpr{
		Op:       tok.Literal,
		Expr:     expr,
		Location: source.NewLocation(p.filePath, tok.Start, ast.EndOf(expr)),
	})
}

func (p *Parser) parseNumberLit(sign string) ast.Expr {
	tok := p.advance()
	literal, err := numeric.ParseLiteral(tok.Literal)
	start := tok.Start
	if sign != "" && p.pos >= 2 {
		start = p.stream[p.pos-2].Start
	}
	loc := source.NewLocation(p.filePath, start, tok.End)
	if err != nil {
		p.diag.Add(diagnostics.NewError(err.Error()).WithCode(diagnostics.ErrInvalidNumber).WithPrimaryLabel(loc, ""))
		return reg(p, &ast.BadExpr{Location: loc})
	}
	if len(literal.Value) > 1 && literal.Value[0] == '0' && numeric.IsDecimal(literal.Value) &&
		(literal.ExplicitType == "" || literal.ExplicitType[0] != 'f') {
		d := diagnostics.NewWarning("decimal integer literal has a leading zero").
			WithCode(diagnostics.WarnLeadingZeroDecimal)
		if zeros := leadingZeroRun(tok.Literal); zeros != "" {
			zerosEnd := tok.Start
			zerosEnd.Advance(zeros)
			d.WithPrimaryLabel(loc, "").
				Help("remove the leading zero", diagnostics.Fix.Remove(source.NewLocation(p.filePath, tok.Start, zerosEnd)))
		} else {
			d.WithPrimaryLabel(loc, "remove the leading zero")
		}
		p.diag.Add(d.Help("use the `0o` prefix for octal values"))
	}
	if sign == "-" {
		literal.Value = "-" + literal.Value
	}
	return reg(p, &ast.NumberLit{Value: literal.Value, ExplicitType: literal.ExplicitType, Location: loc})
}

// leadingZeroRun returns the zeros, and the digit separators among them, that
// can be dropped from the front of a decimal literal as written. It always
// leaves a digit behind, so `000` keeps one zero, and returns "" when what
// would remain does not start with a digit.
func leadingZeroRun(literal string) string {
	digitsEnd := 0
	for digitsEnd < len(literal) && (literal[digitsEnd] == '_' || (literal[digitsEnd] >= '0' && literal[digitsEnd] <= '9')) {
		digitsEnd++
	}
	run := 0
	for run < digitsEnd-1 && (literal[run] == '0' || literal[run] == '_') {
		run++
	}
	if run == digitsEnd || literal[run] == '_' {
		return ""
	}
	return literal[:run]
}

func (p *Parser) parseAddressExpr(mode ast.AddressMode, boundary token.Kind) ast.Expr {
	tok := p.advance()
	if mode == ast.AddressShared && p.match(token.MUT) {
		mode = ast.AddressMutable
	}
	expr := p.parseExpr(precPrefix, boundary)
	if expr == nil {
		loc := source.NewLocation(p.filePath, tok.Start, tok.End)
		return reg(p, &ast.BadExpr{Location: loc})
	}
	return reg(p, &ast.AddressExpr{
		Mode:     mode,
		Expr:     expr,
		Location: source.NewLocation(p.filePath, tok.Start, ast.EndOf(expr)),
	})
}

func (p *Parser) parseFreeExpr() ast.Expr {
	start := p.advance()
	if p.consume(token.LPAREN, "expected '(' after 'free'") == nil {
		return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, start.Start, start.End)})
	}
	expr := p.parseExpr(precLowest, token.RPAREN)
	end := p.expectClose(start.Start, token.RPAREN, "(")
	endPos := ast.EndOf(expr)
	if end != nil {
		endPos = end.End
	}
	return reg(p, &ast.FreeExpr{
		Expr:     expr,
		Location: source.NewLocation(p.filePath, start.Start, endPos),
	})
}

func (p *Parser) parsePrintExpr() ast.Expr {
	start := p.advance()
	if p.consume(token.LPAREN, "expected '(' after 'print'") == nil {
		return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, start.Start, start.End)})
	}
	expr := p.parseExpr(precLowest, token.RPAREN)
	end := p.expectClose(start.Start, token.RPAREN, "(")
	endPos := ast.EndOf(expr)
	if end != nil {
		endPos = end.End
	}
	return reg(p, &ast.PrintExpr{Expr: expr, AppendsNewline: start.Kind == token.PRINTLN, Location: source.NewLocation(p.filePath, start.Start, endPos)})
}

func parseBinaryExpr(p *Parser, left ast.Expr, prec uint8, boundary token.Kind) ast.Expr {
	op := p.advance()
	if op == nil {
		return left
	}
	right := p.parseExpr(prec, boundary)
	if right == nil {
		right = reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, op.Start, op.End)})
	}
	return reg(p, &ast.BinaryExpr{
		Left:     left,
		Op:       op.Literal,
		Right:    right,
		Location: source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(right)),
	})
}

func parseIsExpr(p *Parser, left ast.Expr, _ uint8, _ token.Kind) ast.Expr {
	p.advance()
	casePath := p.parseVariantCasePath()
	if casePath == nil {
		return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(left))})
	}
	return reg(p, &ast.IsExpr{
		Value:    left,
		Case:     casePath,
		Location: source.NewLocation(p.filePath, ast.StartOf(left), ast.EndOf(casePath)),
	})
}

func (p *Parser) parseCall(callee ast.Expr) ast.Expr {
	start := p.consume(token.LPAREN, "expected '('")
	if start == nil {
		return nil
	}
	var args []ast.Expr
	if !p.at(token.RPAREN) {
		var missingCommas []diagnostics.CodeFix
		for {
			arg := p.parseExpr(precLowest, token.RPAREN)
			if arg != nil {
				args = append(args, arg)
			}
			// parseExpr has taken every operator that continues the argument,
			// so a token that begins a value here starts the next argument
			// without its comma.
			if !p.listContinues(arg != nil && p.startsArgument(), &missingCommas) {
				break
			}
		}
		p.reportMissingCommas(missingCommas)
	}
	end := p.expectClose(start.Start, token.RPAREN, "(")
	var fallbackEnd source.Position
	if end == nil {
		if len(args) > 0 {
			fallbackEnd = ast.EndOf(args[len(args)-1])
		} else {
			fallbackEnd = ast.EndOf(callee)
		}
	} else {
		fallbackEnd = end.End
	}
	return reg(p, &ast.CallExpr{
		Callee:   callee,
		Args:     args,
		Location: source.NewLocation(p.filePath, ast.StartOf(callee), fallbackEnd),
	})
}

func (p *Parser) parseIndexExpr(left ast.Expr) ast.Expr {
	start := p.consume(token.LBRACK, "expected '['")
	if start == nil {
		return left
	}
	index := p.parseIndexOperand(token.RBRACK)
	end := p.expectClose(start.Start, token.RBRACK, "[")
	var fallbackEnd source.Position
	if end == nil {
		fallbackEnd = ast.EndOf(index)
		if fallbackEnd.IsZero() {
			fallbackEnd = ast.EndOf(left)
		}
	} else {
		fallbackEnd = end.End
	}
	return reg(p, &ast.IndexExpr{
		Expr:     left,
		Index:    index,
		Location: source.NewLocation(p.filePath, ast.StartOf(left), fallbackEnd),
	})
}

func (p *Parser) parseIndexOperand(boundary token.Kind) ast.Expr {
	if p.at(token.DOTDOT) || p.at(token.DOTDOT_EQ) {
		return p.parseRangeExpr(nil, boundary)
	}
	start := p.parseExpr(precLowest, boundary)
	if p.at(token.DOTDOT) || p.at(token.DOTDOT_EQ) {
		return p.parseRangeExpr(start, boundary)
	}
	return start
}

func (p *Parser) parseRangeExpr(start ast.Expr, boundary token.Kind) ast.Expr {
	tok := p.current()
	isEndExclusive := tok.Kind == token.DOTDOT
	p.advance()
	var end ast.Expr
	if !p.at(token.RBRACK) {
		end = p.parseExpr(precLowest, boundary)
	} else if tok.Kind == token.DOTDOT_EQ {
		loc := source.NewLocation(p.filePath, tok.Start, tok.End)
		p.diag.Add(diagnostics.NewError("inclusive range requires an end bound").
			WithCode(diagnostics.ErrInvalidExpression).
			WithPrimaryLabel(loc, "add the inclusive end bound after `..=`"))
	}
	endPos := tok.End
	if end != nil {
		endPos = ast.EndOf(end)
	}
	startPos := tok.Start
	if start != nil {
		startPos = ast.StartOf(start)
	}
	return reg(p, &ast.RangeExpr{
		Start:          start,
		End:            end,
		IsEndExclusive: isEndExclusive,
		Location:       source.NewLocation(p.filePath, startPos, endPos),
	})
}

func (p *Parser) parseArrayLiteral() ast.Expr {
	start := p.consume(token.LBRACK, "expected '['")
	if start == nil {
		return nil
	}
	isDynamic := p.match(token.RBRACK)
	hasInferredLength := false
	var length *ast.NumberLit
	if !isDynamic {
		if p.current().Kind == token.IDENT && p.current().Literal == "_" {
			p.advance()
			hasInferredLength = true
		} else {
			if !p.at(token.NUMBER) {
				p.consume(token.NUMBER, "expected array literal length")
				p.synchronize(token.RBRACK, token.LBRACE, token.SEMICOLON)
				return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, start.Start, start.End)})
			}
			parsed := p.parseNumberLit("")
			var ok bool
			length, ok = parsed.(*ast.NumberLit)
			if !ok {
				return parsed
			}
		}
		if p.consume(token.RBRACK, "expected ']' after array literal length") == nil {
			return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, start.Start, start.End)})
		}
	}
	elem := p.parseTypeExpr()
	if elem == nil {
		return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, start.Start, start.End)})
	}
	values, end, ok := parseBracedItemList(p, "expected '{' after array literal type", "expected '}' after array literal", true,
		func() (ast.Expr, bool) {
			value := p.parseExpr(precLowest, token.RBRACE)
			return value, value != nil
		})
	if !ok {
		return reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(elem))})
	}
	if hasInferredLength {
		length = reg(p, &ast.NumberLit{
			Value:    fmt.Sprintf("%d", len(values)),
			Location: source.NewLocation(p.filePath, start.Start, start.End),
		})
	}
	shape := ast.ArrayFixed
	if isDynamic {
		shape = ast.ArrayOwner
	}
	typ := reg(p, &ast.ArrayType{
		Len:      length,
		Shape:    shape,
		Elem:     elem,
		Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(elem)),
	})
	return reg(p, &ast.ArrayLit{
		Type:              typ,
		Values:            values,
		HasInferredLength: hasInferredLength,
		Location:          source.NewLocation(p.filePath, start.Start, end.End),
	})
}

func (p *Parser) parseIdentExpr(boundary token.Kind) ast.Expr {
	expr := p.parseIdentPath(boundary)
	path, ok := expr.(*ast.ScopeResolution)
	if !ok || !p.match(token.WITH) {
		return expr
	}
	payload := p.parseExpr(precLowest, boundary)
	return reg(p, &ast.VariantLit{
		Case:     path,
		Payload:  payload,
		Location: source.NewLocation(p.filePath, ast.StartOf(path), ast.EndOf(payload)),
	})
}

func (p *Parser) parseIdentPath(boundary token.Kind) ast.Expr {
	next := p.next().Kind
	if next != token.DCOLON &&
		!(next == token.LT && p.typeArgumentsPrecedePathOrLiteral()) &&
		!(next == token.DOT && p.pos+2 < len(p.stream) && p.stream[p.pos+2].Kind == token.LBRACE) {
		return p.parseIdent()
	}
	typ := p.parseTypeExpr()
	if typ == nil {
		return nil
	}
	if p.at(token.DOT) && p.next().Kind == token.LBRACE {
		return p.parseCompositeLiteral(typ)
	}
	if path, ok := typ.(*ast.ScopeResolution); ok {
		return path
	}
	if p.at(token.LBRACE) {
		if bad := p.recoverBracedLiteral(ast.StartOf(typ), boundary, false); bad != nil {
			return bad
		}
		message := "expected value expression"
		if applied, ok := typ.(*ast.AppliedType); ok && len(applied.TypeArgs) > 0 {
			message += " after type arguments"
		}
		p.diag.Add(diagnostics.NewError(message).
			WithCode(diagnostics.ErrInvalidExpression).
			WithPrimaryLabel(ast.LocOf(typ), "expected a value here").
			Help("if constructing a struct, insert '.' before '{'"))
		return reg(p, &ast.BadExpr{Location: ast.LocOf(typ)})
	}
	return nil
}

func (p *Parser) parseVariantCasePath() *ast.ScopeResolution {
	expr := p.parseIdentPath(token.FATARROW)
	path, ok := expr.(*ast.ScopeResolution)
	if ok {
		return path
	}
	loc := ast.LocOf(expr)
	if loc == nil {
		loc = source.NewLocation(p.filePath, p.current().Start, p.current().End)
	}
	p.diag.Add(diagnostics.NewError("enum case must use a fully named path").
		WithCode(diagnostics.ErrInvalidExpression).
		WithPrimaryLabel(loc, "write `Enum::Variant`"))
	return nil
}

func (p *Parser) typeArgumentsPrecedePathOrLiteral() bool {
	depth := 0
	for index := p.pos + 1; index < len(p.stream); index++ {
		switch p.stream[index].Kind {
		case token.LT:
			depth++
		case token.GT:
			depth--
		case token.SHR:
			depth -= 2
		case token.EOF, token.SEMICOLON:
			return false
		}
		if depth < 0 {
			return false
		}
		if depth == 0 {
			return index+1 < len(p.stream) && (p.stream[index+1].Kind == token.DCOLON ||
				p.stream[index+1].Kind == token.LBRACE ||
				(index+2 < len(p.stream) && p.stream[index+1].Kind == token.DOT && p.stream[index+2].Kind == token.LBRACE))
		}
	}
	return false
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

func (p *Parser) parseCompositeLiteral(typ ast.TypeExpr) ast.Expr {
	start := p.consume(token.DOT, "expected '.'")
	if start == nil {
		return nil
	}
	startPos := start.Start
	if typ != nil {
		startPos = ast.StartOf(typ)
	}
	var colons []diagnostics.CodeFix
	fields, end, ok := parseBracedItemList(p, "expected '{' after '.'", "expected '}' after composite literal", true,
		func() (ast.StructLitField, bool) {
			name := p.parseIdent()
			if name == nil {
				return ast.StructLitField{}, false
			}
			if p.at(token.COLON) {
				// `.{ x: 1 }`: the colon of a declaration written where a
				// field is set. Reading on keeps the field and its value.
				colon := p.advance()
				colons = append(colons, diagnostics.Fix.Replace(source.NewLocation(p.filePath, colon.Start, colon.End), " ="))
			} else if p.consume(token.ASSIGN, "expected '=' after struct literal field name") == nil {
				return ast.StructLitField{}, false
			}
			value := p.parseExpr(precLowest, token.RBRACE)
			if value == nil {
				return ast.StructLitField{}, false
			}
			return ast.StructLitField{
				Name:     name,
				Value:    value,
				Location: source.NewLocation(p.filePath, ast.StartOf(name), ast.EndOf(value)),
			}, true
		})
	if len(colons) > 0 {
		p.diag.Add(diagnostics.NewError("expected '=' after struct literal field name").
			WithCode(diagnostics.ErrExpectedToken).
			WithPrimaryLabel(colons[0].Location, "").
			Help("set a field with `=`", colons...))
	}
	location := source.NewLocation(p.filePath, startPos, end.End)
	if !ok {
		return reg(p, &ast.BadExpr{Location: location})
	}
	return reg(p, &ast.StructLit{
		Type:     typ,
		Fields:   fields,
		Location: location,
	})
}

func (p *Parser) parseIdent() *ast.Ident {
	tok := p.current()
	if tok.Kind != token.IDENT {
		loc := source.NewLocation(p.filePath, tok.Start, tok.End)
		d := diagnostics.NewError(fmt.Sprintf("expected identifier, found `%s`", tok.Literal)).
			WithCode(diagnostics.ErrMissingIdentifier).
			WithPrimaryLabel(loc, fmt.Sprintf("found %s", tok.Kind))
		if token.IsKeyword(tok.Literal) {
			d.WithText("help", "`"+tok.Literal+"` is a reserved keyword and cannot be used as a name", colors.GREEN)
		}
		p.diag.Add(d)
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
