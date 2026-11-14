package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanaikech/gdocumentor/internal/gdocs"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
	"github.com/tanaikech/gdocumentor/internal/workspace"
)

func TestResolveModelName(t *testing.T) {
	origEnv := os.Getenv("DOCUMENTOR_MODEL")
	defer os.Setenv("DOCUMENTOR_MODEL", origEnv)

	os.Unsetenv("DOCUMENTOR_MODEL")
	if got := ResolveModelName(""); got != DefaultFastModel {
		t.Fatalf("expected default %q, got %q", DefaultFastModel, got)
	}
	if got := ResolveModelName("flash"); got != DefaultFastModel {
		t.Fatalf("expected flash alias -> %q, got %q", DefaultFastModel, got)
	}
	if got := ResolveModelName("argon"); got != DefaultDeepModel {
		t.Fatalf("expected argon alias -> %q, got %q", DefaultDeepModel, got)
	}
	if got := ResolveModelName("hydrogen"); got != ModelGemini4Hydrogen {
		t.Fatalf("expected hydrogen alias -> %q, got %q", ModelGemini4Hydrogen, got)
	}
	if got := ResolveModelName("carbon"); got != ModelGemini4Carbon {
		t.Fatalf("expected carbon alias -> %q, got %q", ModelGemini4Carbon, got)
	}
	if got := ResolveModelName("gemini-4-hydrogen"); got != "gemini-4-hydrogen" {
		t.Fatalf("expected gemini-4-hydrogen, got %q", got)
	}
	if got := ResolveModelName("gemini-4-carbon"); got != "gemini-4-carbon" {
		t.Fatalf("expected gemini-4-carbon, got %q", got)
	}
	if got := ResolveModelName("Models/Gemini-4-Carbon"); got != "gemini-4-carbon" {
		t.Fatalf("expected case-insensitive models/ prefix strip & lowercase, got %q", got)
	}
	if got := ResolveModelName("custom-experimental-model-99"); got != "custom-experimental-model-99" {
		t.Fatalf("expected transparent pass-through for arbitrary model, got %q", got)
	}

	os.Setenv("DOCUMENTOR_MODEL", "gemini-4-hydrogen")
	if got := ResolveModelName(""); got != "gemini-4-hydrogen" {
		t.Fatalf("expected DOCUMENTOR_MODEL env override, got %q", got)
	}
}

