package gdocs

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

const (
	// DefaultSummaryTabTitle is the default Google Document Tab title created/updated by --summary-tab.
	DefaultSummaryTabTitle = "🎓 DocuMentor AI Summary"
	// SummaryTabHeaderPrefix is the leading heading marker written inside a DocuMentor AI Summary Tab.
	SummaryTabHeaderPrefix = "🎓 DocuMentor AI —"
)

// EffectiveSummaryTabTitle returns customTitle if non-empty, or DefaultSummaryTabTitle otherwise.
func EffectiveSummaryTabTitle(customTitle string) string {
	trimmed := strings.TrimSpace(customTitle)
	if trimmed != "" {
		return trimmed
	}
	return DefaultSummaryTabTitle
}

// IsDocuMentorSummaryTab reports whether a Google Document tab object is a DocuMentor AI Coaching Summary tab
// (either by its tab title or by the signature header in its first paragraph).
func IsDocuMentorSummaryTab(tab map[string]any, customTitle string) bool {
	if len(tab) == 0 {
		return false
	}
	title := strings.TrimSpace(getString(getMap(tab, "tabProperties"), "title"))
	if strings.EqualFold(title, DefaultSummaryTabTitle) {
		return true
	}
	if strings.TrimSpace(customTitle) != "" && strings.EqualFold(title, strings.TrimSpace(customTitle)) {
		return true
	}
	dt := getMap(tab, "documentTab")
	content := getSlice(getMap(dt, "body"), "content")
	for _, rawEl := range content {
		el, ok := rawEl.(map[string]any)
		if !ok {
			continue
		}
		para := getMap(el, "paragraph")
		if len(para) == 0 {
			continue
		}
		var sb strings.Builder
		for _, rawPE := range getSlice(para, "elements") {
			pe, ok := rawPE.(map[string]any)
			if !ok {
				continue
			}
			sb.WriteString(getString(getMap(pe, "textRun"), "content"))
		}
		firstLine := strings.TrimSpace(sb.String())
		if firstLine != "" {
			return strings.HasPrefix(firstLine, SummaryTabHeaderPrefix)
		}
	}
	return false
}

// ExistingSuggestion represents an unresolved inline suggestion in a Google Document.
type ExistingSuggestion struct {
	ID            string          `json:"id"`
	DeletedText   string          `json:"deletedText"`
	InsertedText  string          `json:"insertedText"`
	StartIndex    int             `json:"startIndex"`
	EndIndex      int             `json:"endIndex"`
	IsAIGenerated bool            `json:"isAiGenerated"`
	Comments      []ActiveComment `json:"comments,omitempty"`
}

// CommentReply represents a single reply post in a comment or suggestion thread.
type CommentReply struct {
	Author  string `json:"author"`
	Content string `json:"content"`
	Action  string `json:"action,omitempty"`
}

// ActiveComment represents an open (unresolved) comment or suggestion thread.
type ActiveComment struct {
	CommentID    string                   `json:"commentId"`
	AnchorID     string                   `json:"anchorId,omitempty"`
	AnchorRanges []utf16idx.RangeInterval `json:"anchorRanges,omitempty"`
	SuggestionID string                   `json:"suggestionId,omitempty"`
	Author       string                   `json:"author"`
	Content      string                   `json:"content"`
	QuotedText   string                   `json:"quotedText,omitempty"`
	Replies      []CommentReply           `json:"replies,omitempty"`
}

// ResolvedReview represents a resolved comment thread or accepted/rejected suggestion thread.
type ResolvedReview struct {
	CommentID       string                   `json:"commentId"`
	AnchorID        string                   `json:"anchorId,omitempty"`
	AnchorRanges    []utf16idx.RangeInterval `json:"anchorRanges,omitempty"`
	SuggestionID    string                   `json:"suggestionId,omitempty"`
	ThreadType      string                   `json:"threadType"`      // "SUGGESTION" or "COMMENT"
	ResolutionState string                   `json:"resolutionState"` // "ACCEPTED", "REJECTED", "RESOLVED", "RESOLVED_SUGGESTION"
	Author          string                   `json:"author"`
	Content         string                   `json:"content"`
	Text            string                   `json:"text"`
	QuotedText      string                   `json:"quotedText,omitempty"`
	SummaryText     string                   `json:"summaryText,omitempty"`
	Replies         []CommentReply           `json:"replies,omitempty"`
}

// TableElement represents a structured table extracted from a Google Document.
type TableElement struct {
	TargetID        string `json:"targetId"` // e.g. "TABLE_1"
	TableNumber     int    `json:"tableNumber"`
	BodyChildIndex  int    `json:"bodyChildIndex"`
	RowsCount       int    `json:"rowsCount"`
	ColsCount       int    `json:"colsCount"`
	Markdown        string `json:"markdown"`
	StartIndex      int    `json:"startIndex"`      // First cell start index
	EndIndex        int    `json:"endIndex"`        // First cell end index
	TableStartIndex int    `json:"tableStartIndex"` // Full structural table start index
	TableEndIndex   int    `json:"tableEndIndex"`   // Full structural table end index
}

// VisualElement represents an inline or positioned figure/chart in a Google Document.
type VisualElement struct {
	TargetID         string `json:"targetId"` // e.g. "VISUAL_1"
	ObjectID         string `json:"objectId"`
	Type             string `json:"type"` // "Chart" or "Figure/Image"
	Title            string `json:"title"`
	Description      string `json:"description"`
	ContentURI       string `json:"contentUri,omitempty"`
	StartIndex       int    `json:"startIndex"`
	EndIndex         int    `json:"endIndex"`
	ParaStartIndex   int    `json:"paraStartIndex"`
	ParaEndIndex     int    `json:"paraEndIndex"`
	BodyChildIndex   int    `json:"bodyChildIndex"`
	TableNumber      int    `json:"tableNumber,omitempty"`
	IsPositioned     bool   `json:"isPositioned,omitempty"`
	IsStandalonePara bool   `json:"isStandalonePara,omitempty"`
	MimeType         string `json:"mimeType,omitempty"`
	ImageBytes       []byte `json:"-"`
}

// ParsedDocument holds the structured representation of a Google Document tab.
type ParsedDocument struct {
	DocumentID          string                   `json:"documentId"`
	Title               string                   `json:"title"`
	ActiveTabID         string                   `json:"activeTabId"`
	ActiveTabTitle      string                   `json:"activeTabTitle"`
	SelectionOnly       bool                     `json:"selectionOnly"`
	SelectionRanges     []utf16idx.RangeInterval `json:"selectionRanges,omitempty"`
	SelectedTextPreview string                   `json:"selectedTextPreview,omitempty"`
	IsEmpty             bool                     `json:"isEmpty"`
	CharCount           int                      `json:"charCount"`
	AnnotatedText       string                   `json:"annotatedText"`
	EditableBlocks      []utf16idx.EditableBlock `json:"editableBlocks"`
	ExistingSuggestions []ExistingSuggestion     `json:"existingSuggestions"`
	ExistingComments    []ActiveComment          `json:"existingComments"`
	ResolvedReviews     []ResolvedReview         `json:"resolvedReviews"`
	AllSuggestionIDs    []string                 `json:"allSuggestionIds"`
	AllAICommentIDs     []string                 `json:"allAiCommentIds"`
	AllCommentIDs       []string                 `json:"allCommentIds"`
	Tables              []TableElement           `json:"tables"`
	VisualElements      []VisualElement          `json:"visualElements"`
	DecisionRecord      *DecisionRecordSummary   `json:"decisionRecord,omitempty"`
}

