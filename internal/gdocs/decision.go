package gdocs

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	DecisionMaxPending    = 25
	DecisionMaxRecentLog  = 12
	DecisionTextSigLimit  = 80
	DecisionSnippetLimit  = 45
	DecisionAnchorLimit   = 28
	DecisionStaleTTLMs    = 7 * 24 * 60 * 60 * 1000 // 7 days in milliseconds
	DecisionSafeJSONBytes = 7500
)

var (
	decisionMu sync.Mutex
	reSpaces   = regexp.MustCompile(`\s+`)
	reAICatJa  = regexp.MustCompile(`【DocuMentor AI[｜|]([^】\n]+)】`)
	reAICatEn  = regexp.MustCompile(`\[DocuMentor AI\s*\|\s*([^\]\n]+)\]`)
)

// DecisionCounters holds the raw counts of issued and resolved AI suggestions.
type DecisionCounters struct {
	Issued   int `json:"issued"`
	Accepted int `json:"accepted"`
	Edited   int `json:"edited"`
	Rejected int `json:"rejected"`
	Expired  int `json:"expired"`
}

// LastRunStats holds the counters and metadata for the most recent AI proofreading run.
type LastRunStats struct {
	RunID     string `json:"runId"`
	Timestamp string `json:"timestamp"`
	Issued    int    `json:"issued"`
	Accepted  int    `json:"accepted"`
	Edited    int    `json:"edited"`
	Rejected  int    `json:"rejected"`
	Expired   int    `json:"expired"`
}

// PendingDecisionItem represents an active AI-issued inline suggestion tracked for future reconciliation.
type PendingDecisionItem struct {
	SuggestionID string `json:"sId"`
	RunID        string `json:"runId"`
	TabID        string `json:"tabId"`
	Category     string `json:"cat"`
	Orig         string `json:"orig"`
	Sug          string `json:"sug"`
	CtxBefore    string `json:"ctxBefore"`
	CtxAfter     string `json:"ctxAfter"`
	TimestampMs  int64  `json:"ts"`
}

// RecentDecisionEntry represents a reconciled decision in the recent decision log.
type RecentDecisionEntry struct {
	SuggestionID string `json:"sId"`
	Status       string `json:"status"` // "ACCEPTED", "EDITED", "REJECTED"
	Category     string `json:"cat"`
	OrigSnippet  string `json:"origSnippet"`
	SugSnippet   string `json:"sugSnippet"`
	FinalSnippet string `json:"finalSnippet"`
	DecidedAt    string `json:"decidedAt"`
}

// RawDecisionRecord is the persisted JSON structure matching GAS `DOCUMENTOR_DECISION_RECORD_V1`.
type RawDecisionRecord struct {
	Version             int                   `json:"v"`
	UpdatedAt           string                `json:"updatedAt"`
	Cumulative          DecisionCounters      `json:"cumulative"`
	LastRun             LastRunStats          `json:"lastRun"`
	Pending             []PendingDecisionItem `json:"pending"`
	RecentDecisions     []RecentDecisionEntry `json:"recentDecisions"`
	ReconciledThreadIDs []string              `json:"reconciledThreadIds,omitempty"`
}

// CumulativeDecisionSummary is the client-facing cumulative metrics summary.
type CumulativeDecisionSummary struct {
	Issued       int  `json:"issued"`
	Accepted     int  `json:"accepted"`
	Edited       int  `json:"edited"`
	Rejected     int  `json:"rejected"`
	Expired      int  `json:"expired"`
	Decided      int  `json:"decided"`
	Constructive int  `json:"constructive"`
	ImpactRate   *int `json:"impactRate"`
	AcceptRate   *int `json:"acceptRate"`
	EditRate     *int `json:"editRate"`
	RejectRate   *int `json:"rejectRate"`
}

// LastRunDecisionSummary is the client-facing summary of the most recent proofreading pass.
type LastRunDecisionSummary struct {
	RunID      string `json:"runId"`
	Timestamp  string `json:"timestamp"`
	Issued     int    `json:"issued"`
	Accepted   int    `json:"accepted"`
	Edited     int    `json:"edited"`
	Rejected   int    `json:"rejected"`
	Expired    int    `json:"expired"`
	Decided    int    `json:"decided"`
	ImpactRate *int   `json:"impactRate"`
}

// DecisionRecordSummary is the client-facing Coach Impact & Decision Record payload.
type DecisionRecordSummary struct {
	UpdatedAt                   string                    `json:"updatedAt"`
	Cumulative                  CumulativeDecisionSummary `json:"cumulative"`
	LastRun                     LastRunDecisionSummary    `json:"lastRun"`
	PendingAICount              int                       `json:"pendingAiCount"`
	ActiveHumanSuggestionsCount int                       `json:"activeHumanSuggestionsCount"`
	VerdictState                string                    `json:"verdictState"` // "NO_DATA", "AWAITING_REVIEW", "IMPROVING_DRAFT", "BALANCED_REVIEW", "HIGH_REVIEW_WORK"
	RecentDecisions             []RecentDecisionEntry     `json:"recentDecisions"`
}

// IssuedAISuggestionInput holds metadata for a newly created AI inline suggestion in Step 4.
type IssuedAISuggestionInput struct {
	SuggestionID  string
	Category      string
	OriginalText  string
	SuggestedText string
	CtxBefore     string
	CtxAfter      string
	TabID         string
}

