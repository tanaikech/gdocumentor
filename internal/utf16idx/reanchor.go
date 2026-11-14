package utf16idx

import (
	"sort"
	"strings"
	"unicode/utf16"
)

const (
	// ContextAnchorUTF16Units is the cross-line UTF-16 window captured before and after each matched span.
	ContextAnchorUTF16Units = 32

	// ReanchorMinSimilarity is the minimum weighted context similarity required to accept a shifted candidate.
	ReanchorMinSimilarity = 0.65

	// ReanchorAmbiguousMargin is the score margin within which two competing duplicate candidates are treated as tied.
	ReanchorAmbiguousMargin = 0.05

	AnchorStatusExact          = "exact"
	AnchorStatusReanchored     = "reanchored"
	AnchorStatusStaleSuperseded = "stale_superseded"
	AnchorStatusStaleAmbiguous  = "stale_ambiguous"
	AnchorStatusStaleOutOfScope = "stale_out_of_scope"
)

// SettledUTF16Buffer holds a monotonically indexed stream of settled UTF-16 code units across an entire tab
// (excluding pending suggestedInsertionIds and suggestedDeletionIds), mapping each unit to its exact 1-based
// Google Docs UTF-16 coordinate.
type SettledUTF16Buffer struct {
	Units      []uint16
	DocIndices []int
}

// ScopeBoundaryAnchor captures surrounding 32-UTF-16-unit context at the start and end boundaries of a scoped range
// (--range / -t) so partial selection scopes track concurrent insertions or deletions above the scoped region.
type ScopeBoundaryAnchor struct {
	StartIndex     int    `json:"startIndex"`
	EndIndex       int    `json:"endIndex"`
	StartCtxBefore string `json:"startCtxBefore,omitempty"`
	StartCtxAfter  string `json:"startCtxAfter,omitempty"`
	EndCtxBefore   string `json:"endCtxBefore,omitempty"`
	EndCtxAfter    string `json:"endCtxAfter,omitempty"`
}

// ReanchorOutcome summarizes the result of a Two-Pass UTF-16 re-anchoring pass.
type ReanchorOutcome struct {
	Surviving       []MatchedSuggestion
	Skipped         []MatchedSuggestion
	ExactCount      int
	ReanchoredCount int
	SkippedCount    int
}

// BuildSettledBufferFromBlocks synthesizes a SettledUTF16Buffer from EditableBlocks when raw Google Docs AST JSON
// is unavailable (e.g. --no-auth local mode or unit-test struct literals), inserting '\n' where adjacent blocks
// are separated by exactly 1 UTF-16 unit (paragraph / line break).
func BuildSettledBufferFromBlocks(blocks []EditableBlock) *SettledUTF16Buffer {
	buf := &SettledUTF16Buffer{}
	if len(blocks) == 0 {
		return buf
	}
	sorted := make([]EditableBlock, len(blocks))
	copy(sorted, blocks)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StartIndex < sorted[j].StartIndex
	})

	prevEnd := -1
	for _, b := range sorted {
		if b.Text == "" || b.StartIndex <= 0 {
			continue
		}
		if prevEnd > 0 && b.StartIndex == prevEnd+1 {
			buf.Units = append(buf.Units, uint16('\n'))
			buf.DocIndices = append(buf.DocIndices, prevEnd)
		}
		u16 := utf16.Encode([]rune(b.Text))
		for offset, unit := range u16 {
			docIdx := b.StartIndex + offset
			if len(buf.DocIndices) > 0 && docIdx <= buf.DocIndices[len(buf.DocIndices)-1] {
				continue
			}
			buf.Units = append(buf.Units, unit)
			buf.DocIndices = append(buf.DocIndices, docIdx)
		}
		prevEnd = b.StartIndex + len(u16)
	}
	return buf
}

