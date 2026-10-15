package typechecker

import (
	"cmp"
	"fmt"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	sourceid "compiler/internal/source"
	"compiler/pkg/typednil"
)

func (c *checker) buildTHIR() *thir.Module {
	if c == nil || c.module == nil || c.module.AST == nil {
		return nil
	}
	constantCondition := func(expr ast.Expr) (*bool, []*diagnostics.Diagnostic) {
		pending := diagnostics.NewDiagnosticBag()
		value, ok := c.evaluateConstant(c.ctx.WithDiagnostics(pending), expr, &typeinfo.BoolType{})
		if !ok {
			return nil, pending.Diagnostics()
		}
		truth := value != nil && value.Truthy()
		return &truth, pending.Diagnostics()
	}
	builder := &thirBuilder{symbolIndex: c.module.SymbolIndex, typing: c.evidence, constantCondition: constantCondition, diagnostics: c.ctx.Diagnostics}
	functions := make([]*thir.Function, 0)
	ast.ForEachDecl(c.module.AST, func(declaration ast.Decl) bool {
		function, ok := declaration.(*ast.FnDecl)
		if !ok || function == nil {
			return true
		}
		functions = append(functions, builder.function(function))
		return true
	})
	return thir.NewModule(c.module.ID.ImportPath, c.module.FilePath, functions)
}

type thirBuilder struct {
	symbolIndex       *symbols.Index
	typing            *evidence
	constantCondition func(ast.Expr) (*bool, []*diagnostics.Diagnostic)
	// enclosing is the scope of the innermost block being built.
	enclosing *symbols.Scope
	// hoisted holds what the `orelse` expressions of the statement being built
	// must run before it.
	hoisted     hoist
	diagnostics *diagnostics.DiagnosticBag
}

// hoist is the statements that run before the one that contains their
// `orelse` expressions, and the carrier of the last of those, which builds
// the block that keeps them together.
type hoist struct {
	stmts   []thir.Stmt
	carrier *caseCarrier
}

// endHoist closes the hoist of built, the statement just built, restores
// outer, and returns built behind what it hoisted.
func (b *thirBuilder) endHoist(outer hoist, built thir.Stmt) thir.Stmt {
	hoisted := b.hoisted
	b.hoisted = outer
	if len(hoisted.stmts) == 0 || built == nil {
		return built
	}
	return hoisted.carrier.block(append(hoisted.stmts, built)...)
}

func (b *thirBuilder) function(source *ast.FnDecl) *thir.Function {
	function := &thir.Function{
		Identity:          source.ID().Function(),
		Source:            sourceInfo(source),
		IsEntrypointShape: source.Receiver == nil && source.Body != nil && len(source.TypeParams) == 0,
		ReturnTypeText:    ast.TypeText(source.ReturnType),
		HasReturnValue:    source.ReturnType != nil,
	}
	if source.ReturnOrigins != nil {
		function.ReturnOriginsLocation = source.ReturnOrigins.Location
	}
	if source.Name != nil {
		function.Name = source.Name.Name
		function.Symbol = b.symbol(source.Name)
	}
	if function.Symbol != nil {
		if typ, ok := symbols.GetSymbolType(function.Symbol); ok {
			if callable, ok := typ.(*typeinfo.FuncType); ok && callable != nil {
				function.ReturnType = callable.Return
			}
		}
	}
	for _, parameter := range source.ParamsWithReceiver() {
		function.Params = append(function.Params, thir.Param{Source: sourceInfo(parameter.Name), Symbol: b.symbol(parameter.Name)})
	}
	function.Body = b.block(source.Body)
	return function
}

func (b *thirBuilder) block(source *ast.BlockStmt) *thir.Block {
	if source == nil {
		return nil
	}
	block := &thir.Block{StmtInfo: stmtInfo(source), Scope: b.scope(source), Stmts: make([]thir.Stmt, 0, len(source.Stmts))}
	outer := b.enclosing
	b.enclosing = block.Scope
	defer func() { b.enclosing = outer }()
	b.statements(block, source.Stmts)
	return block
}

// operands builds values that are evaluated in the order given. A later one
// that hoists an `orelse` would run before the earlier ones, so those are
// stored in hidden locals in front of it and read from there.
func (b *thirBuilder) operands(sources ...ast.Expr) []thir.Expr {
	built := make([]thir.Expr, len(sources))
	var stored []bool
	for index, source := range sources {
		mark := len(b.hoisted.stmts)
		built[index] = b.expression(source)
		if len(b.hoisted.stmts) == mark {
			continue
		}
		if stored == nil {
			stored = make([]bool, len(sources))
		}
		// One left in place earlier is looked at again: this operand may be the
		// one that changes it.
		for earlier := range index {
			if stored[earlier] {
				continue
			}
			if store, read := b.storeFirst(sources[earlier], built[earlier], source); store != nil {
				b.hoisted.stmts = slices.Insert(b.hoisted.stmts, mark, thir.Stmt(store))
				built[earlier], stored[earlier] = read, true
				mark++
			}
		}
	}
	return built
}

