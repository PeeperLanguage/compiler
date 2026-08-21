package ascii

func IsLetter(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z'
}

func IsDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func IsAlnum(r rune) bool {
	return IsLetter(r) || IsDigit(r)
}
