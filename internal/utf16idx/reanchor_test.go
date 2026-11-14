package utf16idx

import (
	"strings"
	"testing"
)

func buildBlocksFromParagraphs(paras []string) ([]EditableBlock, *SettledUTF16Buffer) {
	var blocks []EditableBlock
	cur := 1
	for i, p := range paras {
		uLen := UTF16Len(p)
		blocks = append(blocks, EditableBlock{
			Text:           p,
			StartIndex:     cur,
			EndIndex:       cur + uLen,
			Source:         "body",
			LineNumber:     i + 1,
			TargetEditable: true,
		})
		cur += uLen + 1 // +1 for Google Docs paragraph trailing '\n'
	}
	return blocks, BuildSettledBufferFromBlocks(blocks)
}

func TestCrossLineContextExtractionAndSurrogatePairSafety(t *testing.T) {
	// Place supplementary-plane emojis (2 UTF-16 code units each) right at the 32-unit boundary
	// so naive uint16 slicing would split a high/low surrogate pair and emit U+FFFD.
	line1 := "Header 🧪🚀01234567890123456789🧪" // length: 7 + 2 + 2 + 20 + 2 = 33 UTF-16 units
	line2 := "Target whole line statement"
	line3 := "🚀🧪012345678901234567890123456🧪Footer"

	blocks, buf := buildBlocksFromParagraphs([]string{line1, line2, line3})
	raw := []RawSuggestion{
		{
			OriginalText:  "Target whole line statement",
			SuggestedText: "Refined whole line statement",
			Category:      "Clarity",
		},
	}

	matched := MatchSuggestionsToBlocksWithBuffer(blocks, buf, raw, "t.0", false, "Revision")
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched item, got %d", len(matched))
	}
	m := matched[0]
	if m.CtxBefore == "" || m.CtxAfter == "" {
		t.Fatalf("expected non-empty cross-line CtxBefore and CtxAfter for whole-line match, got before=%q after=%q", m.CtxBefore, m.CtxAfter)
	}
	if !strings.Contains(m.CtxBefore, "\n") || !strings.Contains(m.CtxAfter, "\n") {
		t.Fatalf("expected cross-line newline in context anchors, got before=%q after=%q", m.CtxBefore, m.CtxAfter)
	}
	if strings.ContainsRune(m.CtxBefore, '\uFFFD') || strings.ContainsRune(m.CtxAfter, '\uFFFD') {
		t.Fatalf("surrogate pair was split into U+FFFD replacement char: before=%q after=%q", m.CtxBefore, m.CtxAfter)
	}
	if UTF16Len(m.CtxBefore) > ContextAnchorUTF16Units || UTF16Len(m.CtxAfter) > ContextAnchorUTF16Units {
		t.Fatalf("context exceeded %d UTF-16 units: before=%d after=%d", ContextAnchorUTF16Units, UTF16Len(m.CtxBefore), UTF16Len(m.CtxAfter))
	}
}

