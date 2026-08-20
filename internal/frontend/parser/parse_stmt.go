// Recursive-descent parser for statements.
//
// Statement keywords select dedicated routines, and each routine consumes
// the tokens belonging to its construct before returning an AST node. Blocks
// recurse through parseStmt, so nested control flow uses the same token,
// diagnostic, recovery, and node-registration state as top-level statements.

package parser

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

func (p *Parser) parseStmt(isModuleLevel bool) ast.Stmt {
	if p.at(token.RBRACE) || p.at(token.EOF) {
		return nil
	}
	doc, attrs := p.parseLeadingMetadata()

	if p.at(token.RBRACE) || p.at(token.EOF) {
		return nil
	}

	var stmt ast.Stmt
	switch p.current().Kind {
	case token.FN:
		if decl := p.parseFnDecl(); decl != nil {
			stmt = decl.(ast.Stmt)
		}
	case token.LET:
		if decl := p.parseLetDecl(isModuleLevel); decl != nil {
			stmt = decl.(ast.Stmt)
		}
	case token.CONST:
		if decl := p.parseConstDecl(isModuleLevel); decl != nil {
			stmt = decl.(ast.Stmt)
		}
	case token.STRUCT:
		if decl := p.parseStructDecl(); decl != nil {
			stmt = decl.(ast.Stmt)
		}
	case token.IFACE:
		if decl := p.parseInterfaceDecl(); decl != nil {
			stmt = decl.(ast.Stmt)
		}
	case token.ENUM:
		if decl := p.parseEnumDecl(); decl != nil {
			stmt = decl.(ast.Stmt)
		}
	case token.TYPE:
		if decl := p.parseTypeAliasDecl(); decl != nil {
			stmt = decl.(ast.Stmt)
		}
	case token.LBRACE:
		stmt = p.parseBlock()
	case token.IF:
		stmt = p.parseIfStmt()
	case token.FOR:
		stmt = p.parseForStmt()
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
	if attributed, ok := stmt.(ast.AttributedNode); ok {
		attributed.SetAttributes(attrs)
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
		before := p.pos
		if stmt := p.parseStmt(false); stmt != nil {
			stmts = append(stmts, stmt)
			if p.pos == before && !p.at(token.RBRACE) && !p.at(token.EOF) {
				p.synchronize(token.SEMICOLON, token.RBRACE)
				if !p.at(token.RBRACE) && !p.at(token.EOF) {
					p.advance()
				}
			}
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

func (p *Parser) parseForStmt() ast.Stmt {
	start := p.consume(token.FOR, "expected for")
	if start == nil {
		return nil
	}
	var cond ast.Expr
	if !p.at(token.LBRACE) {
		cond = p.parseExpr(precLowest)
	}
	var body *ast.BlockStmt
	if p.at(token.LBRACE) {
		body = p.parseBlock()
	}
	if body == nil {
		prev := p.lastNonNilToken(*start)
		if cond != nil {
			prev.End = ast.EndOf(cond)
		}
		p.diag.Add(diagnostics.NewError("missing for body").WithCode(diagnostics.ErrExpectedToken).WithPrimaryLabel(source.NewLocation(p.filePath, prev.End, prev.End), "expected '{' here"))
		return reg(p, &ast.ForStmt{
			Cond:     cond,
			Location: source.NewLocation(p.filePath, start.Start, prev.End),
		})
	}
	endTok := p.lastNonNilToken(*start)
	if ast.LocOf(body) != nil && ast.LocOf(body).End != nil {
		endTok.End = *ast.LocOf(body).End
	}
	return reg(p, &ast.ForStmt{
		Cond:     cond,
		Body:     body,
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
