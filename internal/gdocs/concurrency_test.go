package gdocs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tanaikech/gdocumentor/internal/auth"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

func buildMockTabDocJSON(docID, revID, tabID string, paragraphs []string) map[string]any {
	var bodyContent []any
	cur := 1
	for _, p := range paragraphs {
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
		"title":      "Concurrency Test Doc",
		"tabs": []any{
			map[string]any{
				"tabProperties": map[string]any{"tabId": tabID, "title": "Main Tab"},
				"documentTab": map[string]any{
					"body": map[string]any{
						"content": bodyContent,
					},
				},
			},
		},
	}
}

func TestApplySuggestionsZeroOverheadHappyPathBindsRequiredRevisionID(t *testing.T) {
	t.Setenv("DOCUMENTOR_CONFIG_DIR", t.TempDir())
	ctx := context.Background()

	var docGetCalls int32
	var batchCalls []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			atomic.AddInt32(&docGetCalls, 1)
			_ = json.NewEncoder(w).Encode(buildMockTabDocJSON("doc-happy", "rev_1", "t.0", []string{
				"The most biggest benefit of optimistic revision locking.",
			}))
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		batchCalls = append(batchCalls, payload)

		if wc := getMap(payload, "writeControl"); getString(wc, "writeMode") == "SUGGEST" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"writeControl": map[string]any{"requiredRevisionId": "rev_2"},
				"suggestionResponses": []any{
					map[string]any{"createdSuggestionIds": []string{"sug.1"}},
					map[string]any{"createdSuggestionIds": []string{"sug.1"}},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	authMgr := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	authMgr.SetHTTPClient(srv.Client())
	client := NewClient(authMgr)
	client.SetBaseURLs(srv.URL, srv.URL)

	rawDoc := buildMockTabDocJSON("doc-happy", "rev_1", "t.0", []string{
		"The most biggest benefit of optimistic revision locking.",
	})
	docData := ParseDocumentStructure(rawDoc, ParseOptions{PreferredTabID: "t.0", SkipDecisionReconcile: true})
	built := BuildRequestBody(docData, []utf16idx.RawSuggestion{
		{
			OriginalText:  "most biggest",
			SuggestedText: "biggest",
			Category:      "Grammar",
			Reason:        "Eliminate double superlative.",
			LearningPoint: "Avoid combining 'most' with '-est'.",
		},
	}, nil, "en", false)

	res, err := client.ApplySuggestions(ctx, "doc-happy", built, "en")
	if err != nil {
		t.Fatalf("ApplySuggestions failed: %v", err)
	}
	if atomic.LoadInt32(&docGetCalls) != 0 {
		t.Fatalf("expected ZERO extra documents.get calls on uncontended path, got %d", docGetCalls)
	}
	if len(batchCalls) != 2 {
		t.Fatalf("expected 2 batchUpdate calls, got %d", len(batchCalls))
	}
	wc1 := getMap(batchCalls[0], "writeControl")
	if getString(wc1, "writeMode") != "SUGGEST" || getString(wc1, "requiredRevisionId") != "rev_1" {
		t.Fatalf("expected Phase 1 writeControl={SUGGEST, rev_1}, got %+v", wc1)
	}
	// Coordinate-free addCommentReply MUST NOT carry requiredRevisionId
	if wc2 := getMap(batchCalls[1], "writeControl"); len(wc2) != 0 {
		t.Fatalf("expected addCommentReply batchUpdate to be coordinate-free (no writeControl), got %+v", wc2)
	}
	if res.RevisionControl == nil || res.RevisionControl.SourceRevisionID != "rev_1" || res.RevisionControl.AppliedRevisionID != "rev_2" || res.RevisionControl.RevisionRetries != 0 {
		t.Fatalf("unexpected RevisionControl telemetry: %+v", res.RevisionControl)
	}
}

func TestApplySuggestionsConcurrentEditReanchorsAndSkipsStaleWithoutPollutingDecisionRecord(t *testing.T) {
	t.Setenv("DOCUMENTOR_CONFIG_DIR", t.TempDir())
	ctx := context.Background()

	rev1Paras := []string{
		"First paragraph contains a redundant very unique expression for review.",
		"Second paragraph has an old passive sentence to improve.",
	}
	// Collaborator inserts an emoji banner at top (+2 UTF-16 surrogate pairs) and rewrites paragraph 2!
	rev2Paras := []string{
		"🎉🚀 Collaborator added an announcement header!",
		"First paragraph contains a redundant very unique expression for review.",
		"Second paragraph was already rewritten cleanly by collaborator.",
	}

	var docGetCalls int32
	var suggestAttempt int32
	var committedSuggestPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			atomic.AddInt32(&docGetCalls, 1)
			_ = json.NewEncoder(w).Encode(buildMockTabDocJSON("doc-concurrent", "rev_2", "t.0", rev2Paras))
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		wc := getMap(payload, "writeControl")
		if getString(wc, "writeMode") == "SUGGEST" {
			attempt := atomic.AddInt32(&suggestAttempt, 1)
			if attempt == 1 {
				if getString(wc, "requiredRevisionId") != "rev_1" {
					t.Errorf("attempt 1 expected requiredRevisionId=rev_1, got %+v", wc)
				}
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    400,
						"status":  "FAILED_PRECONDITION",
						"message": "The document has been modified since the specified requiredRevisionId.",
					},
				})
				return
			}
			committedSuggestPayload = payload
			_ = json.NewEncoder(w).Encode(map[string]any{
				"writeControl": map[string]any{"requiredRevisionId": "rev_3"},
				"suggestionResponses": []any{
					map[string]any{"createdSuggestionIds": []string{"sug.reanchored.1"}},
					map[string]any{"createdSuggestionIds": []string{"sug.reanchored.1"}},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	authMgr := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	authMgr.SetHTTPClient(srv.Client())
	client := NewClient(authMgr)
	client.SetBaseURLs(srv.URL, srv.URL)

	docData := ParseDocumentStructure(buildMockTabDocJSON("doc-concurrent", "rev_1", "t.0", rev1Paras), ParseOptions{
		PreferredTabID:        "t.0",
		SkipDecisionReconcile: true,
	})
	built := BuildRequestBody(docData, []utf16idx.RawSuggestion{
		{
			OriginalText:  "very unique expression",
			SuggestedText: "distinctive expression",
			Category:      "Conciseness",
			Reason:        "Remove qualifier.",
			LearningPoint: "'Unique' is absolute.",
		},
		{
			OriginalText:  "an old passive sentence",
			SuggestedText: "a crisp active clause",
			Category:      "Voice",
			Reason:        "Use active voice.",
			LearningPoint: "Prefer active verbs.",
		},
	}, nil, "en", false)

	res, err := client.ApplySuggestions(ctx, "doc-concurrent", built, "en")
	if err != nil {
		t.Fatalf("ApplySuggestions failed after concurrent edit: %v", err)
	}
	if atomic.LoadInt32(&docGetCalls) != 1 {
		t.Fatalf("expected exactly 1 documents.get on single conflict retry, got %d", docGetCalls)
	}
	if res.AppliedTextSuggestionsCount != 1 || len(res.SkippedStaleItems) != 1 {
		t.Fatalf("expected 1 applied and 1 skipped stale item, got applied=%d skipped=%d", res.AppliedTextSuggestionsCount, len(res.SkippedStaleItems))
	}
	if res.SkippedStaleItems[0].AnchorStatus != utf16idx.AnchorStatusStaleSuperseded {
		t.Fatalf("expected stale_superseded on rewritten paragraph, got %+v", res.SkippedStaleItems[0])
	}
	if res.RevisionControl == nil || res.RevisionControl.RevisionRetries != 1 || res.RevisionControl.ReanchoredCount != 1 || res.RevisionControl.StaleSkippedCount != 1 || res.RevisionControl.AppliedRevisionID != "rev_3" {
		t.Fatalf("unexpected RevisionControl summary: %+v", res.RevisionControl)
	}
	// Verify the committed batchUpdate bound to rev_2 and shifted by exact UTF-16 length of emoji header + '\n'
	wc2 := getMap(committedSuggestPayload, "writeControl")
	if getString(wc2, "requiredRevisionId") != "rev_2" {
		t.Fatalf("expected retry batchUpdate bound to rev_2, got %+v", wc2)
	}
	expectedShift := utf16idx.UTF16Len(rev2Paras[0] + "\n")
	if res.AppliedTextItems[0].ShiftDelta != expectedShift {
		t.Fatalf("expected UTF-16 shiftDelta=%d (surrogate-pair safe), got %d", expectedShift, res.AppliedTextItems[0].ShiftDelta)
	}
	// Verify DecisionRecord.Cumulative.Issued counted ONLY the 1 applied suggestion (not the stale_superseded one!)
	if res.DecisionRecord == nil || res.DecisionRecord.Cumulative.Issued != 1 {
		t.Fatalf("expected Cumulative.Issued == 1 (excluding stale_superseded), got %+v", res.DecisionRecord)
	}
	if res.DecisionRecord.LastRun.StaleSkippedCount != 1 || res.DecisionRecord.LastRun.ReanchoredCount != 1 {
		t.Fatalf("expected LastRun stale/reanchor stats recorded, got %+v", res.DecisionRecord.LastRun)
	}
}

func TestApplySuggestionsAllStaleSkipsEmptyBatchUpdateAndHintModeReanchors(t *testing.T) {
	t.Setenv("DOCUMENTOR_CONFIG_DIR", t.TempDir())
	ctx := context.Background()

	// Part 1: All-stale empty-batch guard
	var batchCalls int32
	srvAllStale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(buildMockTabDocJSON("doc-stale", "rev_2", "t.0", []string{
				"Collaborator rewrote the entire document from scratch.",
			}))
			return
		}
		atomic.AddInt32(&batchCalls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": 400, "status": "FAILED_PRECONDITION", "message": "requiredRevisionId mismatch"},
		})
	}))
	defer srvAllStale.Close()

	auth1 := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	auth1.SetHTTPClient(srvAllStale.Client())
	c1 := NewClient(auth1)
	c1.SetBaseURLs(srvAllStale.URL, srvAllStale.URL)

	doc1 := ParseDocumentStructure(buildMockTabDocJSON("doc-stale", "rev_1", "t.0", []string{
		"Original draft sentence with redundant phrasing.",
	}), ParseOptions{PreferredTabID: "t.0", SkipDecisionReconcile: true})
	built1 := BuildRequestBody(doc1, []utf16idx.RawSuggestion{
		{OriginalText: "redundant phrasing", SuggestedText: "clarity"},
	}, nil, "en", false)

	res1, err := c1.ApplySuggestions(ctx, "doc-stale", built1, "en")
	if err != nil {
		t.Fatalf("expected graceful 0-applied completion when all items become stale, got err=%v", err)
	}
	// Must have sent ONLY the initial attempt (1) and NOT a second empty requests: [] batchUpdate!
	if atomic.LoadInt32(&batchCalls) != 1 || res1.AppliedSuggestionsCount != 0 || len(res1.SkippedStaleItems) != 1 {
		t.Fatalf("all-stale guard failed: batchCalls=%d res=%+v", batchCalls, res1)
	}

	// Part 2: Hint-Only mode (--mode hint) binds requiredRevisionId and re-anchors on conflict
	var hintAttempt int32
	var committedHintPayload map[string]any
	srvHint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(buildMockTabDocJSON("doc-hint", "rev_h2", "t.0", []string{
				"Added intro line 🧪!",
				"Consider why global mutable state harms concurrency.",
			}))
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if atomic.AddInt32(&hintAttempt, 1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": 400, "status": "FAILED_PRECONDITION", "message": "revision mismatch"},
			})
			return
		}
		committedHintPayload = payload
		_ = json.NewEncoder(w).Encode(map[string]any{
			"writeControl": map[string]any{"requiredRevisionId": "rev_h3"},
		})
	}))
	defer srvHint.Close()

	auth2 := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	auth2.SetHTTPClient(srvHint.Client())
	c2 := NewClient(auth2)
	c2.SetBaseURLs(srvHint.URL, srvHint.URL)

	docHint := ParseDocumentStructure(buildMockTabDocJSON("doc-hint", "rev_h1", "t.0", []string{
		"Consider why global mutable state harms concurrency.",
	}), ParseOptions{PreferredTabID: "t.0", SkipDecisionReconcile: true})
	builtHint := BuildRequestBody(docHint, []utf16idx.RawSuggestion{
		{
			OriginalText:  "global mutable state",
			Category:      "Architecture",
			Reason:        "Shared state creates race conditions.",
			LearningPoint: "How could dependency injection isolate state?",
		},
	}, nil, "en", true)

	resHint, err := c2.ApplySuggestions(ctx, "doc-hint", builtHint, "en")
	if err != nil {
		t.Fatalf("HintOnly ApplySuggestions failed: %v", err)
	}
	if resHint.AppliedTextSuggestionsCount != 1 || resHint.RevisionControl.RevisionRetries != 1 || resHint.RevisionControl.ReanchoredCount != 1 {
		t.Fatalf("unexpected HintOnly reanchor result: %+v / %+v", resHint, resHint.RevisionControl)
	}
	wcHint := getMap(committedHintPayload, "writeControl")
	if getString(wcHint, "requiredRevisionId") != "rev_h2" || getString(wcHint, "writeMode") != "" {
		t.Fatalf("expected HintOnly writeControl={requiredRevisionId: rev_h2}, got %+v", wcHint)
	}
}

