// Package parser builds the syntax tree of one file (grammar subset: imports,
// functions, let, return, expression statements, names, numbers, strings,
// calls and binary operators).
//
// Every node is made when the parser has read up to its end, and its span ends
// there, so node ends never decrease along the tree's node array (see ast).
// A missing name or expression is an empty span at that point.
//
// One mistake gives one error. After an error the parser is recovering: it
// reports nothing more until it is in step with the source again, at the next
// `;`, the `}` closing the block, or a `let`, `return`, `fn` or `import` starting
// a statement or declaration (see sync).
package parser

import (
	"strings"

	"compiler/internal/diag"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
	"compiler/pkg/numeric"
)

// Parse returns the syntax tree of file and the lexical and syntax errors in it.
func Parse(file *source.File) (*ast.Tree, []diag.Diagnostic) {
	tokens, errs := lexer.Scan(file)
	nodes := make([]ast.Node, 1, len(tokens)+1) // index 0: the placeholder
	p := parser{t: &ast.Tree{File: file, Nodes: nodes}, tokens: tokens, errors: errs, lexErrors: len(errs)}
	p.module()
	return p.t, p.errors
}

type parser struct {
	t          *ast.Tree // the tree being built
	tokens     []token.Token
	pos        int
	lastEnd    int // end of the last token read
	errors     []diag.Diagnostic
	lexErrors  int  // errors[:lexErrors] are the lexer's
	recovering bool // an error was reported and the parser is not yet in step again
}

func (p *parser) tok() token.Token {
	for p.tokens[p.pos].Kind == token.DOC_COMMENT {
		p.pos++
	}
	return p.tokens[p.pos]
}

func (p *parser) at(kind token.Kind) bool { return p.tok().Kind == kind }

func (p *parser) next() token.Token {
	t := p.tok()
	if t.Kind != token.EOF {
		p.pos++
		p.lastEnd = t.End
	}
	return t
}

// fail reports a mistake the parser cannot read past, and starts recovering.
// Nothing is reported while recovering already, or when a lexical error lies
// between the last token read and the current one: an illegal character makes
// no token, so the parser's error would only repeat it.
func (p *parser) fail(d *diag.Diagnostic) {
	if !p.recovering && !p.afterLexicalError() {
		p.errors = append(p.errors, *d)
	}
	p.recovering = true
}

// repair reports a mistake the parser reads past as if the missing token were
// there, so it stays in step and goes on reporting.
func (p *parser) repair(d *diag.Diagnostic) {
	if !p.recovering {
		p.errors = append(p.errors, *d)
	}
}

func (p *parser) afterLexicalError() bool {
	next := p.tok().Start
	for _, err := range p.errors[:p.lexErrors] {
		if span := err.Labels[0].Span; p.lastEnd <= span.Start && span.End <= next {
			return true
		}
	}
	return false
}

func (p *parser) expect(kind token.Kind) (token.Token, bool) {
	if p.at(kind) {
		return p.next(), true
	}
	t := p.tok()
	p.fail(diag.New(diag.ExpectedToken, "expected `"+kind.String()+"`, found `"+t.Kind.String()+"`").
		Label(p.t.File, t.Span, "expected `"+kind.String()+"` here"))
	return t, false
}

// close reads the token closing what open opened. When it is missing, the
// error also marks open if it is on an earlier line.
func (p *parser) close(kind token.Kind, open token.Token) {
	if p.at(kind) {
		p.next()
		return
	}
	t := p.tok()
	d := diag.New(diag.ExpectedToken, "expected `"+kind.String()+"`, found `"+t.Kind.String()+"`").
		Label(p.t.File, t.Span, "expected `"+kind.String()+"` here")
	if p.t.File.Line(open.Start) < p.t.File.Line(t.Start) {
		d.Label(p.t.File, open.Span, "this `"+open.Kind.String()+"` is still open")
	}
	p.fail(d)
}

// startsStatement reports whether kind begins a statement or declaration
// wherever it appears, so parsing is in step again there.
func startsStatement(kind token.Kind) bool {
	return kind == token.LET || kind == token.RETURN || kind == token.FN || kind == token.IMPORT
}

