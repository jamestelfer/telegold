// Package tghtmltest holds assertions about tghtml's output that more than one
// package's tests need.
//
// Tag balance is checked at two layers: tghtml's own tests assert it on every
// block and every chunk, and the adapter's tests assert it on the bytes actually
// handed to Telegram. One implementation, so the two cannot drift into
// disagreeing about what balanced means.
package tghtmltest

import (
	"strings"
	"testing"
)

// telegramTags is the set of elements Telegram's HTML parse mode accepts that
// this renderer emits. Telegram documents a closed list; anything outside it is
// rejected by their parser, which would route the whole message to the
// plain-text fallback.
//
// Kept deliberately narrow: constructs Telegram supports but the agent does not
// emit (tg-spoiler, tg-emoji) are absent until something needs them.
var telegramTags = map[string]bool{
	"b":          true,
	"i":          true,
	"u":          true,
	"s":          true,
	"a":          true,
	"code":       true,
	"pre":        true,
	"blockquote": true,
}

// AssertTagBalanced fails unless every element opened in html is closed within
// html, in the correct order, and every tag name is one Telegram accepts.
//
// This is the R5 invariant, and the chunker's safety rests on it entirely: a
// chunker cannot defend itself against a renderer that emits an unclosed
// element. The same helper is applied to every chunk the chunker emits.
func AssertTagBalanced(t testing.TB, html string) {
	t.Helper()

	var stack []string
	for i := 0; i < len(html); i++ {
		if html[i] != '<' {
			continue
		}
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			t.Errorf("unterminated tag at offset %d in %q", i, html)
			return
		}
		tag := html[i+1 : i+end]
		i += end

		closing := strings.HasPrefix(tag, "/")
		tag = strings.TrimPrefix(tag, "/")
		// Drop any attributes; only the element name matters for balance.
		if sp := strings.IndexByte(tag, ' '); sp >= 0 {
			tag = tag[:sp]
		}
		if tag == "" {
			t.Errorf("empty tag name at offset %d in %q", i, html)
			return
		}
		if !telegramTags[tag] {
			t.Errorf("tag %q is not in Telegram's accepted set, in %q", tag, html)
			return
		}

		if !closing {
			stack = append(stack, tag)
			continue
		}
		if len(stack) == 0 {
			t.Errorf("closing </%s> with nothing open, in %q", tag, html)
			return
		}
		open := stack[len(stack)-1]
		if open != tag {
			t.Errorf("closing </%s> but <%s> is innermost open, in %q", tag, open, html)
			return
		}
		stack = stack[:len(stack)-1]
	}

	if len(stack) != 0 {
		t.Errorf("unclosed elements %v in %q", stack, html)
	}
}
