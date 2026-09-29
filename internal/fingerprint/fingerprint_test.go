package fingerprint

import "testing"

func TestTextUsesStableContentDigest(t *testing.T) {
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Text("abc"); got != want {
		t.Fatalf("Text(abc) = %q, want %q", got, want)
	}
	if Text("abc") == Text("abcd") {
		t.Fatal("different source text shares content digest")
	}
}

func TestPartsPreservesBoundariesAndOrderIndependence(t *testing.T) {
	if Parts([]string{"a\nb"}) == Parts([]string{"a", "b"}) {
		t.Fatal("embedded newline collides with part boundary")
	}
	if Parts([]string{"a", "b"}) != Parts([]string{"b", "a"}) {
		t.Fatal("discovery order changed fingerprint")
	}
	if Parts([]string{"a", "a"}) == Parts([]string{"a"}) {
		t.Fatal("duplicate parts changed neither fingerprint nor multiplicity")
	}
	if Parts(nil) != Text("") {
		t.Fatal("empty surface changed fingerprint contract")
	}
}