// ReconcileParams holds inputs for ReconcileDecisionRecordWithTab.
type ReconcileParams struct {
	DocumentID           string
	ActiveTabID          string
	IsTabCompletelyEmpty bool
	FullTabSettledText   string
	AllTabSuggestions    []*ExistingSuggestion
	ActiveSuggestionIDs  []string
	AllResolvedReviews   []ResolvedReview
}

// DecisionOutcome holds the evaluated status and final snippet of a disappeared pending AI suggestion.
type DecisionOutcome struct {
	Status       string // "ACCEPTED", "EDITED", "REJECTED", "EXPIRED"
	FinalSnippet string
}

// CreateEmptyDecisionRecord returns an initialized empty RawDecisionRecord.
func CreateEmptyDecisionRecord() *RawDecisionRecord {
	return &RawDecisionRecord{
		Version:         1,
		Pending:         []PendingDecisionItem{},
		RecentDecisions: []RecentDecisionEntry{},
	}
}

// NormalizeDecisionText normalizes whitespace and truncates a string to maxRunes runes.
func NormalizeDecisionText(str string, maxRunes int) string {
	cleaned := strings.TrimSpace(reSpaces.ReplaceAllString(str, " "))
	if cleaned == "" {
		return ""
	}
	if maxRunes <= 0 {
		return cleaned
	}
	runes := []rune(cleaned)
	if len(runes) <= maxRunes {
		return cleaned
	}
	return string(runes[:maxRunes])
}

// HasDocuMentorAISignature checks whether an ExistingSuggestion carries a DocuMentor AI comment signature.
func HasDocuMentorAISignature(sug *ExistingSuggestion) bool {
	if sug == nil {
		return false
	}
	checkStr := func(s string) bool {
		return strings.Contains(s, "DocuMentor AI") || strings.Contains(s, "AI推敲コーチ")
	}
	for _, c := range sug.Comments {
		if checkStr(c.Content) {
			return true
		}
		for _, r := range c.Replies {
			if checkStr(r.Content) {
				return true
			}
		}
	}
	return false
}

// extractCategoryFromAISignature extracts the coaching category from a DocuMentor AI reply comment on sug.
func extractCategoryFromAISignature(sug *ExistingSuggestion) string {
	if sug == nil {
		return "AI Coaching"
	}
	parseCat := func(s string) string {
		if m := reAICatJa.FindStringSubmatch(s); len(m) > 1 {
			return NormalizeDecisionText(m[1], 24)
		}
		if m := reAICatEn.FindStringSubmatch(s); len(m) > 1 {
			return NormalizeDecisionText(m[1], 24)
		}
		return ""
	}
	for _, c := range sug.Comments {
		if cat := parseCat(c.Content); cat != "" {
			return cat
		}
		for _, r := range c.Replies {
			if cat := parseCat(r.Content); cat != "" {
				return cat
			}
		}
	}
	return "AI Coaching"
}

// extractContextAnchorsFromSettledText extracts up to DecisionAnchorLimit runes before and after origNorm in normTabText.
func extractContextAnchorsFromSettledText(normTabText, origNorm string) (string, string) {
	if normTabText == "" || origNorm == "" {
		return "", ""
	}
	before, after, ok := strings.Cut(normTabText, origNorm)
	if !ok {
		return "", ""
	}
	beforeRunes := []rune(before)
	afterRunes := []rune(after)

	ctxBefore := ""
	if len(beforeRunes) > 0 {
		start := max(len(beforeRunes)-DecisionAnchorLimit, 0)
		ctxBefore = NormalizeDecisionText(string(beforeRunes[start:]), DecisionAnchorLimit)
	}
	ctxAfter := ""
	if len(afterRunes) > 0 {
		end := min(DecisionAnchorLimit, len(afterRunes))
		ctxAfter = NormalizeDecisionText(string(afterRunes[:end]), DecisionAnchorLimit)
	}
	return ctxBefore, ctxAfter
}

// HasDocuMentorAISignatureOnResolved checks whether a ResolvedReview carries a DocuMentor AI signature.
func HasDocuMentorAISignatureOnResolved(rev ResolvedReview) bool {
	checkStr := func(s string) bool {
		return strings.Contains(s, "DocuMentor AI") || strings.Contains(s, "AI推敲コーチ")
	}
	if checkStr(rev.Content) || checkStr(rev.Text) {
		return true
	}
	for _, r := range rev.Replies {
		if checkStr(r.Content) {
			return true
		}
	}
	return false
}

// ComputeBigramSimilarity computes Sørensen–Dice character bigram similarity (0.0 to 1.0) across runes.
// Language-agnostic: works across English prose, Japanese prose, and source code.
func ComputeBigramSimilarity(strA, strB string) float64 {
	aStr := strings.ToLower(NormalizeDecisionText(strA, 120))
	bStr := strings.ToLower(NormalizeDecisionText(strB, 120))
	if aStr == "" || bStr == "" {
		return 0
	}
	if aStr == bStr {
		return 1
	}
	a := []rune(aStr)
	b := []rune(bStr)
	if len(a) < 2 || len(b) < 2 {
		if aStr == bStr {
			return 1
		}
		return 0
	}

	bigramsA := make(map[string]int, len(a)-1)
	for i := 0; i < len(a)-1; i++ {
		bg := string(a[i : i+2])
		bigramsA[bg]++
	}

	intersection := 0
	for j := 0; j < len(b)-1; j++ {
		bg := string(b[j : j+2])
		if cnt := bigramsA[bg]; cnt > 0 {
			intersection++
			bigramsA[bg] = cnt - 1
		}
	}

	return float64(2*intersection) / float64((len(a)-1)+(len(b)-1))
}

