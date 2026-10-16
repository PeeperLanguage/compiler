package parser

import (
	"compiler/internal/diag"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

// block reads a block. A keyword that begins a declaration but not a statement
// means its `}` is missing: the block ends there and the declaration is read
// next.
func (p *parser) block() ast.Index {
	open, ok := p.expect(token.LBRACE)
	if !ok {
		return p.add(ast.KindBlock, p.lastEnd, p.t.PackBlock(ast.Block{Statements: ast.EmptyList}))
	}
	var buf [16]ast.Index
	stmts := buf[:0]
	for {
		kind := p.tok().Kind
		if kind == token.RBRACE || kind == token.EOF || startsDeclaration(kind) && statements[kind] == nil {
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
	statements[token.CONST] = func(p *parser) ast.Index { return constDecl(p, ast.None) }
	statements[token.RETURN] = returnStatement
	statements[token.IF] = ifStatement
	statements[token.FOR] = forStatement
	statements[token.BREAK] = jumpStatement
	statements[token.CONTINUE] = jumpStatement
	statements[token.MATCH] = matchStatement
	statements[token.LBRACE] = func(p *parser) ast.Index { return p.block() }
}

// stmt reads a statement: one a keyword begins, or an expression statement,
// or an assignment `target = value;`.
func (p *parser) stmt() ast.Index {
	if read := statements[p.tok().Kind]; read != nil {
		return read(p)
	}
	start := p.tok().Start
	e := p.expr(precLowest)
	if p.at(token.ASSIGN) {
		p.next()
		value := p.expr(precLowest)
		p.end()
		return p.add(ast.KindAssignStatement, start, p.t.PackAssignStatement(ast.AssignStatement{Target: e, Value: value}))
	}
	p.end()
	return p.add(ast.KindExpressionStatement, start, p.t.PackExpressionStatement(ast.ExpressionStatement{Expression: e}))
}

// letStatement reads `let mut name: Type = value;`, where `mut`, the type and
// the value may be left out, or `let .{ fields } = value;`.
func letStatement(p *parser) ast.Index {
	start := p.next().Start
	if p.at(token.DOT) {
		pattern := p.structPattern()
		p.expect(token.ASSIGN)
		value := p.expr(precLowest)
		p.end()
		return p.add(ast.KindDestructureStatement, start, p.t.PackDestructureStatement(ast.DestructureStatement{Pattern: pattern, Value: value}))
	}
	var op token.Kind
	if p.at(token.MUT) {
		op = p.next().Kind
	}
	name, typ, value := p.binding()
	return p.setOp(p.add(ast.KindLetStatement, start, p.t.PackLetStatement(ast.LetStatement{Name: name, Type: typ, Value: value})), op)
}

// ifStatement reads `if condition { } else if ... else { }`. A `=` written for
// `==` in the condition is read as `==`.
func ifStatement(p *parser) ast.Index {
	start := p.next().Start
	condition := p.expr(precLowest)
	if t := p.tok(); t.Kind == token.ASSIGN {
		p.next()
		p.repair(diag.New(diag.ExpectedToken, "expected `==` in condition, found `=`").Label(p.t.File, t.Span, "").
			Help("compare with `==`", diag.Fix{File: p.t.File, Span: t.Span, NewText: "=="}))
		right := p.expr(precLowest)
		condition = p.setOp(p.add(ast.KindBinaryExpression, p.t.Nodes[condition].Span.Start,
			p.t.PackBinaryExpression(ast.BinaryExpression{Left: condition, Right: right})), token.EQ)
	}
	then := p.body("if")
	otherwise := ast.None
	if p.at(token.ELSE) {
		p.next()
		if p.at(token.IF) {
			otherwise = ifStatement(p)
		} else {
			otherwise = p.body("else")
		}
	}
	return p.add(ast.KindIfStatement, start, p.t.PackIfStatement(ast.IfStatement{Condition: condition, Then: then, Else: otherwise}))
}

// body reads the block of an if, else or for; when it is missing, it reports
// "missing <what> body" where the block should start, and returns None.
func (p *parser) body(what string) ast.Index {
	if p.at(token.LBRACE) {
		return p.block()
	}
	p.fail(diag.New(diag.ExpectedToken, "missing "+what+" body").
		Label(p.t.File, source.Span{Start: p.lastEnd, End: p.lastEnd}, "expected `{` here"))
	return ast.None
}

// forStatement reads `for { }`, `for condition { }`, `for value in items { }`
// or `for index, value in items { }`. The iterable may be a range.
func forStatement(p *parser) ast.Index {
	start := p.next().Start
	if p.at(token.IDENT) && (p.ahead(1).Kind == token.IN || p.ahead(1).Kind == token.COMMA) {
		index, value := ast.None, p.name(ast.KindDeclaredName)
		if p.at(token.COMMA) {
			p.next()
			index, value = value, p.name(ast.KindDeclaredName)
		}
		p.expect(token.IN)
		iterable := p.indexOperand()
		body := p.body("for")
		return p.add(ast.KindForInStatement, start, p.t.PackForInStatement(ast.ForInStatement{Index: index, Value: value, Iterable: iterable, Body: body}))
	}
	condition := ast.None
	if !p.at(token.LBRACE) {
		condition = p.expr(precLowest)
		if t := p.tok(); t.Kind == token.IN {
			p.fail(diag.New(diag.InvalidExpression, "invalid loop variable").
				Label(p.t.File, p.t.Nodes[condition].Span, "expected an identifier before `in`"))
		}
	}
	body := p.body("for")
	return p.add(ast.KindLoopStatement, start, p.t.PackLoopStatement(ast.LoopStatement{Condition: condition, Body: body}))
}

// jumpStatement reads `break;` or `continue;`. Loop labels are not part of the
// language yet; a label is reported and skipped.
func jumpStatement(p *parser) ast.Index {
	keyword := p.next()
	if t := p.tok(); t.Kind == token.IDENT {
		p.next()
		p.repair(diag.New(diag.InvalidStatement, "labeled "+keyword.Kind.String()+" is not supported yet").
			Label(p.t.File, t.Span, "loop labels are planned for a later release"))
	}
	p.end()
	kind := ast.KindBreakStatement
	if keyword.Kind == token.CONTINUE {
		kind = ast.KindContinueStatement
	}
	return p.leaf(kind, keyword.Start)
}

// matchStatement reads `match subject { arms }`. An arm is `_ => { }`,
// `Enum::Case => { }` or `Enum::Case with pattern => { }`; arms are not
// separated by commas.
func matchStatement(p *parser) ast.Index {
	start := p.next().Start
	subject := p.expr(precLowest)
	var buf [8]ast.Index
	arms := buf[:0]
	if open, ok := p.expect(token.LBRACE); ok {
		// Every arm begins with a name (`_` or a path) and reads at least it,
		// so the loop always moves on.
		for p.at(token.IDENT) {
			armStart := p.tok().Start
			arms = append(arms, p.add(ast.KindMatchArm, armStart, p.matchArm()))
			if t := p.tok(); t.Kind == token.COMMA {
				p.next()
				p.repair(diag.New(diag.ExpectedToken, "match arms do not use commas").Label(p.t.File, t.Span, "").
					Help("remove this comma", diag.Fix{File: p.t.File, Span: t.Span}))
			}
		}
		p.close(token.RBRACE, open)
	}
	return p.add(ast.KindMatchStatement, start, p.t.PackMatchStatement(ast.MatchStatement{Subject: subject, Arms: p.list(arms)}))
}

// matchArm reads one arm's pattern, its `=>` and its block. A missing `=>`
// before the block is read as if it were there.
func (p *parser) matchArm() ast.Slots {
	var pattern ast.Index
	if t := p.tok(); t.Kind == token.IDENT && p.text(t) == "_" {
		pattern = p.leaf(ast.KindWildcardPattern, p.next().Start)
	} else {
		pattern = p.casePattern()
	}
	if p.at(token.LBRACE) {
		at := source.Span{Start: p.lastEnd, End: p.lastEnd}
		p.repair(diag.New(diag.ExpectedToken, "expected `=>` after match case").Label(p.t.File, at, "").
			Help("add missing `=>`", diag.Fix{File: p.t.File, Span: at, NewText: " =>"}))
	} else if _, ok := p.expect(token.FATARROW); !ok {
		p.sync()
	}
	return p.t.PackMatchArm(ast.MatchArm{Pattern: pattern, Body: p.block()})
}

// casePattern reads `Enum::Case`, perhaps followed by `with` and the pattern of
// its payload. The case is a full path.
func (p *parser) casePattern() ast.Index {
	start := p.tok().Start
	path := p.typeExpr()
	if kind := p.t.Nodes[path].Kind; kind != ast.KindPath && kind != ast.KindInvalidType {
		p.repair(diag.New(diag.InvalidExpression, "enum case must use a fully named path").
			Label(p.t.File, p.t.Nodes[path].Span, "write `Enum::Variant`"))
	}
	inner := ast.None
	if p.at(token.WITH) {
		p.next()
		inner = p.payloadPattern()
	}
	return p.add(ast.KindCasePattern, start, p.t.PackCasePattern(ast.CasePattern{Case: path, Inner: inner}))
}

// payloadPattern reads the pattern after `with`: `none`, a name to bind,
// `.{ fields }`, or another case.
func (p *parser) payloadPattern() ast.Index {
	switch t := p.tok(); {
	case t.Kind == token.NONE:
		return p.leaf(ast.KindNonePattern, p.next().Start)
	case t.Kind == token.DOT:
		return p.structPattern()
	case t.Kind == token.IDENT && p.ahead(1).Kind != token.DCOLON && p.ahead(1).Kind != token.LT:
		return p.name(ast.KindDeclaredName)
	case t.Kind == token.IDENT:
		return p.casePattern()
	default:
		p.fail(diag.New(diag.ExpectedToken, "expected a pattern after `with`").Label(p.t.File, t.Span, "").
			Help("write `none`, a name, `.{ field }`, or a case of the payload's enum"))
		return p.leaf(ast.KindInvalidExpression, p.lastEnd)
	}
}

// structPattern reads `.{ a, b as c, _ }`: fields to bind, each by its own name
// or by the name after `as`, and a `_` for the fields left out, which must come
// last. A `=` written for `as` is read as `as`.
func (p *parser) structPattern() ast.Index {
	start := p.next().Start // `.`
	var buf [8]ast.Index
	fields := buf[:0]
	if open, ok := p.expect(token.LBRACE); ok {
		fields = p.items(fields, token.RBRACE, isName, func() ast.Index {
			if t := p.tok(); p.text(t) == "_" {
				return p.leaf(ast.KindRestPattern, p.next().Start)
			}
			fieldToken := p.tok()
			field := p.name(ast.KindFieldName)
			binding := ast.None
			if t := p.tok(); t.Kind == token.AS || t.Kind == token.ASSIGN {
				p.next()
				if t.Kind == token.ASSIGN {
					p.repair(diag.New(diag.ExpectedToken, "a destructure field is renamed with `as`").Label(p.t.File, t.Span, "").
						Help("write `"+p.text(fieldToken)+" as name`", diag.Fix{File: p.t.File, Span: t.Span, NewText: "as"}))
				}
				binding = p.name(ast.KindDeclaredName)
			}
			return p.add(ast.KindFieldPattern, fieldToken.Start, p.t.PackFieldPattern(ast.FieldPattern{Field: field, Binding: binding}))
		})
		p.close(token.RBRACE, open)
	}
	for _, field := range fields[:max(len(fields)-1, 0)] {
		if p.t.Nodes[field].Kind == ast.KindRestPattern {
			p.repair(diag.New(diag.ExpectedToken, "`_` must be the last destructure item").Label(p.t.File, p.t.Nodes[field].Span, ""))
		}
	}
	return p.add(ast.KindStructPattern, start, p.t.PackStructPattern(ast.StructPattern{Fields: p.list(fields)}))
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
