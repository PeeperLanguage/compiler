package colors

import (
	"fmt"
	"io"
)

// Print methods (default to stdout)
func (c COLOR) Printf(format string, args ...any) {
	fmt.Print(defaultLogger.Render(c, fmt.Sprintf(format, args...)))
}

func (c COLOR) Println(args ...any) {
	fmt.Print(defaultLogger.Render(c, fmt.Sprintln(args...)))
}

func (c COLOR) Print(args ...any) {
	fmt.Print(defaultLogger.Render(c, fmt.Sprint(args...)))
}

// Fprint methods (write to specific writer)
func (c COLOR) Fprintf(w io.Writer, format string, args ...any) {
	fmt.Fprint(w, defaultLogger.Render(c, fmt.Sprintf(format, args...)))
}

func (c COLOR) Fprintln(w io.Writer, args ...any) {
	fmt.Fprint(w, defaultLogger.Render(c, fmt.Sprintln(args...)))
}

func (c COLOR) Fprint(w io.Writer, args ...any) {
	fmt.Fprint(w, defaultLogger.Render(c, fmt.Sprint(args...)))
}

func (c COLOR) Sprintf(format string, args ...any) string {
	return defaultLogger.Render(c, fmt.Sprintf(format, args...))
}

func (c COLOR) Sprintln(args ...any) string {
	return defaultLogger.Render(c, fmt.Sprintln(args...))
}

func (c COLOR) Sprint(args ...any) string {
	return defaultLogger.Render(c, fmt.Sprint(args...))
}
