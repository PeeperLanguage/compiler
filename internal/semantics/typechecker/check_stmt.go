package typechecker

import (
	"fmt"
	"slices"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/semantics/typeresolution"
	"compiler/internal/source"
	"compiler/pkg/typednil"
)

func (c *checker) checkBlock(parentScope *symbols.Scope, block *ast.BlockStmt, returnType typeinfo.Type) {
	if block == nil {
		return
	}
	scope := parentScope
	if c.module.SymbolIndex != nil {
		if s := c.module.SymbolIndex.Scope(block); s != nil {
			scope = s
		}
	}
	for _, stmt := range block.Stmts {
		c.checkStmt(scope, stmt, returnType)
		// The rest of a block after a guard has its own scope (resolver).
		if guard, isIf := stmt.(*ast.IfStmt); isIf {
			if rest := c.module.SymbolIndex.Scope(guard); rest != nil {
				scope = rest
			}
		}
	}
}

func (c *checker) checkStmt(scope *symbols.Scope, stmt ast.Stmt, returnType typeinfo.Type) {
	if stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		c.checkBlock(scope, node, returnType)
	case *ast.LetDecl:
		c.checkBinding(scope, node, false)
	case *ast.ConstDecl:
		c.checkBinding(scope, node, true)
	case *ast.DestructureStmt:
		c.checkDestructure(scope, node)
	case *ast.ReturnStmt:
		if node.Value == nil {
			if returnType != nil {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn, "return value required", ast.LocOf(node), "")
			}
			return
		}
		if returnType == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn, "cannot return a value from function with no return type", ast.LocOf(node.Value), "")
			return
		}
		retType := c.requireValueType(node.Value, c.typeExpr(scope, node.Value, returnType), "return")
		if typeinfo.IsInvalidOrUnknown(retType) {
			return
		}
		if c.rejectTemporaryBorrowEscape(scope, node.Value, "return") {
			return
		}
		if !c.isAssignable(returnType, retType, node.Value) {
			d := typeMismatchError(node.Value,
				fmt.Sprintf("cannot return %s from function returning %s",
					typeinfo.TypeText(retType), typeinfo.TypeText(returnType)))
			if fn := c.enclosingFnDecl(scope); fn != nil && fn.ReturnType != nil {
				d.WithSecondaryLabel(ast.LocOf(fn.ReturnType),
					fmt.Sprintf("expected %s here", typeinfo.TypeText(returnType)))
			}
			c.addInterfaceHint(d, returnType, retType)
			c.ctx.Diagnostics.Add(d)
		}
	case *ast.IfStmt:
		if node.Cond == nil {
			return // resolver already diagnosed missing condition
		}
		condType := c.typeExpr(scope, node.Cond, nil)
		if condType != nil && !typeinfo.IsInvalidOrUnknown(condType) && !typeinfo.IsCondition(condType) {
			c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Cond, "if condition must be bool"))
		}
		elseBlock, _ := node.Else.(*ast.BlockStmt)
		c.bindPayloadNames(node.Cond, node.Then, elseBlock, node)
		c.checkStmt(scope, node.Then, returnType)
		c.checkStmt(scope, node.Else, returnType)
	case *ast.ForStmt:
		if node.Iterable != nil {
			c.checkForInStmt(scope, node, returnType)
			return
		}
		if node.Cond != nil {
			condType := c.typeExpr(scope, node.Cond, nil)
			if condType != nil && !typeinfo.IsInvalidOrUnknown(condType) && !typeinfo.IsCondition(condType) {
				c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Cond, "for condition must be bool"))
			}
			c.bindPayloadNames(node.Cond, node.Body, nil, nil)
		}
		c.loopDepth++
		c.checkStmt(scope, node.Body, returnType)
		c.loopDepth--
	case *ast.BreakStmt, *ast.ContinueStmt:
		if c.loopDepth == 0 {
			jump := "break"
			if _, ok := stmt.(*ast.ContinueStmt); ok {
				jump = "continue"
			}
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
				jump+" outside loop", ast.LocOf(stmt), "`"+jump+"` exits or restarts the innermost for loop")
		}
	case *ast.MatchStmt:
		c.checkMatchStmt(scope, node, returnType)
	case *ast.ExprStmt:
		if node.Expr == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
				"expression statement requires an expression", ast.LocOf(node), "")
			return
		}
		c.typeExpr(scope, node.Expr, nil)
	case *ast.AssignStmt:
		c.checkAssign(scope, node)
	case *ast.BadStmt, *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl,
		*ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		return // resolver already diagnosed unsupported statements
	default:
		panic(fmt.Sprintf("typechecker: unhandled statement %T", stmt))
	}
}

// checkDestructure types each new local as the struct field it takes. Without
// a rest marker every field must be named, so a field added to the struct
// later is never dropped unnoticed.
func (c *checker) checkDestructure(scope *symbols.Scope, node *ast.DestructureStmt) {
	valueType := c.requireValueType(node.Value, c.typeExpr(scope, node.Value, nil), "destructure value")
	if typeinfo.IsInvalidOrUnknown(valueType) {
		return
	}
	strct, isStruct := typeinfo.Underlying(valueType).(*typeinfo.StructType)
	if !isStruct {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Value,
			"destructure value must be a struct, got "+typeinfo.TypeText(valueType)))
		return
	}
	c.checkDestructureFields(node.Fields, node.HasRest, strct, node)
}

