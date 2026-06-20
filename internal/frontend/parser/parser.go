// Using Recursive descent parser for statements

package parser

import (
	"fmt"
	"reflect"
	"slices"
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
	context  []string // parsing context stack for error messages
}

const (
	ownerFunction = "function"
	ownerIf       = "if"
	ownerElse     = "else"
)

func New(filePath string, stream []token.Token, diag *diagnostics.DiagnosticBag) *Parser {
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
		Stmts:    make([]ast.Stmt, 0),
	}
	surface := moduleSurface{}

	for !p.at(token.EOF) {
		p.consumeRedundant(token.SEMICOLON, diagnostics.InfoUnnecessarySemicolon, "unnecessary semicolons", "remove these semicolons")
		if p.at(token.IMPORT) {
			if imp := p.parseImport(); imp != nil {
				mod.Imports = append(mod.Imports, imp)
				if raw, ok := ast.ImportPathFromDecl(imp); ok {
					surface.addImport(raw)
				}
			}
			continue
		}

		stmt := p.parseStmt(true)
		switch node := stmt.(type) {
		case nil:
			if !p.at(token.EOF) {
				loc := source.NewLocation(p.filePath, p.current().Start, p.current().End)
				mod.Stmts = append(mod.Stmts, reg(p, &ast.BadStmt{Location: loc}))
				p.synchronize(token.IMPORT, token.FN, token.LET, token.CONST, token.STRUCT,
					token.INTERFACE, token.ENUM, token.IMPL, token.TYPE)
			}
		case ast.Decl:
			if _, ok := node.(*ast.LetDecl); ok {
				p.reportInvalidModuleStmt(ast.LocOf(node), "top-level `let` not allowed", "use `const` for module-scope values")
				continue
			}
			mod.Stmts = append(mod.Stmts, stmt)
			surface.addDecl(node)
		case *ast.BadStmt:
			// parseStmt already diagnosed and recovered enough to continue.
			mod.Stmts = append(mod.Stmts, stmt)
		default:
			p.reportInvalidModuleStmt(ast.LocOf(node), "module scope expects declaration", "move this statement into a function")
		}
	}

	surface.finish(mod)
	return mod
}

func (p *Parser) reportInvalidModuleStmt(loc *source.Location, msg, help string) {
	if loc == nil {
		tok := p.current()
		loc = source.NewLocation(p.filePath, tok.Start, tok.End)
	}
	diag := diagnostics.NewError(msg).
		WithCode(diagnostics.ErrInvalidDeclaration).
		WithPrimaryLabel(loc, msg)
	if help != "" {
		diag = diag.WithHelp(help)
	}
	p.diag.Add(diag)
}

func (p *Parser) parseImport() *ast.ImportDecl {
	start := p.consume(token.IMPORT, "expected import")
	if start == nil {
		return nil
	}

	pathToken := p.consume(token.STRING, "expected import path")
	if pathToken == nil {
		p.synchronize(token.SEMICOLON)
		return nil
	}

	path := ast.StringLit{
		Value:    pathToken.Literal,
		Location: source.NewLocation(p.filePath, pathToken.Start, pathToken.End),
	}

	var alias ast.Ident
	if p.current().Kind != token.SEMICOLON {
		if p.consume(token.AS, "expected 'as' keyword for alias") != nil {
			if tok := p.consume(token.IDENT, "expected alias name"); tok != nil {
				alias = ast.Ident{
					Name:     tok.Literal,
					Location: source.NewLocation(p.filePath, tok.Start, tok.End),
				}
			}
		} else {
			p.synchronize(token.SEMICOLON)
		}
	}

	end := p.consume(token.SEMICOLON, "expected ';'")
	if end == nil {
		endPos := pathToken.End
		if alias.Name != "" && alias.Location != nil && alias.Location.End != nil {
			endPos = *alias.Location.End
		}
		end = &token.Token{Kind: token.SEMICOLON, End: endPos}
	}

	return reg(p, &ast.ImportDecl{
		Path:     &path,
		Alias:    &alias,
		Location: source.NewLocation(p.filePath, start.Start, end.End),
	})
}

