package semver

import "testing"

func TestParseAndCompare(t *testing.T) {
	version, err := Parse("v1.2.3")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if version.Major != 1 || version.Minor != 2 || version.Patch != 3 {
		t.Fatalf("unexpected version parsed: %#v", version)
	}
	for _, invalid := range []string{"latest", "bad", "999999999999999999999999999999.0.0"} {
		if _, err := Parse(invalid); err == nil {
			t.Fatalf("Parse(%q) succeeded", invalid)
		}
	}
	if got := version.Compare(&Version{Major: 1, Minor: 2, Patch: 2}); got <= 0 {
		t.Fatalf("expected v1.2.3 > v1.2.2")
	}
}

func TestConstraintsAndBestMatch(t *testing.T) {
	tests := []struct {
		version    string
		constraint string
		want       bool
	}{
		{version: "1.2.3", constraint: ">=1.2.0", want: true},
		{version: "1.2.3", constraint: "~1.2.0", want: true},
		{version: "1.2.3", constraint: "^1.0.0", want: true},
		{version: "1.2.3", constraint: "<1.0.0", want: false},
		{version: "0.9.0", constraint: "^0.2.3", want: false},
		{version: "0.2.9", constraint: "^0.2.3", want: true},
		{version: "0.0.4", constraint: "^0.0.3", want: false},
		{version: "0.2.3", constraint: "=0.2.3", want: true},
		{version: "9.9.9", constraint: "*", want: true},
	}
	for _, test := range tests {
		got, err := Match(test.version, test.constraint)
		if err != nil || got != test.want {
			t.Fatalf("Match(%q, %q) = %v, %v; want %v", test.version, test.constraint, got, err, test.want)
		}
	}

	versions := []string{"nightly", "1.0.0", "1.2.0", "1.2.5", "1.3.0", "2.0.0"}
	best, err := BestMatch(versions, "~1.2.0")
	if err != nil || best != "1.2.5" {
		t.Fatalf("best ~1.2.0 mismatch: %q err=%v", best, err)
	}
	best, err = BestMatchAll(versions, []string{">=1.2.0", "<2.0.0"})
	if err != nil || best != "1.3.0" {
		t.Fatalf("best multi-constraint mismatch: %q err=%v", best, err)
	}
	best, err = BestMatch([]string{"nightly", "1.2.5", "1.3.0"}, "latest")
	if err != nil || best != "1.3.0" {
		t.Fatalf("latest mismatch: %q err=%v", best, err)
	}
	best, err = BestMatch([]string{" 1.2.5 ", "1.2.0"}, "latest")
	if err != nil || best != "1.2.5" {
		t.Fatalf("normalized tag mismatch: %q err=%v", best, err)
	}
}
