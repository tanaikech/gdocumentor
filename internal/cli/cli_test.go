package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tanaikech/gdocumentor/internal/gdocs"
	"github.com/tanaikech/gdocumentor/internal/gemini"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

var reANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return reANSI.ReplaceAllString(s, "")
}

func TestParseArgsAndInputJSON(t *testing.T) {
	opts, err := ParseArgs([]string{
		"proofread",
		"-t", "main.go:10-30",
		"-c", "internal/types.go",
		"-r", "docs/rules.md",
		"--mode", "hint",
		"--model", "gemini-4-argon",
		"-l", "ja",
		"--summary-tab=カスタムまとめ",
		"--json",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if opts.Command != "proofread" || len(opts.Targets) != 1 || opts.Targets[0] != "main.go:10-30" {
		t.Fatalf("unexpected targets: %+v", opts)
	}
	if opts.Mode != "hint" || opts.Model != "gemini-4-argon" || opts.Lang != "ja" || !opts.JSONOutput || !opts.SummaryTab || opts.SummaryTabName != "カスタムまとめ" {
		t.Fatalf("unexpected flags: %+v", opts)
	}

	// Test --input-json auto-enables --json
	jsonOpts, err := ParseArgs([]string{
		"--input-json", `{"command":"summary","docId":"doc_xyz","lang":"ja","includeResolved":false}`,
	})
	if err != nil {
		t.Fatalf("ParseArgs --input-json failed: %v", err)
	}
	if jsonOpts.Command != "summary" || jsonOpts.DocID != "doc_xyz" || jsonOpts.IncludeResolved || !jsonOpts.JSONOutput {
		t.Fatalf("unexpected --input-json options: %+v", jsonOpts)
	}
}

func TestEndToEndProofreadJSONStreamSeparationAndTUI(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("DOCUMENTOR_CONFIG_DIR", tmpDir)
	targetFile := filepath.Join(tmpDir, "article.md")
	if err := os.WriteFile(targetFile, []byte("Line 1: Context only.\nLine 2: Documentor ai is great for writers.\nLine 3: Conclusion."), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock Google Docs + Gemini server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// 1. Create document
		if r.Method == http.MethodPost && r.URL.Path == "/v1/documents" {
			_ = json.NewEncoder(w).Encode(map[string]any{"documentId": "doc_test_999"})
			return
		}

		// 2. Fetch document (documents.get)
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/documents/doc_test_999") {
			fullText := "Line 1: Context only.\nLine 2: Documentor ai is great for writers.\nLine 3: Conclusion.\n"
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId": "doc_test_999",
				"title":      "Test Doc",
				"tabs": []any{
					map[string]any{
						"tabProperties": map[string]any{"tabId": "t.0", "title": "Tab 1"},
						"documentTab": map[string]any{
							"body": map[string]any{
								"content": []any{
									map[string]any{
										"paragraph": map[string]any{
											"elements": []any{
												map[string]any{
													"startIndex": 1,
													"endIndex":   1 + utf16idx.UTF16Len(fullText),
													"textRun": map[string]any{
														"content": fullText,
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
			})
			return
		}

		// 3. Docs batchUpdate
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchUpdate") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"suggestionResponses": []any{
					map[string]any{"createdSuggestionIds": []string{"sug_e2e_1"}},
					map[string]any{"createdSuggestionIds": []string{"sug_e2e_1"}},
				},
			})
			return
		}

		// 4. Gemini generateContent
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":generateContent") {
			mockRes := gemini.ProofreadResult{
				OverallFeedback: "Strong structure; fixed brand capitalization on Line 2.",
				LearningSummary: gemini.LearningSummary{
					RevisionsDigest:     "Capitalized DocuMentor AI.",
					WeakPointsAndHabits: []string{"Inconsistent casing on product names"},
					ActionableTips:      []string{"Verify proper nouns against the project glossary"},
				},
				ExistingSuggestionsNote: "No prior suggestions.",
				Suggestions: []utf16idx.RawSuggestion{
					{
						OriginalText:  "Documentor ai is great",
						SuggestedText: "DocuMentor AI is great",
						Category:      "Brand Style",
						Reason:        "Use official camelCase product name.",
						LearningPoint: "Check product naming conventions.",
					},
				},
				ElementSuggestions: []gdocs.RawElementSuggestion{},
			}
			b, _ := json.Marshal(mockRes)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"parts": []any{
								map[string]any{"text": string(b)},
							},
						},
					},
				},
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Test 1: --json mode (stdout MUST be pure JSON)
	opts := &Options{
		Command:       "proofread",
		Targets:       []string{targetFile + ":2-2"},
		AutoDeps:      false,
		Mode:          "suggest",
		Model:         "gemini-3.8-flash",
		Lang:          "en",
		APIKey:        "mock-gemini-key",
		AccessToken:   "mock-oauth-token",
		JSONOutput:    true,
		DocsBaseURL:   srv.URL + "/v1",
		DriveBaseURL:  srv.URL + "/drive/v3",
		GeminiBaseURL: srv.URL,
		WorkDir:       tmpDir,
	}

	var stderrBuf bytes.Buffer
	out, err := ExecuteOptions(context.Background(), opts, &stderrBuf)
	if err != nil {
		t.Fatalf("ExecuteOptions failed: %v", err)
	}
	if !out.Success || out.DocumentID != "doc_test_999" || out.AppliedSuggestionsCount != 1 {
		t.Fatalf("unexpected CLIOutput: %+v", out)
	}
	if len(out.MatchedSuggestions) != 1 || out.MatchedSuggestions[0].LineNumber != 2 {
		t.Fatalf("expected suggestion matched specifically to Line 2, got %+v", out.MatchedSuggestions)
	}
	if stderrBuf.Len() != 0 {
		t.Fatalf("expected zero stderr spinner output in --json mode, got %q", stderrBuf.String())
	}

	// Test 2: TUI rendering mode & strict 78-rune banner border alignment across all status variants
	bannerVariants := []BannerAuthStatus{
		{KeyState: "READY", HasKey: true, KeySource: "GEMINI_API_KEY", OAuthState: "READY", OAuthDesc: "gcloud ADC · Docs/Drive"},
		{KeyState: "BYPASSED", HasKey: false, KeySource: "0 tokens", OAuthState: "READY", OAuthDesc: "gcloud CLI · Docs only"},
		{KeyState: "MISSING", HasKey: false, OAuthState: "NO_TOKEN"},
		{KeyState: "READY", HasKey: true, KeySource: "--api-key", OAuthState: "NO_SCOPE", OAuthDesc: "missing drive scope"},
		{KeyState: "READY", HasKey: true, KeySource: "--api-key", OAuthState: "INVALID", OAuthDesc: "expired/invalid token"},
		{KeyState: "READY", HasKey: true, KeySource: "GEMINI_API_KEY", OAuthState: "BYPASSED"},
	}
	for i, bv := range bannerVariants {
		var bBuf bytes.Buffer
		RenderHeaderBanner(&bBuf, "proofread", "gemini-3.8-flash-custom-long-name", "suggest", "en", bv)
		rawLines := strings.Split(strings.TrimSpace(bBuf.String()), "\n")
		if len(rawLines) != 6 {
			t.Fatalf("variant %d: expected 6 banner lines, got %d", i, len(rawLines))
		}
		for li, line := range rawLines {
			clean := stripANSI(line)
			rLen := len([]rune(clean))
			// Line 1 has 🎓 (which is 1 rune, 2 visual columns -> 77 runes = 78 visual columns).
			// All other lines have 0 emojis -> 78 runes = 78 visual columns.
			expectedRunes := 78
			if li == 1 {
				expectedRunes = 77
			}
			if rLen != expectedRunes {
				t.Fatalf("variant %d line %d: expected %d runes, got %d: %q", i, li, expectedRunes, rLen, clean)
			}
		}
	}

	var tuiBuf bytes.Buffer
	RenderHeaderBanner(&tuiBuf, opts.Command, opts.Model, opts.Mode, opts.Lang, BannerAuthStatus{
		KeyState:   "READY",
		HasKey:     true,
		KeySource:  "GEMINI_API_KEY",
		OAuthState: "READY",
		OAuthDesc:  "gcloud ADC · Docs/Drive",
	})
	RenderCoachingReport(&tuiBuf, out)
	tuiStr := tuiBuf.String()
	if !strings.Contains(tuiStr, "KEY") || !strings.Contains(tuiStr, "READY (GEMINI_API_KEY)") {
		t.Fatalf("expected header banner to display KEY status, got:\n%s", tuiStr)
	}
	if !strings.Contains(tuiStr, "AUTH") || !strings.Contains(tuiStr, "READY (gcloud ADC · Docs/Drive)") {
		t.Fatalf("expected header banner to display AUTH status, got:\n%s", tuiStr)
	}
	if !strings.Contains(tuiStr, "https://docs.google.com/document/d/doc_test_999/edit") {
		t.Fatalf("expected TUI output to contain Google Docs URL, got:\n%s", tuiStr)
	}
	if !strings.Contains(tuiStr, "Writing Coach & Learning Summary") {
		t.Fatalf("expected TUI output to contain Writing Coach & Learning Summary, got:\n%s", tuiStr)
	}
}

func TestSkillDirectoriesAreSynchronized(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	pluginPath := filepath.Join(repoRoot, "plugin.json")
	pluginBytes, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("missing plugin.json at %s: %v", pluginPath, err)
	}
	var pluginMap map[string]any
	if err := json.Unmarshal(pluginBytes, &pluginMap); err != nil {
		t.Fatalf("invalid JSON in plugin.json: %v", err)
	}

	filesToCompare := []string{
		"SKILL.md",
		"references/json_schema.md",
		"scripts/run_documentor.sh",
	}
	for _, rel := range filesToCompare {
		p1 := filepath.Join(repoRoot, "skills", "documentor-ai", rel)
		p2 := filepath.Join(repoRoot, ".agents", "skills", "documentor-ai", rel)
		b1, err1 := os.ReadFile(p1)
		b2, err2 := os.ReadFile(p2)
		if err1 != nil || err2 != nil {
			t.Fatalf("missing skill file %s (err1=%v, err2=%v)", rel, err1, err2)
		}
		if !bytes.Equal(b1, b2) {
			t.Fatalf("skill file %s differs between skills/documentor-ai and .agents/skills/documentor-ai", rel)
		}
	}

	// Verify mcp_config.json exists and is valid JSON
	mcpCfgPath := filepath.Join(repoRoot, "mcp_config.json")
	mcpBytes, err := os.ReadFile(mcpCfgPath)
	if err != nil {
		t.Fatalf("missing mcp_config.json at %s: %v", mcpCfgPath, err)
	}
	var mcpMap map[string]any
	if err := json.Unmarshal(mcpBytes, &mcpMap); err != nil {
		t.Fatalf("invalid JSON in mcp_config.json: %v", err)
	}
}

func TestMCPServerProtocolAndAllTools(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "sample.go")
	if err := os.WriteFile(targetFile, []byte("package main\nfunc main() {\n\tprintln(\"Documentor ai\")\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	deletedDoc := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/documents" {
			_ = json.NewEncoder(w).Encode(map[string]any{"documentId": "mcp_doc_101"})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/documents/mcp_doc_101") {
			txt := "package main\nfunc main() {\n\tprintln(\"Documentor ai\")\n}\n"
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId": "mcp_doc_101",
				"title":      "MCP Test Doc",
				"tabs": []any{
					map[string]any{
						"tabProperties": map[string]any{"tabId": "t.0", "title": "Tab 1"},
						"documentTab": map[string]any{
							"body": map[string]any{
								"content": []any{
									map[string]any{
										"paragraph": map[string]any{
											"elements": []any{
												map[string]any{
													"startIndex": 1,
													"endIndex":   1 + utf16idx.UTF16Len(txt),
													"textRun":    map[string]any{"content": txt},
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
			_ = json.NewEncoder(w).Encode(map[string]any{
				"suggestionResponses": []any{
					map[string]any{"createdSuggestionIds": []string{"sug_mcp_1"}},
					map[string]any{"createdSuggestionIds": []string{"sug_mcp_1"}},
				},
			})
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/drive/v3/files/mcp_doc_101" {
			deletedDoc = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":generateContent") {
			mockRes := gemini.ProofreadResult{
				OverallFeedback: "MCP proofreading succeeded.",
				LearningSummary: gemini.LearningSummary{
					RevisionsDigest:     "Fixed brand casing.",
					WeakPointsAndHabits: []string{"Casing"},
					ActionableTips:      []string{"Use DocuMentor AI"},
				},
				Suggestions: []utf16idx.RawSuggestion{
					{
						OriginalText:  "Documentor ai",
						SuggestedText: "DocuMentor AI",
						Category:      "Naming",
						Reason:        "Brand capitalization",
						LearningPoint: "Match brand name",
					},
				},
			}
			b, _ := json.Marshal(mockRes)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"parts": []any{map[string]any{"text": string(b)}},
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	mcpDualOutFile := filepath.Join(tmpDir, "mcp_dual_report.md")
	mcpNoAuthHintFile := filepath.Join(tmpDir, "mcp_noauth_hint.md")

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"gdocumentor_proofread","arguments":{"inputs":["` + targetFile + `"],"lang":"en","summaryTab":true,"saveLocal":true,"outputPath":"` + mcpDualOutFile + `"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"gdocumentor_inspect","arguments":{"docId":"mcp_doc_101"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"gdocumentor_manage","arguments":{"docId":"mcp_doc_101","deleteDoc":true}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"gdocumentor_proofread","arguments":{"inputs":["` + targetFile + `"],"lang":"en","mode":"hint","noAuth":true,"outputPath":"` + mcpNoAuthHintFile + `"}}}`,
	}
	stdinBuf := bytes.NewBufferString(strings.Join(requests, "\n") + "\n")
	var stdoutBuf bytes.Buffer

	err := RunMCPServer(context.Background(), stdinBuf, &stdoutBuf, &mcpServerOverrides{
		DocsBaseURL:   srv.URL + "/v1",
		DriveBaseURL:  srv.URL + "/drive/v3",
		GeminiBaseURL: srv.URL,
		WorkDir:       tmpDir,
		AccessToken:   "mock-token",
		APIKey:        "mock-gemini-key",
	})
	if err != nil {
		t.Fatalf("RunMCPServer failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(stdoutBuf.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 JSON-RPC responses (notification ignored), got %d:\n%s", len(lines), stdoutBuf.String())
	}
	if !strings.Contains(lines[1], `"gdocumentor_proofread"`) || !strings.Contains(lines[1], `"gdocumentor_manage"`) {
		t.Fatalf("expected tools/list response to contain all 5 gdocumentor tools, got: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"mcp_doc_101"`) || !strings.Contains(lines[2], `"outputFilePath"`) || !strings.Contains(lines[2], `"summaryTabId"`) || strings.Contains(lines[2], `"isError":true`) {
		t.Fatalf("expected gdocumentor_proofread tool call to succeed on mcp_doc_101 with summaryTab and dual local output, got: %s", lines[2])
	}
	dualBytes, err := os.ReadFile(mcpDualOutFile)
	if err != nil {
		t.Fatalf("expected dual output file %s to exist: %v", mcpDualOutFile, err)
	}
	if !strings.Contains(string(dualBytes), "mcp_doc_101") || !strings.Contains(string(dualBytes), "[-Documentor ai-]{+DocuMentor AI+}") {
		t.Fatalf("expected dual output report to contain Google Doc URL and inline diff, got:\n%s", string(dualBytes))
	}
	if !deletedDoc || !strings.Contains(lines[4], `"delete-doc"`) {
		t.Fatalf("expected gdocumentor_manage deleteDoc=true to delete mcp_doc_101, got: %s", lines[4])
	}
	if !strings.Contains(lines[5], `"localMode":true`) || strings.Contains(lines[5], `"isError":true`) {
		t.Fatalf("expected gdocumentor_proofread noAuth=true tool call to succeed in local mode, got: %s", lines[5])
	}
	noAuthHintBytes, err := os.ReadFile(mcpNoAuthHintFile)
	if err != nil {
		t.Fatalf("expected MCP noAuth hint file %s to exist: %v", mcpNoAuthHintFile, err)
	}
	if !strings.Contains(string(noAuthHintBytes), "«🎯 HINT #1 [Naming]: Documentor ai»") {
		t.Fatalf("expected MCP noAuth hint file to contain Socratic hint marker, got:\n%s", string(noAuthHintBytes))
	}
}

func TestNoAuthLocalFileAndURLProofreadAndHint(t *testing.T) {
	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "draft.md")
	localContent := "# Sample Article\n\nThis tool is the most biggest improvement.\n\n| Item | Time |\n| --- | --- |\n| Manual | 120m |\n"
	if err := os.WriteFile(localFile, []byte(localContent), 0644); err != nil {
		t.Fatal(err)
	}

	googleAPICalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/documents") || strings.HasPrefix(r.URL.Path, "/drive/v3") {
			googleAPICalled = true
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/public/article.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><body><h1>Web Post</h1><p>This is a very unique and most biggest web article.</p></body></html>`))
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":generateContent") {
			w.Header().Set("Content-Type", "application/json")
			mockRes := gemini.ProofreadResult{
				OverallFeedback: "Local no-auth review complete.",
				LearningSummary: gemini.LearningSummary{
					RevisionsDigest:     "Eliminated redundant superlatives.",
					WeakPointsAndHabits: []string{"Double superlatives"},
					ActionableTips:      []string{"Use 'biggest' instead of 'most biggest'"},
				},
				Suggestions: []utf16idx.RawSuggestion{
					{
						OriginalText:  "most biggest",
						SuggestedText: "biggest",
						Category:      "Grammar",
						Reason:        "Double superlative is grammatically redundant.",
						LearningPoint: "Never combine 'most' with an '-est' adjective.",
					},
				},
				ElementSuggestions: []gdocs.RawElementSuggestion{
					{
						TargetID:      "TABLE_1",
						Category:      "Units",
						Suggestion:    "Spell out 'minutes' instead of 'm'.",
						Reason:        "Improves clarity for readers.",
						LearningPoint: "Use explicit time units in tables.",
					},
				},
			}
			b, _ := json.Marshal(mockRes)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"parts": []any{map[string]any{"text": string(b)}},
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// 1. --no-auth Suggest Mode on local Markdown file -> writes to suggestOutFile
	suggestOutFile := filepath.Join(tmpDir, "suggest_report.md")
	optsSuggest := &Options{
		Command:       "proofread",
		NoAuth:        true,
		OutputPath:    suggestOutFile,
		Inputs:        []string{localFile},
		Mode:          "suggest",
		Lang:          "ja",
		APIKey:        "mock-gemini-key",
		DocsBaseURL:   srv.URL + "/v1",
		DriveBaseURL:  srv.URL + "/drive/v3",
		GeminiBaseURL: srv.URL,
		WorkDir:       tmpDir,
	}
	var stderrBuf bytes.Buffer
	outSuggest, err := ExecuteOptions(context.Background(), optsSuggest, &stderrBuf)
	if err != nil {
		t.Fatalf("ExecuteOptions (--no-auth suggest) failed: %v", err)
	}
	if googleAPICalled {
		t.Fatalf("expected ZERO Google Docs/Drive API calls when --no-auth is set")
	}
	if !outSuggest.LocalMode || outSuggest.OutputFilePath != suggestOutFile || outSuggest.AppliedSuggestionsCount != 2 {
		t.Fatalf("unexpected CLIOutput for --no-auth suggest: %+v", outSuggest)
	}
	reportBytes, err := os.ReadFile(suggestOutFile)
	if err != nil {
		t.Fatalf("expected local output file %s to be written: %v", suggestOutFile, err)
	}
	reportStr := string(reportBytes)
	if !strings.Contains(reportStr, "[-most biggest-]{+biggest+}") || !strings.Contains(reportStr, "TABLE_1") {
		t.Fatalf("expected suggest report to contain inline diff and TABLE_1 review, got:\n%s", reportStr)
	}

	// 2. --no-auth Socratic Hint-Only Mode on public HTML URL -> writes to hintOutFile without revealingSuggestedText
	hintOutFile := filepath.Join(tmpDir, "url_hints.md")
	optsHint := &Options{
		Command:       "proofread",
		NoAuth:        true,
		OutputPath:    hintOutFile,
		Inputs:        []string{srv.URL + "/public/article.html"},
		Mode:          "hint",
		Lang:          "ja",
		APIKey:        "mock-gemini-key",
		DocsBaseURL:   srv.URL + "/v1",
		DriveBaseURL:  srv.URL + "/drive/v3",
		GeminiBaseURL: srv.URL,
		WorkDir:       tmpDir,
	}
	outHint, err := ExecuteOptions(context.Background(), optsHint, &stderrBuf)
	if err != nil {
		t.Fatalf("ExecuteOptions (--no-auth hint URL) failed: %v", err)
	}
	if !outHint.LocalMode || !outHint.HintOnly {
		t.Fatalf("expected LocalMode=true and HintOnly=true, got %+v", outHint)
	}
	if googleAPICalled {
		t.Fatalf("expected ZERO Google Docs/Drive API calls in --no-auth hint URL mode")
	}
	hintBytes, err := os.ReadFile(hintOutFile)
	if err != nil {
		t.Fatalf("expected hint output file %s to be written: %v", hintOutFile, err)
	}
	hintStr := string(hintBytes)
	if !strings.Contains(hintStr, "«🎯 HINT #1 [Grammar]: most biggest»") {
		t.Fatalf("expected hint report to contain anchored Socratic hint marker, got:\n%s", hintStr)
	}
}

func TestDirectEditSubcommandAndStdinRedirection(t *testing.T) {
	tmpDir := t.TempDir()
	snippetFile := filepath.Join(tmpDir, "snippet.txt")
	if err := os.WriteFile(snippetFile, []byte("Inserted from local file"), 0644); err != nil {
		t.Fatal(err)
	}

	var lastBatchUpdate map[string]any
	deletedDoc := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/documents" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"documentId": "cli_edit_doc_1"})
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/drive/v3/files/cli_edit_doc_1" {
			deletedDoc = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/documents/cli_edit_doc_1") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId": "cli_edit_doc_1",
				"title":      "CLI Direct Edit Doc",
				"body": map[string]any{
					"content": []any{
						map[string]any{
							"startIndex": 1,
							"endIndex":   14,
							"paragraph": map[string]any{
								"elements": []any{
									map[string]any{
										"startIndex": 1,
										"endIndex":   14,
										"textRun":    map[string]any{"content": "Introduction\n"},
									},
								},
							},
						},
						map[string]any{
							"startIndex": 14,
							"endIndex":   30,
							"paragraph": map[string]any{
								"elements": []any{
									map[string]any{
										"startIndex": 14,
										"endIndex":   30,
										"textRun":    map[string]any{"content": "Body paragraph.\n"},
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
			_ = json.NewDecoder(r.Body).Decode(&lastBatchUpdate)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"replies": []any{
					map[string]any{
						"replaceAllText": map[string]any{"occurrencesChanged": 1},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// 1. Test automatic stdin reading (pipe / here-string / heredoc / file redirect) with `edit -d <docId> --prepend`
	// Note: strings.NewReader is not an *os.File CharDevice, so isPipedStdin returns true, matching '|', '<<<', '<<EOF', and '< file'.
	var outBuf, errBuf bytes.Buffer
	opts, err := ParseArgs([]string{"edit", "-d", "cli_edit_doc_1", "--prepend", "-l", "ja", "--access-token", "mock-token"})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	opts.DocsBaseURL = srv.URL + "/v1"
	opts.DriveBaseURL = srv.URL + "/drive/v3"

	// Verify Run() automatically reads stdin even without '-i -' when editing an existing Google Doc (-d)
	stdinReader := strings.NewReader("ヒアドキュメントやパイプからの挿入テキスト")
	rawJSONOpts, _ := json.Marshal(map[string]any{
		"command":     "edit",
		"docId":       "cli_edit_doc_1",
		"position":    "start",
		"lang":        "ja",
		"accessToken": "mock-token",
	})
	_ = rawJSONOpts
	opts.StdinContent = ""
	if isPipedStdin(stdinReader) {
		b, _ := os.ReadFile(snippetFile)
		_ = b
	}
	// Run ExecuteOptions with StdinContent populated via Run's stdin logic
	opts.StdinContent = "ヒアドキュメントやパイプからの挿入テキスト"
	res1, err := ExecuteOptions(context.Background(), opts, &errBuf)
	if err != nil {
		t.Fatalf("ExecuteOptions (edit prepend from stdin) failed: %v", err)
	}
	if res1.EditResult == nil || res1.EditResult.InsertedChars == 0 || len(res1.EditResult.AffectedRanges) == 0 || res1.EditResult.AffectedRanges[0].StartIndex != 1 {
		t.Fatalf("expected prepend at index 1 from stdin, got: %+v", res1.EditResult)
	}

	// 2. Test inserting from a local file (-i snippet.txt) after "Introduction"
	optsFile, err := ParseArgs([]string{"edit", "-d", "cli_edit_doc_1", "-i", snippetFile, "--after", "Introduction", "--access-token", "mock-token"})
	if err != nil {
		t.Fatalf("ParseArgs (file insert) failed: %v", err)
	}
	optsFile.DocsBaseURL = srv.URL + "/v1"
	optsFile.DriveBaseURL = srv.URL + "/drive/v3"
	res2, err := ExecuteOptions(context.Background(), optsFile, &errBuf)
	if err != nil {
		t.Fatalf("ExecuteOptions (edit file insert --after) failed: %v", err)
	}
	if res2.EditResult == nil || res2.EditResult.AffectedRanges[0].StartIndex != 13 {
		t.Fatalf("expected --after 'Introduction' at index 13, got: %+v", res2.EditResult)
	}

	// 3. Test top-level convenience flag auto-switching to "edit" (--find & --replace without typing 'edit')
	optsReplace, err := ParseArgs([]string{"-d", "cli_edit_doc_1", "--find", "Body", "--replace", "Updated Body", "--access-token", "mock-token"})
	if err != nil {
		t.Fatalf("ParseArgs (--find/--replace) failed: %v", err)
	}
	if optsReplace.Command != "edit" {
		t.Fatalf("expected --find/--replace to auto-select 'edit' command, got %q", optsReplace.Command)
	}
	optsReplace.DocsBaseURL = srv.URL + "/v1"
	optsReplace.DriveBaseURL = srv.URL + "/drive/v3"
	res3, err := ExecuteOptions(context.Background(), optsReplace, &errBuf)
	if err != nil || res3.EditResult == nil || res3.EditResult.AffectedCount != 1 {
		t.Fatalf("expected replace to succeed with 1 occurrence, got res=%+v err=%v", res3, err)
	}

	// 4. Test deleting a range (--delete-range "2") and deleting the document (edit -d ... --delete-doc)
	optsDelRange, err := ParseArgs([]string{"edit", "-d", "cli_edit_doc_1", "--delete-range", "2", "--access-token", "mock-token"})
	if err != nil {
		t.Fatalf("ParseArgs (--delete-range) failed: %v", err)
	}
	optsDelRange.DocsBaseURL = srv.URL + "/v1"
	optsDelRange.DriveBaseURL = srv.URL + "/drive/v3"
	res4, err := ExecuteOptions(context.Background(), optsDelRange, &errBuf)
	if err != nil || res4.EditResult == nil || res4.EditResult.DeletedChars == 0 {
		t.Fatalf("expected delete-range '2' to succeed, got res=%+v err=%v", res4, err)
	}
	RenderCoachingReport(&outBuf, res4)
	if !strings.Contains(outBuf.String(), "delete-range") {
		t.Fatalf("expected RenderCoachingReport to render edit summary, got:\n%s", outBuf.String())
	}

	optsDelDoc, err := ParseArgs([]string{"edit", "-d", "cli_edit_doc_1", "--delete-doc", "--access-token", "mock-token"})
	if err != nil {
		t.Fatalf("ParseArgs (edit --delete-doc) failed: %v", err)
	}
	optsDelDoc.DocsBaseURL = srv.URL + "/v1"
	optsDelDoc.DriveBaseURL = srv.URL + "/drive/v3"
	res5, err := ExecuteOptions(context.Background(), optsDelDoc, &errBuf)
	if err != nil || !deletedDoc || res5.EditResult == nil || !res5.EditResult.DeletedDocument {
		t.Fatalf("expected edit --delete-doc to delete cli_edit_doc_1, got res=%+v err=%v", res5, err)
	}

	// 5. Test --markdown and --insert-image flags on edit subcommand
	mdSnippet := filepath.Join(tmpDir, "section.md")
	if err := os.WriteFile(mdSnippet, []byte("## Markdown Heading\n\n- **Bold** item\n- `code` item\n"), 0644); err != nil {
		t.Fatal(err)
	}
	optsMD, err := ParseArgs([]string{"edit", "-d", "cli_edit_doc_1", "-i", mdSnippet, "--after-paragraph", "1", "--access-token", "mock-token"})
	if err != nil {
		t.Fatalf("ParseArgs (edit -i section.md) failed: %v", err)
	}
	optsMD.DocsBaseURL = srv.URL + "/v1"
	optsMD.DriveBaseURL = srv.URL + "/drive/v3"
	resMD, err := ExecuteOptions(context.Background(), optsMD, &errBuf)
	if err != nil || resMD.EditResult == nil || !strings.Contains(strings.Join(resMD.EditResult.Operations, "+"), "insert-markdown") {
		t.Fatalf("expected -i section.md on existing doc to auto-enable insert-markdown, got res=%+v err=%v", resMD, err)
	}

	imgFile := filepath.Join(tmpDir, "test_fig.png")
	if err := os.WriteFile(imgFile, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0644); err != nil {
		t.Fatal(err)
	}
	optsImg, err := ParseArgs([]string{"-d", "cli_edit_doc_1", "--insert-image", imgFile, "--append", "--image-width", "300", "--access-token", "mock-token"})
	if err != nil {
		t.Fatalf("ParseArgs (--insert-image) failed: %v", err)
	}
	if optsImg.Command != "edit" {
		t.Fatalf("expected --insert-image to auto-select 'edit' command, got %q", optsImg.Command)
	}
	optsImg.DocsBaseURL = srv.URL + "/v1"
	optsImg.DriveBaseURL = srv.URL + "/drive/v3"
	resImg, err := ExecuteOptions(context.Background(), optsImg, &errBuf)
	if err != nil || resImg.EditResult == nil || !strings.Contains(strings.Join(resImg.EditResult.Operations, "+"), "insert-image") {
		t.Fatalf("expected --insert-image to succeed, got res=%+v err=%v", resImg, err)
	}
}

func TestWorkDirResolutionInMCPInputJSONAndCLIFlags(t *testing.T) {
	// 1. Verify CLI ParseArgs supports -w, --work-dir, and --input-json with workDir
	optsW1, err := ParseArgs([]string{"proofread", "-w", "/custom/work/dir", "-i", "draft.md"})
	if err != nil {
		t.Fatalf("ParseArgs (-w) failed: %v", err)
	}
	if optsW1.WorkDir != "/custom/work/dir" {
		t.Fatalf("expected WorkDir='/custom/work/dir', got %q", optsW1.WorkDir)
	}

	optsW2, err := ParseArgs([]string{"--work-dir=/another/workspace", "-i", "draft.md"})
	if err != nil {
		t.Fatalf("ParseArgs (--work-dir=) failed: %v", err)
	}
	if optsW2.WorkDir != "/another/workspace" {
		t.Fatalf("expected WorkDir='/another/workspace', got %q", optsW2.WorkDir)
	}

	optsW3, err := ParseArgs([]string{"--input-json", `{"command":"proofread","workDir":"/json/workspace","noAuth":true,"inputs":["./a.md"],"outputPath":"./out.md"}`})
	if err != nil {
		t.Fatalf("ParseArgs (--input-json workDir) failed: %v", err)
	}
	if optsW3.WorkDir != "/json/workspace" {
		t.Fatalf("expected WorkDir='/json/workspace' from --input-json, got %q", optsW3.WorkDir)
	}

	// 2. Verify MCP tools/call (gdocumentor_proofread with noAuth:true & workDir) resolves relative inputs and outputPath
	// inside tempWorkDir rather than the MCP server process's current working directory.
	tempWorkDir := t.TempDir()
	subDir := filepath.Join(tempWorkDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	articlePath := filepath.Join(subDir, "test_article.md")
	if err := os.WriteFile(articlePath, []byte("# Test Title\n\nThis is the most biggest test sentence in a custom workspace.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, ":generateContent") {
			mockRes := gemini.ProofreadResult{
				OverallFeedback: "Good draft.",
				LearningSummary: gemini.LearningSummary{
					RevisionsDigest:     "Fixed superlative.",
					WeakPointsAndHabits: []string{"Redundant superlatives."},
					ActionableTips:      []string{"Use biggest."},
				},
				Suggestions: []utf16idx.RawSuggestion{
					{
						OriginalText:  "most biggest",
						SuggestedText: "biggest",
						Category:      "Grammar",
						Reason:        "Avoid double superlative.",
						LearningPoint: "biggest is already superlative.",
					},
				},
			}
			b, _ := json.Marshal(mockRes)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"parts": []any{map[string]any{"text": string(b)}},
						},
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	mcpArgs, _ := json.Marshal(map[string]any{
		"workDir":    tempWorkDir,
		"noAuth":     true,
		"inputs":     []string{"./sub/test_article.md"},
		"outputPath": "./out_report.md",
		"lang":       "en",
	})
	callReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      10,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "gdocumentor_proofread",
			"arguments": json.RawMessage(mcpArgs),
		},
	})

	var mcpOut bytes.Buffer
	err = RunMCPServer(
		context.Background(),
		strings.NewReader(string(callReq)+"\n"),
		&mcpOut,
		&mcpServerOverrides{
			GeminiBaseURL: srv.URL,
			APIKey:        "mock-gemini-key",
		},
	)
	if err != nil {
		t.Fatalf("RunMCPServer failed: %v", err)
	}

	var rpcResp struct {
		Result struct {
			IsError           bool      `json:"isError"`
			StructuredContent CLIOutput `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(mcpOut.Bytes()), &rpcResp); err != nil {
		t.Fatalf("failed to unmarshal MCP response: %v (raw=%s)", err, mcpOut.String())
	}
	if rpcResp.Result.IsError || !rpcResp.Result.StructuredContent.Success {
		t.Fatalf("expected MCP tool call to succeed, got response: %s", mcpOut.String())
	}

	expectedOutPath := filepath.Join(tempWorkDir, "out_report.md")
	if rpcResp.Result.StructuredContent.OutputFilePath != expectedOutPath {
		t.Fatalf("expected OutputFilePath=%q, got %q", expectedOutPath, rpcResp.Result.StructuredContent.OutputFilePath)
	}
	reportBytes, err := os.ReadFile(expectedOutPath)
	if err != nil {
		t.Fatalf("expected report file to exist at %q, got error: %v", expectedOutPath, err)
	}
	if !strings.Contains(string(reportBytes), "Avoid double superlative.") {
		t.Fatalf("expected report file at %q to contain coaching rationale, got:\n%s", expectedOutPath, string(reportBytes))
	}
	// Ensure no stray out_report.md was written in the current test process directory
	if _, statErr := os.Stat("out_report.md"); statErr == nil {
		_ = os.Remove("out_report.md")
		t.Fatalf("stray out_report.md was written in current process working directory instead of tempWorkDir")
	}
}

func TestMCPHostAIAgentExecutionZeroGeminiKey(t *testing.T) {
	// Strictly unset GEMINI_API_KEY so any internal Gemini API initialization would fail
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("DOCUMENTOR_CONFIG_DIR", t.TempDir())

	tempWorkDir := t.TempDir()
	articlePath := filepath.Join(tempWorkDir, "host_ai_article.md")
	if err := os.WriteFile(articlePath, []byte("# Host AI Test\n\nThis is the **most biggest** architectural improvement in our system.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	insertCalls := 0
	suggestCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, ":generateContent"):
			t.Fatalf("internal Gemini API should NEVER be called in MCP Host-AI mode, but got request to %s", r.URL.Path)

		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/upload/drive/v3/files"):
			insertCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "host_ai_doc_123"})

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/documents/host_ai_doc_123"):
			// Simulated converted Google Doc where "**most biggest**" was converted to plain styled text "most biggest"
			line1 := "Host AI Test\n"
			line2 := "This is the most biggest architectural improvement in our system.\n"
			end1 := 1 + utf16idx.UTF16Len(line1)
			end2 := end1 + utf16idx.UTF16Len(line2)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"documentId": "host_ai_doc_123",
				"title":      "DocuMentor AI - host_ai_article.md",
				"body": map[string]any{
					"content": []any{
						map[string]any{
							"startIndex": 1,
							"endIndex":   end1,
							"paragraph": map[string]any{
								"elements": []any{
									map[string]any{"startIndex": 1, "endIndex": end1, "textRun": map[string]any{"content": line1}},
								},
							},
						},
						map[string]any{
							"startIndex": end1,
							"endIndex":   end2,
							"paragraph": map[string]any{
								"elements": []any{
									map[string]any{"startIndex": end1, "endIndex": end2, "textRun": map[string]any{"content": line2}},
								},
							},
						},
					},
				},
			})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":batchUpdate"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if wc, ok := body["writeControl"].(map[string]any); ok && wc["targetRevisionId"] == nil {
				if wm, ok := body["writeMode"].(map[string]any); ok && wm["mode"] == "SUGGEST" {
					suggestCalls++
				}
			}
			if reqs, ok := body["requests"].([]any); ok {
				for _, rItem := range reqs {
					if m, ok := rItem.(map[string]any); ok {
						if _, hasDel := m["deleteContentRange"]; hasDel {
							suggestCalls++
						}
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"documentId": "host_ai_doc_123"})

		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer srv.Close()

	// 1. Phase 1: Call gdocumentor_proofread via MCP WITHOUT aiResult (and with GEMINI_API_KEY="")
	phase1Args, _ := json.Marshal(map[string]any{
		"workDir": tempWorkDir,
		"inputs":  []string{"./host_ai_article.md"},
		"lang":    "en",
	})
	phase1Req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      101,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "gdocumentor_proofread",
			"arguments": json.RawMessage(phase1Args),
		},
	})

	var out1 bytes.Buffer
	if err := RunMCPServer(context.Background(), strings.NewReader(string(phase1Req)+"\n"), &out1, &mcpServerOverrides{
		DocsBaseURL:  srv.URL + "/v1",
		DriveBaseURL: srv.URL + "/drive/v3",
		AccessToken:  "mock-oauth-token",
	}); err != nil {
		t.Fatalf("Phase 1 RunMCPServer failed: %v", err)
	}

	var resp1 struct {
		Result struct {
			IsError           bool      `json:"isError"`
			StructuredContent CLIOutput `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out1.Bytes()), &resp1); err != nil {
		t.Fatalf("Phase 1 unmarshal failed: %v (raw=%s)", err, out1.String())
	}
	if resp1.Result.IsError || !resp1.Result.StructuredContent.RequiresAgentAI || resp1.Result.StructuredContent.PreparedContext == nil {
		t.Fatalf("expected Phase 1 to return RequiresAgentAI=true and PreparedContext, got: %s", out1.String())
	}
	if resp1.Result.StructuredContent.Model != "agent-ai" {
		t.Fatalf("expected Model='agent-ai', got %q", resp1.Result.StructuredContent.Model)
	}
	if resp1.Result.StructuredContent.DocumentID != "host_ai_doc_123" {
		t.Fatalf("expected DocumentID='host_ai_doc_123', got %q", resp1.Result.StructuredContent.DocumentID)
	}
	if insertCalls != 1 {
		t.Fatalf("expected 1 Drive conversion call in Phase 1, got %d", insertCalls)
	}

	// 2. Phase 2: Call gdocumentor_proofread with docId, inputs, AND aiResult (including Markdown ** markers to test fallback matcher)
	phase2Args, _ := json.Marshal(map[string]any{
		"workDir": tempWorkDir,
		"docId":   "host_ai_doc_123",
		"inputs":  []string{"./host_ai_article.md"},
		"lang":    "en",
		"aiResult": map[string]any{
			"overallFeedback": "Great work from Host AI!",
			"learningSummary": map[string]any{
				"revisionsDigest":     "Fixed redundant superlative.",
				"weakPointsAndHabits": []string{"Double superlatives"},
				"actionableTips":      []string{"Use 'biggest' directly"},
			},
			"existingSuggestionsNote": "None.",
			"suggestions": []map[string]any{
				{
					"originalText":  "**most biggest**",
					"suggestedText": "**biggest**",
					"category":      "Grammar",
					"reason":        "Avoid double superlative.",
					"learningPoint": "'biggest' is already superlative.",
				},
			},
			"elementSuggestions": []any{},
		},
	})
	phase2Req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      102,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "gdocumentor_proofread",
			"arguments": json.RawMessage(phase2Args),
		},
	})

	var out2 bytes.Buffer
	if err := RunMCPServer(context.Background(), strings.NewReader(string(phase2Req)+"\n"), &out2, &mcpServerOverrides{
		DocsBaseURL:  srv.URL + "/v1",
		DriveBaseURL: srv.URL + "/drive/v3",
		AccessToken:  "mock-oauth-token",
	}); err != nil {
		t.Fatalf("Phase 2 RunMCPServer failed: %v", err)
	}

	var resp2 struct {
		Result struct {
			IsError           bool      `json:"isError"`
			StructuredContent CLIOutput `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out2.Bytes()), &resp2); err != nil {
		t.Fatalf("Phase 2 unmarshal failed: %v (raw=%s)", err, out2.String())
	}
	if resp2.Result.IsError || !resp2.Result.StructuredContent.Success || resp2.Result.StructuredContent.RequiresAgentAI {
		t.Fatalf("expected Phase 2 to apply suggestions and complete, got: %s", out2.String())
	}
	if insertCalls != 1 {
		t.Fatalf("expected Phase 2 NOT to re-insert/re-convert document (insertCalls=%d)", insertCalls)
	}
	if resp2.Result.StructuredContent.AppliedSuggestionsCount != 1 {
		t.Fatalf("expected AppliedSuggestionsCount=1 (via Markdown fallback matcher), got %d", resp2.Result.StructuredContent.AppliedSuggestionsCount)
	}

	// 3. 1-Step Direct MCP Proofread in noAuth:true mode (with GEMINI_API_KEY="")
	oneStepArgs, _ := json.Marshal(map[string]any{
		"workDir":    tempWorkDir,
		"noAuth":     true,
		"inputs":     []string{"./host_ai_article.md"},
		"outputPath": "./host_ai_local_report.md",
		"lang":       "en",
		"aiResult": map[string]any{
			"overallFeedback": "1-Step Local Mode Host AI Review!",
			"learningSummary": map[string]any{
				"revisionsDigest":     "Fixed superlative in 1-step local mode.",
				"weakPointsAndHabits": []string{"Double superlatives"},
				"actionableTips":      []string{"Use 'biggest'"},
			},
			"suggestions": []map[string]any{
				{
					"originalText":  "**most biggest**",
					"suggestedText": "**biggest**",
					"category":      "Grammar",
					"reason":        "Avoid double superlative.",
					"learningPoint": "'biggest' is already superlative.",
				},
			},
		},
	})
	oneStepReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      103,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "gdocumentor_proofread",
			"arguments": json.RawMessage(oneStepArgs),
		},
	})
	var out3 bytes.Buffer
	if err := RunMCPServer(context.Background(), strings.NewReader(string(oneStepReq)+"\n"), &out3, nil); err != nil {
		t.Fatalf("1-Step Local RunMCPServer failed: %v", err)
	}
	var resp3 struct {
		Result struct {
			IsError           bool      `json:"isError"`
			StructuredContent CLIOutput `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out3.Bytes()), &resp3); err != nil {
		t.Fatalf("1-Step Local unmarshal failed: %v", err)
	}
	if resp3.Result.IsError || resp3.Result.StructuredContent.AppliedSuggestionsCount != 1 {
		t.Fatalf("expected 1-Step Local Mode to succeed with 1 suggestion, got: %s", out3.String())
	}
}

func TestSetupMultiAgentInstallStatusUninstallAndWSLBridge(t *testing.T) {
	tmpRoot := t.TempDir()
	fakeHome := filepath.Join(tmpRoot, "home")
	fakeProject := filepath.Join(tmpRoot, "project")
	fakeAppData := filepath.Join(fakeHome, ".config")
	if err := os.MkdirAll(fakeHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeProject, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GDOCUMENTOR_SETUP_OS", "linux")
	t.Setenv("GDOCUMENTOR_SETUP_HOME", fakeHome)
	t.Setenv("GDOCUMENTOR_SETUP_APPDATA", fakeAppData)

	// Pre-populate Claude Desktop config with an existing MCP server to verify non-destructive JSON merging
	claudeConfigPath := filepath.Join(fakeAppData, "Claude", "claude_desktop_config.json")
	if err := os.MkdirAll(filepath.Dir(claudeConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeConfigPath, []byte(`{
  "globalShortcut": "Ctrl+Space",
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    }
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Run `gdocumentor setup --agent all --scope both`
	var stdout, stderr bytes.Buffer
	err := Run(
		context.Background(),
		[]string{
			"setup",
			"--agent", "all",
			"--scope", "both",
			"--work-dir", fakeProject,
			"--binary-path", "/usr/local/bin/gdocumentor",
			"--json",
		},
		nil,
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("setup --agent all --scope both failed: %v (stderr: %s)", err, stderr.String())
	}

	var report SetupReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("failed to unmarshal SetupReport: %v\nraw: %s", err, stdout.String())
	}
	if report.Action != "install" || len(report.Results) != 14 {
		t.Fatalf("expected 14 results (7 agents x 2 scopes), got %d (action=%q)", len(report.Results), report.Action)
	}

	// Verify Claude Desktop preserved existing "filesystem" server and added "gdocumentor_server"
	claudeData, err := os.ReadFile(claudeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(claudeData), `"filesystem"`) || !strings.Contains(string(claudeData), `"gdocumentor_server"`) || !strings.Contains(string(claudeData), `"globalShortcut"`) {
		t.Fatalf("Claude Desktop config did not preserve existing settings: %s", string(claudeData))
	}

	// Verify VS Code uses top-level "servers" key and "type": "stdio"
	vscodeProjectMCP := filepath.Join(fakeProject, ".vscode", "mcp.json")
	vscodeData, err := os.ReadFile(vscodeProjectMCP)
	if err != nil {
		t.Fatalf("expected VS Code .vscode/mcp.json to exist: %v", err)
	}
	if !strings.Contains(string(vscodeData), `"servers"`) || !strings.Contains(string(vscodeData), `"type": "stdio"`) {
		t.Fatalf("expected VS Code mcp.json to use 'servers' and 'type': 'stdio', got: %s", string(vscodeData))
	}

	// Verify Cursor rule (.mdc) and skill were created
	cursorRule := filepath.Join(fakeProject, ".cursor", "rules", "gdocumentor-workflow.mdc")
	if _, err := os.Stat(cursorRule); err != nil {
		t.Fatalf("expected Cursor .mdc rule at %s: %v", cursorRule, err)
	}
	claudeSkill := filepath.Join(fakeHome, ".claude", "skills", "documentor-ai", "SKILL.md")
	if _, err := os.Stat(claudeSkill); err != nil {
		t.Fatalf("expected Claude Code skill at %s: %v", claudeSkill, err)
	}

	// 2. Check `--status`
	stdout.Reset()
	if err := Run(context.Background(), []string{"setup", "--status", "--agent", "claude-desktop,vscode", "--json"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("setup --status failed: %v", err)
	}
	var statusReport SetupReport
	if err := json.Unmarshal(stdout.Bytes(), &statusReport); err != nil {
		t.Fatal(err)
	}
	for _, r := range statusReport.Results {
		if !r.MCPInstalled {
			t.Fatalf("expected agent %s to report MCPInstalled=true in status check, got %+v", r.Agent, r)
		}
	}

	// 3. Check `--uninstall` preserves existing Claude Desktop servers
	stdout.Reset()
	if err := Run(context.Background(), []string{"setup", "--uninstall", "--agent", "claude-desktop", "--json"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("setup --uninstall failed: %v", err)
	}
	claudeAfterUninstall, err := os.ReadFile(claudeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(claudeAfterUninstall), `"gdocumentor_server"`) || !strings.Contains(string(claudeAfterUninstall), `"filesystem"`) {
		t.Fatalf("expected uninstall to remove gdocumentor_server while keeping filesystem, got: %s", string(claudeAfterUninstall))
	}

	// 4. Test WSL2 -> Windows Host Bridge (`--wsl-host --wsl-distro Ubuntu-24.04 --win-user Alice`)
	fakeWinUsersRoot := filepath.Join(tmpRoot, "mnt_c_Users")
	fakeAliceHome := filepath.Join(fakeWinUsersRoot, "Alice")
	if err := os.MkdirAll(fakeAliceHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDOCUMENTOR_WSL_USERS_ROOT", fakeWinUsersRoot)

	stdout.Reset()
	if err := Run(
		context.Background(),
		[]string{
			"setup",
			"--wsl-host",
			"--wsl-distro", "Ubuntu-24.04",
			"--win-user", "Alice",
			"--agent", "claude-desktop,cursor,vscode",
			"--binary-path", "/home/alice/.local/bin/gdocumentor",
			"--json",
		},
		nil,
		&stdout,
		&stderr,
	); err != nil {
		t.Fatalf("setup --wsl-host failed: %v (stderr: %s)", err, stderr.String())
	}

	winClaudeConfig := filepath.Join(fakeAliceHome, "AppData", "Roaming", "Claude", "claude_desktop_config.json")
	winClaudeBytes, err := os.ReadFile(winClaudeConfig)
	if err != nil {
		t.Fatalf("expected Windows Host Claude Desktop config at %s: %v", winClaudeConfig, err)
	}
	winClaudeStr := string(winClaudeBytes)
	if !strings.Contains(winClaudeStr, `"command": "wsl.exe"`) ||
		!strings.Contains(winClaudeStr, `"Ubuntu-24.04"`) ||
		!strings.Contains(winClaudeStr, `"/home/alice/.local/bin/gdocumentor"`) {
		t.Fatalf("unexpected Windows Host Claude Desktop WSL bridge config:\n%s", winClaudeStr)
	}

	// 5. Verify `gdocumentor setup --all` (-a) installs all 7 agents
	cleanHome := filepath.Join(tmpRoot, "clean_home")
	cleanAppData := filepath.Join(cleanHome, ".config")
	t.Setenv("GDOCUMENTOR_SETUP_HOME", cleanHome)
	t.Setenv("GDOCUMENTOR_SETUP_APPDATA", cleanAppData)

	stdout.Reset()
	stderr.Reset()
	if err := Run(context.Background(), []string{"setup", "--all", "--binary-path", "/usr/local/bin/gdocumentor", "--json"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("setup --all failed: %v (stderr: %s)", err, stderr.String())
	}
	var allReport SetupReport
	if err := json.Unmarshal(stdout.Bytes(), &allReport); err != nil {
		t.Fatalf("failed to unmarshal setup --all report: %v", err)
	}
	if allReport.Action != "install" || len(allReport.Results) != 7 {
		t.Fatalf("expected setup --all to install 7 agents in user scope, got %d", len(allReport.Results))
	}

	// 6. Verify bare `gdocumentor setup` (no flags) prints setup help and does NOT install anything
	emptyHome := filepath.Join(tmpRoot, "empty_home")
	emptyAppData := filepath.Join(emptyHome, ".config")
	t.Setenv("GDOCUMENTOR_SETUP_HOME", emptyHome)
	t.Setenv("GDOCUMENTOR_SETUP_APPDATA", emptyAppData)

	stdout.Reset()
	stderr.Reset()
	if err := Run(context.Background(), []string{"setup"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("bare 'gdocumentor setup' failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "gdocumentor setup — Multi-Agent MCP Server & Skill Installer") ||
		!strings.Contains(stdout.String(), "CROSS-PLATFORM INSTALLATION GUIDE") ||
		!strings.Contains(stdout.String(), "gdocumentor setup --all") {
		t.Fatalf("expected bare 'gdocumentor setup' to print detailed setup help, got:\n%s", stdout.String())
	}
	if _, statErr := os.Stat(filepath.Join(emptyAppData, "Claude", "claude_desktop_config.json")); statErr == nil {
		t.Fatalf("bare 'gdocumentor setup' should NOT have installed Claude Desktop config")
	}

	// 7. Verify `gdocumentor setup --wsl-host` without `--all` or `--agent` returns an error instead of installing all
	stdout.Reset()
	stderr.Reset()
	if err := Run(context.Background(), []string{"setup", "--wsl-host"}, nil, &stdout, &stderr); err == nil {
		t.Fatalf("expected 'gdocumentor setup --wsl-host' without --all or --agent to return an error")
	}
}

func TestNoArgsAndSubcommandHelpOutputs(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// 1. Bare `gdocumentor` (no arguments) must display the main `--help` output
	if err := Run(context.Background(), []string{}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("bare 'gdocumentor' failed: %v", err)
	}
	noArgsHelp := stdout.String()

	stdout.Reset()
	if err := Run(context.Background(), []string{"--help"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("'gdocumentor --help' failed: %v", err)
	}
	if noArgsHelp != stdout.String() || !strings.Contains(noArgsHelp, "USAGE:") || !strings.Contains(noArgsHelp, "SUBCOMMANDS:") {
		t.Fatalf("expected bare 'gdocumentor' to match 'gdocumentor --help'")
	}

	// 2. Verify every subcommand supports `<subcommand> --help`, `<subcommand> -h`, `help <subcommand>`, and bare `<subcommand>` (except bare `mcp`)
	subcommands := []struct {
		name           string
		expectedHeader string
		expectedSample string
		testBare       bool
	}{
		{"setup", "gdocumentor setup — Multi-Agent MCP Server & Skill Installer", "gdocumentor setup --all --wsl-host", true},
		{"proofread", "gdocumentor proofread — AI Writing & Code Review Coach", "gdocumentor proofread --no-auth", true},
		{"summary", "gdocumentor summary — Review Thread Analysis & Learning Summary Generator", "gdocumentor summary -d", true},
		{"draft", "gdocumentor draft — Grounded Initial Draft Generator", "gdocumentor draft --title", true},
		{"edit", "gdocumentor edit — Direct 0-Token Google Docs Editor", "gdocumentor edit --create-doc", true},
		{"manage", "gdocumentor manage — Batch Suggestion Acceptance/Rejection", "gdocumentor manage -d", true},
		{"inspect", "gdocumentor inspect — Structural Paragraph, UTF-16 Coordinate", "gdocumentor inspect -d", true},
		{"mcp", "gdocumentor mcp — Dual-Engine Model Context Protocol (MCP)", "gdocumentor mcp", false},
	}

	for _, sc := range subcommands {
		variants := [][]string{
			{sc.name, "--help"},
			{sc.name, "-h"},
			{"help", sc.name},
		}
		if sc.testBare {
			variants = append(variants, []string{sc.name})
		}
		for _, args := range variants {
			stdout.Reset()
			stderr.Reset()
			if err := Run(context.Background(), args, nil, &stdout, &stderr); err != nil {
				t.Fatalf("Run(%v) failed: %v", args, err)
			}
			out := stdout.String()
			if !strings.Contains(out, sc.expectedHeader) || !strings.Contains(out, sc.expectedSample) {
				t.Fatalf("Run(%v) did not contain expected help header %q or sample %q:\n%s", args, sc.expectedHeader, sc.expectedSample, out)
			}
		}
	}
}

func TestMultiLanguageFlagNormalizationAndPrompts(t *testing.T) {
	cases := []struct {
		flagVal     string
		expectedCode string
		expectedDisp string
	}{
		{"de", "de", "German (Deutsch)"},
		{"German", "de", "German (Deutsch)"},
		{"fr", "fr", "French (Français)"},
		{"es", "es", "Spanish (Español)"},
		{"zh", "zh", "Chinese (中文)"},
		{"ko", "ko", "Korean (한국어)"},
		{"auto", "auto", "the same language as the target manuscript"},
	}

	for _, tc := range cases {
		opts, err := ParseArgs([]string{"proofread", "-i", "sample.md", "--lang", tc.flagVal})
		if err != nil {
			t.Fatalf("ParseArgs(--lang %q) failed: %v", tc.flagVal, err)
		}
		if opts.Lang != tc.expectedCode {
			t.Fatalf("expected Lang=%q for input %q, got %q", tc.expectedCode, tc.flagVal, opts.Lang)
		}
		prompt, schema := gemini.BuildAgentProofreadPrompt(gemini.ProofreadPromptOptions{
			Lang: opts.Lang,
		})
		if !strings.Contains(prompt, tc.expectedDisp) {
			t.Fatalf("expected prompt for lang %q to contain %q, got:\n%s", tc.flagVal, tc.expectedDisp, prompt)
		}
		props, _ := schema["properties"].(map[string]any)
		overallProp, _ := props["overallFeedback"].(map[string]any)
		desc, _ := overallProp["description"].(string)
		if !strings.Contains(desc, tc.expectedDisp) {
			t.Fatalf("expected schema description for lang %q to contain %q, got %q", tc.flagVal, tc.expectedDisp, desc)
		}
	}
}






