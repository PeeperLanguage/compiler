package mir

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"compiler/internal/ir"
	"compiler/pkg/typednil"
)

const maxReportedProblems = 10

// Validate checks backend-independent MIR invariants required before emission.
// It validates identity/control shape plus type references and value/place
// relationships already decided by lowering. Target-specific physical layout
// remains backend-owned.
func (m *Module) Validate() error {
	if m == nil {
		return nil
	}
	problems := make([]string, 0)
	hasTypedArtifacts := len(m.Funcs) > 0 || len(m.InterfaceThunks) > 0
	for index, entry := range m.StaticData {
		if entry == nil {
			problems = append(problems, fmt.Sprintf("module holds a nil static entry at %d", index))
			continue
		}
		if entry.Constant != nil {
			hasTypedArtifacts = true
			if m.Types != nil {
				problems = append(problems, validateKnownType(m.Types, entry.Type, fmt.Sprintf("static entry %s", entry.Name))...)
			}
		}
	}
	if m.Types == nil {
		if hasTypedArtifacts {
			problems = append(problems, "module with typed MIR artifacts has no type table")
		}
	} else {
		for index, thunk := range m.InterfaceThunks {
			if thunk == nil {
				problems = append(problems, fmt.Sprintf("module holds a nil interface thunk at %d", index))
				continue
			}
			for role, id := range map[string]ir.TypeID{
				"interface": thunk.InterfaceType, "slot": thunk.SlotType, "function": thunk.FuncType, "data": thunk.DataType,
			} {
				if _, ok := m.Types.Type(id); !ok {
					problems = append(problems, fmt.Sprintf("interface thunk %s has invalid %s type#%d", thunk.Name, role, id))
				}
			}
			if method, ok := m.Types.InterfaceMethod(thunk.InterfaceType, thunk.Slot); !ok {
				problems = append(problems, fmt.Sprintf("interface thunk %s targets invalid slot %d on type#%d", thunk.Name, thunk.Slot, thunk.InterfaceType))
			} else if method.SlotType != thunk.SlotType {
				problems = append(problems, fmt.Sprintf("interface thunk %s has slot type#%d, want published type#%d", thunk.Name, thunk.SlotType, method.SlotType))
			}
			problems = append(problems, validateInterfaceSlotType(m.Types, thunk.SlotType, "interface thunk "+thunk.Name)...)
		}
	}
	for _, fn := range m.Funcs {
		if fn == nil {
			problems = append(problems, "module holds a nil function")
			continue
		}
		problems = append(problems, validateFunction(m.Types, fn)...)
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	if len(problems) > maxReportedProblems {
		return fmt.Errorf("%s (%d more)", strings.Join(problems[:maxReportedProblems], "; "), len(problems)-maxReportedProblems)
	}
	return errors.New(strings.Join(problems, "; "))
}

func validateFunction(types *ir.TypeTable, fn *Function) []string {
	problems := make([]string, 0)
	if types != nil {
		if _, ok := types.Type(fn.ReturnType); !ok {
			problems = append(problems, fmt.Sprintf("function %s has invalid return type#%d", fn.Name, fn.ReturnType))
		}
		for index, param := range fn.Params {
			if _, ok := types.Type(param.Type); !ok {
				problems = append(problems, fmt.Sprintf("function %s parameter %d has invalid type#%d", fn.Name, index, param.Type))
			}
		}
	}
	if fn.Blocks == nil {
		return problems
	}
	blocks := make(map[int]bool, len(fn.Blocks))
	for _, block := range fn.Blocks {
		if block == nil {
			problems = append(problems, fmt.Sprintf("function %s holds a nil block", fn.Name))
			continue
		}
		if blocks[block.ID] {
			problems = append(problems, fmt.Sprintf("function %s declares block b%d twice", fn.Name, block.ID))
			continue
		}
		blocks[block.ID] = true
	}
	if len(problems) > 0 {
		return problems
	}
	if len(fn.Blocks) > 0 && !blocks[fn.EntryID] {
		problems = append(problems, fmt.Sprintf("function %s enters at b%d, which it does not contain", fn.Name, fn.EntryID))
	}
	for _, block := range fn.Blocks {
		for index, instr := range block.Instrs {
			if typednil.IsNil(instr) {
				problems = append(problems, fmt.Sprintf("function %s block b%d holds a nil instruction at %d", fn.Name, block.ID, index))
				continue
			}
			problems = append(problems, validateInstruction(types, fn, block, index, instr)...)
		}
		if typednil.IsNil(block.Term) {
			problems = append(problems, fmt.Sprintf("function %s block b%d has no terminator", fn.Name, block.ID))
			continue
		}
		problems = append(problems, validateTerminator(types, fn, block, blocks)...)
	}
	return problems
}

func validateInstruction(types *ir.TypeTable, fn *Function, block *Block, index int, instr Instr) []string {
	where := fmt.Sprintf("function %s block b%d instruction %d", fn.Name, block.ID, index)
	switch node := instr.(type) {
	case *Assign:
		return validateValueExpr(types, node.Value, where+" assignment")
	case *Store:
		problems := validatePlace(types, node.Place, where+" store")
		problems = append(problems, validateValueRef(types, node.Value, where+" store value")...)
		if node.Place != nil && node.Value != nil && node.Place.Type != node.Value.TypeID() {
			problems = append(problems, fmt.Sprintf("%s stores type#%d into place type#%d", where, node.Value.TypeID(), node.Place.Type))
		}
		return problems
	case *Print:
		return validateValueRef(types, node.Value, where+" print value")
	case *Drop:
		return validateValueRef(types, node.Value, where+" drop value")
	case *DynamicArrayOp:
		problems := validateKnownType(types, node.ArrayType, where+" dynamic-array type")
		problems = append(problems, validateValueRef(types, node.Array, where+" dynamic-array value")...)
		if types != nil {
			arrayType, arrayOK := types.Type(node.ArrayType)
			if !arrayOK || arrayType.Kind != ir.TypeArray || arrayType.Length != "" {
				problems = append(problems, fmt.Sprintf("%s operates on non-dynamic-array type#%d", where, node.ArrayType))
			}
			if node.Array != nil {
				carrier, carrierOK := types.Type(node.Array.TypeID())
				if !carrierOK || carrier.Kind != ir.TypeReference || carrier.Elem != node.ArrayType {
					problems = append(problems, fmt.Sprintf("%s array value type#%d does not reference array type#%d", where, node.Array.TypeID(), node.ArrayType))
				}
			}
		}
		if node.Length != nil {
			problems = append(problems, validateValueRef(types, node.Length, where+" dynamic-array length")...)
		}
		if node.Value != nil {
			problems = append(problems, validateValueRef(types, node.Value, where+" dynamic-array element")...)
		}
		return problems
	case *Call:
		return validateCall(types, node, where)
	case *InterfaceCall:
		return validateInterfaceCall(types, node, where)
	default:
		return []string{fmt.Sprintf("%s has unknown instruction %T", where, instr)}
	}
}

func validateTerminator(types *ir.TypeTable, fn *Function, block *Block, blocks map[int]bool) []string {
	problems := validateTransfers(fn, block, blocks)
	where := fmt.Sprintf("function %s block b%d", fn.Name, block.ID)
	switch term := block.Term.(type) {
	case *Branch:
		problems = append(problems, validateValueRef(types, term.Cond, where+" branch condition")...)
		if term.Cond != nil && types != nil {
			if typ, ok := types.Type(term.Cond.TypeID()); !ok || typ.Kind != ir.TypeBool {
				problems = append(problems, fmt.Sprintf("%s branches on non-bool type#%d", where, term.Cond.TypeID()))
			}
		}
	case *SwitchVariant:
		problems = append(problems, validateValueRef(types, term.Value, where+" variant switch value")...)
		if term.Value != nil && types != nil {
			if variant, ok := types.Type(term.Value.TypeID()); !ok || variant.Kind != ir.TypeVariant {
				problems = append(problems, fmt.Sprintf("%s switches on non-variant type#%d", where, term.Value.TypeID()))
			} else {
				if len(term.Targets) != len(variant.Cases) {
					problems = append(problems, fmt.Sprintf("%s variant switch has %d targets, want %d", where, len(term.Targets), len(variant.Cases)))
				}
				for _, target := range term.Targets {
					if _, ok := variant.VariantCase(target.Case); !ok {
						problems = append(problems, fmt.Sprintf("%s variant switch has invalid case %d", where, target.Case))
					}
				}
			}
		}
	case *Ret:
		if types == nil {
			break
		}
		returnType, returnOK := types.Type(fn.ReturnType)
		if !returnOK {
			break
		}
		if term.Value == nil {
			if returnType.Kind != ir.TypeVoid {
				problems = append(problems, fmt.Sprintf("%s returns no value from type#%d function", where, fn.ReturnType))
			}
			break
		}
		problems = append(problems, validateValueRef(types, term.Value, where+" return value")...)
		if returnType.Kind == ir.TypeVoid {
			problems = append(problems, fmt.Sprintf("%s returns a value from void function", where))
		} else if term.Value.TypeID() != fn.ReturnType {
			problems = append(problems, fmt.Sprintf("%s returns type#%d, want type#%d", where, term.Value.TypeID(), fn.ReturnType))
		}
	}
	return problems
}

func validateTransfers(fn *Function, block *Block, blocks map[int]bool) []string {
	problems := make([]string, 0)
	report := func(target int, role string) {
		if !blocks[target] {
			problems = append(problems, fmt.Sprintf("function %s block b%d transfers to b%d as its %s, which it does not contain",
				fn.Name, block.ID, target, role))
		}
	}
	switch term := block.Term.(type) {
	case *Jump:
		report(term.TargetID, "jump target")
	case *Branch:
		report(term.ThenID, "true target")
		report(term.ElseID, "false target")
	case *SwitchVariant:
		seen := make(map[int]bool, len(term.Targets))
		for _, target := range term.Targets {
			report(target.TargetID, fmt.Sprintf("case %d target", target.Case))
			if seen[target.Case] {
				problems = append(problems, fmt.Sprintf("function %s block b%d selects case %d twice", fn.Name, block.ID, target.Case))
			}
			seen[target.Case] = true
		}
	case *Ret:
	default:
		problems = append(problems, fmt.Sprintf("function %s block b%d ends with unknown terminator %T", fn.Name, block.ID, block.Term))
	}
	return problems
}

func validateKnownType(types *ir.TypeTable, id ir.TypeID, where string) []string {
	if types == nil {
		return nil
	}
	if _, ok := types.Type(id); !ok {
		return []string{fmt.Sprintf("%s references invalid type#%d", where, id)}
	}
	return nil
}

func validateValueRef(types *ir.TypeTable, ref ValueRef, where string) []string {
	if typednil.IsNil(ref) {
		return []string{where + " is nil"}
	}
	return validateKnownType(types, ref.TypeID(), where)
}

func validateValueExpr(types *ir.TypeTable, expr ValueExpr, where string) []string {
	if typednil.IsNil(expr) {
		return []string{where + " is nil"}
	}
	problems := validateKnownType(types, expr.TypeID(), where)
	switch node := expr.(type) {
	case *Move:
		problems = append(problems, validateValueRef(types, node.Src, where+" source")...)
	case *Unary:
		problems = append(problems, validateValueRef(types, node.Arg, where+" operand")...)
	case *Binary:
		problems = append(problems, validateValueRef(types, node.Left, where+" left operand")...)
		problems = append(problems, validateValueRef(types, node.Right, where+" right operand")...)
	case *StringConcat:
		problems = append(problems, validateValueRef(types, node.Left, where+" left string")...)
		problems = append(problems, validateValueRef(types, node.Right, where+" right string")...)
	case *Cast:
		problems = append(problems, validateValueRef(types, node.Arg, where+" cast operand")...)
	case *AddrOf:
		problems = append(problems, validatePlace(types, node.Place, where+" address place")...)
	case *SliceView:
		problems = append(problems, validatePlace(types, node.Source, where+" slice source")...)
		if node.Start != nil {
			problems = append(problems, validateValueRef(types, node.Start, where+" slice start")...)
		}
		if node.End != nil {
			problems = append(problems, validateValueRef(types, node.End, where+" slice end")...)
		}
	case *Load:
		problems = append(problems, validatePlace(types, node.Place, where+" load place")...)
		if node.Place != nil && node.Place.Type != node.Type {
			problems = append(problems, fmt.Sprintf("%s loads place type#%d as type#%d", where, node.Place.Type, node.Type))
		}
	case *Len:
		problems = append(problems, validateValueRef(types, node.Value, where+" length value")...)
	case *StringChars:
		problems = append(problems, validateValueRef(types, node.Value, where+" string value")...)
	case *StringFromBytes:
		problems = append(problems, validateValueRef(types, node.Bytes, where+" byte source")...)
		if node.Allocator != nil {
			problems = append(problems, validateValueRef(types, node.Allocator, where+" allocator")...)
		}
	case *Field:
		problems = append(problems, validateValueRef(types, node.Base, where+" field base")...)
		if node.Base != nil && types != nil {
			if base, ok := types.Type(node.Base.TypeID()); !ok || base.Kind != ir.TypeStruct || node.Index < 0 || node.Index >= len(base.Fields) {
				problems = append(problems, fmt.Sprintf("%s selects invalid field %d from type#%d", where, node.Index, node.Base.TypeID()))
			} else if base.Fields[node.Index].Type != node.Type {
				problems = append(problems, fmt.Sprintf("%s field %d has type#%d, not type#%d", where, node.Index, base.Fields[node.Index].Type, node.Type))
			}
		}
	case *StructLit:
		problems = append(problems, validateStructLiteral(types, node, where)...)
	case *ArrayLit:
		problems = append(problems, validateArrayLiteral(types, node, where)...)
	case *DynamicArrayAlloc:
		if node.Allocator != nil {
			problems = append(problems, validateValueRef(types, node.Allocator, where+" allocator")...)
		}
	case *Alloc:
		problems = append(problems, validateValueRef(types, node.Value, where+" allocated value")...)
		if node.Allocator != nil {
			problems = append(problems, validateValueRef(types, node.Allocator, where+" allocator")...)
		}
		if types != nil {
			if allocated, ok := types.Type(node.Type); ok && allocated.Kind == ir.TypeOwnedPtr && node.Value != nil && allocated.Elem != node.Value.TypeID() {
				problems = append(problems, fmt.Sprintf("%s owns type#%d but allocates type#%d", where, allocated.Elem, node.Value.TypeID()))
			}
		}
	case *ZeroValue, *StringLiteral:
	case *VariantMake:
		problems = append(problems, validateVariantMake(types, node, where)...)
	case *VariantIs:
		problems = append(problems, validateVariantIs(types, node, where)...)
	case *InterfaceMake:
		problems = append(problems, validateValueRef(types, node.Value, where+" interface value")...)
		problems = append(problems, validateKnownType(types, node.DataType, where+" interface data type")...)
		methodCount, interfaceOK := types.InterfaceMethodCount(node.Type)
		if !interfaceOK {
			problems = append(problems, fmt.Sprintf("%s constructs non-interface carrier type#%d", where, node.Type))
		} else if len(node.Slots) != methodCount {
			problems = append(problems, fmt.Sprintf("%s has %d interface slots, want %d", where, len(node.Slots), methodCount))
		}
		for index, slot := range node.Slots {
			problems = append(problems, validateValueRef(types, slot, fmt.Sprintf("%s interface slot %d", where, index))...)
			method, ok := types.InterfaceMethod(node.Type, index)
			if ok && slot != nil && slot.TypeID() != method.SlotType {
				problems = append(problems, fmt.Sprintf("%s interface slot %d has type#%d, want published type#%d", where, index, slot.TypeID(), method.SlotType))
			}
		}
	case *InterfaceCall:
		problems = append(problems, validateInterfaceCall(types, node, where)...)
	case *Call:
		problems = append(problems, validateCall(types, node, where)...)
	default:
		problems = append(problems, fmt.Sprintf("%s has unknown value expression %T", where, expr))
	}
	return problems
}

func validateCall(types *ir.TypeTable, call *Call, where string) []string {
	if call == nil {
		return []string{where + " call is nil"}
	}
	problems := validateKnownType(types, call.Type, where+" call result")
	problems = append(problems, validateValueRef(types, call.Callee, where+" callee")...)
	for index, arg := range call.Args {
		problems = append(problems, validateValueRef(types, arg, fmt.Sprintf("%s argument %d", where, index))...)
	}
	if call.Callee == nil || types == nil {
		return problems
	}
	function, ok := types.Type(call.Callee.TypeID())
	if !ok || function.Kind != ir.TypeFunction {
		return append(problems, fmt.Sprintf("%s calls non-function type#%d", where, call.Callee.TypeID()))
	}
	if function.Return != call.Type {
		problems = append(problems, fmt.Sprintf("%s call result type#%d, want type#%d", where, call.Type, function.Return))
	}
	if len(call.Args) != len(function.Params) {
		problems = append(problems, fmt.Sprintf("%s call has %d arguments, want %d", where, len(call.Args), len(function.Params)))
		return problems
	}
	for index, arg := range call.Args {
		if arg != nil && arg.TypeID() != function.Params[index] {
			problems = append(problems, fmt.Sprintf("%s argument %d has type#%d, want type#%d", where, index, arg.TypeID(), function.Params[index]))
		}
	}
	return problems
}

func validateInterfaceCall(types *ir.TypeTable, call *InterfaceCall, where string) []string {
	if call == nil {
		return []string{where + " interface call is nil"}
	}
	problems := validateKnownType(types, call.Type, where+" interface call result")
	problems = append(problems, validateValueRef(types, call.Base, where+" interface base")...)
	for index, arg := range call.Args {
		problems = append(problems, validateValueRef(types, arg, fmt.Sprintf("%s interface argument %d", where, index))...)
	}
	if call.Base == nil || types == nil {
		return problems
	}
	method, ok := types.InterfaceMethod(call.Base.TypeID(), call.Slot)
	if !ok {
		return append(problems, fmt.Sprintf("%s targets invalid interface slot %d on type#%d", where, call.Slot, call.Base.TypeID()))
	}
	if call.SlotType != method.SlotType {
		problems = append(problems, fmt.Sprintf("%s has slot type#%d, want published type#%d", where, call.SlotType, method.SlotType))
	}
	problems = append(problems, validateInterfaceSlotType(types, call.SlotType, where)...)
	slotType, slotOK := types.Type(call.SlotType)
	if !slotOK || slotType.Kind != ir.TypeFunction {
		return problems
	}
	wantArgs := len(slotType.Params) - 1
	if wantArgs < 0 {
		wantArgs = 0
	}
	if len(call.Args) != wantArgs {
		problems = append(problems, fmt.Sprintf("%s has %d interface arguments, want %d", where, len(call.Args), wantArgs))
	}
	for index, arg := range call.Args {
		param := index + 1
		if param < len(slotType.Params) && arg != nil && arg.TypeID() != slotType.Params[param] {
			problems = append(problems, fmt.Sprintf("%s interface argument %d has type#%d, want type#%d", where, index, arg.TypeID(), slotType.Params[param]))
		}
	}
	if call.Type != slotType.Return {
		problems = append(problems, fmt.Sprintf("%s returns type#%d, want slot result type#%d", where, call.Type, slotType.Return))
	}
	return problems
}

func validateInterfaceSlotType(types *ir.TypeTable, slotType ir.TypeID, where string) []string {
	if types == nil {
		return nil
	}
	fn, ok := types.Type(slotType)
	if !ok {
		return []string{fmt.Sprintf("%s has invalid interface slot type#%d", where, slotType)}
	}
	if fn.Kind != ir.TypeFunction {
		return []string{fmt.Sprintf("%s interface slot type#%d is not a function", where, slotType)}
	}
	if len(fn.Params) == 0 {
		return []string{fmt.Sprintf("%s interface slot type#%d has no erased receiver", where, slotType)}
	}
	receiver, ok := types.Type(fn.Params[0])
	if !ok || receiver.Kind != ir.TypeRawPtr {
		return []string{fmt.Sprintf("%s interface slot type#%d receiver is not rawptr", where, slotType)}
	}
	return nil
}

func validateStructLiteral(types *ir.TypeTable, value *StructLit, where string) []string {
	problems := make([]string, 0)
	if types == nil {
		return problems
	}
	structure, ok := types.Type(value.Type)
	if !ok || structure.Kind != ir.TypeStruct {
		return append(problems, fmt.Sprintf("%s constructs non-struct type#%d", where, value.Type))
	}
	if len(value.Fields) != len(structure.Fields) {
		problems = append(problems, fmt.Sprintf("%s has %d struct fields, want %d", where, len(value.Fields), len(structure.Fields)))
	}
	for index, field := range value.Fields {
		problems = append(problems, validateValueRef(types, field, fmt.Sprintf("%s field %d", where, index))...)
		if index < len(structure.Fields) && field != nil && field.TypeID() != structure.Fields[index].Type {
			problems = append(problems, fmt.Sprintf("%s field %d has type#%d, want type#%d", where, index, field.TypeID(), structure.Fields[index].Type))
		}
	}
	return problems
}

func validateArrayLiteral(types *ir.TypeTable, value *ArrayLit, where string) []string {
	problems := make([]string, 0)
	if types == nil {
		return problems
	}
	array, ok := types.Type(value.Type)
	if !ok || array.Kind != ir.TypeArray {
		return append(problems, fmt.Sprintf("%s constructs non-array type#%d", where, value.Type))
	}
	for index, element := range value.Values {
		problems = append(problems, validateValueRef(types, element, fmt.Sprintf("%s element %d", where, index))...)
		if element != nil && element.TypeID() != array.Elem {
			problems = append(problems, fmt.Sprintf("%s element %d has type#%d, want type#%d", where, index, element.TypeID(), array.Elem))
		}
	}
	return problems
}

func validateVariantMake(types *ir.TypeTable, value *VariantMake, where string) []string {
	if types == nil {
		return nil
	}
	variant, ok := types.Type(value.Type)
	if !ok || variant.Kind != ir.TypeVariant {
		return []string{fmt.Sprintf("%s constructs non-variant type#%d", where, value.Type)}
	}
	variantCase, ok := variant.VariantCase(value.Case)
	if !ok {
		return []string{fmt.Sprintf("%s constructs invalid case %d of type#%d", where, value.Case, value.Type)}
	}
	if variantCase.Payload == ir.InvalidType {
		if value.Payload != nil {
			return []string{fmt.Sprintf("%s gives payload to payloadless case %d", where, value.Case)}
		}
		return nil
	}
	problems := validateValueRef(types, value.Payload, where+" variant payload")
	if value.Payload != nil && value.Payload.TypeID() != variantCase.Payload {
		problems = append(problems, fmt.Sprintf("%s payload has type#%d, want type#%d", where, value.Payload.TypeID(), variantCase.Payload))
	}
	return problems
}

func validateVariantIs(types *ir.TypeTable, value *VariantIs, where string) []string {
	problems := validateValueRef(types, value.Value, where+" variant value")
	if types == nil || value.Value == nil {
		return problems
	}
	variant, ok := types.Type(value.Value.TypeID())
	if !ok || variant.Kind != ir.TypeVariant {
		return append(problems, fmt.Sprintf("%s tests non-variant type#%d", where, value.Value.TypeID()))
	}
	if _, ok := variant.VariantCase(value.Case); !ok {
		problems = append(problems, fmt.Sprintf("%s tests invalid case %d", where, value.Case))
	}
	if result, ok := types.Type(value.Type); !ok || result.Kind != ir.TypeBool {
		problems = append(problems, fmt.Sprintf("%s variant test result is not bool type#%d", where, value.Type))
	}
	return problems
}

func validatePlace(types *ir.TypeTable, place *Place, where string) []string {
	if place == nil {
		return []string{where + " place is nil"}
	}
	problems := validateValueRef(types, place.Root, where+" root")
	problems = append(problems, validateKnownType(types, place.Type, where+" result type")...)
	if types == nil || place.Root == nil {
		return problems
	}
	current := place.Root.TypeID()
	for index, projection := range place.Projections {
		projectionWhere := fmt.Sprintf("%s projection %d", where, index)
		problems = append(problems, validateKnownType(types, projection.Type, projectionWhere)...)
		currentType, ok := types.Type(current)
		if !ok {
			current = projection.Type
			continue
		}
		switch projection.Kind {
		case PlaceProjectionDeref:
			switch currentType.Kind {
			case ir.TypeOwnedPtr, ir.TypeReference:
				if currentType.Elem != projection.Type {
					problems = append(problems, fmt.Sprintf("%s dereferences to type#%d, want type#%d", projectionWhere, projection.Type, currentType.Elem))
				}
			case ir.TypeRawPtr:
			default:
				problems = append(problems, fmt.Sprintf("%s dereferences non-pointer type#%d", projectionWhere, current))
			}
		case PlaceProjectionField:
			if currentType.Kind != ir.TypeStruct || projection.FieldIndex < 0 || projection.FieldIndex >= len(currentType.Fields) {
				problems = append(problems, fmt.Sprintf("%s selects invalid field %d from type#%d", projectionWhere, projection.FieldIndex, current))
			} else if currentType.Fields[projection.FieldIndex].Type != projection.Type {
				problems = append(problems, fmt.Sprintf("%s field %d has type#%d, not type#%d", projectionWhere, projection.FieldIndex, currentType.Fields[projection.FieldIndex].Type, projection.Type))
			}
		case PlaceProjectionIndex:
			problems = append(problems, validateValueRef(types, projection.Index, projectionWhere+" index")...)
			container := currentType
			if container.Kind == ir.TypeOwnedPtr || container.Kind == ir.TypeReference {
				if nested, found := types.Type(container.Elem); found {
					container = nested
				}
			}
			if (container.Kind != ir.TypeArray && container.Kind != ir.TypeSlice) || container.Elem != projection.Type {
				problems = append(problems, fmt.Sprintf("%s indexes incompatible type#%d", projectionWhere, current))
			}
		case PlaceProjectionVariantPayload:
			variantCase, found := currentType.VariantCase(projection.Case)
			if !found || variantCase.Payload == ir.InvalidType {
				problems = append(problems, fmt.Sprintf("%s selects missing payload case %d from type#%d", projectionWhere, projection.Case, current))
			} else if variantCase.Payload != projection.Type {
				problems = append(problems, fmt.Sprintf("%s payload case %d has type#%d, not type#%d", projectionWhere, projection.Case, variantCase.Payload, projection.Type))
			}
		default:
			problems = append(problems, fmt.Sprintf("%s has unknown projection kind %d", projectionWhere, projection.Kind))
		}
		current = projection.Type
	}
	if current != place.Type {
		problems = append(problems, fmt.Sprintf("%s ends at type#%d, not declared type#%d", where, current, place.Type))
	}
	return problems
}
