package analysis

import (
	"reflect"
	"testing"
)

type recordingVisitor struct{ kinds []string }

func (v *recordingVisitor) visitDefine(effectDefine)   { v.kinds = append(v.kinds, "define") }
func (v *recordingVisitor) visitWrite(effectWrite)     { v.kinds = append(v.kinds, "write") }
func (v *recordingVisitor) visitUse(effectUse)         { v.kinds = append(v.kinds, "use") }
func (v *recordingVisitor) visitBorrow(effectBorrow)   { v.kinds = append(v.kinds, "borrow") }
func (v *recordingVisitor) visitIterate(effectIterate) { v.kinds = append(v.kinds, "iterate") }
func (v *recordingVisitor) visitDiscard(effectDiscard) { v.kinds = append(v.kinds, "discard") }
func (v *recordingVisitor) visitCallBegin(effectCallBegin) {
	v.kinds = append(v.kinds, "call-begin")
}
func (v *recordingVisitor) visitCallEnd(effectCallEnd) {
	v.kinds = append(v.kinds, "call-end")
}

func TestVisitorDispatchesEverySemanticOperation(t *testing.T) {
	ops := []effectOp{effectDefine{}, effectWrite{}, effectUse{}, effectBorrow{}, effectIterate{}, effectDiscard{}, effectCallBegin{}, effectCallEnd{}}
	visitor := &recordingVisitor{}
	for _, op := range ops {
		visitEffect(op, visitor)
	}
	want := []string{"define", "write", "use", "borrow", "iterate", "discard", "call-begin", "call-end"}
	if !reflect.DeepEqual(visitor.kinds, want) {
		t.Fatalf("visited %v, want %v", visitor.kinds, want)
	}
}
