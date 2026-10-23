package errors

import (
	"regexp"
	"strings"
)

// codeShape is what a code looks like at the head of an AppError's text: an
// UPPER_SNAKE token of at most 25 characters. It is a shape check, not a membership
// check, so a code a newer release adds still splits; the length cap keeps a long
// uncoded UPPER_SNAKE word (an env-var name) from passing as a code.
var codeShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,24}$`)

// SplitCode splits a coded error's text, AppError.Error()'s "<CODE>: <rest>" form, into
// the code and the rest. ok is false when text has no ": " separator or its prefix is
// not code-shaped, so an uncoded message is never mistaken for a coded one. It is the
// one parser for that form, shared by a display edge that drops the code and a caller
// that recovers it.
func SplitCode(text string) (code, rest string, ok bool) {
	prefix, rest, found := strings.Cut(text, ": ")
	if !found || !codeShape.MatchString(prefix) {
		return "", "", false
	}
	return prefix, rest, true
}
