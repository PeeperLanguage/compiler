package ast

import "sync/atomic"

var nextSyntheticNodeID atomic.Uint32

// SubstituteExpr clones an expression for call-site expansion. Parameter
// identifiers are replaced with their already-evaluated argument expressions;
// every cloned node gets a separate high-range ID so semantic caches cannot
// collide with parser-assigned nodes.
//
// Clone logic lives on each expression type via the Expr.copyExpr interface
// method. Adding a new Expr type that is missing copyExpr produces a compile
// error, so there is no silent default fallthrough.
func SubstituteExpr(expr Expr, substitutions map[string]Expr) (cloned Expr, defaultClones map[NodeID]NodeID, argumentClones map[NodeID]NodeID) {
	if expr == nil {
		return nil, nil, nil
	}
	defaultClones = make(map[NodeID]NodeID)
	argumentClones = make(map[NodeID]NodeID)
	cloneID := func() NodeID {
		return NodeID(nextSyntheticNodeID.Add(1) | (1 << 31))
	}
	newID := func(original NodeID, fromArgument bool) NodeID {
		id := cloneID()
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

func cloneIdent(ident *Ident, newID func(NodeID, bool) NodeID, fromArgument bool) *Ident {
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
// their owning expression. Type nodes have no copy interface because general
// type syntax is not otherwise cloned.
func cloneTypeExpr(typ TypeExpr, newID func(NodeID, bool) NodeID, fromArgument bool) TypeExpr {
	if typ == nil {
		return nil
	}
	id := NodeIDHolder{NodeID: newID(typ.ID(), fromArgument)}
	switch typ := typ.(type) {
	case *NamedType:
		return &NamedType{NodeIDHolder: id, Name: typ.Name, Location: typ.Location}
	case *AppliedType:
		args := make([]TypeExpr, len(typ.TypeArgs))
		for index, arg := range typ.TypeArgs {
			args[index] = cloneTypeExpr(arg, newID, fromArgument)
		}
		return &AppliedType{NodeIDHolder: id, Name: cloneIdent(typ.Name, newID, fromArgument), TypeArgs: args, Location: typ.Location}
	case *OwnedPtrType:
		return &OwnedPtrType{NodeIDHolder: id, Target: cloneTypeExpr(typ.Target, newID, fromArgument), Location: typ.Location}
	case *RawPtrType:
		return &RawPtrType{NodeIDHolder: id, Location: typ.Location}
	case *RefType:
		return &RefType{NodeIDHolder: id, Mutable: typ.Mutable, Target: cloneTypeExpr(typ.Target, newID, fromArgument), Location: typ.Location}
	case *OptionalType:
		return &OptionalType{NodeIDHolder: id, Inner: cloneTypeExpr(typ.Inner, newID, fromArgument), Location: typ.Location}
	case *ArrayType:
		var length *NumberLit
		if typ.Len != nil {
			length = typ.Len.copyExpr(nil, newID, fromArgument).(*NumberLit)
		}
		return &ArrayType{NodeIDHolder: id, Len: length, Shape: typ.Shape, Elem: cloneTypeExpr(typ.Elem, newID, fromArgument), Location: typ.Location}
	case *FuncType:
		params := make([]Param, len(typ.Params))
		for index, param := range typ.Params {
			params[index] = cloneParam(param, newID, fromArgument)
		}
		return &FuncType{NodeIDHolder: id, Params: params, Return: cloneTypeExpr(typ.Return, newID, fromArgument), ReturnOrigins: cloneReturnOrigins(typ.ReturnOrigins, newID, fromArgument), Location: typ.Location}
	case *StructType:
		fields := make([]TypeField, len(typ.Fields))
		for index, field := range typ.Fields {
			fields[index] = TypeField{Name: cloneIdent(field.Name, newID, fromArgument), Type: cloneTypeExpr(field.Type, newID, fromArgument), Location: field.Location}
		}
		return &StructType{NodeIDHolder: id, Fields: fields, Location: typ.Location}
	case *InterfaceType:
		methods := make([]TypeMethod, len(typ.Methods))
		for index, method := range typ.Methods {
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
		return &InterfaceType{NodeIDHolder: id, Methods: methods, Location: typ.Location}
	case *EnumType:
		variants := make([]EnumVariant, len(typ.Variants))
		for index, variant := range typ.Variants {
			variants[index] = EnumVariant{
				Name:     cloneIdent(variant.Name, newID, fromArgument),
				Payload:  cloneTypeExpr(variant.Payload, newID, fromArgument),
				Location: variant.Location,
			}
		}
		return &EnumType{NodeIDHolder: id, Variants: variants, Location: typ.Location}
	case *ScopeResolution:
		return &ScopeResolution{NodeIDHolder: id, Segments: clonePathSegments(typ.Segments, newID, fromArgument), Location: typ.Location}
	default:
		panic("unhandled type expression in call-default clone")
	}
}

func clonePathSegments(segments []PathSegment, newID func(NodeID, bool) NodeID, fromArgument bool) []PathSegment {
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

func cloneParam(param Param, newID func(NodeID, bool) NodeID, fromArgument bool) Param {
	cloned := Param{IsMutable: param.IsMutable, Name: cloneIdent(param.Name, newID, fromArgument), Type: cloneTypeExpr(param.Type, newID, fromArgument), Location: param.Location}
	if param.Default != nil {
		cloned.Default = param.Default.copyExpr(nil, newID, fromArgument)
	}
	return cloned
}

func cloneReturnOrigins(origins *ReturnOriginClause, newID func(NodeID, bool) NodeID, fromArgument bool) *ReturnOriginClause {
	if origins == nil {
		return nil
	}
	sources := make([]*Ident, len(origins.Sources))
	for index, source := range origins.Sources {
		sources[index] = cloneIdent(source, newID, fromArgument)
	}
	return &ReturnOriginClause{Sources: sources, Location: origins.Location}
}
