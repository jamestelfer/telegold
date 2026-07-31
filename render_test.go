package telegold

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// bugReportSource is the message from the defect that motivated this package.
// Telegram's legacy Markdown parser has no intraword-emphasis rule, so it
// consumes the underscore in each URL as an emphasis delimiter, italicises the
// text between them and leaves both autolinked destinations unresolvable.
//
// CommonMark does not treat an intraword underscore as emphasis, so parsing
// locally makes the defect disappear at the root. This is the regression test
// the whole package exists to keep passing.
const bugReportSource = "See https://example.com/a_b and https://example.com/c_d"

func TestRender_UnderscoreBearingURLsSurviveByteForByte(t *testing.T) {
	blocks, err := Render([]byte(bugReportSource))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}

	got := blocks[0].HTML()
	// Bare URLs are plain text under CommonMark — nothing autolinks them here,
	// and Telegram autolinks the visible text after parsing. The requirement is
	// that the destination survives unaltered, not that we wrap it.
	if got != bugReportSource {
		t.Errorf("Render() = %q, want byte-identical input %q", got, bugReportSource)
	}
	for _, url := range []string{"https://example.com/a_b", "https://example.com/c_d"} {
		if !contains(got, url) {
			t.Errorf("output is missing %q: %q", url, got)
		}
	}
}

// renderOne renders src and requires it to produce exactly one block.
func renderOne(t *testing.T, src string) string {
	t.Helper()
	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render(%q): %v", src, err)
	}
	if len(blocks) != 1 {
		t.Fatalf("Render(%q): block count = %d, want 1", src, len(blocks))
	}
	return blocks[0].HTML()
}