// ParseOptions configures how ParseDocumentStructure scopes and extracts document content.
type ParseOptions struct {
	PreferredTabID         string
	SelectionOnly          bool
	SelectionRanges        []utf16idx.RangeInterval
	SkipDecisionReconcile  bool
}

// ParseDocumentStructure parses a raw Google Docs API v1 JSON object into a ParsedDocument,
// faithfully mirroring GAS `parseDocumentStructure_` including SUGGESTIONS_INLINE handling,
// table Markdown conversion, figure extraction, and Fail-Closed Selection Scope filtering.
func ParseDocumentStructure(docJSON map[string]any, opts ParseOptions) *ParsedDocument {
	activeTabID, activeTabTitle, docTab := resolveTargetDocumentTab(docJSON, opts.PreferredTabID)
	bodyContent := getSlice(getMap(docTab, "body"), "content")
	inlineObjectsMap := getMap(docTab, "inlineObjects")
	if len(inlineObjectsMap) == 0 {
		inlineObjectsMap = getMap(docJSON, "inlineObjects")
	}
	positionedObjectsMap := getMap(docTab, "positionedObjects")
	if len(positionedObjectsMap) == 0 {
		positionedObjectsMap = getMap(docJSON, "positionedObjects")
	}

	var plainTextBuilder strings.Builder
	var previewAcceptedBuilder strings.Builder
	var annotatedBuilder strings.Builder

	existingSuggestionsMap := make(map[string]*ExistingSuggestion)
	var suggestionOrder []string
	var allEditableBlocks []utf16idx.EditableBlock
	var allTablesList []TableElement
	var allVisualElements []VisualElement

	type paraMeta struct {
		isInsideTable  bool
		bodyChildIndex int
		tableNumber    int
		rowIndex       int
		colIndex       int
	}

	processParagraph := func(paragraph map[string]any, meta paraMeta) (string, string, string) {
		var paraPlain, paraPreview, paraAnnotated strings.Builder
		var currentBlock *utf16idx.EditableBlock

		pElements := getSlice(paragraph, "elements")
		paraStartIndex := 1
		if len(pElements) > 0 {
			if firstMap, ok := pElements[0].(map[string]any); ok {
				if sIdx := getInt(firstMap, "startIndex"); sIdx > 0 {
					paraStartIndex = sIdx
				}
			}
		}
		rawParaEndIndex := paraStartIndex + 1
		if len(pElements) > 0 {
			if lastMap, ok := pElements[len(pElements)-1].(map[string]any); ok {
				if eIdx := getInt(lastMap, "endIndex"); eIdx > 0 {
					rawParaEndIndex = eIdx
				}
			}
		}
		paraEndIndex := maxInt(paraStartIndex+1, rawParaEndIndex-1)

		// Positioned objects (figures/charts anchored to paragraph)
		for _, rawPosID := range getSlice(paragraph, "positionedObjectIds") {
			posID, _ := rawPosID.(string)
			if posID == "" {
				continue
			}
			posObj := getMap(positionedObjectsMap, posID)
			emb := getMap(getMap(posObj, "positionedObjectProperties"), "embeddedObject")
			if len(emb) > 0 {
				figIdx := len(allVisualElements) + 1
				isChart := len(getMap(getMap(emb, "linkedContentReference"), "sheetsChartReference")) > 0
				imgProps := getMap(emb, "imageProperties")
				contentURI := getString(imgProps, "contentUri")
				if contentURI == "" {
					contentURI = getString(imgProps, "sourceUri")
				}
				vID := fmt.Sprintf("VISUAL_%d", figIdx)
				vType := "Figure/Image"
				if isChart {
					vType = "Chart"
				}
				title := getString(emb, "title")
				if title == "" {
					title = fmt.Sprintf("%s #%d", vType, figIdx)
				}
				desc := getString(emb, "description")
				vItem := VisualElement{
					TargetID:       vID,
					ObjectID:       posID,
					Type:           vType,
					Title:          title,
					Description:    desc,
					ContentURI:     contentURI,
					StartIndex:     paraStartIndex,
					EndIndex:       paraStartIndex + 1,
					ParaStartIndex: paraStartIndex,
					ParaEndIndex:   paraEndIndex,
					BodyChildIndex: meta.bodyChildIndex,
					TableNumber:    meta.tableNumber,
					IsPositioned:   true,
				}
				allVisualElements = append(allVisualElements, vItem)
				paraAnnotated.WriteString(fmt.Sprintf("\n[%s (%s): Title=%q, Description=%q]\n", vID, vType, getString(emb, "title"), desc))
			}
		}

		flushCurrentBlock := func() {
			if currentBlock != nil {
				allEditableBlocks = append(allEditableBlocks, *currentBlock)
				currentBlock = nil
			}
		}

		for _, rawEl := range pElements {
			el, ok := rawEl.(map[string]any)
			if !ok {
				continue
			}

			// Inline object (figure/chart)
			if inlineObjEl := getMap(el, "inlineObjectElement"); len(inlineObjEl) > 0 {
				if objID := getString(inlineObjEl, "inlineObjectId"); objID != "" {
					flushCurrentBlock()
					inlineObj := getMap(inlineObjectsMap, objID)
					emb := getMap(getMap(inlineObj, "inlineObjectProperties"), "embeddedObject")
					figIdx := len(allVisualElements) + 1
					isChart := len(getMap(getMap(emb, "linkedContentReference"), "sheetsChartReference")) > 0
					imgProps := getMap(emb, "imageProperties")
					contentURI := getString(imgProps, "contentUri")
					if contentURI == "" {
						contentURI = getString(imgProps, "sourceUri")
					}
					vID := fmt.Sprintf("VISUAL_%d", figIdx)
					vType := "Figure/Image"
					if isChart {
						vType = "Chart"
					}
					title := getString(emb, "title")
					if title == "" {
						title = fmt.Sprintf("%s #%d", vType, figIdx)
					}
					desc := getString(emb, "description")
					sIdx := getInt(el, "startIndex")
					eIdx := getInt(el, "endIndex")
					if eIdx <= sIdx {
						eIdx = sIdx + 1
					}
					isStandalone := sIdx == paraStartIndex && eIdx == paraStartIndex+1 && rawParaEndIndex == paraStartIndex+2
					vItem := VisualElement{
						TargetID:         vID,
						ObjectID:         objID,
						Type:             vType,
						Title:            title,
						Description:      desc,
						ContentURI:       contentURI,
						StartIndex:       sIdx,
						EndIndex:         eIdx,
						ParaStartIndex:   paraStartIndex,
						ParaEndIndex:     paraEndIndex,
						BodyChildIndex:   meta.bodyChildIndex,
						TableNumber:      meta.tableNumber,
						IsPositioned:     false,
						IsStandalonePara: isStandalone,
					}
					allVisualElements = append(allVisualElements, vItem)
					marker := fmt.Sprintf("[%s (%s): Title=%q, Description=%q]", vID, vType, getString(emb, "title"), desc)
					paraAnnotated.WriteString(marker)
					paraPlain.WriteString(marker)
					paraPreview.WriteString(marker)
					continue
				}
			}

			textRun := getMap(el, "textRun")
			if len(textRun) == 0 {
				continue
			}
			text, ok := textRun["content"].(string)
			if !ok || text == "" {
				continue
			}
			startIndex := getInt(el, "startIndex")
			endIndex := getInt(el, "endIndex")
			if endIndex <= startIndex {
				endIndex = startIndex + utf16idx.UTF16Len(text)
			}

			insIDs := getStringSlice(textRun, "suggestedInsertionIds")
			delIDs := getStringSlice(textRun, "suggestedDeletionIds")

			if len(insIDs) > 0 {
				flushCurrentBlock()
				sugID := insIDs[0]
				entry, exists := existingSuggestionsMap[sugID]
				if !exists {
					entry = &ExistingSuggestion{
						ID:         sugID,
						StartIndex: startIndex,
						EndIndex:   endIndex,
					}
					existingSuggestionsMap[sugID] = entry
					suggestionOrder = append(suggestionOrder, sugID)
				}
				entry.InsertedText += text
				entry.EndIndex = endIndex

				paraPreview.WriteString(text)
				paraAnnotated.WriteString(fmt.Sprintf("[Existing Suggestion (Insert ID:%s): %q]", sugID, strings.ReplaceAll(text, "\n", "")))
			} else if len(delIDs) > 0 {
				flushCurrentBlock()
				sugID := delIDs[0]
				entry, exists := existingSuggestionsMap[sugID]
				if !exists {
					entry = &ExistingSuggestion{
						ID:         sugID,
						StartIndex: startIndex,
						EndIndex:   endIndex,
					}
					existingSuggestionsMap[sugID] = entry
					suggestionOrder = append(suggestionOrder, sugID)
				}
				entry.DeletedText += text
				entry.EndIndex = endIndex

				paraPlain.WriteString(text)
				paraAnnotated.WriteString(fmt.Sprintf("[Existing Suggestion (Delete ID:%s): %q]", sugID, strings.ReplaceAll(text, "\n", "")))
			} else {
				paraPlain.WriteString(text)
				paraPreview.WriteString(text)
				paraAnnotated.WriteString(text)

				// Split multi-line textRuns on '\n' so each line forms a clean single-line EditableBlock
				segments := strings.Split(text, "\n")
				segStartUTF16 := startIndex
				for sIdx, seg := range segments {
					isLastSeg := sIdx == len(segments)-1
					hasNewlineAfter := !isLastSeg
					segUTF16Len := utf16idx.UTF16Len(seg)
					segEndUTF16 := segStartUTF16 + segUTF16Len

					if len(seg) > 0 {
						if currentBlock != nil && currentBlock.EndIndex == segStartUTF16 {
							currentBlock.Text += seg
							currentBlock.EndIndex = segEndUTF16
						} else {
							flushCurrentBlock()
							src := "body"
							if meta.isInsideTable {
								src = "table"
							}
							currentBlock = &utf16idx.EditableBlock{
								Text:           seg,
								StartIndex:     segStartUTF16,
								EndIndex:       segEndUTF16,
								Source:         src,
								TargetEditable: true,
							}
						}
					}

					if hasNewlineAfter {
						flushCurrentBlock()
						segStartUTF16 = segEndUTF16 + 1
					} else {
						segStartUTF16 = segEndUTF16
					}
				}
			}
		}

		flushCurrentBlock()
		return paraPlain.String(), paraPreview.String(), paraAnnotated.String()
	}

	docAppChildCounter := 0
	for _, rawSt := range bodyContent {
		st, ok := rawSt.(map[string]any)
		if !ok {
			continue
		}
		if para := getMap(st, "paragraph"); len(para) > 0 {
			currIdx := docAppChildCounter
			docAppChildCounter++
			pl, pr, an := processParagraph(para, paraMeta{
				isInsideTable:  false,
				bodyChildIndex: currIdx,
			})
			plainTextBuilder.WriteString(pl)
			previewAcceptedBuilder.WriteString(pr)
			annotatedBuilder.WriteString(an)
		} else if tbl := getMap(st, "table"); len(tbl) > 0 {
			currIdx := docAppChildCounter
			docAppChildCounter++
			tblIndex := len(allTablesList) + 1
			tblID := fmt.Sprintf("TABLE_%d", tblIndex)
			rows := getSlice(tbl, "tableRows")
			var markdownRows []string

			stStart := getInt(st, "startIndex")
			stEnd := getInt(st, "endIndex")
			firstCellStart := stStart + 1
			firstCellEnd := stStart + 2
			foundFirstCell := false
			colsCount := getInt(tbl, "columns")

			for rIdx, rawRow := range rows {
				rowMap, ok := rawRow.(map[string]any)
				if !ok {
					continue
				}
				cells := getSlice(rowMap, "tableCells")
				if rIdx == 0 && colsCount == 0 {
					colsCount = len(cells)
				}
				var rowCellsAnnotated []string

				for cIdx, rawCell := range cells {
					cellMap, ok := rawCell.(map[string]any)
					if !ok {
						continue
					}
					var cellParts []string
					for _, rawCellSt := range getSlice(cellMap, "content") {
						cellSt, ok := rawCellSt.(map[string]any)
						if !ok {
							continue
						}
						if cellPara := getMap(cellSt, "paragraph"); len(cellPara) > 0 {
							if !foundFirstCell {
								pEls := getSlice(cellPara, "elements")
								if len(pEls) > 0 {
									if fEl, ok := pEls[0].(map[string]any); ok {
										firstCellStart = getInt(fEl, "startIndex")
										firstCellEnd = maxInt(firstCellStart+1, getInt(fEl, "endIndex")-1)
										foundFirstCell = true
									}
								}
							}
							pl, pr, an := processParagraph(cellPara, paraMeta{
								isInsideTable:  true,
								bodyChildIndex: currIdx,
								tableNumber:    tblIndex,
								rowIndex:       rIdx,
								colIndex:       cIdx,
							})
							plainTextBuilder.WriteString(pl)
							previewAcceptedBuilder.WriteString(pr)
							cleanAn := strings.TrimSpace(strings.ReplaceAll(an, "\n", " "))
							if cleanAn != "" {
								cellParts = append(cellParts, cleanAn)
							}
						}
					}
					rowCellsAnnotated = append(rowCellsAnnotated, strings.Join(cellParts, " "))
				}

				markdownRows = append(markdownRows, "| "+strings.Join(rowCellsAnnotated, " | ")+" |")
				if rIdx == 0 && len(rowCellsAnnotated) > 0 {
					seps := make([]string, len(rowCellsAnnotated))
					for i := range seps {
						seps[i] = "---"
					}
					markdownRows = append(markdownRows, "| "+strings.Join(seps, " | ")+" |")
				}
			}

			tableMD := fmt.Sprintf("\n[%s (%d rows)]\n%s\n\n", tblID, len(rows), strings.Join(markdownRows, "\n"))
			annotatedBuilder.WriteString(tableMD)
			previewAcceptedBuilder.WriteString(tableMD)

			if stStart == 0 {
				stStart = firstCellStart
			}
			if stEnd == 0 {
				stEnd = firstCellEnd
			}
			allTablesList = append(allTablesList, TableElement{
				TargetID:        tblID,
				TableNumber:     tblIndex,
				BodyChildIndex:  currIdx,
				RowsCount:       len(rows),
				ColsCount:       colsCount,
				Markdown:        strings.TrimSpace(tableMD),
				StartIndex:      firstCellStart,
				EndIndex:        firstCellEnd,
				TableStartIndex: stStart,
				TableEndIndex:   stEnd,
			})
		}
	}

	collectedIDs := collectAllSuggestionAndCommentIDs(docJSON, activeTabID)
	activeCommentsList, resolvedReviewsList := extractCommentAndSuggestionThreads(docJSON, activeTabID, collectedIDs.suggestionIDs)

	// Attach suggestion-linked comments to their corresponding suggestion entry
	for _, c := range activeCommentsList {
		if c.SuggestionID != "" {
			if sug, exists := existingSuggestionsMap[c.SuggestionID]; exists {
				sug.Comments = append(sug.Comments, c)
			}
		}
	}

	// Ensure non-text suggestion IDs (style/table/image) are also represented
	for _, sugID := range collectedIDs.suggestionIDs {
		if _, exists := existingSuggestionsMap[sugID]; !exists {
			var linked []ActiveComment
			var inferred []utf16idx.RangeInterval
			for _, c := range activeCommentsList {
				if c.SuggestionID == sugID {
					linked = append(linked, c)
					inferred = append(inferred, c.AnchorRanges...)
				}
			}
			sIdx, eIdx := 1, 1
			if len(inferred) > 0 {
				sIdx = inferred[0].StartIndex
				eIdx = inferred[0].EndIndex
			}
			existingSuggestionsMap[sugID] = &ExistingSuggestion{
				ID:           sugID,
				DeletedText:  "",
				InsertedText: "[Formatting / Structural Suggestion]",
				StartIndex:   sIdx,
				EndIndex:     eIdx,
				Comments:     linked,
			}
			suggestionOrder = append(suggestionOrder, sugID)
		}
	}

	var allTabSuggestionsPtrs []*ExistingSuggestion
	for _, id := range suggestionOrder {
		if s, ok := existingSuggestionsMap[id]; ok {
			allTabSuggestionsPtrs = append(allTabSuggestionsPtrs, s)
		}
	}

	fullUnfilteredActiveComments := activeCommentsList
	fullUnfilteredResolvedReviews := resolvedReviewsList

	trimmedFull := strings.TrimSpace(plainTextBuilder.String())
	isTabCompletelyEmpty := trimmedFull == "" &&
		strings.TrimSpace(previewAcceptedBuilder.String()) == "" &&
		len(allTablesList) == 0 &&
		len(allVisualElements) == 0 &&
		len(allTabSuggestionsPtrs) == 0 &&
		len(fullUnfilteredActiveComments) == 0 &&
		len(fullUnfilteredResolvedReviews) == 0

	var fullTabLines []string
	for _, b := range allEditableBlocks {
		fullTabLines = append(fullTabLines, b.Text)
	}
	fullTabSettledText := strings.Join(fullTabLines, "\n")

	var decisionRecord *DecisionRecordSummary
	if !opts.SkipDecisionReconcile {
		decisionRecord = ReconcileDecisionRecordWithTab(ReconcileParams{
			DocumentID:           getString(docJSON, "documentId"),
			ActiveTabID:          activeTabID,
			IsTabCompletelyEmpty: isTabCompletelyEmpty,
			FullTabSettledText:   fullTabSettledText,
			AllTabSuggestions:    allTabSuggestionsPtrs,
			ActiveSuggestionIDs:  collectedIDs.suggestionIDs,
			AllResolvedReviews:   fullUnfilteredResolvedReviews,
		})
	} else {
		for _, sug := range allTabSuggestionsPtrs {
			sug.IsAIGenerated = HasDocuMentorAISignature(sug)
		}
	}

	var existingSuggestionsList []ExistingSuggestion
	for _, id := range suggestionOrder {
		if s, ok := existingSuggestionsMap[id]; ok {
			existingSuggestionsList = append(existingSuggestionsList, *s)
		}
	}

	// Scope filtering when opts.SelectionOnly is true
	targetBlocks := allEditableBlocks
	targetTables := allTablesList
	targetVisuals := allVisualElements
	effectiveAnnotatedText := annotatedBuilder.String()
	selectedTextPreview := ""
	selectionRanges := normalizeAnchorRangesFromIntervals(opts.SelectionRanges)

	if opts.SelectionOnly && len(selectionRanges) > 0 {
		var filteredBlocks []utf16idx.EditableBlock
		var selectedTextsPool []string
		var unselectedTextsPool []string

		for _, b := range allEditableBlocks {
			bInterval := []utf16idx.RangeInterval{{StartIndex: b.StartIndex, EndIndex: maxInt(b.StartIndex+1, b.EndIndex)}}
			if utf16idx.RangesOverlap(bInterval, selectionRanges) {
				b.TargetEditable = true
				filteredBlocks = append(filteredBlocks, b)
				if b.Text != "" {
					selectedTextsPool = append(selectedTextsPool, b.Text)
				}
			} else if b.Text != "" {
				unselectedTextsPool = append(unselectedTextsPool, b.Text)
			}
		}
		targetBlocks = filteredBlocks

		var filteredTables []TableElement
		for _, t := range allTablesList {
			tInterval := []utf16idx.RangeInterval{{StartIndex: t.TableStartIndex, EndIndex: maxInt(t.TableStartIndex+1, t.TableEndIndex)}}
			if utf16idx.RangesOverlap(tInterval, selectionRanges) {
				filteredTables = append(filteredTables, t)
				if t.Markdown != "" {
					selectedTextsPool = append(selectedTextsPool, t.Markdown)
				}
			} else if t.Markdown != "" {
				unselectedTextsPool = append(unselectedTextsPool, t.Markdown)
			}
		}
		targetTables = filteredTables

		var filteredVisuals []VisualElement
		for _, v := range allVisualElements {
			vInterval := []utf16idx.RangeInterval{{StartIndex: v.StartIndex, EndIndex: maxInt(v.StartIndex+1, v.EndIndex)}}
			if utf16idx.RangesOverlap(vInterval, selectionRanges) {
				filteredVisuals = append(filteredVisuals, v)
				if v.Title != "" {
					selectedTextsPool = append(selectedTextsPool, v.Title)
				}
			}
		}
		targetVisuals = filteredVisuals

		var previewLines []string
		for _, b := range targetBlocks {
			previewLines = append(previewLines, b.Text)
		}
		selectedTextPreview = strings.Join(previewLines, "\n")

		var scopedParts []string
		if selectedTextPreview != "" {
			scopedParts = append(scopedParts, selectedTextPreview)
		}
		for _, t := range targetTables {
			scopedParts = append(scopedParts, t.Markdown)
		}
		for _, v := range targetVisuals {
			scopedParts = append(scopedParts, fmt.Sprintf("[%s (%s): Title=%q, Description=%q]", v.TargetID, v.Type, v.Title, v.Description))
		}
		effectiveAnnotatedText = strings.Join(scopedParts, "\n\n")

		var scopedSuggestions []ExistingSuggestion
		for _, s := range existingSuggestionsList {
			sInterval := []utf16idx.RangeInterval{{StartIndex: s.StartIndex, EndIndex: maxInt(s.StartIndex+1, s.EndIndex)}}
			if utf16idx.RangesOverlap(sInterval, selectionRanges) {
				scopedSuggestions = append(scopedSuggestions, s)
			}
		}
		existingSuggestionsList = scopedSuggestions
		activeCommentsList = FilterActiveCommentsBySelectionScope(activeCommentsList, selectionRanges, selectedTextsPool, unselectedTextsPool, existingSuggestionsMap)
		resolvedReviewsList = FilterResolvedReviewsBySelectionScope(resolvedReviewsList, selectionRanges, selectedTextsPool, unselectedTextsPool, existingSuggestionsMap)
	}

	effectiveCharCount := utf8.RuneCountInString(trimmedFull)
	if opts.SelectionOnly {
		effectiveCharCount = utf8.RuneCountInString(strings.TrimSpace(selectedTextPreview))
	}

	isEmpty := trimmedFull == "" &&
		strings.TrimSpace(previewAcceptedBuilder.String()) == "" &&
		len(allTablesList) == 0 &&
		len(allVisualElements) == 0 &&
		len(existingSuggestionsList) == 0 &&
		len(activeCommentsList) == 0 &&
		len(resolvedReviewsList) == 0

	return &ParsedDocument{
		DocumentID:          getString(docJSON, "documentId"),
		Title:               getString(docJSON, "title"),
		ActiveTabID:         activeTabID,
		ActiveTabTitle:      activeTabTitle,
		SelectionOnly:       opts.SelectionOnly,
		SelectionRanges:     selectionRanges,
		SelectedTextPreview: selectedTextPreview,
		IsEmpty:             isEmpty,
		CharCount:           effectiveCharCount,
		AnnotatedText:       effectiveAnnotatedText,
		EditableBlocks:      targetBlocks,
		ExistingSuggestions: existingSuggestionsList,
		ExistingComments:    activeCommentsList,
		ResolvedReviews:     resolvedReviewsList,
		AllSuggestionIDs:    collectedIDs.suggestionIDs,
		AllAICommentIDs:     collectedIDs.aiCommentIDs,
		AllCommentIDs:       collectedIDs.allCommentIDs,
		Tables:              targetTables,
		VisualElements:      targetVisuals,
		DecisionRecord:      decisionRecord,
	}
}

