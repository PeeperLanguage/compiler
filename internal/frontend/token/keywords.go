package token

var keywords = map[string]Kind{
	"import":    IMPORT,
	"const":     CONST,
	"type":      TYPE,
	"struct":    STRUCT,
	"interface": INTERFACE,
	"enum":      ENUM,
	"union":     UNION,
	"error":     ERROR,
	"fn":        FN,
	"test":      TEST,
	"let":       LET,
	"if":        IF,
	"else":      ELSE,
	"match":     MATCH,
	"for":       FOR,
	"while":     WHILE,
	"break":     BREAK,
	"continue":  CONTINUE,
	"return":    RETURN,
	"as":        AS,
	"is":        IS,
	"mut":       MUT,
	"atomic":    ATOMIC,
	"comptime":  COMPTIME,
	"lock":      LOCK,
	"defer":     DEFER,
	"panic":     PANIC,
	"release":   RELEASE,
	"catch":     CATCH,
	"none":      NONE,
	"unsafe":    UNSAFE,
	"impl":      IMPL,
}

var keywordDocs = map[Kind]string{
	IMPORT:    "Import a module into the current file scope.",
	CONST:     "Declare an immutable binding.",
	TYPE:      "Declare a named type.",
	STRUCT:    "Define a struct type body.",
	INTERFACE: "Define an interface type body.",
	ENUM:      "Define an enum type body.",
	UNION:     "Define a union type body.",
	ERROR:     "Define an error set type body.",
	FN:        "Declare a function or method.",
	TEST:      "Declare a test function.",
	LET:       "Declare a local or module binding.",
	IF:        "Start a conditional branch.",
	ELSE:      "Fallback branch for an if expression or statement.",
	MATCH:     "Pattern-match a value by arms.",
	FOR:       "Iterate over an iterable value.",
	WHILE:     "Loop while condition is true.",
	BREAK:     "Exit the current loop.",
	CONTINUE:  "Skip to the next loop iteration.",
	RETURN:    "Return from the current function.",
	AS:        "Cast an expression to a target type.",
	IS:        "Check whether a value conforms to a target type.",
	MUT:       "Mark a binding or reference as mutable.",
	ATOMIC:    "Declare or name atomic storage.",
	COMPTIME:  "Force compile-time evaluation.",
	LOCK:      "Acquire a lock guard for the block scope.",
	DEFER:     "Run a statement when the current scope exits.",
	PANIC:     "Abort with an error payload.",
	RELEASE:   "Release ownership-managed value(s).",
	CATCH:     "Handle error-union fallback path.",
	NONE:      "Optional-value sentinel representing no value.",
	UNSAFE:    "Enter an unsafe context for unchecked operations.",
	IMPL:      "Attach methods to a target type.",
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
