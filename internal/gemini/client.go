package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tanaikech/gdocumentor/internal/gdocs"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
	"github.com/tanaikech/gdocumentor/internal/workspace"
)

const (
	// DefaultFastModel is the default high-speed model for interactive proofreading and code reviews.
	DefaultFastModel = "gemini-3.8-flash"
	// DefaultDeepModel is the high-precision deep-reasoning model for complex multi-file reviews.
	DefaultDeepModel = "gemini-4-argon"
	// DefaultMaxInlineTextBytes is the threshold (2 MB) above which text contexts overflow to the Gemini File API.
	DefaultMaxInlineTextBytes = 2 * 1024 * 1024
)

// LearningSummary holds the structured 3-part Writing Coach & Learning Summary.
type LearningSummary struct {
	RevisionsDigest     string   `json:"revisionsDigest"`
	WeakPointsAndHabits []string `json:"weakPointsAndHabits"`
	ActionableTips      []string `json:"actionableTips"`
}

// ProofreadResult represents the structured JSON response from Gemini or a Host AI Agent during proofreading / code review.
type ProofreadResult struct {
	OverallFeedback         string                       `json:"overallFeedback"`
	LearningSummary         LearningSummary              `json:"learningSummary"`
	ExistingSuggestionsNote string                       `json:"existingSuggestionsNote"`
	Suggestions             []utf16idx.RawSuggestion     `json:"suggestions"`
	ElementSuggestions      []gdocs.RawElementSuggestion `json:"elementSuggestions"`
}

// UnmarshalJSON allows ProofreadResult to be unmarshaled from either a JSON object or a stringified JSON payload.
func (p *ProofreadResult) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var rawStr string
		if err := json.Unmarshal(trimmed, &rawStr); err != nil {
			return err
		}
		cleaned := reLeadingCodeFence.ReplaceAllString(strings.TrimSpace(rawStr), "")
		cleaned = reTrailingCodeFence.ReplaceAllString(cleaned, "")
		trimmed = []byte(strings.TrimSpace(cleaned))
	}
	type plainProofreadResult ProofreadResult
	var dst plainProofreadResult
	if err := json.Unmarshal(trimmed, &dst); err != nil {
		return err
	}
	*p = ProofreadResult(dst)
	return nil
}

// AnalyzedReviewItem represents a single existing suggestion or comment analyzed in Summary Mode.
type AnalyzedReviewItem struct {
	ID                      string `json:"id"`
	SourceType              string `json:"sourceType"`
	TargetPreview           string `json:"targetPreview"`
	ProposedChangeOrComment string `json:"proposedChangeOrComment"`
	Category                string `json:"category"`
	Reason                  string `json:"reason"`
	LearningPoint           string `json:"learningPoint"`
}

// SummaryResult represents the structured JSON response when summarizing existing suggestions & comments.
type SummaryResult struct {
	OverallFeedback string               `json:"overallFeedback"`
	LearningSummary LearningSummary      `json:"learningSummary"`
	AnalyzedItems   []AnalyzedReviewItem `json:"analyzedItems"`
}

// UnmarshalJSON allows SummaryResult to be unmarshaled from either a JSON object or a stringified JSON payload.
func (s *SummaryResult) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var rawStr string
		if err := json.Unmarshal(trimmed, &rawStr); err != nil {
			return err
		}
		cleaned := reLeadingCodeFence.ReplaceAllString(strings.TrimSpace(rawStr), "")
		cleaned = reTrailingCodeFence.ReplaceAllString(cleaned, "")
		trimmed = []byte(strings.TrimSpace(cleaned))
	}
	type plainSummaryResult SummaryResult
	var dst plainSummaryResult
	if err := json.Unmarshal(trimmed, &dst); err != nil {
		return err
	}
	*s = SummaryResult(dst)
	return nil
}

// UploadedFile tracks a temporary file uploaded to the Gemini File API for guaranteed defer cleanup.
type UploadedFile struct {
	Name        string `json:"name"`
	URI         string `json:"uri"`
	MimeType    string `json:"mimeType"`
	DisplayName string `json:"displayName"`
}

// Client communicates with the Google Gemini REST API (v1beta generateContent and upload/v1beta/files).
type Client struct {
	apiKey             string
	model              string
	baseURL            string
	httpClient         *http.Client
	maxInlineTextBytes int
}