// checkDestructureFields types the locals that `.{ field, field as name, _ }`
// (a destructure statement or a match pattern at site) makes from strct.
func (c *checker) checkDestructureFields(fields []ast.DestructureField, hasRest bool, strct *typeinfo.StructType, site ast.Node) {
	named := make(map[string]*ast.Ident, len(fields))
	for _, item := range fields {
		field, fieldIndex, found := typeinfo.LookupStructField(strct, item.Name.Name)
		if !found {
			c.ctx.Diagnostics.AddError(diagnostics.ErrFieldNotFound,
				"unknown field `"+item.Name.Name+"`", ast.LocOf(item.Name), "")
			continue
		}
		if previous := named[field.Name]; previous != nil {
			c.ctx.Diagnostics.Add(diagnostics.NewError("duplicate destructure field `"+field.Name+"`").
				WithCode(diagnostics.ErrDuplicateField).
				WithPrimaryLabel(ast.LocOf(item.Name), "duplicate field").
				WithSecondaryLabel(ast.LocOf(previous), "first listed here"))
			continue
		}
		named[field.Name] = item.Name
		c.evidence.RecordStructField(item.Name.ID(), StructFieldAccess{Field: fieldIndex, Type: field.Type})
		if binding := c.module.SymbolIndex.Symbol(item.Binding); binding != nil {
			binding.BindType(field.Type)
		}
	}
	if hasRest {
		return
	}
	for _, field := range strct.Fields {
		if named[field.Name] == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingField,
				"destructure leaves out field `"+field.Name+"`", ast.LocOf(site), "name it or end the list with `_`")
		}
	}
}

func (c *checker) checkMatchStmt(scope *symbols.Scope, node *ast.MatchStmt, returnType typeinfo.Type) {
	if c == nil || scope == nil || node == nil || node.Subject == nil {
		return
	}
	subject := c.caseSubject(scope, node.Subject, "match subject")
	subjectType, borrow := subject.typ, subject.borrow
	if typeinfo.IsInvalidOrUnknown(subjectType) {
		return
	}
	descriptor, isNamed := typeinfo.VariantDescriptorOf(subjectType)
	if !isNamed || descriptor.Family != typeinfo.VariantFamilyNamed {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Subject,
			"match subject must be a named enum, got "+typeinfo.TypeText(subjectType)))
		return
	}
	evidenceComplete := true
	_, isVariable := subject.place.(*ast.Ident)
	spills := !isVariable && borrow == nil && slices.ContainsFunc(node.Arms, func(arm *ast.MatchArm) bool {
		return arm != nil && arm.Pattern != nil
	})
	if typeinfo.OwnershipCapabilityOf(subjectType).NeedsDrop && !place.IsPlaceExpr(subject.place) && !spills {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Subject,
			"ownership-bearing match subject must be a named place").
			Help("bind subject to a local before matching it"))
	}
	evidence := Match{EnumType: subjectType, CaseCount: len(descriptor.Cases), Subject: subject.place, Borrow: borrow, Spills: spills}
	seen := make(map[thir.ArmCase]ast.Node, len(node.Arms))
	// tested holds, per case, the first arm that tests its payload.
	tested := make(map[int]ast.Node)
	// reportMissing is off once a `_` arm takes the rest, or an arm's payload
	// test is wrong and what it covers cannot be told.
	reportMissing := true
	// selectArm records what arm selects and types the names it reads the
	// payload under. It reports whether the arm is one the match can be built
	// from.
	selectArm := func(index int, arm *ast.MatchArm) bool {
		if arm.IsFallback {
			reportMissing = false
			rest := uncoveredArmCases(descriptor, seen, tested)
			isValid := true
			switch {
			case index != len(node.Arms)-1:
				// The arms after it decide what is left, so it selects nothing here.
				isValid = false
				rest = nil
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
					"the `_` arm must be the last arm", ast.LocOf(arm), "it handles every case the arms before it do not")
			case len(rest) == 0:
				isValid = false
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
					"the `_` arm handles nothing", ast.LocOf(arm), "every case already has an arm")
			}
			evidence.ArmCases = append(evidence.ArmCases, rest)
			evidence.ArmPayloads = append(evidence.ArmPayloads, nil)
			return isValid
		}
		resolved, pathOK := c.resolveNamedVariant(arm.Case)
		if !pathOK || typeinfo.IsInvalidOrUnknown(resolved.EnumType) {
			return false
		}
		if !typeinfo.IsSameType(subjectType, resolved.EnumType) {
			c.ctx.Diagnostics.Add(typeMismatchError(arm.Case,
				fmt.Sprintf("match arm requires %s, got %s", typeinfo.TypeText(subjectType), typeinfo.TypeText(resolved.EnumType))))
			return false
		}
		// What the arm selects: one payload case when its pattern tests the
		// payload; otherwise the whole case, or what earlier arms left of it.
		whole := thir.ArmCase{Case: resolved.CaseIndex}
		var selects []thir.ArmCase
		payloadCase, testsPayload, isValid := c.patternPayloadCase(arm.Pattern, resolved.Case)
		switch {
		case !isValid:
			reportMissing = false
			return false
		case testsPayload:
			selects = []thir.ArmCase{{Case: whole.Case, TestsPayload: true, PayloadCase: payloadCase}}
		case tested[whole.Case] != nil:
			for _, left := range uncoveredArmCases(descriptor, seen, tested) {
				if left.Case == whole.Case {
					selects = append(selects, left)
				}
			}
		default:
			selects = []thir.ArmCase{whole}
		}
		// An arm repeats an earlier one that took the whole case, the same
		// payload case, or everything that was left of the case.
		previous := seen[whole]
		if previous == nil && len(selects) == 0 {
			previous = tested[whole.Case]
		}
		if previous == nil && testsPayload {
			previous = seen[selects[0]]
		}
		if previous != nil {
			// Two arms for one case cannot become one switch.
			c.ctx.Diagnostics.Add(diagnostics.NewError("duplicate match arm for `"+resolved.CaseName.Name+"`").
				WithCode(diagnostics.ErrInvalidStatement).
				WithPrimaryLabel(ast.LocOf(arm.Case), "duplicate case").
				WithSecondaryLabel(ast.LocOf(previous), "first matched here"))
		}
		for _, selected := range selects {
			if seen[selected] == nil {
				seen[selected] = arm.Case
			}
		}
		if testsPayload && tested[whole.Case] == nil {
			tested[whole.Case] = arm.Case
		}
		// The arm's names read the payload of what it selects.
		payload := resolved.Case.Payload
		if len(selects) == 1 && selects[0].TestsPayload {
			inner, _ := typeinfo.VariantDescriptorOf(payload)
			payload = inner.Cases[selects[0].PayloadCase].Payload
		}
		c.bindPayloadName(arm, payload, borrow, thir.PayloadPath(selects))
		c.bindPattern(arm.Pattern.Taking(), resolved.Case.Name, payload, borrow)
		evidence.ArmCases = append(evidence.ArmCases, selects)
		evidence.ArmPayloads = append(evidence.ArmPayloads, payload)
		return previous == nil
	}
	for index, arm := range node.Arms {
		if arm == nil || arm.Case == nil && !arm.IsFallback {
			evidenceComplete = false
			continue
		}
		if !selectArm(index, arm) {
			evidenceComplete = false
		}
		c.checkStmt(scope, arm.Body, returnType)
	}
	var missing []string
	if reportMissing {
		for _, armCase := range uncoveredArmCases(descriptor, seen, tested) {
			name := descriptor.Cases[armCase.Case].Name
			if armCase.TestsPayload {
				payload, _ := typeinfo.VariantDescriptorOf(descriptor.Cases[armCase.Case].Payload)
				switch {
				case payload.Family != typeinfo.VariantFamilyOptional:
					name += " with " + typeinfo.TypeText(descriptor.Cases[armCase.Case].Payload) + "::" + payload.Cases[armCase.PayloadCase].Name
				case armCase.PayloadCase == ir.OptionalPresentCase:
					name += " (holding a value)"
				default:
					name += " with none"
				}
			}
			missing = append(missing, "`"+name+"`")
		}
	}
	// One error names every missing case: the bag keeps one error per place.
	if len(missing) > 0 {
		noun := "case"
		if len(missing) > 1 {
			noun = "cases"
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
			"match is missing "+noun+" "+strings.Join(missing, ", "), ast.LocOf(node), "add an arm for each, or a `_` arm for every case left")
	}
	if evidenceComplete && len(evidence.ArmCases) == len(node.Arms) {
		c.evidence.RecordMatch(node.ID(), evidence)
	}
}

