// Portions of this file are derived from telegold by Leonid Shevtsov,
// https://github.com/leonid-shevtsov/telegold (commit 36dc899, MIT licence).
// See the LICENSE file in this directory for the upstream copyright notice.

// Package tghtml renders CommonMark to the HTML subset accepted by Telegram's
// HTML parse mode, as an ordered sequence of independently tag-balanced blocks.
//
// The node-handling structure and the choice of Telegram tag for each markdown
// construct are derived from telegold by Leonid Shevtsov
// (https://github.com/leonid-shevtsov/telegold, commit 36dc899, MIT licence —
// see the LICENSE file in this directory). Upstream renders to a single flat
// string via goldmark's renderer interface; this package walks the AST itself so
// it can emit blocks, which is what lets the chunker split long replies at safe
// boundaries. Upstream's known defects are documented against the handlers that
// carry them.
package telegold

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// mdParser parses CommonMark plus the individual extensions this renderer
// needs.
//
// Extensions are selected one at a time rather than through extension.GFM.
// GFM bundles linkify, which wraps bare URLs in anchors — including the
// underscore-bearing URLs of the defect this package exists to fix. Today those
// travel as plain text and Telegram autolinks them after parsing, which is the
// behaviour the regression test pins.
var mdParser = goldmark.New(
	goldmark.WithExtensions(extension.Strikethrough, extension.Table),
).Parser()

// Render parses src as CommonMark and returns its Telegram-HTML blocks in
// source order. One top-level markdown node produces at most one block, so a
// block quotation containing a list is a single block rather than several.
//
// Render returns an error only for constructs Telegram's HTML subset cannot
// represent at all — see UnsupportedError.
func Render(src []byte) ([]Block, error) {
	doc := mdParser.Parse(text.NewReader(src))

	var blocks []Block
	for node := doc.FirstChild(); node != nil; node = node.NextSibling() {
		block, ok, err := renderBlockNode(src, node)
		if err != nil {
			return nil, err
		}
		if ok {
			blocks = append(blocks, block)
		}
	}
	return blocks, nil
}

// thematicBreakText is the separator emitted for a thematic break.
//
// Telegram's subset has no hr element, so the separator must be text. Upstream
// wrote "<hr" and then "* * *", producing the unterminated "<hr* * *" that
// Telegram rejects outright; the opening tag is removed rather than repaired.
//
// Em dashes rather than upstream's "* * *": asterisks read as unrendered markdown
// source, whereas a run of em dashes abuts into a continuous line. Contains no
// character that needs escaping.
const thematicBreakText = "——————————"

// renderBlockNode renders one top-level node. The bool reports whether the node
// produced a block at all — a node whose content is empty produces none.
func renderBlockNode(src []byte, node ast.Node) (Block, bool, error) {
	if block, ok := wrappedBlockFor(src, node); ok {
		return block, block.Len() > 0, nil
	}

	switch n := node.(type) {
	case *ast.Blockquote:
		var inner blockBuilder
		if err := writeChildBlocks(&inner, src, n, BlockSeparator); err != nil {
			return Block{}, false, err
		}
		if inner.Len() == 0 {
			return Block{}, false, nil
		}
		var sb blockBuilder
		sb.open("blockquote", "")
		sb.appendTokens(inner.tokens)
		sb.close("blockquote")
		return sb.block(), true, nil
	}

	var sb blockBuilder
	if err := writeBlockContent(&sb, src, node); err != nil {
		return Block{}, false, err
	}
	if sb.Len() == 0 {
		return Block{}, false, nil
	}
	return sb.block(), true, nil
}

// wrappedBlockFor returns the Block for the node kinds whose content is enclosed
// in elements — the ones a split has to close at the end of one chunk and reopen
// at the start of the next.
//
// Both positions a block can appear in need it, which is why it is one function
// rather than a case arm in each switch: at top level the Block is the block,
// and nested inside a list item or a quotation its tokens are spliced into the
// enclosing block.
func wrappedBlockFor(src []byte, node ast.Node) (Block, bool) {
	switch n := node.(type) {
	case *ast.FencedCodeBlock:
		return codeBlock(src, n, n.Language(src)), true
	case *ast.CodeBlock:
		return codeBlock(src, n, nil), true
	case *east.Table:
		return renderTable(src, n), true
	}
	return Block{}, false
}

