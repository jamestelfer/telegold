// This file replaces telegold's IsDangerousURL check
// (https://github.com/leonid-shevtsov/telegold, commit 36dc899, MIT licence —
// see the LICENSE file in this directory). Upstream's check is a case-sensitive
// prefix denylist; this is an allowlist. The email-prefix handling is derived
// from upstream's autolink renderer.

package telegold

import (
	"bytes"
)

// allowedSchemes are the URL schemes that may appear in an anchor's href.
//
// This is an allowlist, deliberately. It replaces the denylist carried by
// telegold, which compared lowercase prefixes with bytes.HasPrefix and no case
// folding — so "JaVaScRiPt:alert(1)" passed straight through into an href. A
// denylist of "dangerous" schemes also fails open on the scheme nobody thought
// of, whereas a link dropped to plain text is an acceptable outcome.
var allowedSchemes = map[string]bool{
	"http":   true,
	"https":  true,
	"tg":     true,
	"mailto": true,
}

// schemeAllowed reports whether dest may be used as an anchor destination.
//
// It is total: any destination it cannot confidently classify is rejected. A
// destination with no scheme at all — relative, or scheme-relative like
// //host/path — is not on the allowlist and so is rejected.
func schemeAllowed(dest []byte) bool {
	scheme, ok := schemeOf(dest)
	if !ok {
		return false
	}
	return allowedSchemes[scheme]
}

// schemeOf extracts the lowercased scheme of dest, reporting false when dest
// carries no scheme.
//
// Bytes at or below 0x20 — spaces, tabs, newlines and other control characters —
// are stripped before matching, so a padded or line-broken scheme cannot slip
// past the allowlist. Stripping is applied only to the classification, never to
// the emitted destination, and it can only ever make the decision stricter: a
// scheme that matches after stripping would otherwise not have matched at all.
func schemeOf(dest []byte) (string, bool) {
	var scheme []byte
	for _, c := range dest {
		if c <= 0x20 || c == 0x7f {
			continue // stripped: whitespace and control characters
		}
		if c == ':' {
			if len(scheme) == 0 {
				return "", false
			}
			return string(scheme), true
		}
		// A path, query or fragment separator ends any chance of a scheme.
		if c == '/' || c == '?' || c == '#' {
			return "", false
		}
		if !isSchemeByte(c, len(scheme) == 0) {
			return "", false
		}
		scheme = append(scheme, lowerASCII(c))
	}
	return "", false
}

// isSchemeByte reports whether c is valid in a URL scheme. RFC 3986 requires
// the first character to be a letter and the rest to be letters, digits, '+',
// '-' or '.'.
func isSchemeByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case first:
		return false
	case c >= '0' && c <= '9':
		return true
	case c == '+' || c == '-' || c == '.':
		return true
	}
	return false
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// emailDestination builds the href for an email autolink, which CommonMark
// reports without its mailto: prefix.
func emailDestination(url []byte) []byte {
	if bytes.HasPrefix(bytes.ToLower(url), []byte("mailto:")) {
		return url
	}
	return append([]byte("mailto:"), url...)
}