// uncoveredArmCases lists what no arm selects yet: whole cases, and for a case
// whose payload some arm tests, the payload cases still open.
func uncoveredArmCases(descriptor typeinfo.VariantDescriptor, seen map[thir.ArmCase]ast.Node, tested map[int]ast.Node) []thir.ArmCase {
	var rest []thir.ArmCase
	for caseIndex, variant := range descriptor.Cases {
		switch {
		case seen[thir.ArmCase{Case: caseIndex}] != nil:
		case tested[caseIndex] == nil:
			rest = append(rest, thir.ArmCase{Case: caseIndex})
		default:
			payload, _ := typeinfo.VariantDescriptorOf(variant.Payload)
			for payloadCase := range payload.Cases {
				armCase := thir.ArmCase{Case: caseIndex, TestsPayload: true, PayloadCase: payloadCase}
				if seen[armCase] == nil {
					rest = append(rest, armCase)
				}
			}
		}
	}
	return rest
}

// patternPayloadCase resolves the test a pattern makes on the payload of
// variant (`none`, a case of the payload's enum, or taking the value out of
// an optional payload) to a case of that payload. tests is false for a
// pattern that only names things.
func (c *checker) patternPayloadCase(pattern *ast.Pattern, variant typeinfo.VariantCase) (payloadCase int, tests, isValid bool) {
	if pattern == nil {
		return 0, false, true
	}
	if !pattern.IsNone && pattern.Case == nil {
		// As in construction, `Case with value` on an optional payload is the
		// payload holding a value: the pattern takes that value.
		return ir.OptionalPresentCase, typeinfo.IsOptional(variant.Payload), true
	}
	holds := "has no payload"
	if variant.Payload != nil {
		holds = "holds " + typeinfo.TypeText(variant.Payload)
	}
	if pattern.IsNone {
		if !typeinfo.IsOptional(variant.Payload) {
			c.ctx.Diagnostics.Add(invalidOperationError(pattern,
				fmt.Sprintf("`with none` needs an optional payload, but `%s` %s", variant.Name, holds)))
			return 0, false, false
		}
		return ir.OptionalAbsentCase, true, true
	}
	resolved, isCase := c.resolveNamedVariant(pattern.Case)
	if !isCase {
		return 0, false, false
	}
	if variant.Payload == nil || !typeinfo.IsSameType(variant.Payload, resolved.EnumType) {
		c.ctx.Diagnostics.Add(typeMismatchError(pattern.Case,
			fmt.Sprintf("`with %s` does not fit: `%s` %s", typeinfo.TypeText(resolved.EnumType), variant.Name, holds)))
		return 0, false, false
	}
	if inner := pattern.Inner; inner != nil && (inner.IsNone || inner.Case != nil) {
		c.ctx.Diagnostics.Add(invalidOperationError(inner, "a pattern tests one level below its case").
			Help("name the payload and test it in the arm"))
		return 0, false, false
	}
	return resolved.CaseIndex, true, true
}

// bindPattern types the names a pattern declares for payload, the payload of
// what its arm selects in the case named caseName: one name for the whole
// payload, or a local per field of a struct payload. With a borrowed subject
// a name is a reference; fields cannot be taken out of a borrowed value.
func (c *checker) bindPattern(taking *ast.Pattern, caseName string, payload typeinfo.Type, borrow *typeinfo.RefType) {
	switch {
	case taking == nil:
	case payload == nil:
		c.ctx.Diagnostics.Add(invalidOperationError(taking, "`"+caseName+"` has no payload to name"))
	case taking.Name != nil:
		c.module.SymbolIndex.Symbol(taking.Name).BindType(caseNameType(payload, borrow))
	case borrow != nil:
		c.ctx.Diagnostics.Add(invalidOperationError(taking, "a `.{ }` pattern takes the fields out of the value, which a borrowed subject keeps").
			Help("leave the pattern out and read the fields through the subject's name"))
	default:
		strct, isStruct := typeinfo.Underlying(payload).(*typeinfo.StructType)
		if !isStruct {
			c.ctx.Diagnostics.Add(invalidOperationError(taking, "a `.{ }` pattern needs a struct payload, got "+typeinfo.TypeText(payload)))
			return
		}
		c.checkDestructureFields(taking.Fields, taking.HasRest, strct, taking)
	}
}

// testedSubject is what a case test, a match or `orelse` reads: the value
// tested and its type. For `&place` and `&mut place` that is the place, and
// borrow is the reference a payload name becomes.
type testedSubject struct {
	place  ast.Expr
	typ    typeinfo.Type
	borrow *typeinfo.RefType
}

