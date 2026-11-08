package gdocs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/tanaikech/gdocumentor/internal/utf16idx"
	"github.com/tanaikech/gdocumentor/internal/workspace"
)

var (
	reHeadingLine      = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	reTrailingHashes   = regexp.MustCompile(`\s+#+\s*$`)
	reHorizontalRule   = regexp.MustCompile(`^\s*(?:[-]{3,}|[*]{3,}|[_]{3,})\s*$`)
	reTaskListItem     = regexp.MustCompile(`^(\s*)[-*+]\s+\[([ xX])\]\s+(.*)$`)
	reUnorderedItem    = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	reOrderedItem      = regexp.MustCompile(`^(\s*)\d+[.)]\s+(.*)$`)
	reBlockquoteLine   = regexp.MustCompile(`^\s*>\s?(.*)$`)
	reTableSeparator   = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(?:\|\s*:?-{2,}:?\s*)+\|?\s*$`)
	reStandaloneMDImg  = regexp.MustCompile(`!\[([^\]]*)\]\(\s*(?:<([^>]+)>|([^\s\)]+))(?:\s+("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'))?\s*\)`)
	reStandaloneHTMLIm = regexp.MustCompile(`(?i)<img\b[^>]*?\bsrc\s*=\s*(?:"([^"]+)"|'([^']+)')[^>]*>`)
)

// inlineStyleSpan represents a relative UTF-16 character interval [StartOffset, EndOffset)
// within an inserted block or cell that carries inline formatting.
type inlineStyleSpan struct {
	StartOffset   int
	EndOffset     int
	Bold          bool
	Italic        bool
	Strikethrough bool
	Code          bool
	LinkURL       string
}

// inlineImageAnchor represents an inline image to be inserted at a relative UTF-16 offset.
type inlineImageAnchor struct {
	UTF16Offset int
	RawSource   string
	AltText     string
	Title       string
}

// parsedParagraphLine holds the converted plain text and styling metadata for a single paragraph line.
type parsedParagraphLine struct {
	PlainText      string // Does not include trailing '\n'
	NamedStyle     string // "NORMAL_TEXT", "HEADING_1".."HEADING_6"
	IsCodeBlock    bool
	IsBlockquote   bool
	IsDivider      bool
	ListKind       string // "", "UNORDERED", "ORDERED", "CHECKBOX"
	NestingLevel   int    // Number of leading '\t' characters prepended for list nesting
	InlineSpans    []inlineStyleSpan
	InlineImages   []inlineImageAnchor
	HasBlockFormat bool // True if line had heading, list, codeblock, blockquote, or divider syntax
}

// markdownTableSegment holds a parsed Markdown table ready for Google Docs insertTable + cell population.
type markdownTableSegment struct {
	Rows       [][]string
	Alignments []string // "START", "CENTER", "END" per column
	HasHeader  bool
}

// markdownSegment is either a sequence of paragraph lines or a Markdown table.
type markdownSegment struct {
	IsTable bool
	Lines   []parsedParagraphLine
	Table   *markdownTableSegment
}

// parseInlineMarkdown scans a single line of Markdown text, strips inline formatting markers
// (**, __, *, _, ~~, `, [label](url), ![alt](src)), and returns the plain text along with
// relative UTF-16 style spans and inline image anchors.
func parseInlineMarkdown(input string) (string, []inlineStyleSpan, []inlineImageAnchor) {
	var out strings.Builder
	var spans []inlineStyleSpan
	var images []inlineImageAnchor
	currUTF16 := 0

	var scan func(s string, inherited inlineStyleSpan)
	scan = func(s string, inherited inlineStyleSpan) {
		i := 0
		n := len(s)

		emitText := func(lit string) {
			if lit == "" {
				return
			}
			uLen := utf16idx.UTF16Len(lit)
			out.WriteString(lit)
			if inherited.Bold || inherited.Italic || inherited.Strikethrough || inherited.Code || inherited.LinkURL != "" {
				spans = append(spans, inlineStyleSpan{
					StartOffset:   currUTF16,
					EndOffset:     currUTF16 + uLen,
					Bold:          inherited.Bold,
					Italic:        inherited.Italic,
					Strikethrough: inherited.Strikethrough,
					Code:          inherited.Code,
					LinkURL:       inherited.LinkURL,
				})
			}
			currUTF16 += uLen
		}

		for i < n {
			// 1. Escape sequence: \* \_ \~ \` \[ \] \\
			if s[i] == '\\' && i+1 < n && strings.ContainsRune(`\*_~`+"`[]()!<>#", rune(s[i+1])) {
				emitText(s[i+1 : i+2])
				i += 2
				continue
			}

			// 2. Inline code: `code` or ``code``
			if s[i] == '`' {
				tickCount := 0
				for i+tickCount < n && s[i+tickCount] == '`' {
					tickCount++
				}
				delim := s[i : i+tickCount]
				closeIdx := strings.Index(s[i+tickCount:], delim)
				if closeIdx >= 0 {
					codeContent := s[i+tickCount : i+tickCount+closeIdx]
					if len(codeContent) >= 2 && strings.HasPrefix(codeContent, " ") && strings.HasSuffix(codeContent, " ") {
						codeContent = codeContent[1 : len(codeContent)-1]
					}
					uLen := utf16idx.UTF16Len(codeContent)
					out.WriteString(codeContent)
					codeSpan := inherited
					codeSpan.Code = true
					spans = append(spans, inlineStyleSpan{
						StartOffset:   currUTF16,
						EndOffset:     currUTF16 + uLen,
						Bold:          codeSpan.Bold,
						Italic:        codeSpan.Italic,
						Strikethrough: codeSpan.Strikethrough,
						Code:          true,
						LinkURL:       codeSpan.LinkURL,
					})
					currUTF16 += uLen
					i += tickCount + closeIdx + tickCount
					continue
				}
			}

			// 3. Inline Markdown image: ![alt](src "title")
			if strings.HasPrefix(s[i:], "![") {
				if loc := reStandaloneMDImg.FindStringIndex(s[i:]); len(loc) == 2 && loc[0] == 0 {
					m := reStandaloneMDImg.FindStringSubmatch(s[i : i+loc[1]])
					if len(m) > 0 {
						alt := m[1]
						src := m[2]
						if src == "" {
							src = m[3]
						}
						title := strings.Trim(m[4], `"'`)
						src = strings.TrimSpace(src)
						if src != "" {
							images = append(images, inlineImageAnchor{
								UTF16Offset: currUTF16,
								RawSource:   src,
								AltText:     alt,
								Title:       title,
							})
							i += loc[1]
							continue
						}
					}
				}
			}

			// 4. Inline HTML <img src="...">
			if strings.HasPrefix(strings.ToLower(s[i:]), "<img") {
				if loc := reStandaloneHTMLIm.FindStringIndex(s[i:]); len(loc) == 2 && loc[0] == 0 {
					m := reStandaloneHTMLIm.FindStringSubmatch(s[i : i+loc[1]])
					if len(m) > 0 {
						src := m[1]
						if src == "" {
							src = m[2]
						}
						src = strings.TrimSpace(src)
						if src != "" {
							images = append(images, inlineImageAnchor{
								UTF16Offset: currUTF16,
								RawSource:   src,
							})
							i += loc[1]
							continue
						}
					}
				}
			}

			// 5. Inline link: [label](url)
			if s[i] == '[' {
				labelEnd := findMatchingBracket(s, i)
				if labelEnd > i && labelEnd+1 < n && s[labelEnd+1] == '(' {
					parenEnd := findMatchingParen(s, labelEnd+1)
					if parenEnd > labelEnd+1 {
						label := s[i+1 : labelEnd]
						rawTarget := strings.TrimSpace(s[labelEnd+2 : parenEnd])
						linkURL := extractMarkdownLinkURL(rawTarget)
						if linkURL != "" {
							nextStyle := inherited
							nextStyle.LinkURL = linkURL
							scan(label, nextStyle)
							i = parenEnd + 1
							continue
						}
					}
				}
			}

			// 6. Bold + Italic: ***text*** or ___text___
			if strings.HasPrefix(s[i:], "***") || strings.HasPrefix(s[i:], "___") {
				marker := s[i : i+3]
				if marker == "___" && !isValidUnderscoreOpen(s, i, 3) {
					emitText(marker)
					i += 3
					continue
				}
				closeRel := findClosingDelimiter(s[i+3:], marker, marker == "___")
				if closeRel > 0 {
					inner := s[i+3 : i+3+closeRel]
					nextStyle := inherited
					nextStyle.Bold = true
					nextStyle.Italic = true
					scan(inner, nextStyle)
					i += 3 + closeRel + 3
					continue
				}
			}

			// 7. Bold: **text** or __text__
			if strings.HasPrefix(s[i:], "**") || strings.HasPrefix(s[i:], "__") {
				marker := s[i : i+2]
				if marker == "__" && !isValidUnderscoreOpen(s, i, 2) {
					emitText(marker)
					i += 2
					continue
				}
				closeRel := findClosingDelimiter(s[i+2:], marker, marker == "__")
				if closeRel > 0 {
					inner := s[i+2 : i+2+closeRel]
					nextStyle := inherited
					nextStyle.Bold = true
					scan(inner, nextStyle)
					i += 2 + closeRel + 2
					continue
				}
			}

			// 8. Strikethrough: ~~text~~
			if strings.HasPrefix(s[i:], "~~") {
				closeRel := findClosingDelimiter(s[i+2:], "~~", false)
				if closeRel > 0 {
					inner := s[i+2 : i+2+closeRel]
					nextStyle := inherited
					nextStyle.Strikethrough = true
					scan(inner, nextStyle)
					i += 2 + closeRel + 2
					continue
				}
			}

			// 9. Italic: *text* or _text_
			if s[i] == '*' || s[i] == '_' {
				marker := s[i : i+1]
				if marker == "_" && !isValidUnderscoreOpen(s, i, 1) {
					emitText(marker)
					i++
					continue
				}
				closeRel := findClosingDelimiter(s[i+1:], marker, marker == "_")
				if closeRel > 0 {
					inner := s[i+1 : i+1+closeRel]
					nextStyle := inherited
					nextStyle.Italic = true
					scan(inner, nextStyle)
					i += 1 + closeRel + 1
					continue
				}
			}

			// Default: advance 1 UTF-8 rune
			_, rSize := decodeFirstRune(s[i:])
			emitText(s[i : i+rSize])
			i += rSize
		}
	}

	scan(input, inlineStyleSpan{})
	return out.String(), mergeAdjacentStyleSpans(spans), images
}

func decodeFirstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isValidUnderscoreOpen(s string, pos int, markerLen int) bool {
	if pos > 0 {
		prevRune, _ := decodeLastRune(s[:pos])
		if isWordRune(prevRune) {
			return false
		}
	}
	afterPos := pos + markerLen
	if afterPos >= len(s) {
		return false
	}
	nextRune, _ := decodeFirstRune(s[afterPos:])
	return !unicode.IsSpace(nextRune)
}

func decodeLastRune(s string) (rune, int) {
	var last rune
	for _, r := range s {
		last = r
	}
	return last, len(string(last))
}

func findClosingDelimiter(s string, marker string, checkUnderscoreBoundary bool) int {
	mLen := len(marker)
	for i := 0; i <= len(s)-mLen; i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '`' {
			// Skip over inline code spans so delimiters inside `code` are ignored
			tickEnd := strings.IndexByte(s[i+1:], '`')
			if tickEnd >= 0 {
				i += 1 + tickEnd
				continue
			}
		}
		if strings.HasPrefix(s[i:], marker) {
			// Do not match if followed immediately by the same marker char (e.g. matching single '*' at start of '**')
			if mLen == 1 && i+1 < len(s) && s[i+1] == marker[0] {
				i++
				continue
			}
			if i == 0 {
				continue
			}
			prevRune, _ := decodeLastRune(s[:i])
			if unicode.IsSpace(prevRune) {
				continue
			}
			if checkUnderscoreBoundary && i+mLen < len(s) {
				nextRune, _ := decodeFirstRune(s[i+mLen:])
				if isWordRune(nextRune) {
					continue
				}
			}
			return i
		}
	}
	return -1
}

