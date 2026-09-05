package effect

// Visitor is the exhaustive consumer contract for semantic operations.
//
// Syntax additions that reuse existing operations never touch this interface.
// Adding a genuinely new semantic operation is different: the new Op must call
// a corresponding Visitor method from its visit implementation, which makes
// every semantic consumer fail compilation until it explicitly decides what
// the operation means. This is the deliberate "introduce new semantics to
// everyone" boundary; it is not used for AST dispatch throughout the compiler.
type Visitor interface {
	VisitDefine(Define)
	VisitWrite(Write)
	VisitUse(Use)
	VisitBorrow(Borrow)
	VisitIterate(Iterate)
	VisitDiscard(Discard)
	VisitCallBegin(CallBegin)
	VisitCallEnd(CallEnd)
}

// Visit dispatches one operation through the exhaustive semantic visitor.
func Visit(op Op, visitor Visitor) {
	if op == nil {
		panic("effect: cannot visit a nil operation")
	}
	if visitor == nil {
		panic("effect: cannot visit with a nil visitor")
	}
	op.visit(visitor)
}

func (op Define) visit(visitor Visitor)    { visitor.VisitDefine(op) }
func (op Write) visit(visitor Visitor)     { visitor.VisitWrite(op) }
func (op Use) visit(visitor Visitor)       { visitor.VisitUse(op) }
func (op Borrow) visit(visitor Visitor)    { visitor.VisitBorrow(op) }
func (op Iterate) visit(visitor Visitor)   { visitor.VisitIterate(op) }
func (op Discard) visit(visitor Visitor)   { visitor.VisitDiscard(op) }
func (op CallBegin) visit(visitor Visitor) { visitor.VisitCallBegin(op) }
func (op CallEnd) visit(visitor Visitor)   { visitor.VisitCallEnd(op) }