// caseSubject types the subject of a case test, a match or `orelse`.
func (c *checker) caseSubject(scope *symbols.Scope, subject ast.Expr, what string) testedSubject {
	tested := testedSubject{place: subject}
	address, isAddress := subject.(*ast.AddressExpr)
	if isAddress && address.Mode != ast.AddressRaw {
		tested.place = address.Expr
	}
	// Testing a payload name again tests the value it was read from.
	previous := c.wholeCarrierExpr
	c.wholeCarrierExpr = tested.place
	tested.typ = c.requireValueType(subject, c.typeExpr(scope, subject, nil), what)
	c.wholeCarrierExpr = previous
	if tested.place == subject || typeinfo.IsInvalidOrUnknown(tested.typ) {
		return tested
	}
	// Names read the payload through the place again, so the place must be one
	// that reads the same twice: a variable, its fields, and elements picked
	// by a number or a variable.
	for borrowed := tested.place; ; {
		switch part := borrowed.(type) {
		case *ast.SelectorExpr:
			borrowed = part.Expr
			continue
		case *ast.IndexExpr:
			_, isNumber := part.Index.(*ast.NumberLit)
			_, isVariable := part.Index.(*ast.Ident)
			if isNumber || isVariable {
				borrowed = part.Expr
				continue
			}
		case *ast.Ident:
			target, isMutable, _ := typeinfo.ReferenceTarget(tested.typ)
			tested.typ, tested.borrow = target, &typeinfo.RefType{IsMutable: isMutable}
			return tested
		}
		c.ctx.Diagnostics.Add(invalidOperationError(tested.place, "a borrowed subject must be a variable, a field or an element of one").
			Help("bind the value to a local first"))
		tested.typ = &typeinfo.InvalidType{}
		return tested
	}
}

// caseNameType is the type of a name given to payload: the payload itself, or
// a reference to it when the subject is borrowed.
func caseNameType(payload typeinfo.Type, borrow *typeinfo.RefType) typeinfo.Type {
	if borrow == nil {
		return payload
	}
	return &typeinfo.RefType{IsMutable: borrow.IsMutable, Target: payload}
}

// bindPayloadNames types the payload names the resolver declared for the
// blocks a test guards: then (an `if` block or a loop body), otherwise (an
// else block), and rest (the guard, for the code after an `if` whose block
// always leaves). An owner that is nil or has no name is skipped.
func (c *checker) bindPayloadNames(cond ast.Expr, then, otherwise *ast.BlockStmt, rest *ast.IfStmt) {
	test, tested := c.evidence.CaseTest(cond.ID())
	if !tested {
		return
	}
	descriptor, _ := typeinfo.VariantDescriptorOf(c.evidence.ExprType(test.Subject.ID()))
	if test.Case >= len(descriptor.Cases) {
		return
	}
	borrow := test.Borrow
	// held is the case when the test is true, other when it is false: known
	// only for a value with two cases.
	held, other := test.Case, -1
	if len(descriptor.Cases) == 2 {
		other = 1 - test.Case
	}
	if !test.MatchesWhenTrue {
		held, other = other, held
	}
	if held >= 0 {
		c.bindPayloadName(then, descriptor.Cases[held].Payload, borrow, []int{held})
	}
	if other >= 0 {
		c.bindPayloadName(otherwise, descriptor.Cases[other].Payload, borrow, []int{other})
		c.bindPayloadName(rest, descriptor.Cases[other].Payload, borrow, []int{other})
	}
}

// bindPayloadName types the payload name the resolver bound to owner, if it
// declared one and the case has a payload; path is the case read through at
// each level to reach that payload.
func (c *checker) bindPayloadName(owner ast.Node, payload typeinfo.Type, borrow *typeinfo.RefType, path []int) {
	if typednil.IsNil(owner) {
		return
	}
	if name := c.module.SymbolIndex.Symbol(owner); name != nil && name.IsPayloadName && payload != nil {
		name.BindType(caseNameType(payload, borrow))
		name.PayloadPath = path
	}
}

// typeOrElse types `value orelse fallback` or `value orelse { ... }`: the
// payload of the optional value. The fallback has that type; the block must
// leave.
func (c *checker) typeOrElse(scope *symbols.Scope, node *ast.OrElseExpr) typeinfo.Type {
	if scope == c.module.ModuleScope {
		c.ctx.Diagnostics.Add(invalidOperationError(node, "`orelse` is only allowed inside a function"))
		return &typeinfo.InvalidType{}
	}
	tested := c.caseSubject(scope, node.Value, "`orelse` value")
	if typeinfo.IsInvalidOrUnknown(tested.typ) {
		return &typeinfo.InvalidType{}
	}
	descriptor, _ := typeinfo.VariantDescriptorOf(tested.typ)
	if descriptor.Family != typeinfo.VariantFamilyOptional {
		c.ctx.Diagnostics.Add(typeMismatchError(node.Value, "`orelse` needs an optional value, got "+typeinfo.TypeText(tested.typ)))
		return &typeinfo.InvalidType{}
	}
	// `orelse` tests its value for none.
	c.recordCaseTest(node, tested, ir.OptionalPresentCase, len(descriptor.Cases), true, descriptor.Family)
	payload := caseNameType(descriptor.Cases[ir.OptionalPresentCase].Payload, tested.borrow)
	if node.Block != nil {
		c.checkStmt(scope, node.Block, c.returnType)
		return payload
	}
	fallbackType := c.typeExpr(scope, node.Fallback, payload)
	if !typeinfo.IsInvalidOrUnknown(fallbackType) && !c.isAssignable(payload, fallbackType, node.Fallback) {
		c.ctx.Diagnostics.Add(typeMismatchError(node.Fallback,
			fmt.Sprintf("fallback must be %s, got %s", typeinfo.TypeText(payload), typeinfo.TypeText(fallbackType))))
	}
	return payload
}

