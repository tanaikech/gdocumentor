package utf16idx

import (
	"sort"
	"strings"
)

// EditableBlock represents a contiguous settled text block in a Google Document
// along with its exact Google Docs UTF-16 coordinate span and optional local file mapping metadata.
type EditableBlock struct {
	Text           string `json:"text"`
	StartIndex     int    `json:"startIndex"`
	EndIndex       int    `json:"endIndex"`
	Source         string `json:"source"` // "body" or "table"
	FilePath       string `json:"filePath,omitempty"`
	LineNumber     int    `json:"lineNumber,omitempty"`     // 1-based line number in local file
	FileRuneStart  int    `json:"fileRuneStart,omitempty"`  // 0-based rune offset in local file
	FileRuneEnd    int    `json:"fileRuneEnd,omitempty"`    // 0-based rune offset in local file
	TargetEditable bool   `json:"targetEditable"`           // true if within target proofreading scope
}

// RawSuggestion represents a text revision or hint proposed by Gemini.
type RawSuggestion struct {
	OriginalText  string `json:"originalText"`
	SuggestedText string `json:"suggestedText"`
	Category      string `json:"category"`
	Reason        string `json:"reason"`
	LearningPoint string `json:"learningPoint"`
}

// MatchedSuggestion represents a suggestion resolved to exact Google Docs UTF-16 coordinates.
type MatchedSuggestion struct {
	ID            string `json:"id"`
	OriginalText  string `json:"originalText"`
	SuggestedText string `json:"suggestedText"`
	Category      string `json:"category"`
	Reason        string `json:"reason"`
	LearningPoint string `json:"learningPoint"`
	StartIndex    int    `json:"startIndex"`
	EndIndex      int    `json:"endIndex"`
	CtxBefore     string `json:"ctxBefore,omitempty"`
	CtxAfter      string `json:"ctxAfter,omitempty"`
	TabID         string `json:"tabId,omitempty"`
	HintOnly      bool   `json:"hintOnly"`
	FilePath      string `json:"filePath,omitempty"`
	LineNumber    int    `json:"lineNumber,omitempty"`
	AnchorStatus  string `json:"anchorStatus,omitempty"` // "exact", "reanchored", "stale_superseded", "stale_ambiguous", "stale_out_of_scope"
	ShiftDelta    int    `json:"shiftDelta,omitempty"`
}

// RangeInterval represents a half-open UTF-16 character interval [StartIndex, EndIndex).
type RangeInterval struct {
	StartIndex int `json:"startIndex"`
	EndIndex   int `json:"endIndex"`
}