// TestRender_EscapesMarkupCharactersInText covers R3. Telegram documents these
// three characters as the ones needing escapes in HTML parse mode.
func TestRender_EscapesMarkupCharactersInText(t *testing.T) {
	got := renderOne(t, "a < b & c > d")
	want := "a &lt; b &amp; c &gt; d"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// TestRender_LeavesDoubleQuoteLiteral pins the decision to escape exactly the
// three documented characters. goldmark's own escaper emits &quot;, which
// Telegram does not document as supported, so it is restored to a literal.
func TestRender_LeavesDoubleQuoteLiteral(t *testing.T) {
	got := renderOne(t, `she said "hello"`)
	want := `she said "hello"`
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// TestRender_SourceEscapedAmpersandEntitySurvives guards the &quot; restoration
// against corrupting an ampersand the source escaped itself.
func TestRender_SourceEscapedAmpersandEntitySurvives(t *testing.T) {
	got := renderOne(t, "&amp;quot;")
	want := "&amp;quot;"
	if got != want {
		t.Errorf("Render() = %q, want %q — the entity must not be decoded to a quote", got, want)
	}
}

// TestRender_PreservesLiteralMarkdownPunctuation covers R4. These are the
// characters Telegram's legacy Markdown parser consumes as markup; the whole
// point of rendering HTML is that they travel as themselves. In particular no
// backslash may appear in front of any of them.
func TestRender_PreservesLiteralMarkdownPunctuation(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"intraword underscores", "snake_case_identifier", "snake_case_identifier"},
		{"asterisk in arithmetic", "2 * 3 = 6", "2 * 3 = 6"},
		{"unmatched bracket", "array[0] is first", "array[0] is first"},
		{"lone backtick", "a ` backtick", "a ` backtick"},
		{"backslash-escaped underscore", `\_not italic\_`, "_not italic_"},
		{"backslash-escaped asterisk", `\*not bold\*`, "*not bold*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderOne(t, tc.src)
			if got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
			if indexOf(got, `\`) >= 0 {
				t.Errorf("Render(%q) = %q — output must contain no backslash escape", tc.src, got)
			}
		})
	}
}

func TestRender_Emphasis(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"single asterisk is italic", "*italic*", "<i>italic</i>"},
		{"single underscore is italic", "_italic_", "<i>italic</i>"},
		{"double asterisk is bold", "**bold**", "<b>bold</b>"},
		{"nested emphasis", "**bold with *italic* inside**", "<b>bold with <i>italic</i> inside</b>"},
		{"emphasis in prose", "a *b* c", "a <i>b</i> c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderOne(t, tc.src); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestRender_Strikethrough covers R10. Telegram's subset spells strikethrough
// <s>; goldmark's CommonMark core has no strikethrough at all, so the
// Strikethrough extension has to be selected on the parser. It is selected
// individually rather than through extension.GFM, which also bundles linkify —
// that would wrap the bare underscore-bearing URLs of the originating defect in
// anchors.
func TestRender_Strikethrough(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"whole paragraph", "~~struck~~", "<s>struck</s>"},
		{"span in prose", "a ~~b~~ c", "a <s>b</s> c"},
		{"nested emphasis inside", "~~struck *and italic*~~", "<s>struck <i>and italic</i></s>"},
		// GFM matches a pair of one *or* two tildes, so a single tilde is also
		// strikethrough. An unmatched tilde stays literal.
		{"single tilde pair", "a ~b~ c", "a <s>b</s> c"},
		{"unmatched tilde is literal", "a ~ b c", "a ~ b c"},
		// Enabling the extension puts every tilde in ordinary prose at risk of
		// pairing into a strikethrough span. GFM's flanking rules prevent it, and
		// home-directory paths and approximations are common enough in agent
		// output that the property is worth pinning rather than assuming.
		{"home directory paths do not pair", "cd ~/foo and ~/bar", "cd ~/foo and ~/bar"},
		{"dotfile path", "the file ~/.bashrc is there", "the file ~/.bashrc is there"},
		{"approximations do not pair", "approx ~5 to ~10 items", "approx ~5 to ~10 items"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderOne(t, tc.src)
			if got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
			assertTagBalanced(t, got)
		})
	}
}

func TestRender_CodeSpan(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"simple span", "use `go test` here", "use <code>go test</code> here"},
		{"span preserves underscores", "`snake_case`", "<code>snake_case</code>"},
		{"span escapes markup", "`a < b`", "<code>a &lt; b</code>"},
		{"span keeps quotes literal", "`os.Getenv(\"HOME\")`", "<code>os.Getenv(\"HOME\")</code>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderOne(t, tc.src); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestRender_CodeSpanWithNonTextChildDoesNotPanic guards the unchecked
// c.(*ast.Text) assertion carried by upstream. A code span containing a raw
// newline puts a non-Text child in the span, and a panic here would happen
// inside the adapter's send path on user-supplied content.
func TestRender_CodeSpanWithNonTextChildDoesNotPanic(t *testing.T) {
	for _, src := range []string{
		"`multi\nline span`",
		"`span with ` + \"`\" + ` backtick`",
		"``a ` b``",
		"`  padded  `",
	} {
		blocks, err := Render([]byte(src))
		if err != nil {
			t.Fatalf("Render(%q): unexpected error %v", src, err)
		}
		if len(blocks) == 0 {
			t.Errorf("Render(%q) produced no blocks", src)
		}
	}
}

func TestRender_LinkWithAllowedScheme(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"https", "[label](https://example.com/p)", `<a href="https://example.com/p">label</a>`},
		{"http", "[label](http://example.com)", `<a href="http://example.com">label</a>`},
		{"mailto", "[mail](mailto:a@example.com)", `<a href="mailto:a@example.com">mail</a>`},
		{"tg", "[chat](tg://user?id=1)", `<a href="tg://user?id=1">chat</a>`},
		{
			"destination with underscore survives",
			"[doc](https://example.com/a_b)",
			`<a href="https://example.com/a_b">doc</a>`,
		},
		{
			"uppercase scheme is allowed",
			"[label](HTTPS://example.com)",
			`<a href="HTTPS://example.com">label</a>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderOne(t, tc.src); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestRender_LinkWithRejectedSchemeDropsTheAnchor closes the case-sensitivity
// bypass measured in upstream: its denylist compares lowercase prefixes without
// folding, so JaVaScRiPt: went straight into an href. The allowlist replaces it
// on arrival rather than surviving into a commit.
//
// Upstream also kept the anchor and blanked the href. The label must carry no
// anchor at all, and the destination must not appear anywhere in the output —
// Telegram autolinks plain text after parsing, so a destination left visible
// could be turned back into a link.
func TestRender_LinkWithRejectedSchemeDropsTheAnchor(t *testing.T) {
	cases := []struct {
		name string
		src  string
		dest string
	}{
		{"javascript", "[click](javascript:alert(1))", "javascript:alert(1)"},
		{"javascript case varied", "[click](JaVaScRiPt:alert(1))", "JaVaScRiPt:alert(1)"},
		{"data", "[click](data:text/html;base64,AAAA)", "data:text/html"},
		{"vbscript", "[click](vbscript:msgbox)", "vbscript:msgbox"},
		{"file", "[click](file:///etc/passwd)", "file:///etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderOne(t, tc.src)
			if got != "click" {
				t.Errorf("Render(%q) = %q, want the bare label %q", tc.src, got, "click")
			}
			if indexOf(got, "<a") >= 0 {
				t.Errorf("Render(%q) = %q — must emit no anchor element", tc.src, got)
			}
			if indexOf(got, tc.dest) >= 0 {
				t.Errorf("Render(%q) = %q — destination %q must not reach the output", tc.src, got, tc.dest)
			}
		})
	}
}

// TestRender_HostileSchemesAreRejected is R16's adversarial half. Upstream's
// denylist was measured passing "JaVaScRiPt:alert(1)" straight into an href, so
// these are the shapes an allowlist has to survive: case variation, padding,
// embedded control characters, and a scheme split across a line.
//
// CommonMark strips whitespace from a bracketed destination, so the padded and
// line-broken forms have to be built with angle-bracket destinations, which
// preserve the inner bytes.
func TestRender_HostileSchemesAreRejected(t *testing.T) {
	cases := []struct {
		name string
		src  string
		bad  string // a substring that must not survive into the output
		// notALink marks a source CommonMark refuses to parse as a link at all,
		// so the destination never reaches the scheme check. It survives as
		// ordinary prose, which is correct — the renderer emits any other
		// javascript: string in prose verbatim too. Only the anchor matters.
		notALink bool
	}{
		{name: "case varied", src: "[click](JaVaScRiPt:alert(1))", bad: "alert(1)"},
		{name: "all caps", src: "[click](JAVASCRIPT:alert(1))", bad: "alert(1)"},
		{name: "leading space", src: "[click](< javascript:alert(1)>)", bad: "alert(1)"},
		{name: "inner space", src: "[click](<java script:alert(1)>)", bad: "alert(1)"},
		{name: "tab inside the scheme", src: "[click](<java\tscript:alert(1)>)", bad: "alert(1)"},
		{name: "newline inside the scheme", src: "[click](<java\nscript:alert(1)>)", bad: "alert(1)", notALink: true},
		{name: "leading null byte", src: "[click](<\x00javascript:alert(1)>)", bad: "alert(1)"},
		{name: "leading delete byte", src: "[click](<\x7fjavascript:alert(1)>)", bad: "alert(1)"},
		{name: "scheme-relative", src: "[click](//evil.example.com/x)", bad: "evil.example.com"},
		{name: "relative path", src: "[click](/local/path)", bad: "/local/path"},
		{name: "bare word", src: "[click](notascheme)", bad: "notascheme"},
		{name: "empty scheme", src: "[click](<:nohost>)", bad: ":nohost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderOne(t, tc.src)
			if strings.Contains(got, "<a") {
				t.Errorf("Render(%q) = %q — must emit no anchor element", tc.src, got)
			}
			if strings.Contains(got, "href") {
				t.Errorf("Render(%q) = %q — must emit no href", tc.src, got)
			}
			if !tc.notALink && strings.Contains(got, tc.bad) {
				t.Errorf("Render(%q) = %q — %q must not reach the output", tc.src, got, tc.bad)
			}
			if !strings.Contains(got, "click") {
				t.Errorf("Render(%q) = %q — the label must survive", tc.src, got)
			}
			assertTagBalanced(t, got)
		})
	}
}

// TestRender_AutoLinkGoesThroughTheAllowlist covers the path upstream skipped
// entirely: its autolink renderer wrote the href unconditionally, bypassing its
// own scheme check.
//
// A rejected autolink keeps its label as escaped text, because for an autolink
// the label *is* the destination and there is nothing else to fall back to.
// That is no worse than the same string appearing as ordinary prose, which the
// renderer already emits verbatim — and Telegram does not autolink a
// javascript: scheme in plain text.
func TestRender_AutoLinkGoesThroughTheAllowlist(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"https autolink", "<https://example.com/a_b>", `<a href="https://example.com/a_b">https://example.com/a_b</a>`},
		{"email autolink", "<a@example.com>", `<a href="mailto:a@example.com">a@example.com</a>`},
		{"javascript autolink", "<javascript:alert(1)>", "javascript:alert(1)"},
		{"file autolink", "<file:///etc/passwd>", "file:///etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderOne(t, tc.src)
			if got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
			assertTagBalanced(t, got)
		})
	}
}

// TestRender_RawHTMLFailsClosed covers R15. Raw HTML is the only fail-closed
// case in this package: it has no representable form in Telegram's subset, and
// forwarding it would let the source inject markup we never validated.
func TestRender_RawHTMLFailsClosed(t *testing.T) {
	for _, src := range []string{
		"<div>a block of html</div>",
		"text with an inline <span>element</span> in it",
		"<script>alert(1)</script>",
		"<br>",
	} {
		blocks, err := Render([]byte(src))
		if err == nil {
			t.Errorf("Render(%q) = %v, want an error", src, blocks)
			continue
		}
		if !errors.Is(err, ErrRawHTML) {
			t.Errorf("Render(%q) error = %v, want ErrRawHTML", src, err)
		}
		if !strings.Contains(err.Error(), "raw HTML") {
			t.Errorf("Render(%q) error = %q, must name the construct", src, err)
		}
		if blocks != nil {
			t.Errorf("Render(%q) returned %v alongside the error, want nil", src, blocks)
		}
	}
}

// TestRender_ImageDegradesToAnAnchor covers R12. Telegram's HTML subset cannot
// embed an image in a text message, and upstream failed closed on one — which
// for arbitrary LLM output would route ordinary replies to the unstyled
// plain-text fallback. An anchor labelled with the alternative text keeps both
// the description and the destination reachable.
//
// This inverts the Phase 1 assertion at the same site: images used to return an
// error distinguishable from the raw-HTML one, and that split is what let this
// phase degrade images while raw HTML still fails closed.
func TestRender_ImageDegradesToAnAnchor(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			"alt text becomes the label",
			"![alt text](https://example.com/i.png)",
			`<a href="https://example.com/i.png">alt text</a>`,
		},
		{
			"underscore in the destination survives",
			"![diagram](https://example.com/a_b.png)",
			`<a href="https://example.com/a_b.png">diagram</a>`,
		},
		{
			"image inside prose",
			"before ![alt](https://example.com/i.png) after",
			`before <a href="https://example.com/i.png">alt</a> after`,
		},
		{
			"markup in the alt text is escaped",
			"![a < b](https://example.com/i.png)",
			`<a href="https://example.com/i.png">a &lt; b</a>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks, err := Render([]byte(tc.src))
			if err != nil {
				t.Fatalf("Render(%q) = %v, want no error — an image degrades, it does not fail closed", tc.src, err)
			}
			if len(blocks) != 1 {
				t.Fatalf("block count = %d, want 1", len(blocks))
			}
			if got := blocks[0].HTML(); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
			assertTagBalanced(t, blocks[0].HTML())
		})
	}
}

// TestRender_ImageWithRejectedSchemeDropsTheAnchor keeps R16 applying to the
// degraded form. An image is a link once degraded, so a hostile destination has
// exactly the same reach it would have through [label](dest).
func TestRender_ImageWithRejectedSchemeDropsTheAnchor(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"javascript with alt", "![the alt](javascript:alert(1))", "the alt"},
		{"case varied", "![the alt](JaVaScRiPt:alert(1))", "the alt"},
		{"data uri", "![the alt](data:image/png;base64,AAAA)", "the alt"},
		// Neither an allowlisted destination nor a label: the destination is the
		// one thing that must not be emitted, and inventing a placeholder would
		// put text in the message the author never wrote.
		{"no alt text either", "![](javascript:alert(1))", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks, err := Render([]byte(tc.src))
			if err != nil {
				t.Fatalf("Render(%q): %v", tc.src, err)
			}
			var got string
			if len(blocks) == 1 {
				got = blocks[0].HTML()
			}
			if got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
			if strings.Contains(got, "<a") || strings.Contains(got, "alert(1)") {
				t.Errorf("Render(%q) = %q — no anchor and no destination may reach the output", tc.src, got)
			}
		})
	}
}

// TestRender_ImageWithoutAltTextIsLabelledWithItsDestination records the
// empty-alt choice for an allowlisted destination. An anchor with no label
// renders as nothing at all in Telegram, which would drop the image silently.
func TestRender_ImageWithoutAltTextIsLabelledWithItsDestination(t *testing.T) {
	got := renderOne(t, "![](https://example.com/i_1.png)")
	want := `<a href="https://example.com/i_1.png">https://example.com/i_1.png</a>`
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	assertTagBalanced(t, got)
}

// threeColumnTable is a GFM table with cell widths that vary enough for a
// column-width bug to be visible.
const threeColumnTable = "| Name | Qty | Price |\n" +
	"|------|-----|-------|\n" +
	"| apple | 3 | 1.50 |\n" +
	"| watermelon | 12 | 0.25 |\n"

// TestRender_TableDegradesToAnAlignedPreBlock covers R11. Telegram's subset has
// no table elements, and upstream failed closed on one — but LLM replies contain
// tables routinely, so failing closed would send a large share of ordinary
// messages unstyled. A preformatted block is the representable form, and pre is
// what makes fixed-width columns line up.
func TestRender_TableDegradesToAnAlignedPreBlock(t *testing.T) {
	blocks, err := Render([]byte(threeColumnTable))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}
	got := blocks[0].HTML()
	want := "<pre>Name       | Qty | Price\n" +
		"-----------+-----+------\n" +
		"apple      | 3   | 1.50\n" +
		"watermelon | 12  | 0.25</pre>"
	if got != want {
		t.Errorf("Render() =\n%q\nwant\n%q", got, want)
	}
	assertTagBalanced(t, got)
}

// TestRender_TableColumnsStartAtTheSameOffsetOnEveryRow verifies alignment
// arithmetically rather than by eye, which is what catches the one-character
// error a reader forgives. Offsets are computed from the output.
func TestRender_TableColumnsStartAtTheSameOffsetOnEveryRow(t *testing.T) {
	src := "| id | description | n |\n" +
		"|----|-------------|---|\n" +
		"| 1 | a very long description indeed | 42 |\n" +
		"| 200 | short | 7 |\n" +
		"| 33 | m | 1000000 |\n"

	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}

	lines := strings.Split(preContent(t, blocks[0].HTML()), "\n")
	if len(lines) != 5 {
		t.Fatalf("line count = %d, want 5 (header, rule, three rows)", len(lines))
	}

	assertColumnsAligned(t, "row", lines, columnOffsets(lines[0]))
}

// assertColumnsAligned fails unless every non-empty line puts its columns where
// want says. Alignment is checked arithmetically rather than against an expected
// string, which is what catches the one-character error a reader forgives.
//
// label names what a line is in the failure message — a table row here, a chunk
// there.
func assertColumnsAligned(t *testing.T, label string, lines []string, want []int) {
	t.Helper()
	for i, line := range lines {
		if line == "" {
			continue
		}
		if got := columnOffsets(line); !slices.Equal(got, want) {
			t.Errorf("%s %d %q: columns at %v, want %v", label, i, line, got, want)
		}
	}
}

// columnOffsets returns the rune index at which each column of a rendered table
// row begins. Column zero always begins at zero; every later column begins one
// space after its preceding separator.
func columnOffsets(line string) []int {
	offsets := []int{0}
	for i, r := range []rune(line) {
		if r == '|' || r == '+' {
			offsets = append(offsets, i+2)
		}
	}
	return offsets
}

// TestRender_TableCellsAreEscapedAndCarryNoInlineMarkup records the cell-content
// decision. A tag inside the block would add bytes that occupy no display width,
// which is exactly what breaks fixed-width alignment — and Telegram does not
// accept arbitrary nesting inside pre either. Cell text is the plain text of the
// cell, escaped.
func TestRender_TableCellsAreEscapedAndCarryNoInlineMarkup(t *testing.T) {
	src := "| a | b |\n|---|---|\n| **bold** | x < y |\n"
	got := renderOne(t, src)
	if strings.Contains(got, "<b>") {
		t.Errorf("Render() = %q — a cell must not carry inline markup", got)
	}
	if !strings.Contains(got, "x &lt; y") {
		t.Errorf("Render() = %q, want the cell content escaped", got)
	}
	if !strings.Contains(got, "bold") {
		t.Errorf("Render() = %q — the cell's text must survive", got)
	}
	assertTagBalanced(t, got)
}

// TestRender_TableCellResolvesCharacterReferences covers the reference handling a
// cell shares with every other text path. Cells are the only text in the package
// that reaches output without goldmark's resolving writer, so a reference has to
// be resolved before the grid is escaped — otherwise the ampersand of the
// reference is itself escaped and the reader is shown &amp;amp; where the source
// asked for &.
//
// Resolving before layout is also what keeps the column arithmetic honest: a
// reference is one character to the reader and must be one column to tabwriter,
// not the five its source spelling occupies.
func TestRender_TableCellResolvesCharacterReferences(t *testing.T) {
	got := renderOne(t, "| a | b |\n|---|---|\n| &amp; | &lt;b&gt; |\n")
	// The rule spans the widest row measured unescaped — "& | <b>", seven
	// characters — which is the display width the reader sees.
	want := "<pre>a | b\n--+----\n&amp; | &lt;b&gt;</pre>"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	assertTagBalanced(t, got)
}

// TestRender_TableNestedInsideAnotherBlock covers the path a table takes when it
// is not a top-level node. Its pre lands in the enclosing block's content rather
// than as a wrapper, the same way a nested code block does.
func TestRender_TableNestedInsideAnotherBlock(t *testing.T) {
	got := renderOne(t, "> | a | b |\n> |---|---|\n> | 1 | 2 |\n")
	want := "<blockquote><pre>a | b\n--+--\n1 | 2</pre></blockquote>"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	assertTagBalanced(t, got)
}

// TestRender_TableWithoutABodyStillRenders guards the degenerate shape: GFM
// requires a delimiter row, so a header with no data rows is a valid table.
func TestRender_TableWithoutABodyStillRenders(t *testing.T) {
	got := renderOne(t, "| a | b |\n|---|---|\n")
	want := "<pre>a | b</pre>"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	assertTagBalanced(t, got)
}

// TestRender_TableRuleSpansTheWidestRow keeps the rule from stopping short when
// a body cell in the final column is wider than its header.
func TestRender_TableRuleSpansTheWidestRow(t *testing.T) {
	got := renderOne(t, "| a | n |\n|---|---|\n| x | 1000000 |\n")
	lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(got, "<pre>"), "</pre>"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q, want three", lines)
	}
	widest := 0
	for _, line := range lines {
		if n := len([]rune(line)); n > widest {
			widest = n
		}
	}
	if got := len([]rune(lines[1])); got != widest {
		t.Errorf("rule %q is %d wide, want %d — it must span the grid", lines[1], got, widest)
	}
}

