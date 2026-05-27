package parser

import (
	"fmt"
	"strings"

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

type blockOwner string

const (
	blockOwnerFunction blockOwner = "function"
)

type functionLikeSig struct {
	Receiver   *ast.Param
	Name       *ast.Ident
	TypeParams []ast.TypeParam
	Params     []ast.Param
	ReturnType ast.TypeExpr
}

func (p *Parser) expectedInsertionPoint() source.Position {
	tok := p.peek()
	insert := tok.Start
	if p.pos > 0 {
		prev := p.stream[p.pos-1]
		if prev.End.Index <= tok.Start.Index {
			insert = prev.End
		}
	}
	return insert
}

func (p *Parser) isDeclStart(kind tokens.Kind) bool {
	switch kind {
	case tokens.IMPORT, tokens.FN, tokens.LET, tokens.CONST, tokens.TYPE, tokens.EOF:
		return true
	default:
		return false
	}
}

func (p *Parser) isStmtBoundary(kind tokens.Kind) bool {
	switch kind {
	case tokens.RBRACE, tokens.SEMICOLON, tokens.LET, tokens.CONST, tokens.IF, tokens.ELSE, tokens.RETURN, tokens.FN, tokens.IMPORT, tokens.EOF:
		return true
	default:
		return false
	}
}

func (p *Parser) recoverMissingToken(expected tokens.Kind, msg string, fallback source.Position) *tokens.Token {
	insert := p.expectedInsertionPoint()
	if fallback.Line > 0 {
		insert = fallback
	}
	loc := source.NewLocation(p.filePath, insert, insert)
	p.diag.Add(
		diagnostics.NewError(msg).
			WithCode(diagnostics.ErrExpectedToken).
			WithPrimaryLabel(loc, fmt.Sprintf("add missing `%s` here", string(expected))),
	)
	synth := tokens.Token{
		Kind:    expected,
		Literal: string(expected),
		Start:   insert,
		End:     insert,
	}
	return &synth
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
	if p.peek().Kind == tokens.HASH {
		p.parseFnAttributes()
	}
	switch p.peek().Kind {
	case tokens.FN:
		return p.parseFnDecl()
	case tokens.LET:
		return p.parseLetDecl(true)
	case tokens.CONST:
		return p.parseConstDecl(true)
	case tokens.TYPE:
		return p.parseTypeAliasDecl()
	default:
		p.errorf(p.peek(), diagnostics.ErrInvalidDeclaration, "expected declaration")
		return nil
	}
}

func (p *Parser) parseFnAttributes() {
	for p.peek().Kind == tokens.HASH {
		p.advance()
		if p.consume(tokens.LBRACK, "expected '[' after '#'") == nil {
			return
		}
		if p.consume(tokens.IDENT, "expected attribute name") == nil {
			return
		}
		if p.consume(tokens.RBRACK, "expected ']' after attribute") == nil {
			return
		}
	}
}

func (p *Parser) parseFnDecl() ast.Decl {
	start := p.consume(tokens.FN, "expected fn")
	sig, ok := p.parseFunctionLikeSig(start)
	if !ok {
		return nil
	}
	body, _, ok := p.parseFnBody()
	if !ok {
		return nil
	}
	return &ast.FnDecl{
		Receiver:   sig.Receiver,
		Name:       sig.Name,
		TypeParams: sig.TypeParams,
		Params:     sig.Params,
		ReturnType: sig.ReturnType,
		Body:       body,
		Location:   p.loc(*start, p.lastNonNilToken(*start)),
	}
}

func (p *Parser) parseFunctionLikeSig(start *tokens.Token) (functionLikeSig, bool) {
	sig := functionLikeSig{
		ReturnType: ast.TypeExpr(&ast.NamedType{Name: "i32", Location: p.loc(*start, *start)}),
	}
	sig.Receiver = p.parseOptionalReceiver()
	sig.Name = p.parseFunctionName()
	if sig.Name == nil {
		return functionLikeSig{}, false
	}
	sig.TypeParams = p.parseOptionalTypeParams()
	if p.consume(tokens.LPAREN, "expected '(' after function name") == nil {
		return functionLikeSig{}, false
	}
	sig.Params = p.parseParams()
	if p.consume(tokens.RPAREN, "expected ')' after parameters") == nil {
		return functionLikeSig{}, false
	}
	if p.match(tokens.ARROW) {
		sig.ReturnType = p.parseTypeExpr()
		if sig.ReturnType == nil {
			return functionLikeSig{}, false
		}
	}
	return sig, true
}

func (p *Parser) parseFunctionName() *ast.Ident {
	first := p.parseIdent()
	if first == nil {
		return nil
	}
	parts := []string{first.Name}
	end := first.Location
	for p.match(tokens.DCOLON) {
		next := p.parseIdent()
		if next == nil {
			return nil
		}
		parts = append(parts, next.Name)
		end = next.Location
	}
	if len(parts) == 1 {
		return first
	}
	return &ast.Ident{
		Name:     strings.Join(parts, "::"),
		Location: p.locFromNode(first, &ast.BadExpr{Location: end}),
	}
}

func (p *Parser) parseFnBody() (*ast.BlockStmt, bool, bool) {
	if p.match(tokens.SEMICOLON) {
		return nil, true, true
	}
	body, ok := p.parseRequiredBlock(blockOwnerFunction)
	if !ok {
		return nil, false, false
	}
	return body, false, true
}

func (p *Parser) parseRequiredBlock(owner blockOwner) (*ast.BlockStmt, bool) {
	if p.at(tokens.LBRACE) {
		body := p.parseBlock()
		if body == nil {
			return nil, false
		}
		return body, true
	}
	if !p.isDeclStart(p.peek().Kind) && !p.isStmtBoundary(p.peek().Kind) {
		return nil, false
	}
	insert := p.expectedInsertionPoint()
	loc := source.NewLocation(p.filePath, insert, insert)
	p.diag.Add(
		diagnostics.NewError("missing "+string(owner)+" body").
			WithCode(diagnostics.ErrExpectedToken).
			WithPrimaryLabel(loc, "add missing `{}` here"),
	)
	empty := source.NewLocation(p.filePath, insert, insert)
	return &ast.BlockStmt{
		Stmts:    make([]ast.Stmt, 0),
		Location: empty,
	}, true
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
	ty := p.parseTypeExpr()
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
		ty := p.parseTypeExpr()
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
		ty = p.parseTypeExpr()
		if ty == nil {
			return nil, nil, nil, nil, false
		}
	}
	
	if p.match(tokens.ASSIGN) {
	 	value = p.parseExpr(0)
	} else if token == tokens.CONST {
		peek := p.peek()
		diag := diagnostics.NewError("Missing initializer for const declaration")
		diag.WithPrimaryLabel(p.loc(peek, peek), "add initial value here")
		p.diag.Add(diag)
	}

	if p.peek().Kind != tokens.SEMICOLON {
		loc := value.Loc()
		insertPos := source.NewPosition()
		if loc.End != nil {
			insertPos = *loc.End
		}
		if p.isStmtBoundary(p.peek().Kind) || p.isDeclStart(p.peek().Kind) {
			end = p.recoverMissingToken(tokens.SEMICOLON, "expected ';' after statement", insertPos)
			return name, ty, value, end, true
		}
		p.recoverMissingToken(tokens.SEMICOLON, "expected ';' after statement", insertPos)
		return nil, nil, nil, nil, false
	}
	end = p.advance()
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
		// TODO: Add proper handling
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
	case tokens.LBRACE:
		return p.parseBlock()
	case tokens.LET:
		return p.parseLetDecl(false)
	case tokens.CONST:
		return p.parseConstDecl(false)
	case tokens.IF:
		return p.parseIfStmt()
	case tokens.RETURN:
		return p.parseReturnStmt()
	default:
		return p.parseExprStmt()
	}
}