// SliceContextAroundDocSpan locates [startDocIdx, endDocIdx) in O(log N) time, verifies discrete monotonic
// contiguity (guaranteeing no cross-cell table jumps or straddled pending suggestions), and extracts up to
// maxUnits of surrogate-pair-safe cross-line UTF-16 context before and after the span.
func (b *SettledUTF16Buffer) SliceContextAroundDocSpan(startDocIdx, endDocIdx, maxUnits int) (ctxBefore, ctxAfter, exactSpan string, contiguous bool) {
	if b == nil || len(b.Units) == 0 || len(b.Units) != len(b.DocIndices) || endDocIdx <= startDocIdx {
		return "", "", "", false
	}
	if maxUnits <= 0 {
		maxUnits = ContextAnchorUTF16Units
	}

	pos := sort.SearchInts(b.DocIndices, startDocIdx)
	if pos >= len(b.DocIndices) || b.DocIndices[pos] != startDocIdx {
		return "", "", "", false
	}
	spanLen := endDocIdx - startDocIdx
	if pos+spanLen > len(b.DocIndices) {
		return "", "", "", false
	}
	// Discrete monotonic contiguity invariant: since DocIndices is strictly increasing integers,
	// DocIndices[pos+spanLen-1] - DocIndices[pos] == spanLen - 1 iff every adjacent step is +1.
	if b.DocIndices[pos+spanLen-1] != endDocIdx-1 {
		return "", "", "", false
	}

	spanSlice := safeUTF16Slice(b.Units, pos, pos+spanLen)
	if len(spanSlice) != spanLen {
		return "", "", "", false
	}
	exactSpan = string(utf16.Decode(spanSlice))

	leftStart := pos - maxUnits
	if leftStart < 0 {
		leftStart = 0
	}
	leftSlice := safeUTF16Slice(b.Units, leftStart, pos)
	ctxBefore = string(utf16.Decode(leftSlice))

	rightEnd := pos + spanLen + maxUnits
	if rightEnd > len(b.Units) {
		rightEnd = len(b.Units)
	}
	rightSlice := safeUTF16Slice(b.Units, pos+spanLen, rightEnd)
	ctxAfter = string(utf16.Decode(rightSlice))

	return ctxBefore, ctxAfter, exactSpan, true
}

// SliceContextAtDocBoundary extracts up to maxUnits of surrogate-pair-safe UTF-16 context immediately
// before and starting at docIdx (used for re-anchoring --range / SelectionRanges boundaries).
func (b *SettledUTF16Buffer) SliceContextAtDocBoundary(docIdx, maxUnits int) (ctxBefore, ctxAfter string) {
	if b == nil || len(b.Units) == 0 || len(b.Units) != len(b.DocIndices) {
		return "", ""
	}
	if maxUnits <= 0 {
		maxUnits = ContextAnchorUTF16Units
	}
	pos := sort.SearchInts(b.DocIndices, docIdx)
	if pos > len(b.Units) {
		pos = len(b.Units)
	}
	leftStart := pos - maxUnits
	if leftStart < 0 {
		leftStart = 0
	}
	rightEnd := pos + maxUnits
	if rightEnd > len(b.Units) {
		rightEnd = len(b.Units)
	}
	ctxBefore = string(utf16.Decode(safeUTF16Slice(b.Units, leftStart, pos)))
	ctxAfter = string(utf16.Decode(safeUTF16Slice(b.Units, pos, rightEnd)))
	return ctxBefore, ctxAfter
}

// safeUTF16Slice trims orphan leading or trailing UTF-16 surrogates at slice boundaries [s, e)
// so utf16.Decode never emits U+FFFD replacement runes when cutting across supplementary-plane emojis/kanji.
func safeUTF16Slice(units []uint16, s, e int) []uint16 {
	if s < 0 {
		s = 0
	}
	if e > len(units) {
		e = len(units)
	}
	if s >= e {
		return nil
	}
	// Trailing/low surrogate at left boundary (0xDC00..0xDFFF)
	if units[s] >= 0xDC00 && units[s] <= 0xDFFF {
		s++
	}
	if s >= e {
		return nil
	}
	// Leading/high surrogate at right boundary (0xD800..0xDBFF)
	if units[e-1] >= 0xD800 && units[e-1] <= 0xDBFF {
		e--
	}
	if s >= e {
		return nil
	}
	return units[s:e]
}