func TestReanchorDuplicateCodeLinesRejectsFalsePositiveStepA(t *testing.T) {
	// Duplicate code line in two different functions where collaborator deletes lines above funcB
	// such that funcB's `if err != nil { return err }` lands on the EXACT old [StartIndex, EndIndex)
	// previously occupied by funcA's occurrence, or vice versa.
	origParas := []string{
		"func alpha() error {",
		"    err := stepOne()",
		"    if err != nil { return err }",
		"    return nil",
		"}",
		"func beta() error {",
		"    err := stepTwo()",
		"    if err != nil { return err }",
		"    return nil",
		"}",
	}
	origBlocks, origBuf := buildBlocksFromParagraphs(origParas)

	// Target specifically the SECOND occurrence (inside beta) by restricting TargetEditable on origBlocks
	for i := range origBlocks {
		if i < 5 {
			origBlocks[i].TargetEditable = false
		}
	}
	matched := MatchSuggestionsToBlocksWithBuffer(origBlocks, origBuf, []RawSuggestion{
		{
			OriginalText:  "if err != nil { return err }",
			SuggestedText: "if err != nil { return fmt.Errorf(\"beta: %w\", err) }",
			Category:      "Error Handling",
		},
	}, "t.0", false, "Revision")
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched item in beta(), got %d", len(matched))
	}
	origBetaStart := matched[0].StartIndex

	// Collaborator inserts padding at top so alpha()'s `if err != nil { return err }` shifts DOWN
	// to land at the EXACT old [StartIndex, EndIndex) that beta() used to have!
	alphaOldStart := origBlocks[2].StartIndex + 4 // +4 for 4 leading indentation spaces
	deltaNeeded := origBetaStart - alphaOldStart
	padding := strings.Repeat("x", deltaNeeded-1) // +1 for paragraph '\n' = exact deltaNeeded UTF-16 shift!

	freshParas := append([]string{padding}, origParas...)
	freshBlocks, freshBuf := buildBlocksFromParagraphs(freshParas)

	// Verify our adversarial setup: freshBlocks[3] + 4 (alpha's if-err) is now at origBetaStart!
	if freshBlocks[3].StartIndex+4 != origBetaStart {
		t.Fatalf("test setup mismatch: expected alpha if-err at %d, got %d", origBetaStart, freshBlocks[3].StartIndex+4)
	}

	outcome := ReanchorSuggestions(matched, freshBlocks, freshBuf, false, nil)
	if len(outcome.Surviving) != 1 || outcome.SkippedCount != 0 {
		t.Fatalf("expected 1 surviving re-anchored item, got %+v", outcome)
	}
	got := outcome.Surviving[0]
	// Step A MUST reject freshBlocks[3]+4 (alpha) because CtxBefore/CtxAfter belong to alpha, not beta,
	// and Step B MUST anchor onto freshBlocks[8]+4 (beta's shifted if-err)!
	expectedNewBetaStart := freshBlocks[8].StartIndex + 4
	if got.StartIndex != expectedNewBetaStart {
		t.Fatalf("false-positive duplicate anchor! got StartIndex=%d (alpha=%d), want beta=%d", got.StartIndex, freshBlocks[3].StartIndex+4, expectedNewBetaStart)
	}
	if got.AnchorStatus != AnchorStatusReanchored || got.ShiftDelta != deltaNeeded {
		t.Fatalf("unexpected anchor telemetry: status=%q shiftDelta=%d (want %d)", got.AnchorStatus, got.ShiftDelta, deltaNeeded)
	}
}

