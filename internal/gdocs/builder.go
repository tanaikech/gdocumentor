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
	ObjectID       string `json:"objectId,omitempty"`
	TableSignature string `json:"tableSignature,omitempty"`
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
	AnchorStatus   string `json:"anchorStatus,omitempty"`
}

// RevisionControlSummary captures optimistic revision-locking and UTF-16 re-anchoring telemetry.
type RevisionControlSummary struct {
	SourceRevisionID  string `json:"sourceRevisionId,omitempty"`
	AppliedRevisionID string `json:"appliedRevisionId,omitempty"`
	RevisionRetries   int    `json:"revisionRetries"`
	ExactCount        int    `json:"exactCount"`
	ReanchoredCount   int    `json:"reanchoredCount"`
	StaleSkippedCount int    `json:"staleSkippedCount"`
}

// BuiltRequestData holds the Step 3 output payloads for Step 4 application.
type BuiltRequestData struct {
	ActiveTabID            string                         `json:"activeTabId"`
	SourceRevisionID       string                         `json:"sourceRevisionId,omitempty"`
	TargetRevisionID       string                         `json:"-"` // current revision targeted by SuggestBatchUpdateBody
	SelectionOnly          bool                           `json:"selectionOnly,omitempty"`
	SelectionRanges        []utf16idx.RangeInterval       `json:"selectionRanges,omitempty"`
	ScopeAnchors           []utf16idx.ScopeBoundaryAnchor `json:"scopeAnchors,omitempty"`
	HintOnly               bool                           `json:"hintOnly"`
	MatchedCount           int                            `json:"matchedCount"`
	MatchedTextCount       int                            `json:"matchedTextCount"`
	MatchedElementCount    int                            `json:"matchedElementCount"`
	MatchedItems           []utf16idx.MatchedSuggestion   `json:"matchedItems"`
	BaselineMatchedItems   []utf16idx.MatchedSuggestion   `json:"-"`
	SkippedStaleItems      []utf16idx.MatchedSuggestion   `json:"skippedStaleItems,omitempty"`
	PreReanchoredCount     int                            `json:"preReanchoredCount,omitempty"`
	PreSkippedElementCount int                            `json:"preSkippedElementCount,omitempty"`
	MatchedElementItems    []MatchedElementSuggestion     `json:"matchedElementItems"`
	HintCommentRequests    []map[string]any               `json:"hintCommentRequests,omitempty"`
	SuggestBatchUpdateBody map[string]any                 `json:"suggestBatchUpdateBody"`
}

