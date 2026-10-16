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
	nud(token.IDENT, nameReference)
	nud(token.NUMBER, numberLiteral)
	nud(token.STRING, stringLiteral)
	nud(token.LPAREN, parentheses)

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

	// call
	led(token.LPAREN, precCall, call)
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
		return p.add(ast.KindInvalidExpression, p.lastEnd, ast.None, ast.None)
	}
	left := read(p)
	for precTable[p.tok().Kind] > min {
		left = ledTable[p.tok().Kind](p, left)
	}
	return left
}

func nameReference(p *parser) ast.Index {
	return p.add(ast.KindNameReference, p.next().Start, ast.None, ast.None)
}

func stringLiteral(p *parser) ast.Index {
	return p.add(ast.KindStringLiteral, p.next().Start, ast.None, ast.None)
}

func numberLiteral(p *parser) ast.Index {
	t := p.next()
	p.checkNumber(t)
	return p.add(ast.KindNumberLiteral, t.Start, ast.None, ast.None)
}

// parentheses reads `( expression )`.
func parentheses(p *parser) ast.Index {
	open := p.next()
	inner := p.expr(precLowest)
	p.close(token.RPAREN, open)
	return p.add(ast.KindParentheses, open.Start, inner, ast.None)
}

// binary reads a binary operator and its right side. The right side takes only
// operators that bind tighter, so operators of one precedence group to the
// left: a - b - c is (a - b) - c.
func binary(p *parser, left ast.Index) ast.Index {
	prec := precTable[p.tok().Kind]
	op := p.next().Kind
	right := p.expr(prec)
	i := p.add(ast.KindBinaryExpression, p.t.Nodes[left].Span.Start, left, right)
	p.t.Nodes[i].Op = op
	return i
}

// call reads the arguments of a call to left.
func call(p *parser, left ast.Index) ast.Index {
	open := p.next()
	var buf [8]ast.Index
	args := p.items(buf[:0], token.RPAREN, startsExpression, func() ast.Index { return p.expr(precLowest) })
	p.close(token.RPAREN, open)
	return p.add(ast.KindCallExpression, p.t.Nodes[left].Span.Start, left, p.list(args))
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