func (p *Parser) parseIfStmt() ast.Stmt {
	start := p.consume(tokens.IF, "expected if")
	if start == nil {
		return nil
	}
	cond := p.parseExpr(0)
	if cond == nil {
		return nil
	}
	thenBlock, ok := p.parseRequiredBlock(blockOwner("if"))
	if !ok {
		return nil
	}
	endTok := p.lastTokenOfStmt(thenBlock, p.lastNonNilToken(*start))
	var elseStmt ast.Stmt
	if p.match(tokens.ELSE) {
		elseTok := p.lastNonNilToken(*start)
		if p.at(tokens.IF) {
			elseStmt = p.parseIfStmt()
		} else {
			elseBlock, ok := p.parseRequiredBlock(blockOwner("else"))
			if !ok {
				return nil
			}
			elseStmt = elseBlock
		}
		endTok = p.lastTokenOfStmt(elseStmt, elseTok)
	}
	return &ast.IfStmt{
		Cond:     cond,
		Then:     thenBlock,
		Else:     elseStmt,
		Location: p.loc(*start, endTok),
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
		insert := p.expectedInsertionPoint()
		if value != nil && value.Loc().End != nil {
			insert = *value.Loc().End
		}
		if p.isStmtBoundary(p.peek().Kind) || p.isDeclStart(p.peek().Kind) {
			end = p.recoverMissingToken(tokens.SEMICOLON, "expected ';' after return", insert)
		} else {
			return nil
		}
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
		insert := p.expectedInsertionPoint()
		if expr.Loc().End != nil {
			insert = *expr.Loc().End
		}
		if p.isStmtBoundary(p.peek().Kind) || p.isDeclStart(p.peek().Kind) {
			end = p.recoverMissingToken(tokens.SEMICOLON, "expected ';' after expression", insert)
		} else {
			return nil
		}
	}
	return &ast.ExprStmt{
		Expr:     expr,
		Location: p.locFromNode(expr, &ast.BadExpr{Location: p.loc(*end, *end)}),
	}
}