// ExtractPlainTextFromDocsJSON extracts plain text (including tables and all tabs) from a raw Docs JSON object.
func ExtractPlainTextFromDocsJSON(docJSON map[string]any) string {
	var extractElements func(elements []any) string
	extractElements = func(elements []any) string {
		var chunks []string
		for _, rawEl := range elements {
			el, ok := rawEl.(map[string]any)
			if !ok {
				continue
			}
			if para := getMap(el, "paragraph"); len(para) > 0 {
				var pText strings.Builder
				for _, rawPE := range getSlice(para, "elements") {
					pe, ok := rawPE.(map[string]any)
					if !ok {
						continue
					}
					tr := getMap(pe, "textRun")
					if content := getString(tr, "content"); content != "" {
						if len(getStringSlice(tr, "suggestedDeletionIds")) == 0 {
							pText.WriteString(content)
						}
					}
				}
				trimmed := strings.TrimRight(pText.String(), "\n")
				if strings.TrimSpace(trimmed) != "" {
					chunks = append(chunks, trimmed)
				}
			} else if tbl := getMap(el, "table"); len(tbl) > 0 {
				for _, rawRow := range getSlice(tbl, "tableRows") {
					row, ok := rawRow.(map[string]any)
					if !ok {
						continue
					}
					var cells []string
					hasNonEmpty := false
					for _, rawCell := range getSlice(row, "tableCells") {
						cell, ok := rawCell.(map[string]any)
						if !ok {
							continue
						}
						cTxt := strings.TrimSpace(strings.ReplaceAll(extractElements(getSlice(cell, "content")), "\n", " "))
						if cTxt != "" {
							hasNonEmpty = true
						}
						cells = append(cells, cTxt)
					}
					if hasNonEmpty {
						chunks = append(chunks, "| "+strings.Join(cells, " | ")+" |")
					}
				}
			}
		}
		return strings.Join(chunks, "\n")
	}

	allTabs := flattenDocumentTabs(getSlice(docJSON, "tabs"))
	if len(allTabs) > 0 {
		var tabTexts []string
		for _, t := range allTabs {
			if len(allTabs) > 1 && IsDocuMentorSummaryTab(t, "") {
				continue
			}
			dt := getMap(t, "documentTab")
			txt := strings.TrimSpace(extractElements(getSlice(getMap(dt, "body"), "content")))
			if txt != "" {
				tabTitle := getString(getMap(t, "tabProperties"), "title")
				if tabTitle != "" && len(allTabs) > 1 {
					tabTexts = append(tabTexts, fmt.Sprintf("### [%s]\n%s", tabTitle, txt))
				} else {
					tabTexts = append(tabTexts, txt)
				}
			}
		}
		return strings.TrimSpace(strings.Join(tabTexts, "\n\n"))
	}

	return strings.TrimSpace(extractElements(getSlice(getMap(docJSON, "body"), "content")))
}

