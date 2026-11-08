package gdocs

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

// RawElementSuggestion represents a review comment on a figure/chart (VISUAL_1) or table (TABLE_1).
type RawElementSuggestion struct {
	TargetID      string `json:"targetId"`
	Category      string `json:"category"`
	Suggestion    string `json:"suggestion"`
	Reason        string `json:"reason"`
	LearningPoint string `json:"learningPoint"`
}

// MatchedElementSuggestion represents a resolved element comment ready for anchor insertion.
type MatchedElementSuggestion struct {
	ID             string `json:"id"`
	TargetID       string `json:"targetId"`
	ElementKind    string `json:"elementKind"` // "VISUAL" or "TABLE"
	Category       string `json:"category"`
	Suggestion     string `json:"suggestion"`
	Reason         string `json:"reason"`
	LearningPoint  string `json:"learningPoint"`
	StartIndex     int    `json:"startIndex"`
	EndIndex       int    `json:"endIndex"`
	ParaStartIndex int    `json:"paraStartIndex"`
	ParaEndIndex   int    `json:"paraEndIndex"`
	TabID          string `json:"tabId,omitempty"`
}

// BuiltRequestData holds the Step 3 output payloads for Step 4 application.
type BuiltRequestData struct {
	ActiveTabID            string                     `json:"activeTabId"`
	HintOnly               bool                       `json:"hintOnly"`
	MatchedCount           int                        `json:"matchedCount"`
	MatchedTextCount       int                        `json:"matchedTextCount"`
	MatchedElementCount    int                        `json:"matchedElementCount"`
	MatchedItems           []utf16idx.MatchedSuggestion `json:"matchedItems"`
	MatchedElementItems    []MatchedElementSuggestion `json:"matchedElementItems"`
	HintCommentRequests    []map[string]any           `json:"hintCommentRequests,omitempty"`
	SuggestBatchUpdateBody map[string]any             `json:"suggestBatchUpdateBody"`
}

// ApplyResult summarizes the outcome of Step 4 (applying suggestions and comments to Google Docs).
type ApplyResult struct {
	Success                     bool                   `json:"success"`
	HintOnly                    bool                   `json:"hintOnly"`
	AppliedSuggestionsCount     int                    `json:"appliedSuggestionsCount"`
	AppliedTextSuggestionsCount int                    `json:"appliedTextSuggestionsCount"`
	AppliedElementCommentsCount int                    `json:"appliedElementCommentsCount"`
	DecisionRecord              *DecisionRecordSummary `json:"decisionRecord,omitempty"`
}

