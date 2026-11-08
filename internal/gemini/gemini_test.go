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
	if got := ResolveModelName("custom-experimental-model-99"); got != "custom-experimental-model-99" {
		t.Fatalf("expected transparent pass-through for arbitrary model, got %q", got)
	}

	os.Setenv("DOCUMENTOR_MODEL", "gemini-4-argon")
	if got := ResolveModelName(""); got != "gemini-4-argon" {
		t.Fatalf("expected DOCUMENTOR_MODEL env override, got %q", got)
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
