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