// sync skips to where parsing can go on after an error: past the next `;`, or
// up to a `}` closing the current block, a keyword that starts a statement, or
// the end of file. Brackets opened on the way are skipped whole, and a stray
// closing `)` or `]` is skipped. Passing a `;` puts the parser in step again.
func (p *parser) sync() {
	depth := 0
	for !p.at(token.EOF) {
		switch kind := p.tok().Kind; {
		case kind == token.LPAREN || kind == token.LBRACK || kind == token.LBRACE:
			depth++
		case kind == token.RPAREN || kind == token.RBRACK:
			depth = max(depth-1, 0)
		case kind == token.RBRACE:
			if depth == 0 {
				return
			}
			depth--
		case kind == token.SEMICOLON && depth == 0:
			p.next()
			p.recovering = false
			return
		case startsStatement(kind) && depth == 0:
			return
		}
		p.next()
	}
}

// items reads the items of a list up to close, separated by commas, appending
// them to buf. item reads one item and startsItem tells whether a token can
// begin one. An item that follows another without a comma is read as if the
// comma were there, when the list's own close comes next at this depth; all
// missing commas are reported in one error.
func (p *parser) items(buf []ast.Index, close token.Kind, startsItem func(token.Kind) bool, item func() ast.Index) []ast.Index {
	var missing []diag.Fix
	for startsItem(p.tok().Kind) { // neither close nor EOF starts an item
		buf = append(buf, item())
		if p.at(token.COMMA) {
			p.next()
			continue
		}
		if !startsItem(p.tok().Kind) || p.closerAhead() != close {
			break
		}
		missing = append(missing, diag.Fix{File: p.t.File, Span: source.Span{Start: p.lastEnd, End: p.lastEnd}, NewText: ","})
	}
	if len(missing) > 0 {
		help := "add missing `,`"
		if len(missing) > 1 {
			help = "add the missing commas"
		}
		p.repair(diag.New(diag.ExpectedToken, "expected `,`").Label(p.t.File, missing[0].Span, "").Help(help, missing...))
	}
	return buf
}

// closerAhead returns the first closing bracket ahead at the current depth, or
// EOF when there is none.
func (p *parser) closerAhead() token.Kind {
	depth := 0
	for _, t := range p.tokens[p.pos:] {
		switch t.Kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if depth == 0 {
				return t.Kind
			}
			depth--
		}
	}
	return token.EOF
}

// add appends a node spanning from start to the end of the last token read.
func (p *parser) add(kind ast.Kind, start int, a, b ast.Index) ast.Index {
	p.t.Nodes = append(p.t.Nodes, ast.Node{Kind: kind, Span: source.Span{Start: start, End: p.lastEnd}, A: a, B: b})
	return ast.Index(len(p.t.Nodes) - 1)
}

// extra stores values in Extra and returns their offset.
func (p *parser) extra(values ...ast.Index) ast.Index {
	at := ast.Index(len(p.t.Extra))
	p.t.Extra = append(p.t.Extra, values...)
	return at
}

// list stores items as a list in Extra and returns its offset.
func (p *parser) list(items []ast.Index) ast.Index {
	at := p.extra(ast.Index(len(items)))
	p.t.Extra = append(p.t.Extra, items...)
	return at
}

func (p *parser) module() {
	var importBuf, funcBuf [16]ast.Index
	imports, funcs := importBuf[:0], funcBuf[:0]
	for !p.at(token.EOF) {
		if p.at(token.IMPORT) || p.at(token.FN) {
			p.recovering = false
		}
		switch {
		case p.at(token.IMPORT):
			if imp := p.importDecl(); imp != ast.None {
				imports = append(imports, imp)
			}
		case p.at(token.FN):
			funcs = append(funcs, p.funcDecl())
		default:
			// One error for the whole stretch, then go on at the next declaration.
			p.fail(diag.New(diag.InvalidDeclaration, "module scope expects declaration").
				Label(p.t.File, p.tok().Span, "expected a declaration here"))
			for !p.at(token.EOF) && !p.at(token.FN) && !p.at(token.IMPORT) {
				p.next()
			}
		}
	}
	p.t.Root = p.add(ast.KindModule, 0, p.list(imports), p.list(funcs))
}