func (p *Parser) parseFnDecl() ast.Decl {
	start := p.consume(token.FN, "expected fn")
	if start == nil {
		return nil
	}
	name, typeParams, params, returnType, ok := p.parseFnSignature()
	if name != nil {
		p.pushContext("function '" + name.Name + "'")
		defer p.popContext()
	}
	if !ok {
		// Return partial FnDecl with whatever was parsed
		decl := reg(p, &ast.FnDecl{
			Name:       name,
			TypeParams: typeParams,
			Params:     params,
			ReturnType: returnType,
			Location:   source.NewLocation(p.filePath, start.Start, p.lastNonNilToken(*start).End),
		})
		return setDeclSurface(decl, fnDeclSurface("fn", decl))
	}
	body, isExtern := p.parseFnBody()
	_ = isExtern // consumed by caller if needed
	decl := reg(p, &ast.FnDecl{
		Name:       name,
		TypeParams: typeParams,
		Params:     params,
		ReturnType: returnType,
		Body:       body,
		Location:   source.NewLocation(p.filePath, start.Start, p.lastNonNilToken(*start).End),
	})
	return setDeclSurface(decl, fnDeclSurface("fn", decl))
}

// parseFnSignature parses the name, optional type parameters, parameter list,
// and optional return type of a function. When no arrow is present the
// function has no return value.
func (p *Parser) parseFnSignature() (name *ast.Ident, typeParams []ast.TypeParam, params []ast.Param, returnType ast.TypeExpr, ok bool) {
	name = p.parseFunctionName()
	if name == nil {
		return nil, nil, nil, nil, false
	}
	typeParams = p.parseOptionalTypeParams()
	if p.consume(token.LPAREN, "expected '(' after function name") == nil {
		return nil, nil, nil, nil, false
	}
	lparenPos := p.stream[p.pos-1].Start
	params = p.parseParams()
	_ = p.expectClose(lparenPos, token.RPAREN, "(")
	if p.match(token.ARROW) {
		returnType = p.parseTypeExpr()
		// nil is OK — type-checker validates return types
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
		Location: source.NewLocation(p.filePath, ast.StartOf(first), *end.End),
	})
}

// parseFnBody parses a function body. A semicolon means an extern/forward
// declaration (body=nil, isExtern=true). Otherwise a block is required.
func (p *Parser) parseFnBody() (body *ast.BlockStmt, isExtern bool) {
	if p.match(token.SEMICOLON) {
		return nil, true
	}
	if p.at(token.LBRACE) {
		if b := p.parseBlock(); b != nil {
			return b, false
		}
	}

	prev := p.stream[p.pos-1]
	loc := source.NewLocation(p.filePath, prev.End, prev.End)
	p.diag.Add(diagnostics.NewError("missing function body").WithCode(diagnostics.ErrExpectedToken).WithPrimaryLabel(loc, "expected '{' here"))
	return nil, false
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
	decl := reg(p, &ast.LetDecl{
		Name:        name,
		Type:        ty,
		Value:       value,
		IsMutable:   isMutable,
		IsModuleVar: isModuleVar,
		Location:    source.NewLocation(p.filePath, start.Start, end.End),
	})
	return setDeclSurface(decl, letDeclSurface(decl))
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
	decl := reg(p, &ast.ConstDecl{
		Name:        name,
		Type:        ty,
		Value:       value,
		IsModuleVar: isModuleVar,
		Location:    source.NewLocation(p.filePath, start.Start, end.End),
	})
	return setDeclSurface(decl, constDeclSurface(decl))
}