func (p *Parser) lastTokenOfStmt(stmt ast.Stmt, fallback tokens.Token) tokens.Token {
	if stmt == nil {
		return fallback
	}
	loc := stmt.Loc()
	if loc.End == nil {
		return fallback
	}
	for i := p.pos - 1; i >= 0; i-- {
		tok := p.stream[i]
		if tok.End.Index == loc.End.Index {
			return tok
		}
	}
	return fallback
}

func (p *Parser) parseTypeExpr() ast.TypeExpr {
	tok := p.peek()
	switch tok.Kind {
	case tokens.FN:
		return p.parseFuncTypeExpr()
	case tokens.STRUCT:
		return p.parseStructTypeExpr()
	case tokens.INTERFACE:
		return p.parseInterfaceTypeExpr()
	case tokens.ENUM:
		return p.parseEnumTypeExpr()
	case tokens.IDENT:
		p.advance()
		var name strings.Builder
		name.WriteString(tok.Literal)
		end := tok
		for p.match(tokens.DCOLON) {
			next := p.peek()
			if next.Kind != tokens.IDENT {
				p.errorf(next, diagnostics.ErrInvalidType, "expected type segment after '::'")
				return nil
			}
			p.advance()
			name.WriteString("::")
			name.WriteString(next.Literal)
			end = next
		}
		return &ast.NamedType{Name: name.String(), Location: p.loc(tok, end)}
	default:
		p.errorf(tok, diagnostics.ErrInvalidType, "expected type")
		return nil
	}
}

func (p *Parser) parseFuncTypeExpr() ast.TypeExpr {
	start := p.consume(tokens.FN, "expected fn in function type")
	if start == nil {
		return nil
	}
	if p.consume(tokens.LPAREN, "expected '(' after fn in function type") == nil {
		return nil
	}
	params := make([]ast.TypeExpr, 0)
	if !p.at(tokens.RPAREN) {
		for {
			param := p.parseTypeExpr()
			if param == nil {
				return nil
			}
			params = append(params, param)
			if !p.match(tokens.COMMA) {
				break
			}
		}
	}
	if p.consume(tokens.RPAREN, "expected ')' after function type parameters") == nil {
		return nil
	}
	ret := ast.TypeExpr(&ast.NamedType{Name: "void", Location: p.loc(*start, *start)})
	if p.match(tokens.COLON) {
		ret = p.parseTypeExpr()
		if ret == nil {
			return nil
		}
	}
	return &ast.FuncType{Params: params, Return: ret, Location: p.loc(*start, p.lastNonNilToken(*start))}
}

func (p *Parser) parseTypeAliasDecl() ast.Decl {
	start := p.consume(tokens.TYPE, "expected type")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	_ = p.match(tokens.ASSIGN)
	ty := p.parseTypeExpr()
	if ty == nil {
		return nil
	}
	end := p.consume(tokens.SEMICOLON, "expected ';' after type declaration")
	if end == nil {
		return nil
	}
	return &ast.TypeAliasDecl{Name: name, TypeParams: typeParams, Type: ty, Location: p.loc(*start, *end)}
}