func TestGemini4ThoughtPartFilteringTemperatureOmissionAndTimeout(t *testing.T) {
	if !SupportsTemperature("gemini-3.8-flash") {
		t.Fatalf("expected SupportsTemperature=true for gemini-3.8-flash")
	}
	for _, m := range []string{"gemini-4-hydrogen", "gemini-4-carbon", "gemini-4-argon", "models/gemini-4-carbon"} {
		if SupportsTemperature(m) {
			t.Fatalf("expected SupportsTemperature=false for %q", m)
		}
	}

	t.Setenv("DOCUMENTOR_HTTP_TIMEOUT", "")
	if got := resolveHTTPTimeout(); got != DefaultHTTPTimeout {
		t.Fatalf("expected DefaultHTTPTimeout %v, got %v", DefaultHTTPTimeout, got)
	}
	t.Setenv("DOCUMENTOR_HTTP_TIMEOUT", "300")
	if got := resolveHTTPTimeout(); got != 300*time.Second {
		t.Fatalf("expected 300s timeout, got %v", got)
	}
	t.Setenv("DOCUMENTOR_HTTP_TIMEOUT", "15m")
	if got := resolveHTTPTimeout(); got != 15*time.Minute {
		t.Fatalf("expected 15m timeout, got %v", got)
	}
	t.Setenv("DOCUMENTOR_HTTP_TIMEOUT", "")

	var mu sync.Mutex
	var capturedPayloads []map[string]any
	var capturedPaths []string
	mode := "thought_multipart"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPaths = append(capturedPaths, r.URL.Path)
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		capturedPayloads = append(capturedPayloads, p)
		curMode := mode
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch curMode {
		case "thought_multipart":
			mockRes := ProofreadResult{
				OverallFeedback: "Reviewed with Gemini 4 Carbon.",
				LearningSummary: LearningSummary{
					RevisionsDigest:     "Checked clarity.",
					WeakPointsAndHabits: []string{"Redundancy"},
					ActionableTips:      []string{"Be concise"},
				},
				Suggestions: []utf16idx.RawSuggestion{},
			}
			fullBytes, _ := json.Marshal(mockRes)
			half := len(fullBytes) / 2
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []map[string]any{
					{
						"finishReason": "STOP",
						"content": map[string]any{
							"role": "model",
							"parts": []map[string]any{
								{"thought": true, "text": "Internal CoT reasoning that MUST NOT be parsed as JSON or inserted into Docs."},
								{"thought": true, "text": ""},
								{"text": string(fullBytes[:half])},
								{"text": string(fullBytes[half:])},
							},
						},
					},
				},
			})
		case "draft_with_thought":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []map[string]any{
					{
						"finishReason": "STOP",
						"content": map[string]any{
							"role": "model",
							"parts": []map[string]any{
								{"thought": true, "text": "Thinking about draft structure..."},
								{"text": "# Clean Final Draft\n\nBody paragraph."},
							},
						},
					},
				},
			})
		case "max_tokens_truncated":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []map[string]any{
					{
						"finishReason": "MAX_TOKENS",
						"content": map[string]any{
							"role": "model",
							"parts": []map[string]any{
								{"text": `{"overallFeedback": "truncated`},
							},
						},
					},
				},
			})
		}
	}))
	defer srv.Close()

	// 1. Test gemini-4-carbon omits temperature and skips thought:true parts while joining split JSON parts
	carbonClient, err := NewClient("test-key", "carbon")
	if err != nil {
		t.Fatalf("NewClient(carbon) failed: %v", err)
	}
	carbonClient.SetBaseURL(srv.URL)
	docData := &gdocs.ParsedDocument{DocumentID: "d1", AnnotatedText: "Hello world."}
	res, err := carbonClient.GenerateProofreading(context.Background(), ProofreadPromptOptions{
		DocData: docData,
		Lang:    "en",
	})
	if err != nil {
		t.Fatalf("GenerateProofreading on gemini-4-carbon failed: %v", err)
	}
	if res.OverallFeedback != "Reviewed with Gemini 4 Carbon." {
		t.Fatalf("unexpected OverallFeedback: %q", res.OverallFeedback)
	}
	if !strings.Contains(capturedPaths[0], "/v1beta/models/gemini-4-carbon:generateContent") {
		t.Fatalf("expected gemini-4-carbon endpoint, got %q", capturedPaths[0])
	}
	if genCfg, ok := capturedPayloads[0]["generationConfig"].(map[string]any); ok {
		if _, hasTemp := genCfg["temperature"]; hasTemp {
			t.Fatalf("expected temperature to be omitted for gemini-4-carbon, got %+v", genCfg)
		}
	}

	// 2. Test gemini-4-hydrogen draft generation omits empty generationConfig and never leaks thought:true text
	mu.Lock()
	mode = "draft_with_thought"
	mu.Unlock()
	hydrogenClient, err := NewClient("test-key", "gemini-4-hydrogen")
	if err != nil {
		t.Fatalf("NewClient(gemini-4-hydrogen) failed: %v", err)
	}
	hydrogenClient.SetBaseURL(srv.URL)
	draftText, err := hydrogenClient.GenerateInitialDraft(context.Background(), DraftPromptOptions{
		Lang:         "en",
		CustomPrompt: "Write an introduction.",
	})
	if err != nil {
		t.Fatalf("GenerateInitialDraft on gemini-4-hydrogen failed: %v", err)
	}
	if strings.Contains(draftText, "Thinking about draft") || !strings.HasPrefix(draftText, "# Clean Final Draft") {
		t.Fatalf("thought text leaked or draft corrupted: %q", draftText)
	}
	if !strings.Contains(capturedPaths[1], "/v1beta/models/gemini-4-hydrogen:generateContent") {
		t.Fatalf("expected gemini-4-hydrogen endpoint, got %q", capturedPaths[1])
	}
	if _, hasGenCfg := capturedPayloads[1]["generationConfig"]; hasGenCfg {
		t.Fatalf("expected empty generationConfig to be omitted in draft for gemini-4-hydrogen")
	}

	// 3. Test MAX_TOKENS truncation surfaces explicit finishReason=MAX_TOKENS error
	mu.Lock()
	mode = "max_tokens_truncated"
	mu.Unlock()
	_, maxErr := hydrogenClient.GenerateProofreading(context.Background(), ProofreadPromptOptions{
		DocData: docData,
		Lang:    "en",
	})
	if maxErr == nil || !strings.Contains(maxErr.Error(), "MAX_TOKENS") {
		t.Fatalf("expected explicit MAX_TOKENS diagnostic error, got: %v", maxErr)
	}
}

