package colors

import (
	"testing"
)

func TestLogger_Render(t *testing.T) {
	tests := []struct {
		name     string
		format   LogFormat
		color    COLOR
		text     string
		expected string
	}{
		{
			name:     "ANSI with color",
			format:   LogFormatANSI,
			color:    RED,
			text:     "Hello",
			expected: string(RED) + "Hello" + string(RESET),
		},
		{
			name:     "ANSI without color",
			format:   LogFormatANSI,
			color:    "",
			text:     "Hello",
			expected: "Hello",
		},
		{
			name:     "Normal with color",
			format:   LogFormatNormal,
			color:    RED,
			text:     "Hello",
			expected: "Hello",
		},
		{
			name:     "HTML with color",
			format:   LogFormatHTML,
			color:    RED,
			text:     "Hello",
			expected: `<span style="color: #ef4444">Hello</span>`,
		},
		{
			name:     "HTML without color",
			format:   LogFormatHTML,
			color:    "",
			text:     "Hello",
			expected: "Hello",
		},
		{
			name:     "HTML with special characters",
			format:   LogFormatHTML,
			color:    BLUE,
			text:     "<b>Hello</b> & World",
			expected: `<span style="color: #3b82f6">&lt;b&gt;Hello&lt;/b&gt; &amp; World</span>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Logger{format: tt.format}
			got := l.Render(tt.color, tt.text)
			if got != tt.expected {
				t.Errorf("Render() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFormatHTMLText(t *testing.T) {
	tests := []struct {
		name           string
		text           string
		preserveLayout bool
		expected       string
	}{
		{
			name:           "No preservation",
			text:           "Line 1\nLine 2  Space",
			preserveLayout: false,
			expected:       "Line 1\nLine 2  Space",
		},
		{
			name:           "Preserve layout",
			text:           "Line 1\nLine 2  Space",
			preserveLayout: true,
			expected:       "Line 1<br>Line 2&nbsp;&nbsp;Space",
		},
		{
			name:           "Preserve layout with HTML tags",
			text:           "<b>Bold</b>\nNew Line",
			preserveLayout: true,
			expected:       "&lt;b&gt;Bold&lt;/b&gt;<br>New Line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatHTMLText(tt.text, tt.preserveLayout)
			if got != tt.expected {
				t.Errorf("formatHTMLText() = %q, want %q", got, tt.expected)
			}
		})
	}
}