func (p *Parser) parseBindingFields() (name *ast.Ident, ty ast.TypeExpr, value ast.Expr, end *token.Token, ok bool) {
	name = p.parseIdent()
	if name == nil {
		return nil, nil, nil, nil, false
	}
	if p.match(token.COLON) {
		ty = p.parseTypeExpr()
		// ty may be nil if type parsing failed; continue with name and value
	}
	if p.match(token.ASSIGN) {
		value = p.parseExpr(precLowest)
	}
	end = p.consume(token.SEMICOLON, "expected ';' after statement")
	if end == nil {
		insertPos := ast.EndOf(value)
		if insertPos.IsZero() {
			insertPos = ast.EndOf(ty)
		}
		if insertPos.IsZero() {
			insertPos = ast.EndOf(name)
		}
		end = &token.Token{Kind: token.SEMICOLON, End: insertPos}
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
		p.synchronize(token.RBRACE)
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	fields, end, _ := p.parseStructFields()
	p.match(token.SEMICOLON)
	// Named type declarations keep the same payload node shape as anonymous
	// type syntax so later semantic phases only see one struct-type model.
	decl := reg(p, &ast.StructDecl{
		Name:       name,
		TypeParams: typeParams,
		Type:       &ast.StructType{Fields: fields, Location: source.NewLocation(p.filePath, start.Start, end.End)},
		Location:   source.NewLocation(p.filePath, start.Start, end.End),
	})
	return setDeclSurface(decl, structDeclSurface(decl))
}

func (p *Parser) parseInterfaceDecl() ast.Decl {
	start := p.consume(token.INTERFACE, "expected interface")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		p.synchronize(token.RBRACE)
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	methods, end, _ := p.parseInterfaceMethods()
	p.match(token.SEMICOLON)
	decl := reg(p, &ast.InterfaceDecl{
		Name:       name,
		TypeParams: typeParams,
		Type:       &ast.InterfaceType{Methods: methods, Location: source.NewLocation(p.filePath, start.Start, end.End)},
		Location:   source.NewLocation(p.filePath, start.Start, end.End),
	})
	return setDeclSurface(decl, interfaceDeclSurface(decl))
}

func (p *Parser) parseEnumDecl() ast.Decl {
	start := p.consume(token.ENUM, "expected enum")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		p.synchronize(token.RBRACE)
		return nil
	}
	typeParams := p.parseOptionalTypeParams()
	variants, end, _ := p.parseEnumVariants()
	p.match(token.SEMICOLON)
	decl := reg(p, &ast.EnumDecl{
		Name:       name,
		TypeParams: typeParams,
		Type:       &ast.EnumType{Variants: variants, Location: source.NewLocation(p.filePath, start.Start, end.End)},
		Location:   source.NewLocation(p.filePath, start.Start, end.End),
	})
	return setDeclSurface(decl, enumDeclSurface(decl))
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
	lbracePos := p.stream[p.pos-1].Start
	var methods []*ast.FnDecl
	for !p.at(token.RBRACE) && !p.at(token.EOF) {
		if p.current().Kind == token.HASH {
			p.parseFnAttributes()
		}
		if p.current().Kind != token.FN {
			p.diag.Add(diagnostics.NewError("expected method declaration").WithCode(diagnostics.ErrInvalidDeclaration).WithPrimaryLabel(source.NewLocation(p.filePath, p.stream[p.pos-1].Start, p.stream[p.pos-1].End), fmt.Sprintf("found %s", p.current().Kind)))
			p.synchronize(token.FN, token.RBRACE)
			continue
		}
		decl, ok := p.parseFnDecl().(*ast.FnDecl)
		if !ok || decl == nil {
			p.synchronize(token.FN, token.RBRACE)
			continue
		}
		methods = append(methods, decl)
	}
	end := p.expectClose(lbracePos, token.RBRACE, "{")
	var endPos source.Position
	if end != nil {
		endPos = end.End
	} else if len(methods) > 0 && methods[len(methods)-1].Location != nil && methods[len(methods)-1].Location.End != nil {
		endPos = *methods[len(methods)-1].Location.End
	} else {
		endPos = lbracePos
	}
	p.match(token.SEMICOLON)
	decl := reg(p, &ast.ImplDecl{Target: target, Methods: methods, Location: source.NewLocation(p.filePath, start.Start, endPos)})
	return setDeclSurface(decl, implDeclSurface(decl))
}

