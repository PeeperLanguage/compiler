package parser

import (
	"fmt"
	"reflect"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

type Parser struct {
	filePath string
	stream   []token.Token
	diag     *diagnostics.DiagnosticBag
	pos      int
	nodeID   ast.NodeID
}

const (
	ownerFunction = "function"
	ownerIf       = "if"
	ownerElse     = "else"

	voidTypeName = "void"
)

func New(filePath string, stream []token.Token, diag *diagnostics.DiagnosticBag) *Parser {
	return &Parser{
		filePath: filePath,
		stream:   stream,
		diag:     diag,
	}
}

func ParseModule(filePath string, stream []token.Token, diag *diagnostics.DiagnosticBag) *ast.Module {
	return New(filePath, stream, diag).ParseModule()
}

func (p *Parser) ParseModule() *ast.Module {
	mod := &ast.Module{
		FilePath: p.filePath,
		Imports:  make([]*ast.ImportDecl, 0),
		Decls:    make([]ast.Decl, 0),
	}
	for !p.at(token.EOF) {
		if p.at(token.IMPORT) {
			if imp := p.parseImport(); imp != nil {
				mod.Imports = append(mod.Imports, imp)
			}
			continue
		}
		if decl := p.parseDecl(); decl != nil {
			mod.Decls = append(mod.Decls, decl)
			continue
		}
		p.synchronizeDecl()
	}
	return mod
}

// --- Declarations ---

func (p *Parser) parseDecl() ast.Decl {
	if p.peek().Kind == token.HASH {
		p.parseFnAttributes()
	}
	switch p.peek().Kind {
	case token.FN:
		return p.parseFnDecl()
	case token.LET:
		return p.parseLetDecl(true)
	case token.CONST:
		return p.parseConstDecl(true)
	case token.STRUCT:
		return p.parseStructDecl()
	case token.INTERFACE:
		return p.parseInterfaceDecl()
	case token.ENUM:
		return p.parseEnumDecl()
	case token.IMPL:
		return p.parseImplDecl()
	case token.TYPE:
		return p.parseTypeAliasDecl()
	default:
		p.errorf(p.peek(), diagnostics.ErrInvalidDeclaration, "expected declaration")
		return nil
	}
}

func (p *Parser) parseImport() *ast.ImportDecl {
	start := p.consume(token.IMPORT, "expected import")
	if start == nil {
		return nil
	}
	var path ast.Expr
	switch p.peek().Kind {
	case token.STRING:
		tok := p.advance()
		path = reg(p, &ast.StringLit{
			Value:    tok.Literal,
			Location: p.loc(*tok, *tok),
		})
	case token.IDENT:
		path = p.parseIdentExpr()
	default:
		p.errorf(p.peek(), diagnostics.ErrExpectedToken, "expected import path")
		return nil
	}
	var alias *ast.Ident
	if p.match(token.AS) {
		alias = p.parseIdent()
		if alias == nil {
			return nil
		}
	}
	end := p.consume(token.SEMICOLON, "expected ';' after import")
	if end == nil {
		return nil
	}
	return reg(p, &ast.ImportDecl{
		Path:     path,
		Alias:    alias,
		Location: p.loc(*start, *end),
	})
}

func (p *Parser) parseFnDecl() ast.Decl {
	start := p.consume(token.FN, "expected fn")
	name, typeParams, params, returnType, ok := p.parseFnSignature(start)
	if !ok {
		return nil
	}
	body, isExtern, ok := p.parseFnBody()
	if !ok {
		return nil
	}
	_ = isExtern // consumed by caller if needed
	return reg(p, &ast.FnDecl{
		Name:       name,
		TypeParams: typeParams,
		Params:     params,
		ReturnType: returnType,
		Body:       body,
		Location:   p.loc(*start, p.lastNonNilToken(*start)),
	})
}

// parseFnSignature parses the name, optional type parameters, parameter list,
// and optional return type of a function. The default return type when no
// arrow is present is "void".
func (p *Parser) parseFnSignature(start *token.Token) (name *ast.Ident, typeParams []ast.TypeParam, params []ast.Param, returnType ast.TypeExpr, ok bool) {
	returnType = reg(p, &ast.NamedType{
		Name:     voidTypeName,
		Location: source.NewLocation(p.filePath, start.Start, start.End),
	})
	name = p.parseFunctionName()
	if name == nil {
		return nil, nil, nil, nil, false
	}
	typeParams = p.parseOptionalTypeParams()
	if p.consume(token.LPAREN, "expected '(' after function name") == nil {
		return nil, nil, nil, nil, false
	}
	params = p.parseParams()
	if p.consume(token.RPAREN, "expected ')' after parameters") == nil {
		return nil, nil, nil, nil, false
	}
	if p.match(token.ARROW) {
		returnType = p.parseTypeExpr()
		if returnType == nil {
			return nil, nil, nil, nil, false
		}
	}
	return name, typeParams, params, returnType, true
}

func (p *Parser) parseFunctionName() *ast.Ident {
	first := p.parseIdent()
	if first == nil {
		return nil
	}
	parts := []string{first.Name}
	end := first.Location
	for p.match(token.DCOLON) {
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
	return reg(p, &ast.Ident{
		Name:     strings.Join(parts, "::"),
		Location: p.locFromNode(first, reg(p, &ast.BadExpr{Location: end})),
	})
}

// parseFnBody parses a function body. A semicolon means an extern/forward
// declaration (body=nil, isExtern=true). Otherwise a block is required.
func (p *Parser) parseFnBody() (body *ast.BlockStmt, isExtern bool, ok bool) {
	if p.match(token.SEMICOLON) {
		return nil, true, true
	}
	body, ok = p.parseRequiredBlock(ownerFunction)
	return body, false, ok
}

func (p *Parser) parseLetDecl(isModuleVar bool) ast.Decl {
	start := p.consume(token.LET, "expected let")
	if start == nil {
		return nil
	}
	isMutable := p.match(token.MUT)
	name, ty, value, end, ok := p.parseBindingFields()
	if !ok {
		return nil
	}
	return reg(p, &ast.LetDecl{
		Name:        name,
		Type:        ty,
		Value:       value,
		IsMutable:   isMutable,
		IsModuleVar: isModuleVar,
		Location:    p.loc(*start, *end),
	})
}

func (p *Parser) parseConstDecl(isModuleVar bool) ast.Decl {
	start := p.consume(token.CONST, "expected const")
	if start == nil {
		return nil
	}
	name, ty, value, end, ok := p.parseBindingFields()
	if !ok {
		return nil
	}
	return reg(p, &ast.ConstDecl{
		Name:        name,
		Type:        ty,
		Value:       value,
		IsModuleVar: isModuleVar,
		Location:    p.loc(*start, *end),
	})
}

func (p *Parser) parseBindingFields() (name *ast.Ident, ty ast.TypeExpr, value ast.Expr, end *token.Token, ok bool) {
	name = p.parseIdent()
	if name == nil {
		return nil, nil, nil, nil, false
	}
	if p.match(token.COLON) {
		ty = p.parseTypeExpr()
		if ty == nil {
			return nil, nil, nil, nil, false
		}
	}
	if p.match(token.ASSIGN) {
		value = p.parseExpr(precLowest)
	}
	if p.at(token.SEMICOLON) {
		end = p.advance()
		return name, ty, value, end, true
	}
	// missing semicolon — attempt recovery
	insertPos := endLocation(value)
	if isZeroPosition(insertPos) {
		insertPos = endLocation(ty)
	}
	if isZeroPosition(insertPos) {
		insertPos = endLocation(name)
	}
	end = p.recoverSemicolon("after statement", insertPos)
	if end == nil {
		return nil, nil, nil, nil, false
	}
	return name, ty, value, end, true
}

func (p *Parser) parseStructDecl() ast.Decl {
	start := p.consume(token.STRUCT, "expected struct")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	fields, end, ok := p.parseStructFields()
	if !ok {
		return nil
	}
	p.match(token.SEMICOLON)
	return reg(p, &ast.StructDecl{Name: name, TypeParams: typeParams, Fields: fields, Location: p.loc(*start, *end)})
}

func (p *Parser) parseInterfaceDecl() ast.Decl {
	start := p.consume(token.INTERFACE, "expected interface")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	methods, end, ok := p.parseInterfaceMethods()
	if !ok {
		return nil
	}
	p.match(token.SEMICOLON)
	return reg(p, &ast.InterfaceDecl{Name: name, TypeParams: typeParams, Methods: methods, Location: p.loc(*start, *end)})
}

func (p *Parser) parseEnumDecl() ast.Decl {
	start := p.consume(token.ENUM, "expected enum")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	variants, end, ok := p.parseEnumVariants()
	if !ok {
		return nil
	}
	p.match(token.SEMICOLON)
	return reg(p, &ast.EnumDecl{Name: name, TypeParams: typeParams, Variants: variants, Location: p.loc(*start, *end)})
}

func (p *Parser) parseImplDecl() ast.Decl {
	start := p.consume(token.IMPL, "expected impl")
	if start == nil {
		return nil
	}
	target := p.parseTypeExpr()
	if target == nil {
		return nil
	}
	if p.consume(token.LBRACE, "expected '{' after impl target") == nil {
		return nil
	}
	var methods []*ast.FnDecl
	for !p.at(token.RBRACE) && !p.at(token.EOF) {
		if p.peek().Kind == token.HASH {
			p.parseFnAttributes()
		}
		if p.peek().Kind != token.FN {
			p.errorf(p.peek(), diagnostics.ErrInvalidDeclaration, "expected method declaration")
			return nil
		}
		decl, ok := p.parseFnDecl().(*ast.FnDecl)
		if !ok || decl == nil {
			return nil
		}
		methods = append(methods, decl)
	}
	end := p.consume(token.RBRACE, "expected '}' after impl block")
	if end == nil {
		return nil
	}
	p.match(token.SEMICOLON)
	return reg(p, &ast.ImplDecl{Target: target, Methods: methods, Location: p.loc(*start, *end)})
}

func (p *Parser) parseTypeAliasDecl() ast.Decl {
	start := p.consume(token.TYPE, "expected type")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	_ = p.match(token.ASSIGN)
	ty := p.parseTypeExpr()
	if ty == nil {
		return nil
	}
	end := p.consume(token.SEMICOLON, "expected ';' after type declaration")
	if end == nil {
		return nil
	}
	return reg(p, &ast.TypeAliasDecl{Name: name, TypeParams: typeParams, Type: ty, Location: p.loc(*start, *end)})
}

func (p *Parser) parseFnAttributes() {
	for p.peek().Kind == token.HASH {
		p.advance()
		if p.consume(token.LBRACK, "expected '[' after '#'") == nil {
			return
		}
		if p.consume(token.IDENT, "expected attribute name") == nil {
			return
		}
		if p.consume(token.RBRACK, "expected ']' after attribute") == nil {
			return
		}
	}
}

// --- Statements ---

func (p *Parser) parseStmt() ast.Stmt {
	switch p.peek().Kind {
	case token.LBRACE:
		return p.parseBlock()
	case token.LET:
		stmt, _ := p.parseLetDecl(false).(ast.Stmt)
		return stmt
	case token.CONST:
		stmt, _ := p.parseConstDecl(false).(ast.Stmt)
		return stmt
	case token.IF:
		return p.parseIfStmt()
	case token.RETURN:
		return p.parseReturnStmt()
	default:
		return p.parseExprStmt()
	}
}

func (p *Parser) parseBlock() ast.Stmt {
	start := p.consume(token.LBRACE, "expected '{'")
	if start == nil {
		return nil
	}
	var stmts []ast.Stmt
	for !p.at(token.RBRACE) && !p.at(token.EOF) {
		if stmt := p.parseStmt(); stmt != nil {
			stmts = append(stmts, stmt)
		} else {
			p.synchronizeStmt()
		}
	}
	end := p.consume(token.RBRACE, "expected '}'")
	if end == nil {
		return nil
	}
	return reg(p, &ast.BlockStmt{Stmts: stmts, Location: p.loc(*start, *end)})
}

func (p *Parser) parseIfStmt() ast.Stmt {
	start := p.consume(token.IF, "expected if")
	if start == nil {
		return nil
	}
	cond := p.parseExpr(precLowest)
	if cond == nil {
		return nil
	}
	thenBlock, ok := p.parseRequiredBlock(ownerIf)
	if !ok {
		return nil
	}
	endTok := p.lastTokenOfStmt(thenBlock, p.lastNonNilToken(*start))
	var elseStmt ast.Stmt
	if p.match(token.ELSE) {
		elseTok := p.lastNonNilToken(*start)
		if p.at(token.IF) {
			elseStmt = p.parseIfStmt()
		} else {
			elseBlock, ok := p.parseRequiredBlock(ownerElse)
			if !ok {
				return nil
			}
			elseStmt = elseBlock
		}
		endTok = p.lastTokenOfStmt(elseStmt, elseTok)
	}
	return reg(p, &ast.IfStmt{
		Cond:     cond,
		Then:     thenBlock,
		Else:     elseStmt,
		Location: p.loc(*start, endTok),
	})
}

func (p *Parser) parseReturnStmt() ast.Stmt {
	start := p.consume(token.RETURN, "expected return")
	if start == nil {
		return nil
	}
	var value ast.Expr
	if !p.at(token.SEMICOLON) {
		value = p.parseExpr(precLowest)
	}
	end := p.consume(token.SEMICOLON, "expected ';' after return")
	if end == nil {
		if end = p.recoverSemicolon("after return", endLocation(value)); end == nil {
			return nil
		}
	}
	return reg(p, &ast.ReturnStmt{Value: value, Location: p.loc(*start, *end)})
}

func (p *Parser) parseExprStmt() ast.Stmt {
	expr := p.parseExpr(precLowest)
	if expr == nil {
		return nil
	}
	if p.match(token.ASSIGN) {
		value := p.parseExpr(precLowest)
		if value == nil {
			return nil
		}
		end := p.consume(token.SEMICOLON, "expected ';' after assignment")
		if end == nil {
			if end = p.recoverSemicolon("after assignment", endLocation(value)); end == nil {
				return nil
			}
		}
		return reg(p, &ast.AssignStmt{
			Target:   expr,
			Value:    value,
			Location: p.locFromNode(expr, reg(p, &ast.BadExpr{Location: p.loc(*end, *end)})),
		})
	}
	end := p.consume(token.SEMICOLON, "expected ';' after expression")
	if end == nil {
		if end = p.recoverSemicolon("after expression", endLocation(expr)); end == nil {
			return nil
		}
	}
	return reg(p, &ast.ExprStmt{
		Expr:     expr,
		Location: p.locFromNode(expr, reg(p, &ast.BadExpr{Location: p.loc(*end, *end)})),
	})
}

// --- Types ---

func (p *Parser) parseTypeExpr() ast.TypeExpr {
	tok := p.peek()
	switch tok.Kind {
	case token.CARET:
		return p.parseCaretPtrTypeExpr()
	case token.FN:
		return p.parseFuncTypeExpr()
	case token.STRUCT:
		return p.parseStructTypeExpr()
	case token.INTERFACE:
		return p.parseInterfaceTypeExpr()
	case token.ENUM:
		return p.parseEnumTypeExpr()
	case token.IDENT:
		p.advance()
		id := reg(p, &ast.Ident{Name: tok.Literal, Location: p.loc(tok, tok)})
		if p.match(token.DCOLON) {
			next := p.peek()
			if next.Kind != token.IDENT {
				p.errorf(next, diagnostics.ErrInvalidType, "expected type segment after '::'")
				return nil
			}
			p.advance()
			member := reg(p, &ast.Ident{Name: next.Literal, Location: p.loc(next, next)})
			return reg(p, &ast.ScopeResolution{
				Module:   id,
				Name:     member,
				Location: p.loc(tok, next),
			})
		}
		return reg(p, &ast.NamedType{Name: id.Name, Location: id.Location})
	default:
		p.errorf(tok, diagnostics.ErrInvalidType, "expected type")
		return nil
	}
}

func (p *Parser) parseCaretPtrTypeExpr() ast.TypeExpr {
	start := p.consume(token.CARET, "expected '^' in pointer type")
	if start == nil {
		return nil
	}
	target := p.parseTypeExpr()
	if target == nil {
		return nil
	}
	return reg(p, &ast.RawPtrType{
		Mutable:  true,
		Target:   target,
		Location: p.locFromNode(reg(p, &ast.BadExpr{Location: p.loc(*start, *start)}), target),
	})
}

func (p *Parser) parseFuncTypeExpr() ast.TypeExpr {
	start := p.consume(token.FN, "expected fn in function type")
	if start == nil {
		return nil
	}
	if p.consume(token.LPAREN, "expected '(' after fn in function type") == nil {
		return nil
	}
	var params []ast.TypeExpr
	if !p.at(token.RPAREN) {
		for {
			param := p.parseTypeExpr()
			if param == nil {
				return nil
			}
			params = append(params, param)
			if !p.match(token.COMMA) {
				break
			}
		}
	}
	if p.consume(token.RPAREN, "expected ')' after function type parameters") == nil {
		return nil
	}
	ret := ast.TypeExpr(reg(p, &ast.NamedType{
		Name:     voidTypeName,
		Location: source.NewLocation(p.filePath, start.Start, start.End),
	}))
	if p.match(token.COLON) {
		ret = p.parseTypeExpr()
		if ret == nil {
			return nil
		}
	}
	return reg(p, &ast.FuncType{Params: params, Return: ret, Location: p.loc(*start, p.lastNonNilToken(*start))})
}

func (p *Parser) parseStructTypeExpr() ast.TypeExpr {
	start := p.consume(token.STRUCT, "expected struct")
	if start == nil {
		return nil
	}
	fields, end, ok := p.parseStructFields()
	if !ok {
		return nil
	}
	return reg(p, &ast.StructType{Fields: fields, Location: p.loc(*start, *end)})
}

func (p *Parser) parseInterfaceTypeExpr() ast.TypeExpr {
	start := p.consume(token.INTERFACE, "expected interface")
	if start == nil {
		return nil
	}
	methods, end, ok := p.parseInterfaceMethods()
	if !ok {
		return nil
	}
	return reg(p, &ast.InterfaceType{Methods: methods, Location: p.loc(*start, *end)})
}

func (p *Parser) parseEnumTypeExpr() ast.TypeExpr {
	start := p.consume(token.ENUM, "expected enum")
	if start == nil {
		return nil
	}
	variants, end, ok := p.parseEnumVariants()
	if !ok {
		return nil
	}
	return reg(p, &ast.EnumType{Variants: variants, Location: p.loc(*start, *end)})
}

// --- Shared type body parsers ---

func (p *Parser) parseStructFields() ([]ast.TypeField, *token.Token, bool) {
	return parseBracedItemList(p, "expected '{' after struct", "expected '}' after struct fields",
		func() (ast.TypeField, bool) {
			name := p.parseIdent()
			if name == nil {
				return ast.TypeField{}, false
			}
			if p.consume(token.COLON, "expected ':' after field name") == nil {
				return ast.TypeField{}, false
			}
			ty := p.parseTypeExpr()
			if ty == nil {
				return ast.TypeField{}, false
			}
			return ast.TypeField{Name: name, Type: ty, Location: p.locFromNode(name, ty)}, true
		})
}

func (p *Parser) parseInterfaceMethods() ([]ast.TypeMethod, *token.Token, bool) {
	return parseBracedItemList(p, "expected '{' after interface", "expected '}' after interface methods",
		func() (ast.TypeMethod, bool) {
			name := p.parseIdent()
			if name == nil {
				return ast.TypeMethod{}, false
			}
			typeParams := p.parseOptionalTypeParams()
			if p.consume(token.LPAREN, "expected '(' after method name") == nil {
				return ast.TypeMethod{}, false
			}
			params := p.parseParams()
			if p.consume(token.RPAREN, "expected ')' after method parameters") == nil {
				return ast.TypeMethod{}, false
			}
			ret := ast.TypeExpr(reg(p, &ast.NamedType{Name: voidTypeName, Location: name.Location}))
			if p.match(token.COLON) {
				ret = p.parseTypeExpr()
				if ret == nil {
					return ast.TypeMethod{}, false
				}
			}
			return ast.TypeMethod{
				Name:       name,
				TypeParams: typeParams,
				Params:     params,
				ReturnType: ret,
				Location:   p.locFromNode(name, ret),
			}, true
		})
}

func (p *Parser) parseEnumVariants() ([]ast.EnumVariant, *token.Token, bool) {
	return parseBracedItemList(p, "expected '{' after enum", "expected '}' after enum variants",
		func() (ast.EnumVariant, bool) {
			v := p.parseIdent()
			if v == nil {
				return ast.EnumVariant{}, false
			}
			return ast.EnumVariant{Name: v, Location: v.Location}, true
		})
}

// --- Params ---

func (p *Parser) parseOptionalTypeParams() []ast.TypeParam {
	if !p.match(token.LBRACK) {
		return nil
	}
	var params []ast.TypeParam
	for {
		name := p.parseIdent()
		if name == nil {
			break
		}
		params = append(params, ast.TypeParam{Name: name, Location: name.Location})
		if !p.match(token.COMMA) {
			break
		}
	}
	_ = p.consume(token.RBRACK, "expected ']' after type parameters")
	return params
}

func (p *Parser) parseParams() []ast.Param {
	var params []ast.Param
	if p.at(token.RPAREN) {
		return params
	}
	for {
		param, ok := p.parseParam()
		if !ok {
			break
		}
		params = append(params, param)
		if !p.match(token.COMMA) {
			break
		}
	}
	return params
}

func (p *Parser) parseParam() (ast.Param, bool) {
	if p.at(token.IDENT) && p.pos+1 < len(p.stream) && p.stream[p.pos+1].Kind == token.COLON {
		name := p.parseIdent()
		if name == nil {
			return ast.Param{}, false
		}
		if p.consume(token.COLON, "expected ':' after parameter name") == nil {
			return ast.Param{}, false
		}
		ty := p.parseTypeExpr()
		if ty == nil {
			return ast.Param{}, false
		}
		return ast.Param{Name: name, Type: ty, Location: p.locFromNode(name, ty)}, true
	}
	ty := p.parseTypeExpr()
	if ty == nil {
		return ast.Param{}, false
	}
	return ast.Param{Type: ty, Location: ty.Loc()}, true
}

// --- Helpers ---

func (p *Parser) parseRequiredBlock(owner string) (*ast.BlockStmt, bool) {
	if p.at(token.LBRACE) {
		if body := p.parseBlock(); body != nil {
			return body.(*ast.BlockStmt), true
		}
		return nil, false
	}
	if !p.isDeclStart(p.peek().Kind) && !p.isStmtBoundary(p.peek().Kind) {
		return nil, false
	}
	insert := p.expectedInsertionPoint()
	loc := source.NewLocation(p.filePath, insert, insert)
	p.diag.Add(
		diagnostics.NewError("missing "+owner+" body").
			WithCode(diagnostics.ErrExpectedToken).
			WithPrimaryLabel(loc, "add missing `{}` here"),
	)
	return reg(p, &ast.BlockStmt{
		Stmts:    nil,
		Location: loc,
	}), true
}

func parseBracedItemList[T any](
	p *Parser,
	openerMsg string,
	itemMsg string,
	parseItem func() (T, bool),
) ([]T, *token.Token, bool) {
	if p.consume(token.LBRACE, openerMsg) == nil {
		return nil, nil, false
	}
	var items []T
	for !p.at(token.RBRACE) && !p.at(token.EOF) {
		item, ok := parseItem()
		if !ok {
			return nil, nil, false
		}
		items = append(items, item)
		if p.match(token.COMMA) {
			if p.at(token.RBRACE) {
				break
			}
			continue
		}
		if !p.at(token.RBRACE) {
			p.errorf(p.peek(), diagnostics.ErrExpectedToken, itemMsg)
			return nil, nil, false
		}
	}
	end := p.consume(token.RBRACE, itemMsg)
	if end == nil {
		return nil, nil, false
	}
	return items, end, true
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

func (p *Parser) recoverMissingToken(expected token.Kind, msg string, fallback source.Position) *token.Token {
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
	return &token.Token{
		Kind:    expected,
		Literal: string(expected),
		Start:   insert,
		End:     insert,
	}
}

// recoverSemicolon synthesizes a missing ';' when at a statement/decl boundary.
// Returns nil if recovery is not possible.
func (p *Parser) recoverSemicolon(context string, fallback source.Position) *token.Token {
	insert := p.expectedInsertionPoint()
	if !isZeroPosition(fallback) {
		insert = fallback
	}
	if !p.isStmtBoundary(p.peek().Kind) && !p.isDeclStart(p.peek().Kind) {
		return nil
	}
	return p.recoverMissingToken(token.SEMICOLON, "expected ';' "+context, insert)
}

func (p *Parser) isDeclStart(kind token.Kind) bool {
	switch kind {
	case token.IMPORT, token.FN, token.LET, token.CONST, token.STRUCT,
		token.INTERFACE, token.ENUM, token.IMPL, token.TYPE, token.EOF:
		return true
	default:
		return false
	}
}

func (p *Parser) isStmtBoundary(kind token.Kind) bool {
	switch kind {
	case token.RBRACE, token.SEMICOLON, token.LET, token.CONST, token.IF,
		token.ELSE, token.RETURN, token.FN, token.IMPORT, token.STRUCT,
		token.INTERFACE, token.ENUM, token.IMPL, token.EOF:
		return true
	default:
		return false
	}
}

func (p *Parser) synchronizeDecl() {
	for !p.at(token.EOF) {
		if p.match(token.SEMICOLON) {
			return
		}
		switch p.peek().Kind {
		case token.IMPORT, token.FN, token.LET, token.CONST, token.STRUCT,
			token.INTERFACE, token.ENUM, token.IMPL, token.TYPE:
			return
		}
		p.advance()
	}
}

func (p *Parser) synchronizeStmt() {
	for !p.at(token.EOF) && !p.at(token.RBRACE) {
		if p.match(token.SEMICOLON) {
			return
		}
		if p.isStmtBoundary(p.peek().Kind) || p.isDeclStart(p.peek().Kind) {
			return
		}
		p.advance()
	}
}

func (p *Parser) lastTokenOfStmt(stmt ast.Stmt, fallback token.Token) token.Token {
	if stmt == nil {
		return fallback
	}
	loc := stmt.Loc()
	if loc.End == nil {
		return fallback
	}
	for i := p.pos - 1; i >= 0; i-- {
		if p.stream[i].End.Index == loc.End.Index {
			return p.stream[i]
		}
	}
	return fallback
}

func (p *Parser) consume(kind token.Kind, msg string) *token.Token {
	if p.peek().Kind == kind {
		return p.advance()
	}
	p.errorf(p.peek(), diagnostics.ErrExpectedToken, msg)
	return nil
}

func (p *Parser) match(kind token.Kind) bool {
	if p.peek().Kind != kind {
		return false
	}
	p.advance()
	return true
}

func (p *Parser) at(kind token.Kind) bool {
	return p.peek().Kind == kind
}

func (p *Parser) peek() token.Token {
	if p.pos >= len(p.stream) {
		return token.Token{Kind: token.EOF}
	}
	return p.stream[p.pos]
}

func (p *Parser) advance() *token.Token {
	if p.pos >= len(p.stream) {
		return nil
	}
	tok := p.stream[p.pos]
	p.pos++
	return &tok
}

func (p *Parser) nextID() ast.NodeID {
	p.nodeID++
	return p.nodeID
}

// isNilNode handles the Go interface nil trap: a non-nil interface holding a
// nil pointer is not equal to nil, so a plain `n == nil` check is insufficient.
func isNilNode(n ast.Node) bool {
	if n == nil {
		return true
	}
	v := reflect.ValueOf(n)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

func reg[T ast.Node](p *Parser, n T) T {
	if !isNilNode(n) {
		n.SetID(p.nextID())
	}
	return n
}

func (p *Parser) errorf(tok token.Token, code, msg string) {
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

func (p *Parser) loc(start, end token.Token) *source.Location {
	return source.NewLocation(p.filePath, start.Start, end.End)
}

func (p *Parser) locFromNode(left, right ast.Node) *source.Location {
	l, r := left.Loc(), right.Loc()
	start, end := source.NewPosition(), source.NewPosition()
	if l.Start != nil {
		start = *l.Start
	}
	if r.End != nil {
		end = *r.End
	}
	return source.NewLocation(p.filePath, start, end)
}

func (p *Parser) lastNonNilToken(fallback token.Token) token.Token {
	if p.pos > 0 && p.pos-1 < len(p.stream) {
		return p.stream[p.pos-1]
	}
	return fallback
}

func endLocation(n ast.Node) source.Position {
	if n == nil {
		return source.NewPosition()
	}
	loc := n.Loc()
	if loc == nil || loc.End == nil {
		return source.NewPosition()
	}
	return *loc.End
}

func isZeroPosition(pos source.Position) bool {
	return pos == source.NewPosition()
}