func TestPhase2VisualImmutableObjectIDReanchoringAndEditSuggestNoDestructiveFallback(t *testing.T) {
	t.Setenv("DOCUMENTOR_CONFIG_DIR", t.TempDir())
	ctx := context.Background()

	// Stage 1 doc has kix.original_arch at index 10 as VISUAL_1.
	// Before Phase 2 runs, collaborator inserts a NEW image kix.new_banner at index 2 (which becomes the new VISUAL_1),
	// shifting kix.original_arch down to index 35 (now ordinal VISUAL_2)!
	stage1Doc := map[string]any{
		"documentId": "doc-vis",
		"revisionId": "rev_v1",
		"tabs": []any{
			map[string]any{
				"tabProperties": map[string]any{"tabId": "t.0", "title": "Main"},
				"documentTab": map[string]any{
					"inlineObjects": map[string]any{
						"kix.original_arch": map[string]any{
							"inlineObjectProperties": map[string]any{
								"embeddedObject": map[string]any{"title": "Architecture Diagram"},
							},
						},
					},
					"body": map[string]any{
						"content": []any{
							map[string]any{
								"startIndex": float64(1),
								"endIndex":   float64(12),
								"paragraph": map[string]any{
									"elements": []any{
										map[string]any{
											"startIndex": float64(1),
											"endIndex":   float64(10),
											"textRun":    map[string]any{"content": "See fig: "},
										},
										map[string]any{
											"startIndex":          float64(10),
											"endIndex":            float64(11),
											"inlineObjectElement": map[string]any{"inlineObjectId": "kix.original_arch"},
										},
										map[string]any{
											"startIndex": float64(11),
											"endIndex":   float64(12),
											"textRun":    map[string]any{"content": "\n"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	stage4FreshDoc := map[string]any{
		"documentId": "doc-vis",
		"revisionId": "rev_v2",
		"tabs": []any{
			map[string]any{
				"tabProperties": map[string]any{"tabId": "t.0", "title": "Main"},
				"documentTab": map[string]any{
					"inlineObjects": map[string]any{
						"kix.new_banner": map[string]any{
							"inlineObjectProperties": map[string]any{
								"embeddedObject": map[string]any{"title": "Collaborator Banner"},
							},
						},
						"kix.original_arch": map[string]any{
							"inlineObjectProperties": map[string]any{
								"embeddedObject": map[string]any{"title": "Architecture Diagram"},
							},
						},
					},
					"body": map[string]any{
						"content": []any{
							map[string]any{
								"startIndex": float64(1),
								"endIndex":   float64(26),
								"paragraph": map[string]any{
									"elements": []any{
										map[string]any{
											"startIndex":          float64(1),
											"endIndex":            float64(2),
											"inlineObjectElement": map[string]any{"inlineObjectId": "kix.new_banner"},
										},
										map[string]any{
											"startIndex": float64(2),
											"endIndex":   float64(26),
											"textRun":    map[string]any{"content": "Collaborator intro line\n"},
										},
									},
								},
							},
							map[string]any{
								"startIndex": float64(26),
								"endIndex":   float64(37),
								"paragraph": map[string]any{
									"elements": []any{
										map[string]any{
											"startIndex": float64(26),
											"endIndex":   float64(35),
											"textRun":    map[string]any{"content": "See fig: "},
										},
										map[string]any{
											"startIndex":          float64(35),
											"endIndex":            float64(36),
											"inlineObjectElement": map[string]any{"inlineObjectId": "kix.original_arch"},
										},
										map[string]any{
											"startIndex": float64(36),
											"endIndex":   float64(37),
											"textRun":    map[string]any{"content": "\n"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	var anchoredStartIdx int
	srvVis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(stage4FreshDoc)
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		reqs := getSlice(payload, "requests")
		if len(reqs) > 0 {
			ic := getMap(reqs[0].(map[string]any), "insertComment")
			rng := getMap(ic, "range")
			anchoredStartIdx = getInt(rng, "startIndex")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"writeControl": map[string]any{"requiredRevisionId": "rev_v3"}})
	}))
	defer srvVis.Close()

	authVis := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	authVis.SetHTTPClient(srvVis.Client())
	clientVis := NewClient(authVis)
	clientVis.SetBaseURLs(srvVis.URL, srvVis.URL)

	parsedStage1 := ParseDocumentStructure(stage1Doc, ParseOptions{PreferredTabID: "t.0", SkipDecisionReconcile: true})
	builtVis := BuildRequestBody(parsedStage1, nil, []RawElementSuggestion{
		{
			TargetID:      "VISUAL_1",
			Category:      "Figure Clarity",
			Suggestion:    "Increase axis label font size.",
			Reason:        "Small labels are hard to read on mobile.",
			LearningPoint: "Maintain >= 11pt labels in architecture diagrams.",
		},
	}, "en", false)

	resVis, err := clientVis.ApplySuggestions(ctx, "doc-vis", builtVis, "en")
	if err != nil {
		t.Fatalf("Phase 2 visual ApplySuggestions failed: %v", err)
	}
	if resVis.AppliedElementCommentsCount != 1 || anchoredStartIdx != 35 {
		t.Fatalf("expected VISUAL_1 anchored by immutable ObjectID kix.original_arch at shifted index 35 (NOT ordinal index 1), got anchoredStartIdx=%d res=%+v", anchoredStartIdx, resVis)
	}

	// Part 2: EditDocument with SuggestMode: true MUST retry in SUGGEST mode on revision conflict (NEVER fallback to destructive EDIT mode!)
	var editGetCount int32
	var editBatchPayloads []map[string]any
	srvEdit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			n := atomic.AddInt32(&editGetCount, 1)
			if n == 1 {
				_ = json.NewEncoder(w).Encode(buildMockTabDocJSON("doc-edit", "rev_e1", "t.0", []string{
					"Draft version 0.9 ready for review.",
				}))
				return
			}
			_ = json.NewEncoder(w).Encode(buildMockTabDocJSON("doc-edit", "rev_e2", "t.0", []string{
				"Collaborator header line!",
				"Draft version 0.9 ready for review.",
			}))
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		editBatchPayloads = append(editBatchPayloads, payload)
		if len(editBatchPayloads) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": 400, "status": "FAILED_PRECONDITION", "message": "requiredRevisionId mismatch"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"writeControl": map[string]any{"requiredRevisionId": "rev_e3"}})
	}))
	defer srvEdit.Close()

	authEdit := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	authEdit.SetHTTPClient(srvEdit.Client())
	clientEdit := NewClient(authEdit)
	clientEdit.SetBaseURLs(srvEdit.URL, srvEdit.URL)

	editRes, err := clientEdit.EditDocument(ctx, EditDocumentRequest{
		DocID:       "doc-edit",
		FindText:    "version 0.9",
		ReplaceText: "version 1.1.0",
		HasReplace:  true,
		MatchCase:   true,
		SuggestMode: true,
	}, false)
	if err != nil {
		t.Fatalf("EditDocument --suggest retry failed: %v", err)
	}
	if editRes.WriteMode != "SUGGEST" || len(editBatchPayloads) != 2 {
		t.Fatalf("expected 2 batchUpdate calls ending in SUGGEST mode, got mode=%q calls=%d", editRes.WriteMode, len(editBatchPayloads))
	}
	for idx, p := range editBatchPayloads {
		wc := getMap(p, "writeControl")
		if getString(wc, "writeMode") != "SUGGEST" {
			t.Fatalf("call %d destructively downgraded writeMode from SUGGEST: %+v", idx+1, wc)
		}
	}
	if getString(getMap(editBatchPayloads[1], "writeControl"), "requiredRevisionId") != "rev_e2" {
		t.Fatalf("expected retry call bound to rev_e2, got %+v", getMap(editBatchPayloads[1], "writeControl"))
	}
	if !strings.Contains(editRes.SummaryMessage, "SUGGEST") && editRes.AffectedCount != 1 {
		t.Fatalf("unexpected EditDocumentResult: %+v", editRes)
	}

	// Part 3: ComputeTableSignature ordinal-prefix invariance (TABLE_1 -> TABLE_2 when collaborator inserts a table above)
	sig1 := ComputeTableSignature(TableElement{
		TargetID:  "TABLE_1",
		RowsCount: 2,
		ColsCount: 2,
		Markdown:  "[TABLE_1 (2 rows)]\n| Metric | Latency |\n| --- | --- |\n| P99 | 12ms |",
	})
	sig2 := ComputeTableSignature(TableElement{
		TargetID:  "TABLE_2",
		RowsCount: 2,
		ColsCount: 2,
		Markdown:  "[TABLE_2 (2 rows)]\n| Metric | Latency |\n| --- | --- |\n| P99 | 12ms |",
	})
	if sig1 != sig2 {
		t.Fatalf("expected ComputeTableSignature to remain invariant across ordinal shift TABLE_1 -> TABLE_2, got %q vs %q", sig1, sig2)
	}
}
