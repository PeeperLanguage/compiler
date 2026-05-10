package parser

import (
	"fmt"

	"compiler/core/diagnostics"
	"compiler/core/source"
	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
)

type Parser struct {
	filePath string
	stream   []tokens.Token
	diag     *diagnostics.DiagnosticBag
	pos      int
}

func New(filePath string, stream []tokens.Token, diag *diagnostics.DiagnosticBag) *Parser {
	return &Parser{
		filePath: filePath,
		stream:   stream,
		diag:     diag,
	}
}

func (p *Parser) ParseModule() *ast.Module {
	mod := &ast.Module{
		FilePath: p.filePath,
		Imports:  make([]*ast.ImportDecl, 0),
		Decls:    make([]ast.Decl, 0),
	}
	for !p.at(tokens.EOF) {
		if p.at(tokens.IMPORT) {
			imp := p.parseImport()
			if imp != nil {
				mod.Imports = append(mod.Imports, imp)
			}
			continue
		}
		decl := p.parseDecl()
		if decl != nil {
			mod.Decls = append(mod.Decls, decl)
			continue
		}
		p.synchronizeDecl()
	}
	return mod
}

func (p *Parser) parseImport() *ast.ImportDecl {
	start := p.consume(tokens.IMPORT, "expected import")
	if start == nil {
		return nil
	}
	var path ast.Expr
	switch p.peek().Kind {
	case tokens.STRING:
		tok := p.advance()
		path = &ast.StringLit{
			Value:    tok.Literal,
			Location: p.loc(*tok, *tok),
		}
	case tokens.IDENT:
		path = p.parseIdentExpr()
	default:
		p.errorf(p.peek(), diagnostics.ErrExpectedToken, "expected import path")
		return nil
	}
	var alias *ast.Ident
	if p.match(tokens.AS) {
		alias = p.parseIdent()
		if alias == nil {
			return nil
		}
	}
	end := p.consume(tokens.SEMICOLON, "expected ';' after import")
	if end == nil {
		return nil
	}
	return &ast.ImportDecl{
		Path:     path,
		Alias:    alias,
		Location: p.loc(*start, *end),
	}
}

func (p *Parser) parseDecl() ast.Decl {
	switch p.peek().Kind {
	case tokens.FN:
		return p.parseFnDecl()
	case tokens.LET:
		return p.parseLetDecl(true)
	case tokens.CONST:
		return p.parseConstDecl(true)
	default:
		p.errorf(p.peek(), diagnostics.ErrInvalidDeclaration, "expected declaration")
		return nil
	}
}

func (p *Parser) parseFnDecl() ast.Decl {
	start := p.consume(tokens.FN, "expected fn")
	receiver := p.parseOptionalReceiver()
	name := p.parseIdent()
	if name == nil {
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	if p.consume(tokens.LPAREN, "expected '(' after function name") == nil {
		return nil
	}
	params := p.parseParams()
	if p.consume(tokens.RPAREN, "expected ')' after parameters") == nil {
		return nil
	}
	retTy := ast.TypeExpr(&ast.NamedType{Name: "i32", Location: p.loc(*start, *start)})
	if p.match(tokens.COLON) {
		retTy = p.parseTypeI32()
		if retTy == nil {
			return nil
		}
	}
	body := p.parseBlock()
	if body == nil {
		return nil
	}
	return &ast.FnDecl{
		Receiver:   receiver,
		Name:       name,
		TypeParams: typeParams,
		Params:     params,
		ReturnType: retTy,
		Body:       body,
		Location:   p.loc(*start, p.lastNonNilToken(*start)),
	}
}

func (p *Parser) parseOptionalReceiver() *ast.Param {
	if !p.at(tokens.LPAREN) {
		return nil
	}
	if p.pos+2 >= len(p.stream) {
		return nil
	}
	if p.stream[p.pos+1].Kind != tokens.IDENT || p.stream[p.pos+2].Kind != tokens.COLON {
		return nil
	}
	start := p.consume(tokens.LPAREN, "expected '(' before receiver")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		return nil
	}
	if p.consume(tokens.COLON, "expected ':' after receiver name") == nil {
		return nil
	}
	ty := p.parseTypeI32()
	if ty == nil {
		return nil
	}
	end := p.consume(tokens.RPAREN, "expected ')' after receiver")
	if end == nil {
		return nil
	}
	return &ast.Param{
		Name:     name,
		Type:     ty,
		Location: p.loc(*start, *end),
	}
}