// UTF16Len calculates the exact number of UTF-16 code units required to encode s.
// Characters in the Basic Multilingual Plane (<= U+FFFF) count as 1 unit;
// supplementary plane characters (> U+FFFF, surrogate pairs like emojis) count as 2 units.
func UTF16Len(s string) int {
	units := 0
	for _, r := range s {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// ByteOffsetToUTF16Offset converts a valid UTF-8 byte offset within s into its UTF-16 code unit offset.
func ByteOffsetToUTF16Offset(s string, byteOffset int) int {
	if byteOffset <= 0 {
		return 0
	}
	if byteOffset >= len(s) {
		return UTF16Len(s)
	}
	return UTF16Len(s[:byteOffset])
}

// RuneOffsetToUTF16Offset converts a 0-based rune index within s into its UTF-16 code unit offset.
func RuneOffsetToUTF16Offset(s string, runeOffset int) int {
	if runeOffset <= 0 {
		return 0
	}
	units := 0
	idx := 0
	for _, r := range s {
		if idx >= runeOffset {
			break
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
		idx++
	}
	return units
}

// UTF16OffsetToByteOffset converts a UTF-16 code unit offset within s to the nearest UTF-8 byte offset.
func UTF16OffsetToByteOffset(s string, utf16Offset int) int {
	if utf16Offset <= 0 {
		return 0
	}
	units := 0
	for byteIdx, r := range s {
		if units >= utf16Offset {
			return byteIdx
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return len(s)
}

// FindSubstringUTF16 searches for needle within haystack starting at or after searchFromByteOffset.
// If found, it returns the UTF-16 code unit [startUTF16, endUTF16) relative to haystack and the next byte offset.
func FindSubstringUTF16(haystack, needle string, searchFromByteOffset int) (startUTF16, endUTF16, matchedBytePos int, found bool) {
	if needle == "" || searchFromByteOffset < 0 || searchFromByteOffset >= len(haystack) {
		return 0, 0, -1, false
	}
	relBytePos := strings.Index(haystack[searchFromByteOffset:], needle)
	if relBytePos == -1 {
		return 0, 0, -1, false
	}
	absBytePos := searchFromByteOffset + relBytePos
	startUTF16 = ByteOffsetToUTF16Offset(haystack, absBytePos)
	endUTF16 = startUTF16 + UTF16Len(needle)
	return startUTF16, endUTF16, absBytePos, true
}

// RangesOverlap returns true if any interval in rangesA overlaps with any interval in rangesB.
func RangesOverlap(rangesA, rangesB []RangeInterval) bool {
	if len(rangesA) == 0 || len(rangesB) == 0 {
		return false
	}
	for _, a := range rangesA {
		for _, b := range rangesB {
			if a.StartIndex < b.EndIndex && a.EndIndex > b.StartIndex {
				return true
			}
		}
	}
	return false
}

// MatchSuggestionsToBlocks resolves each AI-proposed RawSuggestion against the target EditableBlocks,
// prevents overlapping ranges, computes exact Google Docs UTF-16 coordinates, and sorts results
// in descending StartIndex order (bottom-to-top) so batch updates never shift earlier offsets.
func MatchSuggestionsToBlocks(blocks []EditableBlock, suggestions []RawSuggestion, tabID string, hintOnly bool, defaultCategory string) []MatchedSuggestion {
	return MatchSuggestionsToBlocksWithBuffer(blocks, nil, suggestions, tabID, hintOnly, defaultCategory)
}

// MatchSuggestionsToBlocksWithBuffer resolves AI-proposed RawSuggestions against EditableBlocks while extracting
// cross-line surrogate-pair-safe 32-UTF-16-unit context snapshots from settledBuf (synthesizing one from blocks if nil).
func MatchSuggestionsToBlocksWithBuffer(blocks []EditableBlock, settledBuf *SettledUTF16Buffer, suggestions []RawSuggestion, tabID string, hintOnly bool, defaultCategory string) []MatchedSuggestion {
	if defaultCategory == "" {
		defaultCategory = "Revision"
	}
	if settledBuf == nil {
		settledBuf = BuildSettledBufferFromBlocks(blocks)
	}

	var matched []MatchedSuggestion
	var usedRanges []RangeInterval

	for idx, sug := range suggestions {
		orig := strings.TrimSpace(sug.OriginalText)
		repl := sug.SuggestedText
		if orig == "" || (!hintOnly && orig == repl) {
			continue
		}

		foundRange, matchedBlock, ctxBefore, ctxAfter := findCandidateInBlocks(blocks, orig, usedRanges)
		if foundRange == nil {
			// Fallback for Markdown files converted to Google Docs (where inline markers like **, `, or leading # / - are stripped)
			fbOrig, fbRepl := stripMarkdownForConvertedDocMatch(orig, repl, hintOnly)
			if fbOrig != "" && fbOrig != orig && (hintOnly || fbOrig != fbRepl) {
				foundRange, matchedBlock, ctxBefore, ctxAfter = findCandidateInBlocks(blocks, fbOrig, usedRanges)
				if foundRange != nil {
					orig = fbOrig
					repl = fbRepl
				}
			}
		}
		if foundRange != nil {
			usedRanges = append(usedRanges, *foundRange)
			if settledBuf != nil {
				if sbBefore, sbAfter, _, ok := settledBuf.SliceContextAroundDocSpan(foundRange.StartIndex, foundRange.EndIndex, ContextAnchorUTF16Units); ok {
					ctxBefore = sbBefore
					ctxAfter = sbAfter
				}
			}
		}

		if foundRange != nil {
			cat := strings.TrimSpace(sug.Category)
			if cat == "" {
				cat = defaultCategory
			}
			item := MatchedSuggestion{
				ID:            formatSuggestionItemID(idx + 1),
				OriginalText:  orig,
				SuggestedText: repl,
				Category:      cat,
				Reason:        sug.Reason,
				LearningPoint: sug.LearningPoint,
				StartIndex:    foundRange.StartIndex,
				EndIndex:      foundRange.EndIndex,
				CtxBefore:     ctxBefore,
				CtxAfter:      ctxAfter,
				TabID:         tabID,
				HintOnly:      hintOnly,
				AnchorStatus:  AnchorStatusExact,
			}
			if matchedBlock != nil {
				item.FilePath = matchedBlock.FilePath
				item.LineNumber = matchedBlock.LineNumber
			}
			matched = append(matched, item)
		}
	}

	// Sort in descending StartIndex order (bottom-to-top)
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].StartIndex > matched[j].StartIndex
	})

	return matched
}

func findCandidateInBlocks(blocks []EditableBlock, orig string, usedRanges []RangeInterval) (*RangeInterval, *EditableBlock, string, string) {
	for bIdx := range blocks {
		block := &blocks[bIdx]
		if !block.TargetEditable {
			continue
		}

		searchByteFrom := 0
		for searchByteFrom < len(block.Text) {
			relStartUTF16, relEndUTF16, bytePos, ok := FindSubstringUTF16(block.Text, orig, searchByteFrom)
			if !ok {
				break
			}

			absStart := block.StartIndex + relStartUTF16
			absEnd := block.StartIndex + relEndUTF16
			candidate := RangeInterval{StartIndex: absStart, EndIndex: absEnd}

			if !RangesOverlap([]RangeInterval{candidate}, usedRanges) {
				beforeRunes := []rune(block.Text[:bytePos])
				if len(beforeRunes) > 28 {
					beforeRunes = beforeRunes[len(beforeRunes)-28:]
				}
				ctxBefore := string(beforeRunes)
				ctxAfter := ""
				afterByteStart := bytePos + len(orig)
				if afterByteStart < len(block.Text) {
					afterRunes := []rune(block.Text[afterByteStart:])
					if len(afterRunes) > 28 {
						afterRunes = afterRunes[:28]
					}
					ctxAfter = string(afterRunes)
				}
				return &candidate, block, ctxBefore, ctxAfter
			}
			// Advance past the first rune at bytePos to look for subsequent occurrences
			_, runeWidth := decodeFirstRuneWidth(block.Text[bytePos:])
			searchByteFrom = bytePos + runeWidth
		}
	}
	return nil, nil, "", ""
}

func stripMarkdownForConvertedDocMatch(orig, repl string, hintOnly bool) (string, string) {
	stripLinks := func(s string) string {
		for {
			openBracket := strings.IndexByte(s, '[')
			if openBracket < 0 {
				break
			}
			closeBracket := strings.Index(s[openBracket:], "](")
			if closeBracket < 0 {
				break
			}
			closeBracket += openBracket
			closeParen := strings.IndexByte(s[closeBracket+2:], ')')
			if closeParen < 0 {
				break
			}
			closeParen += closeBracket + 2
			if openBracket > 0 && s[openBracket-1] == '!' {
				break
			}
			label := s[openBracket+1 : closeBracket]
			s = s[:openBracket] + label + s[closeParen+1:]
		}
		return s
	}
	stripInline := func(s string) string {
		trimmed := strings.TrimSpace(s)
		for _, prefix := range []string{"###### ", "##### ", "#### ", "### ", "## ", "# ", "- ", "* ", "> "} {
			if strings.HasPrefix(trimmed, prefix) {
				trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
				break
			}
		}
		trimmed = stripLinks(trimmed)
		trimmed = strings.ReplaceAll(trimmed, "**", "")
		trimmed = strings.ReplaceAll(trimmed, "__", "")
		trimmed = strings.ReplaceAll(trimmed, "`", "")
		return strings.TrimSpace(trimmed)
	}
	fbOrig := stripInline(orig)
	fbRepl := repl
	if !hintOnly {
		fbRepl = stripInline(repl)
	}
	return fbOrig, fbRepl
}

func decodeFirstRuneWidth(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

func formatSuggestionItemID(n int) string {
	const digits = "0123456789"
	if n <= 0 {
		return "sug_item_0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return "sug_item_" + string(buf[i:])
}