func (p *parser) importDecl() ast.Index {
	start := p.next().Start
	path, ok := p.expect(token.STRING)
	if !ok {
		p.sync()
		return ast.None
	}
	pathNode := p.add(ast.KindStringLiteral, path.Start, ast.None, ast.None)
	alias := ast.None
	if p.at(token.AS) {
		p.next()
		alias = p.name(ast.KindDeclaredName)
	}
	p.end()
	return p.add(ast.KindImportDecl, start, pathNode, alias)
}

// name reads a name into a node of kind; a missing one gives an empty span.
func (p *parser) name(kind ast.Kind) ast.Index {
	if t, ok := p.expect(token.IDENT); ok {
		return p.add(kind, t.Start, ast.None, ast.None)
	}
	return p.add(kind, p.lastEnd, ast.None, ast.None)
}

func (p *parser) funcDecl() ast.Index {
	start := p.next().Start
	name := p.name(ast.KindDeclaredName)
	var buf [8]ast.Index
	params := buf[:0]
	if open, ok := p.expect(token.LPAREN); ok {
		params = p.items(params, token.RPAREN, func(kind token.Kind) bool { return kind == token.IDENT }, p.parameter)
		p.close(token.RPAREN, open)
	}
	result := ast.None
	if p.at(token.ARROW) {
		p.next()
		result = p.name(ast.KindTypeName)
	}
	paramList := p.list(params)
	body := p.block()
	return p.add(ast.KindFunctionDecl, start, name, p.extra(paramList, result, body))
}

func (p *parser) parameter() ast.Index {
	start := p.tok().Start
	name := p.name(ast.KindDeclaredName)
	p.expect(token.COLON)
	typ := p.name(ast.KindTypeName)
	return p.add(ast.KindParameter, start, name, typ)
}

// block reads a block. A `fn` or `import` inside it means its `}` is missing:
// the block ends there and the declaration is read next.
func (p *parser) block() ast.Index {
	open, ok := p.expect(token.LBRACE)
	if !ok {
		return p.add(ast.KindBlock, p.lastEnd, p.list(nil), ast.None)
	}
	var buf [16]ast.Index
	stmts := buf[:0]
	for {
		kind := p.tok().Kind
		if kind == token.RBRACE || kind == token.EOF || kind == token.FN || kind == token.IMPORT {
			break
		}
		if startsStatement(kind) {
			p.recovering = false
		}
		stmts = append(stmts, p.stmt())
	}
	if p.at(token.RBRACE) {
		p.recovering = false // the block's own `}`: in step again
	}
	p.close(token.RBRACE, open)
	return p.add(ast.KindBlock, open.Start, p.list(stmts), ast.None)
}

func (p *parser) stmt() ast.Index {
	start := p.tok().Start
	switch {
	case p.at(token.LET):
		p.next()
		name := p.name(ast.KindDeclaredName)
		typ := ast.None
		if p.at(token.COLON) {
			p.next()
			typ = p.name(ast.KindTypeName)
		}
		p.expect(token.ASSIGN)
		value := p.expr(0)
		p.end()
		return p.add(ast.KindLetStatement, start, name, p.extra(typ, value))
	case p.at(token.RETURN):
		p.next()
		value := ast.None
		if !p.at(token.SEMICOLON) {
			value = p.expr(0)
		}
		p.end()
		return p.add(ast.KindReturnStatement, start, value, ast.None)
	}
	e := p.expr(0)
	p.end()
	return p.add(ast.KindExpressionStatement, start, e, ast.None)
}

// end reads the `;` that closes a statement, which puts the parser in step
// again; when it is missing, it skips to where parsing can go on.
func (p *parser) end() {
	if p.at(token.SEMICOLON) {
		p.next()
		p.recovering = false
		return
	}
	p.expect(token.SEMICOLON)
	p.sync()
}

func precedence(kind token.Kind) int {
	switch kind {
	case token.EQ, token.LT:
		return 1
	case token.PLUS, token.MINUS:
		return 2
	case token.ASTERISK, token.SLASH:
		return 3
	}
	return 0
}