func (p *Parser) parseStructTypeExpr() ast.TypeExpr {
	start := p.consume(tokens.STRUCT, "expected struct")
	if start == nil {
		return nil
	}
	if p.consume(tokens.LBRACE, "expected '{' after struct") == nil {
		return nil
	}
	fields := make([]ast.TypeField, 0)
	for !p.at(tokens.RBRACE) && !p.at(tokens.EOF) {
		fieldName := p.parseIdent()
		if fieldName == nil {
			return nil
		}
		if p.consume(tokens.COLON, "expected ':' after field name") == nil {
			return nil
		}
		fieldType := p.parseTypeExpr()
		if fieldType == nil {
			return nil
		}
		if p.consume(tokens.SEMICOLON, "expected ';' after struct field") == nil {
			return nil
		}
		fields = append(fields, ast.TypeField{Name: fieldName, Type: fieldType, Location: p.locFromNode(fieldName, fieldType)})
	}
	end := p.consume(tokens.RBRACE, "expected '}' after struct fields")
	if end == nil {
		return nil
	}
	return &ast.StructType{Fields: fields, Location: p.loc(*start, *end)}
}

func (p *Parser) parseInterfaceTypeExpr() ast.TypeExpr {
	start := p.consume(tokens.INTERFACE, "expected interface")
	if start == nil {
		return nil
	}
	if p.consume(tokens.LBRACE, "expected '{' after interface") == nil {
		return nil
	}
	methods := make([]ast.TypeMethod, 0)
	for !p.at(tokens.RBRACE) && !p.at(tokens.EOF) {
		methodName := p.parseIdent()
		if methodName == nil {
			return nil
		}
		methodTypeParams := p.parseOptionalTypeParams()
		if p.consume(tokens.LPAREN, "expected '(' after method name") == nil {
			return nil
		}
		params := p.parseParams()
		if p.consume(tokens.RPAREN, "expected ')' after method parameters") == nil {
			return nil
		}
		ret := ast.TypeExpr(&ast.NamedType{Name: "void", Location: methodName.Location})
		if p.match(tokens.COLON) {
			ret = p.parseTypeExpr()
			if ret == nil {
				return nil
			}
		}
		if p.consume(tokens.SEMICOLON, "expected ';' after interface method") == nil {
			return nil
		}
		methods = append(methods, ast.TypeMethod{Name: methodName, TypeParams: methodTypeParams, Params: params, ReturnType: ret, Location: p.locFromNode(methodName, ret)})
	}
	end := p.consume(tokens.RBRACE, "expected '}' after interface methods")
	if end == nil {
		return nil
	}
	return &ast.InterfaceType{Methods: methods, Location: p.loc(*start, *end)}
}

func (p *Parser) parseEnumTypeExpr() ast.TypeExpr {
	start := p.consume(tokens.ENUM, "expected enum")
	if start == nil {
		return nil
	}
	if p.consume(tokens.LBRACE, "expected '{' after enum") == nil {
		return nil
	}
	variants := make([]ast.EnumVariant, 0)
	for !p.at(tokens.RBRACE) && !p.at(tokens.EOF) {
		v := p.parseIdent()
		if v == nil {
			return nil
		}
		variants = append(variants, ast.EnumVariant{Name: v, Location: v.Location})
		if p.match(tokens.COMMA) {
			continue
		}
		if p.at(tokens.RBRACE) {
			break
		}
		if p.consume(tokens.SEMICOLON, "expected ',' or ';' after enum variant") == nil {
			return nil
		}
	}
	end := p.consume(tokens.RBRACE, "expected '}' after enum variants")
	if end == nil {
		return nil
	}
	return &ast.EnumType{Variants: variants, Location: p.loc(*start, *end)}
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

func (p *Parser) eat() {
	if p.pos >= len(p.stream) {
		return
	}
	p.pos++
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
		case tokens.IMPORT, tokens.FN, tokens.LET, tokens.CONST, tokens.TYPE:
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
		if p.isStmtBoundary(p.peek().Kind) || p.isDeclStart(p.peek().Kind) {
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
			WithPrimaryLabel(loc, fmt.Sprintf("found %s", tok.Kind)),
	)
}

func (p *Parser) loc(start, end tokens.Token) *source.Location {
	return source.NewLocation(p.filePath, start.Start, end.End)
}

func (p *Parser) locFromNode(left, right ast.Node) *source.Location {
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
