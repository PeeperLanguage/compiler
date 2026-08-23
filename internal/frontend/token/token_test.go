package token

import (
	"strings"
	"testing"

	"compiler/internal/target"
)

func TestLookupIdentAndKeywordHelpers(t *testing.T) {
	if got := LookupIdent("if"); got != IF {
		t.Fatalf("LookupIdent(if) = %v", got)
	}
	if got := LookupIdent("custom"); got != IDENT {
		t.Fatalf("LookupIdent(custom) = %v", got)
	}
	if !IsKeyword("while") || IsKeyword("custom") {
		t.Fatalf("IsKeyword results unexpected")
	}
	if doc, ok := KeywordDoc("if"); !ok || !strings.Contains(doc, "conditional") {
		t.Fatalf("expected keyword doc for if, got (%q, %v)", doc, ok)
	}
	if doc, ok := KeywordDocByKind(RETURN); !ok || !strings.Contains(doc, "Return") {
		t.Fatalf("expected keyword doc for RETURN, got (%q, %v)", doc, ok)
	}
	if got := LookupIdent("free"); got != FREE {
		t.Fatalf("LookupIdent(free) = %v", got)
	}
	if doc, ok := KeywordDocByKind(FREE); !ok || !strings.Contains(doc, "owned") {
		t.Fatalf("expected keyword doc for FREE, got (%q, %v)", doc, ok)
	}
	if got := LookupIdent("print"); got != PRINT {
		t.Fatalf("LookupIdent(print) = %v", got)
	}
	if got := LookupIdent("from"); got != FROM {
		t.Fatalf("LookupIdent(from) = %v", got)
	}
	if _, ok := KeywordDoc("custom"); ok {
		t.Fatalf("did not expect keyword doc for non-keyword ident")
	}
}

func TestValidSymbolName(t *testing.T) {
	for _, name := range []string{"name", "Name2", "snake_case"} {
		if !IsValidSymbolName(name) {
			t.Fatalf("valid symbol name %q rejected", name)
		}
	}
	for _, name := range []string{"", "_", "2name", "two words", "éclair", "fn", "let"} {
		if IsValidSymbolName(name) {
			t.Fatalf("invalid symbol name %q accepted", name)
		}
	}
}

func TestBuiltinTypeAndStringer(t *testing.T) {
	if !IsBuiltinType("i32") || IsBuiltinType("Point") {
		t.Fatalf("IsBuiltinType results unexpected")
	}
	if !IsBuiltinType("byte") {
		t.Fatalf("expected byte to be recognized as builtin type")
	}
	if IsBuiltinType("void") {
		t.Fatalf("did not expect void to be recognized as builtin type")
	}
	if !IsBuiltinType("i128") || !IsBuiltinType("u1024") {
		t.Fatalf("expected arbitrary-width builtin integers to be recognized")
	}
	if !IsBuiltinType("i24") || !IsBuiltinType("i3") || IsBuiltinType("u0") || IsBuiltinType("i8388609") {
		t.Fatalf("expected invalid builtin integer widths to be rejected")
	}
	s := (Token{Kind: IDENT, Literal: "x"}).String()
	if !strings.Contains(s, "identifier") || !strings.Contains(s, "\"x\"") {
		t.Fatalf("unexpected token string: %q", s)
	}
}

func TestParseIntegerBuiltin(t *testing.T) {
	hostTarget := target.Host()
	tests := []struct {
		name   string
		signed bool
		bits   int
		ok     bool
	}{
		{name: "i128", signed: true, bits: 128, ok: true},
		{name: "u1024", signed: false, bits: 1024, ok: true},
		{name: "isize", signed: true, bits: hostTarget.PointerBits, ok: true},
		{name: "usize", signed: false, bits: hostTarget.PointerBits, ok: true},
		{name: "byte", ok: false},
		{name: "i24", signed: true, bits: 24, ok: true},
		{name: "u4", signed: false, bits: 4, ok: true},
		{name: "i8388608", signed: true, bits: 8388608, ok: true},
		{name: "i8388609", ok: false},
		{name: "foo", ok: false},
	}
	for _, tt := range tests {
		signed, bits, ok := ParseIntegerBuiltin(tt.name, hostTarget)
		if signed != tt.signed || bits != tt.bits || ok != tt.ok {
			t.Fatalf("ParseIntegerBuiltin(%q) = (%v, %d, %v)", tt.name, signed, bits, ok)
		}
	}
}

func TestParseIntegerBuiltinUsesExplicitTarget(t *testing.T) {
	target32, err := target.New("linux", "386")
	if err != nil {
		t.Fatal(err)
	}
	target64, err := target.New("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}

	signed, bits, ok := ParseIntegerBuiltin("isize", target32)
	if !ok || !signed || bits != target.Bits32 {
		t.Fatalf("ParseIntegerBuiltin(isize) = (%v, %d, %v)", signed, bits, ok)
	}
	signed, bits, ok = ParseIntegerBuiltin("usize", target64)
	if !ok || signed || bits != target.Bits64 {
		t.Fatalf("ParseIntegerBuiltin(usize) = (%v, %d, %v)", signed, bits, ok)
	}
	signed, bits, ok = ParseIntegerBuiltin("usize", target32)
	if !ok || signed || bits != target.Bits32 {
		t.Fatalf("ParseIntegerBuiltin(usize) = (%v, %d, %v)", signed, bits, ok)
	}
}