func (c *checker) checkAssign(scope *symbols.Scope, node *ast.AssignStmt) {
	if c == nil || scope == nil || node == nil || node.Target == nil || node.Value == nil {
		return
	}
	targetType := c.typeWholeCarrierExpr(scope, node.Target)
	if targetType == nil || typeinfo.IsInvalidOrUnknown(targetType) {
		return
	}
	valueType := c.typeExpr(scope, node.Value, targetType)
	valueType = c.requireValueType(node.Value, valueType, "assignment")
	if typeinfo.IsInvalidOrUnknown(valueType) {
		return
	}
	if c.rejectTemporaryBorrowEscape(scope, node.Value, "assignment") {
		return
	}
	if !c.isAssignable(targetType, valueType, node.Value) {
		c.ctx.Diagnostics.Add(typeMismatchError(node.Value,
			fmt.Sprintf("cannot assign %s to %s",
				typeinfo.TypeText(valueType), typeinfo.TypeText(targetType))))
		return
	}
	switch target := node.Target.(type) {
	case *ast.Ident:
		var sym *symbols.Symbol
		if c.module != nil && c.module.SymbolIndex != nil {
			sym = c.module.SymbolIndex.Symbol(target)
		}
		if sym == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol,
				"unknown assignment target `"+target.Name+"`", ast.LocOf(target), "")
			return
		}
		switch sym.Kind {
		case symbols.SymbolConst:
			c.ctx.Diagnostics.AddError(diagnostics.ErrConstantReassignment,
				"cannot assign to const `"+target.Name+"`", ast.LocOf(target), "").
				WithSecondaryLabel(sym.Location, "declared as const here")
			return
		case symbols.SymbolVar, symbols.SymbolParam:
			if !sym.IsMutable() {
				c.ctx.Diagnostics.AddError(
					diagnostics.ErrInvalidAssignment,
					"modification to immutable symbol",
					ast.LocOf(target),
					"cannot assign to immutable binding `"+target.Name+"`",
				).WithSecondaryLabel(sym.Location, "make this binding mutable")
				return
			}
			c.module.SymbolIndex.RequireMutable(sym)
		default:
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
				"invalid assignment target `"+target.Name+"`", ast.LocOf(target), "")
			return
		}
	case *ast.SelectorExpr:
		isMutable, sharedReference, mutableBinding := c.mutableAddressableExpr(scope, target)
		if isMutable {
			if mutableBinding != nil {
				c.module.SymbolIndex.RequireMutable(mutableBinding)
			}
			return
		}
		if sharedReference != nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
				"cannot assign through immutable reference", ast.LocOf(target), "").
				Help(fmt.Sprintf("use `&mut %s` to modify referenced value", typeinfo.TypeText(sharedReference)))
			return
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"field assignment requires a mutable pointer, reference, or local binding", ast.LocOf(target), "")
		return
	case *ast.IndexExpr:
		if c.checkIndexAssignmentTarget(scope, target, targetType) {
			return
		}
		return
	default:
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"invalid assignment target", ast.LocOf(node.Target), "")
	}
}

func (c *checker) checkIndexAssignmentTarget(scope *symbols.Scope, target *ast.IndexExpr, targetType typeinfo.Type) bool {
	if c == nil || target == nil || target.Expr == nil {
		return false
	}
	if typeinfo.IsInvalidOrUnknown(targetType) {
		return true
	}
	baseType := c.exprType(target.Expr.ID())
	if typeinfo.IsInvalidOrUnknown(baseType) {
		return true
	}
	_, shape, ok := indexableSequence(baseType)
	if !ok {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"index assignment requires array or slice value", ast.LocOf(target), "")
		return false
	}
	if shape == indexableSharedSliceView {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"index assignment requires mutable array or mutable slice view", ast.LocOf(target), "")
		return false
	}
	if isMutable, _, mutableBinding := c.mutableAddressableExpr(scope, target); isMutable {
		if mutableBinding != nil {
			c.module.SymbolIndex.RequireMutable(mutableBinding)
		}
		return true
	}
	c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
		"index assignment requires mutable array or slice binding", ast.LocOf(target), "")
	return false
}

func (c *checker) checkBinding(scope *symbols.Scope, node ast.Stmt, requireInitializer bool) {
	if c == nil || node == nil {
		return
	}
	var (
		declType typeinfo.Type
		typeNode ast.TypeExpr // AST node for the type annotation (for diagnostics)
		name     *ast.Ident
		value    ast.Expr
	)
	switch bind := node.(type) {
	case *ast.LetDecl:
		declType = c.ctx.TypeResolver.Resolve(c.ctx.Diagnostics, c.module, bind.Type, typeresolution.Context{})
		typeNode = bind.Type
		name = bind.Name
		value = bind.Value
	case *ast.ConstDecl:
		declType = c.ctx.TypeResolver.Resolve(c.ctx.Diagnostics, c.module, bind.Type, typeresolution.Context{})
		typeNode = bind.Type
		name = bind.Name
		value = bind.Value
	default:
		return
	}

	if scope == nil || name == nil || c.module.SymbolIndex == nil {
		return
	}
	sym := c.module.SymbolIndex.Symbol(name)
	if sym == nil {
		return
	}
	// Checked expansions may publish a generated declaration occurrence for an
	// already-existing source symbol. In that case the source declaration owns
	// the binding type; the generated occurrence must check its initializer
	// against that type instead of re-inferring and overwriting the symbol.
	if declType == nil && sym.ASTNode != nil && sym.ASTNode != node {
		if established, ok := symbols.GetSymbolType(sym); ok && established != nil && !typeinfo.IsInvalidOrUnknown(established) {
			declType = established
		}
	}
	if declType != nil && c.rejectUnsizedType(declType, typeNode, "binding") {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}

	if value == nil {
		if requireInitializer {
			sym.BindType(&typeinfo.InvalidType{})
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingInitializer,
				"missing initializer for const declaration", ast.LocOf(node), "")
			return
		}
		if declType == nil {
			sym.BindType(&typeinfo.InvalidType{})
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingType,
				"let declaration needs type or initializer", ast.LocOf(node), "")
			return
		}
		if c.rejectBindingReferenceStorage(scope, declType, typeNode) {
			sym.BindType(&typeinfo.InvalidType{})
			return
		}
		sym.BindType(declType)
		return
	}
	if declType != nil && c.rejectBindingReferenceStorage(scope, declType, typeNode) {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}

	valType := c.typeExpr(scope, value, declType)
	valType = c.requireValueType(value, valType, "initializer")
	if typeinfo.IsInvalidOrUnknown(valType) {
		if declType != nil && !typeinfo.IsInvalidOrUnknown(declType) {
			sym.BindType(declType)
		} else {
			sym.BindType(&typeinfo.InvalidType{})
		}
		return
	}
	if c.rejectTemporaryBorrowEscape(scope, value, "binding") {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}
	if declType == nil && c.rejectBindingReferenceStorage(scope, valType, node) {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}
	if declType == nil && c.rejectUnsizedType(valType, node, "binding") {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}
	if declType != nil && !c.isAssignable(declType, valType, value) {
		d := typeMismatchError(value,
			fmt.Sprintf("cannot assign %s to %s",
				typeinfo.TypeText(valType), typeinfo.TypeText(declType)))
		if typeNode != nil {
			d.WithSecondaryLabel(ast.LocOf(typeNode),
				fmt.Sprintf("expected %s because of this type annotation", typeinfo.TypeText(declType)))
		}
		c.addInterfaceHint(d, declType, valType)
		c.ctx.Diagnostics.Add(d)
		if !typeinfo.IsInvalidOrUnknown(declType) {
			sym.BindType(declType)
		} else {
			sym.BindType(&typeinfo.InvalidType{})
		}
		return
	}
	if declType != nil {
		sym.BindType(declType)
	} else {
		sym.BindType(valType)
	}
}