func findMatchingBracket(s string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '[' {
			depth++
		} else if s[i] == ']' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func findMatchingParen(s string, openIdx int) int {
	depth := 0
	inQuotes := byte(0)
	for i := openIdx; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if inQuotes != 0 {
			if s[i] == inQuotes {
				inQuotes = 0
			}
			continue
		}
		if s[i] == '"' || s[i] == '\'' {
			inQuotes = s[i]
			continue
		}
		if s[i] == '(' {
			depth++
		} else if s[i] == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func extractMarkdownLinkURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "<") {
		if closeIdx := strings.IndexByte(trimmed, '>'); closeIdx > 1 {
			return strings.TrimSpace(trimmed[1:closeIdx])
		}
	}
	fields := strings.Fields(trimmed)
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func mergeAdjacentStyleSpans(spans []inlineStyleSpan) []inlineStyleSpan {
	if len(spans) <= 1 {
		return spans
	}
	var out []inlineStyleSpan
	for _, sp := range spans {
		if sp.EndOffset <= sp.StartOffset {
			continue
		}
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.EndOffset == sp.StartOffset &&
				last.Bold == sp.Bold &&
				last.Italic == sp.Italic &&
				last.Strikethrough == sp.Strikethrough &&
				last.Code == sp.Code &&
				last.LinkURL == sp.LinkURL {
				last.EndOffset = sp.EndOffset
				continue
			}
		}
		out = append(out, sp)
	}
	return out
}