// TestRender_TableCellsWithLinksCarryTheirTextOnce covers the link forms in a
// cell, which the rest of the table cases do not reach.
//
// Both link forms and a code span reduce to their text: a cell that emitted an
// anchor would put bytes of no display width into the grid and break the column
// arithmetic, and a cell that wrote its text twice would silently widen its
// column — which reads as an alignment bug rather than a duplication one.
func TestRender_TableCellsWithLinksCarryTheirTextOnce(t *testing.T) {
	src := "| kind | value |\n" +
		"|------|-------|\n" +
		"| autolink | <https://example.com/a_b> |\n" +
		"| inline | [label](https://example.com/c_d) |\n" +
		"| code | `snake_case` |\n"

	got := renderOne(t, src)

	for _, once := range []string{"https://example.com/a_b", "label", "snake_case"} {
		if n := strings.Count(got, once); n != 1 {
			t.Errorf("Render() = %q\n%q appears %d times, want exactly once", got, once, n)
		}
	}
	// A cell is text, so neither link form may bring markup into the pre block.
	if strings.Contains(got, "<a") {
		t.Errorf("Render() = %q — a cell must carry no anchor", got)
	}
	assertTagBalanced(t, got)
}

// TestRender_PipesInProseDoNotBecomeATable guards the parsing change enabling
// the Table extension makes to every input, not just to tables.
func TestRender_PipesInProseDoNotBecomeATable(t *testing.T) {
	cases := []string{
		"run a | b to pipe the output",
		"the regex (foo|bar) matches either",
		"| a lone leading pipe",
	}
	for _, src := range cases {
		got := renderOne(t, src)
		if strings.Contains(got, "<pre>") {
			t.Errorf("Render(%q) = %q — prose must not become a table", src, got)
		}
		if got != src {
			t.Errorf("Render(%q) = %q, want byte-identical input", src, got)
		}
	}
}