// ApplyResult summarizes the outcome of Step 4 (applying suggestions and comments to Google Docs).
type ApplyResult struct {
	Success                     bool                         `json:"success"`
	HintOnly                    bool                         `json:"hintOnly"`
	AppliedSuggestionsCount     int                          `json:"appliedSuggestionsCount"`
	AppliedTextSuggestionsCount int                          `json:"appliedTextSuggestionsCount"`
	AppliedElementCommentsCount int                          `json:"appliedElementCommentsCount"`
	AppliedTextItems            []utf16idx.MatchedSuggestion `json:"appliedTextItems,omitempty"`
	SkippedStaleItems           []utf16idx.MatchedSuggestion `json:"skippedStaleItems,omitempty"`
	AppliedElementItems         []MatchedElementSuggestion   `json:"appliedElementItems,omitempty"`
	RevisionControl             *RevisionControlSummary      `json:"revisionControl,omitempty"`
	DecisionRecord              *DecisionRecordSummary       `json:"decisionRecord,omitempty"`
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
	sourceRevID := ""
	selectionOnly := false
	var selectionRanges []utf16idx.RangeInterval
	var scopeAnchors []utf16idx.ScopeBoundaryAnchor
	var settledBuf *utf16idx.SettledUTF16Buffer
	var editableBlocks []utf16idx.EditableBlock
	if docData != nil {
		activeTabID = docData.ActiveTabID
		sourceRevID = strings.TrimSpace(docData.RevisionID)
		selectionOnly = docData.SelectionOnly
		selectionRanges = docData.SelectionRanges
		scopeAnchors = docData.ScopeAnchors
		settledBuf = docData.SettledBuffer
		editableBlocks = docData.EditableBlocks
		if selectionOnly && len(selectionRanges) > 0 && len(scopeAnchors) == 0 {
			bufForScope := settledBuf
			if bufForScope == nil {
				bufForScope = utf16idx.BuildSettledBufferFromBlocks(editableBlocks)
			}
			scopeAnchors = utf16idx.BuildScopeBoundaryAnchors(selectionRanges, bufForScope)
		}
	}

	defaultCat := "Revision"
	if isJa {
		defaultCat = "推敲"
	}

	matchedItems := utf16idx.MatchSuggestionsToBlocksWithBuffer(editableBlocks, settledBuf, rawSuggestions, activeTabID, hintOnly, defaultCat)
	suggestRequests, hintCommentRequests, suggestBatchUpdateBody := buildPhase1BatchPayloads(matchedItems, activeTabID, sourceRevID, isJa, hintOnly)
	_ = suggestRequests

	type elementCoords struct {
		objectID       string
		tableSignature string
		startIndex     int
		endIndex       int
		paraStartIndex int
		paraEndIndex   int
	}
	elementMap := make(map[string]elementCoords)
	if docData != nil {
		for _, v := range docData.VisualElements {
			elementMap[v.TargetID] = elementCoords{
				objectID:       v.ObjectID,
				startIndex:     v.StartIndex,
				endIndex:       maxInt(v.StartIndex+1, v.EndIndex),
				paraStartIndex: maxInt(1, v.ParaStartIndex),
				paraEndIndex:   maxInt(v.StartIndex+1, v.ParaEndIndex),
			}
		}
		for _, t := range docData.Tables {
			elementMap[t.TargetID] = elementCoords{
				tableSignature: ComputeTableSignature(t),
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
			ObjectID:       coords.objectID,
			TableSignature: coords.tableSignature,
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
			AnchorStatus:   utf16idx.AnchorStatusExact,
		})
	}

	return &BuiltRequestData{
		ActiveTabID:            activeTabID,
		SourceRevisionID:       sourceRevID,
		TargetRevisionID:       sourceRevID,
		SelectionOnly:          selectionOnly,
		SelectionRanges:        selectionRanges,
		ScopeAnchors:           scopeAnchors,
		HintOnly:               hintOnly,
		MatchedCount:           len(matchedItems) + len(matchedElements),
		MatchedTextCount:       len(matchedItems),
		MatchedElementCount:    len(matchedElements),
		MatchedItems:           matchedItems,
		BaselineMatchedItems:   matchedItems,
		MatchedElementItems:    matchedElements,
		HintCommentRequests:    hintCommentRequests,
		SuggestBatchUpdateBody: suggestBatchUpdateBody,
	}
}

func buildPhase1BatchPayloads(
	matchedItems []utf16idx.MatchedSuggestion,
	activeTabID string,
	requiredRevID string,
	isJa bool,
	hintOnly bool,
) ([]map[string]any, []map[string]any, map[string]any) {
	var suggestRequests []map[string]any
	var hintCommentRequests []map[string]any

	if hintOnly {
		for _, item := range matchedItems {
			anchorRange := map[string]any{
				"startIndex": item.StartIndex,
				"endIndex":   maxInt(item.StartIndex+1, item.EndIndex),
			}
			effTab := firstNonEmpty(item.TabID, activeTabID)
			if effTab != "" {
				anchorRange["tabId"] = effTab
			}
			hintCommentRequests = append(hintCommentRequests, map[string]any{
				"insertComment": map[string]any{
					"content": FormatHintComment(item, isJa),
					"range":   anchorRange,
				},
			})
		}
		suggestBatchUpdateBody := map[string]any{
			"requests": hintCommentRequests,
		}
		if wc := BuildWriteControl("", requiredRevID); len(wc) > 0 {
			suggestBatchUpdateBody["writeControl"] = wc
		}
		return suggestRequests, hintCommentRequests, suggestBatchUpdateBody
	}

	for _, item := range matchedItems {
		effTab := firstNonEmpty(item.TabID, activeTabID)
		delRange := map[string]any{
			"startIndex": item.StartIndex,
			"endIndex":   item.EndIndex,
		}
		if effTab != "" {
			delRange["tabId"] = effTab
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
			if effTab != "" {
				insLoc["tabId"] = effTab
			}
			suggestRequests = append(suggestRequests, map[string]any{
				"insertText": map[string]any{
					"location": insLoc,
					"text":     item.SuggestedText,
				},
			})
		}
	}

	suggestBatchUpdateBody := map[string]any{
		"writeControl": BuildWriteControl("SUGGEST", requiredRevID),
		"requests":     suggestRequests,
	}
	return suggestRequests, hintCommentRequests, suggestBatchUpdateBody
}