// FilterActiveCommentsBySelectionScope enforces the Fail-Closed Selection Scope Boundary on active comments.
func FilterActiveCommentsBySelectionScope(
	items []ActiveComment,
	selRanges []utf16idx.RangeInterval,
	selTexts []string,
	unselTexts []string,
	sugMap map[string]*ExistingSuggestion,
) []ActiveComment {
	var out []ActiveComment
	for _, item := range items {
		itemRanges := append([]utf16idx.RangeInterval{}, item.AnchorRanges...)
		if item.SuggestionID != "" && sugMap != nil {
			if sug, ok := sugMap[item.SuggestionID]; ok && sug.StartIndex > 0 {
				itemRanges = append(itemRanges, utf16idx.RangeInterval{
					StartIndex: sug.StartIndex,
					EndIndex:   maxInt(sug.StartIndex+1, sug.EndIndex),
				})
			}
		}
		if matchSelectionScopeRule(itemRanges, item.QuotedText, selRanges, selTexts, unselTexts) {
			out = append(out, item)
		}
	}
	return out
}

// FilterResolvedReviewsBySelectionScope enforces the Fail-Closed Selection Scope Boundary on resolved review threads.
func FilterResolvedReviewsBySelectionScope(
	items []ResolvedReview,
	selRanges []utf16idx.RangeInterval,
	selTexts []string,
	unselTexts []string,
	sugMap map[string]*ExistingSuggestion,
) []ResolvedReview {
	var out []ResolvedReview
	for _, item := range items {
		itemRanges := append([]utf16idx.RangeInterval{}, item.AnchorRanges...)
		if item.SuggestionID != "" && sugMap != nil {
			if sug, ok := sugMap[item.SuggestionID]; ok && sug.StartIndex > 0 {
				itemRanges = append(itemRanges, utf16idx.RangeInterval{
					StartIndex: sug.StartIndex,
					EndIndex:   maxInt(sug.StartIndex+1, sug.EndIndex),
				})
			}
		}
		if matchSelectionScopeRule(itemRanges, item.QuotedText, selRanges, selTexts, unselTexts) {
			out = append(out, item)
		}
	}
	return out
}

