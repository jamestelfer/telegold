// The error split in this file replaces telegold's single errTagNotAllowed
// (https://github.com/leonid-shevtsov/telegold, commit 36dc899, MIT licence —
// see the LICENSE file in this directory).

package telegold

// UnsupportedError reports a markdown construct that Telegram's HTML subset
// cannot represent. It names the construct so a caller can log something
// actionable and so the adapter can tell one cause from another.
//
// telegold returns a single shared error for raw HTML and images alike, which
// names nothing and makes a must-fail-closed case indistinguishable from a
// should-degrade one. These are separate values for that reason.
type UnsupportedError struct {
	// Construct is the markdown construct that could not be rendered.
	Construct string
}

func (e *UnsupportedError) Error() string {
	return "telegold: " + e.Construct + " is not supported by Telegram's HTML subset"
}

// ErrRawHTML is returned when the source contains raw HTML, block or inline.
// This is the only construct that fails closed: it has no representable form and
// forwarding it would emit markup that never passed through this renderer's tag
// allowlist.
//
// Images used to share telegold's single error with raw HTML, which is what made
// the split necessary — a caller could not tell a must-fail-closed case from a
// should-degrade one. Images now degrade to an anchor and tables to a
// preformatted block, so raw HTML is the only value left. The named-construct
// shape stays because it is what a future unsupported construct would reuse.
var ErrRawHTML = &UnsupportedError{Construct: "raw HTML"}