func TestHybridContextAndGenerateProofreading(t *testing.T) {
	var mu sync.Mutex
	uploadedFiles := make(map[string]bool)
	deletedFiles := make(map[string]bool)
	var capturedGeneratePayload map[string]any
	var capturedModelPath string

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		// 1. Resumable upload start
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/v1beta/files") {
			if r.Header.Get("X-Goog-Upload-Command") == "start" {
				w.Header().Set("X-Goog-Upload-URL", srv.URL+"/upload-session/file_pdf_1")
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		// 2. Resumable upload finalize
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/upload-session/") {
			fileName := "files/test_uploaded_pdf_123"
			uploadedFiles[fileName] = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file": map[string]any{
					"name":     fileName,
					"uri":      srv.URL + "/v1beta/" + fileName,
					"mimeType": "application/pdf",
				},
			})
			return
		}

		// 3. File deletion (defer cleanup)
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1beta/files/") {
			name := strings.TrimPrefix(r.URL.Path, "/v1beta/")
			deletedFiles[name] = true
			w.WriteHeader(http.StatusOK)
			return
		}

		// 4. generateContent
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":generateContent") {
			capturedModelPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&capturedGeneratePayload)

			mockResult := ProofreadResult{
				OverallFeedback: "Clear structure with one terminology inconsistency.",
				LearningSummary: LearningSummary{
					RevisionsDigest:     "Aligned product name with style guide.",
					WeakPointsAndHabits: []string{"Occasional informal phrasing"},
					ActionableTips:      []string{"Always use DocuMentor AI with capital M"},
				},
				ExistingSuggestionsNote: "Complemented existing edits.",
				Suggestions: []utf16idx.RawSuggestion{
					{
						OriginalText:  "Documentor ai is helpful.",
						SuggestedText: "DocuMentor AI is helpful.",
						Category:      "Terminology",
						Reason:        "Enforce brand capitalization rule.",
						LearningPoint: "Check brand glossary.",
					},
				},
				ElementSuggestions: []gdocs.RawElementSuggestion{},
			}
			resBytes, _ := json.Marshal(mockResult)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"parts": []map[string]any{
								{"text": string(resBytes)},
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

	client, err := NewClient("test-api-key", "gemini-4-argon")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	client.SetBaseURL(srv.URL)

	docData := &gdocs.ParsedDocument{
		DocumentID:    "doc_1",
		AnnotatedText: "Documentor ai is helpful.",
		EditableBlocks: []utf16idx.EditableBlock{
			{
				Text:           "Documentor ai is helpful.",
				StartIndex:     1,
				EndIndex:       26,
				FilePath:       "article.md",
				LineNumber:     1,
				TargetEditable: true,
			},
		},
	}

	res, err := client.GenerateProofreading(context.Background(), ProofreadPromptOptions{
		DocData: docData,
		TargetFiles: []workspace.FileEntry{
			{Path: "/tmp/article.md", DisplayPath: "article.md", Content: "Documentor ai is helpful."},
		},
		ReferenceFiles: []workspace.FileEntry{
			{Path: "/tmp/glossary.md", DisplayPath: "glossary.md", Content: "Use DocuMentor AI."},
		},
		RagRules: []workspace.RagRuleFile{
			{
				ID:         "RAG_1",
				Title:      "style.md",
				SourceType: "LOCAL_TEXT",
				MimeType:   "text/markdown",
				Content:    "Always spell DocuMentor AI with capital M.",
				IsBinary:   false,
			},
			{
				ID:         "RAG_2",
				Title:      "handbook.pdf",
				SourceType: "LOCAL_BINARY",
				MimeType:   "application/pdf",
				Bytes:      []byte("%PDF-1.4 mock binary content"),
				IsBinary:   true,
			},
		},
		Lang: "en",
	})
	if err != nil {
		t.Fatalf("GenerateProofreading failed: %v", err)
	}

	if len(res.Suggestions) != 1 || res.Suggestions[0].SuggestedText != "DocuMentor AI is helpful." {
		t.Fatalf("unexpected suggestions: %+v", res.Suggestions)
	}
	if !strings.Contains(capturedModelPath, "gemini-4-argon:generateContent") {
		t.Fatalf("expected gemini-4-argon endpoint, got %q", capturedModelPath)
	}

	// Verify that the binary PDF was uploaded via File API AND deleted via defer cleanup
	if !uploadedFiles["files/test_uploaded_pdf_123"] {
		t.Fatalf("expected binary PDF rulebook to be uploaded via Gemini File API")
	}
	if !deletedFiles["files/test_uploaded_pdf_123"] {
		t.Fatalf("expected uploaded File API blob to be deleted via defer cleanup")
	}

	// Verify that inline text tags (<organization_rules>, <reference_context_files>, <target_files>) were sent inline
	var rawBuf strings.Builder
	enc := json.NewEncoder(&rawBuf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(capturedGeneratePayload)
	rawPayloadStr := rawBuf.String()
	for _, tag := range []string{"<organization_rules>", "<reference_context_files>", "<target_files>"} {
		if !strings.Contains(rawPayloadStr, tag) {
			t.Fatalf("expected inline XML boundary tag %q in generateContent payload", tag)
		}
	}
}

func TestResolveEffectivePersonaAndPromptInjection(t *testing.T) {
	ragRules := []workspace.RagRuleFile{
		{
			ID:         "RAG_1",
			Title:      "ja_persona.md",
			SourceType: "LOCAL_TEXT",
			MimeType:   "text/markdown",
			Content:    "あなたは大学で日本語を教えている日本人です。学生が書いた文章を自然で学術的に正確な日本語へ導いてください。",
		},
	}

	eff := ResolveEffectivePersona("", ragRules)
	if !strings.Contains(eff, "あなたは大学で日本語を教えている日本人です。") {
		t.Fatalf("expected persona extracted from RAG rulebook, got %q", eff)
	}

	promptJa := buildProofreadSystemPrompt(
		"ja",
		false,
		false,
		eff,
		"",
		"",
		"なし",
		"なし",
		"なし",
		"なし",
		"なし",
		"<target_files></target_files>",
		false,
	)
	if !strings.Contains(promptJa, "【AIの位置づけ・立場（カスタムペルソナ）】") || !strings.Contains(promptJa, "あなたは大学で日本語を教えている日本人です。") {
		t.Fatalf("expected custom persona block in Japanese system prompt, got:\n%s", promptJa)
	}
}
