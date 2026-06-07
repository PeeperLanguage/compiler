package resolver

import (
	"compiler/internal/source"
	"testing"
)

func Test_locationPtr(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		loc  source.Location
		want *source.Location
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := new(tt.loc)
			// TODO: update the condition below to compare got with tt.want.
			if true {
				t.Errorf("locationPtr() = %v, want %v", got, tt.want)
			}
		})
	}
}