func matchSelectionScopeRule(
	itemRanges []utf16idx.RangeInterval,
	quotedText string,
	selRanges []utf16idx.RangeInterval,
	selTexts []string,
	unselTexts []string,
) bool {
	// Rule A: When coordinate ranges exist on the thread, strictly require coordinate overlap with selection
	if len(itemRanges) > 0 {
		return utf16idx.RangesOverlap(itemRanges, selRanges)
	}
	// Rule B (Fail-Closed): When no coordinates exist, require non-empty quote matching selected text
	quote := strings.TrimSpace(strings.ReplaceAll(quotedText, "\n", " "))
	if quote == "" {
		return false
	}
	matchesSelected := false
	exactSelected := false
	for _, st := range selTexts {
		stClean := strings.TrimSpace(st)
		if strings.Contains(stClean, quote) {
			matchesSelected = true
		}
		if stClean == quote {
			exactSelected = true
		}
	}
	if !matchesSelected {
		return false
	}
	for _, ut := range unselTexts {
		if strings.TrimSpace(ut) == quote && !exactSelected {
			return false
		}
	}
	return true
}

type collectedIDsResult struct {
	suggestionIDs []string
	aiCommentIDs  []string
	allCommentIDs []string
}

func collectAllSuggestionAndCommentIDs(docJSON map[string]any, targetTabID string) collectedIDsResult {
	sugSet := make(map[string]bool)
	var sugList []string
	addSug := func(id string) {
		if id != "" && !sugSet[id] {
			sugSet[id] = true
			sugList = append(sugList, id)
		}
	}

	aiComSet := make(map[string]bool)
	var aiComList []string
	allComSet := make(map[string]bool)
	var allComList []string

	allTabs, effectiveTabs, tabAnchorMap, hasTabAnchorMapping := getEffectiveTabsAndAnchors(docJSON, targetTabID)

	collectFromComments := func(container any, ignoreTabFilter bool) {
		for _, entry := range normalizeContainerEntries(container) {
			c := entry.val
			commentID := getString(c, "commentId")
			if commentID == "" {
				commentID = entry.key
			}
			if commentID == "" || getBool(c, "deleted") || strings.EqualFold(getString(c, "status"), "RESOLVED") || getBool(c, "resolved") {
				continue
			}
			headPost := getMap(c, "headPost")
			if getBool(headPost, "deleted") {
				continue
			}
			anchorID := getString(c, "anchorId")
			if !ignoreTabFilter && hasTabAnchorMapping && anchorID != "" && len(tabAnchorMap) > 0 {
				if _, ok := tabAnchorMap[anchorID]; !ok {
					continue
				}
			}
			if !allComSet[commentID] {
				allComSet[commentID] = true
				allComList = append(allComList, commentID)
			}
			headText := extractPostText(headPost)
			if strings.Contains(headText, "DocuMentor AI") || strings.Contains(headText, "AI推敲コーチ") {
				if !aiComSet[commentID] {
					aiComSet[commentID] = true
					aiComList = append(aiComList, commentID)
				}
			}
		}
	}

	collectFromComments(docJSON["comments"], false)

	var scanElements func(contentArray []any)
	scanElements = func(contentArray []any) {
		for _, rawSt := range contentArray {
			st, ok := rawSt.(map[string]any)
			if !ok {
				continue
			}
			if p := getMap(st, "paragraph"); len(p) > 0 {
				for k := range getMap(p, "suggestedParagraphStyleChanges") {
					addSug(k)
				}
				for k := range getMap(p, "suggestedBulletChanges") {
					addSug(k)
				}
				for _, rawEl := range getSlice(p, "elements") {
					el, ok := rawEl.(map[string]any)
					if !ok {
						continue
					}
					if tr := getMap(el, "textRun"); len(tr) > 0 {
						for _, id := range getStringSlice(tr, "suggestedInsertionIds") {
							addSug(id)
						}
						for _, id := range getStringSlice(tr, "suggestedDeletionIds") {
							addSug(id)
						}
						for k := range getMap(tr, "suggestedTextStyleChanges") {
							addSug(k)
						}
					}
					if ioe := getMap(el, "inlineObjectElement"); len(ioe) > 0 {
						for _, id := range getStringSlice(ioe, "suggestedInsertionIds") {
							addSug(id)
						}
						for _, id := range getStringSlice(ioe, "suggestedDeletionIds") {
							addSug(id)
						}
						for k := range getMap(ioe, "suggestedInlineObjectPropertiesChanges") {
							addSug(k)
						}
					}
				}
			} else if tbl := getMap(st, "table"); len(tbl) > 0 {
				for _, id := range getStringSlice(tbl, "suggestedInsertionIds") {
					addSug(id)
				}
				for _, id := range getStringSlice(tbl, "suggestedDeletionIds") {
					addSug(id)
				}
				for _, rawRow := range getSlice(tbl, "tableRows") {
					row, ok := rawRow.(map[string]any)
					if !ok {
						continue
					}
					for _, id := range getStringSlice(row, "suggestedInsertionIds") {
						addSug(id)
					}
					for _, id := range getStringSlice(row, "suggestedDeletionIds") {
						addSug(id)
					}
					for _, rawCell := range getSlice(row, "tableCells") {
						cell, ok := rawCell.(map[string]any)
						if !ok {
							continue
						}
						for _, id := range getStringSlice(cell, "suggestedInsertionIds") {
							addSug(id)
						}
						for _, id := range getStringSlice(cell, "suggestedDeletionIds") {
							addSug(id)
						}
						scanElements(getSlice(cell, "content"))
					}
				}
			}
		}
	}

	if len(allTabs) > 0 {
		for _, t := range effectiveTabs {
			dt := getMap(t, "documentTab")
			if len(dt) > 0 {
				collectFromComments(dt["comments"], true)
				scanElements(getSlice(getMap(dt, "body"), "content"))
			}
		}
	} else {
		scanElements(getSlice(getMap(docJSON, "body"), "content"))
	}

	return collectedIDsResult{
		suggestionIDs: sugList,
		aiCommentIDs:  aiComList,
		allCommentIDs: allComList,
	}
}

