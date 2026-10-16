package lexer

import (
	"fmt"
	"strconv"
	"strings"
)

// Unquote decodes the escape sequences in the text between the quotes of a
// literal. quote is the literal's quote character; only it may be escaped.
func Unquote(s string, quote byte) (string, error) {
	if strings.IndexByte(s, '\\') < 0 {
		return s, nil
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			out = append(out, s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("unterminated escape sequence")
		}
		switch s[i+1] {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case '0':
			out = append(out, 0)
		case '\\':
			out = append(out, '\\')
		case '"', '\'':
			if s[i+1] != quote {
				return "", fmt.Errorf(`invalid escape sequence \%c`, s[i+1])
			}
			out = append(out, quote)
		case 'x':
			if i+3 >= len(s) {
				return "", fmt.Errorf(`hex escape \x requires exactly two hexadecimal digits`)
			}
			value, err := strconv.ParseUint(s[i+2:i+4], 16, 8)
			if err != nil {
				return "", fmt.Errorf("invalid hex escape %q", `\x`+s[i+2:i+4])
			}
			out = append(out, byte(value))
			i += 2
		default:
			return "", fmt.Errorf("unknown escape sequence %q", s[i:i+2])
		}
		i += 2
	}
	return string(out), nil
}

// Value returns the value of a string, character or byte literal token's text.
// For a literal the lexer reported as invalid it returns its best reading, so
// the error is reported once, by the lexer.
func Value(text string) string {
	prefix := 0
	if text[0] == 'c' || text[0] == 'b' {
		prefix = 1
	}
	quote := text[prefix]
	inner := strings.TrimSuffix(text[prefix+1:], string(quote))
	if value, err := Unquote(inner, quote); err == nil {
		return value
	}
	return inner
}