func TestReanchorStaleSupersededAndAmbiguousAndParagraphSwapOrder(t *testing.T) {
	origParas := []string{
		"Paragraph Alpha discusses distributed consensus latency in detail.",
		"Paragraph Beta contains a passive voice phrase that needs active phrasing.",
		"Paragraph Gamma concludes the architectural benchmark section cleanly.",
	}
	origBlocks, origBuf := buildBlocksFromParagraphs(origParas)

	matched := MatchSuggestionsToBlocksWithBuffer(origBlocks, origBuf, []RawSuggestion{
		{
			OriginalText:  "distributed consensus latency",
			SuggestedText: "consensus tail latency",
		},
		{
			OriginalText:  "a passive voice phrase",
			SuggestedText: "an active verb",
		},
		{
			OriginalText:  "architectural benchmark section",
			SuggestedText: "benchmark evaluation",
		},
	}, "t.0", false, "Revision")
	if len(matched) != 3 {
		t.Fatalf("expected 3 initial matches, got %d", len(matched))
	}

	// Collaborator:
	// 1. Completely rewrites Paragraph Beta (removing "a passive voice phrase")
	// 2. Cuts Paragraph Gamma and pastes it ABOVE Paragraph Alpha!
	freshParas := []string{
		"🚀 New emoji banner added by collaborator!",
		"Paragraph Gamma concludes the architectural benchmark section cleanly.",
		"Collaborator completely rewrote Beta with concrete numbers and active verbs.",
		"Paragraph Alpha discusses distributed consensus latency in detail.",
	}
	freshBlocks, freshBuf := buildBlocksFromParagraphs(freshParas)

	outcome := ReanchorSuggestions(matched, freshBlocks, freshBuf, false, nil)
	if len(outcome.Surviving) != 2 || len(outcome.Skipped) != 1 {
		t.Fatalf("expected 2 surviving and 1 skipped, got surviving=%d skipped=%d", len(outcome.Surviving), len(outcome.Skipped))
	}
	if outcome.Skipped[0].OriginalText != "a passive voice phrase" || outcome.Skipped[0].AnchorStatus != AnchorStatusStaleSuperseded {
		t.Fatalf("expected rewritten Beta item to be stale_superseded, got %+v", outcome.Skipped[0])
	}
	// Surviving items MUST be strictly sorted in descending StartIndex order even after Gamma & Alpha swapped order!
	if outcome.Surviving[0].StartIndex <= outcome.Surviving[1].StartIndex {
		t.Fatalf("post-reanchor bottom-up invariant violated: %d <= %d", outcome.Surviving[0].StartIndex, outcome.Surviving[1].StartIndex)
	}
	if outcome.Surviving[0].OriginalText != "distributed consensus latency" || outcome.Surviving[1].OriginalText != "architectural benchmark section" {
		t.Fatalf("unexpected surviving order after paragraph swap: %+v", outcome.Surviving)
	}

	// Ambiguous duplicate tie check: identical context around two newly duplicated blocks
	ambigOrigBlocks, ambigOrigBuf := buildBlocksFromParagraphs([]string{
		"Shared prefix context around target token and shared suffix context.",
	})
	ambigMatched := MatchSuggestionsToBlocksWithBuffer(ambigOrigBlocks, ambigOrigBuf, []RawSuggestion{
		{OriginalText: "target token", SuggestedText: "refined token"},
	}, "t.0", false, "Revision")
	// Collaborator shifts text by >200 chars and duplicates the exact paragraph twice adjacent to each other
	pad := strings.Repeat("Intro padding text. ", 15)
	dupLine := "Shared prefix context around target token and shared suffix context."
	ambigFreshBlocks, ambigFreshBuf := buildBlocksFromParagraphs([]string{pad, dupLine, dupLine})
	ambigOutcome := ReanchorSuggestions(ambigMatched, ambigFreshBlocks, ambigFreshBuf, false, nil)
	if len(ambigOutcome.Surviving) != 0 || len(ambigOutcome.Skipped) != 1 || ambigOutcome.Skipped[0].AnchorStatus != AnchorStatusStaleAmbiguous {
		t.Fatalf("expected ambiguous duplicate tie to fail closed as stale_ambiguous, got %+v", ambigOutcome)
	}
}

