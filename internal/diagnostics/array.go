package diagnostics

import (
	"fmt"

	"compiler/internal/source"
)

func ArrayIndexOutOfBounds(index, length string, loc *source.Location) *Diagnostic {
	msg := fmt.Sprintf("array index out of bounds: index %s for length %s", index, length)
	d := NewError(msg).WithCode(ErrArrayOutOfBounds)
	if loc != nil {
		d.WithPrimaryLabel(loc, msg)
	}
	return d
}