// storeFirst stores value, built from source, in a hidden local, so that it is
// evaluated before later, which hoists an `orelse`: it returns the statement
// that does and the read that stands in for value. It returns nothing for a
// value that reads the same after later has run: a literal, a constant, a
// function.
func (b *thirBuilder) storeFirst(source ast.Expr, value thir.Expr, later ast.Expr) (*thir.Binding, thir.Expr) {
	if value == nil || typeinfo.IsInvalidOrUnknown(value.ExprType()) {
		return nil, nil
	}
	switch value.(type) {
	case *thir.NumberLiteral, *thir.StringLiteral, *thir.ByteLiteral, *thir.CharLiteral, *thir.BoolLiteral, *thir.NoneLiteral:
		return nil, nil
	case *thir.Ident, *thir.QualifiedIdent:
		if value.ExprPlace() == nil {
			return nil, nil
		}
	}
	typ := value.ExprType()
	// Storing a value that is only looked at would move it out of its place.
	// It stays where it is; only code that names its variable can change it.
	if use, known := value.UseKind(); value.ExprPlace() != nil && typeinfo.IsMoveTracked(typ) && !(known && use == typeinfo.UseMove) {
		ast.Inspect(later, func(node ast.Node) bool {
			if name, isName := node.(*ast.Ident); isName && b.symbol(name).Variable() == value.ExprPlace().Root {
				b.diagnostics.Add(invalidOperationError(source, "this value is read before an `orelse` that uses `"+name.Name+"` and runs first").
					Help("give the `orelse` a name first, with `let`"))
				return false
			}
			return true
		})
		return nil, nil
	}
	carrier := &caseCarrier{statement: source, slot: storedOperandSlot, index: b.symbolIndex, enclosing: b.enclosing}
	local := carrier.local("$value", typ)
	// The read keeps what the use needs from the value: its conversion and how it is used.
	info := b.expressionInfo(source)
	info.Source, info.Type, info.CaseCarrier, info.CasePath = carrier.source(), typ, nil, nil
	info.Place = &thir.Place{Root: local}
	return &thir.Binding{StmtInfo: thir.StmtInfo{Source: carrier.source()}, Symbol: local, Value: value},
		&thir.Ident{ExprInfo: info, Name: local.Name, Symbol: local}
}

// statements builds stmts into block. A guard the resolver gave the rest of
// the block to (a scope of its own, kept under the guard) is built with that
// rest as one inner block.
func (b *thirBuilder) statements(block *thir.Block, stmts []ast.Stmt) {
	for index, statement := range stmts {
		guard, isIf := statement.(*ast.IfStmt)
		if rest := b.scope(guard); isIf && rest != nil {
			source := ir.SourceInfo{NodeID: sourceid.GeneratedNodeID(guard.ID(), sourceid.GeneratedCaseBinding, 1, 1), Location: ast.LocOf(guard)}
			guarded := &thir.Block{StmtInfo: thir.StmtInfo{Source: source}, Scope: rest}
			b.symbolIndex.SetScopeID(source.NodeID, rest)
			outer := b.enclosing
			b.enclosing = rest
			outerHoist := b.hoisted
			b.hoisted = hoist{}
			guarded.Stmts = append(guarded.Stmts, b.endHoist(outerHoist, b.ifStatement(guard, b.payloadName(guard))))
			b.statements(guarded, stmts[index+1:])
			b.enclosing = outer
			block.Stmts = append(block.Stmts, guarded)
			return
		}
		if lowered := b.statement(statement); lowered != nil {
			block.Stmts = append(block.Stmts, lowered)
		}
	}
}

// payloadName returns the reference the resolver declared for owner (an arm,
// a guarded block, or a guard for the rest of its block) to name the payload
// of a borrowed subject, when the code uses it; an unused one is never built.
// A name that reads a variable's payload in place needs nothing built.
func (b *thirBuilder) payloadName(owner ast.Node) *symbols.Symbol {
	name := b.symbol(owner)
	if name == nil || !name.IsPayloadName || name.AliasOf != nil || !b.symbolIndex.IsUsed(name) {
		return nil
	}
	if typ, typed := symbols.GetSymbolType(name); !typed || typeinfo.IsInvalidOrUnknown(typ) {
		return nil
	}
	return name
}

// statement builds one statement, behind what its `orelse` expressions hoist.
func (b *thirBuilder) statement(statement ast.Stmt) thir.Stmt {
	outer := b.hoisted
	b.hoisted = hoist{}
	return b.endHoist(outer, b.plainStatement(statement))
}

func (b *thirBuilder) plainStatement(statement ast.Stmt) thir.Stmt {
	switch node := statement.(type) {
	case nil:
		return nil
	case *ast.BlockStmt:
		return b.block(node)
	case *ast.LetDecl:
		return &thir.Binding{StmtInfo: stmtInfo(node), Symbol: b.symbol(node.Name), IsInferred: node.Type == nil, Value: b.expression(node.Value)}
	case *ast.ConstDecl:
		return &thir.Binding{StmtInfo: stmtInfo(node), Symbol: b.symbol(node.Name), IsConstant: true, IsInferred: node.Type == nil, Value: b.expression(node.Value)}
	case *ast.DestructureStmt:
		return &thir.Destructure{StmtInfo: stmtInfo(node), Value: b.expression(node.Value), Bindings: b.destructureBindings(node.Fields)}
	case *ast.ExprStmt:
		return &thir.ExprStmt{StmtInfo: stmtInfo(node), Value: b.expression(node.Expr)}
	case *ast.AssignStmt:
		return &thir.Assign{StmtInfo: stmtInfo(node), Target: b.expression(node.Target), Value: b.expression(node.Value)}
	case *ast.ReturnStmt:
		return &thir.Return{StmtInfo: stmtInfo(node), Value: b.expression(node.Value)}
	case *ast.IfStmt:
		return b.ifStatement(node, nil)
	case *ast.ForStmt:
		return b.forStatement(node)
	case *ast.BreakStmt:
		return &thir.Break{StmtInfo: stmtInfo(node)}
	case *ast.ContinueStmt:
		return &thir.Continue{StmtInfo: stmtInfo(node)}
	case *ast.MatchStmt:
		return b.matchStatement(node)
	case *ast.BadStmt, *ast.BadDecl:
		return &thir.InvalidStmt{StmtInfo: stmtInfo(node), Message: "invalid source statement"}
	case *ast.ImportDecl, *ast.FnDecl, *ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		return &thir.InvalidStmt{StmtInfo: stmtInfo(node), Message: "declaration is not executable in function body"}
	default:
		panic(fmt.Sprintf("THIR: unhandled statement %T", statement))
	}
}

