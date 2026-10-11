// Package dtovalidate is the leaf rule library the generated DTO Validate() methods
// compose Each rule is a small, hand-written check that REUSES the standard library
// (google/uuid, net/url, path/filepath, net/mail) rather than a hand-rolled regex, and
// returns a core/errors CodeInvalidInput AppError on violation so the failure classifies
// to 400 / InvalidArgument at the boundary. It depends only on core (foundation-tier), so
// the generated per-DTO methods — which live in the app domain packages — can import it
// without a cycle.
package dtovalidate

import (
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// NoLowerBound / NoUpperBound are the IntRange sentinels for an absent bound — the
// generator emits them when a proto int rule omits gt/gte (no lower) or lt/lte (no
// upper), so a legitimate bound of 0 (e.g. {lte: 0}) is enforced rather than mistaken for
// "unbounded".
const (
	NoLowerBound int64 = math.MinInt64 // IntRange sentinel for an absent lower bound
	NoUpperBound int64 = math.MaxInt64 // IntRange sentinel for an absent upper bound
)

// UUID checks that v is a valid RFC-4122 UUID (reuses google/uuid).
func UUID(field, v string) error {
	if _, err := uuid.Parse(v); err != nil {
		return apperr.InvalidInput(field + ": must be a valid UUID")
	}
	return nil
}

// MinMaxLen checks that v's length in Unicode code points is within [minLen, maxLen]. A
// maxLen <= 0 means "no upper bound" (a proto rule with only min_len), so a min-only rule
// is still enforceable.
func MinMaxLen(field, v string, minLen, maxLen int) error {
	n := utf8.RuneCountInString(v)
	if n < minLen || (maxLen > 0 && n > maxLen) {
		return apperr.InvalidInput(
			fmt.Sprintf("%s: length %d not in [%d, %d]", field, n, minLen, maxLen),
		)
	}
	return nil
}

// MaxBytes checks that v's UTF-8 byte length does not exceed maxBytes.
func MaxBytes(field, v string, maxBytes int) error {
	if len(v) > maxBytes {
		return apperr.InvalidInput(
			fmt.Sprintf("%s: %d bytes exceeds max %d", field, len(v), maxBytes),
		)
	}
	return nil
}

// Filename checks that v is a safe object-key filename: non-empty, within maxBytes (a
// maxBytes <= 0 means no byte cap), a local path (no traversal / absolute, via
// path/filepath.IsLocal), free of any ".." substring, and free of control characters —
// mirroring the common.v1.filename proto CEL rule.
func Filename(field, v string, maxBytes int) error {
	if v == "" {
		return apperr.InvalidInput(field + ": must not be empty")
	}
	if maxBytes > 0 && len(v) > maxBytes {
		return apperr.InvalidInput(
			fmt.Sprintf("%s: %d bytes exceeds max %d", field, len(v), maxBytes),
		)
	}
	// The proto CEL rejects ANY ".." substring (not just a leading traversal), so mirror
	// that in addition to filepath.IsLocal (which only rejects an absolute/escaping path)
	// and the separators.
	if !filepath.IsLocal(v) || strings.ContainsAny(v, `/\`) || strings.Contains(v, "..") {
		return apperr.InvalidInput(
			field + ": must be a bare, local filename (no path separators or traversal)",
		)
	}
	if strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return apperr.InvalidInput(field + ": must not contain control characters")
	}
	return nil
}

// HTTPSURL checks that v parses as an http/https URL WITH a host (reuses net/url) —
// rejecting the javascript:, data:, file: URIs a bare URI check admits, and the
// host-less "https:" / "https://".
func HTTPSURL(field, v string) error {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return apperr.InvalidInput(field + ": must be an http(s) URL")
	}
	return nil
}

// Email checks that v is a bare, valid email address (reuses net/mail). It rejects the
// display-name form ("Name <a@b.com>") by requiring the parsed address to equal v, so
// only an addr-spec passes.
func Email(field, v string) error {
	addr, err := mail.ParseAddress(v)
	if err != nil || addr.Name != "" || addr.Address != v {
		return apperr.InvalidInput(field + ": must be a valid email address")
	}
	return nil
}

// IntRange checks that v is within the inclusive [lo, hi]. Use NoLowerBound /
// NoUpperBound for an absent bound; a real bound of 0 (e.g. proto {lte: 0}) is enforced,
// not mistaken for "unbounded".
func IntRange(field string, v, lo, hi int64) error {
	if v < lo || v > hi {
		return apperr.InvalidInput(
			fmt.Sprintf("%s: %d out of range [%d, %d]", field, v, lo, hi),
		)
	}
	return nil
}

// MaxItems checks that a repeated field's length n does not exceed maxItems.
func MaxItems(field string, n, maxItems int) error {
	if n > maxItems {
		return apperr.InvalidInput(
			fmt.Sprintf("%s: %d items exceeds max %d", field, n, maxItems),
		)
	}
	return nil
}

// EnumDefined checks that v is one of the enum's declared values (defined_only). valid is
// the set of declared values; the generated method passes the bound DTO enum's constants.
func EnumDefined(field string, v int32, valid []int32) error {
	for _, ok := range valid {
		if v == ok {
			return nil
		}
	}
	return apperr.InvalidInput(
		fmt.Sprintf("%s: %d is not a defined enum value", field, v),
	)
}
