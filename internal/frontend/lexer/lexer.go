// Package lexer turns source text into tokens.
package lexer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"compiler/internal/diag"
	"compiler/internal/frontend/literal"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
	"compiler/pkg/ascii"
	"compiler/pkg/numeric"
)

// Scan returns the tokens of file, ending with EOF, and the lexical errors in it.
// An illegal character is reported and produces no token. A malformed or
// unclosed literal is reported and still produces its token, so parsing goes on
// without a second error.
func Scan(file *source.File) ([]token.Token, []diag.Diagnostic) {
	s := scanner{file: file, text: file.Text, tokens: make([]token.Token, 0, len(file.Text)/3+1)}
	for s.pos < len(s.text) {
		s.next()
	}
	s.emit(token.EOF, s.pos)
	return s.tokens, s.errors
}

type scanner struct {
	file   *source.File
	text   string
	pos    int
	tokens []token.Token
	errors []diag.Diagnostic
}

func (s *scanner) next() {
	start := s.pos
	c := s.text[start]
	switch {
	case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
		s.pos++
	case c == '/' && s.at(1) == '/':
		s.pos = s.lineEnd()
		if strings.HasPrefix(s.text[start:], "///") {
			s.emit(token.DOC_COMMENT, start)
		}
	case c == '/' && s.at(1) == '*':
		if end := strings.Index(s.text[start+2:], "*/"); end >= 0 {
			s.pos = start + 2 + end + 2
		} else {
			s.pos = start + 2
			s.fail(source.Span{Start: start, End: s.pos}, diag.UnterminatedComment, "unterminated block comment", "add `*/` to close it")
			s.pos = len(s.text)
		}
	case c == '"':
		s.quoted(0, token.STRING)
	case c == 'c' && s.at(1) == '"':
		s.quoted(1, token.CSTRING)
	case c == 'b' && s.at(1) == '\'':
		s.quoted(1, token.BYTE_CHAR)
	case c == '\'':
		s.quoted(0, token.CHAR)
	case ascii.IsDigit(c):
		s.number()
	case c < utf8.RuneSelf:
		if c == '_' || ascii.IsLetter(c) {
			s.word()
		} else {
			s.operator()
		}
	default:
		if r, _ := utf8.DecodeRuneInString(s.text[start:]); unicode.IsLetter(r) {
			s.word()
		}
	}
	if s.pos == start {
		s.illegal()
	}
}

// at returns the byte offset bytes after the current one, or 0 past the end.
func (s *scanner) at(offset int) byte {
	if s.pos+offset < len(s.text) {
		return s.text[s.pos+offset]
	}
	return 0
}

func (s *scanner) lineEnd() int {
	if end := strings.IndexAny(s.text[s.pos:], "\n\r"); end >= 0 {
		return s.pos + end
	}
	return len(s.text)
}

func (s *scanner) emit(kind token.Kind, start int) {
	s.tokens = append(s.tokens, token.Token{Kind: kind, Span: source.Span{Start: start, End: s.pos}})
}

func (s *scanner) fail(span source.Span, code diag.Code, message, label string) {
	s.errors = append(s.errors, *diag.New(code, message).Label(s.file, span, label))
}

// word reads a name or keyword. A name holding letters, digits or marks outside
// ASCII is reported once and still read as a name, so parsing goes on.
func (s *scanner) word() {
	start, isASCII := s.pos, true
	for s.pos < len(s.text) {
		if c := s.text[s.pos]; c < utf8.RuneSelf {
			if c != '_' && !ascii.IsLetter(c) && !ascii.IsDigit(c) {
				break
			}
			s.pos++
			continue
		}
		r, width := utf8.DecodeRuneInString(s.text[s.pos:])
		if r != '_' && !unicode.IsLetter(r) && (s.pos == start || !unicode.IsDigit(r) && !unicode.IsMark(r)) {
			break
		}
		isASCII = isASCII && r < utf8.RuneSelf
		s.pos += width
	}
	if isASCII {
		s.emit(token.LookupIdent(s.text[start:s.pos]), start)
		return
	}
	s.fail(source.Span{Start: start, End: s.pos}, diag.UnexpectedCharacter, fmt.Sprintf("name `%s` uses characters that are not allowed in names", s.text[start:s.pos]),
		"use only `A-Z`, `a-z`, `0-9` and `_`")
	s.emit(token.IDENT, start)
}

