package gdocs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/tanaikech/gdocumentor/internal/utf16idx"
	"github.com/tanaikech/gdocumentor/internal/workspace"
)

// EditOperationType identifies the kind of direct Google Document edit operation performed.
type EditOperationType string

const (
	EditOpCreate         EditOperationType = "create"
	EditOpDeleteDoc      EditOperationType = "delete-doc"
	EditOpInsert         EditOperationType = "insert"
	EditOpInsertMarkdown EditOperationType = "insert-markdown"
	EditOpReplaceText    EditOperationType = "replace-text"
	EditOpReplaceRange   EditOperationType = "replace-range"
	EditOpReplaceBody    EditOperationType = "replace-body"
	EditOpDeleteRange    EditOperationType = "delete-range"
	EditOpDeleteText     EditOperationType = "delete-text"
	EditOpClearBody      EditOperationType = "clear-body"
	EditOpInsertImage    EditOperationType = "insert-image"
	EditOpReplaceImage   EditOperationType = "replace-image"
	EditOpDeleteImage    EditOperationType = "delete-image"
)

// EditDocumentRequest specifies the parameters for creating, editing, replacing, or deleting content in a Google Document.
type EditDocumentRequest struct {
	DocID           string // Target Google Document ID or URL (optional when CreateDoc is true)
	TabID           string // Target tab ID (optional; defaults to first content tab)
	CreateDoc       bool   // Create a new Google Document
	DeleteDoc       bool   // Permanently delete the target Google Document via Drive API
	Title           string // Title when creating a new Google Document
	ParentFolder    string // Optional Drive parent folder ID/URL when creating a new Google Document
	UseDriveConvert bool   // When creating a new Doc from a local file (.md, .docx, .pdf), convert via Drive API
	ConvertMimeType string
	ConvertBytes    []byte
	WorkDir         string // Working directory for resolving relative image paths

	// Insert / Write parameters
	InsertText      string // Text to insert or write (from --text, -i file, pipe, here-string, heredoc, or redirect)
	HasInsertText   bool   // True if insert/replacement text was provided
	Markdown        bool   // Parse and convert InsertText/ReplaceText as rich Markdown elements (headings, bold, lists, tables, images, etc.)
	Position        string // "start" / "prepend", "end" / "append" (default when inserting into existing doc), or "@idx" / paragraph number
	AtSpec          string // Explicit target location: "@150" (UTF-16 index), "3" (paragraph #3), "start", "end"
	BeforeText      string // Insert immediately before the matched text phrase
	AfterText       string // Insert immediately after the matched text phrase
	BeforeParagraph int    // Insert at the start of 1-based paragraph number
	AfterParagraph  int    // Insert at the end of 1-based paragraph number
	ParagraphStyle  string // Optional named paragraph style for inserted text (e.g. "HEADING_1", "HEADING_2", "NORMAL_TEXT")

	// Replace parameters
	FindText     string // Substring/phrase to search and replace
	ReplaceText  string // Replacement text for --find or --replace-range
	HasReplace   bool   // True if --replace was explicitly specified (even if empty string "")
	ReplaceRange string // Range spec ("@100:250", "2-4", "all", or phrase) to replace with ReplaceText/InsertText
	ReplaceBody  bool   // Replace the entire body of the target tab
	MatchCase    bool   // Case-sensitive search for FindText / DeleteText / BeforeText / AfterText (default true)
	Occurrence   int    // 1-based occurrence index to target (0 = all occurrences for replace/delete-text, 1 = first for before/after)

	// Delete parameters
	DeleteRange string // Range spec ("@100:250", "3", "2-5", "all", or phrase) to delete from the document
	DeleteText  string // Specific substring/phrase to delete from the document
	ClearBody   bool   // Clear all body text in the target tab

	// Image parameters
	InsertImage  string  // Local image file path or URL to insert at the target position
	ReplaceImage string  // Target image identifier ("VISUAL_1", "1", "kix.xxx", "@14", or "all") to replace or resize
	ResizeImage  string  // Target image identifier ("VISUAL_1", "1", "kix.xxx", "@14", or "all") to resize in place
	WithImage    string  // Local image file path or URL to replace the target image with (optional when resizing via ImageWidth/ImageHeight)
	DeleteImage  string  // Target image identifier ("VISUAL_1", "1", "kix.xxx", "@14", or "all") to delete
	ImageWidth   float64 // Optional width in PT for inserted/replaced/resized images
	ImageHeight  float64 // Optional height in PT for inserted/replaced/resized images

	// Scope filter
	RangeSpec string // Optional range restriction ("@100:500", "2-5", etc.) for --find / --delete-text / --before / --after

	// Write mode
	SuggestMode bool // If true, use writeMode = "SUGGEST" instead of "EDIT"
}

// EditRangeSummary describes a UTF-16 coordinate interval affected by an edit operation.
type EditRangeSummary struct {
	StartIndex int    `json:"startIndex"`
	EndIndex   int    `json:"endIndex"`
	Preview    string `json:"preview,omitempty"`
}

// EditDocumentResult holds the structured outcome of an EditDocument execution.
type EditDocumentResult struct {
	DocumentID         string             `json:"documentId"`
	DocumentURL        string             `json:"documentUrl"`
	DocumentTitle      string             `json:"documentTitle,omitempty"`
	ActiveTabID        string             `json:"activeTabId,omitempty"`
	CreatedNewDocument bool               `json:"createdNewDocument,omitempty"`
	DeletedDocument    bool               `json:"deletedDocument,omitempty"`
	Operations         []string           `json:"operations"`
	WriteMode          string             `json:"writeMode"`
	AffectedCount      int                `json:"affectedCount"`
	InsertedChars      int                `json:"insertedChars,omitempty"`
	DeletedChars       int                `json:"deletedChars,omitempty"`
	AffectedRanges     []EditRangeSummary `json:"affectedRanges,omitempty"`
	SummaryMessage     string             `json:"summaryMessage"`
}

// indexedUTF16Buffer maps a contiguous UTF-16 slice extracted from a Google Document tab
// back to exact 1-based Google Docs UTF-16 document coordinates.
type indexedUTF16Buffer struct {
	units      []uint16
	docIndices []int
	maxBodyEnd int // The endIndex of the last structural element in the tab body (maxBodyEnd-1 is the trailing segment newline)
}

