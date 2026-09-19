// The table handling in this file has no counterpart in telegold
// (https://github.com/leonid-shevtsov/telegold, commit 36dc899, MIT licence —
// see the LICENSE file in this directory), which fails closed on a table.

package telegold

import (
	"slices"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/yuin/goldmark/v2/ast"
	east "github.com/yuin/goldmark/v2/extension/ast"
	"github.com/yuin/goldmark/v2/util"
)

// Column layout for a degraded table. Telegram's HTML subset has no table
// elements, so the grid is drawn as text inside a pre block, which Telegram
// documents as preformatted monospace.
//
// text/tabwriter does the column arithmetic. It is the standard library's table
// formatter and already this project's convention for tabular CLI output, so the
// width computation is not something this package carries. tabwriter.Debug draws
// the column bar; the leading space on every cell after the first turns its
// "a |b" into "a | b".
const (
	// tablePadding is the space tabwriter adds after each cell before its bar.
	tablePadding = 1
	// tableCellLead is prepended to every cell after the first, so the bar
	// tabwriter.Debug draws is followed by a space as well as preceded by one.
	tableCellLead = " "
)

// renderTable degrades a GFM table to a preformatted block with fixed-width
// columns.
//
// Every column is left-aligned. The source's alignment markers (:---, ---:) are
// not honoured: at chat width the distinction buys nothing, and left-aligning
// everything keeps one rule for the whole grid.
//
// tabwriter counts cell width in runes, not display cells. Full-width CJK and
// emoji occupy more than one cell in a monospaced font, so a table containing
// them will not line up. Correcting that needs a display-width table, which is a
// dependency this package does not otherwise want; misalignment for non-Latin
// content is the accepted cost.
func renderTable(src []byte, table *east.Table) Block {
	rows := tableRows(src, table)
	if len(rows) == 0 {
		return Block{}
	}

	lines := layoutRows(rows)
	if len(lines) > 1 {
		// A rule under the header is what makes the header read as one. It is
		// derived from the formatted header line rather than from the column
		// widths, so its junctions cannot drift out of line with the bars above
		// and below it.
		lines = slices.Insert(lines, 1, headerRule(lines))
	}

	var sb blockBuilder
	sb.open("pre", "")
	sb.WriteString(escapeText([]byte(strings.Join(lines, "\n")), true))
	sb.close("pre")
	return sb.block()
}

// layoutRows aligns every row's cells into fixed-width columns.
func layoutRows(rows [][]string) []string {
	var buf strings.Builder
	w := tabwriter.NewWriter(&buf, 0, 0, tablePadding, ' ', tabwriter.Debug)
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			if i > 0 {
				cell = tableCellLead + cell
			}
			cells[i] = cell
		}
		// tabwriter's own error is the strings.Builder's, which cannot fail.
		_, _ = w.Write([]byte(strings.Join(cells, "\t") + "\n"))
	}
	_ = w.Flush()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, line := range lines {
		// tabwriter pads the final cell of a line when a later row is wider.
		// Trailing spaces are invisible and only cost message budget.
		lines[i] = strings.TrimRight(line, " ")
	}
	return lines
}

// headerRule draws the rule under the header row: the formatted header line with
// every column bar turned into a junction and everything else into fill, then
// extended to the width of the widest row so it spans the whole grid.
func headerRule(lines []string) string {
	rule := strings.Map(func(r rune) rune {
		if r == '|' {
			return '+'
		}
		return '-'
	}, lines[0])

	widest := 0
	for _, line := range lines {
		if n := utf8.RuneCountInString(line); n > widest {
			widest = n
		}
	}
	if pad := widest - utf8.RuneCountInString(rule); pad > 0 {
		rule += strings.Repeat("-", pad)
	}
	return rule
}

// tableRows collects every row's cell text, in source order.
func tableRows(src []byte, table *east.Table) [][]string {
	var rows [][]string
	for node := table.FirstChild(); node != nil; node = node.NextSibling() {
		// A header row and a body row are laid out identically; only the rule
		// drawn after the first row distinguishes them.
		switch node.(type) {
		case *east.TableHeader, *east.TableRow:
			rows = append(rows, tableCells(src, node))
		}
	}
	return rows
}

// tableCells collects one row's cells as plain text.
//
// Cell content is text, never markup. A tag inside the block would add bytes
// that occupy no display width, which is precisely what breaks fixed-width
// alignment, and Telegram's subset does not accept arbitrary nesting inside pre
// either. The text is escaped once, over the whole assembled grid.
func tableCells(src []byte, row ast.Node) []string {
	var cells []string
	for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
		cells = append(cells, cellText(src, cell))
	}
	return cells
}

// cellText returns one cell's plain text, with the characters tabwriter reserves
// removed. A tab inside a cell would open a phantom column; a newline would end
// the row.
//
// Character references and backslash escapes are resolved here, to the character
// they name, and HTML escaping is applied later over the whole assembled grid.
// The split matters twice. Escaping per cell instead would make a reference's
// escaped form its layout width, so one column of & would be laid out five wide.
// Not resolving at all would leave the reference's own ampersand for the grid
// escape to escape again, showing the reader &amp; where the source asked for &
// — cells take goldmark's resolving writer nowhere, so this is the only place
// the resolution can happen.
func cellText(src []byte, cell ast.Node) string {
	var sb strings.Builder
	collectText(&sb, src, cell)
	text := string(resolveReferences([]byte(sb.String())))
	text = strings.ReplaceAll(text, "\t", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.TrimSpace(text)
}

// resolveReferences turns backslash escapes and character references into the
// characters they name, without escaping anything.
//
// goldmark's own writer resolves and escapes in a single interleaved pass, which
// is no use to a grid that must be measured unescaped and escaped once at the
// end. These are the three primitives that pass is built from, applied in the
// order goldmark documents for them on util.URLEscape.
func resolveReferences(text []byte) []byte {
	text = util.UnescapePunctuations(text)
	text = util.ResolveNumericReferences(text)
	return util.ResolveEntityNames(text)
}

// collectText appends the text of every leaf under node, in source order.
//
// Only the three text-bearing leaves contribute; every other node — including
// *ast.RawHTML — is walked through for its children. Raw HTML tags are silently
// dropped while text between them is kept (e.g. "<b>bold</b>" becomes "bold"),
// because tables render as preformatted text where tags would break column
// alignment. All three are leaves in goldmark's AST — an
// AutoLink keeps its text in a field rather than a child — so SkipChildren is
// a statement of intent rather than a behavioural necessity: whatever these
// nodes hold, their own accessor is the whole of their text.
func collectText(sb *strings.Builder, src []byte, node ast.Node) {
	// ast.Walk's only error is one the walker itself returns, and this walker
	// never does.
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Text:
			sb.Write(n.Segment.Value(src))
			if n.HardLineBreak() || n.SoftLineBreak() {
				// A line break inside a cell would break the grid, so it becomes
				// a space. The cell's words stay separated, which is what
				// matters.
				sb.WriteByte(' ')
			}
			return ast.WalkSkipChildren, nil
		case *ast.String:
			sb.Write(n.Value)
			return ast.WalkSkipChildren, nil
		case *ast.AutoLink:
			sb.Write(n.Label(src))
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
}
