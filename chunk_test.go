package telegold

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jamestelfer/telegold/tghtmltest"
)

// textBlock builds a Block of plain content, for chunker cases where what is in
// the block does not matter, only how long it is.
func textBlock(content string) Block {
	var b blockBuilder
	b.WriteString(content)
	return b.block()
}

// assertChunksValid applies the invariants every chunker test shares: no chunk
// over the limit, and every chunk tag-balanced. R19's balance check is asserted
// on every chunk in every test rather than on a sample, because the chunker's
// safety rests on it entirely.
func assertChunksValid(t *testing.T, chunks []string, limit int) {
	t.Helper()
	for i, c := range chunks {
		if len(c) > limit {
			t.Errorf("chunk %d is %d bytes, over the limit of %d: %q", i, len(c), limit, c)
		}
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8: %q", i, c)
		}
		assertTagBalanced(t, c)
	}
}

// TestChunk_PacksGreedilyWithinTheLimit covers R17, R20 and R21: while the
// active chunk has room for the next block it takes it, and when it does not a
// new chunk begins.
func TestChunk_PacksGreedilyWithinTheLimit(t *testing.T) {
	// Four blocks of ten bytes each. With the two-byte separator, two blocks fit
	// in 22 bytes and three do not.
	blocks := []Block{
		textBlock("aaaaaaaaaa"),
		textBlock("bbbbbbbbbb"),
		textBlock("cccccccccc"),
		textBlock("dddddddddd"),
	}

	chunks, err := Chunk(blocks, 22)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	want := []string{
		"aaaaaaaaaa\n\nbbbbbbbbbb",
		"cccccccccc\n\ndddddddddd",
	}
	if len(chunks) != len(want) {
		t.Fatalf("chunk count = %d, want %d: %q", len(chunks), len(want), chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
	assertChunksValid(t, chunks, 22)
}

// TestChunk_ExactFitStaysInOneChunk is the boundary R21 turns on: a join that
// lands exactly on the limit still fits.
func TestChunk_ExactFitStaysInOneChunk(t *testing.T) {
	blocks := []Block{textBlock("aaaaaaaaaa"), textBlock("bbbbbbbbbb")}
	joined := Join(blocks)

	chunks, err := Chunk(blocks, len(joined))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunk count = %d, want 1 — an exact fit is a fit: %q", len(chunks), chunks)
	}
	if chunks[0] != joined {
		t.Errorf("chunk = %q, want %q", chunks[0], joined)
	}
	assertChunksValid(t, chunks, len(joined))
}

// TestChunk_OneOverTheLimitSplits is the other side of the same boundary.
func TestChunk_OneOverTheLimitSplits(t *testing.T) {
	blocks := []Block{textBlock("aaaaaaaaaa"), textBlock("bbbbbbbbbb")}
	limit := len(Join(blocks)) - 1

	chunks, err := Chunk(blocks, limit)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2 — one byte over must split: %q", len(chunks), chunks)
	}
	assertChunksValid(t, chunks, limit)
}

// TestChunk_PreservesEverythingInSourceOrder covers R18 and R23 as one property,
// swept across every limit the input admits.
//
// Rejoining the chunks with the block separator must reproduce Join exactly:
// that is simultaneously "no block was reordered", "no block was dropped",
// "nothing was duplicated" and — because the content is multi-byte throughout —
// "no split landed inside a rune". A per-case assertion can pass on the one
// limit it happens to pick; this cannot.
func TestChunk_PreservesEverythingInSourceOrder(t *testing.T) {
	blocks := []Block{
		textBlock("ünïcödé blöck öne"),
		textBlock("日本語のブロック"),
		textBlock("emoji 🎉🎊 block"),
		textBlock("plain ascii block"),
		textBlock("mixed ünï 日本 🎉 block"),
	}
	joined := Join(blocks)

	widest := 0
	for _, b := range blocks {
		if b.Len() > widest {
			widest = b.Len()
		}
	}

	// Below the widest block every limit produces an oversized chunk, which is
	// this phase's documented interim behaviour and a separate case below.
	for limit := widest; limit <= len(joined)+1; limit++ {
		chunks, err := Chunk(blocks, limit)
		if err != nil {
			t.Fatalf("Chunk(limit=%d): %v", limit, err)
		}
		assertChunksValid(t, chunks, limit)
		if got := strings.Join(chunks, BlockSeparator); got != joined {
			t.Fatalf("limit=%d: rejoined chunks = %q, want %q", limit, got, joined)
		}
	}
}

