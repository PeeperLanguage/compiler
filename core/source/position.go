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
			// Move to next tab stop (every 4 columns, starting from 1)
			tabWidth := 4
			p.Column += tabWidth - ((p.Column - 1) % tabWidth)
			p.Index++
		default:
			p.Column++
			p.Index += len(string(char))
		}
	}
	return p
}