// number reads a number literal: 0x, 0o and 0b numbers take letters and digits
// as digits (checked here against the base); a decimal number may have a
// fraction, an exponent and a type suffix such as i32 or f64. A single `_` may
// sit between digits. The suffix is the parser's to check.
func (s *scanner) number() {
	start := s.pos
	if s.text[start] == '0' && strings.IndexByte("xXoObB", s.at(1)) >= 0 && ascii.IsAlnum(s.at(2)) {
		s.pos += 2
		s.digits(ascii.IsAlnum[byte])
		s.emit(token.NUMBER, start)
		if l := numeric.Split(s.text[start:s.pos]); !l.DigitsInBase() {
			prefixed := s.text[start : start+2+len(l.Digits)] // without the suffix
			s.fail(source.Span{Start: start, End: s.pos}, diag.InvalidNumber, fmt.Sprintf("invalid %s literal %s", baseNames[l.Base], prefixed), "")
		}
		return
	}
	s.digits(ascii.IsDigit[byte])
	if s.at(0) == '.' && ascii.IsDigit(s.at(1)) {
		s.pos++
		s.digits(ascii.IsDigit[byte])
	}
	if e := s.at(0); e == 'e' || e == 'E' {
		sign := 0
		if next := s.at(1); next == '+' || next == '-' {
			sign = 1
		}
		if ascii.IsDigit(s.at(1 + sign)) {
			s.pos += 1 + sign
			s.digits(ascii.IsDigit[byte])
		}
	}
	if suffix := s.at(0); suffix == 'i' || suffix == 'u' || suffix == 'f' {
		s.pos++
		for ascii.IsDigit(s.at(0)) || s.at(0) == '_' {
			s.pos++
		}
	}
	s.emit(token.NUMBER, start)
}

var baseNames = map[int]string{16: "hexadecimal", 8: "octal", 2: "binary"}

// digits reads one or more digits; `_` is taken only when a digit follows it.
func (s *scanner) digits(isDigit func(byte) bool) {
	for {
		if isDigit(s.at(0)) {
			s.pos++
		} else if s.at(0) == '_' && isDigit(s.at(1)) {
			s.pos += 2
		} else {
			return
		}
	}
}

// quoted reads a string, character or byte literal whose quote follows prefix
// bytes, in one pass: it finds the closing quote, reports each bad escape at
// the escape, and checks that a character or byte literal holds exactly one.
// A literal may span lines; an escape needs a following character on the same
// line. When the closing quote is missing, the literal is reported and ends
// with its first line. The literal's token is kept even when it is invalid, so
// parsing goes on without a second error.
func (s *scanner) quoted(prefix int, kind token.Kind) {
	start := s.pos
	quote := s.text[start+prefix]
	var value [utf8.UTFMax]byte // the first bytes of the literal's value
	length, badEscape := 0, false
	for i := start + prefix + 1; ; {
		if i >= len(s.text) || s.text[i] == '\\' && (i+1 >= len(s.text) || s.text[i+1] == '\n') {
			s.unterminated(start, quote, kind)
			return
		}
		c := s.text[i]
		switch c {
		case quote:
			s.pos = i + 1
			if !badEscape {
				s.checkLength(start, kind, value[:min(length, len(value))], length)
			}
			s.emit(kind, start)
			return
		case '\\':
			b, width, err := literal.Escape(s.text, i, quote)
			if err != nil {
				s.fail(source.Span{Start: i, End: i + width}, diag.UnexpectedCharacter, err.Error(), "fix this escape sequence")
				badEscape = true
			} else if length < len(value) {
				value[length] = b
			}
			length++
			i += width
		default:
			if length < len(value) {
				value[length] = c
			}
			length++
			i++
		}
	}
}

