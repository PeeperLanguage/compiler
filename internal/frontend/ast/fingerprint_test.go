package ast

import "testing"

func TestHashTextUsesStableContentDigest(t *testing.T) {
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := HashText("abc"); got != want {
		t.Fatalf("HashText(abc) = %q, want %q", got, want)
	}
	if HashText("abc") == HashText("abcd") {
		t.Fatal("different source text shares content digest")
	}
}

func TestFingerprintPartsPreservesBoundariesAndOrderIndependence(t *testing.T) {
	if FingerprintParts([]string{"a\nb"}) == FingerprintParts([]string{"a", "b"}) {
		t.Fatal("embedded newline collides with part boundary")
	}
	if FingerprintParts([]string{"a", "b"}) != FingerprintParts([]string{"b", "a"}) {
		t.Fatal("discovery order changed fingerprint")
	}
	if FingerprintParts([]string{"a", "a"}) == FingerprintParts([]string{"a"}) {
		t.Fatal("duplicate parts changed neither fingerprint nor multiplicity")
	}
	if FingerprintParts(nil) != HashText("") {
		t.Fatal("empty surface changed fingerprint contract")
	}
}
