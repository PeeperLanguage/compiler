// Package numeric holds the rules of Peeper number literals that the lexer, the
// parser and the type checker share.
package numeric

import "strings"

// Split splits a number token's text into its base, its digits (after
// any 0x, 0o or 0b prefix, `_` separators kept) and its type suffix. The suffix
// of a 0x, 0o or 0b number starts at its first `i` or `u` (`f` is a hex digit);
// that of a decimal number at its first `i`, `u` or `f`, letters a decimal
// number holds nowhere else.
func Split(text string) (base int, digits, suffix string) {
	base, digits, letters := 10, text, "iuf"
	if len(text) > 2 && text[0] == '0' {
		// text[1] is the prefix letter; setting bit 0x20 turns an ASCII
		// upper-case letter into lower case, so 0X matches 'x'.
		switch text[1] | 0x20 {
		case 'x':
			base = 16
		case 'o':
			base = 8
		case 'b':
			base = 2
		}
		if base != 10 {
			digits, letters = text[2:], "iu" // skip the two-byte prefix
		}
	}
	if i := strings.IndexAny(digits, letters); i >= 0 {
		return base, digits[:i], digits[i:]
	}
	return base, digits, ""
}