// EvaluatePendingItemDecision evaluates a disappeared/resolved AI suggestion item to determine whether it was:
// - "ACCEPTED": AI suggested text was adopted verbatim into the document.
// - "EDITED": Author modified the AI suggestion or rewrote the flagged span into a custom phrase.
// - "REJECTED": Author rejected the suggestion and kept the original text intact.
// - "EXPIRED": Surrounding paragraph/context was deleted or replaced wholesale (excluded from metrics).
func EvaluatePendingItemDecision(
	pendingItem PendingDecisionItem,
	normTabText string,
	resolvedStateMap map[string]string,
	explicitAction string,
) DecisionOutcome {
	origNorm := NormalizeDecisionText(pendingItem.Orig, DecisionTextSigLimit)
	sugNorm := NormalizeDecisionText(pendingItem.Sug, DecisionTextSigLimit)
	beforeNorm := NormalizeDecisionText(pendingItem.CtxBefore, DecisionAnchorLimit)
	afterNorm := NormalizeDecisionText(pendingItem.CtxAfter, DecisionAnchorLimit)

	if explicitAction == "ACCEPTED" {
		snip := sugNorm
		if snip == "" {
			snip = origNorm
		}
		return DecisionOutcome{
			Status:       "ACCEPTED",
			FinalSnippet: NormalizeDecisionText(snip, DecisionSnippetLimit),
		}
	}
	if explicitAction == "REJECTED" {
		return DecisionOutcome{
			Status:       "REJECTED",
			FinalSnippet: NormalizeDecisionText(origNorm, DecisionSnippetLimit),
		}
	}

	apiState := ""
	if resolvedStateMap != nil && pendingItem.SuggestionID != "" {
		apiState = resolvedStateMap[pendingItem.SuggestionID]
	}

	beforeRuneLen := len([]rune(beforeNorm))
	afterRuneLen := len([]rune(afterNorm))
	origRuneLen := len([]rune(origNorm))
	sugRuneLen := len([]rune(sugNorm))

	// 1. Check exact local span bounded by ctxBefore and ctxAfter when both anchors exist in the tab
	if beforeRuneLen >= 3 && afterRuneLen >= 3 {
		if bIdx := strings.Index(normTabText, beforeNorm); bIdx != -1 {
			spanStart := bIdx + len(beforeNorm)
			if relAfter := strings.Index(normTabText[spanStart:], afterNorm); relAfter != -1 {
				betweenRaw := strings.TrimSpace(normTabText[spanStart : spanStart+relAfter])
				if len([]rune(betweenRaw)) <= 260 {
					if sugNorm != "" && (betweenRaw == sugNorm || (strings.Contains(betweenRaw, sugNorm) && betweenRaw != origNorm)) {
						return DecisionOutcome{
							Status:       "ACCEPTED",
							FinalSnippet: NormalizeDecisionText(betweenRaw, DecisionSnippetLimit),
						}
					}
					if origNorm != "" && betweenRaw == origNorm {
						return DecisionOutcome{
							Status:       "REJECTED",
							FinalSnippet: NormalizeDecisionText(origNorm, DecisionSnippetLimit),
						}
					}
					if betweenRaw != "" && betweenRaw != origNorm && betweenRaw != sugNorm {
						return DecisionOutcome{
							Status:       "EDITED",
							FinalSnippet: NormalizeDecisionText(betweenRaw, DecisionSnippetLimit),
						}
					}
					if betweenRaw == "" && sugRuneLen == 0 && origRuneLen > 0 {
						return DecisionOutcome{
							Status:       "ACCEPTED",
							FinalSnippet: "(Deleted as suggested)",
						}
					}
				}
			}
		}
	}

	// 2. Check presence of suggestedText vs. originalText in settled tab text
	hasSug := sugRuneLen > 0 && strings.Contains(normTabText, sugNorm)
	hasOrig := origRuneLen > 0 && strings.Contains(normTabText, origNorm)
	hasBeforeAnchor := beforeRuneLen >= 4 && strings.Contains(normTabText, beforeNorm)
	hasAfterAnchor := afterRuneLen >= 4 && strings.Contains(normTabText, afterNorm)
	hasAnyAnchor := hasBeforeAnchor || hasAfterAnchor

	if hasSug && apiState != "REJECTED" {
		return DecisionOutcome{
			Status:       "ACCEPTED",
			FinalSnippet: NormalizeDecisionText(sugNorm, DecisionSnippetLimit),
		}
	}

	if hasOrig && !hasSug {
		return DecisionOutcome{
			Status:       "REJECTED",
			FinalSnippet: NormalizeDecisionText(origNorm, DecisionSnippetLimit),
		}
	}

	// Neither originalText nor exact suggestedText is present verbatim
	if !hasOrig && !hasSug {
		if hasAnyAnchor || apiState == "ACCEPTED" {
			editedSnippet := ""
			if hasBeforeAnchor {
				bPos := strings.Index(normTabText, beforeNorm) + len(beforeNorm)
				remRunes := []rune(normTabText[bPos:])
				if len(remRunes) > DecisionSnippetLimit {
					remRunes = remRunes[:DecisionSnippetLimit]
				}
				editedSnippet = strings.TrimSpace(string(remRunes))
			} else if hasAfterAnchor {
				aPos := strings.Index(normTabText, afterNorm)
				preRunes := []rune(normTabText[:aPos])
				if len(preRunes) > DecisionSnippetLimit {
					preRunes = preRunes[len(preRunes)-DecisionSnippetLimit:]
				}
				editedSnippet = strings.TrimSpace(string(preRunes))
			}
			fallback := editedSnippet
			if fallback == "" {
				fallback = sugNorm
			}
			if fallback == "" {
				fallback = origNorm
			}
			return DecisionOutcome{
				Status:       "EDITED",
				FinalSnippet: NormalizeDecisionText(fallback, DecisionSnippetLimit),
			}
		}

		// Check if a window in normTabText has strong similarity to suggestedText (partial edit without intact anchor)
		tabRunes := []rune(normTabText)
		if sugRuneLen >= 12 && len(tabRunes) > 0 {
			windowLen := min(sugRuneLen+20, len(tabRunes))
			step := max(sugRuneLen/3, 4)
			minWin := min(windowLen, 10)
			bestSim := 0.0
			bestWindow := ""
			for pos := 0; pos <= len(tabRunes)-minWin; pos += step {
				endPos := min(pos+windowLen, len(tabRunes))
				chunk := string(tabRunes[pos:endPos])
				sim := ComputeBigramSimilarity(sugNorm, chunk)
				if sim > bestSim {
					bestSim = sim
					bestWindow = chunk
				}
			}
			if bestSim >= 0.55 && bestSim < 0.99 {
				return DecisionOutcome{
					Status:       "EDITED",
					FinalSnippet: NormalizeDecisionText(bestWindow, DecisionSnippetLimit),
				}
			}
		}

		// Otherwise, the entire section/paragraph was removed or replaced wholesale -> EXPIRED
		return DecisionOutcome{
			Status:       "EXPIRED",
			FinalSnippet: "",
		}
	}

	// Both hasOrig and hasSug matched -> defer to API resolution state
	if apiState == "REJECTED" {
		return DecisionOutcome{
			Status:       "REJECTED",
			FinalSnippet: NormalizeDecisionText(origNorm, DecisionSnippetLimit),
		}
	}
	snip := sugNorm
	if snip == "" {
		snip = origNorm
	}
	return DecisionOutcome{
		Status:       "ACCEPTED",
		FinalSnippet: NormalizeDecisionText(snip, DecisionSnippetLimit),
	}
}