func extractCommentAndSuggestionThreads(docJSON map[string]any, targetTabID string, activeSuggestionIDs []string) ([]ActiveComment, []ResolvedReview) {
	activeMap := make(map[string]ActiveComment)
	var activeOrder []string
	resolvedMap := make(map[string]ResolvedReview)
	var resolvedOrder []string

	activeSugSet := make(map[string]bool)
	for _, id := range activeSuggestionIDs {
		activeSugSet[id] = true
	}

	allTabs, effectiveTabs, tabAnchorMap, hasTabAnchorMapping := getEffectiveTabsAndAnchors(docJSON, targetTabID)

	processContainer := func(container any, isSugContainer, ignoreTabFilter bool) {
		for _, entry := range normalizeContainerEntries(container) {
			c := entry.val
			commentID := firstNonEmpty(getString(c, "commentId"), getString(c, "suggestionId"), getString(c, "id"), entry.key)
			if commentID == "" || getBool(c, "deleted") {
				continue
			}
			headPost := getMap(c, "headPost")
			if getBool(headPost, "deleted") {
				continue
			}
			anchorID := getString(c, "anchorId")
			if !isSugContainer && !ignoreTabFilter && hasTabAnchorMapping && anchorID != "" && len(tabAnchorMap) > 0 {
				if _, ok := tabAnchorMap[anchorID]; !ok {
					continue
				}
			}

			linkedSugID := getString(c, "suggestionId")
			if isSugContainer && linkedSugID == "" {
				linkedSugID = commentID
			}

			headContent := ""
			headAuthor := ""
			if len(headPost) > 0 && !getBool(headPost, "deleted") {
				headContent = extractPostText(headPost)
				headAuthor = getString(getMap(headPost, "author"), "displayName")
			} else {
				headContent = extractPostText(c)
				headAuthor = getString(getMap(c, "author"), "displayName")
			}

			rawReplies := getSlice(c, "replies")
			if len(rawReplies) == 0 {
				rawReplies = getSlice(c, "posts")
			}
			if len(rawReplies) == 0 {
				rawReplies = getSlice(c, "commentHistory")
			}
			if len(rawReplies) == 0 {
				rawReplies = getSlice(c, "suggestionHistory")
			}

			var replies []CommentReply
			detectedAction := ""
			for _, rawR := range rawReplies {
				rMap, ok := rawR.(map[string]any)
				if !ok || getBool(rMap, "deleted") {
					continue
				}
				rAction := strings.ToUpper(firstNonEmpty(getString(rMap, "suggestionAction"), getString(rMap, "commentAction"), getString(rMap, "action")))
				switch rAction {
				case "ACCEPT", "ACCEPTED":
					detectedAction = "ACCEPTED"
				case "REJECT", "REJECTED":
					detectedAction = "REJECTED"
				case "RESOLVE", "RESOLVED":
					if detectedAction == "" {
						detectedAction = "RESOLVED"
					}
				case "REOPEN", "REOPENED":
					detectedAction = ""
				}
				rContent := extractPostText(rMap)
				if rContent == "" {
					continue
				}
				rAuthor := getString(getMap(rMap, "author"), "displayName")
				if headAuthor == "" && rAuthor != "" {
					headAuthor = rAuthor
				}
				replies = append(replies, CommentReply{
					Author:  rAuthor,
					Content: rContent,
					Action:  rAction,
				})
			}

			quotedText := strings.TrimSpace(firstNonEmpty(
				getString(c, "plainTextQuote"),
				getString(c, "anchorText"),
				getString(getMap(c, "quotedFileContent"), "value"),
				getString(c, "summaryText"),
			))
			summaryText := strings.TrimSpace(getString(c, "summaryText"))

			isResolved := false
			resolutionState := "OPEN"
			rawStatus := strings.ToUpper(strings.TrimSpace(getString(c, "status")))

			if rawStatus == "RESOLVED" || rawStatus == "ACCEPTED" || rawStatus == "REJECTED" || getBool(c, "resolved") {
				isResolved = true
				if detectedAction != "" {
					resolutionState = detectedAction
				} else if rawStatus == "ACCEPTED" || rawStatus == "REJECTED" {
					resolutionState = rawStatus
				} else if isSugContainer {
					resolutionState = "RESOLVED_SUGGESTION"
				} else {
					resolutionState = "RESOLVED"
				}
			} else if isSugContainer && linkedSugID != "" && !activeSugSet[linkedSugID] {
				isResolved = true
				if detectedAction != "" {
					resolutionState = detectedAction
				} else {
					resolutionState = "RESOLVED_SUGGESTION"
				}
			} else if detectedAction == "ACCEPTED" || detectedAction == "REJECTED" || detectedAction == "RESOLVED" {
				isResolved = true
				resolutionState = detectedAction
			}

			threadType := "COMMENT"
			if isSugContainer || linkedSugID != "" {
				threadType = "SUGGESTION"
			}

			var resolvedAnchorRanges []utf16idx.RangeInterval
			if anchorID != "" {
				if rList, ok := tabAnchorMap[anchorID]; ok {
					resolvedAnchorRanges = append(resolvedAnchorRanges, rList...)
				}
			}
			resolvedAnchorRanges = append(resolvedAnchorRanges, extractRangesFromMap(c)...)

			if isResolved {
				if headContent == "" && quotedText == "" && summaryText == "" && len(replies) == 0 {
					continue
				}
				var parts []string
				if headContent != "" {
					parts = append(parts, headContent)
				} else if summaryText != "" {
					parts = append(parts, summaryText)
				}
				for _, rep := range replies {
					if rep.Author != "" {
						parts = append(parts, rep.Author+": "+rep.Content)
					} else {
						parts = append(parts, rep.Content)
					}
				}
				combinedTxt := strings.Join(parts, " | ")
				if combinedTxt == "" {
					combinedTxt = firstNonEmpty(summaryText, quotedText)
				}
				if _, exists := resolvedMap[commentID]; !exists {
					resolvedOrder = append(resolvedOrder, commentID)
				}
				resolvedMap[commentID] = ResolvedReview{
					CommentID:       commentID,
					AnchorID:        anchorID,
					AnchorRanges:    resolvedAnchorRanges,
					SuggestionID:    linkedSugID,
					ThreadType:      threadType,
					ResolutionState: resolutionState,
					Author:          headAuthor,
					Content:         firstNonEmpty(headContent, summaryText),
					Text:            combinedTxt,
					QuotedText:      quotedText,
					SummaryText:     summaryText,
					Replies:         replies,
				}
			} else {
				if headContent == "" && len(replies) == 0 {
					continue
				}
				if _, exists := activeMap[commentID]; !exists {
					activeOrder = append(activeOrder, commentID)
				}
				activeMap[commentID] = ActiveComment{
					CommentID:    commentID,
					AnchorID:     anchorID,
					AnchorRanges: resolvedAnchorRanges,
					SuggestionID: linkedSugID,
					Author:       headAuthor,
					Content:      headContent,
					QuotedText:   quotedText,
					Replies:      replies,
				}
			}
		}
	}

	processContainer(docJSON["comments"], false, false)
	processContainer(docJSON["suggestions"], true, true)

	if len(allTabs) > 0 {
		for _, t := range effectiveTabs {
			dt := getMap(t, "documentTab")
			if len(dt) > 0 {
				processContainer(dt["comments"], false, true)
				processContainer(dt["suggestions"], true, true)
			}
		}
	}

	var activeOut []ActiveComment
	for _, id := range activeOrder {
		activeOut = append(activeOut, activeMap[id])
	}
	var resolvedOut []ResolvedReview
	for _, id := range resolvedOrder {
		resolvedOut = append(resolvedOut, resolvedMap[id])
	}
	return activeOut, resolvedOut
}