// NewClient initializes a Gemini API client, resolving the API key and model name with flexible overrides.
func NewClient(explicitAPIKey, explicitModel string) (*Client, error) {
	apiKey := strings.TrimSpace(explicitAPIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	if apiKey == "" {
		return nil, errors.New("Gemini API key is missing: set the GEMINI_API_KEY environment variable or pass --api-key")
	}

	model := ResolveModelName(explicitModel)

	return &Client{
		apiKey:             apiKey,
		model:              model,
		baseURL:            "https://generativelanguage.googleapis.com",
		httpClient:         &http.Client{Timeout: 180 * time.Second},
		maxInlineTextBytes: DefaultMaxInlineTextBytes,
	}, nil
}

// ResolveModelName resolves the Gemini model name from explicit flag -> DOCUMENTOR_MODEL env var -> DefaultFastModel.
// Any arbitrary model identifier is preserved transparently without hardcoded rejection.
func ResolveModelName(explicitModel string) string {
	raw := strings.TrimSpace(explicitModel)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("DOCUMENTOR_MODEL"))
	}
	if raw == "" {
		return DefaultFastModel
	}
	raw = strings.TrimPrefix(raw, "models/")
	switch strings.ToLower(raw) {
	case "fast", "flash", "default":
		return DefaultFastModel
	case "deep", "argon", "pro":
		return DefaultDeepModel
	default:
		return raw
	}
}

// Model returns the configured Gemini model identifier.
func (c *Client) Model() string {
	return c.model
}

// SetBaseURL overrides the Gemini API base URL (used for unit testing with httptest).
func (c *Client) SetBaseURL(baseURL string) {
	if strings.TrimSpace(baseURL) != "" {
		c.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
}

// SetMaxInlineTextBytes overrides the byte threshold for overflowing inline text to the Gemini File API.
func (c *Client) SetMaxInlineTextBytes(maxBytes int) {
	if maxBytes > 0 {
		c.maxInlineTextBytes = maxBytes
	}
}

// UploadBinaryToFileAPI uploads a binary or oversized payload to the Gemini File API using the Resumable Upload protocol.
func (c *Client) UploadBinaryToFileAPI(ctx context.Context, displayName, mimeType string, data []byte) (*UploadedFile, error) {
	if len(data) == 0 {
		return nil, errors.New("cannot upload empty payload to Gemini File API")
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = "documentor_context_blob"
	}

	initURL := fmt.Sprintf("%s/upload/v1beta/files?key=%s", c.baseURL, url.QueryEscape(c.apiKey))
	metaPayload := map[string]any{
		"file": map[string]any{
			"display_name": displayName,
		},
	}
	metaBytes, _ := json.Marshal(metaPayload)

	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, bytes.NewReader(metaBytes))
	if err != nil {
		return nil, err
	}
	initReq.Header.Set("X-Goog-Upload-Protocol", "resumable")
	initReq.Header.Set("X-Goog-Upload-Command", "start")
	initReq.Header.Set("X-Goog-Upload-Header-Content-Length", fmt.Sprintf("%d", len(data)))
	initReq.Header.Set("X-Goog-Upload-Header-Content-Type", mimeType)
	initReq.Header.Set("Content-Type", "application/json")

	initResp, err := c.httpClient.Do(initReq)
	if err != nil {
		return nil, fmt.Errorf("Gemini File API resumable start failed: %w", err)
	}
	initBody, _ := io.ReadAll(initResp.Body)
	initResp.Body.Close()

	if initResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini File API start error (%d): %s", initResp.StatusCode, string(initBody))
	}

	uploadURL := strings.TrimSpace(initResp.Header.Get("X-Goog-Upload-URL"))
	if uploadURL == "" {
		uploadURL = strings.TrimSpace(initResp.Header.Get("x-goog-upload-url"))
	}
	if uploadURL == "" {
		return nil, errors.New("Gemini File API did not return X-Goog-Upload-URL header")
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	putReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(data)))
	putReq.Header.Set("X-Goog-Upload-Offset", "0")
	putReq.Header.Set("X-Goog-Upload-Command", "upload, finalize")

	putResp, err := c.httpClient.Do(putReq)
	if err != nil {
		return nil, fmt.Errorf("Gemini File API upload finalize failed: %w", err)
	}
	putBody, _ := io.ReadAll(putResp.Body)
	putResp.Body.Close()

	if putResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini File API upload error (%d): %s", putResp.StatusCode, string(putBody))
	}

	var parsed struct {
		File struct {
			Name     string `json:"name"`
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
		} `json:"file"`
	}
	if err := json.Unmarshal(putBody, &parsed); err != nil || parsed.File.URI == "" {
		return nil, fmt.Errorf("failed to parse Gemini File API upload response: %s", string(putBody))
	}

	effMime := parsed.File.MimeType
	if effMime == "" {
		effMime = mimeType
	}
	return &UploadedFile{
		Name:        parsed.File.Name,
		URI:         parsed.File.URI,
		MimeType:    effMime,
		DisplayName: displayName,
	}, nil
}

