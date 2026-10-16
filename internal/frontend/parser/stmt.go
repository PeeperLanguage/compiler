package parser

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
)

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

// statements holds, by token kind, the reader of each statement that a keyword
// begins; any other token begins an expression statement. It is filled in
// init: a reader that reads a block calls stmt, which reads this table, and Go
// rejects such a cycle in a variable's initializer.
var statements [256]func(p *parser) ast.Index

func init() {
	statements[token.LET] = (*parser).letStatement
	statements[token.RETURN] = (*parser).returnStatement
}

func (p *parser) stmt() ast.Index {
	if read := statements[p.tok().Kind]; read != nil {
		return read(p)
	}
	start := p.tok().Start
	e := p.expr(precLowest)
	p.end()
	return p.add(ast.KindExpressionStatement, start, e, ast.None)
}

// letStatement reads `let name: Type = value;`; the type may be left out.
func (p *parser) letStatement() ast.Index {
	start := p.next().Start
	name := p.name(ast.KindDeclaredName)
	typ := ast.None
	if p.at(token.COLON) {
		p.next()
		typ = p.typeExpr()
	}
	p.expect(token.ASSIGN)
	value := p.expr(precLowest)
	p.end()
	return p.add(ast.KindLetStatement, start, name, p.extra(typ, value))
}

// returnStatement reads `return value;`; the value may be left out.
func (p *parser) returnStatement() ast.Index {
	start := p.next().Start
	value := ast.None
	if !p.at(token.SEMICOLON) {
		value = p.expr(precLowest)
	}
	p.end()
	return p.add(ast.KindReturnStatement, start, value, ast.None)
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