func (b *thirBuilder) forStatement(source *ast.ForStmt) *thir.For {
	loop := &thir.For{StmtInfo: stmtInfo(source)}
	if b.typing != nil {
		if checked := b.typing.CheckedIteration(source.ID()); checked != nil {
			loop.Checked = b.block(checked)
			return loop
		}
	}
	loop.Index = b.symbol(source.Index)
	loop.Value = b.symbol(source.Value)
	loop.Iterable = b.expression(source.Iterable)
	// The condition runs on every pass, so what it hoists belongs to the
	// body: `for { <hoisted>; if cond {} else { break; } body }`.
	outer, outerScope := b.hoisted, b.enclosing
	b.hoisted, b.enclosing = hoist{}, b.scope(source.Body)
	loop.Condition = b.expression(source.Cond)
	header := b.hoisted
	b.hoisted, b.enclosing = outer, outerScope
	loop.Body = b.block(source.Body)
	if len(header.stmts) > 0 {
		leave := header.carrier.block(&thir.Break{StmtInfo: thir.StmtInfo{Source: header.carrier.source()}})
		test := &thir.If{StmtInfo: thir.StmtInfo{Source: header.carrier.source()}, Condition: loop.Condition, Then: header.carrier.block(), Else: leave}
		loop.Body.Stmts = append(append(header.stmts, test), loop.Body.Stmts...)
		loop.Condition = nil
	}
	switch {
	case loop.ConditionIsTrueLiteral():
		b.diagnostics.Add(endlessLoopWarning(loop.Condition.SourceInfo().Location))
	case loop.Condition != nil && b.constantCondition != nil:
		// Only the value is kept: what evaluating it reports is the checker's.
		loop.ConstantCondition, _ = b.constantCondition(source.Cond)
	}
	if b.typing == nil {
		return loop
	}
	loop.Iteration, _ = b.typing.ForIteration(source.ID())
	return loop
}

// ifStatement builds an `if`. When a block it guards uses its payload name
// (rest is the one for the code after a guard), the test becomes a match that
// defines the names.
func (b *thirBuilder) ifStatement(source *ast.IfStmt, rest *symbols.Symbol) thir.Stmt {
	then := b.payloadName(source.Then)
	var otherwise *symbols.Symbol
	elseBlock, hasElseBlock := source.Else.(*ast.BlockStmt)
	if hasElseBlock {
		otherwise = b.payloadName(elseBlock)
	}
	if then != nil || otherwise != nil || rest != nil {
		return b.boundIfStatement(source, then, otherwise, rest)
	}
	statement := &thir.If{StmtInfo: stmtInfo(source), Condition: b.expression(source.Cond)}
	if b.constantCondition != nil {
		statement.ConstantCondition, statement.ConditionDiagnostics = b.constantCondition(source.Cond)
	}
	statement.Then = b.block(source.Then)
	statement.Else = b.statement(source.Else)
	return statement
}

// destructureBindings builds the locals of `.{ field, field as name }`.
func (b *thirBuilder) destructureBindings(fields []ast.DestructureField) []thir.DestructureBinding {
	var bindings []thir.DestructureBinding
	for _, field := range fields {
		// A field the checker rejected has no evidence and defines nothing.
		if access, ok := b.typing.StructField(field.Name.ID()); ok {
			bindings = append(bindings, thir.DestructureBinding{
				Source: sourceInfo(field.Binding), Field: access.Field, Type: access.Type, Symbol: b.symbol(field.Binding),
			})
		}
	}
	return bindings
}

func (b *thirBuilder) matchStatement(source *ast.MatchStmt) thir.Stmt {
	match := &thir.Match{StmtInfo: stmtInfo(source)}
	var evidence Match
	found := false
	if b.typing != nil {
		evidence, found = b.typing.Match(source.ID())
	}
	if !found {
		match.Subject = b.expression(source.Subject)
		return match
	}
	match.EnumType = evidence.EnumType
	match.CaseCount = evidence.CaseCount
	// An arm that names a payload starts with a statement that takes it, which
	// the carrier builds; so does a match with no place to read payloads from.
	arms := source.Arms[:min(len(source.Arms), len(evidence.ArmCases))]
	var carrier *caseCarrier
	if evidence.Spills || slices.ContainsFunc(arms, func(arm *ast.MatchArm) bool {
		return arm.Pattern.Taking() != nil || b.payloadName(arm) != nil
	}) {
		if variable, isVariable := evidence.Subject.(*ast.Ident); isVariable && evidence.Borrow == nil {
			carrier = b.payloadsOf(source, variable)
		} else {
			carrier = b.caseCarrier(source, evidence.Subject, evidence.Borrow)
		}
	}
	if carrier != nil {
		match.Subject = carrier.subject
	} else {
		match.Subject = b.expression(evidence.Subject)
	}
	for index, arm := range arms {
		match.Arms = append(match.Arms, thir.MatchArm{
			Source: sourceInfo(arm), Cases: evidence.ArmCases[index], Body: b.block(arm.Body),
		})
	}
	if carrier == nil {
		return match
	}
	for index, arm := range arms {
		switch taking := arm.Pattern.Taking(); {
		case taking == nil:
			carrier.startArm(&match.Arms[index], b.payloadName(arm), false)
		case taking.Name != nil:
			carrier.startArm(&match.Arms[index], b.symbol(taking.Name), false)
		default:
			payload := evidence.ArmPayloads[index]
			carrier.prepend(&match.Arms[index], &thir.Destructure{
				StmtInfo: thir.StmtInfo{Source: carrier.source()}, Value: carrier.take(payload, match.Arms[index].Cases), Bindings: b.destructureBindings(taking.Fields),
			}, payload)
		}
	}
	return carrier.wrap(match)
}