// codeBlock builds a pre-wrapped code block. Telegram's subset nests code inside
// pre, in that order.
func codeBlock(src []byte, n ast.Node, language []byte) Block {
	var attr string
	if len(language) > 0 {
		attr = ` class="language-` + escapeAttr(language) + `"`
	}
	var sb blockBuilder
	sb.open("pre", "")
	sb.open("code", attr)
	sb.WriteString(blockLines(src, n))
	sb.close("code")
	sb.close("pre")
	return sb.block()
}

// blockLines renders a block's raw source lines as escaped text. Code content is
// escaped as text content: a < inside a fence is content, never markup.
func blockLines(src []byte, n ast.Node) string {
	var sb strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		sb.WriteString(escapeText(line.Value(src), true))
	}
	return sb.String()
}

// writeBlockContent renders a block-level node's content, without wrappers.
// It recurses for constructs that contain other blocks, so a quotation holding a
// list stays one block.
func writeBlockContent(sb *blockBuilder, src []byte, node ast.Node) error {
	// A wrapper-bearing kind nested inside another block puts its markup in the
	// enclosing block's content rather than becoming a wrapper of its own.
	if block, ok := wrappedBlockFor(src, node); ok {
		sb.appendTokens(block.tokens)
		return nil
	}

	switch n := node.(type) {
	case *ast.Heading:
		// Telegram has no heading element and no way to carry a level, so every
		// level renders bold. The trailing newline is part of the requirement:
		// without it a heading runs into the following sentence wherever the
		// surrounding joiner is not itself a line break. Join collapses its
		// separator against this newline so the gap stays one blank line.
		sb.open("b", "")
		if err := renderInlineChildren(sb, src, n); err != nil {
			return err
		}
		sb.close("b")
		sb.WriteString("\n")
		return nil

	case *ast.ThematicBreak:
		sb.WriteString(thematicBreakText)
		return nil

	case *ast.HTMLBlock:
		return ErrRawHTML

	case *ast.Blockquote:
		// A quotation reached here is nested inside another block — another
		// quotation, or a list item. Telegram represents a quotation as a
		// message entity spanning a text range, and such an entity cannot
		// contain another of its own kind, so the nesting is flattened to the
		// enclosing level rather than emitting markup Telegram would reject.
		// Whether a reader can tell the levels apart is an appearance question.
		return writeChildBlocks(sb, src, n, BlockSeparator)

	case *ast.List:
		return writeList(sb, src, n, 0)

	case *ast.ListItem:
		// Reached only if an item appears outside a list, which the parser does
		// not produce. Rendered without a marker rather than dropped.
		return writeChildBlocks(sb, src, n, "\n")
	}

	// Paragraph, TextBlock and anything else block-shaped: inline content.
	return renderInlineChildren(sb, src, node)
}

// listIndent is one level of nested-list indentation.
//
// Telegram's HTML subset has no list elements, so nesting has to be shown with
// text. Ordinary spaces are safe because Telegram's HTML parse mode is not an
// HTML renderer: the tags become message entities over the message text, and the
// text — whitespace included — is kept as written. The documented blockquote
// example relies on the same property by putting literal newlines inside the
// element, where a real HTML renderer would collapse them to spaces.
const listIndent = "  "

// maxListIndentDepth caps how deep indentation grows. Beyond this, further
// nesting reuses the deepest indent rather than marching a list off the side of a
// narrow screen.
const maxListIndentDepth = 6