func (p *Parser) parseOptionalTypeParams() []ast.TypeParam {
	params := make([]ast.TypeParam, 0)
	if !p.match(tokens.LBRACK) {
		return params
	}
	for {
		name := p.parseIdent()
		if name == nil {
			return params
		}
		params = append(params, ast.TypeParam{
			Name:     name,
			Location: name.Location,
		})
		if p.match(tokens.COMMA) {
			continue
		}
		break
	}
	_ = p.consume(tokens.RBRACK, "expected ']' after type parameters")
	return params
}

func (p *Parser) parseParams() []ast.Param {
	params := make([]ast.Param, 0)
	if p.at(tokens.RPAREN) {
		return params
	}
	for {
		name := p.parseIdent()
		if name == nil {
			return params
		}
		if p.consume(tokens.COLON, "expected ':' after parameter name") == nil {
			return params
		}
		ty := p.parseTypeI32()
		if ty == nil {
			return params
		}
		params = append(params, ast.Param{
			Name:     name,
			Type:     ty,
			Location: p.locFromNode(name, ty),
		})
		if !p.match(tokens.COMMA) {
			break
		}
	}
	return params
}

func (p *Parser) parseBindingFields(token tokens.Kind) (name *ast.Ident, ty ast.TypeExpr, value ast.Expr, end *tokens.Token, ok bool) {
	name = p.parseIdent()
	if name == nil {
		return nil, nil, nil, nil, false
	}
	if p.match(tokens.COLON) {
		ty = p.parseTypeI32()
		if ty == nil {
			return nil, nil, nil, nil, false
		}
	}
	if p.consume(tokens.ASSIGN, "expected '=' after "+string(token)+" binding") == nil {
		return nil, nil, nil, nil, false
	}
	value = p.parseExpr(0)
	if value == nil {
		return nil, nil, nil, nil, false
	}
	end = p.consume(tokens.SEMICOLON, "expected ';' after "+string(token))
	if end == nil {
		return nil, nil, nil, nil, false
	}
	return name, ty, value, end, true
}

func (p *Parser) parseLetDecl(isModuleVar bool) *ast.LetDecl {
	start := p.consume(tokens.LET, "expected let")
	if start == nil {
		return nil
	}
	isMutable := p.match(tokens.MUT)
	name, ty, value, end, ok := p.parseBindingFields(tokens.LET)
	if !ok {
		return nil
	}
	return &ast.LetDecl{
		Name:        name,
		Type:        ty,
		Value:       value,
		IsMutable:   isMutable,
		IsModuleVar: isModuleVar,
		Location:    p.loc(*start, *end),
	}
}

func (p *Parser) parseConstDecl(isModuleVar bool) *ast.ConstDecl {
	start := p.consume(tokens.CONST, "expected const")
	if start == nil {
		return nil
	}
	name, ty, value, end, ok := p.parseBindingFields(tokens.CONST)
	if !ok {
		return nil
	}
	return &ast.ConstDecl{
		Name:        name,
		Type:        ty,
		Value:       value,
		IsModuleVar: isModuleVar,
		Location:    p.loc(*start, *end),
	}
}

func (p *Parser) parseBlock() *ast.BlockStmt {
	start := p.consume(tokens.LBRACE, "expected '{'")
	if start == nil {
		return nil
	}
	stmts := make([]ast.Stmt, 0)
	for !p.at(tokens.RBRACE) && !p.at(tokens.EOF) {
		stmt := p.parseStmt()
		if stmt != nil {
			stmts = append(stmts, stmt)
		} else {
			p.synchronizeStmt()
		}
	}
	end := p.consume(tokens.RBRACE, "expected '}'")
	if end == nil {
		return nil
	}
	return &ast.BlockStmt{
		Stmts:    stmts,
		Location: p.loc(*start, *end),
	}
}

func (p *Parser) parseStmt() ast.Stmt {
	switch p.peek().Kind {
	case tokens.LET:
		return p.parseLetDecl(false)
	case tokens.CONST:
		return p.parseConstDecl(false)
	case tokens.RETURN:
		return p.parseReturnStmt()
	default:
		return p.parseExprStmt()
	}
}