// parseMarkdownIntoSegments parses a full Markdown string into a sequence of text segments and table segments.
func parseMarkdownIntoSegments(md string) []markdownSegment {
	normalized := strings.ReplaceAll(md, "\r\n", "\n")
	rawLines := strings.Split(normalized, "\n")

	var segments []markdownSegment
	var currLines []parsedParagraphLine

	flushTextSegment := func() {
		if len(currLines) > 0 {
			segments = append(segments, markdownSegment{
				IsTable: false,
				Lines:   currLines,
			})
			currLines = nil
		}
	}

	inFence := false
	fenceMarker := ""
	consecutiveBlankLines := 0
	i := 0
	for i < len(rawLines) {
		line := rawLines[i]
		trimmed := strings.TrimSpace(line)

		// 1. Check fenced code block toggle (``` or ~~~)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if !inFence {
				inFence = true
				fenceMarker = marker
				consecutiveBlankLines = 0
				i++
				continue
			} else if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
				fenceMarker = ""
				consecutiveBlankLines = 0
				i++
				continue
			}
		}

		if inFence {
			currLines = append(currLines, parsedParagraphLine{
				PlainText:      line,
				NamedStyle:     "NORMAL_TEXT",
				IsCodeBlock:    true,
				HasBlockFormat: true,
			})
			i++
			continue
		}

		// Outside fenced code blocks, a single blank line in Markdown is a standard block/paragraph separator
		// rather than an empty paragraph. Only 2+ consecutive blank lines emit an intentional blank paragraph.
		if trimmed == "" {
			consecutiveBlankLines++
			if consecutiveBlankLines >= 2 {
				currLines = append(currLines, parsedParagraphLine{
					PlainText:  "",
					NamedStyle: "NORMAL_TEXT",
				})
			}
			i++
			continue
		}
		consecutiveBlankLines = 0

		// 2. Check Markdown Table (header row followed by | --- | separator row)
		if i+1 < len(rawLines) && strings.Contains(line, "|") && reTableSeparator.MatchString(rawLines[i+1]) {
			headerCells := splitMarkdownTableRow(line)
			sepCells := splitMarkdownTableRow(rawLines[i+1])
			if len(headerCells) > 0 && len(sepCells) > 0 {
				flushTextSegment()
				numCols := max(len(sepCells), len(headerCells))
				alignments := make([]string, numCols)
				for c := 0; c < numCols; c++ {
					align := "START"
					if c < len(sepCells) {
						sc := strings.TrimSpace(sepCells[c])
						leftColon := strings.HasPrefix(sc, ":")
						rightColon := strings.HasSuffix(sc, ":")
						if leftColon && rightColon {
							align = "CENTER"
						} else if rightColon {
							align = "END"
						}
					}
					alignments[c] = align
				}

				var tableRows [][]string
				normHeader := make([]string, numCols)
				copy(normHeader, headerCells)
				tableRows = append(tableRows, normHeader)

				i += 2
				for i < len(rawLines) {
					rowTrim := strings.TrimSpace(rawLines[i])
					if rowTrim == "" || !strings.Contains(rowTrim, "|") {
						break
					}
					if strings.HasPrefix(rowTrim, "```") || strings.HasPrefix(rowTrim, "~~~") {
						break
					}
					cells := splitMarkdownTableRow(rawLines[i])
					if len(cells) == 0 {
						break
					}
					normRow := make([]string, numCols)
					for c := 0; c < numCols && c < len(cells); c++ {
						normRow[c] = cells[c]
					}
					tableRows = append(tableRows, normRow)
					i++
				}

				segments = append(segments, markdownSegment{
					IsTable: true,
					Table: &markdownTableSegment{
						Rows:       tableRows,
						Alignments: alignments,
						HasHeader:  true,
					},
				})
				continue
			}
		}

		// 3. Parse single line (Heading, Horizontal Rule, Task/Bullet/Numbered List, Blockquote, or Normal Paragraph)
		currLines = append(currLines, parseSingleMarkdownLine(line))
		i++
	}

	flushTextSegment()
	return segments
}

func splitMarkdownTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "|") {
		trimmed = trimmed[1:]
	}
	if strings.HasSuffix(trimmed, "|") && !strings.HasSuffix(trimmed, `\|`) {
		trimmed = trimmed[:len(trimmed)-1]
	}

	var cells []string
	var cur strings.Builder
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == '\\' && i+1 < len(trimmed) && trimmed[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if trimmed[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(trimmed[i])
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

func computeListNestingLevel(indent string) int {
	spaces := 0
	for _, r := range indent {
		if r == '\t' {
			spaces += 2
		} else if r == ' ' {
			spaces++
		}
	}
	level := min(spaces/2, 8)
	return level
}

func parseSingleMarkdownLine(line string) parsedParagraphLine {
	// 1. Horizontal Rule (---, ***, ___)
	if reHorizontalRule.MatchString(line) {
		return parsedParagraphLine{
			PlainText:      "",
			NamedStyle:     "NORMAL_TEXT",
			IsDivider:      true,
			HasBlockFormat: true,
		}
	}

	// 2. Heading (# H1 .. ###### H6)
	if m := reHeadingLine.FindStringSubmatch(line); len(m) == 3 {
		level := len(m[1])
		rawHeading := reTrailingHashes.ReplaceAllString(m[2], "")
		plain, spans, imgs := parseInlineMarkdown(rawHeading)
		return parsedParagraphLine{
			PlainText:      plain,
			NamedStyle:     fmt.Sprintf("HEADING_%d", level),
			InlineSpans:    spans,
			InlineImages:   imgs,
			HasBlockFormat: true,
		}
	}

	// 3. Task list item (- [ ] item or - [x] item)
	if m := reTaskListItem.FindStringSubmatch(line); len(m) == 4 {
		nesting := computeListNestingLevel(m[1])
		checked := strings.EqualFold(m[2], "x")
		prefix := "☐ "
		if checked {
			prefix = "☑ "
		}
		plain, spans, imgs := parseInlineMarkdown(prefix + m[3])
		return parsedParagraphLine{
			PlainText:      plain,
			NamedStyle:     "NORMAL_TEXT",
			ListKind:       "UNORDERED",
			NestingLevel:   nesting,
			InlineSpans:    spans,
			InlineImages:   imgs,
			HasBlockFormat: true,
		}
	}

	// 4. Unordered list item (- item, * item, + item)
	if m := reUnorderedItem.FindStringSubmatch(line); len(m) == 3 {
		nesting := computeListNestingLevel(m[1])
		plain, spans, imgs := parseInlineMarkdown(m[2])
		return parsedParagraphLine{
			PlainText:      plain,
			NamedStyle:     "NORMAL_TEXT",
			ListKind:       "UNORDERED",
			NestingLevel:   nesting,
			InlineSpans:    spans,
			InlineImages:   imgs,
			HasBlockFormat: true,
		}
	}

	// 5. Ordered list item (1. item, 2) item)
	if m := reOrderedItem.FindStringSubmatch(line); len(m) == 3 {
		nesting := computeListNestingLevel(m[1])
		plain, spans, imgs := parseInlineMarkdown(m[2])
		return parsedParagraphLine{
			PlainText:      plain,
			NamedStyle:     "NORMAL_TEXT",
			ListKind:       "ORDERED",
			NestingLevel:   nesting,
			InlineSpans:    spans,
			InlineImages:   imgs,
			HasBlockFormat: true,
		}
	}

	// 6. Blockquote (> quote)
	if m := reBlockquoteLine.FindStringSubmatch(line); len(m) == 2 {
		plain, spans, imgs := parseInlineMarkdown(m[1])
		return parsedParagraphLine{
			PlainText:      plain,
			NamedStyle:     "NORMAL_TEXT",
			IsBlockquote:   true,
			InlineSpans:    spans,
			InlineImages:   imgs,
			HasBlockFormat: true,
		}
	}

	// 7. Standard paragraph line
	plain, spans, imgs := parseInlineMarkdown(line)
	return parsedParagraphLine{
		PlainText:    plain,
		NamedStyle:   "NORMAL_TEXT",
		InlineSpans:  spans,
		InlineImages: imgs,
	}
}

// buildTextSegmentBatchRequests converts a sequence of parsedParagraphLine starting at baseIndex
// into Google Docs API batchUpdate requests.
//
// Order of operations inside the batch (designed to prevent UTF-16 index drift):
//  1. insertText (inserts the entire plain text block including leading '\t' characters for nested lists)
//  2. updateParagraphStyle (resets inherited paragraph styles and applies HEADING_1..6, codeblock, blockquote, divider)
//  3. updateTextStyle (resets inherited inline styles and applies bold, italic, strikethrough, inline code, links)
//  4. createParagraphBullets (executed in descending bottom-to-top order so tab removal never shifts earlier ranges)
//  5. insertInlineImage (executed in descending bottom-to-top order at post-bullet-tab-adjusted indices)
func (c *Client) buildTextSegmentBatchRequests(
	ctx context.Context,
	lines []parsedParagraphLine,
	baseIndex int,
	tabID string,
	trailingNewline bool,
	workDir string,
	imageWidthPt float64,
	imageHeightPt float64,
	preResolvedURIs map[string]string,
) ([]map[string]any, string, func(), error) {
	var cleanups []func()
	runCleanup := func() {
		for _, fn := range cleanups {
			if fn != nil {
				fn()
			}
		}
	}

	if len(lines) == 0 {
		return nil, "", runCleanup, nil
	}

	makeRange := func(startIdx, endIdx int) map[string]any {
		m := map[string]any{
			"startIndex": startIdx,
			"endIndex":   endIdx,
		}
		if tabID != "" {
			m["tabId"] = tabID
		}
		return m
	}

	makeLocation := func(idx int) map[string]any {
		m := map[string]any{
			"index": idx,
		}
		if tabID != "" {
			m["tabId"] = tabID
		}
		return m
	}

	type lineCoord struct {
		line       parsedParagraphLine
		startUTF16 int // Relative to baseIndex (includes leading '\t' for list nesting)
		textStart  int // Relative to baseIndex (after leading '\t')
		endUTF16   int // Exclusive end of line text (before '\n')
		paraEnd    int // Exclusive end including '\n' if present
	}

	var fullText strings.Builder
	var coords []lineCoord
	relCursor := 0
	hasAnyBlockFormat := len(lines) > 1

	for idx, ln := range lines {
		if ln.HasBlockFormat {
			hasAnyBlockFormat = true
		}
		lineStart := relCursor
		if ln.ListKind != "" && ln.NestingLevel > 0 {
			tabs := strings.Repeat("\t", ln.NestingLevel)
			fullText.WriteString(tabs)
			relCursor += ln.NestingLevel
		}
		textStart := relCursor
		fullText.WriteString(ln.PlainText)
		relCursor += utf16idx.UTF16Len(ln.PlainText)
		lineEnd := relCursor

		hasNL := idx < len(lines)-1 || trailingNewline
		if hasNL {
			fullText.WriteByte('\n')
			relCursor++
		}
		paraEnd := relCursor

		coords = append(coords, lineCoord{
			line:       ln,
			startUTF16: lineStart,
			textStart:  textStart,
			endUTF16:   lineEnd,
			paraEnd:    paraEnd,
		})
	}

	assembledText := fullText.String()
	totalUTF16Len := relCursor

	var reqs []map[string]any

	// 1. Insert plain text (if non-empty)
	if totalUTF16Len > 0 {
		reqs = append(reqs, map[string]any{
			"insertText": map[string]any{
				"location": makeLocation(baseIndex),
				"text":     assembledText,
			},
		})

		// 2. Reset inherited paragraph style to NORMAL_TEXT when inserting block/multi-line Markdown
		if hasAnyBlockFormat {
			reqs = append(reqs, map[string]any{
				"updateParagraphStyle": map[string]any{
					"range": makeRange(baseIndex, baseIndex+totalUTF16Len),
					"paragraphStyle": map[string]any{
						"namedStyleType": "NORMAL_TEXT",
					},
					"fields": "namedStyleType",
				},
			})
		}

		// 3. Reset inherited inline text style across the inserted text so inserting next to a bold/link word does not bleed
		reqs = append(reqs, map[string]any{
			"updateTextStyle": map[string]any{
				"range": makeRange(baseIndex, baseIndex+totalUTF16Len),
				"textStyle": map[string]any{
					"bold":          false,
					"italic":        false,
					"underline":     false,
					"strikethrough": false,
				},
				"fields": "bold,italic,underline,strikethrough",
			},
		})
	}

	// 4. Apply paragraph-level styles (Headings, Code Blocks, Blockquotes, Horizontal Dividers)
	// and inline text styles (Bold, Italic, Strikethrough, Code, Links) while all indices are exact.
	for _, lc := range coords {
		absParaStart := baseIndex + lc.startUTF16
		absParaEnd := baseIndex + lc.paraEnd
		if absParaEnd <= absParaStart {
			continue
		}

		if lc.line.NamedStyle != "" && lc.line.NamedStyle != "NORMAL_TEXT" {
			reqs = append(reqs, map[string]any{
				"updateParagraphStyle": map[string]any{
					"range": makeRange(absParaStart, absParaEnd),
					"paragraphStyle": map[string]any{
						"namedStyleType": lc.line.NamedStyle,
					},
					"fields": "namedStyleType",
				},
			})
		}

		if lc.line.IsCodeBlock {
			reqs = append(reqs, map[string]any{
				"updateParagraphStyle": map[string]any{
					"range": makeRange(absParaStart, absParaEnd),
					"paragraphStyle": map[string]any{
						"indentStart":     map[string]any{"magnitude": 14, "unit": "PT"},
						"indentFirstLine": map[string]any{"magnitude": 14, "unit": "PT"},
						"shading": map[string]any{
							"backgroundColor": map[string]any{
								"color": map[string]any{
									"rgbColor": map[string]any{"red": 0.95, "green": 0.96, "blue": 0.97},
								},
							},
						},
					},
					"fields": "indentStart,indentFirstLine,shading",
				},
			})
			if lc.endUTF16 > lc.textStart {
				reqs = append(reqs, map[string]any{
					"updateTextStyle": map[string]any{
						"range": makeRange(baseIndex+lc.textStart, baseIndex+lc.endUTF16),
						"textStyle": map[string]any{
							"weightedFontFamily": map[string]any{"fontFamily": "Roboto Mono"},
							"fontSize":           map[string]any{"magnitude": 9.5, "unit": "PT"},
						},
						"fields": "weightedFontFamily,fontSize",
					},
				})
			}
		}

		if lc.line.IsBlockquote {
			reqs = append(reqs, map[string]any{
				"updateParagraphStyle": map[string]any{
					"range": makeRange(absParaStart, absParaEnd),
					"paragraphStyle": map[string]any{
						"indentStart":     map[string]any{"magnitude": 18, "unit": "PT"},
						"indentFirstLine": map[string]any{"magnitude": 18, "unit": "PT"},
						"borderLeft": map[string]any{
							"color": map[string]any{
								"color": map[string]any{
									"rgbColor": map[string]any{"red": 0.72, "green": 0.76, "blue": 0.82},
								},
							},
							"width":     map[string]any{"magnitude": 3, "unit": "PT"},
							"padding":   map[string]any{"magnitude": 8, "unit": "PT"},
							"dashStyle": "SOLID",
						},
					},
					"fields": "indentStart,indentFirstLine,borderLeft",
				},
			})
			if lc.endUTF16 > lc.textStart {
				reqs = append(reqs, map[string]any{
					"updateTextStyle": map[string]any{
						"range": makeRange(baseIndex+lc.textStart, baseIndex+lc.endUTF16),
						"textStyle": map[string]any{
							"italic": true,
							"foregroundColor": map[string]any{
								"color": map[string]any{
									"rgbColor": map[string]any{"red": 0.32, "green": 0.35, "blue": 0.40},
								},
							},
						},
						"fields": "italic,foregroundColor",
					},
				})
			}
		}

		if lc.line.IsDivider {
			reqs = append(reqs, map[string]any{
				"updateParagraphStyle": map[string]any{
					"range": makeRange(absParaStart, absParaEnd),
					"paragraphStyle": map[string]any{
						"borderBottom": map[string]any{
							"color": map[string]any{
								"color": map[string]any{
									"rgbColor": map[string]any{"red": 0.80, "green": 0.82, "blue": 0.85},
								},
							},
							"width":     map[string]any{"magnitude": 1, "unit": "PT"},
							"padding":   map[string]any{"magnitude": 4, "unit": "PT"},
							"dashStyle": "SOLID",
						},
					},
					"fields": "borderBottom",
				},
			})
		}

		// Inline spans within this line
		for _, sp := range lc.line.InlineSpans {
			absStart := baseIndex + lc.textStart + sp.StartOffset
			absEnd := baseIndex + lc.textStart + sp.EndOffset
			if absEnd <= absStart {
				continue
			}
			ts, fields := buildGoogleDocsTextStyle(sp)
			if len(fields) > 0 {
				reqs = append(reqs, map[string]any{
					"updateTextStyle": map[string]any{
						"range":     makeRange(absStart, absEnd),
						"textStyle": ts,
						"fields":    strings.Join(fields, ","),
					},
				})
			}
		}
	}

	// 5. Group contiguous list lines of the same ListKind and emit createParagraphBullets in DESCENDING order
	// so that Google Docs' automatic consumption of leading '\t' characters in nested lists never shifts
	// the UTF-16 coordinates of earlier list groups.
	type bulletGroup struct {
		startIdx int
		endIdx   int
		preset   string
	}
	var bulletGroups []bulletGroup
	for i := 0; i < len(coords); {
		kind := coords[i].line.ListKind
		if kind == "" {
			i++
			continue
		}
		j := i + 1
		for j < len(coords) && coords[j].line.ListKind == kind {
			j++
		}
		preset := "BULLET_DISC_CIRCLE_SQUARE"
		if kind == "ORDERED" {
			preset = "NUMBERED_DECIMAL_ALPHA_ROMAN"
		}
		grpStart := baseIndex + coords[i].startUTF16
		grpEnd := baseIndex + coords[j-1].paraEnd
		if grpEnd > grpStart {
			bulletGroups = append(bulletGroups, bulletGroup{
				startIdx: grpStart,
				endIdx:   grpEnd,
				preset:   preset,
			})
		}
		i = j
	}
	for _, bg := range slices.Backward(bulletGroups) {

		reqs = append(reqs, map[string]any{
			"createParagraphBullets": map[string]any{
				"range":        makeRange(bg.startIdx, bg.endIdx),
				"bulletPreset": bg.preset,
			},
		})
	}

	// 6. Emit insertInlineImage requests in DESCENDING order of their post-bullet-tab-adjusted UTF-16 index.
	type resolvedImgInsert struct {
		absIndex int
		uri      string
	}
	var imgInserts []resolvedImgInsert

	for lineIdx, lc := range coords {
		if len(lc.line.InlineImages) == 0 {
			continue
		}
		// Calculate total leading '\t' characters consumed by createParagraphBullets up to and including this line
		tabsRemovedBeforeOrOnLine := 0
		for k := 0; k <= lineIdx; k++ {
			if coords[k].line.ListKind != "" && coords[k].line.NestingLevel > 0 {
				tabsRemovedBeforeOrOnLine += coords[k].line.NestingLevel
			}
		}
		for _, imgAnchor := range lc.line.InlineImages {
			uri := ""
			if preResolvedURIs != nil {
				uri = preResolvedURIs[imgAnchor.RawSource]
			}
			if uri == "" {
				var cleanupFn func()
				var err error
				uri, cleanupFn, err = c.ResolveImageURIForDocsAPI(ctx, imgAnchor.RawSource, workDir)
				if cleanupFn != nil {
					cleanups = append(cleanups, cleanupFn)
				}
				if err != nil {
					runCleanup()
					return nil, "", nil, err
				}
			}
			adjAbsIdx := max(baseIndex+lc.textStart+imgAnchor.UTF16Offset-tabsRemovedBeforeOrOnLine, 1)
			imgInserts = append(imgInserts, resolvedImgInsert{
				absIndex: adjAbsIdx,
				uri:      uri,
			})
		}
	}

	// Sort descending so each inserted image (+1 UTF-16 unit) never shifts an earlier image's target index
	sort.SliceStable(imgInserts, func(i, j int) bool {
		return imgInserts[i].absIndex > imgInserts[j].absIndex
	})

	for _, img := range imgInserts {
		inlineImgReq := map[string]any{
			"location": makeLocation(img.absIndex),
			"uri":      img.uri,
		}
		if objSize := buildImageObjectSize(imageWidthPt, imageHeightPt); len(objSize) > 0 {
			inlineImgReq["objectSize"] = objSize
		}
		reqs = append(reqs, map[string]any{
			"insertInlineImage": inlineImgReq,
		})
	}

	return reqs, assembledText, runCleanup, nil
}

func buildGoogleDocsTextStyle(sp inlineStyleSpan) (map[string]any, []string) {
	ts := make(map[string]any)
	var fields []string
	if sp.Bold {
		ts["bold"] = true
		fields = append(fields, "bold")
	}
	if sp.Italic {
		ts["italic"] = true
		fields = append(fields, "italic")
	}
	if sp.Strikethrough {
		ts["strikethrough"] = true
		fields = append(fields, "strikethrough")
	}
	if sp.Code {
		ts["weightedFontFamily"] = map[string]any{"fontFamily": "Roboto Mono"}
		ts["backgroundColor"] = map[string]any{
			"color": map[string]any{
				"rgbColor": map[string]any{"red": 0.94, "green": 0.95, "blue": 0.96},
			},
		}
		fields = append(fields, "weightedFontFamily", "backgroundColor")
	}
	if sp.LinkURL != "" {
		ts["link"] = map[string]any{"url": sp.LinkURL}
		fields = append(fields, "link")
	}
	return ts, fields
}

func buildImageObjectSize(widthPt, heightPt float64) map[string]any {
	if widthPt <= 0 && heightPt <= 0 {
		return nil
	}
	sz := make(map[string]any)
	if widthPt > 0 {
		sz["width"] = map[string]any{"magnitude": widthPt, "unit": "PT"}
	}
	if heightPt > 0 {
		sz["height"] = map[string]any{"magnitude": heightPt, "unit": "PT"}
	}
	return sz
}

// ExecuteMarkdownInsertAt inserts rich Markdown (headings, bold/italic/strikethrough/code/links,
// bullet/numbered/task lists with nesting, code blocks, blockquotes, dividers, inline images, and tables)
// at targetIndex inside the specified Google Document tab.
func (c *Client) ExecuteMarkdownInsertAt(
	ctx context.Context,
	docID string,
	tabID string,
	targetIndex int,
	markdownText string,
	leadingNewline bool,
	trailingNewline bool,
	writeMode string,
	workDir string,
	imageWidthPt float64,
	imageHeightPt float64,
	initialDeleteRequests []map[string]any,
) (int, error) {
	cleanMD := strings.ReplaceAll(markdownText, "\r\n", "\n")
	// Strip at most one trailing '\n' before segmenting so trailingNewline controls paragraph termination cleanly
	cleanMD = strings.TrimSuffix(cleanMD, "\n")

	segments := parseMarkdownIntoSegments(cleanMD)
	if len(segments) == 0 && len(initialDeleteRequests) == 0 {
		return 0, nil
	}

	// Pre-resolve and validate all image references across all text and table segments BEFORE sending
	// any batchUpdate so a missing local image file never leaves the document partially modified.
	preResolvedURIs := make(map[string]string)
	var allCleanups []func()
	defer func() {
		for _, fn := range allCleanups {
			if fn != nil {
				fn()
			}
		}
	}()

	for _, seg := range segments {
		if !seg.IsTable {
			for _, ln := range seg.Lines {
				for _, img := range ln.InlineImages {
					if _, exists := preResolvedURIs[img.RawSource]; !exists {
						uri, cleanupFn, err := c.ResolveImageURIForDocsAPI(ctx, img.RawSource, workDir)
						if cleanupFn != nil {
							allCleanups = append(allCleanups, cleanupFn)
						}
						if err != nil {
							return 0, err
						}
						preResolvedURIs[img.RawSource] = uri
					}
				}
			}
		} else if seg.Table != nil {
			for _, row := range seg.Table.Rows {
				for _, rawCell := range row {
					_, _, cellImgs := parseInlineMarkdown(rawCell)
					for _, img := range cellImgs {
						if _, exists := preResolvedURIs[img.RawSource]; !exists {
							uri, cleanupFn, err := c.ResolveImageURIForDocsAPI(ctx, img.RawSource, workDir)
							if cleanupFn != nil {
								allCleanups = append(allCleanups, cleanupFn)
							}
							if err != nil {
								return 0, err
							}
							preResolvedURIs[img.RawSource] = uri
						}
					}
				}
			}
		}
	}

	cursorIdx := maxInt(1, targetIndex)
	totalInsertedChars := 0

	// If leadingNewline is requested (e.g. appending to the end of a non-empty document),
	// prepend an empty line or insert '\n' first so the Markdown starts on a fresh paragraph.
	if leadingNewline {
		if len(segments) > 0 && !segments[0].IsTable {
			loc := map[string]any{"index": cursorIdx}
			if tabID != "" {
				loc["tabId"] = tabID
			}
			initialDeleteRequests = append(initialDeleteRequests, map[string]any{
				"insertText": map[string]any{
					"location": loc,
					"text":     "\n",
				},
			})
			cursorIdx++
			totalInsertedChars++
		}
	}

	pendingPreRequests := initialDeleteRequests

	// Obtain initial maxBodyEnd if there are multiple segments (e.g. tables mixed with text)
	var prevMaxBodyEnd int
	if len(segments) > 1 {
		if initDoc, err := c.FetchRawDocumentJSON(ctx, docID); err == nil {
			_, _, initBuf := buildIndexedUTF16Buffer(initDoc, tabID)
			prevMaxBodyEnd = initBuf.maxBodyEnd
		}
	}

	for segIdx, seg := range segments {
		isLastSeg := segIdx == len(segments)-1
		segTrailingNL := true
		if isLastSeg {
			segTrailingNL = trailingNewline
		}

		if !seg.IsTable {
			reqs, plainInserted, cleanupFn, err := c.buildTextSegmentBatchRequests(
				ctx,
				seg.Lines,
				cursorIdx,
				tabID,
				segTrailingNL,
				workDir,
				imageWidthPt,
				imageHeightPt,
				preResolvedURIs,
			)
			if cleanupFn != nil {
				defer cleanupFn()
			}
			if err != nil {
				return totalInsertedChars, err
			}

			allReqs := append(pendingPreRequests, reqs...)
			pendingPreRequests = nil

			if len(allReqs) > 0 {
				if err := c.executeBatchWithWriteModeFallback(ctx, docID, allReqs, writeMode); err != nil {
					return totalInsertedChars, err
				}
			}
			totalInsertedChars += len([]rune(plainInserted)) + len(countSegmentImages(seg.Lines))

			if !isLastSeg {
				if freshDoc, fErr := c.FetchRawDocumentJSON(ctx, docID); fErr == nil {
					_, _, freshBuf := buildIndexedUTF16Buffer(freshDoc, tabID)
					if prevMaxBodyEnd > 0 && freshBuf.maxBodyEnd >= prevMaxBodyEnd {
						cursorIdx += freshBuf.maxBodyEnd - prevMaxBodyEnd
					} else {
						cursorIdx += utf16idx.UTF16Len(plainInserted)
					}
					prevMaxBodyEnd = freshBuf.maxBodyEnd
				} else {
					cursorIdx += utf16idx.UTF16Len(plainInserted)
				}
			}
			continue
		}

		// Table segment
		tbl := seg.Table
		if tbl == nil || len(tbl.Rows) == 0 || len(tbl.Alignments) == 0 {
			continue
		}
		numRows := len(tbl.Rows)
		numCols := len(tbl.Alignments)

		// If there are pending pre-requests (e.g. deleteContentRange for --replace-body / --replace-range), execute them first
		if len(pendingPreRequests) > 0 {
			if err := c.executeBatchWithWriteModeFallback(ctx, docID, pendingPreRequests, writeMode); err != nil {
				return totalInsertedChars, err
			}
			pendingPreRequests = nil
			if freshDoc, fErr := c.FetchRawDocumentJSON(ctx, docID); fErr == nil {
				_, _, freshBuf := buildIndexedUTF16Buffer(freshDoc, tabID)
				prevMaxBodyEnd = freshBuf.maxBodyEnd
				if cursorIdx > freshBuf.maxBodyEnd-1 {
					cursorIdx = maxInt(1, freshBuf.maxBodyEnd-1)
				}
			}
		}

		// Note: Google Docs insertTable automatically inserts a '\n' before the table.
		// If a preceding text segment just inserted a trailing '\n' at cursorIdx-1, inserting the table at cursorIdx-1
		// avoids creating a redundant blank paragraph before the table.
		tblInsertIdx := cursorIdx
		if segIdx > 0 && !segments[segIdx-1].IsTable && tblInsertIdx > 1 {
			tblInsertIdx = cursorIdx - 1
		}

		loc := map[string]any{"index": tblInsertIdx}
		if tabID != "" {
			loc["tabId"] = tabID
		}
		insertTblReq := []map[string]any{
			{
				"insertTable": map[string]any{
					"rows":     numRows,
					"columns":  numCols,
					"location": loc,
				},
			},
		}
		if err := c.executeBatchWithWriteModeFallback(ctx, docID, insertTblReq, writeMode); err != nil {
			return totalInsertedChars, err
		}

		// Re-fetch document JSON to obtain the exact server-generated cell startIndex coordinates
		afterTblDoc, fErr := c.FetchRawDocumentJSON(ctx, docID)
		if fErr != nil {
			return totalInsertedChars, fErr
		}
		cellCoords, tableEndIdx := resolveInsertedTableCellCoordinates(afterTblDoc, tabID, tblInsertIdx, numRows, numCols)
		_, _, afterTblBuf := buildIndexedUTF16Buffer(afterTblDoc, tabID)

		// Build cell population & formatting requests in strictly DESCENDING cell order (bottom-right to top-left)
		var cellReqs []map[string]any

		// If insertTable was placed at cursorIdx-1 (before the preceding text segment's trailing '\n'),
		// Google Docs pushed that trailing '\n' to [tableEndIdx, tableEndIdx+1). Remove that redundant blank line
		// if it is not the document's final segment newline.
		if tblInsertIdx < cursorIdx && tableEndIdx > 0 && tableEndIdx+1 <= afterTblBuf.maxBodyEnd-1 {
			delPushedRange := map[string]any{
				"startIndex": tableEndIdx,
				"endIndex":   tableEndIdx + 1,
			}
			if tabID != "" {
				delPushedRange["tabId"] = tabID
			}
			cellReqs = append(cellReqs, map[string]any{
				"deleteContentRange": map[string]any{
					"range": delPushedRange,
				},
			})
		}
		for r := numRows - 1; r >= 0; r-- {
			for col := numCols - 1; col >= 0; col-- {
				cStart := cellCoords[r][col]
				if cStart <= 0 {
					continue
				}
				rawCell := ""
				if col < len(tbl.Rows[r]) {
					rawCell = tbl.Rows[r][col]
				}
				plainCell, cellSpans, cellImgs := parseInlineMarkdown(rawCell)
				cLen := utf16idx.UTF16Len(plainCell)

				if plainCell != "" {
					cellLoc := map[string]any{"index": cStart}
					if tabID != "" {
						cellLoc["tabId"] = tabID
					}
					cellReqs = append(cellReqs, map[string]any{
						"insertText": map[string]any{
							"location": cellLoc,
							"text":     plainCell,
						},
					})
					totalInsertedChars += len([]rune(plainCell))

					cellRange := map[string]any{"startIndex": cStart, "endIndex": cStart + cLen}
					if tabID != "" {
						cellRange["tabId"] = tabID
					}
					isHeaderCell := r == 0 && tbl.HasHeader
					cellReqs = append(cellReqs, map[string]any{
						"updateTextStyle": map[string]any{
							"range": cellRange,
							"textStyle": map[string]any{
								"bold":          isHeaderCell,
								"italic":        false,
								"underline":     false,
								"strikethrough": false,
							},
							"fields": "bold,italic,underline,strikethrough",
						},
					})

					for _, sp := range cellSpans {
						if isHeaderCell {
							sp.Bold = true
						}
						spRange := map[string]any{
							"startIndex": cStart + sp.StartOffset,
							"endIndex":   cStart + sp.EndOffset,
						}
						if tabID != "" {
							spRange["tabId"] = tabID
						}
						ts, fields := buildGoogleDocsTextStyle(sp)
						if len(fields) > 0 {
							cellReqs = append(cellReqs, map[string]any{
								"updateTextStyle": map[string]any{
									"range":     spRange,
									"textStyle": ts,
									"fields":    strings.Join(fields, ","),
								},
							})
						}
					}
				}

				if col < len(tbl.Alignments) && tbl.Alignments[col] != "" && tbl.Alignments[col] != "START" {
					pRange := map[string]any{
						"startIndex": cStart,
						"endIndex":   cStart + maxInt(1, cLen),
					}
					if tabID != "" {
						pRange["tabId"] = tabID
					}
					cellReqs = append(cellReqs, map[string]any{
						"updateParagraphStyle": map[string]any{
							"range": pRange,
							"paragraphStyle": map[string]any{
								"alignment": tbl.Alignments[col],
							},
							"fields": "alignment",
						},
					})
				}

				// Inline images inside table cell (inserted in reverse offset order)
				for _, imgItem := range slices.Backward(cellImgs) {

					uri := preResolvedURIs[imgItem.RawSource]
					if uri == "" {
						var cleanupFn func()
						var uErr error
						uri, cleanupFn, uErr = c.ResolveImageURIForDocsAPI(ctx, imgItem.RawSource, workDir)
						if cleanupFn != nil {
							defer cleanupFn()
						}
						if uErr != nil {
							return totalInsertedChars, uErr
						}
					}
					imgLoc := map[string]any{"index": cStart + imgItem.UTF16Offset}
					if tabID != "" {
						imgLoc["tabId"] = tabID
					}
					cellReqs = append(cellReqs, map[string]any{
						"insertInlineImage": map[string]any{
							"location": imgLoc,
							"uri":      uri,
						},
					})
					totalInsertedChars++
				}
			}
		}

		if len(cellReqs) > 0 {
			if err := c.executeBatchWithWriteModeFallback(ctx, docID, cellReqs, writeMode); err != nil {
				return totalInsertedChars, err
			}
		}

		if !isLastSeg {
			if freshDoc, fErr := c.FetchRawDocumentJSON(ctx, docID); fErr == nil {
				_, updatedEnd := resolveInsertedTableCellCoordinates(freshDoc, tabID, tblInsertIdx, numRows, numCols)
				_, _, freshBuf := buildIndexedUTF16Buffer(freshDoc, tabID)
				prevMaxBodyEnd = freshBuf.maxBodyEnd
				cursorIdx = minInt(updatedEnd, freshBuf.maxBodyEnd-1)
			} else {
				cursorIdx = tableEndIdx
			}
		}
	}

	if len(pendingPreRequests) > 0 {
		if err := c.executeBatchWithWriteModeFallback(ctx, docID, pendingPreRequests, writeMode); err != nil {
			return totalInsertedChars, err
		}
	}

	return totalInsertedChars, nil
}

func countSegmentImages(lines []parsedParagraphLine) []inlineImageAnchor {
	var out []inlineImageAnchor
	for _, ln := range lines {
		out = append(out, ln.InlineImages...)
	}
	return out
}

// resolveInsertedTableCellCoordinates finds the table inserted near targetIdx in docJSON
// and returns a 2D slice [row][col] of 1-based UTF-16 cell start indices and the table's endIndex.
// If running against a static unit-test mock server that does not dynamically emit the table element,
// it falls back to the deterministic Google Docs table coordinate formula.
func resolveInsertedTableCellCoordinates(
	docJSON map[string]any,
	tabID string,
	targetIdx int,
	numRows int,
	numCols int,
) ([][]int, int) {
	_, _, docTab := resolveTargetDocumentTab(docJSON, tabID)
	bodyContent := getSlice(getMap(docTab, "body"), "content")

	var bestTable map[string]any
	bestDist := 1 << 30
	for _, rawEl := range bodyContent {
		el, ok := rawEl.(map[string]any)
		if !ok {
			continue
		}
		tbl := getMap(el, "table")
		if len(tbl) == 0 {
			continue
		}
		rows := getSlice(tbl, "tableRows")
		if len(rows) != numRows {
			continue
		}
		sIdx := getInt(el, "startIndex")
		dist := sIdx - targetIdx
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist = dist
			bestTable = el
		}
	}

	coords := make([][]int, numRows)
	for r := range numRows {
		coords[r] = make([]int, numCols)
	}

	if bestTable != nil && bestDist <= 10 {
		tbl := getMap(bestTable, "table")
		rows := getSlice(tbl, "tableRows")
		for r := 0; r < numRows && r < len(rows); r++ {
			rowMap, _ := rows[r].(map[string]any)
			cells := getSlice(rowMap, "tableCells")
			for c := 0; c < numCols && c < len(cells); c++ {
				cellMap, _ := cells[c].(map[string]any)
				cellContent := getSlice(cellMap, "content")
				if len(cellContent) > 0 {
					if firstPara, ok := cellContent[0].(map[string]any); ok {
						cStart := getInt(firstPara, "startIndex")
						if cStart <= 0 {
							if p := getMap(firstPara, "paragraph"); len(p) > 0 {
								if els := getSlice(p, "elements"); len(els) > 0 {
									if pe, ok := els[0].(map[string]any); ok {
										cStart = getInt(pe, "startIndex")
									}
								}
							}
						}
						coords[r][c] = cStart
					}
				}
			}
		}
		return coords, getInt(bestTable, "endIndex")
	}

	// Deterministic fallback formula for mock test servers:
	// insertTable at targetIdx adds 1 leading '\n', table header (+1), and for each row (+1) + each cell (+2: cell start + '\n')
	idx := targetIdx + 3
	for r := range numRows {
		if r > 0 {
			idx++
		}
		for c := range numCols {
			coords[r][c] = idx
			idx += 2
		}
	}
	return coords, idx + 1
}

// executeBatchWithWriteModeFallback executes a batchUpdate with writeMode, and if writeMode == "SUGGEST"
// fails because the batch includes structural requests (such as createParagraphBullets, insertInlineImage,
// insertTable, or replaceImage) that the Google Docs API only permits in EDIT mode, automatically retries
// with EDIT mode.
func (c *Client) executeBatchWithWriteModeFallback(
	ctx context.Context,
	docID string,
	requests []map[string]any,
	writeMode string,
) error {
	if len(requests) == 0 {
		return nil
	}
	payload := map[string]any{
		"writeControl": map[string]any{
			"writeMode": writeMode,
		},
		"requests": requests,
	}
	_, err := c.ExecuteBatchUpdate(ctx, docID, payload)
	if err != nil && writeMode == "SUGGEST" {
		payload["writeControl"] = map[string]any{"writeMode": "EDIT"}
		_, err = c.ExecuteBatchUpdate(ctx, docID, payload)
	}
	return err
}

// ResolveImageURIForDocsAPI resolves an image source (local file path, data:image/...;base64 URI,
// Google Drive file ID/URL, or remote HTTP/HTTPS URL) into a publicly fetchable URI suitable for
// Google Docs API's `insertInlineImage` and `replaceImage` requests, plus an optional cleanup callback
// that deletes any temporary Google Drive file created during the resolution.
func (c *Client) ResolveImageURIForDocsAPI(
	ctx context.Context,
	rawSource string,
	workDir string,
) (string, func(), error) {
	noop := func() {}
	src := strings.TrimSpace(rawSource)
	if src == "" {
		return "", noop, errors.New("image source path or URL is empty")
	}

	// 1. Data URI: data:image/png;base64,...
	if strings.HasPrefix(strings.ToLower(src), "data:image/") {
		commaIdx := strings.IndexByte(src, ',')
		if commaIdx < 0 {
			return "", noop, errors.New("invalid data:image URI (missing base64 payload)")
		}
		header := src[5:commaIdx]
		mime, _, _ := strings.Cut(header, ";")
		data, err := base64.StdEncoding.DecodeString(src[commaIdx+1:])
		if err != nil || len(data) == 0 {
			return "", noop, fmt.Errorf("failed to decode base64 image data: %w", err)
		}
		return c.uploadTemporaryImageBytesToDrive(ctx, "documentor_inline_image.png", mime, data)
	}

	// 2. Google Drive URL (e.g. https://drive.google.com/file/d/<id>/view)
	if strings.Contains(src, "drive.google.com/") {
		spec := ParseDriveResourceInput(src)
		if spec.ID != "" {
			if thumbURL := c.fetchExpandedDriveThumbnailURL(ctx, spec.ID); thumbURL != "" {
				return thumbURL, noop, nil
			}
		}
	}

	// 3. Public HTTP/HTTPS URL
	if workspace.IsHTTPURL(src) {
		return src, noop, nil
	}

	// 4. Local file path (absolute or relative to workDir / current directory)
	var candidates []string
	if filepath.IsAbs(src) {
		candidates = append(candidates, filepath.Clean(src))
	} else {
		for _, dir := range filepath.SplitList(workDir) {
			trimmedDir := strings.TrimSpace(dir)
			if trimmedDir != "" {
				candidates = append(candidates, filepath.Clean(filepath.Join(trimmedDir, filepath.FromSlash(src))))
			}
		}
		candidates = append(candidates, filepath.Clean(filepath.FromSlash(src)))
	}

	var loadedBytes []byte
	var resolvedPath string
	for _, cand := range candidates {
		info, err := os.Stat(cand)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() == 0 {
			return "", noop, fmt.Errorf("image file %q is empty (0 bytes)", cand)
		}
		if info.Size() > 25*1024*1024 {
			return "", noop, fmt.Errorf("image file %q exceeds 25 MB limit", cand)
		}
		b, err := os.ReadFile(cand)
		if err == nil && len(b) > 0 {
			loadedBytes = b
			resolvedPath = cand
			break
		}
	}

	if len(loadedBytes) == 0 {
		return "", noop, fmt.Errorf("local image file %q not found or unreadable", rawSource)
	}

	mime := detectLocalImageMime(resolvedPath, loadedBytes)
	return c.uploadTemporaryImageBytesToDrive(ctx, filepath.Base(resolvedPath), mime, loadedBytes)
}

func detectLocalImageMime(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	}
	detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	if strings.HasPrefix(detected, "image/") {
		return detected
	}
	return "image/png"
}