func resolveTargetDocumentTab(docJSON map[string]any, preferredTabID string) (string, string, map[string]any) {
	allTabs := flattenDocumentTabs(getSlice(docJSON, "tabs"))
	if len(allTabs) == 0 {
		return "", getString(docJSON, "title"), docJSON
	}

	var matchedTab map[string]any
	if preferredTabID != "" {
		for _, t := range allTabs {
			if getString(getMap(t, "tabProperties"), "tabId") == preferredTabID {
				matchedTab = t
				break
			}
		}
	}
	if matchedTab == nil {
		for _, t := range allTabs {
			if !IsDocuMentorSummaryTab(t, "") {
				matchedTab = t
				break
			}
		}
	}
	if matchedTab == nil {
		matchedTab = allTabs[0]
	}
	props := getMap(matchedTab, "tabProperties")
	tabID := getString(props, "tabId")
	tabTitle := firstNonEmpty(getString(props, "title"), getString(docJSON, "title"))
	docTab := getMap(matchedTab, "documentTab")
	if len(docTab) == 0 {
		docTab = docJSON
	}
	return tabID, tabTitle, docTab
}

func flattenDocumentTabs(rawTabs []any) []map[string]any {
	var result []map[string]any
	var walk func(list []any)
	walk = func(list []any) {
		for _, item := range list {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			result = append(result, t)
			if children := getSlice(t, "childTabs"); len(children) > 0 {
				walk(children)
			}
		}
	}
	walk(rawTabs)
	return result
}