// BuildRequestBody constructs the Step 3 batchUpdate payload for inline suggestions (WriteMode.SUGGEST)
// or Hint-Only coaching comments (insertComment), sorted in descending StartIndex order (bottom-to-top).
func BuildRequestBody(
	docData *ParsedDocument,
	rawSuggestions []utf16idx.RawSuggestion,
	rawElementSuggestions []RawElementSuggestion,
	lang string,
	hintOnly bool,
) *BuiltRequestData {
	isJa := lang == "ja"
	activeTabID := ""
	var editableBlocks []utf16idx.EditableBlock
	if docData != nil {
		activeTabID = docData.ActiveTabID
		editableBlocks = docData.EditableBlocks
	}

	defaultCat := "Revision"
	if isJa {
		defaultCat = "推敲"
	}

	matchedItems := utf16idx.MatchSuggestionsToBlocks(editableBlocks, rawSuggestions, activeTabID, hintOnly, defaultCat)

	var suggestRequests []map[string]any
	var hintCommentRequests []map[string]any

	if hintOnly {
		for _, item := range matchedItems {
			anchorRange := map[string]any{
				"startIndex": item.StartIndex,
				"endIndex":   maxInt(item.StartIndex+1, item.EndIndex),
			}
			if activeTabID != "" {
				anchorRange["tabId"] = activeTabID
			}
			hintCommentRequests = append(hintCommentRequests, map[string]any{
				"insertComment": map[string]any{
					"content": FormatHintComment(item, isJa),
					"range":   anchorRange,
				},
			})
		}
	} else {
		for _, item := range matchedItems {
			delRange := map[string]any{
				"startIndex": item.StartIndex,
				"endIndex":   item.EndIndex,
			}
			if activeTabID != "" {
				delRange["tabId"] = activeTabID
			}
			suggestRequests = append(suggestRequests, map[string]any{
				"deleteContentRange": map[string]any{
					"range": delRange,
				},
			})

			if len(item.SuggestedText) > 0 {
				insLoc := map[string]any{
					"index": item.StartIndex,
				}
				if activeTabID != "" {
					insLoc["tabId"] = activeTabID
				}
				suggestRequests = append(suggestRequests, map[string]any{
					"insertText": map[string]any{
						"location": insLoc,
						"text":     item.SuggestedText,
					},
				})
			}
		}
	}

	type elementCoords struct {
		startIndex     int
		endIndex       int
		paraStartIndex int
		paraEndIndex   int
	}
	elementMap := make(map[string]elementCoords)
	if docData != nil {
		for _, v := range docData.VisualElements {
			elementMap[v.TargetID] = elementCoords{
				startIndex:     v.StartIndex,
				endIndex:       maxInt(v.StartIndex+1, v.EndIndex),
				paraStartIndex: maxInt(1, v.ParaStartIndex),
				paraEndIndex:   maxInt(v.StartIndex+1, v.ParaEndIndex),
			}
		}
		for _, t := range docData.Tables {
			elementMap[t.TargetID] = elementCoords{
				startIndex:     t.StartIndex,
				endIndex:       maxInt(t.StartIndex+1, t.EndIndex),
				paraStartIndex: t.StartIndex,
				paraEndIndex:   maxInt(t.StartIndex+1, t.EndIndex),
			}
		}
	}

	var matchedElements []MatchedElementSuggestion
	for idx, elSug := range rawElementSuggestions {
		coords, ok := elementMap[elSug.TargetID]
		if !ok {
			continue
		}
		isVisual := strings.HasPrefix(elSug.TargetID, "VISUAL_")
		kind := "TABLE"
		defaultElCat := "Table Review Comment"
		if isJa {
			defaultElCat = "表への推敲コメント"
		}
		if isVisual {
			kind = "VISUAL"
			defaultElCat = "Figure Review Comment"
			if isJa {
				defaultElCat = "図への推敲コメント"
			}
		}
		cat := strings.TrimSpace(elSug.Category)
		if cat == "" {
			cat = defaultElCat
		}
		matchedElements = append(matchedElements, MatchedElementSuggestion{
			ID:             fmt.Sprintf("el_sug_%d", idx+1),
			TargetID:       elSug.TargetID,
			ElementKind:    kind,
			Category:       cat,
			Suggestion:     elSug.Suggestion,
			Reason:         elSug.Reason,
			LearningPoint:  elSug.LearningPoint,
			StartIndex:     coords.startIndex,
			EndIndex:       coords.endIndex,
			ParaStartIndex: coords.paraStartIndex,
			ParaEndIndex:   coords.paraEndIndex,
			TabID:          activeTabID,
		})
	}

	var suggestBatchUpdateBody map[string]any
	if hintOnly {
		suggestBatchUpdateBody = map[string]any{
			"requests": hintCommentRequests,
		}
	} else {
		suggestBatchUpdateBody = map[string]any{
			"writeControl": map[string]any{
				"writeMode": "SUGGEST",
			},
			"requests": suggestRequests,
		}
	}

	return &BuiltRequestData{
		ActiveTabID:            activeTabID,
		HintOnly:               hintOnly,
		MatchedCount:           len(matchedItems) + len(matchedElements),
		MatchedTextCount:       len(matchedItems),
		MatchedElementCount:    len(matchedElements),
		MatchedItems:           matchedItems,
		MatchedElementItems:    matchedElements,
		HintCommentRequests:    hintCommentRequests,
		SuggestBatchUpdateBody: suggestBatchUpdateBody,
	}
}

