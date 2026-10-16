// Package token defines the tokens the lexer produces.
package token

import (
	"fmt"

	"compiler/internal/source"
)

// Token is one token: its kind and where its text is. The text itself is
// file.Text[Start:End]; literal values are decoded by the parser.
type Token struct {
	Kind Kind
	source.Span
}

func (t Token) String() string {
	return fmt.Sprintf("%s@%d-%d", t.Kind, t.Start, t.End)
}