// BuildScopeBoundaryAnchors snapshots 32-unit boundary contexts for each interval in selectionRanges.
func BuildScopeBoundaryAnchors(selectionRanges []RangeInterval, buf *SettledUTF16Buffer) []ScopeBoundaryAnchor {
	if len(selectionRanges) == 0 || buf == nil || len(buf.Units) == 0 {
		return nil
	}
	anchors := make([]ScopeBoundaryAnchor, 0, len(selectionRanges))
	for _, r := range selectionRanges {
		if r.EndIndex <= r.StartIndex {
			continue
		}
		sb, sa := buf.SliceContextAtDocBoundary(r.StartIndex, ContextAnchorUTF16Units)
		eb, ea := buf.SliceContextAtDocBoundary(r.EndIndex, ContextAnchorUTF16Units)
		anchors = append(anchors, ScopeBoundaryAnchor{
			StartIndex:     r.StartIndex,
			EndIndex:       r.EndIndex,
			StartCtxBefore: sb,
			StartCtxAfter:  sa,
			EndCtxBefore:   eb,
			EndCtxAfter:    ea,
		})
	}
	return anchors
}

// ReanchorScopeRanges projects Stage 1 ScopeBoundaryAnchors onto freshBuf so --range / -t scopes
// move accurately when a collaborator inserts or deletes content above the scoped section.
func ReanchorScopeRanges(anchors []ScopeBoundaryAnchor, freshBuf *SettledUTF16Buffer) []RangeInterval {
	if len(anchors) == 0 {
		return nil
	}
	if freshBuf == nil || len(freshBuf.Units) == 0 {
		out := make([]RangeInterval, 0, len(anchors))
		for _, a := range anchors {
			out = append(out, RangeInterval{StartIndex: a.StartIndex, EndIndex: a.EndIndex})
		}
		return out
	}

	var reanchored []RangeInterval
	for _, a := range anchors {
		newStart, okStart := reanchorBoundaryPoint(a.StartIndex, a.StartCtxBefore, a.StartCtxAfter, true, freshBuf)
		newEnd, okEnd := reanchorBoundaryPoint(a.EndIndex, a.EndCtxBefore, a.EndCtxAfter, false, freshBuf)
		if !okStart && okEnd {
			// Estimate start from shifted end when start text was edited right at boundary
			delta := newEnd - a.EndIndex
			newStart = maxIntLocal(1, a.StartIndex+delta)
			okStart = true
		} else if okStart && !okEnd {
			newEnd = newStart + maxIntLocal(1, a.EndIndex-a.StartIndex)
			okEnd = true
		}
		if okStart && okEnd && newEnd > newStart {
			reanchored = append(reanchored, RangeInterval{StartIndex: newStart, EndIndex: newEnd})
		}
	}
	return reanchored
}

func isLowSurrogateAt(units []uint16, pos int) bool {
	return pos >= 0 && pos < len(units) && units[pos] >= 0xDC00 && units[pos] <= 0xDFFF
}