// TestChunk_OversizedBlockIsSplitWithoutLoss covers a block too large to fit on
// its own. It used to go out whole and over-limit, because a chunker packing
// pre-rendered strings had nowhere safe to cut; a block is a token sequence now,
// so the cut is taken inside a text token.
func TestChunk_OversizedBlockIsSplitWithoutLoss(t *testing.T) {
	huge := strings.Repeat("x", 100)
	blocks := []Block{textBlock("before"), textBlock(huge), textBlock("after")}

	const limit = 20
	chunks, err := Chunk(blocks, limit)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertChunksValid(t, chunks, limit)

	// Concatenating strips nothing: these blocks carry no markup, so the joined
	// chunks are exactly the joined blocks with the separators intact.
	if got, want := strings.Join(chunks, ""), Join(blocks); got != want {
		t.Errorf("rejoined chunks = %q, want %q", got, want)
	}
	if n := strings.Count(strings.Join(chunks, ""), "x"); n != 100 {
		t.Errorf("output carries %d of the 100 x's — content was lost or duplicated", n)
	}
}

// TestChunk_OversizedWrappedBlockReopensItsElements is the property the token
// representation exists for: a code block larger than the limit splits into
// several chunks, each a well-formed pre/code block carrying the language
// annotation, and the text content survives exactly.
func TestChunk_OversizedWrappedBlockReopensItsElements(t *testing.T) {
	body := strings.Repeat("fmt.Println(\"x\")\n", 40)
	blocks, err := Render([]byte("```go\n" + body + "```"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}

	const limit = 200
	chunks, err := Chunk(blocks, limit)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("chunk count = %d, want several", len(chunks))
	}
	assertChunksValid(t, chunks, limit)

	var text strings.Builder
	for i, c := range chunks {
		const open = `<pre><code class="language-go">`
		const closed = "</code></pre>"
		if !strings.HasPrefix(c, open) {
			t.Fatalf("chunk %d = %q, want it to reopen the annotated code block", i, c)
		}
		if !strings.HasSuffix(c, closed) {
			t.Fatalf("chunk %d = %q, want it to close the code block", i, c)
		}
		text.WriteString(c[len(open) : len(c)-len(closed)])
	}
	if got := text.String(); got != body {
		t.Errorf("reassembled code = %q, want the original %q", got, body)
	}
}