// checkLength reports a character literal whose value is not exactly one
// valid UTF-8 character, or a byte literal whose value is not exactly one byte.
// value holds the first bytes of the literal's value, which is length bytes long.
func (s *scanner) checkLength(start int, kind token.Kind, value []byte, length int) {
	span := source.Span{Start: start, End: s.pos}
	switch {
	case kind == token.CHAR && (length > utf8.UTFMax || !utf8.Valid(value) || utf8.RuneCount(value) != 1):
		s.fail(span, diag.UnexpectedCharacter, "character literal must contain exactly one character",
			"use exactly one valid UTF-8 character between single quotes")
	case kind == token.BYTE_CHAR && length != 1:
		s.fail(span, diag.UnexpectedCharacter, "byte literal must contain exactly one byte",
			"use exactly one byte after the b'...' prefix")
	}
}

// unterminated reports a literal without its closing quote and emits it up to
// the end of its first line.
func (s *scanner) unterminated(start int, quote byte, kind token.Kind) {
	s.pos = s.lineEnd()
	span := source.Span{Start: start, End: s.pos}
	if quote == '"' {
		s.fail(span, diag.UnterminatedString, "unterminated string literal", "add a closing `\"`")
	} else {
		s.fail(span, diag.UnterminatedString, "unterminated character literal", "add a closing `'`")
	}
	s.emit(kind, start)
}

func (s *scanner) illegal() {
	start := s.pos
	_, width := utf8.DecodeRuneInString(s.text[start:])
	s.pos += width
	s.fail(source.Span{Start: start, End: s.pos}, diag.UnexpectedCharacter, fmt.Sprintf("illegal character %q", s.text[start:s.pos]), "remove or replace this character")
}

type operator struct {
	text string
	kind token.Kind
}

// operators lists every operator and punctuation token, longest first within
// each leading character, so the first match is the right one.
var operators = [...]operator{
	{"::", token.DCOLON},
	{":", token.COLON},
	{"==", token.EQ},
	{"=>", token.FATARROW},
	{"=", token.ASSIGN},
	{"!=", token.NEQ},
	{"!!", token.BB},
	{"!", token.BANG},
	{"<=", token.LE},
	{"<<", token.SHL},
	{"<", token.LT},
	{">=", token.GE},
	{">>", token.SHR},
	{">", token.GT},
	{"&&", token.ANDAND},
	{"&", token.AMP},
	{"||", token.OROR},
	{"|>", token.PIPE_ARROW},
	{"|", token.BAR},
	{"??", token.QQ},
	{"?", token.QUESTION},
	{"->", token.ARROW},
	{"--", token.MINUS_MINUS},
	{"-=", token.MINUS_ASSIGN},
	{"-", token.MINUS},
	{"++", token.PLUS_PLUS},
	{"+=", token.PLUS_ASSIGN},
	{"+", token.PLUS},
	{"*=", token.STAR_ASSIGN},
	{"*", token.ASTERISK},
	{"/=", token.SLASH_ASSIGN},
	{"/", token.SLASH},
	{"%=", token.PCT_ASSIGN},
	{"%", token.PERCENT},
	{"...", token.ELLIPSIS},
	{"..=", token.DOTDOT_EQ},
	{"..", token.DOTDOT},
	{".", token.DOT},
	{"@", token.AT},
	{"^", token.CARET},
	{"~", token.TILDE},
	{",", token.COMMA},
	{"#", token.HASH},
	{";", token.SEMICOLON},
	{"(", token.LPAREN},
	{")", token.RPAREN},
	{"{", token.LBRACE},
	{"}", token.RBRACE},
	{"[", token.LBRACK},
	{"]", token.RBRACK},
}

// operatorsByFirst indexes operators by their first byte.
var operatorsByFirst [utf8.RuneSelf][]operator

func init() {
	for _, op := range operators {
		operatorsByFirst[op.text[0]] = append(operatorsByFirst[op.text[0]], op)
	}
}

// operator reads an operator, or reads nothing when none starts here.
func (s *scanner) operator() {
	rest := s.text[s.pos:]
	for _, op := range operatorsByFirst[rest[0]] {
		if strings.HasPrefix(rest, op.text) {
			start := s.pos
			s.pos += len(op.text)
			s.emit(op.kind, start)
			return
		}
	}
}