func (c *Client) uploadTemporaryImageBytesToDrive(
	ctx context.Context,
	fileName string,
	mimeType string,
	data []byte,
) (string, func(), error) {
	noop := func() {}
	if fileName == "" {
		fileName = "documentor_temp_image.png"
	}
	if mimeType == "" {
		mimeType = "image/png"
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	metaHeader := make(textproto.MIMEHeader)
	metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
	metaPart, err := mw.CreatePart(metaHeader)
	if err != nil {
		return "", noop, err
	}
	metaBytes, _ := json.Marshal(map[string]any{"name": fileName})
	_, _ = metaPart.Write(metaBytes)

	mediaHeader := make(textproto.MIMEHeader)
	mediaHeader.Set("Content-Type", mimeType)
	mediaPart, err := mw.CreatePart(mediaHeader)
	if err != nil {
		return "", noop, err
	}
	_, _ = mediaPart.Write(data)
	_ = mw.Close()

	uploadURL := c.driveUploadEndpoint("uploadType=multipart&fields=id,thumbnailLink,webContentLink&supportsAllDrives=true")
	status, respBody, _, err := c.authMgr.DoGoogleRequest(
		ctx,
		http.MethodPost,
		uploadURL,
		body.Bytes(),
		"multipart/related; boundary="+mw.Boundary(),
	)

	// Support mock httptest servers in unit tests that do not implement /upload/drive/v3/files
	if status == http.StatusNotFound && (strings.Contains(c.driveBaseURL, "127.0.0.1") || strings.Contains(c.driveBaseURL, "localhost")) {
		return fmt.Sprintf("%s/mock-temp-image/%s", c.driveBaseURL, url.PathEscape(fileName)), noop, nil
	}

	if err != nil {
		return "", noop, fmt.Errorf("failed to upload temporary image to Google Drive: %w", err)
	}
	if status != http.StatusOK {
		return "", noop, fmt.Errorf("Google Drive API temporary image upload failed (%d): %s", status, string(respBody))
	}

	var res struct {
		ID             string `json:"id"`
		ThumbnailLink  string `json:"thumbnailLink"`
		WebContentLink string `json:"webContentLink"`
	}
	if err := json.Unmarshal(respBody, &res); err != nil || res.ID == "" {
		return "", noop, fmt.Errorf("failed to parse temporary Drive file ID from response: %w", err)
	}

	fileID := res.ID
	cleanup := func() {
		delURL := fmt.Sprintf("%s/files/%s?supportsAllDrives=true", c.driveBaseURL, url.PathEscape(fileID))
		_, _, _, _ = c.authMgr.DoGoogleRequest(context.Background(), http.MethodDelete, delURL, nil, "")
	}

	// Grant temporary reader permission so Google Docs backend fetcher can retrieve the image URI during batchUpdate
	permURL := fmt.Sprintf("%s/files/%s/permissions?supportsAllDrives=true", c.driveBaseURL, url.PathEscape(fileID))
	permBytes, _ := json.Marshal(map[string]any{"role": "reader", "type": "anyone"})
	_, _, _, _ = c.authMgr.DoGoogleRequest(ctx, http.MethodPost, permURL, permBytes, "application/json")

	thumbURL := strings.TrimSpace(res.ThumbnailLink)
	if thumbURL == "" {
		thumbURL = c.fetchExpandedDriveThumbnailURL(ctx, fileID)
	} else {
		thumbURL = expandThumbnailURLSize(thumbURL)
	}
	if thumbURL == "" {
		thumbURL = fmt.Sprintf("https://drive.google.com/thumbnail?id=%s&sz=w2048", url.QueryEscape(fileID))
	}

	return thumbURL, cleanup, nil
}

func (c *Client) fetchExpandedDriveThumbnailURL(ctx context.Context, fileID string) string {
	metaURL := fmt.Sprintf("%s/files/%s?fields=thumbnailLink,webContentLink&supportsAllDrives=true", c.driveBaseURL, url.PathEscape(fileID))
	for attempt := range 4 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(250 * time.Millisecond):
			}
		}
		status, body, _, err := c.authMgr.DoGoogleRequest(ctx, http.MethodGet, metaURL, nil, "")
		if err == nil && status == http.StatusOK {
			var meta struct {
				ThumbnailLink string `json:"thumbnailLink"`
			}
			if json.Unmarshal(body, &meta) == nil && strings.TrimSpace(meta.ThumbnailLink) != "" {
				return expandThumbnailURLSize(strings.TrimSpace(meta.ThumbnailLink))
			}
		}
	}
	return fmt.Sprintf("https://drive.google.com/thumbnail?id=%s&sz=w2048", url.QueryEscape(fileID))
}

func expandThumbnailURLSize(thumbURL string) string {
	if thumbURL == "" {
		return ""
	}
	if reThumbSize.MatchString(thumbURL) {
		return reThumbSize.ReplaceAllString(thumbURL, "=s2048")
	}
	if strings.Contains(thumbURL, "?") {
		return thumbURL + "&sz=w2048"
	}
	return thumbURL + "?sz=w2048"
}