// buildIndexedUTF16Buffer walks the target tab's body content and constructs an exact UTF-16 coordinate map
// for all settled (non-deleted) text runs and inline objects.
func buildIndexedUTF16Buffer(docJSON map[string]any, preferredTabID string) (string, string, *indexedUTF16Buffer) {
	activeTabID, activeTabTitle, docTab := resolveTargetDocumentTab(docJSON, preferredTabID)
	bodyContent := getSlice(getMap(docTab, "body"), "content")

	buf := &indexedUTF16Buffer{
		maxBodyEnd: 2,
	}

	var walkElements func(elements []any)
	walkElements = func(elements []any) {
		for _, rawSt := range elements {
			st, ok := rawSt.(map[string]any)
			if !ok {
				continue
			}
			if stEnd := getInt(st, "endIndex"); stEnd > buf.maxBodyEnd {
				buf.maxBodyEnd = stEnd
			}
			if para := getMap(st, "paragraph"); len(para) > 0 {
				for _, rawPE := range getSlice(para, "elements") {
					pe, ok := rawPE.(map[string]any)
					if !ok {
						continue
					}
					if peEnd := getInt(pe, "endIndex"); peEnd > buf.maxBodyEnd {
						buf.maxBodyEnd = peEnd
					}
					// Record 0xFFFC (Unicode Object Replacement Character, 1 UTF-16 unit) for inlineObjectElement
					// so coordinate and trailing-newline checks around inline images remain 100% accurate.
					if inlineObj := getMap(pe, "inlineObjectElement"); len(inlineObj) > 0 {
						if len(getStringSlice(inlineObj, "suggestedDeletionIds")) == 0 {
							startIdx := getInt(pe, "startIndex")
							if startIdx <= 0 {
								startIdx = 1
							}
							buf.units = append(buf.units, 0xFFFC)
							buf.docIndices = append(buf.docIndices, startIdx)
						}
						continue
					}
					tr := getMap(pe, "textRun")
					if len(tr) == 0 {
						continue
					}
					// Skip text runs marked as suggested deletions
					if len(getStringSlice(tr, "suggestedDeletionIds")) > 0 {
						continue
					}
					text := getString(tr, "content")
					if text == "" {
						continue
					}
					startIdx := getInt(pe, "startIndex")
					if startIdx <= 0 {
						startIdx = 1
					}
					u16 := utf16.Encode([]rune(text))
					for offset, unit := range u16 {
						buf.units = append(buf.units, unit)
						buf.docIndices = append(buf.docIndices, startIdx+offset)
					}
				}
			} else if tbl := getMap(st, "table"); len(tbl) > 0 {
				for _, rawRow := range getSlice(tbl, "tableRows") {
					row, ok := rawRow.(map[string]any)
					if !ok {
						continue
					}
					for _, rawCell := range getSlice(row, "tableCells") {
						cell, ok := rawCell.(map[string]any)
						if !ok {
							continue
						}
						walkElements(getSlice(cell, "content"))
					}
				}
			}
		}
	}

	walkElements(bodyContent)
	return activeTabID, activeTabTitle, buf
}

// unitAtDocIndex returns the UTF-16 code unit at the 1-based Google Docs coordinate docIdx, if present.
func (buf *indexedUTF16Buffer) unitAtDocIndex(docIdx int) (uint16, bool) {
	if buf == nil || docIdx < 1 {
		return 0, false
	}
	for i, idx := range buf.docIndices {
		if idx == docIdx {
			return buf.units[i], true
		}
	}
	return 0, false
}

// findSubstringIntervals finds all non-overlapping occurrences of needle in buf
// (optionally restricted to scopeIntervals and specific 1-based occurrence number).
func (buf *indexedUTF16Buffer) findSubstringIntervals(
	needle string,
	matchCase bool,
	scopeIntervals []utf16idx.RangeInterval,
	occurrence int,
) []utf16idx.RangeInterval {
	if needle == "" || len(buf.units) == 0 {
		return nil
	}
	searchHaystack := string(utf16.Decode(buf.units))
	searchNeedle := needle
	if !matchCase {
		searchHaystack = strings.ToLower(searchHaystack)
		searchNeedle = strings.ToLower(needle)
	}

	hayUnits := utf16.Encode([]rune(searchHaystack))
	needleUnits := utf16.Encode([]rune(searchNeedle))
	nLen := len(needleUnits)
	if nLen == 0 || nLen > len(hayUnits) {
		return nil
	}

	// Guard against case-folding length differences by falling back to direct unit comparison if lengths differ
	if len(hayUnits) != len(buf.units) {
		hayUnits = make([]uint16, len(buf.units))
		for i, u := range buf.units {
			r := rune(u)
			if !matchCase && r >= 'A' && r <= 'Z' {
				r = r + ('a' - 'A')
			}
			hayUnits[i] = uint16(r)
		}
		needleUnits = utf16.Encode([]rune(strings.ToLower(needle)))
		nLen = len(needleUnits)
	}

	var matches []utf16idx.RangeInterval
	matchCount := 0
	i := 0
	for i <= len(hayUnits)-nLen {
		matched := true
		for j := 0; j < nLen; j++ {
			if hayUnits[i+j] != needleUnits[j] {
				matched = false
				break
			}
		}
		if !matched {
			i++
			continue
		}

		docStart := buf.docIndices[i]
		docEnd := buf.docIndices[i+nLen-1] + 1
		interval := utf16idx.RangeInterval{StartIndex: docStart, EndIndex: docEnd}

		// Ensure the matched span is contiguous in document coordinates (does not jump across table cell boundaries)
		if docEnd-docStart == nLen {
			inScope := true
			if len(scopeIntervals) > 0 {
				inScope = utf16idx.RangesOverlap([]utf16idx.RangeInterval{interval}, scopeIntervals)
			}
			if inScope {
				matchCount++
				if occurrence <= 0 || matchCount == occurrence {
					matches = append(matches, interval)
				}
			}
		}
		i += nLen
	}
	return matches
}

// resolveTargetVisualElements resolves a visual element selector ("VISUAL_1", "1", "kix.xxx", "@14", or "all")
// against the target Google Document tab's VisualElements list.
func resolveTargetVisualElements(
	docJSON map[string]any,
	tabID string,
	targetSpec string,
) ([]VisualElement, error) {
	trimmed := strings.TrimSpace(targetSpec)
	if trimmed == "" {
		return nil, errors.New("empty image target specification")
	}

	fullDoc := ParseDocumentStructure(docJSON, ParseOptions{
		PreferredTabID:        tabID,
		SelectionOnly:         false,
		SkipDecisionReconcile: true,
	})
	if len(fullDoc.VisualElements) == 0 {
		return nil, errors.New("no images or figures (VISUAL_N) were found in the target Google Document")
	}

	lower := strings.ToLower(trimmed)
	if lower == "all" || lower == "*" {
		out := make([]VisualElement, len(fullDoc.VisualElements))
		copy(out, fullDoc.VisualElements)
		return out, nil
	}

	// 1. Match by TargetID ("VISUAL_1", "visual_1") or ObjectID ("kix.xxx")
	for _, v := range fullDoc.VisualElements {
		if strings.EqualFold(v.TargetID, trimmed) || v.ObjectID == trimmed {
			return []VisualElement{v}, nil
		}
	}

	// 2. Match by "@startIndex" or "@startIndex:endIndex"
	if after, ok := strings.CutPrefix(trimmed, "@"); ok {
		idxPart := after
		if colon := strings.IndexByte(idxPart, ':'); colon > 0 {
			idxPart = idxPart[:colon]
		}
		if targetIdx, err := strconv.Atoi(strings.TrimSpace(idxPart)); err == nil && targetIdx >= 1 {
			for _, v := range fullDoc.VisualElements {
				if v.StartIndex == targetIdx {
					return []VisualElement{v}, nil
				}
			}
		}
	}

	// 3. Match by 1-based visual number ("1", "#1", "v1")
	cleanNum := strings.TrimPrefix(strings.TrimPrefix(lower, "visual_"), "#")
	cleanNum = strings.TrimPrefix(cleanNum, "v")
	if vNum, err := strconv.Atoi(cleanNum); err == nil && vNum >= 1 {
		if vNum > len(fullDoc.VisualElements) {
			return nil, fmt.Errorf("image target %q is out of bounds (document has %d visual element(s); run 'gdocumentor inspect -d <doc>' to check)", targetSpec, len(fullDoc.VisualElements))
		}
		return []VisualElement{fullDoc.VisualElements[vNum-1]}, nil
	}

	// 4. Match by title substring
	for _, v := range fullDoc.VisualElements {
		if v.Title != "" && strings.EqualFold(v.Title, trimmed) {
			return []VisualElement{v}, nil
		}
	}

	return nil, fmt.Errorf("could not find image %q in document (specify 'VISUAL_1', '1', objectId, '@index', or 'all'; run 'gdocumentor inspect -d <doc>' to list images)", targetSpec)
}

