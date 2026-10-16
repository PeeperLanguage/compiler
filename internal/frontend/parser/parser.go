// Package parser builds the syntax tree of one file: imports; functions,
// constants, structs, enums, interfaces and type aliases with their doc
// comments and attributes; and every type, statement, pattern and expression.
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
	"compiler/internal/diag"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

// Parse returns the syntax tree of file and the lexical and syntax errors in it.
func Parse(file *source.File) (*ast.Tree, []diag.Diagnostic) {
	tokens, errs := lexer.Scan(file)
	nodes := make([]ast.Node, 1, len(tokens)+1)    // index 0: the placeholder
	extra := make([]ast.Index, 1, len(tokens)/2+1) // offset 0: the empty list
	p := parser{t: &ast.Tree{File: file, Nodes: nodes, Extra: extra}, tokens: tokens, errors: errs, lexErrors: len(errs)}
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

// current returns the index of the current token: the next one that is not a
// doc comment. Doc comments are read only where a declaration may begin (see
// docComment); everywhere else the parser looks past them.
func (p *parser) current() int {
	i := p.pos
	for p.tokens[i].Kind == token.DOC_COMMENT {
		i++
	}
	return i
}

func (p *parser) tok() token.Token { return p.tokens[p.current()] }

func (p *parser) at(kind token.Kind) bool { return p.tok().Kind == kind }

// ahead returns the token n tokens after the current one, or EOF past the end.
func (p *parser) ahead(n int) token.Token {
	i := p.current()
	for ; n > 0 && p.tokens[i].Kind != token.EOF; n-- {
		for i++; p.tokens[i].Kind == token.DOC_COMMENT; i++ {
		}
	}
	return p.tokens[i]
}

func (p *parser) next() token.Token {
	p.pos = p.current()
	t := p.tokens[p.pos]
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
	p.fail(p.missing(kind))
	return p.tok(), false
}

// missing describes kind missing before the current token. A missing closer
// belongs at the end of the text read so far, and so does any token when the
// current one is on a later line: the error points there and, when kind has
// one fixed spelling, offers to insert it. Otherwise it points at the token
// found. The line decides only where the error points, never how parsing
// goes on.
func (p *parser) missing(kind token.Kind) *diag.Diagnostic {
	t := p.tok()
	d := diag.New(diag.ExpectedToken, "expected `"+kind.String()+"`, found `"+t.Kind.String()+"`")
	closer := kind == token.RPAREN || kind == token.RBRACK || kind == token.RBRACE || kind == token.GT
	if p.lastEnd == 0 || !closer && p.t.File.Line(t.Start) == p.t.File.Line(p.lastEnd) {
		return d.Label(p.t.File, t.Span, "expected `"+kind.String()+"` here")
	}
	at := source.Span{Start: p.lastEnd, End: p.lastEnd}
	d.Label(p.t.File, at, "")
	if text, ok := token.Spelling(kind); ok {
		d.Help("add missing `"+text+"`", diag.Fix{File: p.t.File, Span: at, NewText: text})
	}
	return d
}

// close reads the token closing what open opened. When that closer comes
// later at this depth, what stands before it cannot be in the list: that is
// reported, and parsing skips to the closer. Otherwise the closer is missing,
// and the error also marks open if it is on an earlier line.
func (p *parser) close(kind token.Kind, open token.Token) {
	if p.at(kind) {
		p.next()
		return
	}
	if i := p.closerAhead(); p.tokens[i].Kind == kind {
		t := p.tok()
		p.fail(diag.New(diag.ExpectedToken, "expected `"+kind.String()+"`, found `"+t.Kind.String()+"`").
			Label(p.t.File, t.Span, "this cannot be here"))
		for p.current() < i {
			p.next()
		}
		p.next()
		return
	}
	d := p.missing(kind)
	if p.t.File.Line(open.Start) < p.t.File.Line(d.Labels[0].Span.Start) {
		d.Label(p.t.File, open.Span, "this `"+open.Kind.String()+"` is still open")
	}
	p.fail(d)
}

// startsStatement reports whether kind begins a statement or declaration
// wherever it appears, so parsing is in step again there.
func startsStatement(kind token.Kind) bool {
	return statements[kind] != nil || startsDeclaration(kind)
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
// begin one. Extra commas are accepted (they are a matter of style, left to a
// lint). Two mistakes are read as if the comma were there, and each kind is
// reported once per list:
//   - an item that follows another without a comma, when the list's own close
//     comes next at this depth;
//   - a `;` between the items of a `{ }` list: always in a declaration's body
//     (inDeclaration), and in an expression's braces when they end the
//     expression (`;`, `,` or `)` follows the `}`).
func (p *parser) items(buf []ast.Index, close token.Kind, inDeclaration bool, startsItem func(token.Kind) bool, item func() ast.Index) []ast.Index {
	var missing, semicolons []diag.Fix
	for {
		for p.at(token.COMMA) {
			p.next()
		}
		if !startsItem(p.tok().Kind) { // neither close nor EOF starts an item
			break
		}
		buf = append(buf, item())
		switch t := p.tok(); {
		case t.Kind == token.COMMA:
			continue
		case t.Kind == token.SEMICOLON && close == token.RBRACE && p.semicolonSeparates(inDeclaration):
			fix := diag.Fix{File: p.t.File, Span: t.Span, NewText: ","}
			if p.ahead(1).Kind == token.RBRACE {
				fix.NewText = ""
			}
			semicolons = append(semicolons, fix)
			p.next()
			continue
		case startsItem(t.Kind) && (close == token.GT || p.tokens[p.closerAhead()].Kind == close):
			// `>` is no bracket the closer scan follows; in `<T U>` the next
			// name can only be another item.
			missing = append(missing, diag.Fix{File: p.t.File, Span: source.Span{Start: p.lastEnd, End: p.lastEnd}, NewText: ","})
			continue
		}
		break
	}
	if len(missing) > 0 {
		help := "add missing `,`"
		if len(missing) > 1 {
			help = "add the missing commas"
		}
		p.repair(diag.New(diag.ExpectedToken, "expected `,`").Label(p.t.File, missing[0].Span, "").Help(help, missing...))
	}
	if len(semicolons) > 0 {
		p.repair(diag.New(diag.ExpectedToken, "expected `,` between items, found `;`").
			Label(p.t.File, semicolons[0].Span, "").Help("separate items with `,`", semicolons...))
	}
	return buf
}

// semicolonSeparates reports whether the `;` at the current token stands for
// a `,` in a `{ }` list (see items): the list's `}` comes next at this depth,
// and the list is a declaration's body or `;`, `,` or `)` follows the `}`.
func (p *parser) semicolonSeparates(inDeclaration bool) bool {
	i := p.closerAhead()
	if p.tokens[i].Kind != token.RBRACE {
		return false
	}
	if inDeclaration {
		return true
	}
	after := p.tokens[min(i+1, len(p.tokens)-1)].Kind
	return after == token.SEMICOLON || after == token.COMMA || after == token.RPAREN
}

// closerAhead returns the index of the first closing bracket ahead at the
// current depth, or of the EOF token when there is none.
func (p *parser) closerAhead() int {
	depth := 0
	for i := p.pos; i < len(p.tokens); i++ {
		switch p.tokens[i].Kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return len(p.tokens) - 1
}

// add appends a node with slots s, spanning from start, where the token
// beginning it was, to the end of the last token read. A node that read nothing
// (its start lies past what was read) is an empty span at the read position.
func (p *parser) add(kind ast.Kind, start int, s ast.Slots) ast.Index {
	start = min(start, p.lastEnd)
	p.t.Nodes = append(p.t.Nodes, ast.Node{Kind: kind, Span: source.Span{Start: start, End: p.lastEnd}, A: s.A, B: s.B})
	return ast.Index(len(p.t.Nodes) - 1)
}

// leaf appends a node without children.
func (p *parser) leaf(kind ast.Kind, start int) ast.Index { return p.add(kind, start, ast.Slots{}) }

// text returns the source text of token t.
func (p *parser) text(t token.Token) string { return p.t.File.Text[t.Start:t.End] }

// setOp sets the Op of node i and returns i.
func (p *parser) setOp(i ast.Index, op token.Kind) ast.Index {
	p.t.Nodes[i].Op = op
	return i
}

// list stores items as a list in Extra and returns its offset; every empty
// list shares offset 0.
func (p *parser) list(items []ast.Index) ast.List {
	if len(items) == 0 {
		return ast.EmptyList
	}
	at := ast.List(len(p.t.Extra))
	p.t.Extra = append(p.t.Extra, ast.Index(len(items)))
	p.t.Extra = append(p.t.Extra, items...)
	return at
}
