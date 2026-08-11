package llvm

import (
	"fmt"
	"strings"
)

type llvmValue struct {
	Text   string
	Layout *llvmLayout
}

type llvmPlace struct {
	Text    string
	Pointee *llvmLayout
}

type llvmIncoming struct {
	Value llvmValue
	Label string
}

func llvmLayoutsMatch(left, right *llvmLayout) bool {
	return left != nil && right != nil && left.Kind == right.Kind && left.Text == right.Text
}

func llvmPointerLike(layout *llvmLayout) bool {
	return layout != nil && (layout.Kind == llvmLayoutPointer || layout.Kind == llvmLayoutFunction)
}

func (b *llvmBuilder) invariant(format string, args ...any) {
	panic(fmt.Sprintf("llvm invariant: "+format, args...))
}

func (b *llvmBuilder) value(text string, layout *llvmLayout) llvmValue {
	if b == nil || text == "" || layout == nil {
		b.invariant("value requires text and layout")
	}
	return llvmValue{Text: text, Layout: layout}
}

func (b *llvmBuilder) place(text string, pointee *llvmLayout) llvmPlace {
	if b == nil || text == "" || pointee == nil {
		b.invariant("place requires pointer text and pointee layout")
	}
	return llvmPlace{Text: text, Pointee: pointee}
}

func (b *llvmBuilder) nextValue(layout *llvmLayout) llvmValue {
	return b.value(b.nextReg(), layout)
}

func (b *llvmBuilder) nextPlace(pointee *llvmLayout) llvmPlace {
	return b.place(b.nextReg(), pointee)
}

func (b *llvmBuilder) zero(layout *llvmLayout) llvmValue {
	return b.value("zeroinitializer", layout)
}

func (b *llvmBuilder) load(source llvmPlace) llvmValue {
	if source.Pointee == nil {
		b.invariant("load requires typed pointee")
	}
	result := b.nextValue(source.Pointee)
	b.line(fmt.Sprintf("%s = load %s, %s* %s", result.Text, source.Pointee.Text, source.Pointee.Text, source.Text))
	return result
}

func (b *llvmBuilder) alignedLoad(source llvmPlace, alignment int) llvmValue {
	if source.Pointee == nil || alignment <= 0 {
		b.invariant("aligned load requires typed pointee and positive alignment")
	}
	result := b.nextValue(source.Pointee)
	b.line(fmt.Sprintf("%s = load %s, %s* %s, align %d", result.Text, source.Pointee.Text, source.Pointee.Text, source.Text, alignment))
	return result
}

func (b *llvmBuilder) store(target llvmPlace, value llvmValue) {
	if !llvmLayoutsMatch(target.Pointee, value.Layout) {
		b.invariant("store %s into %s pointee", value.Layout.Text, target.Pointee.Text)
	}
	b.line(fmt.Sprintf("store %s %s, %s* %s", value.Layout.Text, value.Text, target.Pointee.Text, target.Text))
}

func (b *llvmBuilder) alloca(layout *llvmLayout) llvmPlace {
	result := b.nextPlace(layout)
	b.line(fmt.Sprintf("%s = alloca %s", result.Text, layout.Text))
	return result
}

func (b *llvmBuilder) extractIndex(aggregate llvmValue, index int) llvmValue {
	if aggregate.Layout == nil || index < 0 {
		b.invariant("extract index %d requires aggregate layout", index)
	}
	var element *llvmLayout
	switch aggregate.Layout.Kind {
	case llvmLayoutAggregate:
		if index < len(aggregate.Layout.Elements) {
			element = aggregate.Layout.Elements[index]
		}
	case llvmLayoutArray:
		element = aggregate.Layout.Element
	}
	if element == nil {
		b.invariant("extract index %d from %s", index, aggregate.Layout.Text)
	}
	result := b.nextValue(element)
	b.line(fmt.Sprintf("%s = extractvalue %s %s, %d", result.Text, aggregate.Layout.Text, aggregate.Text, index))
	return result
}

func (b *llvmBuilder) extractField(aggregate llvmValue, field llvmFieldName) llvmValue {
	index, ok := aggregate.Layout.Fields[field]
	if !ok {
		b.invariant("layout %s has no field %q", aggregate.Layout.Text, field)
	}
	return b.extractIndex(aggregate, index)
}

