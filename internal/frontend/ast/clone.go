package ast

import "compiler/internal/source"

// SubstituteExpr clones an expression for call-site expansion. Parameter
// identifiers are replaced with their already-evaluated argument expressions;
// every cloned node gets a deterministic generated ID owned by the call site
// and omitted parameter slot.
//
// Clone logic lives on each expression type via the Expr.copyExpr interface
// method. Adding a new Expr type that is missing copyExpr produces a compile
// error, so there is no silent default fallthrough.
func SubstituteExpr(owner source.NodeID, parameterSlot uint64, expr Expr, substitutions map[string]Expr) (cloned Expr, defaultClones map[source.NodeID]source.NodeID, argumentClones map[source.NodeID]source.NodeID) {
	if expr == nil {
		return nil, nil, nil
	}
	if owner.Function() == "" {
		panic("default-argument clone requires function-owned call identity")
	}
	defaultClones = make(map[source.NodeID]source.NodeID)
	argumentClones = make(map[source.NodeID]source.NodeID)
	var ordinal uint64
	newID := func(original source.NodeID, fromArgument bool) source.NodeID {
		ordinal++
		id := source.GeneratedNodeID(owner, source.GeneratedDefaultArgument, parameterSlot, ordinal)
		if !id.IsValid() {
			panic("default-argument clone produced invalid generated identity")
		}
		if fromArgument {
			argumentClones[id] = original
		} else {
			defaultClones[id] = original
		}
		return id
	}
	cloned = expr.copyExpr(substitutions, newID, false)
	return cloned, defaultClones, argumentClones
}

func cloneIdent(ident *Ident, newID func(source.NodeID, bool) source.NodeID, fromArgument bool) *Ident {
	if ident == nil {
		return nil
	}
	return &Ident{
		NodeIDHolder: NodeIDHolder{NodeID: newID(ident.ID(), fromArgument)},
		Name:         ident.Name,
		Location:     ident.Location,
	}
}

// cloneTypeExpr keeps expression annotations inside the same unique tree as
// their owning expression. Type-specific cloning is sealed by TypeExpr.
func cloneTypeExpr(typ TypeExpr, newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	if typ == nil {
		return nil
	}
	return typ.copyTypeExpr(newID, fromArgument)
}

func (t *NamedType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	return &NamedType{NodeIDHolder: id, Name: t.Name, Location: t.Location}
}

func (t *AppliedType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	args := make([]TypeExpr, len(t.TypeArgs))
	for index, arg := range t.TypeArgs {
		args[index] = cloneTypeExpr(arg, newID, fromArgument)
	}
	return &AppliedType{NodeIDHolder: id, Name: cloneIdent(t.Name, newID, fromArgument), TypeArgs: args, Location: t.Location}
}

func (t *OwnedPtrType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	return &OwnedPtrType{NodeIDHolder: id, Target: cloneTypeExpr(t.Target, newID, fromArgument), Location: t.Location}
}

func (t *RawPtrType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	return &RawPtrType{NodeIDHolder: id, Location: t.Location}
}

func (t *RefType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	return &RefType{NodeIDHolder: id, IsMutable: t.IsMutable, Target: cloneTypeExpr(t.Target, newID, fromArgument), Location: t.Location}
}

func (t *OptionalType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	return &OptionalType{NodeIDHolder: id, Inner: cloneTypeExpr(t.Inner, newID, fromArgument), Location: t.Location}
}

func (t *ArrayType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	var length *NumberLit
	if t.Len != nil {
		length = t.Len.copyExpr(nil, newID, fromArgument).(*NumberLit)
	}
	return &ArrayType{NodeIDHolder: id, Len: length, Shape: t.Shape, Elem: cloneTypeExpr(t.Elem, newID, fromArgument), Location: t.Location}
}

func (t *FuncType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	params := make([]Param, len(t.Params))
	for index, param := range t.Params {
		params[index] = cloneParam(param, newID, fromArgument)
	}
	return &FuncType{NodeIDHolder: id, Params: params, Return: cloneTypeExpr(t.Return, newID, fromArgument), ReturnOrigins: cloneReturnOrigins(t.ReturnOrigins, newID, fromArgument), Location: t.Location}
}