// TestRender_RawHTMLIsTheOnlyFailClosedConstruct pins the boundary this phase
// draws. Upstream returned one shared error for raw HTML and images alike;
// Phase 1 split it so that exactly this could happen.
func TestRender_RawHTMLIsTheOnlyFailClosedConstruct(t *testing.T) {
	degrades := []string{
		"![alt](https://example.com/i.png)",
		"| a | b |\n|---|---|\n| 1 | 2 |",
	}
	for _, src := range degrades {
		if _, err := Render([]byte(src)); err != nil {
			t.Errorf("Render(%q) = %v, want no error", src, err)
		}
	}
	if _, err := Render([]byte("<div>x</div>")); !errors.Is(err, ErrRawHTML) {
		t.Errorf("Render of raw HTML = %v, want ErrRawHTML — raw HTML still fails closed", err)
	}
}

// TestRender_ParagraphsBecomeSeparateBlocks covers R5's block boundary: one
// top-level markdown node produces at most one block.
func TestRender_ParagraphsBecomeSeparateBlocks(t *testing.T) {
	blocks, err := Render([]byte("first para\n\nsecond para\n\nthird para"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("block count = %d, want 3", len(blocks))
	}
	for i, want := range []string{"first para", "second para", "third para"} {
		if got := blocks[i].HTML(); got != want {
			t.Errorf("block %d = %q, want %q", i, got, want)
		}
	}
}

// TestRender_HeadingIsBoldFollowedByLineBreak covers R14. Telegram's subset has
// no heading element and no way to express a level, so every level renders bold
// — and the line break is part of the requirement, because a heading that runs
// into the next sentence is not recognisable as a heading at all.
func TestRender_HeadingIsBoldFollowedByLineBreak(t *testing.T) {
	for _, src := range []string{"# Title", "## Title", "###### Title", "Title\n=====", "Title\n-----"} {
		got := renderOne(t, src)
		if got != "<b>Title</b>\n" {
			t.Errorf("Render(%q) = %q, want %q", src, got, "<b>Title</b>\n")
		}
		assertTagBalanced(t, got)
	}
}

// TestRender_HeadingIsSeparatedFromFollowingProseByOneBlankLine pins the
// interaction between R14's line break and the block separator. The heading
// carries its own newline, so the joiner must not add a second one on top of it
// — two blank lines between a heading and its first sentence reads as a gap,
// not as a heading.
func TestRender_HeadingIsSeparatedFromFollowingProseByOneBlankLine(t *testing.T) {
	blocks, err := Render([]byte("# Title\n\nBody text."))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := Join(blocks)
	want := "<b>Title</b>\n\nBody text."
	if got != want {
		t.Errorf("Join() = %q, want %q", got, want)
	}
}

// TestRender_HeadingInsideBlockquoteKeepsItsLineBreak checks R14 holds where the
// heading is not a top-level block and the surrounding joiner is a single
// newline rather than a blank line.
func TestRender_HeadingInsideBlockquoteKeepsItsLineBreak(t *testing.T) {
	got := renderOne(t, "> # Quoted heading\n>\n> Body.")
	want := "<blockquote><b>Quoted heading</b>\n\nBody.</blockquote>"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	assertTagBalanced(t, got)
}

// TestRender_HeadingInsideListItemDoesNotOpenABlankLine covers the other joiner
// a heading can sit against: list items are separated by a single newline, and
// the heading already supplied it.
func TestRender_HeadingInsideListItemDoesNotOpenABlankLine(t *testing.T) {
	got := renderOne(t, "- # Item heading\n- plain item")
	want := "- <b>Item heading</b>\n- plain item"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	assertTagBalanced(t, got)
}

// TestRender_FencedCodeBlockKeepsItsTagsSeparateFromItsText covers the Block
// shape the chunker depends on: the enclosing elements are their own tokens, so
// a split can close and reopen them without any string inspection.
func TestRender_FencedCodeBlockKeepsItsTagsSeparateFromItsText(t *testing.T) {
	blocks, err := Render([]byte("```go\nfmt.Println(\"hi\")\n```"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}
	b := blocks[0]

	want := []token{
		openToken("pre", ""),
		openToken("code", ` class="language-go"`),
		textToken("fmt.Println(\"hi\")\n"),
		closeToken("code"),
		closeToken("pre"),
	}
	if len(b.tokens) != len(want) {
		t.Fatalf("tokens = %+v, want %+v", b.tokens, want)
	}
	for i := range want {
		if b.tokens[i] != want[i] {
			t.Errorf("token %d = %+v, want %+v", i, b.tokens[i], want[i])
		}
	}

	wantHTML := "<pre><code class=\"language-go\">fmt.Println(\"hi\")\n</code></pre>"
	if got := b.HTML(); got != wantHTML {
		t.Errorf("HTML() = %q, want %q", got, wantHTML)
	}
	if got := b.Len(); got != len(wantHTML) {
		t.Errorf("Len() = %d, want %d — length must not need the string built", got, len(wantHTML))
	}
}

// preContent returns the text inside a single pre element, for tests that assert
// on a degraded table's grid rather than on its markup.
func preContent(t *testing.T, html string) string {
	t.Helper()
	const open, close = "<pre>", "</pre>"
	if !strings.HasPrefix(html, open) || !strings.HasSuffix(html, close) {
		t.Fatalf("html = %q, want a single pre element", html)
	}
	return html[len(open) : len(html)-len(close)]
}

// TestRender_FencedCodeCarriesItsLanguage covers R13. Telegram documents the
// language annotation as a class on a <code> nested inside <pre>, in that order,
// and names the "language-xxx" form; anything else loses the annotation.
//
// An unannotated fence still emits pre+code rather than a bare pre, so every
// code block has the same shape — which is what lets the chunker reopen one
// wrapper set when it splits an oversized block.
func TestRender_FencedCodeCarriesItsLanguage(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			"annotated fence",
			"```go\nx := 1\n```",
			"<pre><code class=\"language-go\">x := 1\n</code></pre>",
		},
		{
			"unannotated fence",
			"```\nx := 1\n```",
			"<pre><code>x := 1\n</code></pre>",
		},
		{
			"language with punctuation",
			"```objective-c\nid x;\n```",
			"<pre><code class=\"language-objective-c\">id x;\n</code></pre>",
		},
		{
			"info string beyond the language is dropped",
			"```go title=main.go\nx := 1\n```",
			"<pre><code class=\"language-go\">x := 1\n</code></pre>",
		},
		{
			"indented code has no language to carry",
			"    x := 1",
			"<pre><code>x := 1\n</code></pre>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderOne(t, tc.src)
			if got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.src, got, tc.want)
			}
			assertTagBalanced(t, got)
		})
	}
}

