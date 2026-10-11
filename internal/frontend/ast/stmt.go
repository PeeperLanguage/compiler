package ast

import "compiler/internal/source"

type BlockStmt struct {
	NodeIDHolder
	Documented
	Stmts    []Stmt
	Location *source.Location
}

func (*BlockStmt) stmtNode() {}
func (s *BlockStmt) forEachChild(visit func(Node)) {
	for _, stmt := range s.Stmts {
		visit(stmt)
	}
}
func (s *BlockStmt) loc() *source.Location { return s.Location }

type ExprStmt struct {
	NodeIDHolder
	Documented
	Expr     Expr
	Location *source.Location
}

func (*ExprStmt) stmtNode()                       {}
func (s *ExprStmt) forEachChild(visit func(Node)) { visit(s.Expr) }
func (s *ExprStmt) loc() *source.Location         { return s.Location }

type AssignStmt struct {
	NodeIDHolder
	Documented
	Target   Expr
	Value    Expr
	Location *source.Location
}

func (*AssignStmt) stmtNode() {}
func (s *AssignStmt) forEachChild(visit func(Node)) {
	visit(s.Target)
	visit(s.Value)
}
func (s *AssignStmt) loc() *source.Location { return s.Location }

// DestructureStmt moves a struct value whole and gives each listed field its
// own local: `let .{ field, field as name, _ } = value;`. HasRest records a
// trailing `_`, which discards the other fields.
type DestructureStmt struct {
	NodeIDHolder
	Documented
	Fields   []DestructureField
	HasRest  bool
	Value    Expr
	Location *source.Location
}

// DestructureField is one listed field. Binding is the local it becomes: the
// name after `as`, or one with the field's own name.
type DestructureField struct {
	Name     *Ident
	Binding  *Ident
	Location *source.Location
}

func (*DestructureStmt) stmtNode() {}
func (s *DestructureStmt) forEachChild(visit func(Node)) {
	for _, field := range s.Fields {
		visit(field.Name)
		visit(field.Binding)
	}
	visit(s.Value)
}
func (s *DestructureStmt) loc() *source.Location { return s.Location }

type ReturnStmt struct {
	NodeIDHolder
	Documented
	Value    Expr
	Location *source.Location
}

func (*ReturnStmt) stmtNode()                       {}
func (s *ReturnStmt) forEachChild(visit func(Node)) { visit(s.Value) }
func (s *ReturnStmt) loc() *source.Location         { return s.Location }

type BadStmt struct {
	NodeIDHolder
	Location *source.Location
}

func (*BadStmt) stmtNode()               {}
func (*BadStmt) forEachChild(func(Node)) {}
func (s *BadStmt) loc() *source.Location { return s.Location }

type IfStmt struct {
	NodeIDHolder
	Documented
	Cond     Expr
	Then     *BlockStmt
	Else     Stmt
	Location *source.Location
}

func (*IfStmt) stmtNode() {}
func (s *IfStmt) forEachChild(visit func(Node)) {
	visit(s.Cond)
	visit(s.Then)
	visit(s.Else)
}
func (s *IfStmt) loc() *source.Location { return s.Location }

type ForStmt struct {
	NodeIDHolder
	Documented
	Index    *Ident
	Value    *Ident
	Iterable Expr
	Cond     Expr
	Body     *BlockStmt
	Location *source.Location
}

func (*ForStmt) stmtNode() {}
func (s *ForStmt) forEachChild(visit func(Node)) {
	visit(s.Index)
	visit(s.Value)
	visit(s.Iterable)
	visit(s.Cond)
	visit(s.Body)
}
func (s *ForStmt) loc() *source.Location { return s.Location }

type BreakStmt struct {
	NodeIDHolder
	Location *source.Location
}

func (*BreakStmt) stmtNode()                 {}
func (s *BreakStmt) forEachChild(func(Node)) {}
func (s *BreakStmt) loc() *source.Location   { return s.Location }

type ContinueStmt struct {
	NodeIDHolder
	Location *source.Location
}

func (*ContinueStmt) stmtNode()                 {}
func (s *ContinueStmt) forEachChild(func(Node)) {}
func (s *ContinueStmt) loc() *source.Location   { return s.Location }

// Pattern is what a match arm does with the payload of its case, written after
// `with`. It has one form:
//
//	none                       the payload is an optional holding nothing
//	name                       name is the payload
//	.{ field, field as name }  a local per field of a struct payload
//	Enum::Case                 the payload is an enum holding Case, with an
//	                           optional pattern of its own for that payload
type Pattern struct {
	NodeIDHolder
	IsNone bool
	Name   *Ident
	// IsStruct marks the `.{ ... }` form; HasRest records its trailing `_`.
	IsStruct bool
	Fields   []DestructureField
	HasRest  bool
	Case     *ScopeResolution
	Inner    *Pattern
	Location *source.Location
}

func (p *Pattern) forEachChild(visit func(Node)) {
	visit(p.Name)
	for _, field := range p.Fields {
		visit(field.Name)
		visit(field.Binding)
	}
	visit(p.Case)
	visit(p.Inner)
}
func (p *Pattern) loc() *source.Location { return p.Location }

// Taking returns the part of the pattern that names things: p itself, or for
// a case of an enum payload the pattern after it. It is nil when the pattern
// only tests.
func (p *Pattern) Taking() *Pattern {
	if p != nil && p.Case != nil {
		p = p.Inner
	}
	if p == nil || p.Name == nil && !p.IsStruct {
		return nil
	}
	return p
}

// Bindings lists the names the pattern declares.
func (p *Pattern) Bindings() []*Ident {
	taking := p.Taking()
	if taking == nil {
		return nil
	}
	if taking.Name != nil {
		return []*Ident{taking.Name}
	}
	names := make([]*Ident, len(taking.Fields))
	for index, field := range taking.Fields {
		names[index] = field.Binding
	}
	return names
}

// MatchArm is `Case => body`, or with a Pattern for the case's payload
// `Case with pattern => body`. The fallback arm `_ => body` has no Case and
// handles whatever is left.
type MatchArm struct {
	NodeIDHolder
	IsFallback bool
	Case       *ScopeResolution
	Pattern    *Pattern
	Body       *BlockStmt
	Location   *source.Location
}

func (a *MatchArm) forEachChild(visit func(Node)) {
	visit(a.Case)
	visit(a.Pattern)
	visit(a.Body)
}
func (a *MatchArm) loc() *source.Location { return a.Location }

type MatchStmt struct {
	NodeIDHolder
	Documented
	Subject         Expr
	Arms            []*MatchArm
	ArmListLocation *source.Location
	Location        *source.Location
}

func (*MatchStmt) stmtNode() {}
func (s *MatchStmt) forEachChild(visit func(Node)) {
	visit(s.Subject)
	for _, arm := range s.Arms {
		visit(arm)
	}
}
func (s *MatchStmt) loc() *source.Location { return s.Location }