// resolveEditRangeSpec resolves a range specification ("all", "@100:250", "3", "2-5", or text phrase)
// into exact Google Docs UTF-16 RangeIntervals.
// When forDeletion is true and a paragraph range ("3" or "2-5") is specified, it includes the paragraph's
// trailing newline (or preceding newline if at the very end of the body segment) so deleting a paragraph
// does not leave an orphan blank line or trigger Google Docs API's segment-end newline error.
func resolveEditRangeSpec(
	docJSON map[string]any,
	tabID string,
	buf *indexedUTF16Buffer,
	rangeSpec string,
	forDeletion bool,
	matchCase bool,
) ([]utf16idx.RangeInterval, error) {
	trimmed := strings.TrimSpace(rangeSpec)
	if trimmed == "" {
		return nil, errors.New("empty range specification")
	}

	maxDeletableEnd := max(buf.maxBodyEnd-1, 1)

	lower := strings.ToLower(trimmed)
	if lower == "all" || lower == "body" || lower == "*" {
		if maxDeletableEnd <= 1 {
			return nil, nil
		}
		return []utf16idx.RangeInterval{{StartIndex: 1, EndIndex: maxDeletableEnd}}, nil
	}

	// Single integer paragraph number e.g. "3" or "L3" or "#3"
	cleanNum := strings.TrimPrefix(strings.TrimPrefix(lower, "l"), "#")
	if singlePara, err := strconv.Atoi(cleanNum); err == nil && singlePara > 0 && !strings.Contains(trimmed, ":") && !strings.HasPrefix(trimmed, "@") {
		trimmed = fmt.Sprintf("%d-%d", singlePara, singlePara)
	}

	if tf, err := workspace.ParseStandaloneRange(trimmed); err == nil {
		// 1. Explicit UTF-16 character offset range "@start:end" or "start:end"
		if tf.StartChar >= 0 && tf.EndChar > tf.StartChar {
			sIdx := maxInt(1, tf.StartChar)
			eIdx := tf.EndChar
			if forDeletion && eIdx > maxDeletableEnd {
				eIdx = maxDeletableEnd
			}
			if sIdx >= eIdx {
				return nil, fmt.Errorf("UTF-16 range %q is outside the editable document body (1..%d)", rangeSpec, maxDeletableEnd)
			}
			return []utf16idx.RangeInterval{{StartIndex: sIdx, EndIndex: eIdx}}, nil
		}

		// 2. Paragraph / line number range "start-end"
		if tf.StartLine > 0 {
			fullDoc := ParseDocumentStructure(docJSON, ParseOptions{
				PreferredTabID:        tabID,
				SelectionOnly:         false,
				SkipDecisionReconcile: true,
			})
			if len(fullDoc.EditableBlocks) == 0 {
				return nil, errors.New("the target Google Document has no text paragraphs to match paragraph range")
			}
			sPara := tf.StartLine - 1
			ePara := tf.EndLine - 1
			if ePara < 0 {
				ePara = sPara
			}
			if sPara < 0 || sPara >= len(fullDoc.EditableBlocks) || ePara >= len(fullDoc.EditableBlocks) || sPara > ePara {
				return nil, fmt.Errorf("paragraph range %q is out of bounds (document has %d paragraphs; run 'gdocumentor inspect -d <doc>' to check)", rangeSpec, len(fullDoc.EditableBlocks))
			}

			sIdx := fullDoc.EditableBlocks[sPara].StartIndex
			eIdx := fullDoc.EditableBlocks[ePara].EndIndex

			if forDeletion {
				// Include the paragraph's newline if possible without touching maxBodyEnd-1
				if eIdx+1 <= maxDeletableEnd {
					eIdx = eIdx + 1
				} else if sIdx > 1 {
					// Last paragraph in the document: include the preceding newline so no blank line remains
					sIdx = sIdx - 1
				}
				if eIdx > maxDeletableEnd {
					eIdx = maxDeletableEnd
				}
			}
			if sIdx >= eIdx {
				return nil, nil
			}
			return []utf16idx.RangeInterval{{StartIndex: sIdx, EndIndex: eIdx}}, nil
		}
	}

	// 3. Fallback: substring / phrase match in document
	matches := buf.findSubstringIntervals(trimmed, matchCase, nil, 0)
	if len(matches) > 0 {
		var clamped []utf16idx.RangeInterval
		for _, m := range matches {
			s := maxInt(1, m.StartIndex)
			e := m.EndIndex
			if forDeletion && e > maxDeletableEnd {
				e = maxDeletableEnd
			}
			if e > s {
				clamped = append(clamped, utf16idx.RangeInterval{StartIndex: s, EndIndex: e})
			}
		}
		return clamped, nil
	}

	return nil, fmt.Errorf("could not resolve range %q (use '@start:end' UTF-16 offsets, 'N' or 'N-M' paragraph numbers, 'all', or an existing text phrase; run 'gdocumentor inspect -d <doc>' to inspect)", rangeSpec)
}