func (p *Parser) parseTypeAliasDecl() ast.Decl {
	start := p.consume(token.TYPE, "expected type")
	if start == nil {
		return nil
	}
	name := p.parseIdent()
	if name == nil {
		p.synchronize(token.SEMICOLON)
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
		end = &token.Token{Kind: token.SEMICOLON, End: ast.EndOf(ty)}
	}
	decl := reg(p, &ast.TypeAliasDecl{Name: name, TypeParams: typeParams, Type: ty, Location: source.NewLocation(p.filePath, start.Start, end.End)})
	return setDeclSurface(decl, typeAliasDeclSurface(decl))
}

func (p *Parser) parseFnAttributes() {
	for p.current().Kind == token.HASH {
		p.advance()
		if p.consume(token.LBRACK, "expected '[' after '#'") == nil {
			return
		}
		lbrackPos := p.stream[p.pos-1].Start
		if p.consume(token.IDENT, "expected attribute name") == nil {
			return
		}
		_ = p.expectClose(lbrackPos, token.RBRACK, "[")
	}
}

// --- Statements ---

func (p *Parser) parseStmt(isModuleLevel bool) ast.Stmt {
	if p.at(token.RBRACE) || p.at(token.EOF) {
		return nil
	}

	var doc *ast.CommentGroup
	if p.at(token.DOC_COMMENT) {
		tok := p.advance()
		doc = &ast.CommentGroup{
			Text:     tok.Literal,
			Location: source.NewLocation(p.filePath, tok.Start, tok.End),
		}
	}

	if p.current().Kind == token.HASH {
		p.parseFnAttributes()
	}

	if p.at(token.RBRACE) || p.at(token.EOF) {
		return nil
	}

	var stmt ast.Stmt
	switch p.current().Kind {
	case token.FN:
		stmt, _ = p.parseFnDecl().(ast.Stmt)
	case token.LET:
		stmt, _ = p.parseLetDecl(isModuleLevel).(ast.Stmt)
	case token.CONST:
		stmt, _ = p.parseConstDecl(isModuleLevel).(ast.Stmt)
	case token.STRUCT:
		stmt, _ = p.parseStructDecl().(ast.Stmt)
	case token.INTERFACE:
		stmt, _ = p.parseInterfaceDecl().(ast.Stmt)
	case token.ENUM:
		stmt, _ = p.parseEnumDecl().(ast.Stmt)
	case token.IMPL:
		stmt, _ = p.parseImplDecl().(ast.Stmt)
	case token.TYPE:
		stmt, _ = p.parseTypeAliasDecl().(ast.Stmt)
	case token.LBRACE:
		stmt = p.parseBlock()
	case token.IF:
		stmt = p.parseIfStmt()
	case token.RETURN:
		stmt = p.parseReturnStmt()
	default:
		stmt = p.parseExprStmt()
	}

	if stmt != nil && doc != nil {
		if documented, ok := stmt.(ast.DocumentedNode); ok {
			documented.SetDocComment(doc)
		}
	}
	return stmt
}

func (p *Parser) parseBlock() *ast.BlockStmt {
	start := p.consume(token.LBRACE, "expected '{'")
	if start == nil {
		return nil
	}
	var stmts []ast.Stmt
	for !p.at(token.RBRACE) && !p.at(token.EOF) {
		p.consumeRedundant(token.SEMICOLON, diagnostics.InfoUnnecessarySemicolon, "unnecessary semicolons", "remove these semicolons")
		if stmt := p.parseStmt(false); stmt != nil {
			stmts = append(stmts, stmt)
		} else if !p.at(token.RBRACE) && !p.at(token.EOF) {
			loc := source.NewLocation(p.filePath, p.current().Start, p.current().End)
			stmts = append(stmts, reg(p, &ast.BadStmt{Location: loc}))
			p.synchronize(token.RBRACE)
		}
	}
	end := p.expectClose(start.Start, token.RBRACE, "{")
	var endPos source.Position
	if end != nil {
		endPos = end.End
	} else if len(stmts) > 0 {
		endPos = ast.EndOf(stmts[len(stmts)-1])
	} else {
		endPos = start.End
	}
	return reg(p, &ast.BlockStmt{Stmts: stmts, Location: source.NewLocation(p.filePath, start.Start, endPos)})
}

