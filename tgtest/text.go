package tgtest

import "testing"

// AssertSameText fails unless got is byte-identical to want, reporting where
// the two diverge.
//
// The no-loss proofs in this plan compare whole rendered documents — a source
// file, a table — so a plain "not equal" leaves the reader diffing two
// multi-kilobyte strings by eye, and a length comparison says nothing at all
// about a corruption that preserved the length. Both layers assert the same
// property, so both report it the same way.
func AssertSameText(t testing.TB, want, got string) {
	t.Helper()
	if want == got {
		return
	}
	at := 0
	for at < min(len(want), len(got)) && want[at] == got[at] {
		at++
	}
	t.Errorf("text differs at byte %d (want %d bytes, got %d)\n want: %q\n  got: %q",
		at, len(want), len(got), window(want, at), window(got, at))
}

// window quotes the run of s around at, so a failure shows the divergence in
// context instead of dumping the whole string.
func window(s string, at int) string {
	const context = 60
	return s[max(at-context, 0):min(at+context, len(s))]
}
