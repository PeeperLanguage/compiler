// Package parser builds the syntax tree of one file (grammar subset: imports,
// functions, let, return, expression statements, names, numbers, strings,
// calls and binary operators).
//
// Every node is made when the parser has read up to its end, and its span ends
// there, so node ends never decrease along the tree's node array (see ast).
// A missing name or expression is an empty span at that point.
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
	p := parser{t: &ast.Tree{File: file, Nodes: nodes}, tokens: tokens, errors: errs}
	p.module()
	return p.t, p.errors
}

type parser struct {
	t       *ast.Tree // the tree being built
	tokens  []token.Token
	pos     int
	lastEnd int // end of the last token read
	errors  []diag.Diagnostic
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

func (p *parser) expect(kind token.Kind) (token.Token, bool) {
	if p.at(kind) {
		return p.next(), true
	}
	t := p.tok()
	p.errors = append(p.errors, *diag.New(diag.ExpectedToken, "expected `"+kind.String()+"`, found `"+t.Kind.String()+"`").
		Label(p.t.File, t.Span, "expected `"+kind.String()+"` here"))
	return t, false
}

// skipTo moves past the next ';' or to the next '}' or end of file.
func (p *parser) skipTo() {
	for !p.at(token.EOF) && !p.at(token.RBRACE) {
		if p.next().Kind == token.SEMICOLON {
			return
		}
	}
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
		switch {
		case p.at(token.IMPORT):
			if imp := p.importDecl(); imp != ast.None {
				imports = append(imports, imp)
			}
		case p.at(token.FN):
			funcs = append(funcs, p.funcDecl())
		default:
			// One error for the whole stretch, then go on at the next declaration.
			p.errors = append(p.errors, *diag.New(diag.InvalidDeclaration, "module scope expects declaration").
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
		p.skipTo()
		return ast.None
	}
	pathNode := p.add(ast.KindStringLiteral, path.Start, ast.None, ast.None)
	alias := ast.None
	if p.at(token.AS) {
		p.next()
		alias = p.name(ast.KindDeclaredName)
	}
	p.expect(token.SEMICOLON)
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
	p.expect(token.LPAREN)
	var buf [8]ast.Index
	params := buf[:0]
	for !p.at(token.RPAREN) && !p.at(token.EOF) {
		paramStart := p.tok().Start
		pname := p.name(ast.KindDeclaredName)
		p.expect(token.COLON)
		ptype := p.name(ast.KindTypeName)
		params = append(params, p.add(ast.KindParameter, paramStart, pname, ptype))
		if !p.at(token.COMMA) {
			break
		}
		p.next()
	}
	p.expect(token.RPAREN)
	result := ast.None
	if p.at(token.ARROW) {
		p.next()
		result = p.name(ast.KindTypeName)
	}
	paramList := p.list(params)
	body := p.block()
	return p.add(ast.KindFunctionDecl, start, name, p.extra(paramList, result, body))
}

func (p *parser) block() ast.Index {
	start, _ := p.expect(token.LBRACE)
	var buf [16]ast.Index
	stmts := buf[:0]
	for !p.at(token.RBRACE) && !p.at(token.EOF) {
		stmts = append(stmts, p.stmt())
	}
	p.expect(token.RBRACE)
	return p.add(ast.KindBlock, start.Start, p.list(stmts), ast.None)
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

// end reads the ';' that closes a statement, skipping to the next statement
// when it is missing.
func (p *parser) end() {
	if _, ok := p.expect(token.SEMICOLON); !ok {
		p.skipTo()
	}
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
		p.next()
		var buf [8]ast.Index
		args := buf[:0]
		for !p.at(token.RPAREN) && !p.at(token.EOF) {
			args = append(args, p.expr(0))
			if !p.at(token.COMMA) {
				break
			}
			p.next()
		}
		p.expect(token.RPAREN)
		e = p.add(ast.KindCallExpression, start, e, p.list(args))
	}
	return e
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
		p.next()
		e := p.expr(0)
		p.expect(token.RPAREN)
		return e
	}
	p.expect(token.IDENT)
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
		p.errors = append(p.errors, *diag.New(diag.InvalidNumber, "invalid numeric literal suffix "+suffix).Label(p.t.File, t.Span, ""))
	// suffix[0] is the suffix letter: i or u is an integer type, f a float type.
	case suffix != "" && suffix[0] != 'f' && isFloat:
		p.errors = append(p.errors, *diag.New(diag.InvalidNumber, "integer suffix "+suffix+" cannot be used on a float literal").Label(p.t.File, t.Span, ""))
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