// ApplySuggestions executes Step 4 against the Google Docs API:
// Phase 1: Applies either Hint-Only anchored comments (insertComment) OR inline text suggestions
// (WriteMode.SUGGEST) followed by addCommentReply explanations on each createdSuggestionId.
// Phase 2: Re-fetches updated document coordinates (when Phase 1 modified text indices) and attaches
// figure/table review comments via 3-tier anchor fallback.
func (c *Client) ApplySuggestions(ctx context.Context, docID string, built *BuiltRequestData, lang string) (*ApplyResult, error) {
	if built == nil || (len(built.MatchedItems) == 0 && len(built.MatchedElementItems) == 0) {
		return &ApplyResult{Success: true}, nil
	}
	isJa := lang == "ja"
	activeTabID := built.ActiveTabID
	textItems := built.MatchedItems
	elementItems := built.MatchedElementItems

	// Phase 1: Text suggestions or Hint-Only comments
	if len(textItems) > 0 {
		if built.HintOnly {
			reqs := built.HintCommentRequests
			if len(reqs) == 0 {
				for _, item := range textItems {
					anchorRange := map[string]any{
						"startIndex": item.StartIndex,
						"endIndex":   maxInt(item.StartIndex+1, item.EndIndex),
					}
					if item.TabID != "" || activeTabID != "" {
						anchorRange["tabId"] = firstNonEmpty(item.TabID, activeTabID)
					}
					reqs = append(reqs, map[string]any{
						"insertComment": map[string]any{
							"content": FormatHintComment(item, isJa),
							"range":   anchorRange,
						},
					})
				}
			}
			c.ExecuteSuggestionActionRequestsSafely(ctx, docID, reqs)
		} else {
			suggestResJSON, err := c.ExecuteBatchUpdate(ctx, docID, built.SuggestBatchUpdateBody)
			if err != nil {
				return nil, err
			}
			sugResponses := getSlice(suggestResJSON, "suggestionResponses")
			reqCursor := 0
			var replyRequests []map[string]any
			var issuedAISuggestions []IssuedAISuggestionInput

			for idx, item := range textItems {
				numReqs := 1
				if len(item.SuggestedText) > 0 {
					numReqs = 2
				}
				foundSuggestionID := ""
				for k := 0; k < numReqs; k++ {
					if reqCursor+k < len(sugResponses) {
						if srMap, ok := sugResponses[reqCursor+k].(map[string]any); ok {
							createdIDs := getStringSlice(srMap, "createdSuggestionIds")
							if len(createdIDs) > 0 {
								foundSuggestionID = createdIDs[0]
							}
						}
					}
				}
				reqCursor += numReqs

				commentText := FormatLearningComment(item, isJa)
				if foundSuggestionID != "" {
					replyRequests = append(replyRequests, map[string]any{
						"addCommentReply": map[string]any{
							"suggestionId": foundSuggestionID,
							"post": map[string]any{
								"content": commentText,
							},
						},
					})
				} else {
					anchorRange := map[string]any{
						"startIndex": item.StartIndex,
						"endIndex":   item.StartIndex + maxInt(1, utf16idx.UTF16Len(item.SuggestedText)),
					}
					if item.TabID != "" || activeTabID != "" {
						anchorRange["tabId"] = firstNonEmpty(item.TabID, activeTabID)
					}
					replyRequests = append(replyRequests, map[string]any{
						"insertComment": map[string]any{
							"content": commentText,
							"range":   anchorRange,
						},
					})
				}

				recSugID := foundSuggestionID
				if recSugID == "" {
					recSugID = fmt.Sprintf("ai_sug_%d_%d", idx+1, item.StartIndex)
				}
				issuedAISuggestions = append(issuedAISuggestions, IssuedAISuggestionInput{
					SuggestionID:  recSugID,
					Category:      item.Category,
					OriginalText:  item.OriginalText,
					SuggestedText: item.SuggestedText,
					CtxBefore:     item.CtxBefore,
					CtxAfter:      item.CtxAfter,
					TabID:         firstNonEmpty(item.TabID, activeTabID),
				})
			}

			if len(replyRequests) > 0 {
				c.ExecuteSuggestionActionRequestsSafely(ctx, docID, replyRequests)
			}
			if len(issuedAISuggestions) > 0 {
				_ = RegisterIssuedAISuggestions(docID, issuedAISuggestions, activeTabID)
			}
		}
	}

	updatedDecisionRecord := LoadDecisionRecordSummary(docID)

	// Phase 2: Element review comments (VISUAL_1, TABLE_1) with 3-tier anchor fallback
	appliedElementCount := 0
	if len(elementItems) > 0 {
		type freshCoords struct {
			startIndex     int
			endIndex       int
			paraStartIndex int
			paraEndIndex   int
		}
		freshMap := make(map[string]freshCoords)
		var freshBlocks []utf16idx.EditableBlock

		if !built.HintOnly && len(textItems) > 0 {
			if freshDocJSON, err := c.FetchRawDocumentJSON(ctx, docID); err == nil {
				freshParsed := ParseDocumentStructure(freshDocJSON, ParseOptions{
					PreferredTabID:        activeTabID,
					SelectionOnly:         false,
					SkipDecisionReconcile: true,
				})
				for _, v := range freshParsed.VisualElements {
					freshMap[v.TargetID] = freshCoords{
						startIndex:     v.StartIndex,
						endIndex:       v.EndIndex,
						paraStartIndex: v.ParaStartIndex,
						paraEndIndex:   v.ParaEndIndex,
					}
				}
				for _, t := range freshParsed.Tables {
					freshMap[t.TargetID] = freshCoords{
						startIndex:     t.StartIndex,
						endIndex:       t.EndIndex,
						paraStartIndex: t.StartIndex,
						paraEndIndex:   t.EndIndex,
					}
				}
				freshBlocks = freshParsed.EditableBlocks
			}
		}

		for _, elItem := range elementItems {
			elCommentText := FormatElementComment(elItem, isJa)
			sIdx := elItem.StartIndex
			eIdx := elItem.EndIndex
			pStart := elItem.ParaStartIndex
			pEnd := elItem.ParaEndIndex
			if fc, ok := freshMap[elItem.TargetID]; ok {
				sIdx = fc.startIndex
				eIdx = fc.endIndex
				pStart = fc.paraStartIndex
				pEnd = fc.paraEndIndex
			}
			effTabID := firstNonEmpty(elItem.TabID, activeTabID)

			var candidateRanges []map[string]any
			seenKeys := make(map[string]bool)
			addCandidate := func(start, end int) {
				s := maxInt(1, start)
				e := maxInt(s+1, end)
				key := fmt.Sprintf("%d:%d", s, e)
				if seenKeys[key] {
					return
				}
				seenKeys[key] = true
				r := map[string]any{"startIndex": s, "endIndex": e}
				if effTabID != "" {
					r["tabId"] = effTabID
				}
				candidateRanges = append(candidateRanges, r)
			}

			// Tier 1: Exact element range
			addCandidate(sIdx, eIdx)
			// Tier 2: Containing paragraph range
			if pStart > 0 && pEnd > 0 {
				addCandidate(pStart, pEnd)
			}
			// Tier 3: Nearest settled text block fallback
			if len(freshBlocks) > 0 {
				targetPos := maxInt(1, sIdx)
				sortedBlocks := make([]utf16idx.EditableBlock, len(freshBlocks))
				copy(sortedBlocks, freshBlocks)
				sort.Slice(sortedBlocks, func(i, j int) bool {
					distA := minIntVal(absInt(sortedBlocks[i].StartIndex-targetPos), absInt(sortedBlocks[i].EndIndex-targetPos))
					distB := minIntVal(absInt(sortedBlocks[j].StartIndex-targetPos), absInt(sortedBlocks[j].EndIndex-targetPos))
					return distA < distB
				})
				addCandidate(sortedBlocks[0].StartIndex, sortedBlocks[0].EndIndex)
			}

			for _, cand := range candidateRanges {
				if c.TryInsertCommentRequest(ctx, docID, elCommentText, cand) {
					appliedElementCount++
					break
				}
			}
		}
	}

	return &ApplyResult{
		Success:                     true,
		HintOnly:                    built.HintOnly,
		AppliedSuggestionsCount:     len(textItems) + appliedElementCount,
		AppliedTextSuggestionsCount: len(textItems),
		AppliedElementCommentsCount: appliedElementCount,
		DecisionRecord:              updatedDecisionRecord,
	}, nil
}