func TestReanchorScopeRangesAndStartEndOfDocSimilarity(t *testing.T) {
	// 1. Start-of-doc (@1, expBefore == "") and end-of-doc (expAfter == "") weighted similarity
	simStart := WeightedContextSimilarity("", " advantage of optimistic locking in Docs.", "", " advantage of optimistic locking in Docs.")
	if simStart < 0.99 {
		t.Fatalf("expected ~1.0 similarity at start of document (empty expBefore), got %f", simStart)
	}
	simEnd := WeightedContextSimilarity("concludes our evaluation of ", "", "concludes our evaluation of ", "")
	if simEnd < 0.99 {
		t.Fatalf("expected ~1.0 similarity at end of document (empty expAfter), got %f", simEnd)
	}

	// 2. Scoped --range re-anchoring when collaborator inserts a paragraph at the top
	// while an identical phrase outside the scope must NOT be touched (stale_out_of_scope).
	origParas := []string{
		"Unscoped prologue with repeated phrase very unique marker.",
		"Scoped section start: here is very unique marker inside the selected paragraph.",
		"Unscoped epilogue paragraph.",
	}
	origBlocks, origBuf := buildBlocksFromParagraphs(origParas)
	scopeInterval := []RangeInterval{{StartIndex: origBlocks[1].StartIndex, EndIndex: origBlocks[1].EndIndex}}
	anchors := BuildScopeBoundaryAnchors(scopeInterval, origBuf)

	// Only block 1 is in scope
	origBlocks[0].TargetEditable = false
	origBlocks[2].TargetEditable = false
	matched := MatchSuggestionsToBlocksWithBuffer(origBlocks, origBuf, []RawSuggestion{
		{OriginalText: "very unique marker", SuggestedText: "precise marker"},
	}, "t.0", false, "Revision")
	if len(matched) != 1 {
		t.Fatalf("expected 1 scoped match, got %d", len(matched))
	}

	// Case A: Collaborator inserts a new banner above everything -> scope shifts down cleanly
	freshParasShifted := []string{
		"NEW COLLABORATOR BANNER 🧪🚀!",
		"Unscoped prologue with repeated phrase very unique marker.",
		"Scoped section start: here is very unique marker inside the selected paragraph.",
		"Unscoped epilogue paragraph.",
	}
	freshBlocksA, freshBufA := buildBlocksFromParagraphs(freshParasShifted)
	reanchoredScopesA := ReanchorScopeRanges(anchors, freshBufA)
	if len(reanchoredScopesA) != 1 || reanchoredScopesA[0].StartIndex != freshBlocksA[2].StartIndex {
		t.Fatalf("expected scope start to shift to %d, got %+v", freshBlocksA[2].StartIndex, reanchoredScopesA)
	}
	outA := ReanchorSuggestions(matched, freshBlocksA, freshBufA, true, reanchoredScopesA)
	if len(outA.Surviving) != 1 || outA.Surviving[0].StartIndex < freshBlocksA[2].StartIndex {
		t.Fatalf("expected surviving match inside shifted scope, got %+v", outA)
	}

	// Case B: Collaborator deletes "very unique marker" inside the scoped paragraph, leaving only the unscoped copy in prologue
	freshParasOutScope := []string{
		"NEW COLLABORATOR BANNER 🧪🚀!",
		"Unscoped prologue with repeated phrase very unique marker.",
		"Scoped section start: collaborator already rewrote the marker inside the selected paragraph.",
		"Unscoped epilogue paragraph.",
	}
	freshBlocksB, freshBufB := buildBlocksFromParagraphs(freshParasOutScope)
	reanchoredScopesB := ReanchorScopeRanges(anchors, freshBufB)
	outB := ReanchorSuggestions(matched, freshBlocksB, freshBufB, true, reanchoredScopesB)
	if len(outB.Surviving) != 0 || len(outB.Skipped) != 1 {
		t.Fatalf("expected out-of-scope copy NOT to be touched, got %+v", outB)
	}

	// Case C: Collaborator inserts an emoji (surrogate pair 🔥 = 2 UTF-16 units) right at the scope boundary;
	// ReanchorScopeRanges MUST NOT land on the mid-surrogate low-surrogate index!
	emojiAtBoundaryParas := []string{
		"Unscoped prologue with repeated phrase very unique marker.",
		"🔥Scoped section start: here is very unique marker inside the selected paragraph.",
		"Unscoped epilogue paragraph.",
	}
	freshBlocksC, freshBufC := buildBlocksFromParagraphs(emojiAtBoundaryParas)
	reanchoredScopesC := ReanchorScopeRanges(anchors, freshBufC)
	if len(reanchoredScopesC) != 1 {
		t.Fatalf("expected 1 reanchored scope across boundary emoji insertion, got %+v", reanchoredScopesC)
	}
	// Block 1 starts with '🔥' (2 UTF-16 units: startIdx and startIdx+1). Valid rune boundaries are startIdx or startIdx+2, NEVER startIdx+1!
	midSurrogateIdx := freshBlocksC[1].StartIndex + 1
	if reanchoredScopesC[0].StartIndex == midSurrogateIdx || reanchoredScopesC[0].EndIndex == midSurrogateIdx {
		t.Fatalf("mid-surrogate UTF-16 index selected! got %+v (midSurrogate=%d)", reanchoredScopesC[0], midSurrogateIdx)
	}
}