// BuildClientDecisionRecordSummary builds the rich client-facing DecisionRecordSummary with computed rates and verdict.
func BuildClientDecisionRecordSummary(record *RawDecisionRecord, activeAISuggestionsCount *int, activeHumanSuggestionsCount int) *DecisionRecordSummary {
	rec := record
	if rec == nil {
		rec = CreateEmptyDecisionRecord()
	}
	cum := rec.Cumulative
	lr := rec.LastRun

	decidedCount := cum.Accepted + cum.Edited + cum.Rejected
	constructiveCount := cum.Accepted + cum.Edited

	var impactRate, acceptRate, editRate, rejectRate *int
	if decidedCount > 0 {
		ir := int(math.Round(float64(constructiveCount) / float64(decidedCount) * 100.0))
		ar := int(math.Round(float64(cum.Accepted) / float64(decidedCount) * 100.0))
		er := int(math.Round(float64(cum.Edited) / float64(decidedCount) * 100.0))
		rr := int(math.Round(float64(cum.Rejected) / float64(decidedCount) * 100.0))
		impactRate = &ir
		acceptRate = &ar
		editRate = &er
		rejectRate = &rr
	}

	lrDecided := lr.Accepted + lr.Edited + lr.Rejected
	lrConstructive := lr.Accepted + lr.Edited
	var lrImpactRate *int
	if lrDecided > 0 {
		lri := int(math.Round(float64(lrConstructive) / float64(lrDecided) * 100.0))
		lrImpactRate = &lri
	}

	pendingAICount := len(rec.Pending)
	if activeAISuggestionsCount != nil {
		pendingAICount = *activeAISuggestionsCount
	}

	verdictState := "NO_DATA"
	if decidedCount == 0 && pendingAICount > 0 {
		verdictState = "AWAITING_REVIEW"
	} else if decidedCount > 0 && impactRate != nil {
		if *impactRate >= 65 {
			verdictState = "IMPROVING_DRAFT"
		} else if *impactRate >= 40 {
			verdictState = "BALANCED_REVIEW"
		} else {
			verdictState = "HIGH_REVIEW_WORK"
		}
	}

	recent := rec.RecentDecisions
	if len(recent) > DecisionMaxRecentLog {
		recent = recent[:DecisionMaxRecentLog]
	}
	if recent == nil {
		recent = []RecentDecisionEntry{}
	}

	return &DecisionRecordSummary{
		UpdatedAt: rec.UpdatedAt,
		Cumulative: CumulativeDecisionSummary{
			Issued:       cum.Issued,
			Accepted:     cum.Accepted,
			Edited:       cum.Edited,
			Rejected:     cum.Rejected,
			Expired:      cum.Expired,
			Decided:      decidedCount,
			Constructive: constructiveCount,
			ImpactRate:   impactRate,
			AcceptRate:   acceptRate,
			EditRate:     editRate,
			RejectRate:   rejectRate,
		},
		LastRun: LastRunDecisionSummary{
			RunID:      lr.RunID,
			Timestamp:  lr.Timestamp,
			Issued:     lr.Issued,
			Accepted:   lr.Accepted,
			Edited:     lr.Edited,
			Rejected:   lr.Rejected,
			Expired:    lr.Expired,
			Decided:    lrDecided,
			ImpactRate: lrImpactRate,
		},
		PendingAICount:              pendingAICount,
		ActiveHumanSuggestionsCount: activeHumanSuggestionsCount,
		VerdictState:                verdictState,
		RecentDecisions:             recent,
	}
}

