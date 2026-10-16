// Package ascii classifies ASCII characters, given as bytes or runes.
package ascii

func IsLetter[C byte | rune](c C) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func IsDigit[C byte | rune](c C) bool { return '0' <= c && c <= '9' }

func IsAlnum[C byte | rune](c C) bool { return IsLetter(c) || IsDigit(c) }