// writeList renders a list, one item per line, each prefixed with its indent and
// marker.
//
// Upstream wrote a constant "- " for every item at every depth and never
// inspected ast.List.IsOrdered or Start, so ordered lists became bullets and
// nested lists flattened. Both come from the same place and are fixed together.
func writeList(sb *blockBuilder, src []byte, list *ast.List, depth int) error {
	ordinal := list.Start
	indent := listIndentFor(depth)

	first := true
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		var inner blockBuilder
		if err := writeListItem(&inner, src, item, depth); err != nil {
			return err
		}
		if inner.Len() == 0 {
			continue
		}
		if !first {
			sb.writeSeparator("\n")
		}
		sb.WriteString(indent)
		if list.IsOrdered() {
			// Start is the list's own first ordinal, so a list beginning at 5 is
			// honoured rather than renumbered from 1.
			sb.WriteString(strconv.Itoa(ordinal))
			sb.WriteString(". ")
			ordinal++
		} else {
			sb.WriteString("- ")
		}
		sb.appendTokens(inner.tokens)
		first = false
	}
	return nil
}

// writeListItem renders one item's content. A nested list becomes its own set of
// lines, indented one level deeper; everything else is the item's own text.
func writeListItem(sb *blockBuilder, src []byte, item ast.Node, depth int) error {
	var text blockBuilder
	var nested []blockBuilder

	for child := item.FirstChild(); child != nil; child = child.NextSibling() {
		if list, ok := child.(*ast.List); ok {
			var sub blockBuilder
			if err := writeList(&sub, src, list, depth+1); err != nil {
				return err
			}
			if sub.Len() > 0 {
				nested = append(nested, sub)
			}
			continue
		}
		var part blockBuilder
		if err := writeBlockContent(&part, src, child); err != nil {
			return err
		}
		if part.Len() == 0 {
			continue
		}
		if text.Len() > 0 {
			text.writeSeparator("\n")
		}
		text.appendTokens(part.tokens)
	}

	sb.appendTokens(text.tokens)
	for _, n := range nested {
		sb.writeSeparator("\n")
		sb.appendTokens(n.tokens)
	}
	return nil
}

// listIndentFor returns the indent for a nesting depth, capped.
func listIndentFor(depth int) string {
	if depth > maxListIndentDepth {
		depth = maxListIndentDepth
	}
	return strings.Repeat(listIndent, depth)
}

// writeChildBlocks renders a node's block-level children, joined by sep, and
// skips children that render empty so the separator never doubles.
func writeChildBlocks(sb *blockBuilder, src []byte, node ast.Node, sep string) error {
	first := true
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		var inner blockBuilder
		if err := writeBlockContent(&inner, src, child); err != nil {
			return err
		}
		if inner.Len() == 0 {
			continue
		}
		if !first {
			sb.writeSeparator(sep)
		}
		sb.appendTokens(inner.tokens)
		first = false
	}
	return nil
}

// renderInlineChildren renders a node's inline descendants into sb.
func renderInlineChildren(sb *blockBuilder, src []byte, node ast.Node) error {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if err := renderInline(sb, src, child); err != nil {
			return err
		}
	}
	return nil
}

// renderInline renders a single inline node.
func renderInline(sb *blockBuilder, src []byte, node ast.Node) error {
	switch n := node.(type) {
	case *ast.Text:
		sb.WriteString(textContent(src, n))
		// A text node carries a flag for the break that follows it. Upstream
		// inspected neither, so the last word of a wrapped source line fused to
		// the first word of the next — measured as "alpha betagamma delta".
		// Telegram's HTML parse mode does not collapse whitespace: tags become
		// message entities over the text, so a newline character is a newline.
		if n.HardLineBreak() || n.SoftLineBreak() {
			sb.WriteString("\n")
		}
		return nil
	case *ast.Emphasis:
		return renderEmphasis(sb, src, n)
	case *east.Strikethrough:
		// Telegram's subset spells strikethrough <s>; it has no <del>.
		sb.open("s", "")
		if err := renderInlineChildren(sb, src, n); err != nil {
			return err
		}
		sb.close("s")
		return nil
	case *ast.CodeSpan:
		renderCodeSpan(sb, src, n)
		return nil
	case *ast.String:
		sb.WriteString(stringContent(n))
		return nil
	case *ast.Link:
		return renderLink(sb, src, n)
	case *ast.AutoLink:
		return renderAutoLink(sb, src, n)
	case *ast.Image:
		return renderImage(sb, src, n)
	case *ast.RawHTML:
		return ErrRawHTML
	}
	return renderInlineChildren(sb, src, node)
}