// DeleteUploadedFiles deletes temporary files from the Gemini File API in parallel.
// Designed to be invoked via `defer` immediately after uploading context files.
func (c *Client) DeleteUploadedFiles(ctx context.Context, files []*UploadedFile) int {
	if len(files) == 0 {
		return 0
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	deleted := 0

	for _, f := range files {
		if f == nil || strings.TrimSpace(f.Name) == "" {
			continue
		}
		wg.Add(1)
		go func(fileName string) {
			defer wg.Done()
			cleanName := strings.TrimPrefix(strings.TrimSpace(fileName), "/")
			delURL := fmt.Sprintf("%s/v1beta/%s?key=%s", c.baseURL, cleanName, url.QueryEscape(c.apiKey))
			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, delURL, nil)
			if err != nil {
				return
			}
			resp, err := c.httpClient.Do(req)
			if err != nil {
				return
			}
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
				mu.Lock()
				deleted++
				mu.Unlock()
			}
		}(f.Name)
	}
	wg.Wait()
	return deleted
}

// callGenerateContent invokes POST /v1beta/models/{model}:generateContent and returns the text of the first candidate part.
func (c *Client) callGenerateContent(ctx context.Context, payload map[string]any) (string, error) {
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.baseURL, url.PathEscape(c.model), url.QueryEscape(c.apiKey))
	var bodyBuf bytes.Buffer
	enc := json.NewEncoder(&bodyBuf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &bodyBuf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Gemini API request failed: %w", err)
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		var errEnv struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &errEnv) == nil && errEnv.Error.Message != "" {
			return "", fmt.Errorf("Gemini API Error (%d) [model=%s]: %s", resp.StatusCode, c.model, errEnv.Error.Message)
		}
		return "", fmt.Errorf("Gemini API Error (%d) [model=%s]: %s", resp.StatusCode, c.model, string(respBody))
	}

	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse Gemini API response JSON: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", errors.New("received an empty response from Gemini API")
	}

	rawText := strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text)
	if rawText == "" {
		return "", errors.New("received an empty text candidate from Gemini API")
	}
	return rawText, nil
}

