package typednil

import "testing"

func TestIsNil(t *testing.T) {
	type probe struct{ field int }
	var typedNil *probe
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{"untyped nil", nil, true},
		{"typed-nil pointer in interface", typedNil, true},
		{"nil map", map[string]int(nil), false},
		{"nil slice", []int(nil), false},
		{"live pointer", &probe{}, false},
		{"non-pointer value", probe{}, false},
		{"string", "peeper", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsNil(test.value); got != test.want {
				t.Fatalf("IsNil(%v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