// boundIfStatement builds an `if` whose test names a payload as a match on
// the tested value: one arm for the then block, one for the rest. then and
// otherwise are the payload names of the two blocks; rest is the name for the
// code after the statement, set in the arm that does not leave.
func (b *thirBuilder) boundIfStatement(source *ast.IfStmt, then, otherwise, rest *symbols.Symbol) thir.Stmt {
	test, tested := b.typing.CaseTest(source.Cond.ID())
	if !tested {
		return &thir.InvalidStmt{StmtInfo: stmtInfo(source), Message: "invalid case test"}
	}
	carrier := b.caseCarrier(source, test.Subject, test.Borrow)
	if carrier == nil {
		return &thir.InvalidStmt{StmtInfo: stmtInfo(source), Message: "invalid case test"}
	}
	descriptor, _ := typeinfo.VariantDescriptorOf(carrier.typ)
	var held, others []thir.ArmCase
	for caseIndex := range descriptor.Cases {
		if caseIndex == test.Case {
			held = append(held, thir.ArmCase{Case: caseIndex})
		} else {
			others = append(others, thir.ArmCase{Case: caseIndex})
		}
	}
	if !test.MatchesWhenTrue {
		held, others = others, held
	}
	var elseBody *thir.Block
	switch elseStmt := source.Else.(type) {
	case *ast.BlockStmt:
		elseBody = b.block(elseStmt)
	case *ast.IfStmt:
		elseBody = carrier.block(b.statement(elseStmt))
	default:
		elseBody = carrier.block()
	}
	match := &thir.Match{
		StmtInfo: stmtInfo(source), Subject: carrier.subject, EnumType: carrier.typ, CaseCount: len(descriptor.Cases),
		Arms: []thir.MatchArm{
			{Source: sourceInfo(source.Then), Cases: held, Body: b.block(source.Then)},
			{Source: elseBody.Source, Cases: others, Body: elseBody},
		},
	}
	carrier.startArm(&match.Arms[0], then, false)
	if rest == nil {
		carrier.startArm(&match.Arms[1], otherwise, false)
		return carrier.wrap(match)
	}
	carrier.startArm(&match.Arms[1], rest, true)
	return carrier.wrap(match, &thir.Binding{StmtInfo: thir.StmtInfo{Source: carrier.source()}, Symbol: rest})
}

// orElse builds `value orelse fallback` as statements that run before the one
// it is part of: a hidden local is declared; when value holds none it gets the
// fallback, or the fallback block leaves; otherwise it gets the payload. The
// expression is then a read of that local.
func (b *thirBuilder) orElse(source *ast.OrElseExpr, info thir.ExprInfo) thir.Expr {
	test, tested := b.typing.CaseTest(source.ID())
	if !tested || info.Type == nil || typeinfo.IsInvalidOrUnknown(info.Type) {
		return &thir.InvalidExpr{ExprInfo: info, Message: "invalid orelse"}
	}
	carrier := b.caseCarrier(source, test.Subject, test.Borrow)
	if carrier == nil {
		return &thir.InvalidExpr{ExprInfo: info, Message: "invalid orelse"}
	}
	result := carrier.local("$orelse", info.Type)
	var none *thir.Block
	if source.Block != nil {
		none = b.block(source.Block)
	} else {
		// The fallback runs only when needed, and so does what it hoists.
		outer := b.hoisted
		b.hoisted = hoist{}
		fallback := carrier.assign(result, b.expression(source.Fallback))
		none = carrier.block(b.endHoist(outer, fallback))
	}
	present := carrier.block()
	match := &thir.Match{
		StmtInfo: thir.StmtInfo{Source: carrier.source()}, Subject: carrier.subject, EnumType: carrier.typ, CaseCount: 2,
		Arms: []thir.MatchArm{
			{Source: none.Source, Cases: []thir.ArmCase{{Case: ir.OptionalAbsentCase}}, Body: none},
			{Source: present.Source, Cases: []thir.ArmCase{{Case: ir.OptionalPresentCase}}, Body: present},
		},
	}
	carrier.prepend(&match.Arms[0], nil, nil)
	carrier.startArm(&match.Arms[1], result, true)
	if carrier.spill != nil {
		b.hoisted.stmts = append(b.hoisted.stmts, carrier.spill)
	}
	b.hoisted.stmts = append(b.hoisted.stmts, &thir.Binding{StmtInfo: thir.StmtInfo{Source: carrier.source()}, Symbol: result}, match)
	b.hoisted.carrier = carrier
	info.Place = &thir.Place{Root: result}
	return &thir.Ident{ExprInfo: info, Name: result.Name, Symbol: result}
}

// shortCircuit builds `left && right` or `left || right` whose right side must
// not run when the left side decides the result, as statements that run
// before the one it is part of: a hidden local gets the right side in one
// branch of an `if` on the left side, and the known result in the other. The
// expression is then a read of that local.
func (b *thirBuilder) shortCircuit(source *ast.BinaryExpr, info thir.ExprInfo) thir.Expr {
	if typeinfo.IsInvalidOrUnknown(info.Type) {
		return &thir.InvalidExpr{ExprInfo: info, Message: "invalid condition"}
	}
	carrier := &caseCarrier{statement: source, index: b.symbolIndex, enclosing: b.enclosing}
	result := carrier.local("$either", info.Type)
	test := &thir.If{StmtInfo: thir.StmtInfo{Source: carrier.source()}, Condition: b.expression(source.Left)}
	// The right side runs only when needed, and so does what it hoists.
	outer := b.hoisted
	b.hoisted = hoist{}
	right := carrier.assign(result, b.expression(source.Right))
	needed := carrier.block(b.endHoist(outer, right))
	decided := carrier.block(carrier.assign(result, &thir.BoolLiteral{
		ExprInfo: thir.ExprInfo{Source: carrier.source(), Type: info.Type}, Value: source.Op == "||",
	}))
	test.Then, test.Else = needed, decided
	if source.Op == "||" {
		test.Then, test.Else = decided, needed
	}
	b.hoisted.stmts = append(b.hoisted.stmts, &thir.Binding{StmtInfo: thir.StmtInfo{Source: carrier.source()}, Symbol: result}, test)
	b.hoisted.carrier = carrier
	info.Place = &thir.Place{Root: result}
	return &thir.Ident{ExprInfo: info, Name: result.Name, Symbol: result}
}