func (p *Parser) parseIfStmt() ast.Stmt {
	start := p.consume(token.IF, "expected if")
	if start == nil {
		return nil
	}
	cond := p.parseExpr(precLowest)
	if cond == nil {
		cond = reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, start.Start, start.End)})
	}
	var thenBlock *ast.BlockStmt
	if p.at(token.LBRACE) {
		thenBlock = p.parseBlock()
	}
	if thenBlock == nil {
		prev := p.stream[p.pos-1]
		p.diag.Add(diagnostics.NewError("missing if body").WithCode(diagnostics.ErrExpectedToken).WithPrimaryLabel(source.NewLocation(p.filePath, prev.End, prev.End), "expected '{' here"))
		// Return partial IfStmt preserving condition
		return reg(p, &ast.IfStmt{
			Cond:     cond,
			Location: source.NewLocation(p.filePath, start.Start, prev.End),
		})
	}
	endTok := p.lastNonNilToken(*start)
	if ast.LocOf(thenBlock) != nil && ast.LocOf(thenBlock).End != nil {
		endTok.End = *ast.LocOf(thenBlock).End
	}
	var elseStmt ast.Stmt
	if p.match(token.ELSE) {
		elseTok := p.lastNonNilToken(*start)
		if p.at(token.IF) {
			elseStmt = p.parseIfStmt()
		} else {
			var elseBlock *ast.BlockStmt
			if p.at(token.LBRACE) {
				elseBlock = p.parseBlock()
			}
			if elseBlock == nil {
				prev := p.stream[p.pos-1]
				p.diag.Add(diagnostics.NewError("missing else body").WithCode(diagnostics.ErrExpectedToken).WithPrimaryLabel(source.NewLocation(p.filePath, prev.End, prev.End), "expected '{' here"))
				// Return partial IfStmt with then block but no else
				return reg(p, &ast.IfStmt{
					Cond:     cond,
					Then:     thenBlock,
					Location: source.NewLocation(p.filePath, start.Start, prev.End),
				})
			}
			elseStmt = elseBlock
		}
		endTok = p.lastNonNilToken(elseTok)
		if elseStmt != nil && ast.LocOf(elseStmt) != nil && ast.LocOf(elseStmt).End != nil {
			endTok.End = *ast.LocOf(elseStmt).End
		}
	}
	return reg(p, &ast.IfStmt{
		Cond:     cond,
		Then:     thenBlock,
		Else:     elseStmt,
		Location: source.NewLocation(p.filePath, start.Start, endTok.End),
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
		fallbackPos := ast.EndOf(value)
		if fallbackPos.IsZero() {
			fallbackPos = start.End
		}
		end = &token.Token{Kind: token.SEMICOLON, End: fallbackPos}
	}
	return reg(p, &ast.ReturnStmt{Value: value, Location: source.NewLocation(p.filePath, start.Start, end.End)})
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
			fallbackPos := ast.EndOf(value)
			if fallbackPos.IsZero() {
				fallbackPos = ast.EndOf(expr)
			}
			end = &token.Token{Kind: token.SEMICOLON, Start: fallbackPos, End: fallbackPos}
		}
		return reg(p, &ast.AssignStmt{
			Target:   expr,
			Value:    value,
			Location: source.NewLocation(p.filePath, ast.StartOf(expr), end.End),
		})
	}
	end := p.consume(token.SEMICOLON, "expected ';' after expression")
	if end == nil {
		fallbackPos := ast.EndOf(expr)
		end = &token.Token{Kind: token.SEMICOLON, Start: fallbackPos, End: fallbackPos}
	}
	return reg(p, &ast.ExprStmt{
		Expr:     expr,
		Location: source.NewLocation(p.filePath, ast.StartOf(expr), end.End),
	})
}

