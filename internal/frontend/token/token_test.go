package token

import "testing"

func TestEveryKindHasAName(t *testing.T) {
	for kind := ILLEGAL; kind <= UNSAFE; kind++ {
		if kind.String() == "" || kind.String() == "unknown" {
			t.Errorf("kind %d has no name", kind)
		}
	}
	if got := UNSAFE + 1; got.String() != "unknown" {
		t.Errorf("kind past the last one = %q, want unknown", got.String())
	}
}

func TestLookupIdent(t *testing.T) {
	if LookupIdent("fn") != FN || LookupIdent("value") != IDENT {
		t.Fatal("LookupIdent must map keywords to their kind and other names to IDENT")
	}
}

func TestSpelling(t *testing.T) {
	for kind, want := range map[Kind]string{SEMICOLON: ";", RPAREN: ")", FATARROW: "=>", SHR: ">>", FN: "fn", ORELSE: "orelse"} {
		if got, ok := Spelling(kind); !ok || got != want {
			t.Errorf("Spelling(%s) = %q, %v; want %q", kind, got, ok, want)
		}
	}
	for _, kind := range []Kind{IDENT, NUMBER, STRING, EOF, DOC_COMMENT} {
		if got, ok := Spelling(kind); ok {
			t.Errorf("Spelling(%s) = %q, want none", kind, got)
		}
	}
}