// isPlain reports whether evaluating expr can be noticed: it cannot fail and
// changes nothing. A plain right side of `&&` or `||` may run when it is not
// needed, which costs no hidden local and no branch.
func (b *thirBuilder) isPlain(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.NumberLit, *ast.StringLit, *ast.ByteLit, *ast.CharLit, *ast.BoolLit, *ast.NoneLit, *ast.ScopeResolution:
		return true
	case *ast.Ident:
		// A payload read is only valid where its test held.
		symbol := b.symbol(node)
		return symbol == nil || symbol.AliasOf == nil
	case *ast.UnaryExpr:
		return node.Op == "!" && b.isPlain(node.Expr)
	case *ast.IsExpr:
		return b.isPlain(node.Value)
	case *ast.SelectorExpr:
		// A field read through a raw pointer can fail; one through a
		// reference cannot.
		access, isField := b.typing.StructField(node.ID())
		if !isField || !b.isPlain(node.Expr) {
			return false
		}
		_, _, isReference := typeinfo.ReferenceTarget(typeinfo.Underlying(b.typing.ExprType(node.Expr.ID())))
		return access.DereferenceType == nil || isReference
	case *ast.BinaryExpr:
		switch node.Op {
		case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
			return b.isPlain(node.Left) && b.isPlain(node.Right)
		}
	}
	return false
}

// caseCarrier builds every statement that reads a case's payload under a name
// (a pattern's name, the name of a borrowed payload, or the one `orelse`
// defines) in one shape: a match selects the case, and a name is set from the
// payload only in the arm that selects that case. A variable is matched where
// it is and names take the payload out of it. Any other value is first moved
// or copied into a hidden local, the carrier, and names are taken out of
// that. A borrowed subject (`&place`) is matched in place, and names are
// references into it.
type caseCarrier struct {
	statement ast.Node
	// typ is the type of the value matched: the enum or the optional.
	typ typeinfo.Type
	// subject is what the match reads whole: the carrier, or the place.
	subject thir.Expr
	// symbol is the carrier, and spill stores the subject in it. Both are nil
	// when the payload is read where it is: from variable, or for a borrowed
	// subject through references, mode saying which kind.
	symbol   *symbols.Symbol
	spill    *thir.Binding
	variable *symbols.Symbol
	mode     thir.AddressMode

	index     *symbols.Index
	enclosing *symbols.Scope
	// slot keeps the nodes built for one statement by different carriers apart.
	slot    uint64
	ordinal uint64
}

// storedOperandSlot is the slot of what storeFirst builds: the statement of a
// stored operand may be an `orelse` with a carrier of its own (slot 0), or a
// guard (slot 1, see statements).
const storedOperandSlot = 2

// caseCarrier returns how statement takes subject apart (borrow is set when
// it is tested through a reference), or nil when the checker could not type
// the subject.
func (b *thirBuilder) caseCarrier(statement ast.Node, subject ast.Expr, borrow *typeinfo.RefType) *caseCarrier {
	c := &caseCarrier{statement: statement, index: b.symbolIndex, enclosing: b.enclosing, typ: b.typing.ExprType(subject.ID())}
	if typeinfo.IsInvalidOrUnknown(c.typ) {
		return nil
	}
	if borrow != nil {
		c.subject, c.mode = b.expression(subject), thir.AddressShared
		if borrow.IsMutable {
			c.mode = thir.AddressMutable
		}
		return c
	}
	c.symbol = c.local("$case", c.typ)
	source := c.source()
	value := b.expression(subject)
	c.spill = &thir.Binding{StmtInfo: thir.StmtInfo{Source: ir.SourceInfo{NodeID: source.NodeID, Location: value.SourceInfo().Location}}, Symbol: c.symbol, Value: value}
	c.subject = c.ident(c.symbol, c.typ)
	return c
}

// local declares a hidden local of the scope the statement is in. A rebuild
// of the same scope finds the one declared before.
func (c *caseCarrier) local(prefix string, typ typeinfo.Type) *symbols.Symbol {
	name := prefix + "." + c.statement.ID().Local()
	if existing, found := c.enclosing.LookupLocal(name); found {
		return existing
	}
	local := symbols.New(symbols.SourceSymbolID(c.source().NodeID), name, symbols.SymbolVar, c.statement, ast.LocOf(c.statement))
	local.BindType(typ)
	c.index.MarkUsed(local)
	if err := c.enclosing.Declare(local); err != nil {
		panic(err)
	}
	return local
}

// payloadsOf returns how a match takes payloads out of the variable it
// matches, where the variable is, or nil when the checker could not type it.
func (b *thirBuilder) payloadsOf(statement *ast.MatchStmt, variable *ast.Ident) *caseCarrier {
	typ := b.typing.ExprType(variable.ID())
	if typeinfo.IsInvalidOrUnknown(typ) {
		return nil
	}
	return &caseCarrier{
		statement: statement, index: b.symbolIndex, enclosing: b.enclosing,
		typ: typ, variable: b.symbol(variable).Variable(), subject: b.expression(variable),
	}
}

// source identifies one node built for the statement.
func (c *caseCarrier) source() ir.SourceInfo {
	c.ordinal++
	return ir.SourceInfo{
		NodeID:   sourceid.GeneratedNodeID(c.statement.ID(), sourceid.GeneratedCaseBinding, c.slot, c.ordinal),
		Location: ast.LocOf(c.statement),
	}
}

// block is a built block inside the statement's scope. Its own scope holds
// at most a dropped carrier; the carrier and the names belong to the scopes
// the resolver gave them.
func (c *caseCarrier) block(stmts ...thir.Stmt) *thir.Block {
	block := &thir.Block{StmtInfo: thir.StmtInfo{Source: c.source()}, Scope: symbols.NewScope(c.enclosing), Stmts: stmts}
	c.index.SetScopeID(block.Source.NodeID, block.Scope)
	return block
}