// TestChunk_OversizedBlockReopensEveryLevelOfDeepNesting extends the reopen
// contract past the two elements a fenced block opens. Reopening walks a stack,
// and a stack of three is the first depth at which order and completeness are
// distinguishable from each other: dropping a level, duplicating one, or
// reopening them inside out all still produce balanced markup, so the balance
// assertion cannot see any of it. Only the exact sequence can.
func TestChunk_OversizedBlockReopensEveryLevelOfDeepNesting(t *testing.T) {
	body := strings.Repeat("fmt.Println(\"x\")\n", 40)
	blocks, err := Render([]byte("> ```go\n" + prefixLines(body, "> ") + "> ```"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	const limit = 200
	chunks, err := Chunk(blocks, limit)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("chunk count = %d, want several", len(chunks))
	}
	assertChunksValid(t, chunks, limit)

	const open = `<blockquote><pre><code class="language-go">`
	const closed = "</code></pre></blockquote>"
	var text strings.Builder
	for i, c := range chunks {
		if !strings.HasPrefix(c, open) {
			t.Fatalf("chunk %d = %q, want every enclosing element reopened in order", i, c)
		}
		if !strings.HasSuffix(c, closed) {
			t.Fatalf("chunk %d = %q, want every enclosing element closed in order", i, c)
		}
		text.WriteString(c[len(open) : len(c)-len(closed)])
	}
	if got := text.String(); got != body {
		t.Errorf("reassembled code = %q, want the original %q", got, body)
	}
}

// prefixLines puts prefix in front of every line of s, so a block can be nested
// inside a blockquote without the source becoming unreadable in the test.
func prefixLines(s, prefix string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestChunk_SplitNeverLandsInsideAnEntity is the escaping counterpart of the
// rune-boundary rule. Text tokens hold escaped text, so a cut at an arbitrary
// byte could bisect "&amp;" and emit a fragment Telegram would show literally.
// Swept across every limit rather than sampled.
func TestChunk_SplitNeverLandsInsideAnEntity(t *testing.T) {
	blocks, err := Render([]byte("a < b & c > d and more < & > text here to push it along"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	joined := Join(blocks)

	for limit := 1; limit <= len(joined)+1; limit++ {
		chunks, err := Chunk(blocks, limit)
		if err != nil {
			t.Fatalf("Chunk(limit=%d): %v", limit, err)
		}
		rejoined := strings.Join(chunks, "")
		if rejoined != joined {
			t.Fatalf("limit=%d: rejoined = %q, want %q", limit, rejoined, joined)
		}
		for i, c := range chunks {
			if strings.Count(c, "&") != strings.Count(c, ";") {
				t.Fatalf("limit=%d chunk %d = %q — an entity was split", limit, i, c)
			}
		}
	}
}

// TestChunk_OversizedBlockTerminatesAtAnyLimit is the non-termination guard. A
// packing loop that cannot place a block is the classic place to spin forever.
func TestChunk_OversizedBlockTerminatesAtAnyLimit(t *testing.T) {
	blocks := []Block{
		textBlock(strings.Repeat("a", 500)),
		textBlock("b"),
		textBlock(strings.Repeat("c", 500)),
	}
	for _, limit := range []int{1, 2, 7, 499, 500, 501, 1200} {
		chunks, err := Chunk(blocks, limit)
		if err != nil {
			t.Fatalf("Chunk(limit=%d): %v", limit, err)
		}
		if len(chunks) == 0 {
			t.Errorf("Chunk(limit=%d) produced no chunks — content was dropped", limit)
		}
		var total int
		for _, c := range chunks {
			total += len(c)
		}
		if total < 1001 {
			t.Errorf("Chunk(limit=%d) emitted %d bytes, want at least the 1001 bytes of content", limit, total)
		}
	}
}

// TestChunk_EveryLimitProducesBalancedChunksAndLosesNothing is the property the
// whole token representation exists to make true, swept exhaustively.
//
// A rich document is chunked at every limit from one byte upward. At each one,
// every chunk must be tag-balanced and use only Telegram's tags, and the text
// content — the message with its markup stripped, which is what a reader
// actually sees — must come back exactly. No sampling: the interesting limits
// are the ones that land a split inside a tag, an entity or a rune, and those
// are precisely the ones a hand-picked case misses.
func TestChunk_EveryLimitProducesBalancedChunksAndLosesNothing(t *testing.T) {
	src := strings.Join([]string{
		"# Heading with a **bold** word",
		"",
		"Prose with *italic*, `code_span`, ~~struck~~, a < b & c, and a",
		"[link](https://example.com/a_b) that wraps across lines.",
		"",
		"> a quotation where 5 < 6 & 7 > 3, plus 日本語 text",
		"",
		"- parent bullet with émphasis",
		"    - child bullet",
		"",
		"| a | b |",
		"|---|---|",
		"| 1 | 2 |",
		"",
		"```go",
		"if a < b { return \"ok\" } // 🎉",
		"```",
	}, "\n")

	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Per block, not one flattened string: whitespace *between* blocks is what a
	// chunk boundary may absorb, while a newline *inside* a block is content and
	// must survive exactly. Flattening first would conflate the two.
	want := make([]string, 0, len(blocks))
	for _, blk := range blocks {
		want = append(want, stripTags(blk.HTML()))
	}

	for limit := 1; limit <= len(Join(blocks))+1; limit++ {
		chunks, err := Chunk(blocks, limit)
		if err != nil {
			t.Fatalf("Chunk(limit=%d): %v", limit, err)
		}
		var text strings.Builder
		for i, c := range chunks {
			if !utf8.ValidString(c) {
				t.Fatalf("limit=%d chunk %d is not valid UTF-8: %q", limit, i, c)
			}
			// Balance is asserted on every chunk at every limit: it is the
			// invariant the adapter cannot recover from if it is ever violated.
			assertTagBalanced(t, c)
			text.WriteString(stripTags(c))
		}
		if err := matchBlockTexts(text.String(), want); err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
	}
}

// matchBlockTexts checks that got is exactly the blocks' texts in order,
// separated by nothing but newlines.
//
// A chunk boundary landing between two blocks absorbs the separator — the two
// become separate Telegram messages, already visually apart, and a trailing
// blank line would only be trimmed — so the run of newlines between blocks is
// free. Everything inside a block is compared byte for byte, which is what keeps
// a dropped soft line break or a bisected entity visible.
func matchBlockTexts(got string, want []string) error {
	pos := 0
	for i, w := range want {
		for pos < len(got) && got[pos] == '\n' {
			pos++
		}
		if !strings.HasPrefix(got[pos:], w) {
			return fmt.Errorf("at block %d: got %q, want it to continue with %q", i, got[pos:], w)
		}
		pos += len(w)
	}
	if rest := strings.TrimLeft(got[pos:], "\n"); rest != "" {
		return fmt.Errorf("trailing content after the last block: %q", rest)
	}
	return nil
}

// stripTags removes every element, leaving the text a reader sees. It relies on
// the renderer escaping every literal '<' in text content, which the R3 cases
// pin independently.
func stripTags(html string) string {
	var sb strings.Builder
	for {
		open := strings.IndexByte(html, '<')
		if open < 0 {
			sb.WriteString(html)
			return sb.String()
		}
		sb.WriteString(html[:open])
		closeAt := strings.IndexByte(html[open:], '>')
		if closeAt < 0 {
			// An unterminated tag is a balance failure the caller asserts on;
			// keep the remainder so the mismatch is visible rather than hidden.
			sb.WriteString(html[open:])
			return sb.String()
		}
		html = html[open+closeAt+1:]
	}
}

// TestChunk_EmptyInputProducesNoChunks keeps the adapter from sending an empty
// message for a reply that rendered to nothing.
func TestChunk_EmptyInputProducesNoChunks(t *testing.T) {
	chunks, err := Chunk(nil, 100)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("chunks = %q, want none", chunks)
	}
}

// TestChunk_RejectsANonPositiveLimit is the total-function guard: a limit that
// nothing can fit into is a caller bug, not something to loop over.
func TestChunk_RejectsANonPositiveLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := Chunk([]Block{textBlock("x")}, limit); err == nil {
			t.Errorf("Chunk(limit=%d) returned no error", limit)
		}
	}
}

// TestChunk_NestedTagUsesTheWholeBudget extends R21's exact-fit boundary to a
// tag opened while another element is already open. The budget is the emitted
// length plus the cost of closing what is open; opening one more element adds
// its own closer to that cost and nothing else. Counting the enclosing closers a
// second time reserves room the chunk has already reserved, and the block breaks
// apart on a limit it fits inside.
func TestChunk_NestedTagUsesTheWholeBudget(t *testing.T) {
	blocks, err := Render([]byte("> aaa **b**"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	joined := Join(blocks)

	chunks, err := Chunk(blocks, len(joined))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunk count = %d, want 1 — the block fits exactly: %q", len(chunks), chunks)
	}
	if chunks[0] != joined {
		t.Errorf("chunk = %q, want %q", chunks[0], joined)
	}
	assertChunksValid(t, chunks, len(joined))
}

// TestChunk_EveryChunkCarriesText is the precondition R19's balance check does
// not cover: a chunk becomes a Telegram message, and Telegram rejects one whose
// text is empty however well-formed its markup is.
//
// An element whose opening tag alone overruns the limit is the case that
// produces one — the tag is written because a fresh chunk has nowhere else to
// put it, and if the chunk then wraps before any text arrives, what it emits is
// a pair of tags around nothing. Reopened tags are already treated as not being
// content; a tag opened for the first time is no more content than a reopened
// one.
func TestChunk_EveryChunkCarriesText(t *testing.T) {
	// The anchor's opening tag is 28 bytes on its own, so no chunk can hold it
	// and the label together within the limit.
	blocks, err := Render([]byte("[a](https://e.com) [b](https://e.com)"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	chunks, err := Chunk(blocks, 20)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks — the content must survive")
	}
	for i, c := range chunks {
		if strings.TrimSpace(stripTags(c)) == "" {
			t.Errorf("chunk %d of %d carries no text: %q", i, len(chunks), c)
		}
		assertTagBalanced(t, c)
	}
	if got := stripTags(strings.Join(chunks, "")); !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("chunk text = %q, want both labels", got)
	}
}

// realisticChunkLimit is the adapter's own headroom constant. The table cases
// below are about what happens at a real message size, not at a synthetic one.
const realisticChunkLimit = 3500

// TestChunk_TallTableSplitsBetweenRowsAndKeepsItsAlignment covers Phase 6's
// table watchpoint. A degraded table is a pre-wrapped block like a fenced code
// block, so it became splittable the moment oversized blocks did — and a split
// that landed mid-row would leave every following cell shifted.
//
// It does not: tabwriter pads every row to the same widths over the whole
// table, and the chunker prefers a line break as its cut point, so each chunk
// holds whole rows that still line up. The header row rides on the first chunk
// only; there is nothing to repeat it with, and inventing one would be text the
// author never wrote.
func TestChunk_TallTableSplitsBetweenRowsAndKeepsItsAlignment(t *testing.T) {
	var src strings.Builder
	src.WriteString("| id | description | owner |\n|---|---|---|\n")
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&src, "| %d | a reasonably long description cell number %d | owner-%d |\n", i, i, i)
	}

	blocks, err := Render([]byte(src.String()))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	chunks, err := Chunk(blocks, realisticChunkLimit)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunk count = %d, want several — the table must exceed one chunk", len(chunks))
	}
	assertChunksValid(t, chunks, realisticChunkLimit)

	// Every row of every chunk agrees with the header row's column offsets.
	want := columnOffsets(strings.Split(preContent(t, chunks[0]), "\n")[0])
	for i, c := range chunks {
		lines := strings.Split(preContent(t, c), "\n")
		assertColumnsAligned(t, fmt.Sprintf("chunk %d line", i+1), lines, want)
	}
}

