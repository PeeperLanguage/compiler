package analysis

// effectVisitor is the exhaustive consumer contract for semantic operations.
//
// Syntax additions that reuse existing operations never touch this interface.
// Adding a genuinely new semantic operation is different: the new effectOp must call
// a corresponding effectVisitor method from its visit implementation, which makes
// every semantic consumer fail compilation until it explicitly decides what
// the operation means. This is the deliberate "introduce new semantics to
// everyone" boundary; it is not used for AST dispatch throughout the compiler.
type effectVisitor interface {
	visitDefine(effectDefine)
	visitWrite(effectWrite)
	visitUse(effectUse)
	visitBorrow(effectBorrow)
	visitIterate(effectIterate)
	visitDiscard(effectDiscard)
	visitCallBegin(effectCallBegin)
	visitCallEnd(effectCallEnd)
}

// visitEffect dispatches one operation through the exhaustive semantic visitor.
func visitEffect(op effectOp, visitor effectVisitor) {
	if op == nil {
		panic("effect: cannot visit a nil operation")
	}
	if visitor == nil {
		panic("effect: cannot visit with a nil visitor")
	}
	op.visit(visitor)
}

func (op effectDefine) visit(visitor effectVisitor)    { visitor.visitDefine(op) }
func (op effectWrite) visit(visitor effectVisitor)     { visitor.visitWrite(op) }
func (op effectUse) visit(visitor effectVisitor)       { visitor.visitUse(op) }
func (op effectBorrow) visit(visitor effectVisitor)    { visitor.visitBorrow(op) }
func (op effectIterate) visit(visitor effectVisitor)   { visitor.visitIterate(op) }
func (op effectDiscard) visit(visitor effectVisitor)   { visitor.visitDiscard(op) }
func (op effectCallBegin) visit(visitor effectVisitor) { visitor.visitCallBegin(op) }
func (op effectCallEnd) visit(visitor effectVisitor)   { visitor.visitCallEnd(op) }