// AcceptAllSuggestions accepts all pending inline suggestions in the target tab and updates the Decision Record.
func (c *Client) AcceptAllSuggestions(ctx context.Context, docID, tabID string) (int, *DecisionRecordSummary, error) {
	docJSON, err := c.FetchRawDocumentJSON(ctx, docID)
	if err != nil {
		return 0, nil, err
	}
	// Reconcile first so any newly discovered AI suggestions are known
	parsed := ParseDocumentStructure(docJSON, ParseOptions{
		PreferredTabID: tabID,
		SelectionOnly:  false,
	})
	ids := parsed.AllSuggestionIDs
	if len(ids) == 0 {
		return 0, parsed.DecisionRecord, nil
	}
	var reqs []map[string]any
	for _, id := range ids {
		reqs = append(reqs, map[string]any{
			"acceptSuggestion": map[string]any{"suggestionId": id},
		})
	}
	cnt := c.ExecuteSuggestionActionRequestsSafely(ctx, docID, reqs)
	dr := RecordBulkSuggestionDecision(docID, ids, "ACCEPTED")
	return cnt, dr, nil
}

// RejectAllSuggestions rejects (clears) all pending inline suggestions in the target tab and updates the Decision Record.
func (c *Client) RejectAllSuggestions(ctx context.Context, docID, tabID string) (int, *DecisionRecordSummary, error) {
	docJSON, err := c.FetchRawDocumentJSON(ctx, docID)
	if err != nil {
		return 0, nil, err
	}
	parsed := ParseDocumentStructure(docJSON, ParseOptions{
		PreferredTabID: tabID,
		SelectionOnly:  false,
	})
	ids := parsed.AllSuggestionIDs
	if len(ids) == 0 {
		return 0, parsed.DecisionRecord, nil
	}
	var reqs []map[string]any
	for _, id := range ids {
		reqs = append(reqs, map[string]any{
			"rejectSuggestion": map[string]any{"suggestionId": id},
		})
	}
	cnt := c.ExecuteSuggestionActionRequestsSafely(ctx, docID, reqs)
	dr := RecordBulkSuggestionDecision(docID, ids, "REJECTED")
	return cnt, dr, nil
}