// ReanchorBuiltRequestData projects a BuiltRequestData constructed on an earlier revision (e.g. Phase 1 prepareOnly rev_1)
// onto a newer ParsedDocument (rev_2), preserving SourceRevisionID while binding the batch to freshDoc.RevisionID.
func ReanchorBuiltRequestData(built *BuiltRequestData, freshDoc *ParsedDocument, lang string) *BuiltRequestData {
	if built == nil || freshDoc == nil {
		return built
	}
	isJa := lang == "ja"
	var reanchoredScopes []utf16idx.RangeInterval
	if built.SelectionOnly {
		if len(built.ScopeAnchors) > 0 {
			reanchoredScopes = utf16idx.ReanchorScopeRanges(built.ScopeAnchors, freshDoc.SettledBuffer)
		} else {
			reanchoredScopes = freshDoc.SelectionRanges
		}
	}

	baseItems := built.BaselineMatchedItems
	if len(baseItems) == 0 {
		baseItems = built.MatchedItems
	}

	outcome := utf16idx.ReanchorSuggestions(
		baseItems,
		freshDoc.EditableBlocks,
		freshDoc.SettledBuffer,
		built.SelectionOnly,
		reanchoredScopes,
	)

	reanchoredEls, skippedEls := reanchorElementSuggestions(built.MatchedElementItems, freshDoc)

	targetRev := strings.TrimSpace(freshDoc.RevisionID)
	_, hintReqs, batchBody := buildPhase1BatchPayloads(outcome.Surviving, built.ActiveTabID, targetRev, isJa, built.HintOnly)

	updated := *built
	updated.TargetRevisionID = targetRev
	updated.BaselineMatchedItems = baseItems
	updated.MatchedItems = outcome.Surviving
	updated.SkippedStaleItems = outcome.Skipped
	updated.PreReanchoredCount = outcome.ReanchoredCount
	updated.PreSkippedElementCount = built.PreSkippedElementCount + skippedEls
	updated.MatchedElementItems = reanchoredEls
	updated.MatchedTextCount = len(outcome.Surviving)
	updated.MatchedElementCount = len(reanchoredEls)
	updated.MatchedCount = len(outcome.Surviving) + len(reanchoredEls)
	updated.HintCommentRequests = hintReqs
	updated.SuggestBatchUpdateBody = batchBody
	return &updated
}