func (b *llvmBuilder) insertIndex(aggregate, value llvmValue, index int) llvmValue {
	if aggregate.Layout == nil || index < 0 {
		b.invariant("insert index %d requires aggregate layout", index)
	}
	var element *llvmLayout
	switch aggregate.Layout.Kind {
	case llvmLayoutAggregate:
		if index < len(aggregate.Layout.Elements) {
			element = aggregate.Layout.Elements[index]
		}
	case llvmLayoutArray:
		element = aggregate.Layout.Element
	}
	if element == nil || !llvmLayoutsMatch(element, value.Layout) {
		b.invariant("insert %s into %s index %d", value.Layout.Text, aggregate.Layout.Text, index)
	}
	result := b.nextValue(aggregate.Layout)
	b.line(fmt.Sprintf("%s = insertvalue %s %s, %s %s, %d", result.Text, aggregate.Layout.Text, aggregate.Text, value.Layout.Text, value.Text, index))
	return result
}

func (b *llvmBuilder) insertField(aggregate, value llvmValue, field llvmFieldName) llvmValue {
	index, ok := aggregate.Layout.Fields[field]
	if !ok {
		b.invariant("layout %s has no field %q", aggregate.Layout.Text, field)
	}
	return b.insertIndex(aggregate, value, index)
}

func (b *llvmBuilder) compare(opcode, predicate string, left, right llvmValue) llvmValue {
	if !llvmLayoutsMatch(left.Layout, right.Layout) {
		b.invariant("compare %s with %s", left.Layout.Text, right.Layout.Text)
	}
	if left.Layout.Kind != llvmLayoutScalar && left.Layout.Kind != llvmLayoutPointer {
		b.invariant("compare unsupported layout %s", left.Layout.Text)
	}
	result := b.nextValue(llvmScalarLayout("i1"))
	b.line(fmt.Sprintf("%s = %s %s %s %s, %s", result.Text, opcode, predicate, left.Layout.Text, left.Text, right.Text))
	return result
}

func (b *llvmBuilder) arithmetic(opcode string, left, right llvmValue) llvmValue {
	result := b.nextValue(left.Layout)
	b.defineArithmetic(result, opcode, left, right)
	return result
}

// defineArithmetic emits a value reserved before its defining block, as
// required by loop-carried phi inputs.
func (b *llvmBuilder) defineArithmetic(result llvmValue, opcode string, left, right llvmValue) {
	if !llvmLayoutsMatch(left.Layout, right.Layout) {
		b.invariant("%s operands %s and %s", opcode, left.Layout.Text, right.Layout.Text)
	}
	if left.Layout.Kind != llvmLayoutScalar || !llvmLayoutsMatch(result.Layout, left.Layout) {
		b.invariant("%s requires scalar operands, got %s", opcode, left.Layout.Text)
	}
	b.line(fmt.Sprintf("%s = %s %s %s, %s", result.Text, opcode, left.Layout.Text, left.Text, right.Text))
}

func (b *llvmBuilder) cast(opcode string, value llvmValue, target *llvmLayout) llvmValue {
	if value.Layout == nil || target == nil || value.Layout.Kind == llvmLayoutAggregate || target.Kind == llvmLayoutAggregate ||
		value.Layout.Kind == llvmLayoutArray || target.Kind == llvmLayoutArray {
		b.invariant("%s cast from %s to %s", opcode, value.Layout.Text, target.Text)
	}
	result := b.nextValue(target)
	b.line(fmt.Sprintf("%s = %s %s %s to %s", result.Text, opcode, value.Layout.Text, value.Text, target.Text))
	return result
}

func (b *llvmBuilder) bitcast(value llvmValue, target *llvmLayout) llvmValue {
	if !llvmPointerLike(value.Layout) || !llvmPointerLike(target) {
		b.invariant("bitcast requires pointers, got %s to %s", value.Layout.Text, target.Text)
	}
	result := b.nextValue(target)
	b.line(fmt.Sprintf("%s = bitcast %s %s to %s", result.Text, value.Layout.Text, value.Text, target.Text))
	return result
}

func (b *llvmBuilder) pointerValue(place llvmPlace) llvmValue {
	return b.value(place.Text, llvmPointerLayout(place.Pointee))
}

