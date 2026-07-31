package telegold

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Chunk packs blocks into ordered chunk strings, each no longer than limit and
// each independently tag-balanced.
//
// Packing is greedy: while the active chunk has room for the next block it takes
// it, and when appending would exceed the limit a new chunk begins. Source order
// is preserved — a reordered reply is worse than an unchunked one.
//
// A block too large to fit on its own is split. Because a block is a token
// sequence rather than a string, the split is taken inside a text token and the
// elements enclosing it are closed at the end of one chunk and reopened at the
// start of the next. Tag balance is therefore a property of how a chunk is
// built, not something checked afterwards: a chunk always ends by closing
// whatever is open and begins by reopening it.
//
// limit is a parameter and never Telegram's 4096: the adapter owns that number
// and applies headroom to it. Length is the byte length of the raw HTML, which
// over-counts against Telegram's own limit (that one counts the entity-stripped
// text in UTF-16 code units). Over-counting is conservative — it costs an
// occasional extra chunk and needs no second parser.
func Chunk(blocks []Block, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("tghtml: chunk limit must be positive, got %d", limit)
	}

	c := chunker{limit: limit}
	for _, b := range blocks {
		c.addBlock(b)
	}
	c.flush()
	c.drainPending()
	return c.chunks, nil
}

// ChunkPlain splits unescaped, unmarkup text into ordered pieces of at most
// limit characters each, cutting at a line or word break where one is close
// enough and never inside a rune.
//
// It exists for the adapter's plain-text fallback, which sends the original
// markdown with no parse mode. That text is not HTML, so it needs the size and
// rune-boundary discipline of Chunk without any of its tag handling — and a
// fallback that cannot deliver a long reply is not a fallback, it is a silent
// loss of the whole message.
//
// The limit is in characters, not bytes. Chunk counts bytes because it is
// measuring HTML, where over-counting against Telegram's entity-stripped
// measure is conservative and cheap. Here the text goes out as-is, so bytes
// would simply be wrong: a Japanese reply runs three bytes to the character and
// would be split at a third of the length that actually fits.
func ChunkPlain(text string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("tghtml: chunk limit must be positive, got %d", limit)
	}
	var out []string
	for text != "" {
		if utf8.RuneCountInString(text) <= limit {
			out = append(out, text)
			break
		}
		cut := preferBreak(text, byteOffsetOfRune(text, limit))
		out = append(out, text[:cut])
		text = text[cut:]
	}
	return out, nil
}

// byteOffsetOfRune returns the byte index just past the nth rune of s, which
// callers reach only when s is known to hold more than n runes.
func byteOffsetOfRune(s string, n int) int {
	at := 0
	for range n {
		_, size := utf8.DecodeRuneInString(s[at:])
		at += size
	}
	return at
}

// chunker accumulates tokens into size-bounded, tag-balanced chunks.
type chunker struct {
	limit  int
	chunks []string

	cur   strings.Builder
	open  []token // elements currently open, outermost first
	tail  string  // trailing run of newlines in cur, for separator collapsing
	empty bool    // true until cur holds text that is not merely whitespace
	// pending is whitespace held back because the chunk has no content yet. It
	// is written out as soon as content arrives, and otherwise survives the
	// flush to lead the next chunk — so a chunk is never nothing but spaces and
	// no character is dropped on the way.
	pending string
}

// addBlock appends one block, starting a new chunk in preference to splitting
// the block when the block would fit in a chunk of its own.
func (c *chunker) addBlock(b Block) {
	size := b.Len()
	if size == 0 {
		return
	}

	if !c.empty && c.cur.Len() > 0 {
		sep := c.separator(BlockSeparator)
		// Break between blocks rather than inside one whenever the block could
		// start a chunk of its own — a split mid-paragraph is a worse read than
		// a message boundary at a paragraph break.
		if c.used()+len(sep)+size > c.limit && size <= c.limit {
			c.flush()
		} else {
			c.writeText(sep)
		}
	}

	for _, t := range b.tokens {
		c.add(t)
	}
}

// add appends one token, wrapping to a new chunk or splitting the token's text
// when it does not fit.
func (c *chunker) add(t token) {
	if !t.isText() {
		// Only an opening tag can force a wrap. A closing tag is budget-neutral,
		// and deferring one to the next chunk would strand its element open at the
		// end of this chunk and reopen it in the next with nothing between the
		// reopened tag and the close — an element wrapped around nothing.
		if t.open && c.used()+admitCost(t) > c.limit && c.hasContent() {
			c.wrap()
		}
		c.writeTag(t)
		return
	}

	text := t.text
	for text != "" {
		room := c.limit - c.used()
		cut := textCut(text, room)
		if cut == 0 {
			if !c.hasContent() {
				// A fresh chunk cannot hold even the smallest indivisible piece
				// once the reopened elements are paid for. Emitting nothing would
				// loop forever, and emitting part of a rune or part of an entity
				// is not a thing that can be sent, so the chunk goes over the
				// limit rather than the content being lost.
				cut = minCut(text)
			} else {
				c.wrap()
				continue
			}
		}
		c.writeText(text[:cut])
		text = text[cut:]
		if text != "" {
			c.wrap()
		}
	}
}