// renderLink emits an anchor when the destination's scheme is allowlisted, and
// the label alone otherwise.
//
// Dropping the anchor entirely is a correction of upstream, which kept the
// anchor and blanked its href. An empty-href anchor is also a plausible Telegram
// rejection, which would route the whole message to the plain-text fallback.
func renderLink(sb *blockBuilder, src []byte, n *ast.Link) error {
	var label blockBuilder
	if err := renderInlineChildren(&label, src, n); err != nil {
		return err
	}
	writeAnchor(sb, n.Destination, label.tokens)
	return nil
}

// writeAnchor emits an anchor when dest's scheme is allowlisted, and the label
// alone otherwise.
//
// Every construct that can carry a destination — link, autolink, image — goes
// through here. The allowlist is the security-relevant decision in this package
// and upstream's autolink handler bypassed its own copy of the check entirely,
// so there is exactly one copy of it on the emitting side.
// The label is a token sequence, not a string: a link's text can carry its own
// emphasis, and flattening it here would hide that markup from the chunker.
func writeAnchor(sb *blockBuilder, dest []byte, label []token) {
	if !schemeAllowed(dest) {
		// Label only. The destination is not emitted at all, in any form:
		// Telegram autolinks visible text after parsing, so leaving a rejected
		// destination in the text could hand it back a live link.
		sb.appendTokens(label)
		return
	}
	sb.open("a", ` href="`+escapeAttr(dest)+`"`)
	sb.appendTokens(label)
	sb.close("a")
}

// renderImage degrades an image to an anchor labelled with its alternative
// text. Telegram's HTML subset cannot embed an image in a text message, and
// upstream failed closed on one — which for arbitrary LLM output would route
// ordinary replies to the unstyled plain-text fallback.
//
// The destination goes through the same allowlist as a link: an image is a link
// as far as Telegram is concerned once it is degraded, so R16 applies unchanged.
// An image with neither an allowlisted destination nor alternative text emits
// nothing: the destination is exactly what must not be shown, and a placeholder
// would be text the author never wrote.
func renderImage(sb *blockBuilder, src []byte, n *ast.Image) error {
	var label blockBuilder
	if err := renderInlineChildren(&label, src, n); err != nil {
		return err
	}

	if label.Len() == 0 && schemeAllowed(n.Destination) {
		// No alternative text to label the anchor with. The destination is
		// allowlisted, so showing it is safe and is more useful than an empty
		// anchor, which Telegram would render as nothing at all.
		label.WriteString(escapeText(n.Destination, true))
	}
	writeAnchor(sb, n.Destination, label.tokens)
	return nil
}

// renderAutoLink emits an anchor for a bracketed autolink. Upstream wrote the
// href unconditionally, bypassing its scheme check entirely; this routes through
// the same allowlist as an ordinary link.
// A rejected autolink keeps its label, which is also its destination — there is
// nothing else to fall back to, and it is no more reachable than the same string
// written as ordinary prose.
func renderAutoLink(sb *blockBuilder, src []byte, n *ast.AutoLink) error {
	dest := n.URL(src)
	if n.AutoLinkType == ast.AutoLinkEmail {
		dest = emailDestination(dest)
	}
	var label blockBuilder
	label.WriteString(escapeText(n.Label(src), true))
	writeAnchor(sb, dest, label.tokens)
	return nil
}

// renderCodeSpan emits Telegram's inline <code> element.
//
// Unlike upstream, the children are not type-asserted to *ast.Text. A code span
// can hold other node kinds, and an unchecked assertion would panic inside the
// adapter's send path on user-supplied content.
func renderCodeSpan(sb *blockBuilder, src []byte, n *ast.CodeSpan) {
	sb.open("code", "")
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch child := c.(type) {
		case *ast.Text:
			value := child.Segment.Value(src)
			// A span that wraps across a source line ends the segment with a
			// newline; CommonMark renders that as a single space.
			if bytes.HasSuffix(value, []byte("\n")) {
				sb.WriteString(escapeText(value[:len(value)-1], true))
				sb.WriteString(" ")
				continue
			}
			sb.WriteString(escapeText(value, true))
		case *ast.String:
			sb.WriteString(stringContent(child))
		default:
			// Unknown inline kind inside a code span: emit its text content
			// rather than dropping it or panicking.
			_ = renderInlineChildren(sb, src, c)
		}
	}
	sb.close("code")
}

