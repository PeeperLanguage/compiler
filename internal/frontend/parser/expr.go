package parser

import (
	"compiler/internal/diag"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/pkg/numeric"
)

// Expressions are read by a Pratt parser. A nud reads an expression that a
// token begins (a name, a literal, `(`); a led reads what a token adds after an
// expression on its left (an operator and its right side, a call). Each led
// token has a precedence: it joins the expression being read only when it binds
// tighter than the operator before it, so precedence and associativity live in
// the tables below and not in nested grammar functions.

// precedence is how tightly an operator binds, from loosest to tightest.
type precedence uint8

const (
	precLowest precedence = iota
	precOrElse
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

// nudFunc reads an expression that the current token begins.
type nudFunc func(p *parser) ast.Index

// ledFunc reads what the current token adds to the expression left.
type ledFunc func(p *parser, left ast.Index) ast.Index

// The tables, indexed by token kind. A token without a nud cannot begin an
// expression; a token without a led has precedence precLowest and so never
// continues one.
var (
	nudTable  [256]nudFunc
	ledTable  [256]ledFunc
	precTable [256]precedence
)

func nud(kind token.Kind, read nudFunc) { nudTable[kind] = read }

func led(kind token.Kind, prec precedence, read ledFunc) {
	ledTable[kind] = read
	precTable[kind] = prec
}

// The tables are filled in init: the handlers call expr, which reads them, and
// Go rejects such a cycle in a variable's initializer.
func init() {
	// values
	nud(token.IDENT, nameOrPath)
	nud(token.NUMBER, numberLiteral)
	nud(token.STRING, stringLiteral)
	nud(token.CSTRING, stringLiteral)
	nud(token.CHAR, characterLiteral)
	nud(token.BYTE_CHAR, characterLiteral)
	nud(token.TRUE, boolLiteral)
	nud(token.FALSE, boolLiteral)
	nud(token.NONE, noneLiteral)
	nud(token.LPAREN, parentheses)
	nud(token.LBRACK, arrayLiteral)
	nud(token.DOT, anonymousStructLiteral)

	// prefix operators and built-in calls
	nud(token.PLUS, unary)
	nud(token.MINUS, unary)
	nud(token.BANG, unary)
	nud(token.TILDE, unary)
	nud(token.AMP, reference)
	nud(token.AT, rawAddress)
	nud(token.FREE, builtin)
	nud(token.PRINT, builtin)
	nud(token.PRINTLN, builtin)

	// fallback and pipe
	led(token.ORELSE, precOrElse, orElse)
	led(token.PIPE_ARROW, precPipe, pipe)

	// logical
	led(token.OROR, precLogicalOr, binary)
	led(token.ANDAND, precLogicalAnd, binary)

	// bitwise
	led(token.BAR, precBitOr, binary)
	led(token.CARET, precBitXor, binary)
	led(token.AMP, precBitAnd, binary)

	// equality and comparison
	led(token.EQ, precEquality, binary)
	led(token.NEQ, precEquality, binary)
	led(token.IS, precEquality, isExpression)
	led(token.LT, precCompare, binary)
	led(token.LE, precCompare, binary)
	led(token.GT, precCompare, binary)
	led(token.GE, precCompare, binary)

	// arithmetic
	led(token.SHL, precShift, binary)
	led(token.SHR, precShift, binary)
	led(token.PLUS, precSum, binary)
	led(token.MINUS, precSum, binary)
	led(token.ASTERISK, precProduct, binary)
	led(token.SLASH, precProduct, binary)
	led(token.PERCENT, precProduct, binary)
	led(token.AS, precCast, cast)

	// call, index and selector
	led(token.LPAREN, precCall, call)
	led(token.LBRACK, precCall, index)
	led(token.DOT, precCall, selector)
}

// startsExpression reports whether an expression can begin with kind.
func startsExpression(kind token.Kind) bool { return nudTable[kind] != nil }

// expr reads an expression whose operators bind tighter than min.
func (p *parser) expr(min precedence) ast.Index {
	t := p.tok()
	read := nudTable[t.Kind]
	if read == nil {
		p.fail(diag.New(diag.ExpectedToken, "expected expression, found `"+t.Kind.String()+"`").
			Label(p.t.File, t.Span, "expected an expression here"))
		return p.leaf(ast.KindInvalidExpression, p.lastEnd)
	}
	left := read(p)
	for precTable[p.tok().Kind] > min {
		left = ledTable[p.tok().Kind](p, left)
	}
	return left
}

// nameOrPath reads a name; or, when a path or type arguments follow it, a path
// (`a::B`, `Box<T>::new`), a struct literal (`Point.{...}`) or a variant
// literal (`Enum::Case with payload`).
func nameOrPath(p *parser) ast.Index {
	if !p.startsPath() {
		return p.leaf(ast.KindNameReference, p.next().Start)
	}
	path := p.namedType()
	start := p.t.Nodes[path].Span.Start
	switch {
	case p.at(token.DOT) && p.ahead(1).Kind == token.LBRACE:
		return p.structLiteral(path, start)
	case p.at(token.WITH) && p.t.Nodes[path].Kind == ast.KindPath:
		p.next()
		payload := p.expr(precLowest)
		return p.add(ast.KindVariantLiteral, start, p.t.PackVariantLiteral(ast.VariantLiteral{Case: path, Payload: payload}))
	}
	return path
}

// startsPath reports whether the name at the current token begins a path or a
// type rather than standing alone: `::` follows it, or `.{`, or type
// arguments that `::` or `.{` follow.
func (p *parser) startsPath() bool {
	switch p.ahead(1).Kind {
	case token.DCOLON:
		return true
	case token.DOT:
		return p.ahead(2).Kind == token.LBRACE
	case token.LT:
		return p.typeArgumentsAhead()
	}
	return false
}

// typeArgumentsAhead reports whether the `<` after the current name opens type
// arguments: its matching `>` comes before the statement ends, and `::` or `.{`
// follows it. Only those two decide, so in `if a < b && c > d {` the `<` stays
// a comparison.
func (p *parser) typeArgumentsAhead() bool {
	depth := 0
	for i := p.current() + 1; i < len(p.tokens); i++ {
		switch p.tokens[i].Kind {
		case token.LT:
			depth++
		case token.GT:
			depth--
		case token.SHR:
			depth -= 2
		case token.SEMICOLON, token.LBRACE, token.RBRACE, token.EOF:
			return false
		}
		if depth < 0 {
			return false
		}
		if depth == 0 {
			next := p.tokens[i+1].Kind
			return next == token.DCOLON || next == token.DOT && p.tokens[i+2].Kind == token.LBRACE
		}
	}
	return false
}

func stringLiteral(p *parser) ast.Index {
	t := p.next()
	i := p.leaf(ast.KindStringLiteral, t.Start)
	if t.Kind == token.CSTRING {
		p.setOp(i, token.CSTRING)
	}
	return i
}

func characterLiteral(p *parser) ast.Index {
	t := p.next()
	i := p.leaf(ast.KindCharacterLiteral, t.Start)
	if t.Kind == token.BYTE_CHAR {
		p.setOp(i, token.BYTE_CHAR)
	}
	return i
}

func boolLiteral(p *parser) ast.Index { return p.leaf(ast.KindBoolLiteral, p.next().Start) }

func noneLiteral(p *parser) ast.Index { return p.leaf(ast.KindNoneLiteral, p.next().Start) }

func numberLiteral(p *parser) ast.Index {
	t := p.next()
	p.checkNumber(t)
	return p.leaf(ast.KindNumberLiteral, t.Start)
}

// parentheses reads `( expression )`.
func parentheses(p *parser) ast.Index {
	open := p.next()
	inner := p.expr(precLowest)
	p.close(token.RPAREN, open)
	return p.add(ast.KindParentheses, open.Start, p.t.PackParentheses(ast.Parentheses{Inner: inner}))
}

// binary reads a binary operator and its right side. The right side takes only
// operators that bind tighter, so operators of one precedence group to the
// left: a - b - c is (a - b) - c.
func binary(p *parser, left ast.Index) ast.Index {
	prec := precTable[p.tok().Kind]
	op := p.next().Kind
	right := p.expr(prec)
	return p.setOp(p.add(ast.KindBinaryExpression, p.t.Nodes[left].Span.Start, p.t.PackBinaryExpression(ast.BinaryExpression{Left: left, Right: right})), op)
}

// call reads the arguments of a call to left.
func call(p *parser, left ast.Index) ast.Index {
	open := p.next()
	var buf [8]ast.Index
	args := p.items(buf[:0], token.RPAREN, startsExpression, func() ast.Index { return p.expr(precLowest) })
	p.close(token.RPAREN, open)
	return p.add(ast.KindCallExpression, p.t.Nodes[left].Span.Start, p.t.PackCallExpression(ast.CallExpression{Callee: left, Arguments: p.list(args)}))
}

// unary reads `+x`, `-x`, `!x` or `~x`.
func unary(p *parser) ast.Index {
	op := p.next()
	value := p.expr(precPrefix)
	return p.setOp(p.add(ast.KindUnaryExpression, op.Start, p.t.PackOperand(ast.Operand{Value: value})), op.Kind)
}

// reference reads `&x` or `&mut x`.
func reference(p *parser) ast.Index {
	start := p.next().Start
	var op token.Kind
	if p.at(token.MUT) {
		op = p.next().Kind
	}
	value := p.expr(precPrefix)
	return p.setOp(p.add(ast.KindReferenceExpression, start, p.t.PackOperand(ast.Operand{Value: value})), op)
}

// rawAddress reads `@x`.
func rawAddress(p *parser) ast.Index {
	start := p.next().Start
	value := p.expr(precPrefix)
	return p.add(ast.KindRawAddressExpression, start, p.t.PackOperand(ast.Operand{Value: value}))
}

// builtin reads `free(x)`, `print(x)` or `println(x)`.
func builtin(p *parser) ast.Index {
	keyword := p.next()
	value := ast.None
	if open, ok := p.expect(token.LPAREN); ok {
		value = p.expr(precLowest)
		p.close(token.RPAREN, open)
	}
	if keyword.Kind == token.FREE {
		return p.add(ast.KindFreeExpression, keyword.Start, p.t.PackOperand(ast.Operand{Value: value}))
	}
	return p.setOp(p.add(ast.KindPrintExpression, keyword.Start, p.t.PackOperand(ast.Operand{Value: value})), keyword.Kind)
}

// anonymousStructLiteral reads `.{ name = value, ... }`.
func anonymousStructLiteral(p *parser) ast.Index {
	return p.structLiteral(ast.None, p.tok().Start)
}

// structLiteral reads `.{ name = value, ... }` after typ (None when there is
// no type), a literal that starts at start. A `name: value` field, the colon of
// a declaration where a field is set, is read as if it were `=`, and all such
// colons are reported in one error.
func (p *parser) structLiteral(typ ast.Index, start int) ast.Index {
	p.next() // `.`
	var colons []diag.Fix
	var buf [8]ast.Index
	fields := buf[:0]
	if open, ok := p.expect(token.LBRACE); ok {
		fields = p.items(fields, token.RBRACE, isName, func() ast.Index {
			fieldStart := p.tok().Start
			name := p.name(ast.KindFieldName)
			if t := p.tok(); t.Kind == token.COLON {
				p.next()
				colons = append(colons, diag.Fix{File: p.t.File, Span: t.Span, NewText: " ="})
			} else {
				p.expect(token.ASSIGN)
			}
			value := p.expr(precLowest)
			return p.add(ast.KindFieldValue, fieldStart, p.t.PackFieldValue(ast.FieldValue{Name: name, Value: value}))
		})
		p.close(token.RBRACE, open)
	}
	if len(colons) > 0 {
		p.repair(diag.New(diag.ExpectedToken, "expected `=` after struct literal field name").
			Label(p.t.File, colons[0].Span, "").Help("set a field with `=`", colons...))
	}
	return p.add(ast.KindStructLiteral, start, p.t.PackStructLiteral(ast.StructLiteral{Type: typ, Fields: p.list(fields)}))
}

// arrayLiteral reads `[N]T{ values }`, `[_]T{ values }` (the length is the
// number of values) or `[]T{ values }`.
func arrayLiteral(p *parser) ast.Index {
	start := p.tok().Start
	var typ ast.Index
	if t := p.ahead(1); t.Kind == token.IDENT && p.text(t) == "_" {
		open := p.next()
		length := p.leaf(ast.KindInferredLength, p.next().Start)
		p.close(token.RBRACK, open)
		typ = p.add(ast.KindFixedArrayType, open.Start, p.t.PackFixedArrayType(ast.FixedArrayType{Length: length, Element: p.typeExpr()}))
	} else {
		typ = p.arrayType()
	}
	var buf [8]ast.Index
	values := buf[:0]
	if open, ok := p.expect(token.LBRACE); ok {
		values = p.items(values, token.RBRACE, startsExpression, func() ast.Index { return p.expr(precLowest) })
		p.close(token.RBRACE, open)
	}
	return p.add(ast.KindArrayLiteral, start, p.t.PackArrayLiteral(ast.ArrayLiteral{Type: typ, Values: p.list(values)}))
}

// index reads `[index]` after left.
func index(p *parser, left ast.Index) ast.Index {
	open := p.next()
	operand := p.indexOperand()
	p.close(token.RBRACK, open)
	return p.add(ast.KindIndexExpression, p.t.Nodes[left].Span.Start, p.t.PackIndexExpression(ast.IndexExpression{Value: left, Index: operand}))
}

// indexOperand reads what may stand inside `[ ]` or after `in`: an expression,
// or a range `a..b`, `a..=b`, `..b` or `a..`. Ranges appear nowhere else.
func (p *parser) indexOperand() ast.Index {
	start := p.tok().Start
	low := ast.None
	if !p.at(token.DOTDOT) && !p.at(token.DOTDOT_EQ) {
		if low = p.expr(precLowest); !p.at(token.DOTDOT) && !p.at(token.DOTDOT_EQ) {
			return low
		}
	}
	op := p.next()
	high := ast.None
	if startsExpression(p.tok().Kind) {
		high = p.expr(precLowest)
	} else if op.Kind == token.DOTDOT_EQ {
		p.repair(diag.New(diag.InvalidExpression, "inclusive range requires an end bound").
			Label(p.t.File, op.Span, "add the inclusive end bound after `..=`"))
	}
	return p.setOp(p.add(ast.KindRange, start, p.t.PackRange(ast.Range{Start: low, End: high})), op.Kind)
}

// selector reads `.name` after left.
func selector(p *parser, left ast.Index) ast.Index {
	p.next()
	field := p.name(ast.KindFieldName)
	return p.add(ast.KindSelectorExpression, p.t.Nodes[left].Span.Start, p.t.PackSelectorExpression(ast.SelectorExpression{Value: left, Field: field}))
}

// cast reads `as Type` after left.
func cast(p *parser, left ast.Index) ast.Index {
	p.next()
	typ := p.typeExpr()
	return p.add(ast.KindCastExpression, p.t.Nodes[left].Span.Start, p.t.PackCastExpression(ast.CastExpression{Value: left, Type: typ}))
}

// isExpression reads `is Enum::Case` after left; the case is a full path.
func isExpression(p *parser, left ast.Index) ast.Index {
	p.next()
	path := p.typeExpr()
	if p.t.Nodes[path].Kind != ast.KindPath && p.t.Nodes[path].Kind != ast.KindInvalidType {
		p.repair(diag.New(diag.InvalidExpression, "enum case must use a fully named path").
			Label(p.t.File, p.t.Nodes[path].Span, "write `Enum::Variant`"))
	}
	return p.add(ast.KindIsExpression, p.t.Nodes[left].Span.Start, p.t.PackIsExpression(ast.IsExpression{Value: left, Case: path}))
}

// orElse reads `orelse fallback` or `orelse { block }` after left. The
// fallback may itself end in `orelse`: a orelse b orelse c is a orelse (b
// orelse c).
func orElse(p *parser, left ast.Index) ast.Index {
	p.next()
	var fallback ast.Index
	if p.at(token.LBRACE) {
		fallback = p.block()
	} else {
		fallback = p.expr(precOrElse - 1)
	}
	return p.add(ast.KindOrElseExpression, p.t.Nodes[left].Span.Start, p.t.PackOrElseExpression(ast.OrElseExpression{Value: left, Fallback: fallback}))
}

// pipe reads `|> f(arguments)` after left. Its right side takes only what
// binds tighter than a prefix operator, a call, so `a |> len() as i32` is
// (a |> len()) as i32. The target must call a function, not a method.
func pipe(p *parser, left ast.Index) ast.Index {
	p.next()
	target := p.expr(precPrefix)
	switch n := p.t.Nodes[target]; {
	case n.Kind != ast.KindCallExpression:
		p.repair(diag.New(diag.InvalidExpression, "pipe target must be a free-function call").
			Label(p.t.File, n.Span, "write `value |> function(...)`"))
	case p.t.Nodes[p.t.CallExpression(target).Callee].Kind == ast.KindSelectorExpression:
		callee := p.t.CallExpression(target).Callee
		p.repair(diag.New(diag.InvalidExpression, "pipe cannot call a method").
			Label(p.t.File, p.t.Nodes[callee].Span, "use `value.method(...)`"))
	}
	return p.add(ast.KindPipeExpression, p.t.Nodes[left].Span.Start, p.t.PackPipeExpression(ast.PipeExpression{Value: left, Call: target}))
}

// checkNumber checks the type suffix of number literal t, the part of a number
// the lexer leaves to the parser. Whether the value fits its type is the type
// checker's to decide.
func (p *parser) checkNumber(t token.Token) {
	l := numeric.Split(p.t.File.Text[t.Start:t.End])
	switch {
	case l.Suffix == "":
		// most numbers
	case !l.SuffixWellFormed():
		p.repair(diag.New(diag.InvalidNumberSuffix, "invalid numeric literal suffix "+l.Suffix).Label(p.t.File, t.Span, ""))
	case l.IntegerSuffix() && l.IsFloat():
		p.repair(diag.New(diag.InvalidNumberSuffix, "integer suffix "+l.Suffix+" cannot be used on a float literal").Label(p.t.File, t.Span, ""))
	}
}
