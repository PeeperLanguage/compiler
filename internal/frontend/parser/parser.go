// Parser entry point and shared recursive-descent state.
//
// Statement and type parsing use recursive descent because grammar structure
// determines which parser routine runs next. Expression parsing is delegated
// to parse_expr.go, where Pratt precedence handles operators and postfix
// expressions. Both styles share this Parser's token stream, diagnostics,
// source locations, recovery, and AST node registration.

package parser

import (
	"fmt"
	"slices"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
	"compiler/pkg/typednil"
)

type Parser struct {
	filePath string
	stream   []token.Token
	diag     *diagnostics.DiagnosticBag
	pos      int
	nodeID   uint64
}

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
		p.consumeRedundant(token.SEMICOLON, diagnostics.InfoUnnecessarySemicolon, "semicolon", false)
		if p.at(token.IMPORT) {
			if imp := p.parseImport(); imp != nil {
				mod.Imports = append(mod.Imports, imp)
				if raw, ok := ast.ImportPathFromDecl(imp); ok {
					surface.addImport(raw)
				}
			}
			continue
		}

		before := p.pos
		stmt := p.parseStmt(true)
		switch node := stmt.(type) {
		case nil:
			if !p.at(token.EOF) {
				loc := source.NewLocation(p.filePath, p.current().Start, p.current().End)
				p.reportInvalidModuleStmt(loc, "module scope expects declaration", "remove unexpected token")
				mod.Stmts = append(mod.Stmts, reg(p, &ast.BadStmt{Location: loc}))
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
		if p.pos == before && !p.at(token.EOF) {
			p.synchronize(token.IMPORT, token.FN, token.LET, token.CONST, token.STRUCT,
				token.IFACE, token.ENUM, token.TYPE)
			if p.pos == before && !p.at(token.EOF) {
				p.advance()
			}
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
		diag = diag.Help(help)
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
	var receiver *ast.Param
	if p.at(token.LPAREN) {
		receiver = p.parseReceiver()
	}
	name, typeParams, params, returnType, returnOrigins, ok := p.parseFnSignature()
	if !ok {
		// Return partial FnDecl with whatever was parsed
		decl := reg(p, &ast.FnDecl{
			Name:          name,
			Receiver:      receiver,
			TypeParams:    typeParams,
			Params:        params,
			ReturnType:    returnType,
			ReturnOrigins: returnOrigins,
			Location:      source.NewLocation(p.filePath, start.Start, p.lastNonNilToken(*start).End),
		})
		return setDeclSurface(decl, fnDeclSurface("fn", decl))
	}
	body := p.parseFnBody()
	decl := reg(p, &ast.FnDecl{
		Name:          name,
		Receiver:      receiver,
		TypeParams:    typeParams,
		Params:        params,
		ReturnType:    returnType,
		ReturnOrigins: returnOrigins,
		Body:          body,
		Location:      source.NewLocation(p.filePath, start.Start, p.lastNonNilToken(*start).End),
	})
	return setDeclSurface(decl, fnDeclSurface("fn", decl))
}

func (p *Parser) parseReceiver() *ast.Param {
	start := p.consume(token.LPAREN, "expected '(' before receiver")
	if start == nil {
		return nil
	}
	receiver, ok := p.parseParam()
	p.expectClose(start.Start, token.RPAREN, "(")
	if !ok {
		return nil
	}
	return &receiver
}

// parseFnSignature parses the name, optional type parameters, parameter list,
// and optional return type of a function. When no arrow is present the
// function has no return value.
func (p *Parser) parseFnSignature() (name *ast.Ident, typeParams []ast.TypeParam, params []ast.Param, returnType ast.TypeExpr, returnOrigins *ast.ReturnOriginClause, ok bool) {
	name = p.parseFunctionName()
	if name == nil {
		return nil, nil, nil, nil, nil, false
	}
	typeParams = p.parseOptionalTypeParams()
	if p.consume(token.LPAREN, "expected '(' after function name") == nil {
		return nil, nil, nil, nil, nil, false
	}
	lparenPos := p.stream[p.pos-1].Start
	params = p.parseParams()
	p.expectClose(lparenPos, token.RPAREN, "(")
	returnType = p.parseReturnType()
	returnOrigins = p.parseReturnOriginClause()
	return name, typeParams, params, returnType, returnOrigins, true
}

// parseReturnType reads `-> Type` after a parameter list; nil means no return
// value, or a type that failed to parse and was reported. A type written on
// that line without its arrow is reported and read, so the body still parses.
func (p *Parser) parseReturnType() ast.TypeExpr {
	if p.match(token.ARROW) {
		return p.parseTypeExpr()
	}
	if isTypeStart(p.current().Kind) && p.isOnLineOfPrev() {
		p.diag.Add(insertionError("expected '->' before the return type", p.prev().End, p.filePath, "->", " ->"))
		return p.parseTypeExpr()
	}
	return nil
}

func (p *Parser) parseReturnOriginClause() *ast.ReturnOriginClause {
	if !p.at(token.FROM) {
		return nil
	}
	start := p.advance()
	sources := make([]*ast.Ident, 0, 1)
	end := start.End
	if p.match(token.LPAREN) {
		var missingCommas []diagnostics.CodeFix
		for !p.at(token.RPAREN) && !p.at(token.EOF) {
			sourceName := p.parseIdent()
			if sourceName == nil {
				break
			}
			sources = append(sources, sourceName)
			end = ast.EndOf(sourceName)
			if !p.listContinues(p.at(token.IDENT), &missingCommas) {
				break
			}
		}
		p.reportMissingCommas(missingCommas)
		if close := p.consume(token.RPAREN, "expected ')' after reference return origins"); close != nil {
			end = close.End
		}
	} else if sourceName := p.parseIdent(); sourceName != nil {
		sources = append(sources, sourceName)
		end = ast.EndOf(sourceName)
	}
	return &ast.ReturnOriginClause{Sources: sources, Location: source.NewLocation(p.filePath, start.Start, end)}
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

// parseFnBody parses a function body. A trailing semicolon means an
// extern/forward declaration, so the body remains nil.
func (p *Parser) parseFnBody() *ast.BlockStmt {
	if p.match(token.SEMICOLON) {
		return nil
	}
	if p.at(token.LBRACE) {
		if b := p.parseBlock(); b != nil {
			return b
		}
	}

	prev := p.stream[p.pos-1]
	loc := source.NewLocation(p.filePath, prev.End, prev.End)
	p.diag.Add(diagnostics.NewError("missing function body").WithCode(diagnostics.ErrExpectedToken).WithPrimaryLabel(loc, "expected '{' here"))
	return nil
}

func (p *Parser) parseLetDecl(isModuleVar bool) ast.Decl {
	start := p.consume(token.LET, "expected let")
	if start == nil {
		return nil
	}
	var mutableLocation *source.Location
	if p.at(token.MUT) {
		modifier := p.advance()
		mutableLocation = source.NewLocation(p.filePath, modifier.Start, modifier.End)
	}
	name, ty, value, end, ok := p.parseBindingFields()
	if !ok {
		return nil
	}
	decl := reg(p, &ast.LetDecl{
		Name:            name,
		Type:            ty,
		Value:           value,
		IsMutable:       mutableLocation != nil,
		MutableLocation: mutableLocation,
		IsModuleVar:     isModuleVar,
		Location:        source.NewLocation(p.filePath, start.Start, end.End),
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
	} else if p.isLoneNameAfterBindingName() {
		// `let a b;`: `b` reads as the type or as the value, and only what it
		// names can tell. Both repairs are shown, and the binding keeps an
		// unknown value so later uses of it stay quiet.
		at := source.NewLocation(p.filePath, p.prev().End, p.prev().End)
		word := p.advance()
		p.diag.Add(diagnostics.NewError("expected ':' or '=' after the name").
			WithCode(diagnostics.ErrExpectedToken).
			WithPrimaryLabel(at, "").
			HelpWithChoices(at,
				diagnostics.Choice{If: "`" + word.Literal + "` is a type", Insert: ":"},
				diagnostics.Choice{If: "`" + word.Literal + "` is a value", Insert: " ="}))
		value = reg(p, &ast.BadExpr{Location: source.NewLocation(p.filePath, word.Start, word.End)})
	} else if p.isTypeWithoutColon() {
		// `let a i32 = 1;`: reading the type here keeps the rest of the
		// statement from producing errors of its own.
		p.diag.Add(insertionError("expected ':' before the type", p.prev().End, p.filePath, ":", ":"))
		ty = p.parseTypeExpr()
	}
	if p.match(token.ASSIGN) {
		value = p.parseExpr(precLowest, token.SEMICOLON)
	} else if p.startsValue() && p.isOnLineOfPrev() && !p.hasBeforeStatementEnd(token.ASSIGN) {
		// `let a 5;`: a value on the same line with no `=` anywhere before the
		// statement ends. Reading the value keeps the binding usable. The fix
		// is only shown when the statement ends right after that value;
		// otherwise more than the `=` is wrong.
		at := p.prev().End
		value = p.parseExpr(precLowest, token.SEMICOLON)
		if p.at(token.SEMICOLON) || !p.isOnLineOfPrev() {
			p.diag.Add(insertionError("expected '=' before the value", at, p.filePath, "=", " ="))
		} else {
			p.diag.Add(diagnostics.NewError("expected '=' before the value").
				WithCode(diagnostics.ErrExpectedToken).
				WithPrimaryLabel(source.NewLocation(p.filePath, at, at), "add missing `=` here"))
		}
	}
	end = p.consume(token.SEMICOLON, "expected ';' after statement")
	if end == nil {
		// The statement ends where its last token does.
		end = &token.Token{Kind: token.SEMICOLON, End: p.prev().End}
	}
	return name, ty, value, end, true
}

// isLoneNameAfterBindingName reports whether the binding name is followed on
// its line by one more name that ends the statement or the line.
func (p *Parser) isLoneNameAfterBindingName() bool {
	if !p.isOnLineOfPrev() || !p.at(token.IDENT) {
		return false
	}
	next := p.next()
	return next.Kind == token.SEMICOLON || next.Kind == token.EOF || next.Start.Line > p.current().End.Line
}

// isTypeWithoutColon reports whether the tokens after a binding name are its
// type with the colon left out. They must begin a type, and then one of these
// holds: an `=` still follows in the statement (`let a &T = x;`); no value
// can begin that way (`let a ?T;`); or it is an array type with no `{` after
// it, which an array literal would need (`let a [3]T;`).
func (p *Parser) isTypeWithoutColon() bool {
	if !p.isOnLineOfPrev() || !isTypeStart(p.current().Kind) {
		return false
	}
	return p.hasBeforeStatementEnd(token.ASSIGN) || !p.startsValue() ||
		p.at(token.LBRACK) && !p.hasBeforeStatementEnd(token.LBRACE)
}

// isOnLineOfPrev reports whether the current token sits on the line where the
// previous one ended.
func (p *Parser) isOnLineOfPrev() bool {
	return !p.at(token.EOF) && p.current().Start.Line == p.prev().End.Line
}

// hasBeforeStatementEnd looks ahead on the current line for a token of kind
// outside brackets before the statement's `;`.
func (p *Parser) hasBeforeStatementEnd(kind token.Kind) bool {
	depth := 0
	for i := p.pos; i < len(p.stream) && p.stream[i].Start.Line == p.current().Start.Line; i++ {
		if depth <= 0 && p.stream[i].Kind == kind {
			return true
		}
		switch p.stream[i].Kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.SEMICOLON:
			if depth <= 0 {
				return false
			}
		}
	}
	return false
}

// insertionError is the diagnostic for a token known to be missing at a point:
// an empty marker there, and a help line whose fix inserts text. shown is the
// token as named in the help; text may carry the spacing the insertion needs.
func insertionError(msg string, at source.Position, filePath, shown, text string) *diagnostics.Diagnostic {
	loc := source.NewLocation(filePath, at, at)
	return diagnostics.NewError(msg).
		WithCode(diagnostics.ErrExpectedToken).
		WithPrimaryLabel(loc, "").
		Help("add missing `"+shown+"`", diagnostics.Fix.Insert(loc, text))
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
	fields, end, _ := p.parseTypeFields("expected '{' after struct", "expected '}' after struct fields")
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
	start := p.consume(token.IFACE, "expected iface")
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
	p.match(token.ASSIGN)
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

func (p *Parser) parseAttributes() []ast.Attribute {
	var attrs []ast.Attribute
	for p.current().Kind == token.HASH {
		hash := p.advance()
		if p.consume(token.LBRACK, "expected '[' after '#'") == nil {
			return attrs
		}
		lbrackPos := p.stream[p.pos-1].Start
		nameTok := p.current()
		if nameTok.Kind != token.IDENT && !token.IsKeyword(nameTok.Literal) {
			p.diag.Add(diagnostics.NewError("expected attribute name").
				WithCode(diagnostics.ErrMissingIdentifier).
				WithPrimaryLabel(source.NewLocation(p.filePath, nameTok.Start, nameTok.End), fmt.Sprintf("found %s", nameTok.Kind)))
			p.synchronize(token.RBRACK)
			p.expectClose(lbrackPos, token.RBRACK, "[")
			return attrs
		}
		p.advance()
		var (
			args    []ast.Expr
			attrEnd = nameTok.End
		)
		if p.match(token.LPAREN) {
			var missingCommas []diagnostics.CodeFix
			for !p.at(token.RPAREN) && !p.at(token.EOF) {
				arg := p.parseExpr(precLowest, token.RPAREN)
				if arg != nil {
					args = append(args, arg)
				}
				if !p.listContinues(arg != nil && p.startsArgument(), &missingCommas) {
					break
				}
			}
			p.reportMissingCommas(missingCommas)
			if end := p.consume(token.RPAREN, "expected ')' after attribute arguments"); end != nil {
				attrEnd = end.End
			} else if len(args) > 0 {
				attrEnd = ast.EndOf(args[len(args)-1])
			}
		}
		attrs = append(attrs, ast.Attribute{
			Name:     nameTok.Literal,
			Args:     args,
			Location: source.NewLocation(p.filePath, hash.Start, attrEnd),
		})
		if p.at(token.COMMA) {
			tok := p.advance()
			p.diag.Add(diagnostics.NewError("only one attribute is allowed per `#[...]` block").
				WithCode(diagnostics.ErrExpectedToken).
				WithPrimaryLabel(source.NewLocation(p.filePath, tok.Start, tok.End), "split attributes into separate `#[...]` blocks"))
			p.synchronize(token.RBRACK)
		}
		end := p.expectClose(lbrackPos, token.RBRACK, "[")
		if end != nil && len(attrs) > 0 && attrs[len(attrs)-1].Location != nil {
			attrs[len(attrs)-1].Location.End = &end.End
		}
	}
	return attrs
}

// parseLeadingMetadata keeps comment attachment in parser instead of lexer so
// node spans remain syntax-only. Leading `///` comments merge onto next
// statement or declaration, even across blank lines or skipped normal comments.
// Attributes are transparent. Same-line trailing comments never attach to the
// following item.
func (p *Parser) parseLeadingMetadata() (*ast.CommentGroup, []ast.Attribute) {
	var (
		doc   *ast.CommentGroup
		attrs []ast.Attribute
	)
	mergeComment := func(tok *token.Token) {
		if tok == nil {
			return
		}
		next := &ast.CommentGroup{
			Text:     tok.Literal,
			Location: source.NewLocation(p.filePath, tok.Start, tok.End),
		}
		if doc == nil {
			doc = next
			return
		}
		doc.Text += "\n" + next.Text
		if doc.Location == nil {
			doc.Location = next.Location
			return
		}
		if next.Location != nil && next.Location.End != nil {
			doc.Location.End = next.Location.End
		}
	}
	for {
		switch p.current().Kind {
		case token.DOC_COMMENT:
			tok := p.advance()
			if p.pos > 1 {
				prev := p.stream[p.pos-2]
				if prev.End.Line == tok.Start.Line && prev.Kind != token.DOC_COMMENT {
					doc = nil
					continue
				}
			}
			mergeComment(tok)
		case token.HASH:
			prevCount := len(attrs)
			attrs = append(attrs, p.parseAttributes()...)
			if len(attrs) == prevCount {
				continue
			}
		default:
			return doc, attrs
		}
	}
}
func (p *Parser) synchronize(kinds ...token.Kind) {
	for !p.at(token.EOF) {
		switch p.current().Kind {
		case token.SEMICOLON, token.RBRACE, token.RPAREN, token.RBRACK,
			token.FN, token.LET, token.CONST, token.STRUCT, token.IFACE,
			token.ENUM, token.TYPE, token.IF, token.RETURN, token.IMPORT:
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
			fmt.Sprintf("unclosed '%s' - missing '%s'", name, string(kind)),
		).WithCode(diagnostics.ErrUnclosedDelimiter).WithPrimaryLabel(loc, "opened here"))
		return nil
	}
	prev := p.prev()
	loc := source.NewLocation(p.filePath, prev.End, prev.End)
	p.diag.Add(p.withOpener(p.missingTokenError(fmt.Sprintf("expected '%s'", string(kind)), kind, loc), openPos, name))
	if p.isMissingTokenLikely(kind, loc) {
		// The closer belongs right here, so the tokens after it are not part
		// of the bracket; skipping ahead to find one would drop them.
		return nil
	}
	p.synchronize(kind)
	if p.current().Kind == kind {
		return p.advance()
	}
	return nil
}

// withOpener marks the bracket a missing closer belongs to, when it was opened
// on a line above the one the diagnostic points at. On the same line the
// opener is in view already; further up it is the part of the mistake the
// reader cannot see.
func (p *Parser) withOpener(d *diagnostics.Diagnostic, openPos source.Position, name string) *diagnostics.Diagnostic {
	if openPos.Line >= p.prev().End.Line {
		return d
	}
	openEnd := openPos
	openEnd.Column++
	return d.WithSecondaryLabel(source.NewLocation(p.filePath, openPos, openEnd), "this `"+name+"` is still open")
}

// consumeRedundant skips a run of kind, named noun in messages, and reports it
// when it holds more than one token. isFirstNeeded says the run starts with a
// token the grammar wants there, such as the comma after a list item, so the
// advice and the fix leave that one alone.
func (p *Parser) consumeRedundant(kind token.Kind, code string, noun string, isFirstNeeded bool) {
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
		loc := source.NewLocation(p.filePath, first.Start, last.End)
		removed, advice := loc, "remove these "+noun+"s"
		if isFirstNeeded {
			removed = source.NewLocation(p.filePath, first.End, last.End)
			advice = "remove the extra " + noun
			if count > 2 {
				advice += "s"
			}
		}
		d := diagnostics.NewInfo("unnecessary " + noun + "s").WithCode(code)
		// A fixed line can only be shown for a run that sits on one line; a
		// longer run keeps the advice beside the marker.
		if first.Start.Line == last.End.Line {
			d.WithPrimaryLabel(loc, "").Help(advice, diagnostics.Fix.Remove(removed))
		} else {
			d.WithPrimaryLabel(loc, advice)
		}
		p.diag.Add(d)
	}
}

func parseBracedItemList[T any](
	p *Parser,
	openerMsg string,
	itemMsg string,
	isExpression bool,
	parseItem func() (T, bool),
) ([]T, *token.Token, bool) {
	lbrace := p.consume(token.LBRACE, openerMsg)
	if lbrace == nil {
		endPos := p.prev().End
		return nil, &token.Token{Kind: token.RBRACE, Start: endPos, End: endPos}, false
	}
	lbraceStart := lbrace.Start
	var items []T
	// The comma that follows an item is consumed with any extra ones at the top
	// of the next round, so only a run before the first item is all redundant.
	isAfterItem := false
	isClosedEarly := false
	var wrongSeparators, missingCommas []diagnostics.CodeFix
	for !p.at(token.RBRACE) && !p.at(token.EOF) {
		for p.at(token.DOC_COMMENT) {
			p.advance()
		}
		p.consumeRedundant(token.COMMA, diagnostics.InfoRedundantComma, "comma", isAfterItem)
		for p.at(token.DOC_COMMENT) {
			p.advance()
		}
		if p.at(token.RBRACE) {
			break
		}
		item, ok := parseItem()
		isAfterItem = true
		if ok {
			items = append(items, item)
		} else {
			p.synchronize(token.COMMA, token.RBRACE)
		}
		if p.at(token.COMMA) {
			if p.next().Kind == token.RBRACE {
				p.reportTrailingComma(p.current())
			}
			continue
		}
		if p.at(token.SEMICOLON) {
			if !isExpression || p.isSemicolonForComma() {
				// `.{ x = 1; y = 2 }`: the `;` stands where a `,` belongs. One
				// before the closing brace separates nothing and is removed.
				loc := source.NewLocation(p.filePath, p.current().Start, p.current().End)
				if p.next().Kind == token.RBRACE {
					wrongSeparators = append(wrongSeparators, diagnostics.Fix.Remove(loc))
				} else {
					wrongSeparators = append(wrongSeparators, diagnostics.Fix.Replace(loc, ","))
				}
				p.advance()
				continue
			}
			// `[3]i32{1, 2, 3;`: the statement ended where the list should
			// have closed. Leaving the `;` for the statement keeps the rest of
			// the block from being read as values.
			p.diag.Add(p.withOpener(insertionError(itemMsg, p.prev().End, p.filePath, "}", "}"), lbraceStart, "{"))
			isClosedEarly = true
			break
		}
		if p.at(token.RPAREN) || p.at(token.RBRACK) {
			// `f(.{ x = 1 );`: the closer of a bracket around the list can
			// only follow the list's own `}`.
			p.diag.Add(p.withOpener(insertionError(itemMsg, p.prev().End, p.filePath, "}", "}"), lbraceStart, "{"))
			isClosedEarly = true
			break
		}
		// A token that begins an item right after a good one means only the
		// comma is missing. On a later line of a value list it may instead
		// begin the statement after a list left unclosed, so there the old
		// report stays. After a failed item the skip below must run, or a
		// token no item accepts would be offered again without end.
		startsItem := ok && (p.at(token.IDENT) || p.at(token.FN) || isExpression && p.startsValue())
		if p.listContinues(startsItem && (p.isOnLineOfPrev() || !isExpression), &missingCommas) {
			continue
		}
		if !p.at(token.RBRACE) {
			prev := p.prev()
			p.diag.Add(p.missingTokenError("expected ','", token.COMMA, source.NewLocation(p.filePath, prev.End, prev.End)))
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
	p.reportMissingCommas(missingCommas)
	if len(wrongSeparators) > 0 {
		p.diag.Add(diagnostics.NewError("expected ',' between items, found ';'").
			WithCode(diagnostics.ErrExpectedToken).
			WithPrimaryLabel(wrongSeparators[0].Location, "").
			Help("separate items with `,`", wrongSeparators...))
	}
	var end *token.Token
	if !isClosedEarly {
		end = p.expectClose(lbraceStart, token.RBRACE, "{")
	}
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
	p.diag.Add(p.missingTokenError(msg, kind, loc))
	return nil
}

// missingTokenError reports a token that should follow loc. Where the guess
// is reliable the diagnostic shows the line with the token inserted. Where a
// separator or closer is expected but the guess is not reliable, it points at
// the token found instead and proposes nothing: the real mistake is often a
// different token between the two pieces of code on that line. Any other kind
// has one place it can go, so the diagnostic says to add it there.
func (p *Parser) missingTokenError(msg string, kind token.Kind, loc *source.Location) *diagnostics.Diagnostic {
	if p.isMissingTokenLikely(kind, loc) {
		return insertionError(msg, *loc.Start, p.filePath, string(kind), string(kind))
	}
	d := diagnostics.NewError(msg).WithCode(diagnostics.ErrExpectedToken)
	switch kind {
	case token.SEMICOLON, token.COMMA, token.RPAREN, token.RBRACK, token.RBRACE, token.GT:
		found := p.current()
		return d.WithPrimaryLabel(source.NewLocation(p.filePath, found.Start, found.End), "found `"+found.Literal+"`")
	}
	return d.WithPrimaryLabel(loc, fmt.Sprintf("add missing `%s` here", string(kind)))
}

// isMissingTokenLikely reports whether inserting kind at loc is very probably
// what the author meant, judged by the unexpected token that follows. A
// separator is trusted when the line ends there; a statement also ends before
// a closing brace; a closing delimiter is also trusted before another closing
// delimiter, or before a semicolon that itself ends the line. Nothing is
// trusted right after a semicolon or an opening delimiter, where the parser
// has already skipped past the real mistake. Kinds that name a category, such
// as an identifier, cannot be inserted as text at all.
func (p *Parser) isMissingTokenLikely(kind token.Kind, loc *source.Location) bool {
	switch p.prev().Kind {
	case token.SEMICOLON, token.LPAREN, token.LBRACK, token.LBRACE:
		return false
	}
	next := p.current()
	endsLine := func(tok token.Token) bool { return tok.Kind == token.EOF || tok.Start.Line > loc.Start.Line }
	isCloser := func(k token.Kind) bool {
		return k == token.RPAREN || k == token.RBRACK || k == token.RBRACE || k == token.GT
	}
	switch {
	case kind == token.SEMICOLON:
		return endsLine(next) || next.Kind == token.RBRACE
	case kind == token.COMMA:
		return endsLine(next)
	case isCloser(kind):
		after := p.next()
		// An arrow only follows the `)` of a parameter list.
		return endsLine(next) || isCloser(next.Kind) || kind == token.RPAREN && next.Kind == token.ARROW ||
			next.Kind == token.SEMICOLON && (endsLine(after) || isCloser(after.Kind))
	}
	return false
}

// listContinues reports whether another list item follows the one just read:
// either a comma was there, or startsItem says the next token begins an item,
// which means the comma between the two is missing. Each missing comma is
// added to missing; reportMissingCommas turns them into one diagnostic once
// the list is read, so `f(1 2 3)` shows a single line with both commas.
func (p *Parser) listContinues(startsItem bool, missing *[]diagnostics.CodeFix) bool {
	if p.match(token.COMMA) {
		// A comma right before the closer separates nothing.
		if p.at(token.RPAREN) || p.at(token.GT) {
			p.reportTrailingComma(p.prev())
			return false
		}
		return true
	}
	if !startsItem {
		return false
	}
	at := p.prev().End
	*missing = append(*missing, diagnostics.Fix.Insert(source.NewLocation(p.filePath, at, at), ","))
	return true
}

func (p *Parser) reportTrailingComma(comma token.Token) {
	loc := source.NewLocation(p.filePath, comma.Start, comma.End)
	p.diag.Add(diagnostics.NewInfo("trailing comma is unnecessary").
		WithCode(diagnostics.InfoTrailingComma).
		WithPrimaryLabel(loc, "").
		Help("remove this comma", diagnostics.Fix.Remove(loc)))
}

func (p *Parser) reportMissingCommas(missing []diagnostics.CodeFix) {
	if len(missing) == 0 {
		return
	}
	help := "add missing `,`"
	if len(missing) > 1 {
		help = "add the missing commas"
	}
	p.diag.Add(diagnostics.NewError("expected ','").
		WithCode(diagnostics.ErrExpectedToken).
		WithPrimaryLabel(missing[0].Location, "").
		Help(help, missing...))
}

// startsArgument reports whether the current token begins another argument of
// the list being read. On the line of the previous argument any value does. On
// a later line it may be the statement after a call left unclosed, so it
// counts only when the `)` of the list is still ahead.
func (p *Parser) startsArgument() bool {
	if !p.startsValue() {
		return false
	}
	if p.isOnLineOfPrev() {
		return true
	}
	closer := p.closerAhead(p.pos)
	return closer >= 0 && p.stream[closer].Kind == token.RPAREN
}

// isSemicolonForComma reports whether the `;` at the current token of a value
// list was typed for a `,`. It was when the list still closes with its own
// `}`, which shows in what follows that brace: the end of the statement or of
// an enclosing list. A `}` followed by anything else closes the block around
// the statement, so the list's own brace is the one missing.
func (p *Parser) isSemicolonForComma() bool {
	closer := p.closerAhead(p.pos)
	if closer < 0 || p.stream[closer].Kind != token.RBRACE || closer+1 >= len(p.stream) {
		return false
	}
	switch p.stream[closer+1].Kind {
	case token.SEMICOLON, token.COMMA, token.RPAREN:
		return true
	}
	return false
}

// closerAhead returns the index of the token that closes the bracket the
// token at from sits in, skipping brackets that open and close on the way, or
// -1 when the file ends first.
func (p *Parser) closerAhead(from int) int {
	depth := 0
	for i := from; i < len(p.stream); i++ {
		switch p.stream[i].Kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

// startsValue reports whether the current token can begin an expression. A
// `!` is left out: after a value it is as likely a mistyped `!=` as a new one.
func (p *Parser) startsValue() bool {
	_, ok := nudLookup[p.current().Kind]
	return ok && !p.at(token.BANG)
}

// isTypeStart reports whether kind can begin a type.
func isTypeStart(kind token.Kind) bool {
	switch kind {
	case token.IDENT, token.AMP, token.QUESTION, token.QQ, token.ASTERISK, token.RAWPTR,
		token.LBRACK, token.FN, token.STRUCT, token.IFACE, token.ENUM:
		return true
	}
	return false
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

func (p *Parser) nextID() source.NodeID {
	p.nodeID++
	return source.ParsedNodeID(p.nodeID)
}

func reg[T ast.Node](p *Parser, n T) T {
	if !typednil.IsNil(n) {
		n.SetID(p.nextID())
	}
	return n
}

func (p *Parser) lastNonNilToken(fallback token.Token) token.Token {
	if p.pos > 0 && p.pos-1 < len(p.stream) {
		return p.stream[p.pos-1]
	}
	return fallback
}
