// Package numeric holds the rules of Peeper number literals that the lexer, the
// parser and the type checker share. Each phase asks its question here and
// reports in its own words.
package numeric

import (
	"strings"

	"compiler/pkg/ascii"
)

// Literal is a number literal's text in its parts.
type Literal struct {
	Base   int    // 2, 8, 10 or 16
	Digits string // the digits after any 0x, 0o or 0b prefix, `_` separators kept
	Suffix string // the type suffix, such as i32 or f64, or ""
}

// Split splits a number token's text into its parts. The suffix of a 0x, 0o or
// 0b number starts at its first `i` or `u` (`f` is a hex digit); that of a
// decimal number at its first `i`, `u` or `f`, letters a decimal number holds
// nowhere else.
func Split(text string) Literal {
	l, letters := Literal{Base: 10, Digits: text}, "iuf"
	if len(text) > 2 && text[0] == '0' {
		// text[1] is the prefix letter; setting bit 0x20 turns an ASCII
		// upper-case letter into lower case, so 0X matches 'x'.
		switch text[1] | 0x20 {
		case 'x':
			l.Base = 16
		case 'o':
			l.Base = 8
		case 'b':
			l.Base = 2
		}
		if l.Base != 10 {
			l.Digits, letters = text[2:], "iu" // skip the two-byte prefix
		}
	}
	if i := strings.IndexAny(l.Digits, letters); i >= 0 {
		l.Digits, l.Suffix = l.Digits[:i], l.Digits[i:]
	}
	return l
}

// DigitsInBase reports whether the digits hold at least one digit and only
// digits of the base, besides `_` separators and, in a decimal number, the
// marks of a fraction and an exponent (. e E + -).
func (l Literal) DigitsInBase() bool {
	count := 0
	for i := 0; i < len(l.Digits); i++ {
		c := l.Digits[i]
		if c == '_' || l.Base == 10 && strings.IndexByte(".eE+-", c) >= 0 {
			continue
		}
		// A digit's value: 0-9 for '0'-'9', 10-35 for 'a'-'z' in either case
		// (bit 0x20 makes a letter lower case). 36 is above every base, so any
		// other byte is rejected.
		value := 36
		if ascii.IsDigit(c) {
			value = int(c - '0')
		} else if ascii.IsLetter(c) {
			value = int(c|0x20-'a') + 10
		}
		if value >= l.Base {
			return false
		}
		count++
	}
	return count > 0
}

// IsFloat reports whether the digits are written as a float: a decimal number
// with a fraction or an exponent.
func (l Literal) IsFloat() bool {
	return l.Base == 10 && strings.ContainsAny(l.Digits, ".eE")
}

// SuffixWellFormed reports whether the suffix, if there is one, is its letter
// followed by a size (i32, not i) and holds no `_`.
func (l Literal) SuffixWellFormed() bool {
	return l.Suffix == "" || len(l.Suffix) > 1 && strings.IndexByte(l.Suffix, '_') < 0
}

// IntegerSuffix reports whether the suffix names an integer type: its letter
// is i (signed) or u (unsigned); f names a float type.
func (l Literal) IntegerSuffix() bool {
	return l.Suffix != "" && l.Suffix[0] != 'f'
}
