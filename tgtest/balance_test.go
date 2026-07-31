package tgtest

import "testing"

func TestAssertTagBalanced_AcceptsBalancedHTML(t *testing.T) {
	for _, html := range []string{
		"",
		"plain text",
		"<b>bold</b>",
		"<b>outer <i>inner</i> outer</b>",
		`<a href="https://example.com/a_b">label</a>`,
		`<pre><code class="language-go">body</code></pre>`,
		"a &lt; b — an escaped angle bracket is not a tag",
	} {
		t.Run(html, func(t *testing.T) {
			AssertTagBalanced(t, html)
		})
	}
}

// TestAssertTagBalanced_RejectsMalformedHTML proves the helper actually fails.
// A balance assertion that cannot fail would silently pass every later phase.
func TestAssertTagBalanced_RejectsMalformedHTML(t *testing.T) {
	cases := map[string]string{
		"unclosed element":     "<b>bold",
		"stray close":          "bold</b>",
		"crossed nesting":      "<b><i>x</b></i>",
		"unterminated tag":     "<hr* * *",
		"tag outside the set":  "<div>x</div>",
		"fragment of a tag":    "<b>x</b><co",
		"empty tag name":       "<>x",
		"close of unopened":    "<b>x</b></i>",
		"upstream broken rule": "<hr",
	}
	for name, html := range cases {
		t.Run(name, func(t *testing.T) {
			spy := &testing.T{}
			AssertTagBalanced(spy, html)
			if !spy.Failed() {
				t.Errorf("AssertTagBalanced(%q) passed, want a failure", html)
			}
		})
	}
}