// --- Types ---

func (p *Parser) parseTypeExpr() ast.TypeExpr {
	tok := p.current()
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
		id := reg(p, &ast.Ident{Name: tok.Literal, Location: source.NewLocation(p.filePath, tok.Start, tok.End)})
		if p.match(token.DCOLON) {
			next := p.current()
			if next.Kind != token.IDENT {
				p.diag.Add(diagnostics.NewError("expected type segment after '::'").WithCode(diagnostics.ErrInvalidTypeInParser).WithPrimaryLabel(source.NewLocation(p.filePath, next.Start, next.End), fmt.Sprintf("found %s", next.Kind)))
				return nil
			}
			p.advance()
			member := reg(p, &ast.Ident{Name: next.Literal, Location: source.NewLocation(p.filePath, next.Start, next.End)})
			return reg(p, &ast.ScopeResolution{
				Module:   id,
				Name:     member,
				Location: source.NewLocation(p.filePath, tok.Start, next.End),
			})
		}
		return reg(p, &ast.NamedType{Name: id.Name, Location: id.Location})
	default:
		loc := source.NewLocation(p.filePath, tok.Start, tok.End)
		d := diagnostics.NewError("expected type").
			WithCode(diagnostics.ErrInvalidTypeInParser).
			WithPrimaryLabel(loc, fmt.Sprintf("found %s", tok.Kind))
		p.diag.Add(d)
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
		Location: source.NewLocation(p.filePath, start.Start, ast.EndOf(target)),
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
	lparenPos := p.stream[p.pos-1].Start
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
	_ = p.expectClose(lparenPos, token.RPAREN, "(")
	var ret ast.TypeExpr
	if p.match(token.ARROW) {
		ret = p.parseTypeExpr()
		if ret == nil {
			return nil
		}
	}
	var endPos source.Position
	if ret != nil {
		endPos = ast.EndOf(ret)
	} else if len(params) > 0 {
		endPos = ast.EndOf(params[len(params)-1])
	} else {
		endPos = start.End
	}
	return reg(p, &ast.FuncType{Params: params, Return: ret, Location: source.NewLocation(p.filePath, start.Start, endPos)})
}

func (p *Parser) parseStructTypeExpr() ast.TypeExpr {
	start := p.consume(token.STRUCT, "expected struct")
	if start == nil {
		return nil
	}
	fields, end, _ := p.parseStructFields()
	return reg(p, &ast.StructType{Fields: fields, Location: source.NewLocation(p.filePath, start.Start, end.End)})
}

func (p *Parser) parseInterfaceTypeExpr() ast.TypeExpr {
	start := p.consume(token.INTERFACE, "expected interface")
	if start == nil {
		return nil
	}
	methods, end, _ := p.parseInterfaceMethods()
	return reg(p, &ast.InterfaceType{Methods: methods, Location: source.NewLocation(p.filePath, start.Start, end.End)})
}

