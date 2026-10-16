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

// Operator is an operator or punctuation token by its text.
type Operator struct {
	Text string
	Kind Kind
}

// Operators lists every operator and punctuation token, longest first within
// each leading character, so the lexer's first match is the right one.
var Operators = [...]Operator{
	{"::", DCOLON},
	{":", COLON},
	{"==", EQ},
	{"=>", FATARROW},
	{"=", ASSIGN},
	{"!=", NEQ},
	{"!!", BB},
	{"!", BANG},
	{"<=", LE},
	{"<<", SHL},
	{"<", LT},
	{">=", GE},
	{">>", SHR},
	{">", GT},
	{"&&", ANDAND},
	{"&", AMP},
	{"||", OROR},
	{"|>", PIPE_ARROW},
	{"|", BAR},
	{"??", QQ},
	{"?", QUESTION},
	{"->", ARROW},
	{"--", MINUS_MINUS},
	{"-=", MINUS_ASSIGN},
	{"-", MINUS},
	{"++", PLUS_PLUS},
	{"+=", PLUS_ASSIGN},
	{"+", PLUS},
	{"*=", STAR_ASSIGN},
	{"*", ASTERISK},
	{"/=", SLASH_ASSIGN},
	{"/", SLASH},
	{"%=", PCT_ASSIGN},
	{"%", PERCENT},
	{"...", ELLIPSIS},
	{"..=", DOTDOT_EQ},
	{"..", DOTDOT},
	{".", DOT},
	{"@", AT},
	{"^", CARET},
	{"~", TILDE},
	{",", COMMA},
	{"#", HASH},
	{";", SEMICOLON},
	{"(", LPAREN},
	{")", RPAREN},
	{"{", LBRACE},
	{"}", RBRACE},
	{"[", LBRACK},
	{"]", RBRACK},
}

// spellings holds, by kind, the one text every token of that kind is written
// as: the keywords and Operators. Names and literals have none.
var spellings [256]string

func init() {
	for text, kind := range keywords {
		spellings[kind] = text
	}
	for _, op := range Operators {
		spellings[op.Kind] = op.Text
	}
}

// Spelling returns the text every token of kind is written as, and false for
// kinds without one fixed text, such as names and literals.
func Spelling(kind Kind) (string, bool) {
	return spellings[kind], spellings[kind] != ""
}