func reanchorElementSuggestions(items []MatchedElementSuggestion, freshDoc *ParsedDocument) ([]MatchedElementSuggestion, int) {
	if len(items) == 0 || freshDoc == nil {
		return items, 0
	}
	visualByObj := make(map[string]VisualElement, len(freshDoc.VisualElements))
	visualByTarget := make(map[string]VisualElement, len(freshDoc.VisualElements))
	for _, v := range freshDoc.VisualElements {
		if v.ObjectID != "" {
			visualByObj[v.ObjectID] = v
		}
		visualByTarget[v.TargetID] = v
	}

	tableByTarget := make(map[string]TableElement, len(freshDoc.Tables))
	for _, t := range freshDoc.Tables {
		tableByTarget[t.TargetID] = t
	}

	var surviving []MatchedElementSuggestion
	skippedCount := 0

	for _, el := range items {
		if el.ElementKind == "VISUAL" {
			var matchedV *VisualElement
			if el.ObjectID != "" {
				if v, ok := visualByObj[el.ObjectID]; ok {
					vCopy := v
					matchedV = &vCopy
				} else {
					// Collaborator deleted this specific inline/positioned object -> skip safely
					skippedCount++
					continue
				}
			} else if v, ok := visualByTarget[el.TargetID]; ok {
				vCopy := v
				matchedV = &vCopy
			} else {
				skippedCount++
				continue
			}

			upd := el
			oldStart := el.StartIndex
			upd.TargetID = matchedV.TargetID
			upd.StartIndex = matchedV.StartIndex
			upd.EndIndex = maxInt(matchedV.StartIndex+1, matchedV.EndIndex)
			upd.ParaStartIndex = maxInt(1, matchedV.ParaStartIndex)
			upd.ParaEndIndex = maxInt(matchedV.StartIndex+1, matchedV.ParaEndIndex)
			if upd.StartIndex != oldStart {
				upd.AnchorStatus = utf16idx.AnchorStatusReanchored
			} else {
				upd.AnchorStatus = utf16idx.AnchorStatusExact
			}
			surviving = append(surviving, upd)
			continue
		}

		// TABLE matching: match by TableSignature (closest StartIndex on tie), falling back to TargetID
		var matchedT *TableElement
		if el.TableSignature != "" {
			bestDist := int(^uint(0) >> 1)
			for i := range freshDoc.Tables {
				t := &freshDoc.Tables[i]
				if ComputeTableSignature(*t) == el.TableSignature {
					d := absInt(t.StartIndex - el.StartIndex)
					if d < bestDist {
						bestDist = d
						matchedT = t
					}
				}
			}
		}
		if matchedT == nil {
			if t, ok := tableByTarget[el.TargetID]; ok && el.TableSignature == "" {
				tCopy := t
				matchedT = &tCopy
			}
		}
		if matchedT == nil {
			skippedCount++
			continue
		}
		upd := el
		oldStart := el.StartIndex
		upd.TargetID = matchedT.TargetID
		upd.StartIndex = matchedT.StartIndex
		upd.EndIndex = maxInt(matchedT.StartIndex+1, matchedT.EndIndex)
		upd.ParaStartIndex = matchedT.StartIndex
		upd.ParaEndIndex = maxInt(matchedT.StartIndex+1, matchedT.EndIndex)
		if upd.StartIndex != oldStart {
			upd.AnchorStatus = utf16idx.AnchorStatusReanchored
		} else {
			upd.AnchorStatus = utf16idx.AnchorStatusExact
		}
		surviving = append(surviving, upd)
	}
	return surviving, skippedCount
}