func (p *Parser) parseEnumTypeExpr() ast.TypeExpr {
	start := p.consume(token.ENUM, "expected enum")
	if start == nil {
		return nil
	}
	variants, end, _ := p.parseEnumVariants()
	return reg(p, &ast.EnumType{Variants: variants, Location: source.NewLocation(p.filePath, start.Start, end.End)})
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
			return ast.TypeField{Name: name, Type: ty, Location: source.NewLocation(p.filePath, ast.StartOf(name), ast.EndOf(ty))}, true
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
			lparenPos := p.stream[p.pos-1].Start
			params := p.parseParams()
			_ = p.expectClose(lparenPos, token.RPAREN, "(")
			var ret ast.TypeExpr
			if p.match(token.ARROW) {
				ret = p.parseTypeExpr()
				// nil is OK — type-checker validates return types
			}
			endPos := ast.EndOf(ret)
			if endPos.IsZero() && len(params) > 0 {
				endPos = ast.EndOf(params[len(params)-1].Type)
			}
			if endPos.IsZero() {
				endPos = ast.EndOf(name)
			}
			return ast.TypeMethod{
				Name:       name,
				TypeParams: typeParams,
				Params:     params,
				ReturnType: ret,
				Location:   source.NewLocation(p.filePath, ast.StartOf(name), endPos),
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
	if !p.match(token.LBRACK) { // change to <> later
		return nil
	}
	lbrackPos := p.stream[p.pos-1].Start
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
	_ = p.expectClose(lbrackPos, token.RBRACK, "[")
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
		// ty may be nil if type parsing failed; continue with name
		endPos := ast.EndOf(name)
		if ty != nil {
			endPos = ast.EndOf(ty)
		}
		return ast.Param{Name: name, Type: ty, Location: source.NewLocation(p.filePath, ast.StartOf(name), endPos)}, true
	}
	ty := p.parseTypeExpr()
	if ty == nil {
		return ast.Param{}, false
	}
	return ast.Param{Type: ty, Location: ast.LocOf(ty)}, true
}

// --- Helpers ---

func (p *Parser) synchronize(kinds ...token.Kind) {
	for !p.at(token.EOF) {
		switch p.current().Kind {
		case token.SEMICOLON, token.RBRACE, token.RPAREN, token.RBRACK,
			token.FN, token.LET, token.CONST, token.STRUCT, token.INTERFACE,
			token.ENUM, token.IMPL, token.TYPE, token.IF, token.RETURN, token.IMPORT:
			return
		}
		if slices.Contains(kinds, p.current().Kind) {
			return
		}
		p.advance()
	}
}

func (p *Parser) expectClose(openPos source.Position, kind token.Kind, name string) *token.Token {
	if p.current().Kind == kind {
		return p.advance()
	}
	if p.at(token.EOF) {
		loc := source.NewLocation(p.filePath, openPos, openPos)
		p.diag.Add(diagnostics.NewError(
			fmt.Sprintf("unclosed '%s' — missing '%s'", name, string(kind)),
		).WithCode(diagnostics.ErrUnclosedDelimiter).WithPrimaryLabel(loc, "opened here"))
		return nil
	}
	prev := p.prev()
	loc := source.NewLocation(p.filePath, prev.End, prev.End)
	p.diag.Add(diagnostics.NewError(
		fmt.Sprintf("expected '%s'", string(kind)),
	).WithCode(diagnostics.ErrExpectedToken).WithPrimaryLabel(loc, fmt.Sprintf("add missing `%s` here", string(kind))))
	p.synchronize(kind)
	if p.current().Kind == kind {
		return p.advance()
	}
	return nil
}

func (p *Parser) consumeRedundant(kind token.Kind, code string, msg string, label string) {
	count := 0
	var first, last *token.Token
	for p.at(kind) {
		tok := p.advance()
		count++
		if count == 1 {
			first = tok
		}
		last = tok
	}
	if count > 1 && first != nil && last != nil {
		p.diag.Add(diagnostics.NewInfo(msg).
			WithCode(code).
			WithPrimaryLabel(source.NewLocation(p.filePath, first.Start, last.End), label))
	}
}