// resolveInsertPositionDetails determines the exact 1-based UTF-16 index where content should be inserted,
// along with whether a leading newline (to finish a preceding paragraph) or trailing newline (to separate
// from a following paragraph) is required.
func resolveInsertPositionDetails(
	docJSON map[string]any,
	tabID string,
	buf *indexedUTF16Buffer,
	req EditDocumentRequest,
	scopeIntervals []utf16idx.RangeInterval,
) (targetIdx int, leadingNL bool, trailingNL bool, isParagraphBlock bool, err error) {
	maxInsertIdx := max(buf.maxBodyEnd-1, 1)

	isMultiLineMD := req.Markdown && strings.Contains(strings.TrimSpace(req.InsertText), "\n")

	// 1. Explicit --before <phrase> (inline insertion for plain/single-line text; paragraph-separated block insertion for multi-line Markdown)
	if req.BeforeText != "" {
		occ := req.Occurrence
		if occ <= 0 {
			occ = 1
		}
		matches := buf.findSubstringIntervals(req.BeforeText, req.MatchCase, scopeIntervals, occ)
		if len(matches) == 0 {
			return 0, false, false, false, fmt.Errorf("target text for --before %q was not found in the document", req.BeforeText)
		}
		idx := matches[0].StartIndex
		if isMultiLineMD {
			leadNL := false
			if idx > 1 {
				if uPrev, ok := buf.unitAtDocIndex(idx - 1); ok && uPrev != '\n' {
					leadNL = true
				}
			}
			trailNL := true
			if uAt, ok := buf.unitAtDocIndex(idx); ok && uAt == '\n' {
				trailNL = false
			}
			return idx, leadNL, trailNL, true, nil
		}
		return idx, false, false, false, nil
	}

	// 2. Explicit --after <phrase> (inline insertion for plain/single-line text; paragraph-separated block insertion for multi-line Markdown)
	if req.AfterText != "" {
		occ := req.Occurrence
		if occ <= 0 {
			occ = 1
		}
		matches := buf.findSubstringIntervals(req.AfterText, req.MatchCase, scopeIntervals, occ)
		if len(matches) == 0 {
			return 0, false, false, false, fmt.Errorf("target text for --after %q was not found in the document", req.AfterText)
		}
		idx := min(matches[0].EndIndex, maxInsertIdx)
		if isMultiLineMD {
			leadNL := false
			if idx > 1 {
				if uPrev, ok := buf.unitAtDocIndex(idx - 1); ok && uPrev != '\n' {
					leadNL = true
				}
			}
			trailNL := true
			if uAt, ok := buf.unitAtDocIndex(idx); ok && uAt == '\n' {
				trailNL = false
			}
			return idx, leadNL, trailNL, true, nil
		}
		return idx, false, false, false, nil
	}

	// 3. Explicit --before-paragraph <N> or --after-paragraph <N>
	if req.BeforeParagraph > 0 || req.AfterParagraph > 0 {
		fullDoc := ParseDocumentStructure(docJSON, ParseOptions{
			PreferredTabID:        tabID,
			SelectionOnly:         false,
			SkipDecisionReconcile: true,
		})
		if len(fullDoc.EditableBlocks) == 0 {
			return 1, false, false, true, nil
		}
		if req.BeforeParagraph > 0 {
			pIdx := req.BeforeParagraph - 1
			if pIdx < 0 || pIdx >= len(fullDoc.EditableBlocks) {
				return 0, false, false, false, fmt.Errorf("--before-paragraph %d is out of bounds (document has %d paragraphs)", req.BeforeParagraph, len(fullDoc.EditableBlocks))
			}
			return fullDoc.EditableBlocks[pIdx].StartIndex, false, true, true, nil
		}
		pIdx := req.AfterParagraph - 1
		if pIdx < 0 || pIdx >= len(fullDoc.EditableBlocks) {
			return 0, false, false, false, fmt.Errorf("--after-paragraph %d is out of bounds (document has %d paragraphs)", req.AfterParagraph, len(fullDoc.EditableBlocks))
		}
		blkEnd := fullDoc.EditableBlocks[pIdx].EndIndex
		if blkEnd+1 <= maxInsertIdx {
			return blkEnd + 1, false, true, true, nil
		}
		return minInt(blkEnd, maxInsertIdx), true, false, true, nil
	}

	// 4. Explicit --at / --position spec
	posSpec := strings.TrimSpace(req.AtSpec)
	if posSpec == "" {
		posSpec = strings.TrimSpace(req.Position)
	}
	lowerPos := strings.ToLower(posSpec)

	switch lowerPos {
	case "start", "prepend", "top", "begin", "beginning":
		return 1, false, maxInsertIdx > 1, true, nil

	case "", "end", "append", "bottom":
		if maxInsertIdx <= 1 {
			return 1, false, false, true, nil
		}
		needsLeadingNewline := false
		if len(buf.units) > 0 {
			lastIdx := len(buf.units) - 1
			if buf.docIndices[lastIdx] == maxInsertIdx && buf.units[lastIdx] == '\n' {
				if lastIdx > 0 && buf.units[lastIdx-1] != '\n' {
					needsLeadingNewline = true
				}
			}
		}
		return maxInsertIdx, needsLeadingNewline, false, true, nil
	}

	// Check if posSpec is "@150" (UTF-16 index)
	if after, ok := strings.CutPrefix(posSpec, "@"); ok {
		idxStr := after
		if colon := strings.IndexByte(idxStr, ':'); colon > 0 {
			idxStr = idxStr[:colon]
		}
		idxVal, err := strconv.Atoi(strings.TrimSpace(idxStr))
		if err != nil || idxVal < 1 {
			return 0, false, false, false, fmt.Errorf("invalid UTF-16 index %q (must be '@<number>' >= 1, e.g. '@1' or '@150')", posSpec)
		}
		if idxVal > maxInsertIdx {
			idxVal = maxInsertIdx
		}
		return idxVal, false, false, false, nil
	}

	// Check if posSpec is a paragraph number ("3" or "#3" or "L3")
	cleanPara := strings.TrimPrefix(strings.TrimPrefix(lowerPos, "l"), "#")
	if pNum, err := strconv.Atoi(cleanPara); err == nil && pNum > 0 {
		fullDoc := ParseDocumentStructure(docJSON, ParseOptions{
			PreferredTabID:        tabID,
			SelectionOnly:         false,
			SkipDecisionReconcile: true,
		})
		if len(fullDoc.EditableBlocks) == 0 {
			return 1, false, false, true, nil
		}
		if pNum > len(fullDoc.EditableBlocks) {
			return 0, false, false, false, fmt.Errorf("paragraph #%d is out of bounds (document has %d paragraphs; run 'gdocumentor inspect -d <doc>' to check)", pNum, len(fullDoc.EditableBlocks))
		}
		return fullDoc.EditableBlocks[pNum-1].StartIndex, false, true, true, nil
	}

	return 0, false, false, false, fmt.Errorf("invalid insert position %q (use 'start', 'end', '@<utf16-index>', '<paragraph-number>', --before <text>, or --after <text>)", posSpec)
}

// resolveInsertIndex determines the exact 1-based UTF-16 index where plain text should be inserted,
// and applies leading or trailing newlines when appropriate.
func resolveInsertIndex(
	docJSON map[string]any,
	tabID string,
	buf *indexedUTF16Buffer,
	req EditDocumentRequest,
	scopeIntervals []utf16idx.RangeInterval,
) (int, string, error) {
	targetIdx, leadingNL, trailingNL, _, err := resolveInsertPositionDetails(docJSON, tabID, buf, req, scopeIntervals)
	if err != nil {
		return 0, "", err
	}
	insertText := strings.ReplaceAll(req.InsertText, "\r\n", "\n")
	if leadingNL && !strings.HasPrefix(insertText, "\n") {
		insertText = "\n" + insertText
	}
	if trailingNL && !strings.HasSuffix(insertText, "\n") {
		insertText += "\n"
	}
	return targetIdx, insertText, nil
}

