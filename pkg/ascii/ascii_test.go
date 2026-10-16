package ascii

import "testing"

func TestClassify(t *testing.T) {
	for c := rune(0); c < 256; c++ {
		letter := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
		digit := '0' <= c && c <= '9'
		if IsLetter(c) != letter || IsDigit(c) != digit || IsAlnum(c) != (letter || digit) {
			t.Errorf("%q classified wrongly", c)
		}
		if c < 128 && (IsLetter(byte(c)) != letter || IsDigit(byte(c)) != digit) {
			t.Errorf("byte %q classified wrongly", c)
		}
	}
	if IsLetter('é') || IsDigit('٣') {
		t.Error("non-ASCII letters and digits must not count")
	}
}
