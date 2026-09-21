package thir

import (
	"fmt"

	"compiler/internal/ir"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/typednil"
)

func (m *Module) Validate() error {
	if m == nil {
		return fmt.Errorf("nil THIR module")
	}
	seen := make(map[ir.NodeID]Node)
	for index, function := range m.Functions {
		if function == nil {
			return fmt.Errorf("function %d is nil", index)
		}
		if function.Source.NodeID == 0 {
			return fmt.Errorf("function %d has no source identity", index)
		}
		if function.Symbol == nil {
			return fmt.Errorf("function %d has no symbol", function.Source.NodeID)
		}
		for paramIndex, parameter := range function.Params {
			if parameter.Symbol == nil || parameter.Type == nil {
				return fmt.Errorf("function %d parameter %d is incomplete", function.Source.NodeID, paramIndex)
			}
		}
		if function.Body == nil {
			continue
		}
		var validationError error
		Inspect(function.Body, func(node Node) bool {
			if err := node.validateSelf(); err != nil {
				validationError = fmt.Errorf("%T at node %d: %w", node, node.SourceInfo().NodeID, err)
				return false
			}
			id := node.SourceInfo().NodeID
			if previous := seen[id]; previous != nil && previous != node {
				validationError = fmt.Errorf("source node %d appears more than once in executable THIR", id)
				return false
			}
			seen[id] = node
			if m.byNodeID[id] != node {
				validationError = fmt.Errorf("source node %d is missing from module index", id)
				return false
			}
			return true
		})
		if validationError != nil {
			return validationError
		}
	}
	return nil
}

func (s StmtInfo) validateSelf() error {
	if s.Source.NodeID == 0 {
		return fmt.Errorf("missing source identity")
	}
	return nil
}

func (e ExprInfo) validateSelf() error { return e.validate(true) }

func (e ExprInfo) validate(requireType bool) error {
	if e.Source.NodeID == 0 {
		return fmt.Errorf("missing source identity")
	}
	if requireType && e.Type == nil {
		return fmt.Errorf("missing semantic type")
	}
	if e.Place != nil {
		if err := e.Place.validate(); err != nil {
			return err
		}
	}
	for index, implementation := range e.interfaceImplementations {
		if implementation.Symbol == nil || implementation.CallableType == nil {
			return fmt.Errorf("interface implementation %d is incomplete", index)
		}
	}
	return nil
}

func (p *Place) validate() error {
	if p == nil {
		return nil
	}
	if (p.Root == nil) == typednil.IsNil(p.Temporary) {
		return fmt.Errorf("place must have exactly one symbol or temporary root")
	}
	if p.Type == nil {
		return fmt.Errorf("place has no semantic type")
	}
	for index, projection := range p.Projections {
		if projection.Type == nil {
			return fmt.Errorf("place projection %d has no semantic type", index)
		}
		switch projection.Kind {
		case PlaceField:
			if projection.Name == "" {
				return fmt.Errorf("field projection %d has no field name", index)
			}
		case PlaceIndex:
			if typednil.IsNil(projection.Index) {
				return fmt.Errorf("index projection %d has no index", index)
			}
		default:
			return fmt.Errorf("place projection %d has unknown kind %d", index, projection.Kind)
		}
	}
	return nil
}

func (s *Block) validateSelf() error {
	if err := s.StmtInfo.validateSelf(); err != nil {
		return err
	}
	if s.Scope == nil {
		return fmt.Errorf("block has no lexical scope")
	}
	return nil
}

func (s *Binding) validateSelf() error {
	if err := s.StmtInfo.validateSelf(); err != nil {
		return err
	}
	if s.Symbol == nil {
		return fmt.Errorf("binding has no symbol")
	}
	return nil
}

func (s *ExprStmt) validateSelf() error {
	if err := s.StmtInfo.validateSelf(); err != nil {
		return err
	}
	if typednil.IsNil(s.Value) {
		return fmt.Errorf("expression statement has no value")
	}
	return nil
}

func (s *Assign) validateSelf() error {
	if err := s.StmtInfo.validateSelf(); err != nil {
		return err
	}
	if typednil.IsNil(s.Target) || s.Target.ExprPlace() == nil || typednil.IsNil(s.Value) {
		return fmt.Errorf("assignment requires place target and value")
	}
	return nil
}

func (s *If) validateSelf() error {
	if err := s.StmtInfo.validateSelf(); err != nil {
		return err
	}
	if typednil.IsNil(s.Condition) || s.Then == nil {
		return fmt.Errorf("if requires condition and then block")
	}
	return nil
}