func reanchorBoundaryPoint(origDocIdx int, expBefore, expAfter string, isStartBoundary bool, freshBuf *SettledUTF16Buffer) (int, bool) {
	// O(log N) exact boundary check first (rejecting invalid mid-surrogate positions)
	origPos := sort.SearchInts(freshBuf.DocIndices, origDocIdx)
	if !isLowSurrogateAt(freshBuf.Units, origPos) {
		curB, curA := freshBuf.SliceContextAtDocBoundary(origDocIdx, ContextAnchorUTF16Units)
		if curB == expBefore && curA == expAfter {
			return origDocIdx, true
		}
	}

	bestPos := -1
	bestScore := -1.0
	bestDist := int(^uint(0) >> 1)

	for pos := 0; pos <= len(freshBuf.Units); pos++ {
		if isLowSurrogateAt(freshBuf.Units, pos) {
			// Never evaluate or return a mid-surrogate UTF-16 index (between 0xD800..0xDBFF and 0xDC00..0xDFFF),
			// where safeUTF16Slice would trim both halves of an inserted emoji and yield a false 1.0 match.
			continue
		}
		var docIdx int
		if pos < len(freshBuf.DocIndices) {
			docIdx = freshBuf.DocIndices[pos]
		} else if len(freshBuf.DocIndices) > 0 {
			docIdx = freshBuf.DocIndices[len(freshBuf.DocIndices)-1] + 1
		} else {
			docIdx = 1
		}
		leftStart := pos - ContextAnchorUTF16Units
		if leftStart < 0 {
			leftStart = 0
		}
		rightEnd := pos + ContextAnchorUTF16Units
		if rightEnd > len(freshBuf.Units) {
			rightEnd = len(freshBuf.Units)
		}
		candB := string(utf16.Decode(safeUTF16Slice(freshBuf.Units, leftStart, pos)))
		candA := string(utf16.Decode(safeUTF16Slice(freshBuf.Units, pos, rightEnd)))

		score := WeightedContextSimilarity(expBefore, expAfter, candB, candA)
		// If a collaborator deleted or rewrote the exterior paragraph immediately outside the selected --range,
		// anchor on the interior side of the selected scope (After for StartIndex, Before for EndIndex) when high-fidelity.
		if isStartBoundary && UTF16Len(expAfter) >= 12 {
			intSim := BigramDiceSimilarity(expAfter, candA)
			if UTF16Len(candA) > UTF16Len(expAfter) {
				if pSim := BigramDiceSimilarity(expAfter, headUTF16Units(candA, UTF16Len(expAfter))); pSim > intSim {
					intSim = pSim
				}
			}
			if intSim >= 0.85 && 0.85*intSim > score {
				score = 0.85 * intSim
			}
		} else if !isStartBoundary && UTF16Len(expBefore) >= 12 {
			intSim := BigramDiceSimilarity(expBefore, candB)
			if UTF16Len(candB) > UTF16Len(expBefore) {
				if sSim := BigramDiceSimilarity(expBefore, tailUTF16Units(candB, UTF16Len(expBefore))); sSim > intSim {
					intSim = sSim
				}
			}
			if intSim >= 0.85 && 0.85*intSim > score {
				score = 0.85 * intSim
			}
		}

		dist := absIntLocal(docIdx - origDocIdx)
		if score > bestScore+1e-6 || (absFloatLocal(score-bestScore) <= 1e-6 && dist < bestDist) {
			bestScore = score
			bestPos = docIdx
			bestDist = dist
		}
	}

	if bestPos > 0 && bestScore >= ReanchorMinSimilarity {
		return bestPos, true
	}
	return 0, false
}

