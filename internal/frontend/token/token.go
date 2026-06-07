package token

import (
	"fmt"

	"compiler/internal/source"
)

type Token struct {
	Kind    Kind
	Literal string
	Start   source.Position
	End     source.Position
}

func (t Token) String() string {
	return fmt.Sprintf("%s(%q)", t.Kind, t.Literal)
}
