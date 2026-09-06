package effect

import (
	"reflect"
	"testing"
)

type recordingVisitor struct{ kinds []string }

func (v *recordingVisitor) VisitDefine(Define)       { v.kinds = append(v.kinds, "define") }
func (v *recordingVisitor) VisitWrite(Write)         { v.kinds = append(v.kinds, "write") }
func (v *recordingVisitor) VisitUse(Use)             { v.kinds = append(v.kinds, "use") }
func (v *recordingVisitor) VisitBorrow(Borrow)       { v.kinds = append(v.kinds, "borrow") }
func (v *recordingVisitor) VisitIterate(Iterate)     { v.kinds = append(v.kinds, "iterate") }
func (v *recordingVisitor) VisitDiscard(Discard)     { v.kinds = append(v.kinds, "discard") }
func (v *recordingVisitor) VisitCallBegin(CallBegin) { v.kinds = append(v.kinds, "call-begin") }
func (v *recordingVisitor) VisitCallEnd(CallEnd)     { v.kinds = append(v.kinds, "call-end") }

func TestVisitorDispatchesEverySemanticOperation(t *testing.T) {
	ops := []Op{Define{}, Write{}, Use{}, Borrow{}, Iterate{}, Discard{}, CallBegin{}, CallEnd{}}
	visitor := &recordingVisitor{}
	for _, op := range ops {
		Visit(op, visitor)
	}
	want := []string{"define", "write", "use", "borrow", "iterate", "discard", "call-begin", "call-end"}
	if !reflect.DeepEqual(visitor.kinds, want) {
		t.Fatalf("visited %v, want %v", visitor.kinds, want)
	}
}
