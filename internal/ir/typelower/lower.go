package typelower

import (
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/semantics/typeinfo"
)

type context struct {
	types       *ir.TypeTable
	diagnostics *diagnostics.DiagnosticBag
}

func Type(types *ir.TypeTable, diagnostics *diagnostics.DiagnosticBag, t typeinfo.Type) ir.TypeID {
	if types == nil || t == nil {
		return ir.InvalidType
	}
	interner := runtimeTypeInterner{ctx: &context{types: types, diagnostics: diagnostics}, active: make(map[string]ir.TypeID)}
	return interner.intern(t)
}

func ReturnType(types *ir.TypeTable, diagnostics *diagnostics.DiagnosticBag, t typeinfo.Type) ir.TypeID {
	if types == nil {
		return ir.InvalidType
	}
	if t == nil {
		return types.Intern(ir.Type{Kind: ir.TypeVoid})
	}
	return Type(types, diagnostics, t)
}

// runtimeTypeInterner is semantic-to-IR type construction state. Named
// composites reserve identity before child descent so legal pointer/reference
// recursion closes on one canonical TypeID.
type runtimeTypeInterner struct {
	ctx    *context
	active map[string]ir.TypeID
}

func (l *runtimeTypeInterner) intern(t typeinfo.Type) ir.TypeID {
	if l == nil || l.ctx == nil || l.ctx.types == nil || t == nil {
		return ir.InvalidType
	}
	visitor := runtimeTypeVisitor{lowerer: l, result: ir.InvalidType}
	t.VisitRuntimeType(&visitor)
	return visitor.result
}

func (l *runtimeTypeInterner) internNamed(shell ir.Type, descriptor func() (ir.Type, bool)) ir.TypeID {
	id, err := l.ctx.types.ReserveNamed(shell)
	if err != nil {
		return l.invalid(err.Error())
	}
	if _, complete := l.ctx.types.Type(id); complete {
		return id
	}
	key := l.ctx.types.ABIKey(id)
	if activeID, active := l.active[key]; active {
		return activeID
	}
	l.active[key] = id
	defer delete(l.active, key)
	typ, valid := descriptor()
	if !valid {
		return l.invalid("named type " + shell.Name + " has invalid runtime descriptor")
	}
	if err := l.ctx.types.CompleteNamed(id, typ); err != nil {
		return l.invalid(err.Error())
	}
	return id
}

func (l *runtimeTypeInterner) invalid(message string) ir.TypeID {
	if l != nil && l.ctx != nil && l.ctx.diagnostics != nil {
		l.ctx.diagnostics.Add(diagnostics.NewError(message).WithCode(diagnostics.ErrInvalidType))
	}
	return ir.InvalidType
}
