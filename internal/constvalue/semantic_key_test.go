package constvalue

import "testing"

func TestSemanticKeyIncludesTypeAndValue(t *testing.T) {
	i32, _ := NewIntText("1", "i32")
	i32Two, _ := NewIntText("2", "i32")
	i64, _ := NewIntText("1", "i64")
	text, _ := NewString("a:b", "str")
	leftReady, _ := NewVariant("left::Status", "Status", 0, []Value{i32})
	leftWaiting, _ := NewVariant("left::Status", "Status", 1, nil)
	rightReady, _ := NewVariant("right::Status", "Status", 0, []Value{i32})
	leftReadyTwo, _ := NewVariant("left::Status", "Status", 0, []Value{i32Two})

	if SemanticKey(i32) == SemanticKey(i64) {
		t.Fatal("integer type did not change constant key")
	}
	if SemanticKey(text) == "" {
		t.Fatal("string constant key is empty")
	}
	base := SemanticKey(leftReady)
	for name, value := range map[string]Value{
		"case":     leftWaiting,
		"identity": rightReady,
		"field":    leftReadyTwo,
	} {
		if base == SemanticKey(value) {
			t.Fatalf("variant %s did not change constant key", name)
		}
	}
}

func TestSemanticKeyFramesVariantIdentity(t *testing.T) {
	left, _ := NewVariant("a", "b:c", 0, nil)
	right, _ := NewVariant("a:b", "c", 0, nil)
	if SemanticKey(left) == SemanticKey(right) {
		t.Fatal("variant identity components collided")
	}
}

func TestNewVariantRejectsTypedNilField(t *testing.T) {
	var field Value = (*IntConst)(nil)
	if _, ok := NewVariant("test::Value", "Value", 0, []Value{field}); ok {
		t.Fatal("typed-nil variant field accepted")
	}
}
