package gdocs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tanaikech/gdocumentor/internal/auth"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

func TestParseDriveResourceInput(t *testing.T) {
	c1 := ParseDriveResourceInput("https://docs.google.com/document/d/1AbCdEfGhIjKlMnOpQrStUvWxYz/edit?tab=t.0")
	if c1.ID != "1AbCdEfGhIjKlMnOpQrStUvWxYz" || c1.HintType != "file" {
		t.Fatalf("unexpected doc URL parse: %+v", c1)
	}

	c2 := ParseDriveResourceInput("https://drive.google.com/drive/folders/1FolderIdXYZ_123")
	if c2.ID != "1FolderIdXYZ_123" || c2.HintType != "folder" {
		t.Fatalf("unexpected folder URL parse: %+v", c2)
	}

	c3 := ParseDriveResourceInput("1RawIdString98765")
	if c3.ID != "1RawIdString98765" || c3.HintType != "unknown" {
		t.Fatalf("unexpected raw ID parse: %+v", c3)
	}
}

func TestParseDocumentStructureAndSelectionScopeBoundary(t *testing.T) {
	rawDocJSON := map[string]any{
		"documentId": "doc-123",
		"title":      "Sample Doc",
		"commentAnchors": map[string]any{
			"anchor-in-scope": map[string]any{
				"ranges": []any{
					map[string]any{"startIndex": float64(1), "endIndex": float64(25)},
				},
			},
			"anchor-out-of-scope": map[string]any{
				"ranges": []any{
					map[string]any{"startIndex": float64(35), "endIndex": float64(55)},
				},
			},
		},
		"comments": []any{
			map[string]any{
				"commentId": "com-in",
				"anchorId":  "anchor-in-scope",
				"status":    "OPEN",
				"headPost": map[string]any{
					"content": "In-scope active comment",
					"author":  map[string]any{"displayName": "Alice"},
				},
			},
			map[string]any{
				"commentId": "com-out",
				"anchorId":  "anchor-out-of-scope",
				"status":    "RESOLVED",
				"headPost": map[string]any{
					"content": "Out-of-scope resolved comment",
					"author":  map[string]any{"displayName": "Bob"},
				},
			},
		},
		"tabs": []any{
			map[string]any{
				"tabProperties": map[string]any{
					"tabId": "t.0",
					"title": "Main Tab",
				},
				"documentTab": map[string]any{
					"body": map[string]any{
						"content": []any{
							map[string]any{
								"startIndex": float64(1),
								"endIndex":   float64(30),
								"paragraph": map[string]any{
									"elements": []any{
										map[string]any{
											"startIndex": float64(1),
											"endIndex":   float64(30),
											"textRun": map[string]any{
												"content": "First paragraph in selection\n",
											},
										},
									},
								},
							},
							map[string]any{
								"startIndex": float64(30),
								"endIndex":   float64(62),
								"paragraph": map[string]any{
									"elements": []any{
										map[string]any{
											"startIndex": float64(30),
											"endIndex":   float64(62),
											"textRun": map[string]any{
												"content": "Second paragraph outside scope!\n",
											},
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

	// 1. Full tab parse
	fullParsed := ParseDocumentStructure(rawDocJSON, ParseOptions{PreferredTabID: "t.0"})
	if len(fullParsed.EditableBlocks) != 2 {
		t.Fatalf("expected 2 editable blocks in full tab, got %d", len(fullParsed.EditableBlocks))
	}
	if len(fullParsed.ExistingComments) != 1 || len(fullParsed.ResolvedReviews) != 1 {
		t.Fatalf("expected 1 active and 1 resolved review in full tab, got %d / %d", len(fullParsed.ExistingComments), len(fullParsed.ResolvedReviews))
	}

	// 2. Selection-only parse restricted to [1, 29)
	scopedParsed := ParseDocumentStructure(rawDocJSON, ParseOptions{
		PreferredTabID:  "t.0",
		SelectionOnly:   true,
		SelectionRanges: []utf16idx.RangeInterval{{StartIndex: 1, EndIndex: 29}},
	})
	if len(scopedParsed.EditableBlocks) != 1 || scopedParsed.EditableBlocks[0].Text != "First paragraph in selection" {
		t.Fatalf("expected strictly 1 scoped block, got %+v", scopedParsed.EditableBlocks)
	}
	if len(scopedParsed.ExistingComments) != 1 || scopedParsed.ExistingComments[0].CommentID != "com-in" {
		t.Fatalf("expected in-scope comment com-in retained, got %+v", scopedParsed.ExistingComments)
	}
	if len(scopedParsed.ResolvedReviews) != 0 {
		t.Fatalf("expected out-of-scope resolved comment stripped by Fail-Closed boundary, got %+v", scopedParsed.ResolvedReviews)
	}
}

func TestBuildAndApplySuggestionsEndToEndWithMockServer(t *testing.T) {
	t.Setenv("DOCUMENTOR_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	var batchRequestsReceived []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		batchRequestsReceived = append(batchRequestsReceived, payload)

		// If writeControl.writeMode == "SUGGEST", return suggestionResponses with createdSuggestionIds
		if wc := getMap(payload, "writeControl"); getString(wc, "writeMode") == "SUGGEST" {
			resp := map[string]any{
				"suggestionResponses": []any{
					map[string]any{"createdSuggestionIds": []string{"sug.alpha"}},
					map[string]any{"createdSuggestionIds": []string{"sug.alpha"}},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	authMgr := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	authMgr.SetHTTPClient(srv.Client())
	client := NewClient(authMgr)
	client.SetBaseURLs(srv.URL, srv.URL)

	docData := &ParsedDocument{
		DocumentID:  "doc-xyz",
		ActiveTabID: "t.0",
		EditableBlocks: []utf16idx.EditableBlock{
			{
				Text:           "The most biggest advantage of GAS",
				StartIndex:     10,
				EndIndex:       43,
				Source:         "body",
				TargetEditable: true,
			},
		},
	}

	rawSugs := []utf16idx.RawSuggestion{
		{
			OriginalText:  "most biggest",
			SuggestedText: "biggest",
			Category:      "Grammar",
			Reason:        "Fixes double superlative",
			LearningPoint: "Do not combine 'most' with '-est' adjectives.",
		},
	}

	built := BuildRequestBody(docData, rawSugs, nil, "en", false)
	if built.MatchedTextCount != 1 {
		t.Fatalf("expected 1 matched item, got %d", built.MatchedTextCount)
	}
	if built.MatchedItems[0].StartIndex != 14 || built.MatchedItems[0].EndIndex != 26 {
		t.Fatalf("expected [14, 26), got [%d, %d)", built.MatchedItems[0].StartIndex, built.MatchedItems[0].EndIndex)
	}

	res, err := client.ApplySuggestions(ctx, "doc-xyz", built, "en")
	if err != nil {
		t.Fatalf("ApplySuggestions failed: %v", err)
	}
	if !res.Success || res.AppliedTextSuggestionsCount != 1 {
		t.Fatalf("unexpected ApplyResult: %+v", res)
	}
	if len(batchRequestsReceived) != 2 {
		t.Fatalf("expected 2 batchUpdate calls (1st SUGGEST, 2nd addCommentReply), got %d", len(batchRequestsReceived))
	}
}

func TestCoachDecisionRecordReconciliationAndProvenanceIsolation(t *testing.T) {
	t.Setenv("DOCUMENTOR_CONFIG_DIR", t.TempDir())
	docID := "doc-decision-test-001"
	tabID := "t.0"

	// 1. Register 4 AI-issued suggestions
	drAfterIssue := RegisterIssuedAISuggestions(docID, []IssuedAISuggestionInput{
		{
			SuggestionID:  "sug.accept",
			Category:      "Grammar",
			OriginalText:  "most biggest",
			SuggestedText: "biggest",
			CtxBefore:     "The ",
			CtxAfter:      " advantage of GAS",
			TabID:         tabID,
		},
		{
			SuggestionID:  "sug.edit",
			Category:      "Clarity",
			OriginalText:  "do many various tasks",
			SuggestedText: "automate diverse workflows",
			CtxBefore:     "allows you to ",
			CtxAfter:      " across Workspace.",
			TabID:         tabID,
		},
		{
			SuggestionID:  "sug.reject",
			Category:      "Style",
			OriginalText:  "very easy",
			SuggestedText: "straightforward",
			CtxBefore:     "It is ",
			CtxAfter:      " to start.",
			TabID:         tabID,
		},
		{
			SuggestionID:  "sug.expire",
			Category:      "Structure",
			OriginalText:  "obsolete footer note",
			SuggestedText: "updated footer note",
			CtxBefore:     "Appendix: ",
			CtxAfter:      " [End]",
			TabID:         tabID,
		},
	}, tabID)

	if drAfterIssue.PendingAICount != 4 || drAfterIssue.VerdictState != "AWAITING_REVIEW" {
		t.Fatalf("expected 4 pending AI items in AWAITING_REVIEW state, got %+v", drAfterIssue)
	}

	// 2. Simulate document state after user resolves all 4 AI suggestions:
	// - sug.accept: adopted verbatim ("biggest")
	// - sug.edit: user edited between anchors ("streamline complex workflows")
	// - sug.reject: user kept original ("very easy")
	// - sug.expire: entire paragraph deleted
	// Plus 1 active manual human suggestion ("sug.human.1") and 1 active AI suggestion ("sug.ai.active")
	settledText := "The biggest advantage of GAS allows you to streamline complex workflows across Workspace.\nIt is very easy to start."
	humanSug := &ExistingSuggestion{
		ID:           "sug.human.1",
		DeletedText:  "GAS",
		InsertedText: "Google Apps Script",
		Comments: []ActiveComment{
			{Author: "Colleague", Content: "Spell out acronym on first use"},
		},
	}
	aiActiveSug := &ExistingSuggestion{
		ID:           "sug.ai.active",
		DeletedText:  "start",
		InsertedText: "begin",
		Comments: []ActiveComment{
			{Author: "User", Content: "[DocuMentor AI | Style]\n💡 Reason for Revision:\nMore formal verb."},
		},
	}

	drReconciled := ReconcileDecisionRecordWithTab(ReconcileParams{
		DocumentID:           docID,
		ActiveTabID:          tabID,
		IsTabCompletelyEmpty: false,
		FullTabSettledText:   settledText,
		AllTabSuggestions:    []*ExistingSuggestion{humanSug, aiActiveSug},
		ActiveSuggestionIDs:  []string{"sug.human.1", "sug.ai.active"},
	})

	if humanSug.IsAIGenerated {
		t.Fatalf("expected human suggestion IsAIGenerated=false")
	}
	if !aiActiveSug.IsAIGenerated {
		t.Fatalf("expected AI signature suggestion IsAIGenerated=true")
	}
	if drReconciled.ActiveHumanSuggestionsCount != 1 || drReconciled.PendingAICount != 1 {
		t.Fatalf("expected 1 active human and 1 pending AI suggestion, got human=%d, ai=%d",
			drReconciled.ActiveHumanSuggestionsCount, drReconciled.PendingAICount)
	}

	cum := drReconciled.Cumulative
	if cum.Accepted != 1 || cum.Edited != 1 || cum.Rejected != 1 || cum.Expired != 1 {
		t.Fatalf("expected Accepted=1, Edited=1, Rejected=1, Expired=1; got %+v", cum)
	}
	// Decided = Accepted(1) + Edited(1) + Rejected(1) = 3 (Expired is excluded from denominator!)
	// Constructive = Accepted(1) + Edited(1) = 2 -> ImpactRate = round(2/3 * 100) = 67% -> IMPROVING_DRAFT
	if cum.Decided != 3 || cum.Constructive != 2 || cum.ImpactRate == nil || *cum.ImpactRate != 67 {
		t.Fatalf("expected Decided=3, Constructive=2, ImpactRate=67%%, got %+v", cum)
	}
	if drReconciled.VerdictState != "IMPROVING_DRAFT" {
		t.Fatalf("expected VerdictState=IMPROVING_DRAFT, got %q", drReconciled.VerdictState)
	}

	// 3. Test ResetDecisionRecord on the same Google Document:
	//    - Counters and RecentDecisions reset to 0.
	//    - When continuing to use the SAME Google Document (which still has pre-reset resolved threads in AllResolvedReviews),
	//      the pre-reset resolved threads ("sug.accept", "sug.edit", "sug.reject") MUST NOT resurrect!
	//    - Meanwhile, any still-open active AI suggestion ("sug.ai.active") on the document is hydrated into Pending,
	//      so when the user later accepts it, it is properly tracked as Accepted=1!
	drReset := ResetDecisionRecord(docID)
	if drReset.Cumulative.Issued != 0 || drReset.Cumulative.Decided != 0 || drReset.VerdictState != "NO_DATA" {
		t.Fatalf("expected clean reset record, got %+v", drReset)
	}

	drAfterReset := ReconcileDecisionRecordWithTab(ReconcileParams{
		DocumentID:           docID,
		ActiveTabID:          tabID,
		IsTabCompletelyEmpty: false,
		FullTabSettledText:   settledText,
		AllTabSuggestions:    []*ExistingSuggestion{aiActiveSug},
		ActiveSuggestionIDs:  []string{"sug.ai.active"},
		AllResolvedReviews: []ResolvedReview{
			{
				CommentID:       "c.accept",
				SuggestionID:    "sug.accept",
				ThreadType:      "SUGGESTION",
				ResolutionState: "ACCEPTED",
				Content:         "[DocuMentor AI | Grammar]\n💡 Reason for Revision: Avoid double superlative.",
			},
			{
				CommentID:       "c.reject",
				SuggestionID:    "sug.reject",
				ThreadType:      "SUGGESTION",
				ResolutionState: "REJECTED",
				Content:         "[DocuMentor AI | Style]\n💡 Reason for Revision: Formal phrasing.",
			},
		},
	})
	if drAfterReset.Cumulative.Decided != 0 {
		t.Fatalf("expected pre-reset resolved threads NOT to resurrect after ResetDecisionRecord on same doc, got %+v", drAfterReset.Cumulative)
	}
	if drAfterReset.PendingAICount != 1 || drAfterReset.Cumulative.Issued != 1 {
		t.Fatalf("expected active AI suggestion 'sug.ai.active' to be hydrated after reset (Pending=1, Issued=1), got %+v", drAfterReset)
	}

	// Now user accepts the hydrated suggestion ("start" -> "begin") on the same document
	settledAfterAccept := "The biggest advantage of GAS allows you to streamline complex workflows across Workspace.\nIt is very easy to begin."
	drAfterHydratedAccept := ReconcileDecisionRecordWithTab(ReconcileParams{
		DocumentID:           docID,
		ActiveTabID:          tabID,
		IsTabCompletelyEmpty: false,
		FullTabSettledText:   settledAfterAccept,
		AllTabSuggestions:    nil,
		ActiveSuggestionIDs:  nil,
	})
	if drAfterHydratedAccept.Cumulative.Accepted != 1 || drAfterHydratedAccept.Cumulative.Decided != 1 {
		t.Fatalf("expected hydrated suggestion to reconcile as Accepted=1, Decided=1, got %+v", drAfterHydratedAccept.Cumulative)
	}
}

func TestConvertFileToGoogleDocAndFetchVisualImagesWithAuthFallback(t *testing.T) {
	ctx := context.Background()
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}
	pagelessCalled := false
	deletedDocID := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/upload/drive/v3/files" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       "converted_doc_777",
				"name":     "Converted Markdown Doc",
				"mimeType": "application/vnd.google-apps.document",
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/documents/converted_doc_777:batchUpdate" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			rawJSON, _ := json.Marshal(body)
			if strings.Contains(string(rawJSON), `"documentMode":"PAGELESS"`) {
				pagelessCalled = true
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/drive/v3/files/converted_doc_777" {
			deletedDocID = "converted_doc_777"
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/protected-image.png" {
			if r.Header.Get("Authorization") != "Bearer mock-token" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(pngMagic)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	authMgr := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	authMgr.SetHTTPClient(srv.Client())
	client := NewClient(authMgr)
	client.SetBaseURLs(srv.URL+"/v1", srv.URL+"/drive/v3")

	docID, err := client.ConvertFileToGoogleDoc(ctx, "Converted Markdown Doc", "", "text/markdown", []byte("# Hello\n"), "# Hello\n")
	if err != nil || docID != "converted_doc_777" {
		t.Fatalf("ConvertFileToGoogleDoc failed: id=%q err=%v", docID, err)
	}
	if !pagelessCalled {
		t.Fatalf("expected ConvertFileToGoogleDoc to set documentMode=PAGELESS via SetDocumentPageless")
	}

	visuals := client.FetchVisualImages(ctx, []VisualElement{
		{
			TargetID:   "VISUAL_1",
			ObjectID:   "kix.img1",
			Type:       "Figure/Image",
			Title:      "Test Image",
			ContentURI: srv.URL + "/protected-image.png",
		},
	})
	if len(visuals) != 1 || len(visuals[0].ImageBytes) != len(pngMagic) || visuals[0].MimeType != "image/png" {
		t.Fatalf("expected OAuth fallback image download and PNG magic detection, got mime=%q bytes=%d",
			visuals[0].MimeType, len(visuals[0].ImageBytes))
	}

	if err := client.DeleteDocument(ctx, "converted_doc_777"); err != nil || deletedDocID != "converted_doc_777" {
		t.Fatalf("DeleteDocument failed: deleted=%q err=%v", deletedDocID, err)
	}
}

func TestUpsertDocumentSummaryTabAndManuscriptTabExclusion(t *testing.T) {
	ctx := context.Background()
	summaryTabCreated := false
	deleteRangeCalled := false
	var lastInsertedSummary string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/documents/doc_sum_tab") {
			w.Header().Set("Content-Type", "application/json")
			tabs := []any{
				map[string]any{
					"tabProperties": map[string]any{"tabId": "t.0", "title": "Manuscript Tab"},
					"documentTab": map[string]any{
						"body": map[string]any{
							"content": []any{
								map[string]any{
									"paragraph": map[string]any{
										"elements": []any{
											map[string]any{
												"startIndex": 1,
												"endIndex":   20,
												"textRun":    map[string]any{"content": "Main body content.\n"},
											},
										},
									},
								},
							},
						},
					},
				},
			}
			if summaryTabCreated {
				// Place summary tab FIRST in tabs[] to also verify resolveTargetDocumentTab skips summary tabs
				tabs = append([]any{
					map[string]any{
						"tabProperties": map[string]any{"tabId": "t.sum99", "title": DefaultSummaryTabTitle},
						"documentTab": map[string]any{
							"body": map[string]any{
								"content": []any{
									map[string]any{
										"endIndex": 55,
										"paragraph": map[string]any{
											"elements": []any{
												map[string]any{
													"startIndex": 1,
													"endIndex":   55,
													"textRun":    map[string]any{"content": "🎓 DocuMentor AI — Old summary text that will be replaced.\n"},
												},
											},
										},
									},
								},
							},
						},
					},
				}, tabs...)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId": "doc_sum_tab",
				"title":      "Summary Tab Test Doc",
				"tabs":       tabs,
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/documents/doc_sum_tab:batchUpdate" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			rawBytes, _ := json.Marshal(body)
			rawStr := string(rawBytes)
			w.Header().Set("Content-Type", "application/json")

			if strings.Contains(rawStr, `"addDocumentTab"`) {
				summaryTabCreated = true
				_ = json.NewEncoder(w).Encode(map[string]any{
					"replies": []any{
						map[string]any{
							"addDocumentTab": map[string]any{
								"tabProperties": map[string]any{
									"tabId": "t.sum99",
									"title": DefaultSummaryTabTitle,
								},
							},
						},
					},
				})
				return
			}
			if strings.Contains(rawStr, `"deleteContentRange"`) {
				deleteRangeCalled = true
			}
			for _, rawReq := range getSlice(body, "requests") {
				reqMap, _ := rawReq.(map[string]any)
				if ins := getMap(reqMap, "insertText"); len(ins) > 0 {
					lastInsertedSummary = getString(ins, "text")
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	authMgr := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	authMgr.SetHTTPClient(srv.Client())
	client := NewClient(authMgr)
	client.SetBaseURLs(srv.URL+"/v1", srv.URL+"/drive/v3")

	// 1. First call creates a brand-new Summary Tab ("t.sum99")
	res1, err := client.UpsertDocumentSummaryTab(ctx, "doc_sum_tab", "", "🎓 DocuMentor AI — Run 1 Summary\nBody 1\n", []SummaryTabHeadingRange{
		{StartIndex: 1, EndIndex: 33, NamedStyle: "HEADING_1"},
	})
	if err != nil {
		t.Fatalf("UpsertDocumentSummaryTab (run 1) failed: %v", err)
	}
	if !res1.CreatedNewTab || res1.TabID != "t.sum99" || res1.TabTitle != DefaultSummaryTabTitle {
		t.Fatalf("expected CreatedNewTab=true and TabID=t.sum99, got %+v", res1)
	}
	if deleteRangeCalled {
		t.Fatalf("expected deleteContentRange NOT to be called when creating a new tab")
	}

	// 2. Second call finds the existing Summary Tab by title and updates it in-place
	res2, err := client.UpsertDocumentSummaryTab(ctx, "doc_sum_tab", "", "🎓 DocuMentor AI — Run 2 Updated Summary\nBody 2\n", []SummaryTabHeadingRange{
		{StartIndex: 1, EndIndex: 41, NamedStyle: "HEADING_1"},
	})
	if err != nil {
		t.Fatalf("UpsertDocumentSummaryTab (run 2) failed: %v", err)
	}
	if res2.CreatedNewTab || res2.TabID != "t.sum99" || !deleteRangeCalled || !strings.Contains(lastInsertedSummary, "Run 2 Updated Summary") {
		t.Fatalf("expected in-place update of existing summary tab t.sum99, got res2=%+v deleteRangeCalled=%v lastInserted=%q",
			res2, deleteRangeCalled, lastInsertedSummary)
	}

	// 3. Verify ParseDocumentStructure automatically selects the Manuscript Tab ("t.0") even when Summary Tab is first
	docJSON, err := client.FetchRawDocumentJSON(ctx, "doc_sum_tab")
	if err != nil {
		t.Fatalf("FetchRawDocumentJSON failed: %v", err)
	}
	parsed := ParseDocumentStructure(docJSON, ParseOptions{})
	if parsed.ActiveTabID != "t.0" || parsed.ActiveTabTitle != "Manuscript Tab" {
		t.Fatalf("expected ParseDocumentStructure to skip Summary Tab and select t.0 Manuscript Tab, got %s (%s)",
			parsed.ActiveTabID, parsed.ActiveTabTitle)
	}
	plain := ExtractPlainTextFromDocsJSON(docJSON)
	if strings.Contains(plain, "Old summary text") || !strings.Contains(plain, "Main body content.") {
		t.Fatalf("expected ExtractPlainTextFromDocsJSON to exclude Summary Tab, got: %q", plain)
	}
}

func TestEditDocumentOperations(t *testing.T) {
	ctx := context.Background()
	var batchPayloads []map[string]any
	deletedDocID := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/documents" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"documentId": "doc_edit_created"})
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/drive/v3/files/") {
			deletedDocID = strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/documents/doc_edit_1") {
			w.Header().Set("Content-Type", "application/json")
			// Three paragraphs:
			// #1: "First line\n"  (1..12)
			// #2: "Second line\n" (12..24)
			// #3: "Third line\n"  (24..35)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId": "doc_edit_1",
				"title":      "Editable Test Doc",
				"tabs": []any{
					map[string]any{
						"tabProperties": map[string]any{"tabId": "t.0", "title": "Tab 1"},
						"documentTab": map[string]any{
							"body": map[string]any{
								"content": []any{
									map[string]any{
										"startIndex": 1,
										"endIndex":   12,
										"paragraph": map[string]any{
											"elements": []any{
												map[string]any{
													"startIndex": 1,
													"endIndex":   12,
													"textRun":    map[string]any{"content": "First line\n"},
												},
											},
										},
									},
									map[string]any{
										"startIndex": 12,
										"endIndex":   24,
										"paragraph": map[string]any{
											"elements": []any{
												map[string]any{
													"startIndex": 12,
													"endIndex":   24,
													"textRun":    map[string]any{"content": "Second line\n"},
												},
											},
										},
									},
									map[string]any{
										"startIndex": 24,
										"endIndex":   35,
										"paragraph": map[string]any{
											"elements": []any{
												map[string]any{
													"startIndex": 24,
													"endIndex":   35,
													"textRun":    map[string]any{"content": "Third line\n"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			})
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchUpdate") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			batchPayloads = append(batchPayloads, body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"replies": []any{
					map[string]any{
						"replaceAllText": map[string]any{
							"occurrencesChanged": 2,
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	authMgr := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	authMgr.SetHTTPClient(srv.Client())
	client := NewClient(authMgr)
	client.SetBaseURLs(srv.URL+"/v1", srv.URL+"/drive/v3")

	// 1. Create blank Google Document
	resCreate, err := client.EditDocument(ctx, EditDocumentRequest{
		CreateDoc: true,
		Title:     "Brand New Doc",
	}, false)
	if err != nil {
		t.Fatalf("EditDocument (create) failed: %v", err)
	}
	if !resCreate.CreatedNewDocument || resCreate.DocumentID != "doc_edit_created" {
		t.Fatalf("unexpected create result: %+v", resCreate)
	}

	// 2. Insert at start (prepend), end (append), @12, before "Second line", and after "Second line"
	resPrepend, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:         "doc_edit_1",
		InsertText:    "Header",
		HasInsertText: true,
		Position:      "start",
	}, false)
	if err != nil || len(resPrepend.AffectedRanges) == 0 || resPrepend.AffectedRanges[0].StartIndex != 1 {
		t.Fatalf("expected prepend at index 1, got res=%+v err=%v", resPrepend, err)
	}

	resAppend, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:         "doc_edit_1",
		InsertText:    "Footer",
		HasInsertText: true,
		Position:      "end",
	}, false)
	if err != nil || len(resAppend.AffectedRanges) == 0 || resAppend.AffectedRanges[0].StartIndex != 34 {
		t.Fatalf("expected append at index 34 (maxBodyEnd-1), got res=%+v err=%v", resAppend, err)
	}

	resBefore, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:         "doc_edit_1",
		InsertText:    "[PREFIX] ",
		HasInsertText: true,
		BeforeText:    "Second line",
		MatchCase:     true,
	}, false)
	if err != nil || len(resBefore.AffectedRanges) == 0 || resBefore.AffectedRanges[0].StartIndex != 12 {
		t.Fatalf("expected --before 'Second line' at index 12, got res=%+v err=%v", resBefore, err)
	}

	resAfter, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:         "doc_edit_1",
		InsertText:    " [SUFFIX]",
		HasInsertText: true,
		AfterText:     "Second line",
		MatchCase:     true,
	}, false)
	if err != nil || len(resAfter.AffectedRanges) == 0 || resAfter.AffectedRanges[0].StartIndex != 23 {
		t.Fatalf("expected --after 'Second line' at index 23, got res=%+v err=%v", resAfter, err)
	}

	// 3. Replace text (--find & --replace) and Replace range (--replace-range "2")
	resReplace, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:       "doc_edit_1",
		FindText:    "line",
		ReplaceText: "paragraph",
		HasReplace:  true,
		MatchCase:   true,
	}, false)
	if err != nil || resReplace.AffectedCount != 2 {
		t.Fatalf("expected replaceAllText to report 2 occurrences, got res=%+v err=%v", resReplace, err)
	}

	resReplaceRange, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:        "doc_edit_1",
		ReplaceRange: "2",
		ReplaceText:  "Updated second line",
		HasReplace:   true,
	}, false)
	if err != nil || len(resReplaceRange.AffectedRanges) == 0 || resReplaceRange.AffectedRanges[0].StartIndex != 12 || resReplaceRange.AffectedRanges[0].EndIndex != 23 {
		t.Fatalf("expected replace-range '2' to replace [12..23), got res=%+v err=%v", resReplaceRange, err)
	}

	// 4. Delete paragraph #2 (includes trailing newline [12..24)) and last paragraph #3 (includes preceding newline [23..34) without touching segment-end index 34..35)
	resDelPara2, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:       "doc_edit_1",
		DeleteRange: "2",
	}, false)
	if err != nil || len(resDelPara2.AffectedRanges) == 0 || resDelPara2.AffectedRanges[0].StartIndex != 12 || resDelPara2.AffectedRanges[0].EndIndex != 24 {
		t.Fatalf("expected delete-range '2' to delete [12..24), got res=%+v err=%v", resDelPara2, err)
	}

	resDelPara3, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:       "doc_edit_1",
		DeleteRange: "3",
	}, false)
	if err != nil || len(resDelPara3.AffectedRanges) == 0 || resDelPara3.AffectedRanges[0].StartIndex != 23 || resDelPara3.AffectedRanges[0].EndIndex != 34 {
		t.Fatalf("expected delete-range '3' (last paragraph) to delete [23..34) safely before final segment newline, got res=%+v err=%v", resDelPara3, err)
	}

	// 5. Delete document (--delete-doc)
	resDelDoc, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:     "doc_edit_created",
		DeleteDoc: true,
	}, true)
	if err != nil || !resDelDoc.DeletedDocument || deletedDocID != "doc_edit_created" {
		t.Fatalf("expected delete-doc to delete doc_edit_created, got res=%+v deletedDocID=%q err=%v", resDelDoc, deletedDocID, err)
	}
}

func TestMarkdownConversionAndImageOperations(t *testing.T) {
	// 1. Unit test inline Markdown scanner (preserving snake_case, nested bold links, code, strikethrough, inline images)
	plain, spans, imgs := parseInlineMarkdown("Check `my_snake_var` with **bold**, *italic*, ~~strike~~, [**Link**](https://example.com) and ![Alt](https://example.com/pic.png)")
	if !strings.Contains(plain, "my_snake_var") {
		t.Fatalf("expected snake_case identifier to be preserved, got %q", plain)
	}
	if len(imgs) != 1 || imgs[0].RawSource != "https://example.com/pic.png" || imgs[0].AltText != "Alt" {
		t.Fatalf("expected 1 parsed inline image, got %+v", imgs)
	}
	hasCode, hasBold, hasItalic, hasStrike, hasLink := false, false, false, false, false
	for _, sp := range spans {
		if sp.Code {
			hasCode = true
		}
		if sp.Bold {
			hasBold = true
		}
		if sp.Italic {
			hasItalic = true
		}
		if sp.Strikethrough {
			hasStrike = true
		}
		if sp.LinkURL == "https://example.com" && sp.Bold {
			hasLink = true
		}
	}
	if !hasCode || !hasBold || !hasItalic || !hasStrike || !hasLink {
		t.Fatalf("expected all inline style flags (code=%v bold=%v italic=%v strike=%v boldLink=%v), spans=%+v",
			hasCode, hasBold, hasItalic, hasStrike, hasLink, spans)
	}

	// 2. Test full Markdown conversion + table + image operations against mock Docs/Drive server
	var recordedBatches [][]map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/documents/doc_md_img") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId": "doc_md_img",
				"title":      "Markdown & Image Doc",
				"tabs": []any{
					map[string]any{
						"tabProperties": map[string]any{
							"tabId": "t.0",
							"title": "Main Tab",
						},
						"documentTab": map[string]any{
							"inlineObjects": map[string]any{
								"kix.inline_img_1": map[string]any{
									"inlineObjectProperties": map[string]any{
										"embeddedObject": map[string]any{
											"title": "Diagram 1",
											"imageProperties": map[string]any{
												"contentUri": "https://example.com/old.png",
											},
										},
									},
								},
							},
							"positionedObjects": map[string]any{
								"kix.pos_img_2": map[string]any{
									"positionedObjectProperties": map[string]any{
										"embeddedObject": map[string]any{
											"title": "Positioned Banner",
											"imageProperties": map[string]any{
												"contentUri": "https://example.com/banner.png",
											},
										},
									},
								},
							},
							"body": map[string]any{
								"content": []any{
									map[string]any{
										"startIndex": 1,
										"endIndex":   12,
										"paragraph": map[string]any{
											"positionedObjectIds": []any{"kix.pos_img_2"},
											"elements": []any{
												map[string]any{
													"startIndex": 1,
													"endIndex":   12,
													"textRun":    map[string]any{"content": "First line\n"},
												},
											},
										},
									},
									map[string]any{
										"startIndex": 12,
										"endIndex":   14,
										"paragraph": map[string]any{
											"elements": []any{
												map[string]any{
													"startIndex": 12,
													"endIndex":   13,
													"inlineObjectElement": map[string]any{
														"inlineObjectId": "kix.inline_img_1",
													},
												},
												map[string]any{
													"startIndex": 13,
													"endIndex":   14,
													"textRun":    map[string]any{"content": "\n"},
												},
											},
										},
									},
									map[string]any{
										"startIndex": 14,
										"endIndex":   25,
										"paragraph": map[string]any{
											"elements": []any{
												map[string]any{
													"startIndex": 14,
													"endIndex":   25,
													"textRun":    map[string]any{"content": "Third line\n"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			})
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchUpdate") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			var reqList []map[string]any
			for _, rawReq := range getSlice(body, "requests") {
				if rm, ok := rawReq.(map[string]any); ok {
					reqList = append(reqList, rm)
				}
			}
			recordedBatches = append(recordedBatches, reqList)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"replies": []any{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	authMgr := auth.NewManager(auth.Options{AccessToken: "mock-token"})
	client := NewClient(authMgr)
	client.SetBaseURLs(srv.URL+"/v1", srv.URL+"/drive/v3")
	ctx := context.Background()

	// 2a. Insert rich Markdown (headings, lists, code block, table, inline image) at --after-paragraph 1
	mdInput := strings.Join([]string{
		"## Heading Two",
		"",
		"- Bullet **one**",
		"  - Sub-bullet *two*",
		"",
		"| Col A | Col B |",
		"| :--- | ---: |",
		"| Val 1 | `Val 2` |",
	}, "\n")

	recordedBatches = nil
	resMD, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:          "doc_md_img",
		InsertText:     mdInput,
		HasInsertText:  true,
		Markdown:       true,
		AfterParagraph: 1,
	}, true)
	if err != nil {
		t.Fatalf("EditDocument (Markdown insert) failed: %v", err)
	}
	if resMD.InsertedChars == 0 || len(recordedBatches) < 2 {
		t.Fatalf("expected Markdown with table to execute text+table batches, got batches=%d res=%+v", len(recordedBatches), resMD)
	}

	// 2b. Insert image (--insert-image) with custom width
	recordedBatches = nil
	resInsImg, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:          "doc_md_img",
		InsertImage:    "https://example.com/new_chart.png",
		AfterParagraph: 1,
		ImageWidth:     240,
	}, false)
	if err != nil || len(recordedBatches) != 1 {
		t.Fatalf("EditDocument (--insert-image) failed: res=%+v err=%v", resInsImg, err)
	}

	// 2c. Replace inline image (VISUAL_2 -> kix.inline_img_1) via native replaceImage
	recordedBatches = nil
	resRepImg, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:        "doc_md_img",
		ReplaceImage: "VISUAL_2",
		WithImage:    "https://example.com/replacement.png",
	}, false)
	if err != nil || len(recordedBatches) != 1 || len(getMap(recordedBatches[0][0], "replaceImage")) == 0 {
		t.Fatalf("expected VISUAL_2 replacement to emit replaceImage request, got batches=%+v err=%v", recordedBatches, err)
	}
	_ = resRepImg

	// 2d. Delete inline standalone image (VISUAL_2 at [12..13) with standalone paragraph newline [13..14) -> deletes [12..14))
	recordedBatches = nil
	resDelInline, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:       "doc_md_img",
		DeleteImage: "VISUAL_2",
	}, false)
	if err != nil || len(resDelInline.AffectedRanges) == 0 || resDelInline.AffectedRanges[0].StartIndex != 12 || resDelInline.AffectedRanges[0].EndIndex != 14 {
		t.Fatalf("expected standalone inline image VISUAL_2 deletion to remove [12..14), got res=%+v err=%v", resDelInline, err)
	}

	// 2e. Delete positioned image (VISUAL_1 -> kix.pos_img_2) via deletePositionedObject
	recordedBatches = nil
	_, err = client.EditDocument(ctx, EditDocumentRequest{
		DocID:       "doc_md_img",
		DeleteImage: "VISUAL_1",
	}, false)
	if err != nil || len(recordedBatches) != 1 || len(getMap(recordedBatches[0][0], "deletePositionedObject")) == 0 {
		t.Fatalf("expected positioned image VISUAL_1 deletion to emit deletePositionedObject, got batches=%+v err=%v", recordedBatches, err)
	}

	// 2f. Resize inline image (VISUAL_2 -> kix.inline_img_1) in place WITHOUT WithImage (using existing contentUri)
	recordedBatches = nil
	resResizeImg, err := client.EditDocument(ctx, EditDocumentRequest{
		DocID:        "doc_md_img",
		ReplaceImage: "VISUAL_2",
		ImageWidth:   300,
	}, false)
	if err != nil || len(recordedBatches) != 1 || len(recordedBatches[0]) < 2 {
		t.Fatalf("expected in-place VISUAL_2 resize without WithImage to emit deleteContentRange+insertInlineImage, got batches=%+v err=%v", recordedBatches, err)
	}
	insInline := getMap(recordedBatches[0][1], "insertInlineImage")
	if getString(insInline, "uri") != "https://example.com/old.png" || len(getMap(insInline, "objectSize")) == 0 {
		t.Fatalf("expected insertInlineImage to reuse existing contentUri with objectSize, got %+v", insInline)
	}
	_ = resResizeImg

	// 2g. Resize inline image via ResizeImage alias
	recordedBatches = nil
	_, err = client.EditDocument(ctx, EditDocumentRequest{
		DocID:       "doc_md_img",
		ResizeImage: "VISUAL_2",
		ImageWidth:  250,
	}, false)
	if err != nil || len(recordedBatches) != 1 {
		t.Fatalf("expected ResizeImage alias to succeed, got err=%v", err)
	}

	// 2h. Insert Markdown with BeforeText ("Third line") and verify trailing newline prevents paragraph concatenation
	recordedBatches = nil
	_, err = client.EditDocument(ctx, EditDocumentRequest{
		DocID:         "doc_md_img",
		InsertText:    "### Inserted Heading\n\nInserted paragraph.",
		HasInsertText: true,
		Markdown:      true,
		BeforeText:    "Third line",
		MatchCase:     true,
	}, false)
	if err != nil || len(recordedBatches) != 1 || len(recordedBatches[0]) == 0 {
		t.Fatalf("expected Markdown insert with BeforeText to succeed, got batches=%+v err=%v", recordedBatches, err)
	}
	insMDText := getString(getMap(recordedBatches[0][0], "insertText"), "text")
	if !strings.HasSuffix(insMDText, "\n") {
		t.Fatalf("expected Markdown insert with BeforeText to end with trailing newline so it does not merge into 'Third line', got %q", insMDText)
	}
}