// ReanchorSuggestions performs Two-Pass surrogate-pair-safe UTF-16 re-anchoring of Stage 3 MatchedSuggestions
// against a freshly fetched document revision:
//   - Pass 1 (Step A): O(log N) exact coordinate + exact 64-unit context verification across ALL items first,
//     reserving usedRanges so fuzzy matches never steal an unshifted occurrence.
//   - Pass 2 (Step B): Length-weighted context similarity search across settled freshBlocks for shifted items,
//     safely marking rewritten spans stale_superseded, ambiguous ties stale_ambiguous, and out-of-scope
//     shifts stale_out_of_scope.
//   - Finally sorts all surviving items in strictly descending StartIndex order (bottom-to-top).
func ReanchorSuggestions(
	items []MatchedSuggestion,
	freshBlocks []EditableBlock,
	freshBuf *SettledUTF16Buffer,
	selectionOnly bool,
	reanchoredScopes []RangeInterval,
) ReanchorOutcome {
	outcome := ReanchorOutcome{}
	if len(items) == 0 {
		return outcome
	}
	if freshBuf == nil || len(freshBuf.Units) == 0 {
		freshBuf = BuildSettledBufferFromBlocks(freshBlocks)
	}

	var usedRanges []RangeInterval
	pass1Done := make([]bool, len(items))
	surviving := make([]MatchedSuggestion, 0, len(items))
	skipped := make([]MatchedSuggestion, 0)

	// Helper verifying that [startIdx, endIdx) lies strictly within a settled EditableBlock in freshBlocks
	// (preventing anchoring onto pending collaborator suggestedInsertionIds / suggestedDeletionIds or across '\n').
	findContainingBlock := func(startIdx, endIdx int) *EditableBlock {
		for i := range freshBlocks {
			b := &freshBlocks[i]
			bEnd := b.EndIndex
			if bEnd <= b.StartIndex {
				bEnd = b.StartIndex + UTF16Len(b.Text)
			}
			if startIdx >= b.StartIndex && endIdx <= bEnd {
				return b
			}
		}
		return nil
	}

	inSelectionScope := func(r RangeInterval) bool {
		if !selectionOnly {
			return true
		}
		if len(reanchoredScopes) == 0 {
			return false
		}
		return RangesOverlap([]RangeInterval{r}, reanchoredScopes)
	}

	// Pass 1 (Step A): Exact coordinate + exact CtxBefore/CtxAfter fast path
	for i, item := range items {
		candRange := RangeInterval{StartIndex: item.StartIndex, EndIndex: item.EndIndex}
		if RangesOverlap([]RangeInterval{candRange}, usedRanges) {
			continue
		}
		if !inSelectionScope(candRange) {
			continue
		}
		blk := findContainingBlock(item.StartIndex, item.EndIndex)
		if blk == nil {
			continue
		}
		ctxB, ctxA, exactSpan, ok := freshBuf.SliceContextAroundDocSpan(item.StartIndex, item.EndIndex, ContextAnchorUTF16Units)
		if !ok || exactSpan != item.OriginalText {
			continue
		}
		if ctxB != item.CtxBefore || ctxA != item.CtxAfter {
			continue
		}

		updated := item
		updated.AnchorStatus = AnchorStatusExact
		updated.ShiftDelta = 0
		if updated.FilePath == "" && blk.FilePath != "" {
			updated.FilePath = blk.FilePath
			updated.LineNumber = blk.LineNumber
		}
		usedRanges = append(usedRanges, candRange)
		surviving = append(surviving, updated)
		outcome.ExactCount++
		pass1Done[i] = true
	}

	// Pass 2 (Step B): Context-similarity re-anchoring across freshBlocks
	type scoredCandidate struct {
		interval RangeInterval
		block    *EditableBlock
		ctxB     string
		ctxA     string
		score    float64
		dist     int
	}

	for i, item := range items {
		if pass1Done[i] {
			continue
		}

		var inScopeCands []scoredCandidate
		var outScopeCands []scoredCandidate

		for bIdx := range freshBlocks {
			blk := &freshBlocks[bIdx]
			searchByteFrom := 0
			for searchByteFrom < len(blk.Text) {
				relStart, relEnd, bytePos, ok := FindSubstringUTF16(blk.Text, item.OriginalText, searchByteFrom)
				if !ok {
					break
				}
				absStart := blk.StartIndex + relStart
				absEnd := blk.StartIndex + relEnd
				iv := RangeInterval{StartIndex: absStart, EndIndex: absEnd}

				if !RangesOverlap([]RangeInterval{iv}, usedRanges) {
					candB, candA, _, spanOK := freshBuf.SliceContextAroundDocSpan(absStart, absEnd, ContextAnchorUTF16Units)
					if !spanOK {
						// Fallback block-local context measured strictly in UTF-16 units
						candB = tailUTF16Units(blk.Text[:bytePos], ContextAnchorUTF16Units)
						afterByte := bytePos + len(item.OriginalText)
						if afterByte < len(blk.Text) {
							candA = headUTF16Units(blk.Text[afterByte:], ContextAnchorUTF16Units)
						}
					}
					sc := scoredCandidate{
						interval: iv,
						block:    blk,
						ctxB:     candB,
						ctxA:     candA,
						score:    WeightedContextSimilarity(item.CtxBefore, item.CtxAfter, candB, candA),
						dist:     absIntLocal(absStart - item.StartIndex),
					}
					if inSelectionScope(iv) {
						inScopeCands = append(inScopeCands, sc)
					} else {
						outScopeCands = append(outScopeCands, sc)
					}
				}

				_, runeW := decodeFirstRuneWidth(blk.Text[bytePos:])
				searchByteFrom = bytePos + runeW
			}
		}

		if len(inScopeCands) == 0 {
			staleItem := item
			staleItem.AnchorStatus = AnchorStatusStaleSuperseded
			for _, oc := range outScopeCands {
				if oc.score >= ReanchorMinSimilarity {
					staleItem.AnchorStatus = AnchorStatusStaleOutOfScope
					break
				}
			}
			skipped = append(skipped, staleItem)
			outcome.SkippedCount++
			continue
		}

		// Sort candidates by descending similarity, breaking ties by proximity to original StartIndex
		sort.SliceStable(inScopeCands, func(a, b int) bool {
			if absFloatLocal(inScopeCands[a].score-inScopeCands[b].score) > 1e-6 {
				return inScopeCands[a].score > inScopeCands[b].score
			}
			return inScopeCands[a].dist < inScopeCands[b].dist
		})

		best := inScopeCands[0]
		// Special case: whole-document single short phrase where both expected contexts were empty
		if item.CtxBefore == "" && item.CtxAfter == "" {
			if len(inScopeCands) == 1 {
				best.score = 1.0
			} else {
				staleItem := item
				staleItem.AnchorStatus = AnchorStatusStaleAmbiguous
				skipped = append(skipped, staleItem)
				outcome.SkippedCount++
				continue
			}
		}

		if best.score < ReanchorMinSimilarity {
			staleItem := item
			staleItem.AnchorStatus = AnchorStatusStaleSuperseded
			skipped = append(skipped, staleItem)
			outcome.SkippedCount++
			continue
		}

		if len(inScopeCands) >= 2 {
			second := inScopeCands[1]
			if best.score-second.score < ReanchorAmbiguousMargin {
				// Only break near-ties if context is still essentially intact (>= 0.90), best barely moved (<= 32 units),
				// AND the competing duplicate is decisively farther away (> 64 UTF-16 units beyond best).
				if best.score < 0.90 || best.dist > 32 || second.dist-best.dist <= 64 {
					staleItem := item
					staleItem.AnchorStatus = AnchorStatusStaleAmbiguous
					skipped = append(skipped, staleItem)
					outcome.SkippedCount++
					continue
				}
			}
		}

		updated := item
		updated.ShiftDelta = best.interval.StartIndex - item.StartIndex
		updated.StartIndex = best.interval.StartIndex
		updated.EndIndex = best.interval.EndIndex
		updated.CtxBefore = best.ctxB
		updated.CtxAfter = best.ctxA
		if updated.ShiftDelta == 0 {
			updated.AnchorStatus = AnchorStatusExact
			outcome.ExactCount++
		} else {
			updated.AnchorStatus = AnchorStatusReanchored
			outcome.ReanchoredCount++
		}
		if updated.FilePath == "" && best.block != nil && best.block.FilePath != "" {
			updated.FilePath = best.block.FilePath
			updated.LineNumber = best.block.LineNumber
		}
		usedRanges = append(usedRanges, best.interval)
		surviving = append(surviving, updated)
	}

	// Mandatory post-reanchor sort in strictly descending StartIndex order (bottom-to-top)
	// so even paragraph cut-and-paste reorderings maintain 100% atomic batchUpdate offset safety.
	sort.SliceStable(surviving, func(i, j int) bool {
		return surviving[i].StartIndex > surviving[j].StartIndex
	})

	outcome.Surviving = surviving
	outcome.Skipped = skipped
	return outcome
}

