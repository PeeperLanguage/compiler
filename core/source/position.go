package source

import "fmt"

type Position struct {
	Index  int
	Line   int
	Column int
}

func NewPosition() Position {
	return Position{Line: 1, Column: 1}
}

func (p Position) String() string {
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

func (p *Position) Advance(toSkip string) *Position {
	for _, char := range toSkip {
		switch char {
		case '\n':
			p.Line++
			p.Column = 1
			p.Index++
		case '\t':
			// Treat tab as single column to match editor behavior
			p.Column++
			p.Index++
		default:
			p.Column++
			p.Index += len(string(char))
		}
	}
	return p
}