// ApplySuggestions executes Step 4 against the Google Docs API with Zero-Overhead Optimistic Revision Control:
// Phase 1: Binds batchUpdate to requiredRevisionId (both WriteMode.SUGGEST and Hint-Only insertComment); on concurrent
// edit conflict (HTTP 400 FAILED_PRECONDITION), re-fetches the latest revision, runs Two-Pass UTF-16 Re-Anchoring,
// drops rewritten spans as stale_superseded, and retries bottom-to-top up to MaxRevisionRetries times.
// Phase 2: Re-fetches fresh coordinates unconditionally when element items exist, matching figures by immutable ObjectID
// and tables by TableSignature with requiredRevisionId chaining.
func (c *Client) ApplySuggestions(ctx context.Context, docID string, built *BuiltRequestData, lang string) (*ApplyResult, error) {
	if built == nil || (len(built.MatchedItems) == 0 && len(built.MatchedElementItems) == 0 && len(built.SkippedStaleItems) == 0) {
		return &ApplyResult{Success: true}, nil
	}
	isJa := lang == "ja"
	activeTabID := built.ActiveTabID
	baselineTextItems := built.BaselineMatchedItems
	if len(baselineTextItems) == 0 {
		baselineTextItems = built.MatchedItems
	}
	activeTextItems := built.MatchedItems
	skippedTextItems := append([]utf16idx.MatchedSuggestion{}, built.SkippedStaleItems...)
	elementItems := built.MatchedElementItems

	sourceRevID := strings.TrimSpace(built.SourceRevisionID)
	currentRevID := strings.TrimSpace(built.TargetRevisionID)
	if currentRevID == "" {
		currentRevID = sourceRevID
	}
	appliedRevID := currentRevID
	revisionRetries := 0
	reanchoredCount := built.PreReanchoredCount
	exactCount := len(activeTextItems)
	if reanchoredCount > 0 && exactCount >= reanchoredCount {
		exactCount -= reanchoredCount
	}

	var latestFreshParsed *ParsedDocument
	appliedTextCount := 0

	// Phase 1: Optimistic Revision-Controlled Text Suggestions or Hint-Only Comments
	if len(baselineTextItems) > 0 {
		for attempt := 0; attempt <= MaxRevisionRetries; attempt++ {
			if len(activeTextItems) == 0 {
				// All candidate text items became stale_superseded / stale_ambiguous after re-anchoring;
				// skip sending an empty requests: [] batchUpdate (which Google Docs rejects with HTTP 400).
				break
			}
			if attempt > 0 {
				if err := sleepRevisionBackoff(ctx, attempt); err != nil {
					return nil, err
				}
			}

			_, hintReqs, batchBody := buildPhase1BatchPayloads(activeTextItems, activeTabID, currentRevID, isJa, built.HintOnly)
			if attempt == 0 && !built.HintOnly && len(built.SuggestBatchUpdateBody) > 0 {
				// Preserve caller-supplied SuggestBatchUpdateBody on first attempt (shallow-cloned) while ensuring requiredRevisionId is bound
				clonedBody := make(map[string]any, len(built.SuggestBatchUpdateBody)+1)
				for k, v := range built.SuggestBatchUpdateBody {
					clonedBody[k] = v
				}
				if currentRevID != "" {
					if wc := BuildWriteControl("SUGGEST", currentRevID); len(wc) > 0 {
						clonedBody["writeControl"] = wc
					}
				}
				batchBody = clonedBody
			}

			suggestResJSON, err := c.ExecuteBatchUpdate(ctx, docID, batchBody)
			if err != nil {
				if IsRevisionMismatchError(err) && attempt < MaxRevisionRetries {
					revisionRetries++
					freshDocJSON, fErr := c.FetchRawDocumentJSON(ctx, docID)
					if fErr != nil {
						return nil, fmt.Errorf("failed to re-fetch Google Document during concurrent-edit recovery: %w", fErr)
					}
					freshParsed := ParseDocumentStructure(freshDocJSON, ParseOptions{
						PreferredTabID:        activeTabID,
						SelectionOnly:         false,
						SkipDecisionReconcile: true,
					})
					if freshParsed.RevisionID == "" || freshParsed.RevisionID == currentRevID {
						return nil, err
					}
					latestFreshParsed = freshParsed
					currentRevID = freshParsed.RevisionID
					appliedRevID = currentRevID

					var reanchoredScopes []utf16idx.RangeInterval
					if built.SelectionOnly {
						if len(built.ScopeAnchors) > 0 {
							reanchoredScopes = utf16idx.ReanchorScopeRanges(built.ScopeAnchors, freshParsed.SettledBuffer)
						} else {
							reanchoredScopes = built.SelectionRanges
						}
					}

					outcome := utf16idx.ReanchorSuggestions(
						baselineTextItems,
						freshParsed.EditableBlocks,
						freshParsed.SettledBuffer,
						built.SelectionOnly,
						reanchoredScopes,
					)
					activeTextItems = outcome.Surviving
					skippedTextItems = outcome.Skipped
					exactCount = outcome.ExactCount
					reanchoredCount = outcome.ReanchoredCount
					continue
				}

				// Non-revision error in Hint-Only mode: fall back to safe per-item comment insertion (never on exhausted revision conflict)
				if built.HintOnly && !IsRevisionMismatchError(err) {
					appliedTextCount = c.ExecuteSuggestionActionRequestsSafely(ctx, docID, hintReqs)
					break
				}
				return nil, err
			}

			if nextRev := ExtractWriteControlRevisionID(suggestResJSON); nextRev != "" {
				appliedRevID = nextRev
			}

			if built.HintOnly {
				appliedTextCount = len(activeTextItems)
				break
			}

			sugResponses := getSlice(suggestResJSON, "suggestionResponses")
			reqCursor := 0
			var replyRequests []map[string]any
			var issuedAISuggestions []IssuedAISuggestionInput

			for idx, item := range activeTextItems {
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
					// addCommentReply targets immutable suggestionId (coordinate-free), so it intentionally omits requiredRevisionId
					replyRequests = append(replyRequests, map[string]any{
						"addCommentReply": map[string]any{
							"suggestionId": foundSuggestionID,
							"post": map[string]any{
								"content": commentText,
							},
						},
					})
				} else {
					// Compensate for inline suggestedInsertionIds inserted by lower-StartIndex items (j > idx) in this same batch
					shiftFromLower := 0
					for j := idx + 1; j < len(activeTextItems); j++ {
						shiftFromLower += utf16idx.UTF16Len(activeTextItems[j].SuggestedText)
					}
					shiftedStart := item.StartIndex + shiftFromLower
					anchorRange := map[string]any{
						"startIndex": shiftedStart,
						"endIndex":   shiftedStart + maxInt(1, utf16idx.UTF16Len(item.SuggestedText)),
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
			appliedTextCount = len(activeTextItems)

			revMeta := &RevisionControlSummary{
				SourceRevisionID:  sourceRevID,
				AppliedRevisionID: appliedRevID,
				RevisionRetries:   revisionRetries,
				ExactCount:        exactCount,
				ReanchoredCount:   reanchoredCount,
				StaleSkippedCount: len(skippedTextItems),
			}
			_ = RegisterIssuedAISuggestionsWithRevision(docID, issuedAISuggestions, activeTabID, revMeta)
			break
		}

		// Record stale-only run telemetry when all text items were skipped as stale_superseded
		if !built.HintOnly && len(activeTextItems) == 0 && len(skippedTextItems) > 0 {
			revMeta := &RevisionControlSummary{
				SourceRevisionID:  sourceRevID,
				AppliedRevisionID: appliedRevID,
				RevisionRetries:   revisionRetries,
				ExactCount:        0,
				ReanchoredCount:   reanchoredCount,
				StaleSkippedCount: len(skippedTextItems),
			}
			_ = RegisterIssuedAISuggestionsWithRevision(docID, nil, activeTabID, revMeta)
		}
	}

	updatedDecisionRecord := LoadDecisionRecordSummary(docID)

	// Phase 2: Element review comments (VISUAL_1, TABLE_1) matched by immutable ObjectID / TableSignature
	appliedElementCount := 0
	var appliedElements []MatchedElementSuggestion
	staleSkippedElementCount := 0

	if len(elementItems) > 0 {
		var freshParsed *ParsedDocument
		if appliedTextCount == 0 && latestFreshParsed != nil {
			freshParsed = latestFreshParsed
		} else if freshDocJSON, err := c.FetchRawDocumentJSON(ctx, docID); err == nil {
			freshParsed = ParseDocumentStructure(freshDocJSON, ParseOptions{
				PreferredTabID:        activeTabID,
				SelectionOnly:         false,
				SkipDecisionReconcile: true,
			})
		}

		resolvedElements := elementItems
		var freshBlocks []utf16idx.EditableBlock
		phase2RevID := ""
		if freshParsed != nil {
			resolvedElements, staleSkippedElementCount = reanchorElementSuggestions(elementItems, freshParsed)
			freshBlocks = freshParsed.EditableBlocks
			phase2RevID = freshParsed.RevisionID
			if phase2RevID != "" {
				appliedRevID = phase2RevID
			}
		}

		for _, elItem := range resolvedElements {
			elCommentText := FormatElementComment(elItem, isJa)
			effTabID := firstNonEmpty(elItem.TabID, activeTabID)

			buildCandidatesFor := func(target MatchedElementSuggestion, blocks []utf16idx.EditableBlock) []map[string]any {
				var cands []map[string]any
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
					cands = append(cands, r)
				}
				addCandidate(target.StartIndex, target.EndIndex)
				if target.ParaStartIndex > 0 && target.ParaEndIndex > 0 {
					addCandidate(target.ParaStartIndex, target.ParaEndIndex)
				}
				if len(blocks) > 0 {
					targetPos := maxInt(1, target.StartIndex)
					sortedBlocks := make([]utf16idx.EditableBlock, len(blocks))
					copy(sortedBlocks, blocks)
					sort.Slice(sortedBlocks, func(i, j int) bool {
						distA := minIntVal(absInt(sortedBlocks[i].StartIndex-targetPos), absInt(sortedBlocks[i].EndIndex-targetPos))
						distB := minIntVal(absInt(sortedBlocks[j].StartIndex-targetPos), absInt(sortedBlocks[j].EndIndex-targetPos))
						return distA < distB
					})
					addCandidate(sortedBlocks[0].StartIndex, sortedBlocks[0].EndIndex)
				}
				return cands
			}

			inserted := false
			curEl := elItem
			for p2Attempt := 0; p2Attempt <= 1 && !inserted; p2Attempt++ {
				cands := buildCandidatesFor(curEl, freshBlocks)
				revMismatchSeen := false
				for _, cand := range cands {
					nextRev, ok, revMismatch := c.TryInsertCommentRequestWithRevision(ctx, docID, elCommentText, cand, phase2RevID)
					if ok {
						if nextRev != "" {
							phase2RevID = nextRev
							appliedRevID = nextRev
						} else {
							phase2RevID = ""
						}
						appliedElementCount++
						appliedElements = append(appliedElements, curEl)
						inserted = true
						break
					}
					if revMismatch {
						revMismatchSeen = true
						break
					}
				}
				if !inserted && revMismatchSeen && p2Attempt == 0 {
					revisionRetries++
					if reJSON, rErr := c.FetchRawDocumentJSON(ctx, docID); rErr == nil {
						reParsed := ParseDocumentStructure(reJSON, ParseOptions{
							PreferredTabID:        activeTabID,
							SelectionOnly:         false,
							SkipDecisionReconcile: true,
						})
						phase2RevID = reParsed.RevisionID
						freshBlocks = reParsed.EditableBlocks
						reEls, _ := reanchorElementSuggestions([]MatchedElementSuggestion{elItem}, reParsed)
						if len(reEls) == 0 {
							staleSkippedElementCount++
							break
						}
						curEl = reEls[0]
						continue
					}
				}
				break
			}
		}
	}

	revSummary := &RevisionControlSummary{
		SourceRevisionID:  sourceRevID,
		AppliedRevisionID: appliedRevID,
		RevisionRetries:   revisionRetries,
		ExactCount:        exactCount,
		ReanchoredCount:   reanchoredCount,
		StaleSkippedCount: len(skippedTextItems) + built.PreSkippedElementCount + staleSkippedElementCount,
	}

	return &ApplyResult{
		Success:                     true,
		HintOnly:                    built.HintOnly,
		AppliedSuggestionsCount:     appliedTextCount + appliedElementCount,
		AppliedTextSuggestionsCount: appliedTextCount,
		AppliedElementCommentsCount: appliedElementCount,
		AppliedTextItems:            activeTextItems,
		SkippedStaleItems:           skippedTextItems,
		AppliedElementItems:         appliedElements,
		RevisionControl:             revSummary,
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