func parseBracedItemList[T any](
	p *Parser,
	openerMsg string,
	itemMsg string,
	parseItem func() (T, bool),
) ([]T, *token.Token, bool) {
	lbrace := p.consume(token.LBRACE, openerMsg)
	if lbrace == nil {
		return nil, nil, false
	}
	lbraceStart := lbrace.Start
	var items []T
	for !p.at(token.RBRACE) && !p.at(token.EOF) {
		for p.at(token.DOC_COMMENT) {
			p.advance()
		}
		p.consumeRedundant(token.COMMA, diagnostics.InfoRedundantComma, "unnecessary commas", "remove these commas")
		for p.at(token.DOC_COMMENT) {
			p.advance()
		}
		if p.at(token.RBRACE) {
			break
		}
		item, ok := parseItem()
		if ok {
			items = append(items, item)
		} else {
			p.synchronize(token.COMMA, token.RBRACE)
		}
		if p.at(token.COMMA) {
			if p.next().Kind == token.RBRACE {
				p.diag.Add(diagnostics.NewInfo("trailing comma is unnecessary").
					WithCode(diagnostics.InfoTrailingComma).
					WithPrimaryLabel(source.NewLocation(p.filePath, p.current().Start, p.current().End), "remove this comma"))
			}
			continue
		}
		if !p.at(token.RBRACE) {
			prev := p.stream[p.pos-1]
			p.diag.Add(diagnostics.NewError(itemMsg).WithCode(diagnostics.ErrExpectedToken).WithPrimaryLabel(source.NewLocation(p.filePath, prev.End, prev.End), "add missing `,` here"))
			// Recovery must always consume or skip the unexpected separator token.
			// Without this, inputs like `foo();` inside a braced item list keep
			// reporting the same missing-comma diagnostic forever.
			p.synchronize(token.COMMA, token.RBRACE)
			if !p.at(token.COMMA) && !p.at(token.RBRACE) && !p.at(token.EOF) {
				p.advance()
			}
			continue
		}
	}
	end := p.expectClose(lbraceStart, token.RBRACE, "{")
	var endPos source.Position
	if end != nil {
		endPos = end.End
	} else if len(items) > 0 {
		if n, ok := any(items[len(items)-1]).(ast.Node); ok {
			endPos = ast.EndOf(n)
		} else {
			endPos = lbrace.End
		}
	} else {
		endPos = lbrace.End
	}
	return items, &token.Token{Kind: token.RBRACE, Start: endPos, End: endPos}, true
}

func (p *Parser) consume(kind token.Kind, msg string) *token.Token {
	if p.current().Kind == kind {
		return p.advance()
	}
	prev := p.stream[p.pos-1]
	loc := source.NewLocation(p.filePath, prev.End, prev.End)
	p.diag.Add(diagnostics.NewError(msg).
		WithCode(diagnostics.ErrExpectedToken).
		WithPrimaryLabel(loc, fmt.Sprintf("add missing `%s` here", string(kind))))
	return nil
}

// advances token if matched
func (p *Parser) match(kind token.Kind) bool {
	if p.current().Kind != kind {
		return false
	}
	p.advance()
	return true
}

// returns true if we are at the token
func (p *Parser) at(kind token.Kind) bool {
	return p.current().Kind == kind
}

// returns the current token without advancing
func (p *Parser) current() token.Token {
	if p.pos >= len(p.stream) {
		return token.Token{Kind: token.EOF}
	}
	return p.stream[p.pos]
}

// returns the next token without advancing
func (p *Parser) next() token.Token {
	// next one after current one
	if p.pos+1 >= len(p.stream) {
		return token.Token{Kind: token.EOF}
	}
	return p.stream[p.pos+1]
}

// returns the previous token without advancing
func (p *Parser) prev() token.Token {
	if p.pos-1 < 0 {
		return token.Token{Kind: token.EOF}
	}
	return p.stream[p.pos-1]
}

// advances to the next token and returns it
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

func (p *Parser) pushContext(ctx string) { p.context = append(p.context, ctx) }
func (p *Parser) popContext() {
	if len(p.context) > 0 {
		p.context = p.context[:len(p.context)-1]
	}
}
func (p *Parser) currentContext() string {
	if len(p.context) == 0 {
		return ""
	}
	return p.context[len(p.context)-1]
}

func (p *Parser) lastNonNilToken(fallback token.Token) token.Token {
	if p.pos > 0 && p.pos-1 < len(p.stream) {
		return p.stream[p.pos-1]
	}
	return fallback
}
