package problems

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/source"
)

func ArrayIndexOutOfBounds(index, length string, loc *source.Location) *diagnostics.Diagnostic {
	msg := fmt.Sprintf("array index out of bounds: index %s for length %s", index, length)
	d := diagnostics.NewError(msg).WithCode(diagnostics.ErrArrayOutOfBounds)
	if loc != nil {
		d.WithPrimaryLabel(loc, msg)
	}
	return d
}