func (c *caseCarrier) ident(symbol *symbols.Symbol, typ typeinfo.Type) *thir.Ident {
	info := thir.ExprInfo{Source: c.source(), Type: typ, Place: &thir.Place{Root: symbol}}
	return &thir.Ident{ExprInfo: info, Name: symbol.Name, Symbol: symbol}
}

// take reads the payload of what an arm with cases selects, as a name of type
// nameType gets it: out of the carrier or the variable, or borrowed from the
// place.
func (c *caseCarrier) take(nameType typeinfo.Type, cases []thir.ArmCase) thir.Expr {
	path := append(slices.Clone(c.subject.PayloadPath()), thir.PayloadPath(cases)...)
	if c.symbol != nil || c.variable != nil {
		read := c.ident(cmp.Or(c.symbol, c.variable), nameType)
		read.CaseCarrier, _ = symbols.GetSymbolType(read.Symbol)
		read.CasePath = path
		return read
	}
	payload, _, _ := typeinfo.ReferenceTarget(nameType)
	read := c.reread(c.subject, payload, path)
	return &thir.Address{ExprInfo: thir.ExprInfo{Source: c.source(), Type: nameType}, Mode: c.mode, Value: read}
}

// reread reads the borrowed place (a variable, its fields, and elements picked
// by a number or a variable) again; the match read it first. With payload set,
// it reads the payload reached through path.
func (c *caseCarrier) reread(place thir.Expr, payload typeinfo.Type, path []int) thir.Expr {
	// retarget gives a read its own identity and, with payload set, makes it
	// read the payload; base and index are its rereads, nil for a variable.
	retarget := func(info *thir.ExprInfo, base, index thir.Expr) {
		original := *info
		info.Source = c.source()
		if payload != nil {
			info.Type, info.CaseCarrier, info.CasePath = payload, original.StoredType(), path
		}
		if base == nil {
			info.Place = &thir.Place{Root: original.Place.Root}
			return
		}
		step := original.Place.Projections[len(original.Place.Projections)-1]
		step.Source, step.BaseSource = info.Source, base.SourceInfo()
		if index != nil {
			step.Index = index
		}
		info.Place = projectPlace(base, step)
	}
	switch original := place.(type) {
	case *thir.Ident:
		read := *original
		retarget(&read.ExprInfo, nil, nil)
		return &read
	case *thir.Field:
		read := *original
		read.Base = c.reread(original.Base, nil, nil)
		retarget(&read.ExprInfo, read.Base, nil)
		return &read
	case *thir.Index:
		read := *original
		read.Base = c.reread(original.Base, nil, nil)
		if index, isVariable := original.Index.(*thir.Ident); isVariable {
			read.Index = c.reread(index, nil, nil)
		} else {
			number := *original.Index.(*thir.NumberLiteral)
			number.Source = c.source()
			read.Index = &number
		}
		retarget(&read.ExprInfo, read.Base, read.Index)
		return &read
	default:
		panic(fmt.Sprintf("borrowed subject %T cannot be read twice", place))
	}
}

func (c *caseCarrier) assign(target *symbols.Symbol, value thir.Expr) *thir.Assign {
	typ, _ := symbols.GetSymbolType(target)
	return &thir.Assign{StmtInfo: thir.StmtInfo{Source: c.source()}, Target: c.ident(target, typ), Value: value}
}

// startArm starts arm by setting name from the payload: defining it, or with
// declared, assigning it (it is declared outside the match). A nil name, or
// one the checker could not type, gets nothing.
func (c *caseCarrier) startArm(arm *thir.MatchArm, name *symbols.Symbol, declared bool) {
	nameType, typed := symbols.GetSymbolType(name)
	if !typed || typeinfo.IsInvalidOrUnknown(nameType) {
		c.prepend(arm, nil, nil)
		return
	}
	var set thir.Stmt
	if declared {
		set = c.assign(name, c.take(nameType, arm.Cases))
	} else {
		set = &thir.Binding{StmtInfo: thir.StmtInfo{Source: c.source()}, Symbol: name, Value: c.take(nameType, arm.Cases)}
	}
	c.prepend(arm, set, nameType)
}

// prepend puts take (a statement that takes the payload as type taken, or
// nil) at the start of arm. A carrier that take did not move is then dropped,
// so every arm leaves an owning carrier consumed and the arms agree where
// they join.
func (c *caseCarrier) prepend(arm *thir.MatchArm, take thir.Stmt, taken typeinfo.Type) {
	var prologue []thir.Stmt
	if take != nil {
		prologue = append(prologue, take)
	}
	if c.symbol != nil && !typeinfo.IsMoveTracked(taken) && typeinfo.IsMoveTracked(c.typ) {
		// `let _ = carrier;`: the arm's scope drops it.
		source := c.source()
		discard := symbols.New(symbols.SourceSymbolID(source.NodeID), "_", symbols.SymbolVar, c.statement, source.Location)
		discard.BindType(c.typ)
		if err := arm.Body.Scope.Declare(discard); err != nil {
			panic(err)
		}
		prologue = append(prologue, &thir.Binding{StmtInfo: thir.StmtInfo{Source: source}, Symbol: discard, Value: c.ident(c.symbol, c.typ)})
	}
	arm.Body.Stmts = append(prologue, arm.Body.Stmts...)
}

// wrap keeps the statements together: storing the subject in the carrier,
// the declared ones, then match. The block declares nothing itself.
func (c *caseCarrier) wrap(match *thir.Match, declared ...thir.Stmt) *thir.Block {
	var stmts []thir.Stmt
	if c.spill != nil {
		stmts = append(stmts, c.spill)
	}
	return c.block(append(append(stmts, declared...), match)...)
}