// WeightedContextSimilarity computes length-weighted bigram Dice similarity between expected
// (expBefore, expAfter) and candidate (candBefore, candAfter) contexts, handling start-of-doc
// (expBefore == "") and end-of-doc (expAfter == "") boundaries without empty-side dilution.
func WeightedContextSimilarity(expBefore, expAfter, candBefore, candAfter string) float64 {
	wB := UTF16Len(expBefore)
	wA := UTF16Len(expAfter)
	if wB == 0 && wA == 0 {
		if candBefore == "" && candAfter == "" {
			return 1.0
		}
		return 0.0
	}

	var sB, sA float64
	if wB > 0 {
		sB = BigramDiceSimilarity(expBefore, candBefore)
		// Also compare against the trailing wB UTF-16 units of candBefore so insertions right above
		// a near-top-of-document suggestion do not dilute the short original prefix context.
		if UTF16Len(candBefore) > wB {
			if sufSim := BigramDiceSimilarity(expBefore, tailUTF16Units(candBefore, wB)); sufSim > sB {
				sB = sufSim
			}
		}
	}
	if wA > 0 {
		sA = BigramDiceSimilarity(expAfter, candAfter)
		if UTF16Len(candAfter) > wA {
			if prefSim := BigramDiceSimilarity(expAfter, headUTF16Units(candAfter, wA)); prefSim > sA {
				sA = prefSim
			}
		}
	}

	return (float64(wB)*sB + float64(wA)*sA) / float64(wB+wA)
}