// ReconcileDecisionRecordWithTab reconciles pending AI-issued suggestions stored in the Decision Record
// against the active tab's current settled text, active suggestions, and resolved review threads.
// It also tags each suggestion in params.AllTabSuggestions with IsAIGenerated = true | false.
func ReconcileDecisionRecordWithTab(params ReconcileParams) *DecisionRecordSummary {
	decisionMu.Lock()
	defer decisionMu.Unlock()

	docID := ExtractDocumentID(params.DocumentID)
	record := loadRawDecisionRecord(docID)

	pendingByID := make(map[string]PendingDecisionItem, len(record.Pending))
	for _, p := range record.Pending {
		if p.SuggestionID != "" {
			pendingByID[p.SuggestionID] = p
		}
	}

	activeSugIDSet := make(map[string]bool, len(params.ActiveSuggestionIDs))
	for _, id := range params.ActiveSuggestionIDs {
		activeSugIDSet[id] = true
	}

	nowMs := time.Now().UnixMilli()
	mutated := false
	normTabText := NormalizeDecisionText(params.FullTabSettledText, 250000)

	reconciledSet := make(map[string]bool, len(record.ReconciledThreadIDs))
	for _, id := range record.ReconciledThreadIDs {
		reconciledSet[id] = true
	}

	activeAICount := 0
	activeHumanCount := 0
	for _, sug := range params.AllTabSuggestions {
		if sug == nil {
			continue
		}
		_, isTrackedPending := pendingByID[sug.ID]
		hasAISig := HasDocuMentorAISignature(sug)
		sug.IsAIGenerated = isTrackedPending || hasAISig
		if sug.IsAIGenerated {
			activeAICount++
			// Cross-environment & post-reset active suggestion hydration:
			// If an active AI suggestion in the Google Doc is not currently in record.Pending
			// (e.g., created on another machine/GAS add-on, or the local store was reset/deleted while the suggestion remained open),
			// reconstruct its PendingDecisionItem from the live document so future accept/edit/reject actions are tracked.
			if !isTrackedPending && sug.ID != "" {
				origNorm := NormalizeDecisionText(sug.DeletedText, DecisionTextSigLimit)
				sugNorm := NormalizeDecisionText(sug.InsertedText, DecisionTextSigLimit)
				ctxBefore, ctxAfter := extractContextAnchorsFromSettledText(normTabText, origNorm)
				hydrated := PendingDecisionItem{
					SuggestionID: sug.ID,
					RunID:        "hydrated",
					TabID:        params.ActiveTabID,
					Category:     extractCategoryFromAISignature(sug),
					Orig:         origNorm,
					Sug:          sugNorm,
					CtxBefore:    ctxBefore,
					CtxAfter:     ctxAfter,
					TimestampMs:  nowMs,
				}
				record.Pending = append(record.Pending, hydrated)
				pendingByID[sug.ID] = hydrated
				if !reconciledSet[sug.ID] {
					record.Cumulative.Issued++
				}
				mutated = true
			}
		} else {
			activeHumanCount++
		}
	}

	// Case 1: If the active tab was wiped completely blank, expire any pending items belonging to this tab
	if params.IsTabCompletelyEmpty && len(record.Pending) > 0 {
		var keptPending []PendingDecisionItem
		for _, item := range record.Pending {
			if item.TabID != "" && params.ActiveTabID != "" && item.TabID != params.ActiveTabID && (nowMs-item.TimestampMs) < DecisionStaleTTLMs {
				keptPending = append(keptPending, item)
			} else {
				record.Cumulative.Expired++
				if item.RunID != "" && item.RunID == record.LastRun.RunID {
					record.LastRun.Expired++
				}
				mutated = true
			}
		}
		record.Pending = keptPending
		if mutated && docID != "" {
			saveRawDecisionRecord(docID, record)
		}
		zero := 0
		return BuildClientDecisionRecordSummary(record, &zero, 0)
	}

	// Build map of suggestionId -> resolutionState from resolved threads in Docs API
	resolvedStateMap := make(map[string]string)
	for _, r := range params.AllResolvedReviews {
		if r.SuggestionID != "" && r.ResolutionState != "" {
			resolvedStateMap[r.SuggestionID] = r.ResolutionState
		}
	}

	if len(record.Pending) > 0 {
		var stillPending []PendingDecisionItem
		for _, item := range record.Pending {
			if item.SuggestionID == "" {
				mutated = true
				continue
			}

			// Expire ancient pending items (> 7 days old)
			if item.TimestampMs > 0 && (nowMs-item.TimestampMs) > DecisionStaleTTLMs {
				record.Cumulative.Expired++
				if item.RunID != "" && item.RunID == record.LastRun.RunID {
					record.LastRun.Expired++
				}
				mutated = true
				continue
			}

			// If this item is still an active suggestion in the document, keep it in pending
			if activeSugIDSet[item.SuggestionID] {
				stillPending = append(stillPending, item)
				continue
			}

			// If this item belongs to a different tab than the one currently parsed, preserve it in pending
			if item.TabID != "" && params.ActiveTabID != "" && item.TabID != params.ActiveTabID {
				stillPending = append(stillPending, item)
				continue
			}

			// The AI suggestion is no longer active on this tab -> evaluate user decision!
			outcome := EvaluatePendingItemDecision(item, normTabText, resolvedStateMap, "")
			mutated = true
			if item.SuggestionID != "" && !reconciledSet[item.SuggestionID] {
				reconciledSet[item.SuggestionID] = true
				record.ReconciledThreadIDs = append(record.ReconciledThreadIDs, item.SuggestionID)
			}

			switch outcome.Status {
			case "ACCEPTED":
				record.Cumulative.Accepted++
				if item.RunID != "" && item.RunID == record.LastRun.RunID {
					record.LastRun.Accepted++
				}
			case "EDITED":
				record.Cumulative.Edited++
				if item.RunID != "" && item.RunID == record.LastRun.RunID {
					record.LastRun.Edited++
				}
			case "REJECTED":
				record.Cumulative.Rejected++
				if item.RunID != "" && item.RunID == record.LastRun.RunID {
					record.LastRun.Rejected++
				}
			default:
				record.Cumulative.Expired++
				if item.RunID != "" && item.RunID == record.LastRun.RunID {
					record.LastRun.Expired++
				}
			}

			if outcome.Status == "ACCEPTED" || outcome.Status == "EDITED" || outcome.Status == "REJECTED" {
				cat := item.Category
				if cat == "" {
					cat = "Revision"
				}
				entry := RecentDecisionEntry{
					SuggestionID: item.SuggestionID,
					Status:       outcome.Status,
					Category:     NormalizeDecisionText(cat, 24),
					OrigSnippet:  NormalizeDecisionText(item.Orig, DecisionSnippetLimit),
					SugSnippet:   NormalizeDecisionText(item.Sug, DecisionSnippetLimit),
					FinalSnippet: outcome.FinalSnippet,
					DecidedAt:    time.Now().UTC().Format(time.RFC3339),
				}
				record.RecentDecisions = append([]RecentDecisionEntry{entry}, record.RecentDecisions...)
			}
		}
		record.Pending = stillPending
	}

	// Stateless fallback: if resolved reviews in Google Docs carry DocuMentor AI signatures that were never tracked locally
	// (e.g., created on another machine or via the GAS add-on), credit them once so cross-environment metrics stay accurate.
	for _, r := range params.AllResolvedReviews {
		if r.ThreadType != "SUGGESTION" && r.SuggestionID == "" {
			continue
		}
		if !HasDocuMentorAISignatureOnResolved(r) {
			continue
		}
		threadKey := firstNonEmpty(r.SuggestionID, r.CommentID)
		if threadKey == "" || reconciledSet[threadKey] {
			continue
		}
		reconciledSet[threadKey] = true
		record.ReconciledThreadIDs = append(record.ReconciledThreadIDs, threadKey)
		record.Cumulative.Issued++
		state := strings.ToUpper(r.ResolutionState)
		status := "ACCEPTED"
		if state == "REJECTED" {
			status = "REJECTED"
			record.Cumulative.Rejected++
		} else {
			record.Cumulative.Accepted++
		}
		record.RecentDecisions = append([]RecentDecisionEntry{{
			SuggestionID: threadKey,
			Status:       status,
			Category:     "AI Coaching",
			OrigSnippet:  NormalizeDecisionText(r.QuotedText, DecisionSnippetLimit),
			SugSnippet:   NormalizeDecisionText(r.SummaryText, DecisionSnippetLimit),
			FinalSnippet: NormalizeDecisionText(firstNonEmpty(r.QuotedText, r.SummaryText), DecisionSnippetLimit),
			DecidedAt:    time.Now().UTC().Format(time.RFC3339),
		}}, record.RecentDecisions...)
		mutated = true
	}

	if mutated && docID != "" {
		saveRawDecisionRecord(docID, record)
	}

	effAICount := max(len(record.Pending), activeAICount)
	return BuildClientDecisionRecordSummary(record, &effAICount, activeHumanCount)
}

