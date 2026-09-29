package thir

import "compiler/pkg/typednil"

// Inspect traverses executable THIR in depth-first preorder. Child declarations
// live on concrete nodes, so new node kinds cannot silently depend on a central
// type switch.
func Inspect(node Node, visit func(Node) bool) {
	if typednil.IsNil(node) || visit == nil || !visit(node) {
		return
	}
	node.forEachChild(func(child Node) { Inspect(child, visit) })
}

func (s *Block) forEachChild(visit func(Node)) {
	for _, statement := range s.Stmts {
		visit(statement)
	}
}
func (s *Binding) forEachChild(visit func(Node))  { visit(s.Value) }
func (s *ExprStmt) forEachChild(visit func(Node)) { visit(s.Value) }
func (s *Assign) forEachChild(visit func(Node)) {
	visit(s.Target)
	visit(s.Value)
}
func (s *Return) forEachChild(visit func(Node)) { visit(s.Value) }
func (s *If) forEachChild(visit func(Node)) {
	visit(s.Condition)
	visit(s.Then)
	visit(s.Else)
}
func (s *For) forEachChild(visit func(Node)) {
	if s.Checked != nil {
		visit(s.Checked)
		return
	}
	visit(s.Iterable)
	visit(s.Condition)
	visit(s.Body)
}
func (*Break) forEachChild(func(Node))    {}
func (*Continue) forEachChild(func(Node)) {}
func (s *Match) forEachChild(visit func(Node)) {
	visit(s.Subject)
	for _, arm := range s.Arms {
		visit(arm.Body)
	}
}
func (*InvalidStmt) forEachChild(func(Node)) {}

func (*InvalidExpr) forEachChild(func(Node))    {}
func (*NumberLiteral) forEachChild(func(Node))  {}
func (*StringLiteral) forEachChild(func(Node))  {}
func (*ByteLiteral) forEachChild(func(Node))    {}
func (*CharLiteral) forEachChild(func(Node))    {}
func (*BoolLiteral) forEachChild(func(Node))    {}
func (*NoneLiteral) forEachChild(func(Node))    {}
func (*Ident) forEachChild(func(Node))          {}
func (*QualifiedIdent) forEachChild(func(Node)) {}
func (e *Field) forEachChild(visit func(Node))  { visit(e.Base) }
func (e *Index) forEachChild(visit func(Node)) {
	visit(e.Base)
	visit(e.Index)
}
func (e *Range) forEachChild(visit func(Node)) {
	visit(e.Start)
	visit(e.End)
}
func (e *StructLiteral) forEachChild(visit func(Node)) {
	for _, field := range e.Fields {
		visit(field.Value)
	}
}
func (e *Variant) forEachChild(visit func(Node)) { visit(e.Payload) }
func (e *ArrayLiteral) forEachChild(visit func(Node)) {
	for _, value := range e.Values {
		visit(value)
	}
}
func (e *Address) forEachChild(visit func(Node)) { visit(e.Value) }
func (e *Unary) forEachChild(visit func(Node))   { visit(e.Value) }
func (e *Binary) forEachChild(visit func(Node)) {
	visit(e.Left)
	visit(e.Right)
}
func (e *Is) forEachChild(visit func(Node)) { visit(e.Value) }
func (e *Call) forEachChild(visit func(Node)) {
	visit(e.Callee)
	for _, argument := range e.Args {
		visit(argument)
	}
}
func (e *Free) forEachChild(visit func(Node))  { visit(e.Value) }
func (e *Print) forEachChild(visit func(Node)) { visit(e.Value) }
func (e *Cast) forEachChild(visit func(Node))  { visit(e.Value) }