// checkForInStmt types a `for x in iterable` loop over a range, indexable
// sequence, or optional-producing call. Strings require an explicit
// as_bytes/as_chars view.
func (c *checker) checkForInStmt(scope *symbols.Scope, node *ast.ForStmt, returnType typeinfo.Type) {
	indexType := typeinfo.DefaultIntegerType()
	evidence := ForIteration{}
	if node.Index != nil {
		evidence.Index = c.module.SymbolIndex.Symbol(node.Index)
	}
	if node.Value != nil {
		evidence.Value = c.module.SymbolIndex.Symbol(node.Value)
	}
	valid := node.Value != nil && node.Value.Name != "" && evidence.Value != nil
	if node.Index != nil && (node.Index.Name == "" || evidence.Index == nil) {
		valid = false
	}

	var elemType typeinfo.Type
	var carrierType typeinfo.Type
	rangeExpr, isRange := node.Iterable.(*ast.RangeExpr)
	if isRange {
		elemType, valid, evidence.HasGuaranteedEntry = c.checkRangeIteration(scope, rangeExpr, valid)
		evidence.ElementType = elemType
	} else {
		var ok bool
		indexType, ok = typeinfo.NumericTypeFromName("usize", c.ctx.Target)
		if !ok {
			panic("missing builtin usize type")
		}
		iterableType := c.typeExpr(scope, node.Iterable, nil)
		iterableType = c.requireValueType(node.Iterable, iterableType, "iterable")

		if typeinfo.IsInvalidOrUnknown(iterableType) {
			valid = false
		} else if isStringSequence(iterableType) {
			valid = false
			c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable,
				"string iteration requires `value |> as_bytes()` or `value |> as_chars()`"))
		} else if elem, shape, ok := indexableSequence(iterableType); ok {
			elemType = elem
			if shape == indexableFixedArray || shape == indexableDynamicArray {
				exprType := func(expr ast.Expr) typeinfo.Type {
					return c.typeExpr(scope, expr, nil)
				}
				if !place.IsAddressable(scope, node.Iterable, exprType, c.expandedDefaultBinding) {
					valid = false
					c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable,
						"for-in requires addressable array storage"))
				}
			}
			if typeinfo.OwnershipCapabilityOf(elem).Copy != typeinfo.CopyImplicit {
				valid = false
				c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable,
					"for-in requires copyable sequence elements; iterate indexes and borrow move-only elements explicitly"))
			}
			if _, isArray := typeinfo.Underlying(iterableType).(*typeinfo.ArrayType); isArray {
				carrierType = &typeinfo.RefType{Target: iterableType}
			} else {
				carrierType = iterableType
			}
			evidence.ElementType = elem
		} else {
			if valid && c.checkCallIteration(scope, node, iterableType, returnType) {
				return
			}
			valid = false
		}
	}
	if node.Index != nil {
		c.bindLoopVariable(node.Index, indexType)
	}
	if node.Value != nil {
		c.bindLoopVariable(node.Value, elemType)
	}
	c.evidence.ForgetForIteration(node.ID())
	if valid && elemType != nil && !typeinfo.IsInvalidOrUnknown(elemType) {
		location := ast.LocOf(node)
		evidence.Cursor = symbols.New(symbols.GeneratedSymbolID(node.ID(), symbols.GeneratedForCursor), "$for.cursor", symbols.SymbolVar, nil, location)
		if isRange {
			evidence.Cursor.BindType(elemType)
			plan := &RangeIteration{
				Limit: symbols.New(symbols.GeneratedSymbolID(node.ID(), symbols.GeneratedForRangeLimit), "$for.end", symbols.SymbolVar, nil, location),
			}
			plan.Limit.BindType(elemType)
			if node.Index != nil {
				plan.Ordinal = symbols.New(symbols.GeneratedSymbolID(node.ID(), symbols.GeneratedForRangeOrdinal), "$for.ordinal", symbols.SymbolVar, nil, location)
				plan.Ordinal.BindType(indexType)
			}
			evidence.Plan = plan
		} else {
			evidence.Cursor.BindType(indexType)
			carrier := symbols.New(symbols.GeneratedSymbolID(node.ID(), symbols.GeneratedForSequenceCarrier), "$for.carrier", symbols.SymbolVar, nil, location)
			carrier.BindType(carrierType)
			evidence.Plan = &SequenceIteration{Carrier: carrier, CarrierType: carrierType}
		}
		c.evidence.RecordForIteration(node.ID(), evidence)
	}
	c.loopDepth++
	c.checkStmt(scope, node.Body, returnType)
	c.loopDepth--
}