func (t *StructType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	fields := make([]TypeField, len(t.Fields))
	for index, field := range t.Fields {
		fields[index] = TypeField{Name: cloneIdent(field.Name, newID, fromArgument), Type: cloneTypeExpr(field.Type, newID, fromArgument), Location: field.Location}
	}
	return &StructType{NodeIDHolder: id, Fields: fields, Location: t.Location}
}

func (t *InterfaceType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	methods := make([]TypeMethod, len(t.Methods))
	for index, method := range t.Methods {
		cloned := TypeMethod{Name: cloneIdent(method.Name, newID, fromArgument), ReturnType: cloneTypeExpr(method.ReturnType, newID, fromArgument), ReturnOrigins: cloneReturnOrigins(method.ReturnOrigins, newID, fromArgument), Location: method.Location}
		if method.Receiver != nil {
			receiver := cloneParam(*method.Receiver, newID, fromArgument)
			cloned.Receiver = &receiver
		}
		cloned.TypeParams = make([]TypeParam, len(method.TypeParams))
		for typeIndex, param := range method.TypeParams {
			cloned.TypeParams[typeIndex] = TypeParam{Name: cloneIdent(param.Name, newID, fromArgument), Location: param.Location}
		}
		cloned.Params = make([]Param, len(method.Params))
		for paramIndex, param := range method.Params {
			cloned.Params[paramIndex] = cloneParam(param, newID, fromArgument)
		}
		methods[index] = cloned
	}
	return &InterfaceType{NodeIDHolder: id, Methods: methods, Location: t.Location}
}

func (t *EnumType) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	variants := make([]EnumVariant, len(t.Variants))
	for index, variant := range t.Variants {
		variants[index] = EnumVariant{Name: cloneIdent(variant.Name, newID, fromArgument), Payload: cloneTypeExpr(variant.Payload, newID, fromArgument), Location: variant.Location}
	}
	return &EnumType{NodeIDHolder: id, Variants: variants, Location: t.Location}
}

func (t *ScopeResolution) copyTypeExpr(newID func(source.NodeID, bool) source.NodeID, fromArgument bool) TypeExpr {
	id := NodeIDHolder{NodeID: newID(t.ID(), fromArgument)}
	return &ScopeResolution{NodeIDHolder: id, Segments: clonePathSegments(t.Segments, newID, fromArgument), Location: t.Location}
}

func clonePathSegments(segments []PathSegment, newID func(source.NodeID, bool) source.NodeID, fromArgument bool) []PathSegment {
	cloned := make([]PathSegment, len(segments))
	for index, segment := range segments {
		args := make([]TypeExpr, len(segment.TypeArgs))
		for argIndex, arg := range segment.TypeArgs {
			args[argIndex] = cloneTypeExpr(arg, newID, fromArgument)
		}
		cloned[index] = PathSegment{
			Name: cloneIdent(segment.Name, newID, fromArgument), TypeArgs: args, Location: segment.Location,
		}
	}
	return cloned
}

func cloneParam(param Param, newID func(source.NodeID, bool) source.NodeID, fromArgument bool) Param {
	cloned := Param{IsMutable: param.IsMutable, MutableLocation: param.MutableLocation, Name: cloneIdent(param.Name, newID, fromArgument), Type: cloneTypeExpr(param.Type, newID, fromArgument), Location: param.Location}
	if param.Default != nil {
		cloned.Default = param.Default.copyExpr(nil, newID, fromArgument)
	}
	return cloned
}

func cloneReturnOrigins(origins *ReturnOriginClause, newID func(source.NodeID, bool) source.NodeID, fromArgument bool) *ReturnOriginClause {
	if origins == nil {
		return nil
	}
	sources := make([]*Ident, len(origins.Sources))
	for index, source := range origins.Sources {
		sources[index] = cloneIdent(source, newID, fromArgument)
	}
	return &ReturnOriginClause{Sources: sources, Location: origins.Location}
}
