package token

var keywords = map[string]Kind{
	"import":   IMPORT,
	"const":    CONST,
	"type":     TYPE,
	"struct":   STRUCT,
	"iface":    IFACE,
	"enum":     ENUM,
	"union":    UNION,
	"error":    ERROR,
	"fn":       FN,
	"test":     TEST,
	"let":      LET,
	"if":       IF,
	"else":     ELSE,
	"match":    MATCH,
	"for":      FOR,
	"while":    WHILE,
	"break":    BREAK,
	"continue": CONTINUE,
	"return":   RETURN,
	"from":     FROM,
	"free":     FREE,
	"print":    PRINT,
	"println":  PRINTLN,
	"rawptr":   RAWPTR,
	"as":       AS,
	"is":       IS,
	"in":       IN,
	"with":     WITH,
	"mut":      MUT,
	"atomic":   ATOMIC,
	"comptime": COMPTIME,
	"lock":     LOCK,
	"defer":    DEFER,
	"panic":    PANIC,
	"release":  RELEASE,
	"catch":    CATCH,
	"orelse":   ORELSE,
	"none":     NONE,
	"true":     TRUE,
	"false":    FALSE,
	"unsafe":   UNSAFE,
}

// LookupIdent returns the keyword kind of ident, or IDENT.
func LookupIdent(ident string) Kind {
	if kind, ok := keywords[ident]; ok {
		return kind
	}
	return IDENT
}

func IsKeyword(ident string) bool {
	_, ok := keywords[ident]
	return ok
}