// checkRangeIteration types the bounds of `for x in a..b`. It returns the
// element type, whether the loop is still valid, and whether constant bounds
// prove the body runs at least once.
func (c *checker) checkRangeIteration(scope *symbols.Scope, rangeExpr *ast.RangeExpr, valid bool) (elemType typeinfo.Type, stillValid, hasGuaranteedEntry bool) {
	if !rangeExpr.IsEndExclusive {
		valid = false
		c.ctx.Diagnostics.Add(invalidExpressionError(rangeExpr, "for range requires an exclusive end; use `..` instead of `..=`"))
	}
	_, badStart := rangeExpr.Start.(*ast.BadExpr)
	if rangeExpr.Start == nil || badStart {
		valid = false
		c.ctx.Diagnostics.Add(invalidExpressionError(rangeExpr, "range iteration requires a start bound"))
	}
	_, badEnd := rangeExpr.End.(*ast.BadExpr)
	if rangeExpr.End == nil || badEnd {
		valid = false
		c.ctx.Diagnostics.Add(invalidExpressionError(rangeExpr, "range iteration requires an end bound"))
	}

	defaultType := typeinfo.DefaultIntegerType()
	startNumber, startLiteral := rangeExpr.Start.(*ast.NumberLit)
	endNumber, endLiteral := rangeExpr.End.(*ast.NumberLit)
	startUntyped := startLiteral && startNumber.ExplicitType == ""
	endUntyped := endLiteral && endNumber.ExplicitType == ""
	var startType, endType typeinfo.Type
	if startUntyped && !endUntyped {
		endType = c.checkRangeBound(scope, rangeExpr.End, defaultType)
		startType = c.checkRangeBound(scope, rangeExpr.Start, endType)
	} else {
		startType = c.checkRangeBound(scope, rangeExpr.Start, defaultType)
		endExpected := defaultType
		if endUntyped && !typeinfo.IsInvalidOrUnknown(startType) {
			endExpected = startType
		}
		endType = c.checkRangeBound(scope, rangeExpr.End, endExpected)
	}
	if typeinfo.IsInvalidOrUnknown(startType) || typeinfo.IsInvalidOrUnknown(endType) ||
		!typeinfo.IsIntegral(startType) || !typeinfo.IsIntegral(endType) {
		valid = false
	} else {
		elemType = typeinfo.CommonNumericType(startType, endType)
		if elemType == nil || !typeinfo.IsIntegral(elemType) {
			valid = false
			c.ctx.Diagnostics.Add(typeMismatchError(rangeExpr,
				"range bounds of type "+typeinfo.TypeText(startType)+" and "+typeinfo.TypeText(endType)+" have no common integer type"))
		} else {
			if !c.isAssignable(elemType, startType, rangeExpr.Start) || !c.isAssignable(elemType, endType, rangeExpr.End) {
				valid = false
			}
		}
	}
	if valid {
		startValue, startFound := c.evaluateConstant(c.ctx, rangeExpr.Start, elemType)
		endValue, endFound := c.evaluateConstant(c.ctx, rangeExpr.End, elemType)
		start, startIntegral := startValue.(*constvalue.IntConst)
		end, endIntegral := endValue.(*constvalue.IntConst)
		if startFound && endFound && startIntegral && endIntegral && start.Int().Cmp(end.Int()) < 0 {
			hasGuaranteedEntry = true
		}
	}
	return elemType, valid, hasGuaranteedEntry
}

// checkCallIteration handles `for item in producer()`. It reports true when it
// expanded the loop and checked the expansion, so the caller has nothing left
// to do; false means the iterable is not a usable producer call.
func (c *checker) checkCallIteration(scope *symbols.Scope, node *ast.ForStmt, iterableType, returnType typeinfo.Type) bool {
	call, isCallExpression := node.Iterable.(*ast.CallExpr)
	optional, isOptionalResultType := typeinfo.Underlying(iterableType).(*typeinfo.OptionalType)
	switch {
	case !isCallExpression:
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable, "cannot iterate over "+typeinfo.TypeText(iterableType)).
			Help("use a range, array, slice, or an explicit call returning an optional item, such as `for item in producer()`").
			Note("iterator calls, including arguments, are evaluated on every attempt; a bare optional value is not a producer"))
	case !isOptionalResultType || optional.Inner == nil:
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable, "iterator call must return an optional item").
			Help("return an optional type, such as `?i32`: return an item to continue, or `none` to end the loop"))
	case node.Index != nil:
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Index, "iterator loops provide an item, not an index").
			Help("use `for item in producer()`; if you need an index, maintain a separate counter"))
	default:
		c.expandCallIteration(scope, node)
	}
	if checked := c.evidence.CheckedIteration(node.ID()); checked != nil {
		c.bindLoopVariable(node.Value, optional.Inner)
		previous := c.reusedCall
		c.reusedCall = call
		c.checkStmt(scope, checked, returnType)
		c.reusedCall = previous
		return true
	}
	return false
}

// expandCallIteration publishes ordinary checked statements before flow and
// ownership. Keeping the entire source call inside the loop repeats its receiver
// and arguments on every attempt, including the terminating attempt.
func (c *checker) expandCallIteration(scope *symbols.Scope, node *ast.ForStmt) {
	location := ast.LocOf(node)
	var generatedOrdinal uint64
	nextGeneratedID := func() ast.NodeIDHolder {
		generatedOrdinal++
		id := source.GeneratedNodeID(node.ID(), source.GeneratedCheckedIteration, 0, generatedOrdinal)
		if !id.IsValid() {
			panic("checked iteration produced invalid generated identity")
		}
		return ast.NodeIDHolder{NodeID: id}
	}
	// for { let item = producer() orelse { break; }; body }
	stop := &ast.BlockStmt{
		NodeIDHolder: nextGeneratedID(),
		Stmts:        []ast.Stmt{&ast.BreakStmt{NodeIDHolder: nextGeneratedID(), Location: location}},
		Location:     location,
	}
	item := &ast.LetDecl{
		NodeIDHolder: nextGeneratedID(),
		Name:         &ast.Ident{NodeIDHolder: nextGeneratedID(), Name: node.Value.Name, Location: location},
		Value:        &ast.OrElseExpr{NodeIDHolder: nextGeneratedID(), Value: node.Iterable, Block: stop, Location: location},
		Location:     location,
	}
	checked := &ast.ForStmt{
		NodeIDHolder: nextGeneratedID(),
		Body: &ast.BlockStmt{
			NodeIDHolder: nextGeneratedID(),
			Stmts:        []ast.Stmt{item, node.Body},
			Location:     location,
		},
		Location: location,
	}
	expansion := &ast.BlockStmt{NodeIDHolder: nextGeneratedID(), Stmts: []ast.Stmt{checked}, Location: location}
	expansionScope := symbols.NewScope(scope)
	c.module.SymbolIndex.SetScope(expansion, expansionScope)
	iterationScope := symbols.NewScope(expansionScope)
	c.module.SymbolIndex.SetScope(checked.Body, iterationScope)
	c.module.SymbolIndex.SetScope(stop, symbols.NewScope(iterationScope))
	c.module.SymbolIndex.Bind(item.Name, c.module.SymbolIndex.Symbol(node.Value))

	c.evidence.RecordCheckedIteration(node.ID(), expansion)
}

