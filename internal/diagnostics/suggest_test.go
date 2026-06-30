package diagnostics

import "testing"

func TestNearestNameSuggestsCloseTypo(t *testing.T) {
	match, ok := NearestName("pritn", []string{"print"})
	if !ok {
		t.Fatal("expected suggestion for close typo")
	}
	if match != "print" {
		t.Fatalf("match = %q, want %q", match, "print")
	}
}

func TestNearestNameSuggestsOneEditWithoutSharedPrefix(t *testing.T) {
	match, ok := NearestName("rint", []string{"print"})
	if !ok {
		t.Fatal("expected suggestion for one-edit missing prefix typo")
	}
	if match != "print" {
		t.Fatalf("match = %q, want %q", match, "print")
	}
}

func TestNearestNameSuggestsLeadingDropOnLongerWord(t *testing.T) {
	match, ok := NearestName("ength", []string{"length"})
	if !ok {
		t.Fatal("expected suggestion for leading-drop typo")
	}
	if match != "length" {
		t.Fatalf("match = %q, want %q", match, "length")
	}
}

func TestNearestNameRejectsWeakMatch(t *testing.T) {
	if match, ok := NearestName("foo", []string{"bar"}); ok {
		t.Fatalf("unexpected weak suggestion %q", match)
	}
}

func TestNearestNameRejectsAmbiguousMatch(t *testing.T) {
	if match, ok := NearestName("cotn", []string{"cost", "count"}); ok {
		t.Fatalf("unexpected ambiguous suggestion %q", match)
	}
}

func TestNearestNameWithPriorityPrefersHigherPriorityTie(t *testing.T) {
	match, ok := NearestNameWithPriority("foa", []NameCandidate{
		{Name: "for", Priority: 1},
		{Name: "foo", Priority: 0},
	})
	if !ok {
		t.Fatal("expected prioritized tie to keep suggestion")
	}
	if match != "foo" {
		t.Fatalf("match = %q, want %q", match, "foo")
	}
}