// BigramDiceSimilarity computes whitespace-normalized character/bigram Sørensen-Dice similarity in [0.0, 1.0].
func BigramDiceSimilarity(a, b string) float64 {
	na := normalizeContextWhitespace(a)
	nb := normalizeContextWhitespace(b)
	if na == "" || nb == "" {
		return 0.0
	}
	if na == nb {
		return 1.0
	}
	ra := []rune(na)
	rb := []rune(nb)
	if len(ra) < 2 || len(rb) < 2 {
		if strings.Contains(na, nb) || strings.Contains(nb, na) {
			shorter := len(ra)
			longer := len(rb)
			if shorter > longer {
				shorter, longer = longer, shorter
			}
			return float64(shorter) / float64(longer)
		}
		return 0.0
	}

	counts := make(map[string]int, len(ra)-1)
	for i := 0; i < len(ra)-1; i++ {
		bg := string(ra[i : i+2])
		counts[bg]++
	}
	intersection := 0
	for i := 0; i < len(rb)-1; i++ {
		bg := string(rb[i : i+2])
		if counts[bg] > 0 {
			intersection++
			counts[bg]--
		}
	}
	return (2.0 * float64(intersection)) / float64((len(ra)-1)+(len(rb)-1))
}

func normalizeContextWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func tailUTF16Units(s string, maxUnits int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= maxUnits {
		return s
	}
	return string(utf16.Decode(safeUTF16Slice(u, len(u)-maxUnits, len(u))))
}

func headUTF16Units(s string, maxUnits int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= maxUnits {
		return s
	}
	return string(utf16.Decode(safeUTF16Slice(u, 0, maxUnits)))
}

func absIntLocal(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func maxIntLocal(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absFloatLocal(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