func (b *llvmBuilder) pointerPlace(value llvmValue) llvmPlace {
	if value.Layout == nil || value.Layout.Kind != llvmLayoutPointer || value.Layout.Pointee == nil {
		b.invariant("value %s is not data pointer", value.Layout.Text)
	}
	return b.place(value.Text, value.Layout.Pointee)
}

func (b *llvmBuilder) gep(base llvmPlace, index llvmValue, inbounds bool) llvmPlace {
	if index.Layout == nil || index.Layout.Kind != llvmLayoutScalar {
		b.invariant("GEP index requires scalar, got %s", index.Layout.Text)
	}
	result := b.nextPlace(base.Pointee)
	opcode := "getelementptr"
	if inbounds {
		opcode += " inbounds"
	}
	b.line(fmt.Sprintf("%s = %s %s, %s* %s, %s %s", result.Text, opcode, base.Pointee.Text, base.Pointee.Text, base.Text, index.Layout.Text, index.Text))
	return result
}

func (b *llvmBuilder) arrayElement(base llvmPlace, index llvmValue, inbounds bool) llvmPlace {
	if base.Pointee == nil || base.Pointee.Kind != llvmLayoutArray || base.Pointee.Element == nil {
		b.invariant("array GEP requires array place, got %s", base.Pointee.Text)
	}
	if index.Layout == nil || index.Layout.Kind != llvmLayoutScalar {
		b.invariant("array GEP index requires scalar, got %s", index.Layout.Text)
	}
	result := b.nextPlace(base.Pointee.Element)
	opcode := "getelementptr"
	if inbounds {
		opcode += " inbounds"
	}
	b.line(fmt.Sprintf("%s = %s %s, %s* %s, i32 0, %s %s", result.Text, opcode, base.Pointee.Text, base.Pointee.Text, base.Text, index.Layout.Text, index.Text))
	return result
}

func (b *llvmBuilder) fieldPlace(base llvmPlace, index int) llvmPlace {
	if base.Pointee == nil || base.Pointee.Kind != llvmLayoutAggregate || index < 0 || index >= len(base.Pointee.Elements) {
		b.invariant("field GEP index %d from %s", index, base.Pointee.Text)
	}
	result := b.nextPlace(base.Pointee.Elements[index])
	b.line(fmt.Sprintf("%s = getelementptr inbounds %s, %s* %s, i32 0, i32 %d", result.Text, base.Pointee.Text, base.Pointee.Text, base.Text, index))
	return result
}

func (b *llvmBuilder) namedFieldPlace(base llvmPlace, field llvmFieldName) llvmPlace {
	index, ok := base.Pointee.Fields[field]
	if !ok {
		b.invariant("layout %s has no field %q", base.Pointee.Text, field)
	}
	return b.fieldPlace(base, index)
}

func (b *llvmBuilder) phi(layout *llvmLayout, incoming ...llvmIncoming) llvmValue {
	result := b.nextValue(layout)
	b.definePhi(result, incoming...)
	return result
}

// definePhi emits a phi into a result reserved before loop back-edge values.
func (b *llvmBuilder) definePhi(result llvmValue, incoming ...llvmIncoming) {
	if result.Layout == nil || len(incoming) == 0 {
		b.invariant("phi requires layout and incoming values")
	}
	parts := make([]string, len(incoming))
	for i, item := range incoming {
		if !llvmLayoutsMatch(result.Layout, item.Value.Layout) || item.Label == "" {
			b.invariant("phi incoming %d does not match %s", i, result.Layout.Text)
		}
		parts[i] = fmt.Sprintf("[ %s, %%%s ]", item.Value.Text, item.Label)
	}
	b.line(fmt.Sprintf("%s = phi %s %s", result.Text, result.Layout.Text, strings.Join(parts, ", ")))
}

func (b *llvmBuilder) call(callee llvmValue, args []llvmValue) llvmValue {
	if callee.Layout == nil || callee.Layout.Kind != llvmLayoutFunction || callee.Layout.Return == nil {
		b.invariant("call requires function value, got %s", callee.Layout.Text)
	}
	if len(args) != len(callee.Layout.Parameters) {
		b.invariant("call %s argument count %d, want %d", callee.Text, len(args), len(callee.Layout.Parameters))
	}
	parts := make([]string, len(args))
	for i, arg := range args {
		if !llvmLayoutsMatch(arg.Layout, callee.Layout.Parameters[i]) {
			b.invariant("call %s argument %d is %s, want %s", callee.Text, i, arg.Layout.Text, callee.Layout.Parameters[i].Text)
		}
		parts[i] = arg.Layout.Text + " " + arg.Text
	}
	callText := fmt.Sprintf("call %s %s(%s)", callee.Layout.Return.Text, callee.Text, strings.Join(parts, ", "))
	if callee.Layout.Return.Kind == llvmLayoutVoid {
		b.line(callText)
		return b.value("void", callee.Layout.Return)
	}
	result := b.nextValue(callee.Layout.Return)
	b.line(result.Text + " = " + callText)
	return result
}