// DeleteAllComments deletes all active comment threads in the target tab.
func (c *Client) DeleteAllComments(ctx context.Context, docID, tabID string) (int, error) {
	docJSON, err := c.FetchRawDocumentJSON(ctx, docID)
	if err != nil {
		return 0, err
	}
	ids := collectAllSuggestionAndCommentIDs(docJSON, tabID).allCommentIDs
	if len(ids) == 0 {
		return 0, nil
	}
	var reqs []map[string]any
	for _, id := range ids {
		reqs = append(reqs, map[string]any{
			"deleteComment": map[string]any{"commentId": id},
		})
	}
	return c.ExecuteSuggestionActionRequestsSafely(ctx, docID, reqs), nil
}

// ResetDocumentDecisionRecord resets the Coach Decision Record for docID to 0 while recording all currently
// resolved review thread IDs on the Google Document into ReconciledThreadIDs so pre-reset resolved threads
// never resurrect on subsequent runs on the same Google Document.
func (c *Client) ResetDocumentDecisionRecord(ctx context.Context, docID, tabID string) *DecisionRecordSummary {
	var suppressedIDs []string
	if docJSON, err := c.FetchRawDocumentJSON(ctx, docID); err == nil && len(docJSON) > 0 {
		parsed := ParseDocumentStructure(docJSON, ParseOptions{
			PreferredTabID:        tabID,
			SelectionOnly:         false,
			SkipDecisionReconcile: true,
		})
		for _, r := range parsed.ResolvedReviews {
			if r.SuggestionID != "" {
				suppressedIDs = append(suppressedIDs, r.SuggestionID)
			}
			if r.CommentID != "" {
				suppressedIDs = append(suppressedIDs, r.CommentID)
			}
		}
	}
	return ResetDecisionRecord(docID, suppressedIDs...)
}