func (s *For) validateSelf() error {
	if err := s.StmtInfo.validateSelf(); err != nil {
		return err
	}
	if s.Checked != nil {
		return nil
	}
	if s.Body == nil {
		return fmt.Errorf("loop has no body")
	}
	if s.Iterable != nil && s.Iteration == nil {
		return fmt.Errorf("for-in loop has no iteration plan")
	}
	switch plan := s.Iteration.(type) {
	case nil:
	case *RangeIteration:
		if plan.ElementType == nil || plan.Cursor == nil || plan.Limit == nil || s.Value == nil {
			return fmt.Errorf("range iteration plan is incomplete")
		}
	case *SequenceIteration:
		if plan.ElementType == nil || plan.Cursor == nil || plan.Value == nil || plan.Carrier == nil || plan.CarrierType == nil {
			return fmt.Errorf("sequence iteration plan is incomplete")
		}
	default:
		return fmt.Errorf("unknown iteration plan %T", s.Iteration)
	}
	return nil
}

func (s *Match) validateSelf() error {
	if err := s.StmtInfo.validateSelf(); err != nil {
		return err
	}
	if typednil.IsNil(s.Subject) || s.EnumType == nil || s.CaseCount <= 0 || len(s.Arms) == 0 {
		return fmt.Errorf("match evidence is incomplete")
	}
	for index, arm := range s.Arms {
		if arm.Case < 0 || arm.Case >= s.CaseCount || arm.Body == nil {
			return fmt.Errorf("match arm %d is incomplete", index)
		}
		for bindingIndex, binding := range arm.Bindings {
			if binding.Type == nil || binding.Symbol == nil {
				return fmt.Errorf("match arm %d binding %d is incomplete", index, bindingIndex)
			}
		}
	}
	return nil
}

func (e InvalidExpr) validateSelf() error {
	if e.Source.NodeID == 0 {
		return fmt.Errorf("missing source identity")
	}
	return fmt.Errorf("invalid expression reached validated THIR: %s", e.Message)
}

func (e *Range) validateSelf() error { return e.ExprInfo.validate(false) }

func (e *Ident) validateSelf() error {
	if err := e.ExprInfo.validateSelf(); err != nil {
		return err
	}
	if e.Symbol == nil {
		return fmt.Errorf("identifier %q has no symbol", e.Name)
	}
	return nil
}

func (e *QualifiedIdent) validateSelf() error {
	if err := e.ExprInfo.validateSelf(); err != nil {
		return err
	}
	if e.Symbol == nil {
		return fmt.Errorf("qualified identifier %q has no symbol", e.Name)
	}
	return nil
}

func (e *Field) validateSelf() error {
	if err := e.ExprInfo.validateSelf(); err != nil {
		return err
	}
	if typednil.IsNil(e.Base) || e.Name == "" {
		return fmt.Errorf("field access is incomplete")
	}
	return nil
}

func (e *Index) validateSelf() error {
	if err := e.ExprInfo.validateSelf(); err != nil {
		return err
	}
	if typednil.IsNil(e.Base) || typednil.IsNil(e.Index) {
		return fmt.Errorf("index expression is incomplete")
	}
	return nil
}

func (e *StructLiteral) validateSelf() error {
	if err := e.ExprInfo.validateSelf(); err != nil {
		return err
	}
	semantic, ok := typeinfo.Underlying(e.Type).(*typeinfo.StructType)
	if !ok || semantic == nil || len(e.Fields) != len(semantic.Fields) {
		return fmt.Errorf("struct literal fields do not match semantic type")
	}
	return nil
}

func (e *Variant) validateSelf() error {
	if err := e.ExprInfo.validateSelf(); err != nil {
		return err
	}
	if e.Case < 0 {
		return fmt.Errorf("variant construction is unresolved")
	}
	if typednil.IsNil(e.Payload) != (e.PayloadType == nil) {
		return fmt.Errorf("variant payload and expected type must be published together")
	}
	return nil
}

func (e *Call) validateSelf() error {
	if err := e.ExprInfo.validate(false); err != nil {
		return err
	}
	if typednil.IsNil(e.Callee) {
		return fmt.Errorf("call has no callee")
	}
	if e.Type == nil {
		callable, ok := typeinfo.Underlying(e.Callee.ExprType()).(*typeinfo.FuncType)
		if !ok || callable == nil || callable.Return != nil {
			return fmt.Errorf("value-producing call has no semantic type")
		}
	}
	return nil
}

func (e *Free) validateSelf() error {
	if err := e.ExprInfo.validate(false); err != nil {
		return err
	}
	if typednil.IsNil(e.Value) {
		return fmt.Errorf("free has no operand")
	}
	return nil
}

func (e *Print) validateSelf() error {
	if err := e.ExprInfo.validate(false); err != nil {
		return err
	}
	if typednil.IsNil(e.Value) {
		return fmt.Errorf("print has no operand")
	}
	return nil
}
