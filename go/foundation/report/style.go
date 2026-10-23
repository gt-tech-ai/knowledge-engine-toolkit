package report

import (
	"fmt"
	"io"
)

// ansiReset is the SGR sequence that clears every attribute.
const ansiReset = "0"

// style is one ANSI text style: a select-graphic-rendition code and the code that ends
// it inside a line. The sequences match what fatih/color writes, so a console that
// moved here from a fatih/color renderer prints the same bytes.
type style struct {
	// code is the SGR parameter that starts the style ("32" green, "1" bold).
	code string

	// reset is the SGR parameter that ends the style after inline text ("0", or "22"
	// for bold so the surrounding attributes survive).
	reset string

	// on is false when color is off: the style then writes text unchanged.
	on bool
}

// newStyle returns the style for code; reset ends it inline.
func newStyle(on bool, code, reset string) style {
	return style{on: on, code: code, reset: reset}
}

// Sprint styles fmt.Sprint(a...) for inline use.
func (s style) Sprint(a ...any) string { return s.wrap(fmt.Sprint(a...)) }

// Sprintf styles fmt.Sprintf(format, a...) for inline use.
func (s style) Sprintf(format string, a ...any) string {
	return s.wrap(fmt.Sprintf(format, a...))
}

// wrap brackets text with the style's start and inline-end sequences.
func (s style) wrap(text string) string {
	if !s.on {
		return text
	}
	return "\x1b[" + s.code + "m" + text + "\x1b[" + s.reset + "m"
}

// Fprintf writes fmt.Sprintf(format, a...) to w as a whole styled block, ended by a
// full reset (a block may span lines).
func (s style) Fprintf(w io.Writer, format string, a ...any) {
	text := fmt.Sprintf(format, a...)
	if s.on {
		text = "\x1b[" + s.code + "m" + text + "\x1b[" + ansiReset + "m"
	}
	_, _ = io.WriteString(w, text)
}

// palette is the console's set of styles.
type palette struct {
	// bold emphasizes names and headers.
	bold style

	// success marks passes.
	success style

	// failure marks failures and errors.
	failure style

	// warn marks warnings.
	warn style

	// info marks phase arrows, info lines and hints.
	info style

	// muted de-emphasizes metrics and debug lines.
	muted style
}

// newPalette returns the console palette, styled only when color is on.
func newPalette(color bool) palette {
	return palette{
		bold:    newStyle(color, "1", "22"),
		success: newStyle(color, "32", ansiReset),
		failure: newStyle(color, "31", ansiReset),
		warn:    newStyle(color, "33", ansiReset),
		info:    newStyle(color, "36", ansiReset),
		muted:   newStyle(color, "90", ansiReset),
	}
}