// RegisterIssuedAISuggestions registers newly applied AI inline suggestions at the end of Step 4.
func RegisterIssuedAISuggestions(docID string, issuedItems []IssuedAISuggestionInput, activeTabID string) *DecisionRecordSummary {
	decisionMu.Lock()
	defer decisionMu.Unlock()

	cleanDocID := ExtractDocumentID(docID)
	record := loadRawDecisionRecord(cleanDocID)

	var validItems []IssuedAISuggestionInput
	for _, it := range issuedItems {
		if strings.TrimSpace(it.SuggestionID) != "" {
			validItems = append(validItems, it)
		}
	}
	if len(validItems) == 0 {
		return BuildClientDecisionRecordSummary(record, nil, 0)
	}

	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	runID := "run_" + time.Now().Format("20060102150405.000")

	record.LastRun = LastRunStats{
		RunID:     runID,
		Timestamp: now.Format(time.RFC3339),
		Issued:    len(validItems),
	}
	record.Cumulative.Issued += len(validItems)

	existingIDs := make(map[string]bool, len(record.Pending))
	for _, p := range record.Pending {
		existingIDs[p.SuggestionID] = true
	}

	for _, it := range validItems {
		if existingIDs[it.SuggestionID] {
			continue
		}
		existingIDs[it.SuggestionID] = true
		cat := it.Category
		if cat == "" {
			cat = "Revision"
		}
		tab := it.TabID
		if tab == "" {
			tab = activeTabID
		}
		record.Pending = append(record.Pending, PendingDecisionItem{
			SuggestionID: it.SuggestionID,
			RunID:        runID,
			TabID:        tab,
			Category:     NormalizeDecisionText(cat, 24),
			Orig:         NormalizeDecisionText(it.OriginalText, DecisionTextSigLimit),
			Sug:          NormalizeDecisionText(it.SuggestedText, DecisionTextSigLimit),
			CtxBefore:    NormalizeDecisionText(it.CtxBefore, DecisionAnchorLimit),
			CtxAfter:     NormalizeDecisionText(it.CtxAfter, DecisionAnchorLimit),
			TimestampMs:  nowMs,
		})
	}

	if cleanDocID != "" {
		saveRawDecisionRecord(cleanDocID, record)
	}
	cnt := len(record.Pending)
	return BuildClientDecisionRecordSummary(record, &cnt, 0)
}