// EditDocument executes direct Google Document creation, deletion, text/Markdown insertion, replacement, range deletion, or image operations.
func (c *Client) EditDocument(ctx context.Context, req EditDocumentRequest, isJa bool) (*EditDocumentResult, error) {
	writeMode := "EDIT"
	if req.SuggestMode {
		writeMode = "SUGGEST"
	}

	docID := ExtractDocumentID(req.DocID)

	// 1. Handle permanent document deletion (--delete-doc)
	if req.DeleteDoc {
		if docID == "" {
			return nil, errors.New("--delete-doc requires --doc-id (-d) with a target Google Document ID or URL")
		}
		if err := c.DeleteDocument(ctx, docID); err != nil {
			return nil, err
		}
		_ = PurgeDecisionRecordFile(docID)
		msg := fmt.Sprintf("Permanently deleted Google Document %s from Google Drive", docID)
		if isJa {
			msg = fmt.Sprintf("Google ドキュメント (%s) を Google Drive から完全に削除しました", docID)
		}
		return &EditDocumentResult{
			DocumentID:      docID,
			DocumentURL:     DocumentURL(docID),
			DeletedDocument: true,
			Operations:      []string{string(EditOpDeleteDoc)},
			WriteMode:       writeMode,
			AffectedCount:   1,
			SummaryMessage:  msg,
		}, nil
	}

	createdNew := false
	docTitle := strings.TrimSpace(req.Title)

	// 2. Handle new Google Document creation (--create-doc or no -d when CreateDoc is true)
	if req.CreateDoc || docID == "" {
		if !req.CreateDoc && docID == "" {
			return nil, errors.New("edit command requires --doc-id (-d) for an existing Google Document, or --create-doc (--create) to create a new Google Document")
		}
		if docTitle == "" {
			docTitle = "Untitled Google Document"
		}

		// If creating a new document from a rich file (.md, .docx, .pdf) with no sub-edit operations, use Drive conversion
		hasSubEdits := req.FindText != "" || req.ReplaceRange != "" || req.ReplaceBody ||
			req.DeleteRange != "" || req.DeleteText != "" || req.ClearBody ||
			req.BeforeText != "" || req.AfterText != "" || req.BeforeParagraph > 0 || req.AfterParagraph > 0 ||
			req.InsertImage != "" || req.ReplaceImage != "" || req.ResizeImage != "" || req.DeleteImage != ""

		if req.UseDriveConvert && len(req.ConvertBytes) > 0 && !hasSubEdits {
			newID, err := c.ConvertFileToGoogleDoc(ctx, docTitle, req.ParentFolder, req.ConvertMimeType, req.ConvertBytes, req.InsertText)
			if err != nil {
				return nil, err
			}
			docID = newID
			createdNew = true
			insRunes := len([]rune(req.InsertText))
			msg := fmt.Sprintf("Created new Google Document %q (%s) via Drive conversion", docTitle, docID)
			if isJa {
				msg = fmt.Sprintf("新規 Google ドキュメント %q (%s) を作成し、ファイル内容を書き込みました", docTitle, docID)
			}
			return &EditDocumentResult{
				DocumentID:         docID,
				DocumentURL:        DocumentURL(docID),
				DocumentTitle:      docTitle,
				CreatedNewDocument: true,
				Operations:         []string{string(EditOpCreate), string(EditOpInsert)},
				WriteMode:          writeMode,
				AffectedCount:      1,
				InsertedChars:      insRunes,
				SummaryMessage:     msg,
			}, nil
		}

		newID, err := c.CreateDocument(ctx, docTitle, req.ParentFolder)
		if err != nil {
			return nil, err
		}
		docID = newID
		createdNew = true

		// If user only requested creating an empty Google Document (no insert/replace/delete/image flags), return immediately
		if !req.HasInsertText && !hasSubEdits {
			msg := fmt.Sprintf("Created new blank Google Document %q (%s)", docTitle, docID)
			if isJa {
				msg = fmt.Sprintf("新規の空の Google ドキュメント %q (%s) を作成しました", docTitle, docID)
			}
			return &EditDocumentResult{
				DocumentID:         docID,
				DocumentURL:        DocumentURL(docID),
				DocumentTitle:      docTitle,
				CreatedNewDocument: true,
				Operations:         []string{string(EditOpCreate)},
				WriteMode:          writeMode,
				AffectedCount:      1,
				SummaryMessage:     msg,
			}, nil
		}
	}

	// 3. Fetch current document structure and build exact UTF-16 coordinate map
	docJSON, err := c.FetchRawDocumentJSON(ctx, docID)
	if err != nil {
		return nil, err
	}
	if docTitle == "" {
		docTitle = getString(docJSON, "title")
	}
	activeTabID, _, buf := buildIndexedUTF16Buffer(docJSON, req.TabID)

	var scopeIntervals []utf16idx.RangeInterval
	if strings.TrimSpace(req.RangeSpec) != "" {
		resolvedScope, sErr := resolveEditRangeSpec(docJSON, activeTabID, buf, req.RangeSpec, false, req.MatchCase)
		if sErr != nil {
			return nil, sErr
		}
		scopeIntervals = resolvedScope
	}

	var operations []string
	if createdNew {
		operations = append(operations, string(EditOpCreate))
	}

	var batchRequests []map[string]any
	var affectedRanges []EditRangeSummary
	totalAffected := 0
	insertedChars := 0
	deletedChars := 0
	alreadyExecutedBatch := false

	makeRangeMap := func(startIdx, endIdx int) map[string]any {
		m := map[string]any{
			"startIndex": startIdx,
			"endIndex":   endIdx,
		}
		if activeTabID != "" {
			m["tabId"] = activeTabID
		}
		return m
	}

	makeLocationMap := func(idx int) map[string]any {
		m := map[string]any{
			"index": idx,
		}
		if activeTabID != "" {
			m["tabId"] = activeTabID
		}
		return m
	}

	// Operation IMG-1: Delete image(s) (--delete-image)
	if strings.TrimSpace(req.DeleteImage) != "" {
		visuals, vErr := resolveTargetVisualElements(docJSON, activeTabID, req.DeleteImage)
		if vErr != nil {
			return nil, vErr
		}
		// Sort in descending StartIndex order so multiple deletions never shift earlier image indices
		sort.SliceStable(visuals, func(i, j int) bool {
			return visuals[i].StartIndex > visuals[j].StartIndex
		})
		maxDelEnd := buf.maxBodyEnd - 1
		for _, v := range visuals {
			if v.IsPositioned {
				delPos := map[string]any{
					"objectId": v.ObjectID,
				}
				if activeTabID != "" {
					delPos["tabId"] = activeTabID
				}
				batchRequests = append(batchRequests, map[string]any{
					"deletePositionedObject": delPos,
				})
				deletedChars++
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: v.StartIndex,
					EndIndex:   v.EndIndex,
					Preview:    fmt.Sprintf("%s (%s)", v.TargetID, v.ObjectID),
				})
				totalAffected++
				continue
			}

			delStart := v.StartIndex
			delEnd := v.EndIndex
			if v.IsStandalonePara {
				if delEnd+1 <= maxDelEnd {
					delEnd = delEnd + 1
				} else if delStart > 1 && delEnd == maxDelEnd {
					delStart = delStart - 1
				}
			}
			if delEnd > maxDelEnd {
				delEnd = maxDelEnd
			}
			if delEnd > delStart {
				batchRequests = append(batchRequests, map[string]any{
					"deleteContentRange": map[string]any{
						"range": makeRangeMap(delStart, delEnd),
					},
				})
				deletedChars += delEnd - delStart
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: delStart,
					EndIndex:   delEnd,
					Preview:    fmt.Sprintf("%s (%s)", v.TargetID, v.ObjectID),
				})
				totalAffected++
			}
		}
		operations = append(operations, string(EditOpDeleteImage))
	} else if replaceImgTarget := strings.TrimSpace(firstNonEmptyRaw(req.ReplaceImage, req.ResizeImage)); replaceImgTarget != "" {
		// Operation IMG-2: Replace or resize existing image(s) (--replace-image / --resize-image <target> [--with-image <source>] [--image-width W] [--image-height H])
		imgSrc := strings.TrimSpace(firstNonEmptyRaw(req.WithImage, req.InsertImage))
		objSize := buildImageObjectSize(req.ImageWidth, req.ImageHeight)
		if imgSrc == "" && len(objSize) == 0 {
			return nil, errors.New("--replace-image / --resize-image requires --with-image <path-or-url> (or --image-width / --image-height to resize an existing image in place)")
		}
		visuals, vErr := resolveTargetVisualElements(docJSON, activeTabID, replaceImgTarget)
		if vErr != nil {
			return nil, vErr
		}
		var explicitURI string
		if imgSrc != "" {
			u, cleanupFn, uErr := c.ResolveImageURIForDocsAPI(ctx, imgSrc, req.WorkDir)
			if cleanupFn != nil {
				defer cleanupFn()
			}
			if uErr != nil {
				return nil, uErr
			}
			explicitURI = u
		}

		sort.SliceStable(visuals, func(i, j int) bool {
			return visuals[i].StartIndex > visuals[j].StartIndex
		})
		maxDelEnd := buf.maxBodyEnd - 1

		for _, v := range visuals {
			targetURI := explicitURI
			previewLabel := imgSrc
			if targetURI == "" {
				targetURI = strings.TrimSpace(v.ContentURI)
				if targetURI == "" {
					return nil, fmt.Errorf("cannot resize %s (%s) in place without --with-image because the document element has no accessible contentUri", v.TargetID, v.ObjectID)
				}
				previewLabel = fmt.Sprintf("resize(w=%.0fpt,h=%.0fpt)", req.ImageWidth, req.ImageHeight)
			}
			// If custom dimensions were requested on an inline image, or if replacing a Chart, use deleteContentRange + insertInlineImage
			if !v.IsPositioned && (len(objSize) > 0 || v.Type == "Chart") {
				eIdx := minInt(v.EndIndex, maxDelEnd)
				if eIdx > v.StartIndex {
					batchRequests = append(batchRequests, map[string]any{
						"deleteContentRange": map[string]any{
							"range": makeRangeMap(v.StartIndex, eIdx),
						},
					})
					deletedChars += eIdx - v.StartIndex
				}
				inlineReq := map[string]any{
					"location": makeLocationMap(v.StartIndex),
					"uri":      targetURI,
				}
				if len(objSize) > 0 {
					inlineReq["objectSize"] = objSize
				}
				batchRequests = append(batchRequests, map[string]any{
					"insertInlineImage": inlineReq,
				})
				insertedChars++
			} else {
				repImg := map[string]any{
					"imageObjectId":      v.ObjectID,
					"uri":                targetURI,
					"imageReplaceMethod": "CENTER_CROP",
				}
				if activeTabID != "" {
					repImg["tabId"] = activeTabID
				}
				batchRequests = append(batchRequests, map[string]any{
					"replaceImage": repImg,
				})
				deletedChars++
				insertedChars++
			}
			affectedRanges = append(affectedRanges, EditRangeSummary{
				StartIndex: v.StartIndex,
				EndIndex:   v.EndIndex,
				Preview:    fmt.Sprintf("%s (%s) -> %s", v.TargetID, v.ObjectID, previewLabel),
			})
			totalAffected++
		}
		operations = append(operations, string(EditOpReplaceImage))
	} else if strings.TrimSpace(req.InsertImage) != "" && !req.HasInsertText {
		// Operation IMG-3: Insert standalone image at arbitrary location (--insert-image <path-or-url>)
		uri, cleanupFn, uErr := c.ResolveImageURIForDocsAPI(ctx, req.InsertImage, req.WorkDir)
		if cleanupFn != nil {
			defer cleanupFn()
		}
		if uErr != nil {
			return nil, uErr
		}
		targetIdx, leadingNL, trailingNL, isParaBlock, pErr := resolveInsertPositionDetails(docJSON, activeTabID, buf, req, scopeIntervals)
		if pErr != nil {
			return nil, pErr
		}
		objSize := buildImageObjectSize(req.ImageWidth, req.ImageHeight)
		imgIdx := targetIdx

		if isParaBlock {
			if leadingNL {
				// E.g. --append at the end of a non-empty document: insert '\n' at targetIdx, then image at targetIdx+1
				batchRequests = append(batchRequests,
					map[string]any{
						"insertText": map[string]any{
							"location": makeLocationMap(targetIdx),
							"text":     "\n",
						},
					},
					map[string]any{
						"updateParagraphStyle": map[string]any{
							"range": makeRangeMap(targetIdx, targetIdx+1),
							"paragraphStyle": map[string]any{
								"namedStyleType": "NORMAL_TEXT",
							},
							"fields": "namedStyleType",
						},
					},
				)
				imgIdx = targetIdx + 1
			} else if trailingNL {
				// E.g. --prepend or --before-paragraph or --after-paragraph: insert '\n' at targetIdx, then image at targetIdx
				batchRequests = append(batchRequests,
					map[string]any{
						"insertText": map[string]any{
							"location": makeLocationMap(targetIdx),
							"text":     "\n",
						},
					},
					map[string]any{
						"updateParagraphStyle": map[string]any{
							"range": makeRangeMap(targetIdx, targetIdx+1),
							"paragraphStyle": map[string]any{
								"namedStyleType": "NORMAL_TEXT",
							},
							"fields": "namedStyleType",
						},
					},
				)
				imgIdx = targetIdx
			}
		}

		inlineReq := map[string]any{
			"location": makeLocationMap(imgIdx),
			"uri":      uri,
		}
		if len(objSize) > 0 {
			inlineReq["objectSize"] = objSize
		}
		batchRequests = append(batchRequests, map[string]any{
			"insertInlineImage": inlineReq,
		})
		insertedChars = 1
		totalAffected = 1
		affectedRanges = append(affectedRanges, EditRangeSummary{
			StartIndex: imgIdx,
			EndIndex:   imgIdx + 1,
			Preview:    fmt.Sprintf("[Image: %s]", req.InsertImage),
		})
		operations = append(operations, string(EditOpInsertImage))
	} else if req.ClearBody || req.ReplaceBody {
		// Operation A: Clear entire body (--clear) or Replace entire body (--replace-body)
		maxDelEnd := buf.maxBodyEnd - 1
		var delReqs []map[string]any
		if maxDelEnd > 1 {
			delReqs = append(delReqs, map[string]any{
				"deleteContentRange": map[string]any{
					"range": makeRangeMap(1, maxDelEnd),
				},
			})
			deletedChars += maxDelEnd - 1
			affectedRanges = append(affectedRanges, EditRangeSummary{
				StartIndex: 1,
				EndIndex:   maxDelEnd,
				Preview:    "[Entire Document Body]",
			})
		}
		if req.ReplaceBody {
			operations = append(operations, string(EditOpReplaceBody))
			replacement := strings.ReplaceAll(firstNonEmptyRaw(req.ReplaceText, req.InsertText), "\r\n", "\n")
			if req.Markdown && replacement != "" {
				operations = append(operations, string(EditOpInsertMarkdown))
				insCount, mErr := c.ExecuteMarkdownInsertAt(
					ctx, docID, activeTabID, 1, replacement,
					false, false, writeMode, req.WorkDir, req.ImageWidth, req.ImageHeight, delReqs,
				)
				if mErr != nil {
					return nil, mErr
				}
				insertedChars += insCount
				alreadyExecutedBatch = true
			} else {
				batchRequests = append(batchRequests, delReqs...)
				if replacement != "" {
					batchRequests = append(batchRequests, map[string]any{
						"insertText": map[string]any{
							"location": makeLocationMap(1),
							"text":     replacement,
						},
					})
					u16Len := utf16idx.UTF16Len(replacement)
					if style := normalizeNamedParagraphStyle(req.ParagraphStyle); style != "" {
						batchRequests = append(batchRequests, map[string]any{
							"updateParagraphStyle": map[string]any{
								"range": makeRangeMap(1, 1+u16Len),
								"paragraphStyle": map[string]any{
									"namedStyleType": style,
								},
								"fields": "namedStyleType",
							},
						})
					}
					insertedChars += len([]rune(replacement))
				}
			}
			totalAffected++
		} else {
			batchRequests = append(batchRequests, delReqs...)
			operations = append(operations, string(EditOpClearBody))
			totalAffected++
		}
	} else if strings.TrimSpace(req.DeleteRange) != "" {
		// Operation B: Delete arbitrary range(s) (--delete-range)
		intervals, dErr := resolveEditRangeSpec(docJSON, activeTabID, buf, req.DeleteRange, true, req.MatchCase)
		if dErr != nil {
			return nil, dErr
		}
		sortIntervalsDescending(intervals)
		for _, iv := range intervals {
			if iv.EndIndex > iv.StartIndex {
				batchRequests = append(batchRequests, map[string]any{
					"deleteContentRange": map[string]any{
						"range": makeRangeMap(iv.StartIndex, iv.EndIndex),
					},
				})
				deletedChars += iv.EndIndex - iv.StartIndex
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: iv.StartIndex,
					EndIndex:   iv.EndIndex,
				})
				totalAffected++
			}
		}
		operations = append(operations, string(EditOpDeleteRange))
	} else if req.DeleteText != "" {
		// Operation C: Delete specific text substring/phrase (--delete-text)
		matches := buf.findSubstringIntervals(req.DeleteText, req.MatchCase, scopeIntervals, req.Occurrence)
		if len(matches) == 0 {
			return nil, fmt.Errorf("text to delete %q was not found in the target document scope", req.DeleteText)
		}
		maxDelEnd := buf.maxBodyEnd - 1
		sortIntervalsDescending(matches)
		for _, iv := range matches {
			eIdx := min(iv.EndIndex, maxDelEnd)
			if eIdx > iv.StartIndex {
				batchRequests = append(batchRequests, map[string]any{
					"deleteContentRange": map[string]any{
						"range": makeRangeMap(iv.StartIndex, eIdx),
					},
				})
				deletedChars += eIdx - iv.StartIndex
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: iv.StartIndex,
					EndIndex:   eIdx,
					Preview:    req.DeleteText,
				})
				totalAffected++
			}
		}
		operations = append(operations, string(EditOpDeleteText))
	} else if strings.TrimSpace(req.ReplaceRange) != "" {
		// Operation D: Replace arbitrary range (--replace-range) with new text or Markdown
		intervals, rErr := resolveEditRangeSpec(docJSON, activeTabID, buf, req.ReplaceRange, false, req.MatchCase)
		if rErr != nil {
			return nil, rErr
		}
		replacement := strings.ReplaceAll(firstNonEmptyRaw(req.ReplaceText, req.InsertText), "\r\n", "\n")
		maxDelEnd := buf.maxBodyEnd - 1
		sortIntervalsDescending(intervals)

		if req.Markdown && replacement != "" {
			operations = append(operations, string(EditOpReplaceRange), string(EditOpInsertMarkdown))
			for _, iv := range intervals {
				eIdx := minInt(iv.EndIndex, maxDelEnd)
				var delReqs []map[string]any
				if eIdx > iv.StartIndex {
					delReqs = append(delReqs, map[string]any{
						"deleteContentRange": map[string]any{
							"range": makeRangeMap(iv.StartIndex, eIdx),
						},
					})
					deletedChars += eIdx - iv.StartIndex
				}
				insCount, mErr := c.ExecuteMarkdownInsertAt(
					ctx, docID, activeTabID, iv.StartIndex, replacement,
					false, false, writeMode, req.WorkDir, req.ImageWidth, req.ImageHeight, delReqs,
				)
				if mErr != nil {
					return nil, mErr
				}
				insertedChars += insCount
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: iv.StartIndex,
					EndIndex:   eIdx,
					Preview:    truncatePreview(replacement, 80),
				})
				totalAffected++
			}
			alreadyExecutedBatch = true
		} else {
			for _, iv := range intervals {
				eIdx := min(iv.EndIndex, maxDelEnd)
				if eIdx > iv.StartIndex {
					batchRequests = append(batchRequests, map[string]any{
						"deleteContentRange": map[string]any{
							"range": makeRangeMap(iv.StartIndex, eIdx),
						},
					})
					deletedChars += eIdx - iv.StartIndex
				}
				if replacement != "" {
					batchRequests = append(batchRequests, map[string]any{
						"insertText": map[string]any{
							"location": makeLocationMap(iv.StartIndex),
							"text":     replacement,
						},
					})
					u16Len := utf16idx.UTF16Len(replacement)
					if style := normalizeNamedParagraphStyle(req.ParagraphStyle); style != "" {
						batchRequests = append(batchRequests, map[string]any{
							"updateParagraphStyle": map[string]any{
								"range": makeRangeMap(iv.StartIndex, iv.StartIndex+u16Len),
								"paragraphStyle": map[string]any{
									"namedStyleType": style,
								},
								"fields": "namedStyleType",
							},
						})
					}
					insertedChars += len([]rune(replacement))
				}
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: iv.StartIndex,
					EndIndex:   eIdx,
					Preview:    replacement,
				})
				totalAffected++
			}
			operations = append(operations, string(EditOpReplaceRange))
		}
	} else if req.FindText != "" {
		// Operation E: Search & Replace text (--find <old> --replace <new>)
		replacement := strings.ReplaceAll(firstNonEmptyRaw(req.ReplaceText, req.InsertText), "\r\n", "\n")

		matches := buf.findSubstringIntervals(req.FindText, req.MatchCase, scopeIntervals, req.Occurrence)
		if len(matches) == 0 && (len(scopeIntervals) > 0 || req.Occurrence > 0 || req.SuggestMode || req.Markdown) {
			return nil, fmt.Errorf("search text %q was not found in the target document scope", req.FindText)
		}

		if req.Markdown && replacement != "" {
			operations = append(operations, string(EditOpReplaceText), string(EditOpInsertMarkdown))
			maxDelEnd := buf.maxBodyEnd - 1
			sortIntervalsDescending(matches)
			for _, iv := range matches {
				eIdx := minInt(iv.EndIndex, maxDelEnd)
				var delReqs []map[string]any
				if eIdx > iv.StartIndex {
					delReqs = append(delReqs, map[string]any{
						"deleteContentRange": map[string]any{
							"range": makeRangeMap(iv.StartIndex, eIdx),
						},
					})
					deletedChars += len([]rune(req.FindText))
				}
				insCount, mErr := c.ExecuteMarkdownInsertAt(
					ctx, docID, activeTabID, iv.StartIndex, replacement,
					false, false, writeMode, req.WorkDir, req.ImageWidth, req.ImageHeight, delReqs,
				)
				if mErr != nil {
					return nil, mErr
				}
				insertedChars += insCount
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: iv.StartIndex,
					EndIndex:   eIdx,
					Preview:    fmt.Sprintf("%q -> %q", req.FindText, truncatePreview(replacement, 60)),
				})
				totalAffected++
			}
			alreadyExecutedBatch = true
		} else if len(scopeIntervals) == 0 && req.Occurrence <= 0 && !req.SuggestMode {
			// Use native Google Docs replaceAllText request for full-tab/document plain-text replacement
			replaceAllReq := map[string]any{
				"containsText": map[string]any{
					"text":      req.FindText,
					"matchCase": req.MatchCase,
				},
				"replaceText": replacement,
			}
			if activeTabID != "" {
				replaceAllReq["tabsCriteria"] = map[string]any{
					"tabIds": []string{activeTabID},
				}
			}
			batchRequests = append(batchRequests, map[string]any{
				"replaceAllText": replaceAllReq,
			})
			for _, m := range matches {
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: m.StartIndex,
					EndIndex:   m.EndIndex,
					Preview:    fmt.Sprintf("%q -> %q", req.FindText, replacement),
				})
			}
			totalAffected = len(matches)
			deletedChars = len(matches) * len([]rune(req.FindText))
			insertedChars = len(matches) * len([]rune(replacement))
			operations = append(operations, string(EditOpReplaceText))
		} else {
			// Scoped or occurrence-specific or SUGGEST-mode replacement via bottom-to-top deleteContentRange + insertText
			maxDelEnd := buf.maxBodyEnd - 1
			sortIntervalsDescending(matches)
			for _, iv := range matches {
				eIdx := min(iv.EndIndex, maxDelEnd)
				if eIdx > iv.StartIndex {
					batchRequests = append(batchRequests, map[string]any{
						"deleteContentRange": map[string]any{
							"range": makeRangeMap(iv.StartIndex, eIdx),
						},
					})
					deletedChars += len([]rune(req.FindText))
				}
				if replacement != "" {
					batchRequests = append(batchRequests, map[string]any{
						"insertText": map[string]any{
							"location": makeLocationMap(iv.StartIndex),
							"text":     replacement,
						},
					})
					insertedChars += len([]rune(replacement))
				}
				affectedRanges = append(affectedRanges, EditRangeSummary{
					StartIndex: iv.StartIndex,
					EndIndex:   eIdx,
					Preview:    fmt.Sprintf("%q -> %q", req.FindText, replacement),
				})
				totalAffected++
			}
			operations = append(operations, string(EditOpReplaceText))
		}
	} else if req.HasInsertText {
		// Operation F: Insert plain text or rich Markdown at start, end, @UTF16-index, paragraph number, --before, or --after
		if req.Markdown {
			targetIdx, leadingNL, trailingNL, _, pErr := resolveInsertPositionDetails(docJSON, activeTabID, buf, req, scopeIntervals)
			if pErr != nil {
				return nil, pErr
			}
			if strings.TrimSpace(req.InsertText) == "" {
				return nil, errors.New("insert text is empty")
			}
			insCount, mErr := c.ExecuteMarkdownInsertAt(
				ctx, docID, activeTabID, targetIdx, req.InsertText,
				leadingNL, trailingNL, writeMode, req.WorkDir, req.ImageWidth, req.ImageHeight, nil,
			)
			if mErr != nil {
				return nil, mErr
			}
			insertedChars = insCount
			affectedRanges = append(affectedRanges, EditRangeSummary{
				StartIndex: targetIdx,
				EndIndex:   targetIdx + maxInt(1, insCount),
				Preview:    truncatePreview(req.InsertText, 80),
			})
			totalAffected = 1
			operations = append(operations, string(EditOpInsertMarkdown))
			alreadyExecutedBatch = true
		} else {
			insIdx, finalInsertText, iErr := resolveInsertIndex(docJSON, activeTabID, buf, req, scopeIntervals)
			if iErr != nil {
				return nil, iErr
			}
			if finalInsertText == "" {
				return nil, errors.New("insert text is empty")
			}
			batchRequests = append(batchRequests, map[string]any{
				"insertText": map[string]any{
					"location": makeLocationMap(insIdx),
					"text":     finalInsertText,
				},
			})
			u16Len := utf16idx.UTF16Len(finalInsertText)
			if style := normalizeNamedParagraphStyle(req.ParagraphStyle); style != "" {
				batchRequests = append(batchRequests, map[string]any{
					"updateParagraphStyle": map[string]any{
						"range": makeRangeMap(insIdx, insIdx+u16Len),
						"paragraphStyle": map[string]any{
							"namedStyleType": style,
						},
						"fields": "namedStyleType",
					},
				})
			}
			insertedChars = len([]rune(finalInsertText))
			affectedRanges = append(affectedRanges, EditRangeSummary{
				StartIndex: insIdx,
				EndIndex:   insIdx + u16Len,
				Preview:    truncatePreview(finalInsertText, 80),
			})
			totalAffected = 1
			operations = append(operations, string(EditOpInsert))
		}
	} else {
		return nil, errors.New("no edit operation specified (provide text via --text / -i / stdin to insert, --markdown, --insert-image, --replace-image, --delete-image, --find & --replace, --replace-range, --replace-body, --delete-range, --delete-text, --clear, --create-doc, or --delete-doc)")
	}

	if !alreadyExecutedBatch && len(batchRequests) > 0 {
		payload := map[string]any{
			"writeControl": map[string]any{
				"writeMode": writeMode,
			},
			"requests": batchRequests,
		}
		resp, bErr := c.ExecuteBatchUpdate(ctx, docID, payload)
		if bErr != nil && writeMode == "SUGGEST" {
			// Fallback to EDIT mode if structural/image operations were rejected in SUGGEST mode
			payload["writeControl"] = map[string]any{"writeMode": "EDIT"}
			resp, bErr = c.ExecuteBatchUpdate(ctx, docID, payload)
		}
		if bErr != nil {
			return nil, bErr
		}
		// Extract occurrencesChanged from replaceAllText reply if present
		for _, rawReply := range getSlice(resp, "replies") {
			if replyMap, ok := rawReply.(map[string]any); ok {
				if repAll := getMap(replyMap, "replaceAllText"); len(repAll) > 0 {
					if occ, hasOcc := getNumber(repAll, "occurrencesChanged"); hasOcc {
						totalAffected = occ
					}
				}
			}
		}
	}

	summaryMsg := buildEditSummaryMessage(operations, docTitle, docID, totalAffected, insertedChars, deletedChars, writeMode, isJa)
	return &EditDocumentResult{
		DocumentID:         docID,
		DocumentURL:        DocumentURL(docID),
		DocumentTitle:      docTitle,
		ActiveTabID:        activeTabID,
		CreatedNewDocument: createdNew,
		Operations:         operations,
		WriteMode:          writeMode,
		AffectedCount:      totalAffected,
		InsertedChars:      insertedChars,
		DeletedChars:       deletedChars,
		AffectedRanges:     affectedRanges,
		SummaryMessage:     summaryMsg,
	}, nil
}