func (c *checker) bindLoopVariable(name *ast.Ident, typ typeinfo.Type) {
	if typ == nil {
		return
	}
	if sym := c.module.SymbolIndex.Symbol(name); sym != nil {
		sym.BindType(typ)
	}
}

func (c *checker) rejectUnsizedType(typ typeinfo.Type, site ast.Node, context string) bool {
	if typeinfo.IsSizedType(typ) {
		return false
	}
	// A type with an invalid part is rejected by the error already reported
	// for that part; saying it has no size would only repeat it.
	if typeinfo.ContainsInvalid(typ) {
		return true
	}
	diagnostic := invalidTypeError(site,
		fmt.Sprintf("%s requires a sized type; %s is unsized", context, typeinfo.TypeText(typ)))
	if _, ok := typeinfo.Underlying(typ).(*typeinfo.InterfaceType); ok {
		diagnostic.Help("use &Interface, &mut Interface, or *Interface instead of a bare interface value")
	}
	c.ctx.Diagnostics.Add(diagnostic)
	return true
}

func (c *checker) rejectBindingReferenceStorage(scope *symbols.Scope, typ typeinfo.Type, site ast.Node) bool {
	moduleBinding := c != nil && c.module != nil && scope == c.module.ModuleScope
	if !moduleBinding {
		if descriptor, isVariant := typeinfo.VariantDescriptorOf(typ); isVariant && descriptor.Family == typeinfo.VariantFamilyNamed {
			for _, variantCase := range descriptor.Cases {
				payload, _ := typeinfo.Underlying(variantCase.Payload).(*typeinfo.StructType)
				if payload == nil {
					continue
				}
				for _, field := range payload.Fields {
					if c.rejectReferenceStorage(field.Type, site, "named enum payloads", false) {
						return true
					}
				}
			}
			return false
		}
	}
	context := "array or heap-owned values"
	if moduleBinding {
		context = "module bindings"
	}
	return c.rejectReferenceStorage(typ, site, context, moduleBinding)
}

func (c *checker) rejectReferenceStorage(typ typeinfo.Type, site ast.Node, context string, includeDirectReference bool) bool {
	rejected := typeinfo.ContainsStoredReference(typ)
	if includeDirectReference {
		rejected = typeinfo.ContainsReference(typ)
	}
	if !rejected {
		return false
	}
	c.ctx.Diagnostics.Add(invalidTypeError(site,
		fmt.Sprintf("references cannot be stored in %s in v1", context)))
	return true
}

func (c *checker) rejectTemporaryBorrowEscape(scope *symbols.Scope, expr ast.Expr, context string) bool {
	if c.module == nil {
		return false
	}
	temporary := c.temporaryBorrowSource(scope, expr)
	if temporary == nil {
		return false
	}
	diagnostic := c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
		fmt.Sprintf("reference to temporary cannot escape through %s", context), ast.LocOf(expr), "temporary borrow escapes here").
		Help("pass the borrow directly to a call so it ends with the full expression")
	if temporary != expr {
		diagnostic.WithSecondaryLabel(ast.LocOf(temporary), "temporary borrowed here")
	}
	return true
}

func (c *checker) temporaryBorrowSource(scope *symbols.Scope, expr ast.Expr) ast.Expr {
	if c == nil || c.module == nil || expr == nil {
		return nil
	}
	exprType := func(node ast.Expr) typeinfo.Type {
		if node == nil {
			return nil
		}
		return c.exprType(node.ID())
	}
	if _, _, isReference := typeinfo.ReferenceValueTarget(exprType(expr)); !isReference {
		return nil
	}
	switch node := expr.(type) {
	case *ast.AddressExpr:
		if node == nil || node.Expr == nil || node.Mode == ast.AddressRaw || place.IsAddressable(scope, node.Expr, exprType, c.expandedDefaultBinding) {
			return nil
		}
		return node
	case *ast.CallExpr:
		fn, _ := typeinfo.Underlying(exprType(node.Callee)).(*typeinfo.FuncType)
		args := c.evidence.CallArgumentsOrSource(node)
		for _, source := range typeinfo.ReturnOriginSources(node, args, fn) {
			if temporary := c.temporaryBorrowSource(scope, source); temporary != nil {
				return temporary
			}
			if c.evidence.ImplicitCallArgument(source.ID()) != nil && !place.IsAddressable(scope, source, exprType, c.expandedDefaultBinding) {
				if _, _, isReference := typeinfo.ReferenceValueTarget(exprType(source)); !isReference {
					return source
				}
			}
		}
	case *ast.AsExpr:
		return c.temporaryBorrowSource(scope, node.Expr)
	case *ast.SelectorExpr:
		if temporary := c.temporaryBorrowSource(scope, node.Expr); temporary != nil {
			return temporary
		}
		if !place.IsAddressable(scope, node.Expr, exprType, c.expandedDefaultBinding) {
			return node
		}
	case *ast.IndexExpr:
		if temporary := c.temporaryBorrowSource(scope, node.Expr); temporary != nil {
			return temporary
		}
		if !place.IsAddressable(scope, node.Expr, exprType, c.expandedDefaultBinding) {
			return node
		}
	}
	return nil
}