// RecordBulkSuggestionDecision updates the Decision Record when the user triggers bulk Accept All or Reject All.
// Strictly updates only AI-issued pending items (or items carrying the DocuMentor AI signature).
func RecordBulkSuggestionDecision(docID string, actionedSuggestionIDs []string, explicitDecision string) *DecisionRecordSummary {
	decisionMu.Lock()
	defer decisionMu.Unlock()

	cleanDocID := ExtractDocumentID(docID)
	record := loadRawDecisionRecord(cleanDocID)
	if len(record.Pending) == 0 || len(actionedSuggestionIDs) == 0 {
		return BuildClientDecisionRecordSummary(record, nil, 0)
	}

	idSet := make(map[string]bool, len(actionedSuggestionIDs))
	for _, id := range actionedSuggestionIDs {
		idSet[id] = true
	}
	reconciledSet := make(map[string]bool, len(record.ReconciledThreadIDs))
	for _, id := range record.ReconciledThreadIDs {
		reconciledSet[id] = true
	}

	var stillPending []PendingDecisionItem
	mutated := false

	for _, item := range record.Pending {
		if item.SuggestionID == "" {
			continue
		}
		if idSet[item.SuggestionID] {
			mutated = true
			if !reconciledSet[item.SuggestionID] {
				reconciledSet[item.SuggestionID] = true
				record.ReconciledThreadIDs = append(record.ReconciledThreadIDs, item.SuggestionID)
			}
			status := "REJECTED"
			finalSnip := item.Orig
			if explicitDecision == "ACCEPTED" {
				status = "ACCEPTED"
				record.Cumulative.Accepted++
				if item.RunID != "" && item.RunID == record.LastRun.RunID {
					record.LastRun.Accepted++
				}
				if item.Sug != "" {
					finalSnip = item.Sug
				}
			} else {
				record.Cumulative.Rejected++
				if item.RunID != "" && item.RunID == record.LastRun.RunID {
					record.LastRun.Rejected++
				}
			}
			cat := item.Category
			if cat == "" {
				cat = "Revision"
			}
			entry := RecentDecisionEntry{
				SuggestionID: item.SuggestionID,
				Status:       status,
				Category:     NormalizeDecisionText(cat, 24),
				OrigSnippet:  NormalizeDecisionText(item.Orig, DecisionSnippetLimit),
				SugSnippet:   NormalizeDecisionText(item.Sug, DecisionSnippetLimit),
				FinalSnippet: NormalizeDecisionText(finalSnip, DecisionSnippetLimit),
				DecidedAt:    time.Now().UTC().Format(time.RFC3339),
			}
			record.RecentDecisions = append([]RecentDecisionEntry{entry}, record.RecentDecisions...)
		} else {
			stillPending = append(stillPending, item)
		}
	}

	record.Pending = stillPending
	if mutated && cleanDocID != "" {
		saveRawDecisionRecord(cleanDocID, record)
	}
	cnt := len(stillPending)
	return BuildClientDecisionRecordSummary(record, &cnt, 0)
}