// buildHybridContextParts implements the Hybrid Context Optimization strategy:
// - Text rules (.md/.txt/.json/.yaml/Google Docs) and reference context files are structured into
//   zero-latency inline XML-tagged blocks (<organization_rules>, <reference_context_files>) unless the
//   cumulative text exceeds c.maxInlineTextBytes (2 MB), in which case overflow files are uploaded in parallel
//   to the Gemini File API.
// - Binary RAG files (PDFs with IsBinary=true) and embedded visual images (VISUAL_N) are uploaded in parallel
//   to the Gemini File API (with inlineData fallback if File API upload fails on an image) and registered for
//   guaranteed defer cleanup.
func (c *Client) buildHybridContextParts(
	ctx context.Context,
	ragRules []workspace.RagRuleFile,
	refFiles []workspace.FileEntry,
	visuals []gdocs.VisualElement,
	targetFiles []workspace.FileEntry,
	isJa bool,
) ([]map[string]any, []*UploadedFile, string, error) {
	var parts []map[string]any
	var uploaded []*UploadedFile
	var uploadedMu sync.Mutex

	type uploadTask struct {
		label       string
		displayName string
		mimeType    string
		data        []byte
		isVisual    bool
		visualMeta  gdocs.VisualElement
	}

	var binaryTasks []uploadTask
	var inlineRulesBuilder strings.Builder
	var inlineRefsBuilder strings.Builder
	runningInlineBytes := 0

	// 1. Partition RAG Rules into inline text (<organization_rules>) vs. File API binary/overflow
	var ruleTitles []string
	for _, r := range ragRules {
		ruleTitles = append(ruleTitles, fmt.Sprintf("%s (%s)", r.Title, r.SourceType))
		if r.IsBinary && len(r.Bytes) > 0 {
			binaryTasks = append(binaryTasks, uploadTask{
				label:       fmt.Sprintf("[Attached Organization Rulebook File: %s (ID: %s, Type: %s)]", r.Title, r.ID, r.SourceType),
				displayName: r.Title,
				mimeType:    r.MimeType,
				data:        r.Bytes,
			})
			continue
		}
		content := strings.TrimSpace(r.Content)
		if content == "" {
			continue
		}
		entryText := fmt.Sprintf("<rule_file id=%q title=%q source=%q>\n%s\n</rule_file>\n", r.ID, r.Title, r.SourceType, content)
		if runningInlineBytes+len(entryText) <= c.maxInlineTextBytes {
			inlineRulesBuilder.WriteString(entryText)
			runningInlineBytes += len(entryText)
		} else {
			binaryTasks = append(binaryTasks, uploadTask{
				label:       fmt.Sprintf("[Attached Organization Rulebook File (Overflow): %s (ID: %s)]", r.Title, r.ID),
				displayName: r.Title,
				mimeType:    firstNonEmptyStr(r.MimeType, "text/plain"),
				data:        []byte(content),
			})
		}
	}

	// 2. Partition Reference Context Files (--context / --auto-deps) into inline text (<reference_context_files>) vs. File API overflow
	for _, rf := range refFiles {
		content := strings.TrimSpace(rf.Content)
		if content == "" {
			continue
		}
		entryText := fmt.Sprintf("<context_file path=%q>\n%s\n</context_file>\n", rf.DisplayPath, content)
		if runningInlineBytes+len(entryText) <= c.maxInlineTextBytes {
			inlineRefsBuilder.WriteString(entryText)
			runningInlineBytes += len(entryText)
		} else {
			binaryTasks = append(binaryTasks, uploadTask{
				label:       fmt.Sprintf("[Attached Reference Context File (Read-Only): %s]", rf.DisplayPath),
				displayName: rf.DisplayPath,
				mimeType:    "text/plain",
				data:        []byte(content),
			})
		}
	}

	// 3. Queue visual figure/chart images for parallel upload
	for _, v := range visuals {
		if len(v.ImageBytes) == 0 {
			continue
		}
		vLabel := fmt.Sprintf("[Attached Image for %s (%s): Title=%q, Description=%q]", v.TargetID, v.Type, v.Title, v.Description)
		if isJa {
			vLabel = fmt.Sprintf("【%s (%s) の実画像データ: タイトル=%q, 説明=%q】", v.TargetID, v.Type, v.Title, v.Description)
		}
		binaryTasks = append(binaryTasks, uploadTask{
			label:       vLabel,
			displayName: v.TargetID,
			mimeType:    firstNonEmptyStr(v.MimeType, "image/png"),
			data:        v.ImageBytes,
			isVisual:    true,
			visualMeta:  v,
		})
	}

	// 4. Queue original PDF target files (if any) for parallel upload so Gemini can inspect visual layout & embedded figures
	for _, tf := range targetFiles {
		if tf.MimeType == workspace.MimeTypePDF && len(tf.RawBytes) > 0 {
			pdfLabel := fmt.Sprintf("[Attached Original Target PDF for Visual & Layout Inspection: %s]", tf.DisplayPath)
			if isJa {
				pdfLabel = fmt.Sprintf("【推敲対象PDFの原本データ（レイアウト・図表参照用）: %s】", tf.DisplayPath)
			}
			binaryTasks = append(binaryTasks, uploadTask{
				label:       pdfLabel,
				displayName: tf.DisplayPath,
				mimeType:    workspace.MimeTypePDF,
				data:        tf.RawBytes,
			})
		}
	}

	// Assemble inline <organization_rules> part if present
	if inlineRulesBuilder.Len() > 0 {
		parts = append(parts, map[string]any{
			"text": "<organization_rules>\n" + inlineRulesBuilder.String() + "</organization_rules>",
		})
	}

	// Assemble inline <reference_context_files> part if present
	if inlineRefsBuilder.Len() > 0 {
		parts = append(parts, map[string]any{
			"text": "<reference_context_files>\n" + inlineRefsBuilder.String() + "</reference_context_files>",
		})
	}

	// Execute parallel uploads to Gemini File API for binary PDFs, visual images, and >2MB overflow files
	if len(binaryTasks) > 0 {
		type indexedUploadResult struct {
			idx      int
			task     uploadTask
			uploaded *UploadedFile
			err      error
		}
		resCh := make(chan indexedUploadResult, len(binaryTasks))
		var wg sync.WaitGroup

		for idx, task := range binaryTasks {
			wg.Add(1)
			go func(i int, t uploadTask) {
				defer wg.Done()
				up, err := c.UploadBinaryToFileAPI(ctx, t.displayName, t.mimeType, t.data)
				if err == nil && up != nil {
					uploadedMu.Lock()
					uploaded = append(uploaded, up)
					uploadedMu.Unlock()
				}
				resCh <- indexedUploadResult{
					idx:      i,
					task:     t,
					uploaded: up,
					err:      err,
				}
			}(idx, task)
		}
		wg.Wait()
		close(resCh)

		ordered := make([]indexedUploadResult, len(binaryTasks))
		for r := range resCh {
			ordered[r.idx] = r
		}

		for _, r := range ordered {
			if r.uploaded != nil {
				parts = append(parts,
					map[string]any{"text": r.task.label},
					map[string]any{
						"file_data": map[string]any{
							"mime_type": r.uploaded.MimeType,
							"file_uri":  r.uploaded.URI,
						},
					},
				)
			} else if r.task.isVisual && len(r.task.data) > 0 {
				// Graceful fallback to inlineData if File API upload is unavailable for an embedded image
				parts = append(parts,
					map[string]any{"text": r.task.label},
					map[string]any{
						"inlineData": map[string]any{
							"mimeType": r.task.mimeType,
							"data":     base64.StdEncoding.EncodeToString(r.task.data),
						},
					},
				)
			} else if r.err != nil {
				// Clean up any already-uploaded files before returning error
				c.DeleteUploadedFiles(context.Background(), uploaded)
				return nil, nil, "", r.err
			}
		}
	}

	ragHeading := ""
	if len(ruleTitles) > 0 {
		if isJa {
			ragHeading = fmt.Sprintf(
				"\n\n### 📚 参照すべき組織・プロジェクト固有ルールおよびAIの立場設定（RAGコンテキスト: %d件 — %s）\n"+
					"添付または `<organization_rules>` に含まれるルールブックに「AIの位置づけ・立場（ペルソナ、例：『あなたは大学で日本語を教えている日本人です。』等）」が定義されている場合は、その立場・専門性・語り口になりきって推敲およびコーチングを行ってください。\n"+
					"また、表記ゆれ規定、スタイルガイド、コーディング規約、用語集などに厳格に準拠して推敲を行い、ルールに基づく修正の場合は `reason` や `learningPoint` で「どのルールや観点に基づくか」を明記してください。",
				len(ruleTitles),
				strings.Join(ruleTitles, ", "),
			)
		} else {
			ragHeading = fmt.Sprintf(
				"\n\n### 📚 Organization / Project Guidelines & AI Persona (RAG Context: %d file(s) — %s)\n"+
					"If `<organization_rules>` or any attached rulebook defines a custom AI persona, role, or pedagogical positioning (e.g., \"You are a university professor teaching Japanese...\"), adopt that exact persona, perspective, and voice as your primary identity.\n"+
					"Strictly enforce the terminology, style rules, coding conventions, and formatting guidelines defined in `<organization_rules>` or the attached rulebook files. When a suggestion enforces a specific organization rule, explicitly cite that rule in `reason` and `learningPoint`.",
				len(ruleTitles),
				strings.Join(ruleTitles, ", "),
			)
		}
	}

	return parts, uploaded, ragHeading, nil
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}