func sortIntervalsDescending(intervals []utf16idx.RangeInterval) {
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].StartIndex != intervals[j].StartIndex {
			return intervals[i].StartIndex > intervals[j].StartIndex
		}
		return intervals[i].EndIndex > intervals[j].EndIndex
	})
}

func firstNonEmptyRaw(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncatePreview(s string, maxRunes int) string {
	clean := strings.ReplaceAll(strings.TrimSpace(s), "\n", "↵")
	r := []rune(clean)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "..."
	}
	return clean
}

func normalizeNamedParagraphStyle(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "H1", "HEADING1", "HEADING_1":
		return "HEADING_1"
	case "H2", "HEADING2", "HEADING_2":
		return "HEADING_2"
	case "H3", "HEADING3", "HEADING_3":
		return "HEADING_3"
	case "H4", "HEADING4", "HEADING_4":
		return "HEADING_4"
	case "H5", "HEADING5", "HEADING_5":
		return "HEADING_5"
	case "H6", "HEADING6", "HEADING_6":
		return "HEADING_6"
	case "TITLE":
		return "TITLE"
	case "SUBTITLE":
		return "SUBTITLE"
	case "NORMAL", "NORMAL_TEXT", "BODY":
		return "NORMAL_TEXT"
	default:
		return ""
	}
}

func buildEditSummaryMessage(
	ops []string,
	docTitle string,
	docID string,
	affected int,
	insertedChars int,
	deletedChars int,
	writeMode string,
	isJa bool,
) string {
	opStr := strings.Join(ops, "+")
	if isJa {
		return fmt.Sprintf("Google ドキュメント %q (%s) の編集完了 [操作=%s, モード=%s, 対象=%d箇所, 挿入=%d文字, 削除=%d文字]",
			docTitle, docID, opStr, writeMode, affected, insertedChars, deletedChars)
	}
	return fmt.Sprintf("Edited Google Document %q (%s) [ops=%s, mode=%s, affected=%d, inserted=%d chars, deleted=%d chars]",
		docTitle, docID, opStr, writeMode, affected, insertedChars, deletedChars)
}