// ResetDecisionRecord clears the persisted Coach Decision Record counters and recent decisions for docID,
// while preserving ReconciledThreadIDs (plus any additionalSuppressedIDs from already-resolved threads on the document)
// so that historical resolved threads on the same Google Document are never re-counted on subsequent runs.
func ResetDecisionRecord(docID string, additionalSuppressedIDs ...string) *DecisionRecordSummary {
	decisionMu.Lock()
	defer decisionMu.Unlock()

	cleanDocID := ExtractDocumentID(docID)
	resetRec := CreateEmptyDecisionRecord()
	if cleanDocID != "" {
		existing := loadRawDecisionRecord(cleanDocID)
		seen := make(map[string]bool)
		var suppressed []string
		addID := func(id string) {
			trimmed := strings.TrimSpace(id)
			if trimmed != "" && !seen[trimmed] {
				seen[trimmed] = true
				suppressed = append(suppressed, trimmed)
			}
		}
		for _, id := range existing.ReconciledThreadIDs {
			addID(id)
		}
		for _, entry := range existing.RecentDecisions {
			addID(entry.SuggestionID)
		}
		for _, id := range additionalSuppressedIDs {
			addID(id)
		}
		resetRec.ReconciledThreadIDs = suppressed
		if len(suppressed) > 0 {
			saveRawDecisionRecord(cleanDocID, resetRec)
		} else if p, err := decisionRecordFilePath(cleanDocID); err == nil {
			_ = os.Remove(p)
		}
	}
	zero := 0
	return BuildClientDecisionRecordSummary(resetRec, &zero, 0)
}

// PurgeDecisionRecordFile completely removes the local Decision Record JSON file when a Google Document is deleted.
func PurgeDecisionRecordFile(docID string) *DecisionRecordSummary {
	decisionMu.Lock()
	defer decisionMu.Unlock()

	cleanDocID := ExtractDocumentID(docID)
	if cleanDocID != "" {
		if p, err := decisionRecordFilePath(cleanDocID); err == nil {
			_ = os.Remove(p)
		}
	}
	zero := 0
	return BuildClientDecisionRecordSummary(CreateEmptyDecisionRecord(), &zero, 0)
}

// LoadDecisionRecordSummary loads the current DecisionRecordSummary for docID without mutating it.
func LoadDecisionRecordSummary(docID string) *DecisionRecordSummary {
	decisionMu.Lock()
	defer decisionMu.Unlock()

	cleanDocID := ExtractDocumentID(docID)
	record := loadRawDecisionRecord(cleanDocID)
	return BuildClientDecisionRecordSummary(record, nil, 0)
}

func decisionRecordFilePath(docID string) (string, error) {
	cleanID := ExtractDocumentID(docID)
	if cleanID == "" {
		return "", os.ErrInvalid
	}
	baseDir := os.Getenv("DOCUMENTOR_CONFIG_DIR")
	if strings.TrimSpace(baseDir) == "" {
		if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
			baseDir = filepath.Join(cfgDir, "gdocumentor")
		} else {
			baseDir = filepath.Join(os.TempDir(), "gdocumentor")
		}
	}
	dir := filepath.Join(baseDir, "decision_records")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, cleanID+".json"), nil
}

func loadRawDecisionRecord(docID string) *RawDecisionRecord {
	empty := CreateEmptyDecisionRecord()
	if docID == "" {
		return empty
	}
	fp, err := decisionRecordFilePath(docID)
	if err != nil {
		return empty
	}
	data, err := os.ReadFile(fp)
	if err != nil || len(data) == 0 {
		return empty
	}
	var parsed RawDecisionRecord
	if err := json.Unmarshal(data, &parsed); err != nil {
		return empty
	}
	parsed.Version = 1
	if len(parsed.Pending) > DecisionMaxPending {
		parsed.Pending = parsed.Pending[:DecisionMaxPending]
	}
	if len(parsed.RecentDecisions) > DecisionMaxRecentLog {
		parsed.RecentDecisions = parsed.RecentDecisions[:DecisionMaxRecentLog]
	}
	return &parsed
}

func saveRawDecisionRecord(docID string, record *RawDecisionRecord) {
	if docID == "" || record == nil {
		return
	}
	fp, err := decisionRecordFilePath(docID)
	if err != nil {
		return
	}
	record.Version = 1
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if len(record.Pending) > DecisionMaxPending {
		record.Pending = record.Pending[len(record.Pending)-DecisionMaxPending:]
	}
	if len(record.RecentDecisions) > DecisionMaxRecentLog {
		record.RecentDecisions = record.RecentDecisions[:DecisionMaxRecentLog]
	}
	if len(record.ReconciledThreadIDs) > 100 {
		record.ReconciledThreadIDs = record.ReconciledThreadIDs[len(record.ReconciledThreadIDs)-100:]
	}

	serialized, err := json.Marshal(record)
	if err != nil {
		return
	}
	for len(serialized) > DecisionSafeJSONBytes && (len(record.RecentDecisions) > 3 || len(record.Pending) > 5) {
		if len(record.RecentDecisions) > 3 {
			record.RecentDecisions = record.RecentDecisions[:len(record.RecentDecisions)-1]
		} else if len(record.Pending) > 5 {
			record.Pending = record.Pending[1:]
		} else {
			break
		}
		serialized, _ = json.Marshal(record)
	}

	_ = os.WriteFile(fp, serialized, 0o600)
}