// stringContent renders an ast.String, which carries its bytes inline rather
// than referencing a source segment.
func stringContent(n *ast.String) string {
	if n.IsCode() {
		return string(n.Value)
	}
	return escapeText(n.Value, n.IsRaw())
}

// renderEmphasis wraps its children in Telegram's italic or bold element.
// Telegram's subset has no <em>/<strong>, so level 1 is <i> and level 2 is <b>.
func renderEmphasis(sb *blockBuilder, src []byte, n *ast.Emphasis) error {
	tag := "i"
	if n.Level == 2 {
		tag = "b"
	}
	sb.open(tag, "")
	if err := renderInlineChildren(sb, src, n); err != nil {
		return err
	}
	sb.close(tag)
	return nil
}

// textContent renders a text node's segment as Telegram-safe HTML text.
func textContent(src []byte, n *ast.Text) string {
	value := n.Segment.Value(src)
	if n.IsRaw() {
		return escapeText(value, true)
	}
	return escapeText(value, false)
}

// escapeGrowth is the headroom escapeText's buffer gets over its input, for the
// few bytes an escape adds. Overflowing it costs a flush, not a wrong answer.
const escapeGrowth = 16

// escapeText converts markdown text content to Telegram-safe HTML text.
//
// Telegram documents exactly three characters as needing escapes in HTML parse
// mode: <, > and &. goldmark's escaper also emits &quot; for a double quote,
// which Telegram does not document as a supported entity, so the quote is
// restored to its literal form. A literal " in text content is unambiguous to
// any parser; a named entity depends on the parser's entity table.
//
// raw selects goldmark's RawWrite, which escapes without resolving character
// references or unescaping backslash-escaped punctuation.
func escapeText(value []byte, raw bool) string {
	var buf bytes.Buffer
	// Sized to the segment, not to bufio's 4 KB default. A text node is usually
	// a few dozen bytes, and this is the most-called allocation in the renderer:
	// with the default size it accounted for the large majority of the bytes the
	// whole render path allocated.
	w := bufio.NewWriterSize(&buf, len(value)+escapeGrowth)
	if raw {
		html.DefaultWriter.RawWrite(w, value)
	} else {
		// Write resolves character references and drops the backslash from
		// backslash-escaped punctuation, which is what keeps a literal
		// underscore from reaching the user with a backslash in front of it.
		html.DefaultWriter.Write(w, value)
	}
	_ = w.Flush()
	return restoreQuotes(buf.String())
}

// escapeAttr escapes a destination for use inside a double-quoted attribute.
//
// Attribute context is not text content: a literal double quote would end the
// attribute, so here the quote must stay escaped. The destination is otherwise
// emitted as written — percent-encoding it would alter URLs, and preserving them
// byte-for-byte is the defect this package exists to fix.
func escapeAttr(dest []byte) string {
	var sb strings.Builder
	sb.Grow(len(dest))
	for _, c := range dest {
		switch c {
		case '&':
			sb.WriteString("&amp;")
		case '<':
			sb.WriteString("&lt;")
		case '>':
			sb.WriteString("&gt;")
		case '"':
			sb.WriteString("%22")
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// restoreQuotes turns goldmark's &quot; back into a literal double quote. Only
// goldmark's escaper produces that sequence, and only from a literal quote
// byte, so the substitution cannot corrupt a quote the source escaped itself:
// source "&amp;quot;" renders as "&amp;quot;", which does not contain "&quot;".
func restoreQuotes(s string) string {
	return strings.ReplaceAll(s, "&quot;", `"`)
}

// util is imported for its BufWriter contract, which *bufio.Writer satisfies.
var _ util.BufWriter = (*bufio.Writer)(nil)