// TestRender_FencedCodeLanguageIsEscapedIntoTheClass keeps the language
// annotation from becoming an injection point: it comes from the source, lands
// inside a quoted attribute, and must not be able to close it.
func TestRender_FencedCodeLanguageIsEscapedIntoTheClass(t *testing.T) {
	got := renderOne(t, "```a\"><b\nbody\n```")
	if strings.Contains(got, `"><b`) {
		t.Errorf("Render() = %q — the language annotation escaped its attribute", got)
	}
	assertTagBalanced(t, got)
}

// TestRender_CodeBlockContentIsEscaped guards against code fences becoming an
// injection vector: a < inside a fence is content, not markup.
func TestRender_CodeBlockContentIsEscaped(t *testing.T) {
	blocks, err := Render([]byte("```\n<script>alert(1)</script>\n```"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := blocks[0].HTML()
	if strings.Contains(got, "<script") {
		t.Errorf("HTML() = %q — the fence content must be escaped, not emitted as markup", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("HTML() = %q, want the escaped script tag", got)
	}
}

// TestRender_ThematicBreakEmitsNoElement covers R7, and R2 and R5 at the point
// upstream breaks both: it writes "<hr" and then a separator, producing the
// unterminated "<hr* * *" that Telegram rejects outright. Telegram's subset has
// no hr element, so the separator has to be text.
//
// A run of em dashes is chosen over upstream's "* * *", which reads as unrendered
// markdown source rather than as a rule; em dashes abut into a continuous line.
func TestRender_ThematicBreakEmitsNoElement(t *testing.T) {
	for _, src := range []string{"---", "***", "___", "- - -"} {
		got := renderOne(t, src)
		if strings.Contains(got, "<") {
			t.Errorf("Render(%q) = %q — must contain no < at all (upstream emitted %q)", src, got, "<hr* * *")
		}
		if got != thematicBreakText {
			t.Errorf("Render(%q) = %q, want %q", src, got, thematicBreakText)
		}
		assertTagBalanced(t, got)
	}
}

// TestRender_ThematicBreakSeparatorIsPinned fixes the chosen bytes so the
// decision cannot drift silently. Whether it looks like a rule on a given client
// is an appearance question for system testing.
func TestRender_ThematicBreakSeparatorIsPinned(t *testing.T) {
	if thematicBreakText != "——————————" {
		t.Errorf("thematicBreakText = %q, want ten em dashes", thematicBreakText)
	}
	if strings.ContainsAny(thematicBreakText, "<>&") {
		t.Errorf("thematicBreakText = %q must not contain a character that needs escaping", thematicBreakText)
	}
}

// TestRender_NoConstructIsSilentlyDropped is the content-preservation guard. A
// renderer that quietly skips a node kind loses user content with no error and
// no fallback, which is the failure class this whole package exists to remove.
func TestRender_NoConstructIsSilentlyDropped(t *testing.T) {
	cases := map[string]struct {
		src  string
		want string // a substring that must survive
	}{
		"bullet list":     {"- alpha\n- beta", "alpha"},
		"ordered list":    {"1. alpha\n2. beta", "alpha"},
		"nested list":     {"- parent\n    - child", "child"},
		"block quote":     {"> quoted", "quoted"},
		"indented code":   {"    indented code", "indented code"},
		"setext heading":  {"Title\n=====", "Title"},
		"loose list item": {"- alpha\n\n- beta", "beta"},
		"table syntax":    {"| a | b |\n|---|---|\n| 1 | 2 |", "a"},
		"strikethrough":   {"~~struck~~", "struck"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			blocks, err := Render([]byte(tc.src))
			if err != nil {
				t.Fatalf("Render(%q): %v", tc.src, err)
			}
			var all strings.Builder
			for _, b := range blocks {
				all.WriteString(b.HTML())
				all.WriteString("\n")
			}
			if !strings.Contains(all.String(), tc.want) {
				t.Errorf("Render(%q) = %q, must retain %q", tc.src, all.String(), tc.want)
			}
		})
	}
}

// TestRender_EveryBlockIsTagBalanced covers R5 across the whole construct set.
func TestRender_EveryBlockIsTagBalanced(t *testing.T) {
	src := strings.Join([]string{
		"# Heading",
		"",
		"Prose with *italic*, **bold**, `code` and a [link](https://example.com/a_b).",
		"",
		"- bullet one",
		"- bullet two",
		"",
		"1. first",
		"2. second",
		"",
		"> a quotation",
		"",
		"---",
		"",
		"```go",
		"if a < b { return }",
		"```",
		"",
		"A rejected [link](javascript:alert(1)) and an escaped & ampersand.",
	}, "\n")

	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("no blocks rendered")
	}
	for i, b := range blocks {
		t.Run("block"+strconv.Itoa(i), func(t *testing.T) {
			assertTagBalanced(t, b.HTML())
		})
	}
}

// TestRender_SoftLineBreakEmitsNewline covers R6.
//
// Measured before the fix: "alpha beta\ngamma delta" rendered as
// "alpha betagamma delta" — the last word of one source line fused to the first
// word of the next. LLM output is wrapped prose, so this corrupted a large share
// of ordinary replies.
func TestRender_SoftLineBreakEmitsNewline(t *testing.T) {
	const src = "alpha beta\ngamma delta"
	got := renderOne(t, src)
	if got != src {
		t.Errorf("Render(%q) = %q, want %q", src, got, src)
	}
	if strings.Contains(got, "betagamma") {
		t.Errorf("Render(%q) = %q — words fused across the line break", src, got)
	}
}

// TestRender_HardLineBreakEmitsNewline covers R34.
//
// Measured before the fix: "alpha␠␠\ngamma" rendered as "alphagamma". R6 covered
// only the soft case, but the failure and the fix are identical.
func TestRender_HardLineBreakEmitsNewline(t *testing.T) {
	for _, src := range []string{"alpha  \ngamma", "alpha\\\ngamma"} {
		got := renderOne(t, src)
		if got != "alpha\ngamma" {
			t.Errorf("Render(%q) = %q, want %q", src, got, "alpha\ngamma")
		}
	}
}

// TestRender_WrappedProseNeverFusesWords is the realistic form of R6 and R34.
// A per-construct test can pass while ordinary wrapped prose still breaks, so
// this renders a multi-paragraph reply wrapped at 80 columns and checks every
// source line boundary.
func TestRender_WrappedProseNeverFusesWords(t *testing.T) {
	src := strings.Join([]string{
		"The quick brown fox jumps over the lazy dog and continues running until it",
		"reaches the far side of the meadow where the grass grows tall and green in",
		"the summer sunshine without any interruption whatsoever from the weather.",
		"",
		"A second paragraph follows the first one here so that the block boundary is",
		"exercised alongside the soft line breaks inside each individual paragraph.",
	}, "\n")

	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := Join(blocks)

	// Every word in the source must appear as a whole word in the output.
	for word := range strings.FieldsSeq(src) {
		if !containsWord(got, word) {
			t.Errorf("word %q is not present as a whole word in %q", word, got)
		}
	}
	// And the source's line count must survive.
	if wantLines, gotLines := strings.Count(src, "\n"), strings.Count(got, "\n"); gotLines != wantLines {
		t.Errorf("newline count = %d, want %d — a line break was dropped", gotLines, wantLines)
	}
}

// containsWord reports whether s contains word delimited by whitespace or the
// string boundary, which is what catches a fused pair like "dogA".
func containsWord(s, word string) bool {
	for field := range strings.FieldsSeq(s) {
		if strings.Trim(field, ".,;:") == strings.Trim(word, ".,;:") {
			return true
		}
	}
	return false
}

// TestRender_BlockquoteEmitsElement covers R8, and both of its defects.
//
// Measured before the fix: "> line one\n> line two" rendered as
// "&gt; line oneline two" — a literal &gt; instead of the supported element, and
// the quote's internal line break dropped through the same path as R6. Fixing
// only the visible one leaves the quotation's lines fused.
func TestRender_BlockquoteEmitsElement(t *testing.T) {
	got := renderOne(t, "> line one\n> line two")

	if !strings.HasPrefix(got, "<blockquote>") || !strings.HasSuffix(got, "</blockquote>") {
		t.Errorf("Render() = %q, want a blockquote element", got)
	}
	if strings.Contains(got, "&gt;") {
		t.Errorf("Render() = %q — must not emit a literal &gt; quote marker", got)
	}
	if got != "<blockquote>line one\nline two</blockquote>" {
		t.Errorf("Render() = %q, want the two lines separated by a newline", got)
	}
	assertTagBalanced(t, got)
}

func TestRender_BlockquoteWithMultipleParagraphs(t *testing.T) {
	got := renderOne(t, "> first para\n>\n> second para")
	want := "<blockquote>first para\n\nsecond para</blockquote>"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	assertTagBalanced(t, got)
}

// TestRender_NestedBlockquoteIsFlattened records the nesting decision. Telegram
// represents a quotation as a message entity over a text range, and a blockquote
// entity cannot contain another, so a nested quote collapses to one level rather
// than emitting markup Telegram would reject.
func TestRender_NestedBlockquoteIsFlattened(t *testing.T) {
	got := renderOne(t, "> outer\n> > inner")

	if n := strings.Count(got, "<blockquote>"); n != 1 {
		t.Errorf("Render() = %q — want exactly one blockquote element, got %d", got, n)
	}
	if !strings.Contains(got, "outer") || !strings.Contains(got, "inner") {
		t.Errorf("Render() = %q — both quote levels' text must survive", got)
	}
	assertTagBalanced(t, got)
}

// TestRender_NestedListIndents covers R9.
//
// Measured before the fix: "- parent\n    - child" rendered as
// "- parent\n- child" — upstream writes a constant "- " at every depth, so the
// nesting was flattened and the structure lost.
//
// Telegram's HTML subset has no list elements, so the indent must be textual.
// Ordinary spaces are used: Telegram's HTML parse mode is not an HTML renderer —
// tags are parsed into message entities over the text and the text is kept
// verbatim, which is the same property the newline fixes above rely on and which
// the documented blockquote example demonstrates by putting literal newlines
// inside the element.
func TestRender_NestedListIndents(t *testing.T) {
	got := renderOne(t, "- parent\n    - child")
	want := "- parent\n  - child"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestRender_NestedListIndentsPerLevel(t *testing.T) {
	src := "- one\n    - two\n        - three"
	got := renderOne(t, src)
	want := "- one\n  - two\n    - three"
	if got != want {
		t.Errorf("Render(%q) = %q, want %q", src, got, want)
	}

	// Nesting depth must be recoverable from the output.
	for i, line := range strings.Split(got, "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if want := i * len(listIndent); indent != want {
			t.Errorf("line %d %q has indent %d, want %d", i, line, indent, want)
		}
	}
}

// TestRender_OrderedListKeepsOrdinals covers R35.
//
// Measured before the fix: "1. first\n2. second" rendered as
// "- first\n- second" — numbered steps silently became bullets.
func TestRender_OrderedListKeepsOrdinals(t *testing.T) {
	got := renderOne(t, "1. first\n2. second\n3. third")
	want := "1. first\n2. second\n3. third"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// TestRender_OrderedListHonoursNonOneStart guards against a fix that simply
// counts items from the top, which would silently renumber the list.
func TestRender_OrderedListHonoursNonOneStart(t *testing.T) {
	got := renderOne(t, "5. five\n6. six")
	want := "5. five\n6. six"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestRender_NestedOrderedListInsideBullet(t *testing.T) {
	got := renderOne(t, "- parent\n    1. first\n    2. second")
	want := "- parent\n  1. first\n  2. second"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// KitchenSinkSource contains every construct fixed alongside the renderer's
// vendored-in defects, together, so the fixes are shown not to interfere with
// each other.
//
// The adapter package keeps its own copy of this document rather than importing
// it: an identifier declared in a _test.go file is not visible outside its own
// test binary, so the two literals have to be edited together.
const KitchenSinkSource = "Wrapped prose that runs across\ntwo source lines here.\n\n" +
	"A hard break line  \nfollows immediately.\n\n" +
	"---\n\n" +
	"> quoted line one\n> quoted line two\n\n" +
	"- parent bullet\n    - child bullet\n\n" +
	"1. first step\n2. second step\n"

// KitchenSinkHTML is the expected rendering of KitchenSinkSource.
const KitchenSinkHTML = "Wrapped prose that runs across\ntwo source lines here." +
	"\n\n" + "A hard break line\nfollows immediately." +
	"\n\n" + "——————————" +
	"\n\n" + "<blockquote>quoted line one\nquoted line two</blockquote>" +
	"\n\n" + "- parent bullet\n  - child bullet" +
	"\n\n" + "1. first step\n2. second step"

// TestRender_KitchenSink covers R6, R7, R8, R9, R34 and R35 in one document.
// Each construct has its own case above; this one exists because a per-construct
// fix can pass while two constructs in sequence still interfere.
func TestRender_KitchenSink(t *testing.T) {
	blocks, err := Render([]byte(KitchenSinkSource))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := Join(blocks)
	if got != KitchenSinkHTML {
		t.Errorf("Render() mismatch\n got: %q\nwant: %q", got, KitchenSinkHTML)
	}
	for i, b := range blocks {
		assertTagBalanced(t, b.HTML())
		if b.Len() == 0 {
			t.Errorf("block %d is empty", i)
		}
	}
}

// TypographySource is the inline-and-block typography document: a heading, a
// language-annotated fenced code block, a strikethrough span, an ordinary link
// and a javascript: link, together. Each construct has its own case above; this
// exists because they can each be right in isolation and interfere in sequence.
//
// As with KitchenSinkSource, the adapter package keeps its own copy — a
// _test.go identifier cannot be imported — so the two must be edited together.
const TypographySource = "## Results\n\n" +
	"The ~~old~~ new approach works. See [the docs](https://example.com/a_b) " +
	"and do not follow [this one](javascript:alert(1)).\n\n" +
	"```go\nif a < b { return \"ok\" }\n```\n"

// TypographyHTML is the expected rendering of TypographySource.
const TypographyHTML = "<b>Results</b>\n" +
	"\n" +
	"The <s>old</s> new approach works. See <a href=\"https://example.com/a_b\">the docs</a> " +
	"and do not follow this one." +
	"\n\n" +
	"<pre><code class=\"language-go\">if a &lt; b { return \"ok\" }\n</code></pre>"

// TestRender_TypographyDocument covers R10, R13, R14 and R16 in one document.
func TestRender_TypographyDocument(t *testing.T) {
	blocks, err := Render([]byte(TypographySource))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := Join(blocks)
	if got != TypographyHTML {
		t.Errorf("Render() mismatch\n got: %q\nwant: %q", got, TypographyHTML)
	}
	if strings.Contains(got, "alert(1)") {
		t.Errorf("Render() = %q — the rejected destination reached the output", got)
	}
	for i, b := range blocks {
		assertTagBalanced(t, b.HTML())
		if b.Len() == 0 {
			t.Errorf("block %d is empty", i)
		}
	}
}

// contains is strings.Contains, spelled out to keep the failure messages above
// readable without an import that later cases do not need.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestRender_TableCellKeepsALinkLabelAndDropsItsDestination records a limit of
// the plain-text cell rule, so it is a decision rather than an accident.
//
// A cell carries text and no markup — that is what keeps the columns aligned,
// since a tag adds bytes occupying no display width. An inline link therefore
// contributes its label and its destination goes nowhere, while an autolink
// keeps its URL because for an autolink the URL *is* the label. The asymmetry
// looks odd side by side and is the honest consequence of the rule: the
// alternative is either a destination in parentheses, which is text the author
// did not write and which wrecks the column widths, or markup inside `pre`.
func TestRender_TableCellKeepsALinkLabelAndDropsItsDestination(t *testing.T) {
	src := "| doc | ref |\n|---|---|\n" +
		"| [manual](https://example.com/grep) | <https://example.com/auto> |\n"

	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := Join(blocks)

	if !strings.Contains(got, "manual") {
		t.Errorf("output %q lost the link label", got)
	}
	if strings.Contains(got, "https://example.com/grep") {
		t.Errorf("output %q carries an inline link's destination, which the cell rule drops", got)
	}
	if !strings.Contains(got, "https://example.com/auto") {
		t.Errorf("output %q lost the autolink, whose label is its destination", got)
	}
	assertTagBalanced(t, got)
}