// FormatLearningComment formats the educational reply attached to each inline suggestion card.
func FormatLearningComment(item utf16idx.MatchedSuggestion, isJa bool) string {
	if isJa {
		return strings.Join([]string{
			fmt.Sprintf("【DocuMentor AI｜%s】", item.Category),
			"💡 推敲の理由:",
			item.Reason,
			"",
			"🎓 学習ポイント:",
			item.LearningPoint,
		}, "\n")
	}
	return strings.Join([]string{
		fmt.Sprintf("[DocuMentor AI | %s]", item.Category),
		"💡 Reason for Revision:",
		item.Reason,
		"",
		"🎓 Learning Point:",
		item.LearningPoint,
	}, "\n")
}

// FormatHintComment formats the Socratic coaching hint anchored in Hint-Only Mode.
func FormatHintComment(item utf16idx.MatchedSuggestion, isJa bool) string {
	if isJa {
		return strings.Join([]string{
			fmt.Sprintf("【DocuMentor AI｜💡 ヒントモード：%s】", item.Category),
			fmt.Sprintf("🎯 対象箇所: 「%s」", item.OriginalText),
			"",
			"💡 見直しの理由（問題点）:",
			item.Reason,
			"",
			"🎓 推敲のヒント（自分で書き直してみましょう！）:",
			item.LearningPoint,
		}, "\n")
	}
	return strings.Join([]string{
		fmt.Sprintf("[DocuMentor AI | 💡 Hint Mode: %s]", item.Category),
		fmt.Sprintf("🎯 Flagged Phrase: %q", item.OriginalText),
		"",
		"💡 Issue to Check:",
		item.Reason,
		"",
		"🎓 Coaching Hint (Try Rewriting Yourself!):",
		item.LearningPoint,
	}, "\n")
}

// FormatElementComment formats the review comment anchored to a figure, chart, or table.
func FormatElementComment(elItem MatchedElementSuggestion, isJa bool) string {
	isVisual := strings.HasPrefix(elItem.TargetID, "VISUAL_")
	if isJa {
		label := "📊 表への推敲コメント:"
		if isVisual {
			label = "🖼️ 図への推敲コメント:"
		}
		return strings.Join([]string{
			fmt.Sprintf("【DocuMentor AI｜%s: %s】", elItem.TargetID, elItem.Category),
			label,
			elItem.Suggestion,
			"",
			"💡 理由:",
			elItem.Reason,
			"",
			"🎓 学習ポイント:",
			elItem.LearningPoint,
		}, "\n")
	}
	label := "📊 Table Review Comment:"
	if isVisual {
		label = "🖼️ Figure Review Comment:"
	}
	return strings.Join([]string{
		fmt.Sprintf("[DocuMentor AI | %s: %s]", elItem.TargetID, elItem.Category),
		label,
		elItem.Suggestion,
		"",
		"💡 Reason:",
		elItem.Reason,
		"",
		"🎓 Learning Point:",
		elItem.LearningPoint,
	}, "\n")
}

func minIntVal(a, b int) int {
	if a < b {
		return a
	}
	return b
}