func (p *Parser) parseReturnStmt() ast.Stmt {
	start := p.consume(tokens.RETURN, "expected return")
	if start == nil {
		return nil
	}
	var value ast.Expr
	if !p.at(tokens.SEMICOLON) {
		value = p.parseExpr(0)
	}
	end := p.consume(tokens.SEMICOLON, "expected ';' after return")
	if end == nil {
		return nil
	}
	return &ast.ReturnStmt{
		Value:    value,
		Location: p.loc(*start, *end),
	}
}

func (p *Parser) parseExprStmt() ast.Stmt {
	expr := p.parseExpr(0)
	if expr == nil {
		return nil
	}
	end := p.consume(tokens.SEMICOLON, "expected ';' after expression")
	if end == nil {
		return nil
	}
	return &ast.ExprStmt{
		Expr:     expr,
		Location: p.locFromNode(expr, &ast.BadExpr{Location: p.loc(*end, *end)}),
	}
}

func (p *Parser) parseTypeI32() ast.TypeExpr {
	tok := p.peek()
	if tok.Kind != tokens.IDENT || tok.Literal != "i32" {
		p.errorf(tok, diagnostics.ErrInvalidType, "only i32 type is supported for now")
		return nil
	}
	p.advance()
	return &ast.NamedType{Name: "i32", Location: p.loc(tok, tok)}
}

func (p *Parser) consume(kind tokens.Kind, msg string) *tokens.Token {
	if p.peek().Kind == kind {
		return p.advance()
	}
	p.errorf(p.peek(), diagnostics.ErrExpectedToken, msg)
	return nil
}

func (p *Parser) match(kind tokens.Kind) bool {
	if p.peek().Kind != kind {
		return false
	}
	p.advance()
	return true
}

func (p *Parser) at(kind tokens.Kind) bool {
	return p.peek().Kind == kind
}

func (p *Parser) peek() tokens.Token {
	if p.pos >= len(p.stream) {
		return tokens.Token{Kind: tokens.EOF}
	}
	return p.stream[p.pos]
}

func (p *Parser) advance() *tokens.Token {
	if p.pos >= len(p.stream) {
		return nil
	}
	tok := p.stream[p.pos]
	p.pos++
	return &tok
}

func (p *Parser) peekPrecedence() int {
	if p, ok := infixPrec[p.peek().Kind]; ok {
		return p
	}
	return precLowest
}

func (p *Parser) synchronizeDecl() {
	for !p.at(tokens.EOF) {
		if p.match(tokens.SEMICOLON) {
			return
		}
		switch p.peek().Kind {
		case tokens.IMPORT, tokens.FN, tokens.LET, tokens.CONST:
			return
		}
		p.advance()
	}
}

func (p *Parser) synchronizeStmt() {
	for !p.at(tokens.EOF) && !p.at(tokens.RBRACE) {
		if p.match(tokens.SEMICOLON) {
			return
		}
		p.advance()
	}
}

func (p *Parser) errorf(tok tokens.Token, code, msg string) {
	if p.diag == nil {
		return
	}
	loc := source.NewLocation(p.filePath, tok.Start, tok.End)
	p.diag.Add(
		diagnostics.NewError(msg).
			WithCode(code).
			WithPrimaryLabel(&loc, fmt.Sprintf("found %s", tok.Kind)),
	)
}

func (p *Parser) loc(start, end tokens.Token) source.Location {
	return source.NewLocation(p.filePath, start.Start, end.End)
}

func (p *Parser) locFromNode(left, right ast.Node) source.Location {
	l := left.Loc()
	r := right.Loc()
	start := source.NewPosition()
	end := source.NewPosition()
	if l.Start != nil {
		start = *l.Start
	}
	if r.End != nil {
		end = *r.End
	}
	return source.NewLocation(p.filePath, start, end)
}

func (p *Parser) lastNonNilToken(fallback tokens.Token) tokens.Token {
	if p.pos == 0 {
		return fallback
	}
	if p.pos-1 < len(p.stream) {
		return p.stream[p.pos-1]
	}
	return fallback
}

func ParseModule(filePath string, stream []tokens.Token, diag *diagnostics.DiagnosticBag) *ast.Module {
	return New(filePath, stream, diag).ParseModule()
}
