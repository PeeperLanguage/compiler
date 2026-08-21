package lexer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
	"compiler/pkg/numeric"
)

type regexHandler func(lex *Lexer, match string)

type regexPattern struct {
	regex   *regexp.Regexp
	handler regexHandler
}

// Order defines token precedence; specific patterns must precede their prefixes.
var regexPatterns = [...]regexPattern{
	{regexp.MustCompile(`\s+`), skipHandler},
	{regexp.MustCompile(`///[^\n\r]*`), docHandler},
	{regexp.MustCompile(`//[^\n\r]*`), skipHandler},
	{regexp.MustCompile(`(?s)/\*.*?\*/`), skipHandler},
	{regexp.MustCompile(`c"(?:\\.|[^"\\])*"`), cstringHandler},
	{regexp.MustCompile(`"(?:\\.|[^"\\])*"`), stringHandler},
	{regexp.MustCompile(`b'(?:\\.|[^'\\])*'`), byteCharHandler},
	{regexp.MustCompile(`'(?:\\.|[^'\\])*'`), charHandler},
	{regexp.MustCompile(numeric.NumberTokenPattern), numberHandler},
	{regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`), identifierHandler},
	{regexp.MustCompile(`::`), defaultHandler(token.DCOLON)},
	{regexp.MustCompile(`==`), defaultHandler(token.EQ)},
	{regexp.MustCompile(`!=`), defaultHandler(token.NEQ)},
	{regexp.MustCompile(`<=`), defaultHandler(token.LE)},
	{regexp.MustCompile(`>=`), defaultHandler(token.GE)},
	{regexp.MustCompile(`<<`), defaultHandler(token.SHL)},
	{regexp.MustCompile(`>>`), defaultHandler(token.SHR)},
	{regexp.MustCompile(`&&`), defaultHandler(token.ANDAND)},
	{regexp.MustCompile(`\|\|`), defaultHandler(token.OROR)},
	{regexp.MustCompile(`\?\?`), defaultHandler(token.QQ)},
	{regexp.MustCompile(`!!`), defaultHandler(token.BB)},
	{regexp.MustCompile(`=>`), defaultHandler(token.FATARROW)},
	{regexp.MustCompile(`->`), defaultHandler(token.ARROW)},
	{regexp.MustCompile(`\+\+`), defaultHandler(token.PLUS_PLUS)},
	{regexp.MustCompile(`--`), defaultHandler(token.MINUS_MINUS)},
	{regexp.MustCompile(`\+=`), defaultHandler(token.PLUS_ASSIGN)},
	{regexp.MustCompile(`-=`), defaultHandler(token.MINUS_ASSIGN)},
	{regexp.MustCompile(`\*=`), defaultHandler(token.STAR_ASSIGN)},
	{regexp.MustCompile(`/=`), defaultHandler(token.SLASH_ASSIGN)},
	{regexp.MustCompile(`%=`), defaultHandler(token.PCT_ASSIGN)},
	{regexp.MustCompile(`=`), defaultHandler(token.ASSIGN)},
	{regexp.MustCompile(`\+`), defaultHandler(token.PLUS)},
	{regexp.MustCompile(`-`), defaultHandler(token.MINUS)},
	{regexp.MustCompile(`\*`), defaultHandler(token.ASTERISK)},
	{regexp.MustCompile(`/`), defaultHandler(token.SLASH)},
	{regexp.MustCompile(`%`), defaultHandler(token.PERCENT)},
	{regexp.MustCompile(`!`), defaultHandler(token.BANG)},
	{regexp.MustCompile(`\?`), defaultHandler(token.QUESTION)},
	{regexp.MustCompile(`@`), defaultHandler(token.AT)},
	{regexp.MustCompile(`&`), defaultHandler(token.AMP)},
	{regexp.MustCompile(`\^`), defaultHandler(token.CARET)},
	{regexp.MustCompile(`^\|>`), defaultHandler(token.PIPE_ARROW)},
	{regexp.MustCompile(`\|`), defaultHandler(token.BAR)},
	{regexp.MustCompile(`~`), defaultHandler(token.TILDE)},
	{regexp.MustCompile(`<`), defaultHandler(token.LT)},
	{regexp.MustCompile(`>`), defaultHandler(token.GT)},
	{regexp.MustCompile(`:`), defaultHandler(token.COLON)},
	{regexp.MustCompile(`,`), defaultHandler(token.COMMA)},
	{regexp.MustCompile(`^\.\.\.`), defaultHandler(token.ELLIPSIS)},
	{regexp.MustCompile(`^\.\.=`), defaultHandler(token.DOTDOT_EQ)},
	{regexp.MustCompile(`^\.\.`), defaultHandler(token.DOTDOT)},
	{regexp.MustCompile(`\.`), defaultHandler(token.DOT)},
	{regexp.MustCompile(`#`), defaultHandler(token.HASH)},
	{regexp.MustCompile(`;`), defaultHandler(token.SEMICOLON)},
	{regexp.MustCompile(`\(`), defaultHandler(token.LPAREN)},
	{regexp.MustCompile(`\)`), defaultHandler(token.RPAREN)},
	{regexp.MustCompile(`\{`), defaultHandler(token.LBRACE)},
	{regexp.MustCompile(`\}`), defaultHandler(token.RBRACE)},
	{regexp.MustCompile(`\[`), defaultHandler(token.LBRACK)},
	{regexp.MustCompile(`\]`), defaultHandler(token.RBRACK)},
}

type Lexer struct {
	file  string
	input string
	pos   source.Position
	diag  *diagnostics.DiagnosticBag
	toks  []token.Token
}

func New(file, input string, diag *diagnostics.DiagnosticBag) *Lexer {
	if diag == nil {
		diag = diagnostics.NewDiagnosticBag()
	}

	return &Lexer{
		file:  file,
		input: input,
		pos:   source.NewPosition(),
		diag:  diag,
	}
}

func defaultHandler(kind token.Kind) regexHandler {
	return func(l *Lexer, match string) {
		start := l.pos

		l.advanceBy(match)
		l.push(token.Token{
			Kind:    kind,
			Literal: match,
			Start:   start,
			End:     l.pos,
		})
	}
}

func skipHandler(l *Lexer, match string) {
	l.advanceBy(match)
}

func identifierHandler(l *Lexer, match string) {
	start := l.pos

	l.advanceBy(match)
	l.push(token.Token{
		Kind:    token.LookupIdent(match),
		Literal: match,
		Start:   start,
		End:     l.pos,
	})
}

func docHandler(l *Lexer, match string) {
	start := l.pos
	text := strings.TrimSpace(strings.TrimPrefix(match, "///"))

	l.advanceBy(match)
	l.push(token.Token{
		Kind:    token.DOC_COMMENT,
		Literal: text,
		Start:   start,
		End:     l.pos,
	})
}

func numberHandler(l *Lexer, match string) {
	start := l.pos

	l.advanceBy(match)
	l.push(token.Token{
		Kind:    token.NUMBER,
		Literal: match,
		Start:   start,
		End:     l.pos,
	})
}

func stringHandler(l *Lexer, match string) {
	start := l.pos

	l.advanceBy(match)

	inner := match[1 : len(match)-1]
	value, err := unescapeQuoted(inner, '"')
	if err != nil {
		l.reportEscapeError(start, err)
		return
	}

	l.push(token.Token{
		Kind:    token.STRING,
		Literal: value,
		Start:   start,
		End:     l.pos,
	})
}

func cstringHandler(l *Lexer, match string) {
	start := l.pos

	l.advanceBy(match)

	inner := match[2 : len(match)-1]
	value, err := unescapeQuoted(inner, '"')
	if err != nil {
		l.reportEscapeError(start, err)
		return
	}

	l.push(token.Token{
		Kind:    token.CSTRING,
		Literal: value,
		Start:   start,
		End:     l.pos,
	})
}

func charHandler(l *Lexer, match string) {
	start := l.pos

	l.advanceBy(match)

	inner := match[1 : len(match)-1]
	value, err := unescapeQuoted(inner, '\'')
	if err != nil {
		l.reportEscapeError(start, err)
		return
	}

	if !utf8.ValidString(value) || utf8.RuneCountInString(value) != 1 {
		loc := source.NewLocation(l.file, start, l.pos)
		l.diag.Add(
			diagnostics.NewError("character literal must contain exactly one character").
				WithCode(diagnostics.ErrUnexpectedCharacter).
				WithPrimaryLabel(loc, "use exactly one valid UTF-8 character between single quotes"),
		)
		return
	}

	l.push(token.Token{
		Kind:    token.CHAR,
		Literal: value,
		Start:   start,
		End:     l.pos,
	})
}

func byteCharHandler(l *Lexer, match string) {
	start := l.pos

	l.advanceBy(match)

	inner := match[2 : len(match)-1]
	value, err := unescapeQuoted(inner, '\'')
	if err != nil {
		l.reportEscapeError(start, err)
		return
	}

	if len(value) != 1 {
		loc := source.NewLocation(l.file, start, l.pos)
		l.diag.Add(
			diagnostics.NewError("byte literal must contain exactly one byte").
				WithCode(diagnostics.ErrUnexpectedCharacter).
				WithPrimaryLabel(loc, "use exactly one byte after the b'...' prefix"),
		)
		return
	}

	l.push(token.Token{
		Kind:    token.BYTE_CHAR,
		Literal: value,
		Start:   start,
		End:     l.pos,
	})
}

func (l *Lexer) reportEscapeError(start source.Position, err error) {
	loc := source.NewLocation(l.file, start, l.pos)

	l.diag.Add(
		diagnostics.NewError(err.Error()).
			WithCode(diagnostics.ErrUnexpectedCharacter).
			WithPrimaryLabel(loc, "fix this escape sequence"),
	)
}

func (l *Lexer) Tokenize() []token.Token {
	for !l.atEOF() {
		matched := false
		rem := l.remainder()

		for _, p := range regexPatterns {
			loc := p.regex.FindStringIndex(rem)
			if loc != nil && loc[0] == 0 {
				p.handler(l, rem[:loc[1]])
				matched = true
				break
			}
		}

		if matched {
			continue
		}

		start := l.pos

		_, width := utf8.DecodeRuneInString(rem)
		if width < 1 {
			width = 1
		}

		bad := rem[:width]
		l.advanceBy(bad)

		loc := source.NewLocation(l.file, start, l.pos)
		l.diag.Add(
			diagnostics.NewError(fmt.Sprintf("illegal character %q", bad)).
				WithCode(diagnostics.ErrUnexpectedCharacter).
				WithPrimaryLabel(loc, "remove or replace this character"),
		)
	}

	l.push(token.Token{
		Kind:  token.EOF,
		Start: l.pos,
		End:   l.pos,
	})

	return append([]token.Token(nil), l.toks...)
}

func (l *Lexer) push(t token.Token) {
	l.toks = append(l.toks, t)
}

func (l *Lexer) advanceBy(text string) {
	l.pos.Advance(text)
}

func (l *Lexer) remainder() string {
	if l.pos.Index >= len(l.input) {
		return ""
	}

	return l.input[l.pos.Index:]
}

func (l *Lexer) atEOF() bool {
	return l.pos.Index >= len(l.input)
}

func unescapeQuoted(s string, quote byte) (string, error) {
	var out []byte

	for i := 0; i < len(s); {
		if s[i] != '\\' {
			out = append(out, s[i])
			i++
			continue
		}

		if i+1 >= len(s) {
			return "", fmt.Errorf("unterminated escape sequence")
		}

		switch s[i+1] {
		case 'n':
			out = append(out, '\n')
			i += 2

		case 'r':
			out = append(out, '\r')
			i += 2

		case 't':
			out = append(out, '\t')
			i += 2

		case '0':
			out = append(out, 0)
			i += 2

		case '\\':
			out = append(out, '\\')
			i += 2

		case '"':
			if quote != '"' {
				return "", fmt.Errorf(`invalid escape sequence \"`)
			}

			out = append(out, '"')
			i += 2

		case '\'':
			if quote != '\'' {
				return "", fmt.Errorf(`invalid escape sequence \'`)
			}

			out = append(out, '\'')
			i += 2

		case 'x':
			if i+3 >= len(s) {
				return "", fmt.Errorf(`hex escape \x requires exactly two hexadecimal digits`)
			}

			hex := s[i+2 : i+4]
			value, err := strconv.ParseUint(hex, 16, 8)
			if err != nil {
				return "", fmt.Errorf("invalid hex escape %q", `\x`+hex)
			}

			out = append(out, byte(value))
			i += 4

		default:
			return "", fmt.Errorf(
				"unknown escape sequence %q",
				s[i:i+2],
			)
		}
	}

	return string(out), nil
}
