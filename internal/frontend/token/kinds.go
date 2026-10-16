package token

// Kind is the kind of a token.
type Kind uint8

const (
	ILLEGAL Kind = iota
	EOF
	IDENT
	NUMBER
	STRING
	CSTRING
	CHAR
	BYTE_CHAR
	DOC_COMMENT
	ASSIGN
	PLUS
	MINUS
	ASTERISK
	SLASH
	PERCENT
	PLUS_ASSIGN
	MINUS_ASSIGN
	STAR_ASSIGN
	SLASH_ASSIGN
	PCT_ASSIGN
	PLUS_PLUS
	MINUS_MINUS
	BANG
	QUESTION
	AT
	AMP
	CARET
	TILDE
	LT
	GT
	SHL
	SHR
	EQ
	NEQ
	LE
	GE
	ANDAND
	OROR
	BAR
	PIPE_ARROW
	QQ
	BB
	FATARROW
	ARROW
	COLON
	DCOLON
	COMMA
	DOT
	DOTDOT
	DOTDOT_EQ
	ELLIPSIS
	HASH
	SEMICOLON
	LPAREN
	RPAREN
	LBRACE
	RBRACE
	LBRACK
	RBRACK
	IMPORT
	CONST
	TYPE
	STRUCT
	IFACE
	ENUM
	UNION
	ERROR
	FN
	TEST
	LET
	IF
	ELSE
	MATCH
	FOR
	WHILE
	BREAK
	CONTINUE
	RETURN
	FROM
	FREE
	PRINT
	PRINTLN
	RAWPTR
	AS
	IS
	IN
	WITH
	MUT
	ATOMIC
	COMPTIME
	LOCK
	DEFER
	PANIC
	RELEASE
	CATCH
	ORELSE
	NONE
	TRUE
	FALSE
	UNSAFE
)

// kindNames are the words used for each kind in messages.
var kindNames = [...]string{
	ILLEGAL:      "illegal",
	EOF:          "end of file",
	IDENT:        "identifier",
	NUMBER:       "number",
	STRING:       "string",
	CSTRING:      "c string",
	CHAR:         "character",
	BYTE_CHAR:    "byte character",
	DOC_COMMENT:  "document comment",
	ASSIGN:       "=",
	PLUS:         "+",
	MINUS:        "-",
	ASTERISK:     "*",
	SLASH:        "/",
	PERCENT:      "%",
	PLUS_ASSIGN:  "+=",
	MINUS_ASSIGN: "-=",
	STAR_ASSIGN:  "*=",
	SLASH_ASSIGN: "/=",
	PCT_ASSIGN:   "%=",
	PLUS_PLUS:    "++",
	MINUS_MINUS:  "--",
	BANG:         "!",
	QUESTION:     "?",
	AT:           "@",
	AMP:          "&",
	CARET:        "^",
	TILDE:        "~",
	LT:           "<",
	GT:           ">",
	SHL:          "<<",
	SHR:          ">>",
	EQ:           "==",
	NEQ:          "!=",
	LE:           "<=",
	GE:           ">=",
	ANDAND:       "&&",
	OROR:         "||",
	BAR:          "|",
	PIPE_ARROW:   "|>",
	QQ:           "??",
	BB:           "!!",
	FATARROW:     "=>",
	ARROW:        "->",
	COLON:        ":",
	DCOLON:       "::",
	COMMA:        ",",
	DOT:          ".",
	DOTDOT:       "..",
	DOTDOT_EQ:    "..=",
	ELLIPSIS:     "...",
	HASH:         "#",
	SEMICOLON:    ";",
	LPAREN:       "(",
	RPAREN:       ")",
	LBRACE:       "{",
	RBRACE:       "}",
	LBRACK:       "[",
	RBRACK:       "]",
	IMPORT:       "import",
	CONST:        "constant",
	TYPE:         "type",
	STRUCT:       "struct",
	IFACE:        "iface",
	ENUM:         "enum",
	UNION:        "union",
	ERROR:        "error",
	FN:           "function",
	TEST:         "test",
	LET:          "let",
	IF:           "if",
	ELSE:         "else",
	MATCH:        "match",
	FOR:          "for",
	WHILE:        "while",
	BREAK:        "break",
	CONTINUE:     "continue",
	RETURN:       "return",
	FROM:         "from",
	FREE:         "free",
	PRINT:        "print",
	PRINTLN:      "println",
	RAWPTR:       "rawptr",
	AS:           "as",
	IS:           "is",
	IN:           "in",
	WITH:         "with",
	MUT:          "mut",
	ATOMIC:       "atomic",
	COMPTIME:     "comptime",
	LOCK:         "lock",
	DEFER:        "defer",
	PANIC:        "panic",
	RELEASE:      "release",
	CATCH:        "catch",
	ORELSE:       "orelse",
	NONE:         "none",
	TRUE:         "true",
	FALSE:        "false",
	UNSAFE:       "unsafe",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}
