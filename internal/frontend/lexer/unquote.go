package lexer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// escape reads the escape sequence at s[i], a backslash, and returns the byte
// it stands for and its length in s. quote is the literal's quote character,
// the only quote that may be escaped. Every escape stands for one byte: \n \r
// \t \0 \\ \" \' and \xHH.
func escape(s string, i int, quote byte) (value byte, width int, err error) {
	if i+1 >= len(s) {
		return 0, 1, errors.New("unterminated escape sequence")
	}
	switch c := s[i+1]; c {
	case 'n':
		return '\n', 2, nil
	case 'r':
		return '\r', 2, nil
	case 't':
		return '\t', 2, nil
	case '0':
		return 0, 2, nil
	case '\\':
		return '\\', 2, nil
	case '"', '\'':
		if c != quote {
			return 0, 2, fmt.Errorf(`invalid escape sequence \%c`, c)
		}
		return c, 2, nil
	case 'x':
		digits := 0
		for digits < 2 && i+2+digits < len(s) && s[i+2+digits] != quote && s[i+2+digits] != '\n' {
			digits++
		}
		if digits < 2 {
			return 0, 2 + digits, errors.New(`hex escape \x requires exactly two hexadecimal digits`)
		}
		hex := s[i+2 : i+4]
		v, err := strconv.ParseUint(hex, 16, 8)
		if err != nil {
			return 0, 4, fmt.Errorf("invalid hex escape %q", `\x`+hex)
		}
		return byte(v), 4, nil
	}
	_, w := utf8.DecodeRuneInString(s[i+1:])
	return 0, 1 + w, fmt.Errorf("unknown escape sequence %q", s[i:i+1+w])
}

// unquote decodes the escape sequences in the text between the quotes of a
// literal; quote is the literal's quote character.
func unquote(s string, quote byte) (string, error) {
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
		value, width, err := escape(s, i, quote)
		if err != nil {
			return "", err
		}
		out = append(out, value)
		i += width
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
	if value, err := unquote(inner, quote); err == nil {
		return value
	}
	return inner
}