func (b *thirBuilder) expression(expression ast.Expr) thir.Expr {
	if expression == nil {
		return nil
	}
	info := b.expressionInfo(expression)
	if b.typing != nil {
		if construction, found := b.typing.VariantConstruction(expression.ID()); found {
			if info.Type == nil {
				info.Type = construction.EnumType
			}
			return &thir.Variant{
				ExprInfo: info, Case: construction.Case,
				Payload: b.expression(construction.Value), PayloadType: construction.Payload,
			}
		}
	}

	var result thir.Expr
	switch node := expression.(type) {
	case *ast.NumberLit:
		result = &thir.NumberLiteral{ExprInfo: info, Value: node.Value, ExplicitType: node.ExplicitType}
	case *ast.StringLit:
		result = &thir.StringLiteral{ExprInfo: info, Value: node.Value, IsCString: node.IsCString}
	case *ast.ByteLit:
		result = &thir.ByteLiteral{ExprInfo: info, Value: node.Value}
	case *ast.CharLit:
		result = &thir.CharLiteral{ExprInfo: info, Value: node.Value}
	case *ast.BoolLit:
		result = &thir.BoolLiteral{ExprInfo: info, Value: node.Value}
	case *ast.NoneLit:
		result = &thir.NoneLiteral{ExprInfo: info}
	case *ast.Ident:
		// A payload name stands for its variable; the use is marked as a
		// payload read (CaseCarrier).
		symbol := b.symbol(node).Variable()
		if info.Type == nil && symbol != nil {
			info.Type, _ = symbols.GetSymbolType(symbol)
		}
		if isStorageSymbol(symbol) {
			info.Place = &thir.Place{Root: symbol}
		}
		result = &thir.Ident{ExprInfo: info, Name: node.Name, Symbol: symbol, IsExpandedDefaultBinding: b.typing != nil && b.typing.ExpandedDefaultBinding(node.ID())}
	case *ast.ScopeResolution:
		symbol := b.symbol(node)
		if info.Type == nil && symbol != nil {
			info.Type, _ = symbols.GetSymbolType(symbol)
		}
		if isStorageSymbol(symbol) {
			info.Place = &thir.Place{Root: symbol}
		}
		result = &thir.QualifiedIdent{ExprInfo: info, Name: node.TypeText(), Symbol: symbol}
	case *ast.SelectorExpr:
		result = b.fieldExpression(node, info)
	case *ast.IndexExpr:
		result = b.indexExpression(node, info)
	case *ast.RangeExpr:
		ends := b.operands(node.Start, node.End)
		result = &thir.Range{ExprInfo: info, Start: ends[0], End: ends[1], IsEndExclusive: node.IsEndExclusive}
	case *ast.StructLit:
		result = b.structLiteral(node, info)
	case *ast.VariantLit:
		result = &thir.InvalidExpr{ExprInfo: info, Message: "variant construction missing semantic evidence"}
	case *ast.ArrayLit:
		result = &thir.ArrayLiteral{ExprInfo: info, Values: b.operands(node.Values...)}
	case *ast.AddressExpr:
		mode := thir.AddressRaw
		switch node.Mode {
		case ast.AddressShared:
			mode = thir.AddressShared
		case ast.AddressMutable:
			mode = thir.AddressMutable
		}
		result = &thir.Address{ExprInfo: info, Mode: mode, Value: b.expression(node.Expr)}
	case *ast.UnaryExpr:
		result = &thir.Unary{ExprInfo: info, Op: node.Op, Value: b.expression(node.Expr)}
	case *ast.BinaryExpr:
		if (node.Op == "&&" || node.Op == "||") && !b.isPlain(node.Right) {
			return b.shortCircuit(node, info)
		}
		isStringConcatenation := b.typing != nil && b.typing.StringConcatenation(node.ID())
		left, right := node.Left, node.Right
		// A `none` test reads its subject in place, borrowed or not.
		test, subject := b.caseTest(node.ID())
		if test != nil && isNoneExpr(left) {
			right = subject
		} else if test != nil {
			left = subject
		}
		sides := b.operands(left, right)
		result = &thir.Binary{
			ExprInfo: info, Left: sides[0], Op: node.Op,
			Right: sides[1], IsStringConcatenation: isStringConcatenation, Test: test,
		}
	case *ast.OrElseExpr:
		result = b.orElse(node, info)
	case *ast.IsExpr:
		value := node.Value
		test, subject := b.caseTest(node.ID())
		if test != nil {
			value = subject
		}
		result = &thir.Is{ExprInfo: info, Value: b.expression(value), Test: test}
	case *ast.CallExpr:
		result = b.callExpression(node, info)
	case *ast.FreeExpr:
		result = &thir.Free{ExprInfo: info, Value: b.expression(node.Expr)}
	case *ast.PrintExpr:
		result = &thir.Print{ExprInfo: info, Value: b.expression(node.Expr), AppendsNewline: node.AppendsNewline}
	case *ast.AsExpr:
		result = &thir.Cast{ExprInfo: info, Value: b.expression(node.Expr)}
	case *ast.BadExpr:
		result = &thir.InvalidExpr{ExprInfo: info, Message: "invalid source expression"}
	default:
		panic(fmt.Sprintf("THIR: unhandled expression %T", expression))
	}
	return result
}

func (b *thirBuilder) fieldExpression(source *ast.SelectorExpr, info thir.ExprInfo) thir.Expr {
	field := &thir.Field{ExprInfo: info, Base: b.expression(source.Expr)}
	if source.Name != nil {
		field.Name = source.Name.Name
		field.Symbol = b.symbol(source.Name)
		if info.Type == nil && field.Symbol != nil {
			info.Type, _ = symbols.GetSymbolType(field.Symbol)
			field.ExprInfo.Type = info.Type
		}
	}
	if b.typing != nil {
		if access, found := b.typing.StructField(source.ID()); found {
			if field.ExprInfo.Type == nil {
				field.ExprInfo.Type = access.Type
			}
			field.Access = &thir.FieldAccess{Field: access.Field, DereferenceType: access.DereferenceType}
		}
	}
	if projection, projected := place.Project(source); projected {
		fieldIndex := -1
		var dereferenceType typeinfo.Type
		if field.Access != nil {
			fieldIndex = field.Access.Field
			dereferenceType = field.Access.DereferenceType
		}
		field.ExprInfo.Place = projectPlace(field.Base, thir.PlaceProjection{
			Source: field.Source, BaseSource: field.Base.SourceInfo(),
			Kind: thir.PlaceField, Name: projection.Step.Field, Field: fieldIndex,
			DereferenceType: dereferenceType, Type: field.StoredType(),
		})
	}
	return field
}

