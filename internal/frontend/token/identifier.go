package token

import "regexp"

const IdentifierPattern = `[A-Za-z_][A-Za-z0-9_]*`

var symbolNamePattern = regexp.MustCompile(`^(?:` + IdentifierPattern + `)$`)

func IsValidSymbolName(name string) bool {
	return name != "_" && symbolNamePattern.MatchString(name) && !IsKeyword(name)
}