// TestChunk_RowWiderThanTheLimitWrapsAndLosesItsAlignment records the case the
// one above cannot cover: a single row too wide for a chunk has no row boundary
// to break at, so it wraps mid-row and its columns do not line up across the
// boundary.
//
// That is the accepted outcome, not a defect to fix here. The alternative is
// dropping cells, and losing content is the failure class this work exists to
// remove — a wrapped row is ugly and complete, which beats tidy and truncated.
func TestChunk_RowWiderThanTheLimitWrapsAndLosesItsAlignment(t *testing.T) {
	var hdr, rule, row strings.Builder
	for c := 1; c <= 200; c++ {
		fmt.Fprintf(&hdr, "| h%03d ", c)
		rule.WriteString("|---")
		fmt.Fprintf(&row, "| body-value-%03d-padded-out-wide ", c)
	}
	src := hdr.String() + "|\n" + rule.String() + "|\n" + row.String() + "|\n"

	blocks, err := Render([]byte(src))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	original := preContent(t, blocks[0].HTML())

	chunks, err := Chunk(blocks, realisticChunkLimit)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunk count = %d, want several", len(chunks))
	}
	assertChunksValid(t, chunks, realisticChunkLimit)

	// A chunk continues a row when the one before it did not end at a line
	// break. Stated that way rather than by matching a cell from the fixture,
	// so renaming a cell cannot silently change what is being asserted.
	continued := slices.ContainsFunc(chunks[:len(chunks)-1], func(c string) bool {
		return !strings.HasSuffix(preContent(t, c), "\n")
	})
	if !continued {
		t.Fatal("no chunk continued a row — this test no longer covers the case it documents")
	}

	// The alignment loss is the point: a continuation chunk opens part-way
	// through a row, so its first line does not carry the full column set.
	want := len(columnOffsets(strings.Split(original, "\n")[0]))
	misaligned := slices.ContainsFunc(chunks[1:], func(c string) bool {
		first := strings.Split(preContent(t, c), "\n")[0]
		return len(columnOffsets(first)) != want
	})
	if !misaligned {
		t.Errorf("every chunk kept all %d columns — the wrap did not land mid-row after all", want)
	}

	// Nothing is lost, which is the property that actually matters.
	var reassembled strings.Builder
	for _, c := range chunks {
		reassembled.WriteString(preContent(t, c))
	}
	tghtmltest.AssertSameText(t, original, reassembled.String())
}