func (b *thirBuilder) indexExpression(source *ast.IndexExpr, info thir.ExprInfo) thir.Expr {
	index := &thir.Index{ExprInfo: info, Base: b.expression(source.Expr), Index: b.expression(source.Index)}
	if b.typing != nil {
		if constant, found := b.typing.ConstantIndex(source.ID()); found {
			index.Constant = &constant
		}
	}
	if _, ranged := source.Index.(*ast.RangeExpr); !ranged && index.Index != nil {
		if _, projected := place.Project(source); projected {
			index.ExprInfo.Place = projectPlace(index.Base, thir.PlaceProjection{
				Source: index.Source, BaseSource: index.Base.SourceInfo(),
				Kind: thir.PlaceIndex, Index: index.Index, ConstantIndex: index.Constant, Type: index.StoredType(),
			})
		}
	}
	return index
}

func (b *thirBuilder) structLiteral(source *ast.StructLit, info thir.ExprInfo) thir.Expr {
	values := make([]ast.Expr, 0, len(source.Fields))
	if b.typing != nil {
		if ordered, found := b.typing.StructLiteralFields(source.ID()); found {
			values = ordered
		}
	}
	if values == nil || len(values) == 0 && len(source.Fields) != 0 {
		for _, field := range source.Fields {
			values = append(values, field.Value)
		}
	}
	fields := make([]thir.StructField, 0, len(values))
	semantic, _ := typeinfo.Underlying(info.Type).(*typeinfo.StructType)
	for index, value := range b.operands(values...) {
		name := ""
		if semantic != nil && index < len(semantic.Fields) {
			name = semantic.Fields[index].Name
		}
		fields = append(fields, thir.StructField{Name: name, Index: index, Value: value})
	}
	return &thir.StructLiteral{ExprInfo: info, Fields: fields}
}

func (b *thirBuilder) callExpression(source *ast.CallExpr, info thir.ExprInfo) thir.Expr {
	call := &thir.Call{ExprInfo: info, Callee: b.expression(source.Callee), IsPiped: source.IsPiped}
	arguments := source.Args
	if b.typing != nil {
		arguments = b.typing.CallArgumentsOrSource(source)
		if compilerCall, found := b.typing.CompilerCall(source.ID()); found {
			call.CompilerCall = &compilerCall
		}
	}
	call.Args = b.operands(arguments...)
	return call
}

func (b *thirBuilder) expressionInfo(expression ast.Expr) thir.ExprInfo {
	info := thir.ExprInfo{Source: sourceInfo(expression)}
	if b.typing == nil {
		return info
	}
	info.Type = b.typing.ExprType(expression.ID())
	info.CaseCarrier = b.typing.CaseCarrier(expression.ID())
	info.CasePath = b.typing.CasePath(expression.ID())
	if conversion, found := b.typing.ImplicitConversion(expression.ID()); found {
		copied := conversion
		info.ConversionInfo = &copied
	}
	if use, found := b.typing.ValueUse(expression.ID()); found {
		info.Use = use
		info.HasUse = true
	}
	if isMutable, found := b.typing.ReferenceArgument(expression.ID()); found {
		info.HasReferenceArgument = true
		info.IsReferenceArgumentMutable = isMutable
	}
	info.ImplicitReference = b.typing.ImplicitCallArgument(expression.ID())
	info.Implementations = b.typing.InterfaceImplementations(expression.ID())
	return info
}

// caseTest returns the case test the checker recorded for expression id and
// the subject it reads, or nil for an expression that is not one.
func (b *thirBuilder) caseTest(id sourceid.NodeID) (*thir.CaseTest, ast.Expr) {
	test, found := b.typing.CaseTest(id)
	if !found {
		return nil, nil
	}
	return &thir.CaseTest{
		SubjectID: test.Subject.ID(), Case: test.Case, MatchesWhenTrue: test.MatchesWhenTrue,
		CaseCount: test.CaseCount, Family: test.Family,
	}, test.Subject
}

func projectPlace(base thir.Expr, projection thir.PlaceProjection) *thir.Place {
	if base == nil {
		return nil
	}
	projection.BasePath = base.PayloadPath()
	if existing := base.ExprPlace(); existing != nil {
		projected := &thir.Place{
			Root: existing.Root, Temporary: existing.Temporary,
			Projections: append([]thir.PlaceProjection(nil), existing.Projections...),
		}
		projected.Projections = append(projected.Projections, projection)
		return projected
	}
	return &thir.Place{Temporary: base, Projections: []thir.PlaceProjection{projection}}
}

func (b *thirBuilder) symbol(node ast.Node) *symbols.Symbol {
	if b.symbolIndex == nil || typednil.IsNil(node) {
		return nil
	}
	return b.symbolIndex.Symbol(node)
}

func (b *thirBuilder) scope(node ast.Node) *symbols.Scope {
	if b.symbolIndex == nil || typednil.IsNil(node) {
		return nil
	}
	return b.symbolIndex.Scope(node)
}

func sourceInfo(node ast.Node) ir.SourceInfo {
	if typednil.IsNil(node) {
		return ir.SourceInfo{}
	}
	return ir.SourceInfo{NodeID: node.ID(), Location: ast.LocOf(node)}
}

func stmtInfo(node ast.Node) thir.StmtInfo { return thir.StmtInfo{Source: sourceInfo(node)} }

func isStorageSymbol(symbol *symbols.Symbol) bool {
	if symbol == nil {
		return false
	}
	switch symbol.Kind {
	case symbols.SymbolVar, symbols.SymbolConst, symbols.SymbolParam:
		return true
	default:
		return false
	}
}