func getEffectiveTabsAndAnchors(docJSON map[string]any, targetTabID string) ([]map[string]any, []map[string]any, map[string][]utf16idx.RangeInterval, bool) {
	allTabs := flattenDocumentTabs(getSlice(docJSON, "tabs"))
	tabAnchorMap := make(map[string][]utf16idx.RangeInterval)
	hasTabAnchorMapping := false
	var effectiveTabs []map[string]any

	registerAnchors := func(container any) {
		if container == nil {
			return
		}
		entries := normalizeContainerEntries(container)
		if len(entries) > 0 {
			hasTabAnchorMapping = true
		}
		for _, e := range entries {
			anchorID := firstNonEmpty(getString(e.val, "anchorId"), e.key)
			if anchorID != "" {
				tabAnchorMap[anchorID] = extractRangesFromMap(e.val)
			}
		}
	}

	if docJSON["commentAnchors"] != nil {
		registerAnchors(docJSON["commentAnchors"])
	}

	if len(allTabs) > 0 {
		if targetTabID != "" {
			for _, t := range allTabs {
				if getString(getMap(t, "tabProperties"), "tabId") == targetTabID {
					effectiveTabs = append(effectiveTabs, t)
				}
			}
		}
		if len(effectiveTabs) == 0 {
			for _, t := range allTabs {
				if !IsDocuMentorSummaryTab(t, "") {
					effectiveTabs = append(effectiveTabs, t)
				}
			}
			if len(effectiveTabs) == 0 {
				effectiveTabs = allTabs
			}
			if targetTabID != "" {
				effectiveTabs = []map[string]any{allTabs[0]}
			}
		}
		for _, t := range effectiveTabs {
			dt := getMap(t, "documentTab")
			if dt["commentAnchors"] != nil {
				registerAnchors(dt["commentAnchors"])
			}
		}
	}

	return allTabs, effectiveTabs, tabAnchorMap, hasTabAnchorMapping
}

type containerEntry struct {
	key string
	val map[string]any
}

func normalizeContainerEntries(container any) []containerEntry {
	var out []containerEntry
	switch v := container.(type) {
	case []any:
		for i, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, containerEntry{key: fmt.Sprintf("%d", i), val: m})
			}
		}
	case map[string]any:
		var keys []string
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if m, ok := v[k].(map[string]any); ok {
				out = append(out, containerEntry{key: k, val: m})
			}
		}
	}
	return out
}

func extractRangesFromMap(m map[string]any) []utf16idx.RangeInterval {
	if len(m) == 0 {
		return nil
	}
	var rawList []any
	for _, key := range []string{"ranges", "anchorRanges", "anchorSpans"} {
		if s := getSlice(m, key); len(s) > 0 {
			rawList = append(rawList, s...)
		}
	}
	if r := getMap(m, "range"); len(r) > 0 {
		rawList = append(rawList, r)
	}
	if r := getMap(m, "anchorSpan"); len(r) > 0 {
		rawList = append(rawList, r)
	}
	if len(rawList) == 0 {
		rawList = append(rawList, m)
	}

	var out []utf16idx.RangeInterval
	for _, rawItem := range rawList {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		target := item
		if sub := getMap(item, "range"); len(sub) > 0 {
			target = sub
		} else if sub := getMap(item, "anchorSpan"); len(sub) > 0 {
			target = sub
		}
		sVal, hasS := getNumber(target, "startIndex")
		eVal, hasE := getNumber(target, "endIndex")
		if hasS && hasE && sVal >= 0 && eVal >= sVal {
			out = append(out, utf16idx.RangeInterval{
				StartIndex: sVal,
				EndIndex:   maxInt(sVal+1, eVal),
			})
		}
	}
	return out
}

func normalizeAnchorRangesFromIntervals(in []utf16idx.RangeInterval) []utf16idx.RangeInterval {
	var out []utf16idx.RangeInterval
	for _, r := range in {
		if r.StartIndex >= 0 && r.EndIndex >= r.StartIndex {
			out = append(out, utf16idx.RangeInterval{
				StartIndex: r.StartIndex,
				EndIndex:   maxInt(r.StartIndex+1, r.EndIndex),
			})
		}
	}
	return out
}

func extractPostText(post map[string]any) string {
	if len(post) == 0 {
		return ""
	}
	if s, ok := post["content"].(string); ok {
		return strings.TrimSpace(s)
	}
	if cMap := getMap(post, "content"); len(cMap) > 0 {
		if s, ok := cMap["text"].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	if s, ok := post["text"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func getMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

func getSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].([]any); ok {
		return v
	}
	return nil
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringSlice(m map[string]any, key string) []string {
	raw := getSlice(m, key)
	var out []string
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func getBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func getInt(m map[string]any, key string) int {
	val, _ := getNumber(m, key)
	return val
}

func getNumber(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