func (b *llvmBuilder) variadicCall(callee llvmValue, fixed, variadic []llvmValue) llvmValue {
	if callee.Layout == nil || callee.Layout.Kind != llvmLayoutFunction || callee.Layout.Return == nil || len(fixed) != len(callee.Layout.Parameters) {
		b.invariant("variadic call requires function and exact fixed arguments")
	}
	args := make([]string, 0, len(fixed)+len(variadic))
	fixedTypes := make([]string, len(fixed))
	for i, arg := range fixed {
		if !llvmLayoutsMatch(arg.Layout, callee.Layout.Parameters[i]) {
			b.invariant("variadic call %s fixed argument %d is %s, want %s", callee.Text, i, arg.Layout.Text, callee.Layout.Parameters[i].Text)
		}
		fixedTypes[i] = arg.Layout.Text
		args = append(args, arg.Layout.Text+" "+arg.Text)
	}
	for _, arg := range variadic {
		if arg.Layout == nil || arg.Layout.Kind == llvmLayoutVoid || arg.Layout.Kind == llvmLayoutFunction {
			b.invariant("variadic call %s has invalid variadic argument", callee.Text)
		}
		args = append(args, arg.Layout.Text+" "+arg.Text)
	}
	signature := "..."
	if len(fixedTypes) > 0 {
		signature = strings.Join(fixedTypes, ", ") + ", ..."
	}
	callText := fmt.Sprintf("call %s (%s) %s(%s)", callee.Layout.Return.Text, signature, callee.Text, strings.Join(args, ", "))
	if callee.Layout.Return.Kind == llvmLayoutVoid {
		b.line(callText)
		return b.value("void", callee.Layout.Return)
	}
	result := b.nextValue(callee.Layout.Return)
	b.line(result.Text + " = " + callText)
	return result
}

func (b *llvmBuilder) trap() {
	void := &llvmLayout{Text: "void", Kind: llvmLayoutVoid}
	b.call(b.value("@llvm.trap", llvmFunctionLayout(void, nil)), nil)
	b.line("unreachable")
}

func (b *llvmBuilder) branch(label string) {
	if label == "" {
		b.invariant("branch requires target label")
	}
	b.line("br label %" + label)
}

func (b *llvmBuilder) condBranch(condition llvmValue, yes, no string) {
	if condition.Layout == nil || condition.Layout.Text != "i1" || yes == "" || no == "" {
		b.invariant("conditional branch requires i1 and target labels")
	}
	b.line(fmt.Sprintf("br i1 %s, label %%%s, label %%%s", condition.Text, yes, no))
}

func (b *llvmBuilder) ret(value llvmValue, expected *llvmLayout) {
	if value.Layout == nil || expected == nil || expected.Kind == llvmLayoutVoid || expected.Kind == llvmLayoutFunction ||
		!llvmLayoutsMatch(value.Layout, expected) {
		b.invariant("return requires concrete value")
	}
	b.line("ret " + value.Layout.Text + " " + value.Text)
}

func (b *llvmBuilder) retVoid(expected *llvmLayout) {
	if expected == nil || expected.Kind != llvmLayoutVoid {
		b.invariant("void return requires void function result")
	}
	b.line("ret void")
}

func (b *llvmBuilder) selectValue(condition, yes, no llvmValue) llvmValue {
	if condition.Layout == nil || condition.Layout.Text != "i1" || !llvmLayoutsMatch(yes.Layout, no.Layout) {
		b.invariant("select requires i1 and matching values")
	}
	result := b.nextValue(yes.Layout)
	b.line(fmt.Sprintf("%s = select i1 %s, %s %s, %s %s", result.Text, condition.Text, yes.Layout.Text, yes.Text, no.Layout.Text, no.Text))
	return result
}