// TestChunk_BlockOfOnlyWhitespaceStaysWithinTheLimit guards the whitespace
// hold-back against the case it was not written for.
//
// Whitespace arriving before a chunk has content is held back, so a chunk is
// never nothing but spaces. Inside a fenced block that reasoning inverts: blank
// lines are the content, there is no non-whitespace text coming to release
// them, and holding them back let the held run grow without bound until it was
// appended to a finished chunk — outside its closing tags and far over the
// limit. Telegram rejects the result and the reply is lost.
func TestChunk_BlockOfOnlyWhitespaceStaysWithinTheLimit(t *testing.T) {
	const blankLines = 9000
	blocks, err := Render([]byte("```\n" + strings.Repeat("\n", blankLines) + "```\n"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	const limit = 3500
	chunks, err := Chunk(blocks, limit)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	assertChunksValid(t, chunks, limit)

	// Every chunk is a code block in its own right — nothing lands outside the
	// tags.
	for i, c := range chunks {
		if !strings.HasPrefix(c, "<pre>") || !strings.HasSuffix(c, "</pre>") {
			t.Errorf("chunk %d is not a whole pre block: starts %.20q, ends %q",
				i+1, c, c[max(len(c)-20, 0):])
		}
	}

	var newlines int
	for _, c := range chunks {
		newlines += strings.Count(preContent(t, c), "\n")
	}
	if newlines != blankLines {
		t.Errorf("newline count = %d, want %d — blank lines are content and must survive", newlines, blankLines)
	}
}
