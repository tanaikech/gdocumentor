package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tanaikech/gdocumentor/internal/gemini"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

func buildCLIMockDocJSON(docID, revID string, paras []string) map[string]any {
	var bodyContent []any
	cur := 1
	for _, p := range paras {
		line := p + "\n"
		uLen := utf16idx.UTF16Len(line)
		bodyContent = append(bodyContent, map[string]any{
			"startIndex": float64(cur),
			"endIndex":   float64(cur + uLen),
			"paragraph": map[string]any{
				"elements": []any{
					map[string]any{
						"startIndex": float64(cur),
						"endIndex":   float64(cur + uLen),
						"textRun": map[string]any{
							"content": line,
						},
					},
				},
			},
		})
		cur += uLen
	}
	return map[string]any{
		"documentId": docID,
		"revisionId": revID,
		"title":      "MCP Two-Step Concurrency Doc",
		"tabs": []any{
			map[string]any{
				"tabProperties": map[string]any{"tabId": "t.0", "title": "Tab 1"},
				"documentTab": map[string]any{
					"body": map[string]any{"content": bodyContent},
				},
			},
		},
	}
}

func TestTwoStepPrepareOnlyAndConcurrentCollaboratorEditReanchorsAutomatically(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("DOCUMENTOR_CONFIG_DIR", cfgDir)

	// Verify CLI flag parsing of --base-revision-id
	parsedOpts, err := ParseArgs([]string{
		"proofread",
		"-d", "doc_mcp_2step",
		"--skip-doc-insert",
		"--base-revision-id", "rev_step1",
		"--json",
	})
	if err != nil {
		t.Fatalf("ParseArgs --base-revision-id failed: %v", err)
	}
	if parsedOpts.BaseRevisionID != "rev_step1" {
		t.Fatalf("expected BaseRevisionID=rev_step1, got %q", parsedOpts.BaseRevisionID)
	}

	var currentPhase int32 = 1
	var committedRequiredRev string

	phase1Paras := []string{
		"Unscoped header repeating target phrase in module A.",
		"Scoped target section repeating target phrase in module B.",
		"Second scoped sentence that collaborator will rewrite.",
	}
	// While Host AI thinks between Step 1 and Step 2, collaborator:
	// 1. Inserts an emoji callout banner at the very top (shifting everything down in UTF-16)
	// 2. Rewrites the third paragraph completely!
	phase2Paras := []string{
		"🔥🧪 Urgent collaborator callout inserted during Host-AI inference!",
		"Unscoped header repeating target phrase in module A.",
		"Scoped target section repeating target phrase in module B.",
		"Collaborator already rewrote the final sentence cleanly.",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/documents/doc_mcp_2step") {
			if atomic.LoadInt32(&currentPhase) == 1 {
				_ = json.NewEncoder(w).Encode(buildCLIMockDocJSON("doc_mcp_2step", "rev_step1", phase1Paras))
			} else {
				_ = json.NewEncoder(w).Encode(buildCLIMockDocJSON("doc_mcp_2step", "rev_step2", phase2Paras))
			}
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchUpdate") {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if wc, ok := payload["writeControl"].(map[string]any); ok {
				if wm, _ := wc["writeMode"].(string); wm == "SUGGEST" {
					committedRequiredRev, _ = wc["requiredRevisionId"].(string)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"writeControl": map[string]any{"requiredRevisionId": "rev_step3"},
						"suggestionResponses": []any{
							map[string]any{"createdSuggestionIds": []string{"sug_2step_1"}},
							map[string]any{"createdSuggestionIds": []string{"sug_2step_1"}},
						},
					})
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Step 1: Stateless --prepare-only scoped to paragraphs 2-3 on rev_step1
	var stderr1 bytes.Buffer
	out1, err := ExecuteOptions(context.Background(), &Options{
		Command:      "proofread",
		DocID:        "doc_mcp_2step",
		RangeSpec:    "2-3",
		PrepareOnly:  true,
		AccessToken:  "mock-oauth-token",
		JSONOutput:   true,
		DocsBaseURL:  srv.URL + "/v1",
		DriveBaseURL: srv.URL + "/drive/v3",
		WorkDir:      cfgDir,
	}, &stderr1)
	if err != nil {
		t.Fatalf("Step 1 --prepare-only failed: %v", err)
	}
	if out1.RevisionID != "rev_step1" || out1.PreparedContext == nil || out1.PreparedContext.RevisionID != "rev_step1" {
		t.Fatalf("expected RevisionID=rev_step1 in Step 1 output & PreparedContext, got out=%q prep=%+v", out1.RevisionID, out1.PreparedContext)
	}

	// Simulate collaborator concurrent edit before Step 2 runs
	atomic.StoreInt32(&currentPhase, 2)

	aiResultObj := gemini.ProofreadResult{
		OverallFeedback: "Refined scoped module B phrasing.",
		LearningSummary: gemini.LearningSummary{
			RevisionsDigest: "Improved terminology in module B.",
		},
		Suggestions: []utf16idx.RawSuggestion{
			{
				OriginalText:  "target phrase",
				SuggestedText: "canonical phrase",
				Category:      "Terminology",
				Reason:        "Align with glossary.",
				LearningPoint: "Use canonical module terms.",
			},
			{
				OriginalText:  "collaborator will rewrite",
				SuggestedText: "author will refine",
				Category:      "Clarity",
				Reason:        "Tighten verb tense.",
				LearningPoint: "Keep tense consistent.",
			},
		},
	}
	// Step 2: Stateless --skip-doc-insert --ai-result WITHOUT needing to pass --base-revision-id explicitly
	// (automatic prepared_snapshots discovery must detect rev_step1 -> rev_step2, re-anchor module B's duplicate,
	// and skip the rewritten sentence as stale_superseded!).
	var stderr2 bytes.Buffer
	out2, err := ExecuteOptions(context.Background(), &Options{
		Command:       "proofread",
		DocID:         "doc_mcp_2step",
		RangeSpec:     "2-3",
		SkipDocInsert: true,
		AIResult:      &aiResultObj,
		AccessToken:   "mock-oauth-token",
		JSONOutput:    true,
		DocsBaseURL:   srv.URL + "/v1",
		DriveBaseURL:  srv.URL + "/drive/v3",
		WorkDir:       cfgDir,
	}, &stderr2)
	if err != nil {
		t.Fatalf("Step 2 --ai-result failed: %v", err)
	}
	if committedRequiredRev != "rev_step2" {
		t.Fatalf("expected Step 2 batchUpdate bound to rev_step2, got %q", committedRequiredRev)
	}
	if out2.AppliedSuggestionsCount != 1 || len(out2.SkippedStaleSuggestions) != 1 {
		t.Fatalf("expected 1 applied and 1 skipped stale suggestion, got applied=%d skipped=%d", out2.AppliedSuggestionsCount, len(out2.SkippedStaleSuggestions))
	}
	if out2.RevisionControl == nil || out2.RevisionControl.SourceRevisionID != "rev_step1" || out2.RevisionControl.AppliedRevisionID != "rev_step3" || out2.RevisionControl.ReanchoredCount != 1 || out2.RevisionControl.StaleSkippedCount != 1 {
		t.Fatalf("unexpected Step 2 RevisionControl telemetry: %+v", out2.RevisionControl)
	}
	// Crucial check: "target phrase" appears in BOTH module A (paragraph 2 of rev_step2) and module B (paragraph 3 of rev_step2).
	// Re-anchoring from the Step 1 snapshot MUST anchor onto module B (NOT unscoped module A!).
	modAEnd := utf16idx.UTF16Len(phase2Paras[0]+"\n") + utf16idx.UTF16Len(phase2Paras[1]+"\n")
	if len(out2.MatchedSuggestions) != 1 || out2.MatchedSuggestions[0].StartIndex <= modAEnd {
		t.Fatalf("expected re-anchored suggestion in module B (> %d), got %+v", modAEnd, out2.MatchedSuggestions)
	}

	// Verify shrinking-document --range 3-3 case: Step 1 targets paragraph 3-3 on a 3-paragraph doc,
	// then collaborator deletes paragraph 1 (leaving only 2 paragraphs on rev_shrink2) — Step 2 MUST NOT fail with "3-3 is out of bounds"!
	atomic.StoreInt32(&currentPhase, 1)
	_, err = ExecuteOptions(context.Background(), &Options{
		Command:      "proofread",
		DocID:        "doc_mcp_2step",
		RangeSpec:    "2-2",
		PrepareOnly:  true,
		AccessToken:  "mock-oauth-token",
		JSONOutput:   true,
		DocsBaseURL:  srv.URL + "/v1",
		DriveBaseURL: srv.URL + "/drive/v3",
		WorkDir:      cfgDir,
	}, &stderr1)
	if err != nil {
		t.Fatalf("Step 1 --range 2-2 prepare failed: %v", err)
	}
	// Collaborator deletes everything except the single target paragraph (so rev_shrink2 has only 1 paragraph -> --range 2-2 would fail without snapshot bridge!)
	phase2Paras = []string{
		"Scoped target section repeating target phrase in module B.",
	}
	atomic.StoreInt32(&currentPhase, 2)
	outShrink, err := ExecuteOptions(context.Background(), &Options{
		Command:       "proofread",
		DocID:         "doc_mcp_2step",
		RangeSpec:     "2-2",
		SkipDocInsert: true,
		AIResult:      &aiResultObj,
		AccessToken:   "mock-oauth-token",
		JSONOutput:    true,
		DocsBaseURL:   srv.URL + "/v1",
		DriveBaseURL:  srv.URL + "/drive/v3",
		WorkDir:       cfgDir,
	}, &stderr2)
	if err != nil {
		t.Fatalf("Step 2 shrinking --range 2-2 failed instead of re-anchoring via snapshot: %v", err)
	}
	if outShrink.AppliedSuggestionsCount != 1 {
		t.Fatalf("expected 1 applied suggestion after paragraph deletion above --range 2-2, got %+v", outShrink)
	}
}