// expr parses a binary expression whose operators bind tighter than min.
func (p *parser) expr(min int) ast.Index {
	start := p.tok().Start
	left := p.postfix()
	for prec := precedence(p.tok().Kind); prec > min; prec = precedence(p.tok().Kind) {
		op := p.next().Kind
		right := p.expr(prec)
		left = p.add(ast.KindBinaryExpression, start, left, right)
		p.t.Nodes[left].Op = op
	}
	return left
}

func (p *parser) postfix() ast.Index {
	start := p.tok().Start
	e := p.primary()
	for p.at(token.LPAREN) {
		open := p.next()
		var buf [8]ast.Index
		args := p.items(buf[:0], token.RPAREN, startsExpression, func() ast.Index { return p.expr(0) })
		p.close(token.RPAREN, open)
		e = p.add(ast.KindCallExpression, start, e, p.list(args))
	}
	return e
}

// startsExpression reports whether an expression can begin with kind.
func startsExpression(kind token.Kind) bool {
	return kind == token.IDENT || kind == token.NUMBER || kind == token.STRING || kind == token.LPAREN
}

func (p *parser) primary() ast.Index {
	t := p.tok()
	switch t.Kind {
	case token.IDENT:
		p.next()
		return p.add(ast.KindNameReference, t.Start, ast.None, ast.None)
	case token.NUMBER:
		p.next()
		p.checkNumber(t)
		return p.add(ast.KindNumberLiteral, t.Start, ast.None, ast.None)
	case token.STRING:
		p.next()
		return p.add(ast.KindStringLiteral, t.Start, ast.None, ast.None)
	case token.LPAREN:
		open := p.next()
		e := p.expr(0)
		p.close(token.RPAREN, open)
		return e
	}
	p.fail(diag.New(diag.ExpectedToken, "expected expression, found `"+t.Kind.String()+"`").
		Label(p.t.File, t.Span, "expected an expression here"))
	return p.add(ast.KindInvalidExpression, p.lastEnd, ast.None, ast.None)
}

// checkNumber checks the parts of number literal t that the lexer leaves to the
// parser: its type suffix, and a leading zero on a decimal integer. Whether the
// value fits its type is the type checker's to decide.
func (p *parser) checkNumber(t token.Token) {
	text := p.t.File.Text[t.Start:t.End]
	base, digits, suffix := numeric.Split(text)
	if suffix == "" && digits[0] != '0' {
		return // most numbers: no suffix, no leading zero
	}
	isFloat := base == 10 && strings.ContainsAny(digits, ".eE")
	switch {
	// A suffix needs a size after its letter (i32, not i), and no `_`.
	case len(suffix) == 1 || strings.IndexByte(suffix, '_') >= 0:
		p.repair(diag.New(diag.InvalidNumber, "invalid numeric literal suffix "+suffix).Label(p.t.File, t.Span, ""))
	// suffix[0] is the suffix letter: i or u is an integer type, f a float type.
	case suffix != "" && suffix[0] != 'f' && isFloat:
		p.repair(diag.New(diag.InvalidNumber, "integer suffix "+suffix+" cannot be used on a float literal").Label(p.t.File, t.Span, ""))
	// A decimal integer of two or more digits starting with 0, such as 007;
	// with an f suffix (01f32) it is a float and the zero is harmless.
	case base == 10 && !isFloat && len(digits) > 1 && digits[0] == '0' && (suffix == "" || suffix[0] != 'f'):
		// Remove the zeros and separators in front, keeping the last digit.
		zeros := 0
		for zeros < len(digits)-1 && (digits[zeros] == '0' || digits[zeros] == '_') {
			zeros++
		}
		p.errors = append(p.errors, *diag.New(diag.LeadingZero, "decimal integer literal has a leading zero").
			Label(p.t.File, t.Span, "").
			Help("remove the leading zero", diag.Fix{File: p.t.File, Span: source.Span{Start: t.Start, End: t.Start + zeros}}).
			Help("use the `0o` prefix for octal values"))
	}
}
