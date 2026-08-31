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
	"none":     NONE,
	"true":     TRUE,
	"false":    FALSE,
	"unsafe":   UNSAFE,
}

var keywordDocs = map[Kind]string{
	IMPORT:   "Import a module into the current file scope.",
	CONST:    "Declare an immutable binding.",
	TYPE:     "Declare a named type.",
	STRUCT:   "Define a struct type body.",
	IFACE:    "Define an interface contract.",
	ENUM:     "Define an enum type body.",
	UNION:    "Define a union type body.",
	ERROR:    "Define an error set type body.",
	FN:       "Declare a function or method.",
	TEST:     "Declare a test function.",
	LET:      "Declare a local or module binding.",
	IF:       "Start a conditional branch.",
	ELSE:     "Fallback branch for an if expression or statement.",
	MATCH:    "Pattern-match a value by arms.",
	FOR:      "Iterate over an iterable value.",
	WHILE:    "Loop while condition is true.",
	BREAK:    "Exit the current loop.",
	CONTINUE: "Skip to the next loop iteration.",
	RETURN:   "Return from the current function.",
	FROM:     "Declare which borrowed parameter origins a reference return may use.",
	FREE:     "Consume and release an owned heap value.",
	PRINT:    "Write one primitive scalar value to standard output.",
	RAWPTR:   "Name an opaque unsafe non-owning pointer.",
	AS:       "Cast an expression to a target type.",
	IS:       "Check whether a value conforms to a target type.",
	IN:       "Iterate over an iterable value in a for loop.",
	WITH:     "Attach a payload value to an enum variant.",
	MUT:      "Mark a binding or reference as mutable.",
	ATOMIC:   "Declare or name atomic storage.",
	COMPTIME: "Force compile-time evaluation.",
	LOCK:     "Acquire a lock guard for the block scope.",
	DEFER:    "Run a statement when the current scope exits.",
	PANIC:    "Abort with an error payload.",
	RELEASE:  "Release ownership-managed value(s).",
	CATCH:    "Handle error-union fallback path.",
	NONE:     "Optional-value sentinel representing no value.",
	TRUE:     "Boolean true literal.",
	FALSE:    "Boolean false literal.",
	UNSAFE:   "Enter an unsafe context for unchecked operations.",
}

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

func KeywordDocByKind(kind Kind) (string, bool) {
	doc, ok := keywordDocs[kind]
	return doc, ok
}

func KeywordDoc(ident string) (string, bool) {
	kind, ok := keywords[ident]
	if !ok {
		return "", false
	}
	return KeywordDocByKind(kind)
}