// used is the emitted length of the active chunk plus what it will cost to close
// the elements currently open. A chunk is only ever completed by closing them,
// so that cost is part of the budget from the moment they are opened.
func (c *chunker) used() int { return c.cur.Len() + len(c.pending) + c.closeCost() }

// closeCost is the cost of closing everything currently open.
func (c *chunker) closeCost() int {
	n := 0
	for _, t := range c.open {
		n += t.closer().size()
	}
	return n
}

// admitCost is what admitting t adds to the budget used() reports.
//
// An opening tag costs its own bytes plus the closer it obliges. A closing tag
// costs nothing: used() has reserved its bytes since the element was opened, and
// writing it hands those same bytes back. The elements already open are likewise
// carried by used(), so counting them again here would reserve their closers
// twice and wrap a chunk that still has room.
func admitCost(t token) int {
	switch {
	case t.isText():
		return len(t.text)
	case t.open:
		return t.size() + t.closer().size()
	default:
		return 0
	}
}

// hasContent reports whether the active chunk holds anything beyond the elements
// reopened at its start. Wrapping an empty chunk would emit an empty message and
// make no progress.
func (c *chunker) hasContent() bool { return !c.empty }

func (c *chunker) writeText(s string) {
	if s == "" {
		return
	}
	if c.empty && len(c.open) == 0 && strings.TrimSpace(s) == "" {
		// Leading whitespace of a chunk that has nothing in it yet. Held back
		// rather than written: if the chunk never gains content, this is all it
		// would carry, and Telegram rejects a message whose text is empty.
		//
		// Only while nothing is open, which is to say only between blocks. Inside
		// an element the whitespace is the content — the blank lines of a fenced
		// block are the code — and there is no non-whitespace text coming to
		// release it. Held back there, the run grows past the limit unchecked and
		// is finally appended outside the closing tags, which loses the whole
		// reply to a message Telegram will not accept.
		c.pending += s
	} else {
		c.cur.WriteString(c.pending)
		c.pending = ""
		c.cur.WriteString(s)
		c.empty = false
	}
	if trimmed := strings.TrimRight(s, "\n"); trimmed == "" {
		c.tail += s
	} else {
		c.tail = s[len(trimmed):]
	}
}

func (c *chunker) writeTag(t token) {
	c.cur.WriteString(t.emitted())
	c.tail = ""
	// A tag is markup, not content — the same reasoning wrap() applies to the
	// tags it reopens. An element whose opening tag alone overruns the limit is
	// written anyway, because a fresh chunk has nowhere else to put it; if the
	// chunk then wraps before any text arrives, treating the tag as content would
	// emit a pair of tags around nothing.
	if t.open {
		c.open = append(c.open, t)
		return
	}
	if k := len(c.open) - 1; k >= 0 {
		c.open = c.open[:k]
	}
}

// separator returns the part of sep the active chunk does not already end with,
// matching how blocks are joined when they are not chunked.
func (c *chunker) separator(sep string) string {
	trailing := len(c.tail)
	if trailing > len(sep) {
		trailing = len(sep)
	}
	return sep[trailing:]
}

// wrap ends the active chunk and begins the next one with the same elements
// open, so content that spans a boundary stays inside its formatting.
//
// The stack is cloned because flush truncates c.open to length zero without
// releasing its array. Ranging over the original while appending to the
// truncated slice would read and write one array, which is correct only while
// the loop appends exactly once per iteration and so writes each slot the value
// it just read. Nothing signals that arity is load-bearing, and the failure it
// guards against is invisible: a corrupted reopen stack still closes whatever
// it holds, so the chunk stays balanced and only the formatting is wrong.
func (c *chunker) wrap() {
	reopen := slices.Clone(c.open)
	c.flush()
	for _, t := range reopen {
		c.cur.WriteString(t.emitted())
		c.open = append(c.open, t)
	}
	// The reopened tags are not content: a chunk holding only them is still
	// empty, and wrapping again must not emit it.
	c.empty = true
	c.tail = ""
}

// drainPending appends whitespace still held back once there is no next chunk
// for it to lead. Trailing whitespace of the whole reply is inert to Telegram,
// which trims it, but inside a fenced block a final newline is content and
// dropping it would silently shorten the code. With no chunk to append it to
// there is nothing to preserve it in, and a chunk of nothing but whitespace is
// exactly what must not be sent.
func (c *chunker) drainPending() {
	if c.pending == "" || len(c.chunks) == 0 {
		return
	}
	c.chunks[len(c.chunks)-1] += c.pending
	c.pending = ""
}

// flush emits the active chunk, closing everything still open.
func (c *chunker) flush() {
	for i := len(c.open) - 1; i >= 0; i-- {
		c.cur.WriteString(c.open[i].closer().emitted())
	}
	c.open = c.open[:0]
	if !c.empty && c.cur.Len() > 0 {
		c.chunks = append(c.chunks, c.cur.String())
	}
	c.cur.Reset()
	c.cur.Grow(c.limit)
	c.empty = true
	c.tail = ""
}
