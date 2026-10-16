package parser

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
)

// block reads a block. A declaration keyword inside it means its `}` is
// missing: the block ends there and the declaration is read next.
func (p *parser) block() ast.Index {
	open, ok := p.expect(token.LBRACE)
	if !ok {
		return p.add(ast.KindBlock, p.lastEnd, p.t.PackBlock(ast.Block{Statements: ast.EmptyList}))
	}
	var buf [16]ast.Index
	stmts := buf[:0]
	for {
		kind := p.tok().Kind
		if kind == token.RBRACE || kind == token.EOF || startsDeclaration(kind) {
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
	return p.add(ast.KindBlock, open.Start, p.t.PackBlock(ast.Block{Statements: p.list(stmts)}))
}

// statements holds, by token kind, the reader of each statement that a keyword
// begins; any other token begins an expression statement. It is filled in
// init: a reader that reads a block calls stmt, which reads this table, and Go
// rejects such a cycle in a variable's initializer.
var statements [256]func(p *parser) ast.Index

func init() {
	statements[token.LET] = letStatement
	statements[token.RETURN] = returnStatement
}

func (p *parser) stmt() ast.Index {
	if read := statements[p.tok().Kind]; read != nil {
		return read(p)
	}
	start := p.tok().Start
	e := p.expr(precLowest)
	p.end()
	return p.add(ast.KindExpressionStatement, start, p.t.PackExpressionStatement(ast.ExpressionStatement{Expression: e}))
}

// letStatement reads `let name: Type = value;`; the type and the value may be
// left out.
func letStatement(p *parser) ast.Index {
	start := p.next().Start
	name, typ, value := p.binding()
	return p.add(ast.KindLetStatement, start, p.t.PackLetStatement(ast.LetStatement{Name: name, Type: typ, Value: value}))
}

// returnStatement reads `return value;`; the value may be left out.
func returnStatement(p *parser) ast.Index {
	start := p.next().Start
	value := ast.None
	if !p.at(token.SEMICOLON) {
		value = p.expr(precLowest)
	}
	p.end()
	return p.add(ast.KindReturnStatement, start, p.t.PackReturnStatement(ast.ReturnStatement{Value: value}))
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
