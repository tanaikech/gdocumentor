package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/tanaikech/gdocumentor/internal/gdocs"
	"github.com/tanaikech/gdocumentor/internal/workspace"
)

var (
	reLeadingCodeFence  = regexp.MustCompile(`(?i)^` + "```" + `[a-z0-9_-]*\s*\n?`)
	reTrailingCodeFence = regexp.MustCompile(`\n?` + "```" + `\s*$`)
)

// ProofreadPromptOptions contains all inputs for GenerateProofreading.
type ProofreadPromptOptions struct {
	DocData            *gdocs.ParsedDocument
	TargetFiles        []workspace.FileEntry
	ReferenceFiles     []workspace.FileEntry
	RagRules           []workspace.RagRuleFile
	VisualsWithImages  []gdocs.VisualElement
	UseDriveConversion bool
	Persona            string // Optional custom AI persona / positioning override
	Lang               string // Language code or name (e.g., "en", "ja", "de", "fr", "es", "zh", "ko", "auto")
	CustomPrompt       string
	HintOnly           bool
	IncludeResolved    bool
}

// SummaryPromptOptions contains all inputs for GenerateLearningSummaryFromExisting.
type SummaryPromptOptions struct {
	DocData           *gdocs.ParsedDocument
	ReferenceFiles    []workspace.FileEntry
	RagRules          []workspace.RagRuleFile
	VisualsWithImages []gdocs.VisualElement
	Persona           string // Optional custom AI persona / positioning override
	Lang              string // Language code or name (e.g., "en", "ja", "de", "fr", "es", "zh", "ko", "auto")
	CustomPrompt      string
	IncludeResolved   bool
}

// DraftPromptOptions contains all inputs for GenerateInitialDraft.
type DraftPromptOptions struct {
	Persona        string // Optional custom AI persona / positioning override
	Lang           string // Language code or name (e.g., "en", "ja", "de", "fr", "es", "zh", "ko", "auto")
	CustomPrompt   string
	ReferenceFiles []workspace.FileEntry
	RagRules       []workspace.RagRuleFile
}

// NormalizeLanguageCode normalizes a language flag value into a canonical code or lowercased name.
func NormalizeLanguageCode(lang string) string {
	raw := strings.ToLower(strings.TrimSpace(lang))
	switch raw {
	case "", "en", "eng", "english", "en-us", "en-gb":
		return "en"
	case "ja", "jp", "jpn", "japanese", "ja-jp", "日本語":
		return "ja"
	case "de", "deu", "ger", "german", "deutsch", "de-de", "de-at", "de-ch", "ドイツ語":
		return "de"
	case "fr", "fra", "fre", "french", "français", "francais", "fr-fr", "フランス語":
		return "fr"
	case "es", "spa", "spanish", "español", "espanol", "es-es", "es-mx", "スペイン語":
		return "es"
	case "it", "ita", "italian", "italiano", "it-it", "イタリア語":
		return "it"
	case "pt", "por", "portuguese", "português", "portugues", "pt-br", "pt-pt", "ポルトガル語":
		return "pt"
	case "nl", "nld", "dut", "dutch", "nederlands", "オランダ語":
		return "nl"
	case "ru", "rus", "russian", "русский", "ロシア語":
		return "ru"
	case "zh-tw", "zh-hk", "zh-hant", "traditional chinese", "繁体字", "繁體中文":
		return "zh-tw"
	case "zh", "zh-cn", "zh-hans", "zho", "chi", "chinese", "中文", "中国語":
		return "zh"
	case "ko", "kor", "korean", "한국어", "韓国語":
		return "ko"
	case "ar", "ara", "arabic", "العربية", "アラビア語":
		return "ar"
	case "hi", "hin", "hindi", "हिन्दी", "ヒンディー語":
		return "hi"
	case "id", "ind", "indonesian", "bahasa indonesia", "インドネシア語":
		return "id"
	case "vi", "vie", "vietnamese", "tiếng việt", "ベトナム語":
		return "vi"
	case "th", "tha", "thai", "ไทย", "タイ語":
		return "th"
	case "tr", "tur", "turkish", "türkçe", "トルコ語":
		return "tr"
	case "pl", "pol", "polish", "polski", "ポーランド語":
		return "pl"
	case "sv", "swe", "swedish", "svenska", "スウェーデン語":
		return "sv"
	case "auto", "detect", "match", "same":
		return "auto"
	default:
		return raw
	}
}

// LanguageDisplayName returns a human-readable English + native name for the given language code.
func LanguageDisplayName(lang string) string {
	switch NormalizeLanguageCode(lang) {
	case "en":
		return "English"
	case "ja":
		return "Japanese (日本語)"
	case "de":
		return "German (Deutsch)"
	case "fr":
		return "French (Français)"
	case "es":
		return "Spanish (Español)"
	case "it":
		return "Italian (Italiano)"
	case "pt":
		return "Portuguese (Português)"
	case "nl":
		return "Dutch (Nederlands)"
	case "ru":
		return "Russian (Русский)"
	case "zh-tw":
		return "Traditional Chinese (繁體中文)"
	case "zh":
		return "Chinese (中文)"
	case "ko":
		return "Korean (한국어)"
	case "ar":
		return "Arabic (العربية)"
	case "hi":
		return "Hindi (हिन्दी)"
	case "id":
		return "Indonesian (Bahasa Indonesia)"
	case "vi":
		return "Vietnamese (Tiếng Việt)"
	case "th":
		return "Thai (ไทย)"
	case "tr":
		return "Turkish (Türkçe)"
	case "pl":
		return "Polish (Polski)"
	case "sv":
		return "Swedish (Svenska)"
	case "auto":
		return "the same language as the target manuscript"
	default:
		trimmed := strings.TrimSpace(lang)
		if trimmed == "" {
			return "English"
		}
		return trimmed
	}
}

// ResolveEffectivePersona merges an explicit CLI/JSON persona override with any custom AI persona
// declared inside the loaded RAG rulebooks (e.g. "あなたは大学で日本語を教えている日本人です。").
func ResolveEffectivePersona(explicitPersona string, ragRules []workspace.RagRuleFile) string {
	var parts []string
	if p := strings.TrimSpace(explicitPersona); p != "" {
		parts = append(parts, p)
	}
	if ragP := strings.TrimSpace(workspace.ExtractPersonaFromRagRules(ragRules)); ragP != "" && ragP != strings.TrimSpace(explicitPersona) {
		parts = append(parts, ragP)
	}
	return strings.Join(parts, "\n")
}

// GenerateProofreading calls Gemini with hybrid inline/File-API context and strict responseSchema
// to produce inline text suggestions, figure/table element suggestions, and the Writing Coach & Learning Summary.
func (c *Client) GenerateProofreading(ctx context.Context, opts ProofreadPromptOptions) (*ProofreadResult, error) {
	lang := NormalizeLanguageCode(opts.Lang)
	isJa := lang == "ja"
	docData := opts.DocData
	if docData == nil {
		return nil, errors.New("ParsedDocument is required for GenerateProofreading")
	}

	visuals := opts.VisualsWithImages
	if len(visuals) == 0 && len(docData.VisualElements) > 0 {
		visuals = docData.VisualElements
	}

	contextParts, uploadedFiles, ragHeading, err := c.buildHybridContextParts(
		ctx,
		opts.RagRules,
		opts.ReferenceFiles,
		visuals,
		opts.TargetFiles,
		isJa,
	)
	if err != nil {
		return nil, err
	}
	defer c.DeleteUploadedFiles(context.Background(), uploadedFiles)

	effectivePersona := ResolveEffectivePersona(opts.Persona, opts.RagRules)
	existingSuggestionsSummary := formatExistingSuggestions(docData.ExistingSuggestions, isJa)
	existingCommentsSummary := formatExistingComments(docData.ExistingComments, isJa)
	var resolvedItems []gdocs.ResolvedReview
	if opts.IncludeResolved {
		resolvedItems = docData.ResolvedReviews
	}
	resolvedReviewsSummary := formatResolvedReviews(resolvedItems, opts.IncludeResolved, isJa)
	tablesSummary := formatTablesSummary(docData.Tables, isJa)
	visualsSummary := formatVisualsSummary(visuals, isJa)
	targetFilesBlock := formatTargetFilesXML(docData, opts.TargetFiles, opts.UseDriveConversion)

	promptText := buildProofreadSystemPrompt(
		lang,
		opts.HintOnly,
		docData.SelectionOnly,
		effectivePersona,
		opts.CustomPrompt,
		ragHeading,
		existingSuggestionsSummary,
		existingCommentsSummary,
		resolvedReviewsSummary,
		tablesSummary,
		visualsSummary,
		targetFilesBlock,
		len(opts.ReferenceFiles) > 0,
	)

	var parts []map[string]any
	parts = append(parts, contextParts...)
	parts = append(parts, map[string]any{"text": promptText})

	genCfg := map[string]any{
		"responseMimeType": "application/json",
		"responseSchema":   buildProofreadResponseSchema(lang),
	}
	if SupportsTemperature(c.model) {
		genCfg["temperature"] = 0.25
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": parts,
			},
		},
		"generationConfig": genCfg,
	}

	rawJSON, finishReason, err := c.callGenerateContentWithFinishReason(ctx, payload)
	if err != nil {
		return nil, err
	}

	var result ProofreadResult
	if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
		if finishReason == "MAX_TOKENS" {
			return nil, fmt.Errorf("Gemini ProofreadResult JSON was truncated at finishReason=MAX_TOKENS [model=%s]; try scoping a smaller range (-t / --range): %w", c.model, err)
		}
		return nil, fmt.Errorf("failed to parse Gemini ProofreadResult JSON: %w (raw: %s)", err, rawJSON)
	}
	return &result, nil
}

// GenerateLearningSummaryFromExisting analyzes existing inline suggestions, open comments, and (optionally)
// resolved review threads on a Google Document without creating new suggestions.
func (c *Client) GenerateLearningSummaryFromExisting(ctx context.Context, opts SummaryPromptOptions) (*SummaryResult, error) {
	lang := NormalizeLanguageCode(opts.Lang)
	isJa := lang == "ja"
	docData := opts.DocData
	if docData == nil {
		return nil, errors.New("ParsedDocument is required for GenerateLearningSummaryFromExisting")
	}

	visuals := opts.VisualsWithImages
	if len(visuals) == 0 && len(docData.VisualElements) > 0 {
		visuals = docData.VisualElements
	}

	contextParts, uploadedFiles, ragHeading, err := c.buildHybridContextParts(
		ctx,
		opts.RagRules,
		opts.ReferenceFiles,
		visuals,
		nil,
		isJa,
	)
	if err != nil {
		return nil, err
	}
	defer c.DeleteUploadedFiles(context.Background(), uploadedFiles)

	effectivePersona := ResolveEffectivePersona(opts.Persona, opts.RagRules)
	existingSuggestionsSummary := formatExistingSuggestions(docData.ExistingSuggestions, isJa)
	existingCommentsSummary := formatExistingComments(docData.ExistingComments, isJa)
	var resolvedItems []gdocs.ResolvedReview
	if opts.IncludeResolved {
		resolvedItems = docData.ResolvedReviews
	}
	resolvedReviewsSummary := formatResolvedReviews(resolvedItems, opts.IncludeResolved, isJa)

	promptText := buildSummarySystemPrompt(
		lang,
		opts.IncludeResolved,
		effectivePersona,
		opts.CustomPrompt,
		ragHeading,
		existingSuggestionsSummary,
		existingCommentsSummary,
		resolvedReviewsSummary,
		docData.AnnotatedText,
	)

	var parts []map[string]any
	parts = append(parts, contextParts...)
	parts = append(parts, map[string]any{"text": promptText})

	genCfg := map[string]any{
		"responseMimeType": "application/json",
		"responseSchema":   buildSummaryResponseSchema(lang),
	}
	if SupportsTemperature(c.model) {
		genCfg["temperature"] = 0.25
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": parts,
			},
		},
		"generationConfig": genCfg,
	}

	rawJSON, finishReason, err := c.callGenerateContentWithFinishReason(ctx, payload)
	if err != nil {
		return nil, err
	}

	var result SummaryResult
	if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
		if finishReason == "MAX_TOKENS" {
			return nil, fmt.Errorf("Gemini SummaryResult JSON was truncated at finishReason=MAX_TOKENS [model=%s]; try scoping a smaller range (--range): %w", c.model, err)
		}
		return nil, fmt.Errorf("failed to parse Gemini SummaryResult JSON: %w (raw: %s)", err, rawJSON)
	}
	return &result, nil
}

// GenerateInitialDraft generates an initial manuscript or code draft for an empty Google Document.
func (c *Client) GenerateInitialDraft(ctx context.Context, opts DraftPromptOptions) (string, error) {
	lang := NormalizeLanguageCode(opts.Lang)
	isJa := lang == "ja"
	if strings.TrimSpace(opts.CustomPrompt) == "" {
		if isJa {
			return "", errors.New("白紙ドキュメントから下書きを生成するには --prompt (-p) でトピックや構成案を指定してください")
		}
		return "", errors.New("please provide a topic or outline via --prompt (-p) to generate an initial draft")
	}

	contextParts, uploadedFiles, ragHeading, err := c.buildHybridContextParts(
		ctx,
		opts.RagRules,
		opts.ReferenceFiles,
		nil,
		nil,
		isJa,
	)
	if err != nil {
		return "", err
	}
	defer c.DeleteUploadedFiles(context.Background(), uploadedFiles)

	effectivePersona := ResolveEffectivePersona(opts.Persona, opts.RagRules)

	var promptText string
	if isJa {
		personaHeader := "あなたはプロフェッショナルな執筆コーチ兼エディターです。"
		if effectivePersona != "" {
			personaHeader = fmt.Sprintf("【AIの位置づけ・立場（カスタムペルソナ）】\n%s\n\n上記の立場・専門性・視点を厳格に保ちながら、執筆コーチ兼エディターとして振る舞ってください。", effectivePersona)
		}
		promptText = fmt.Sprintf(`%s
現在、Google ドキュメントは白紙の状態です。以下のユーザーの指示・メモ（および添付のルールブックや参照コンテキスト）に基づき、Google ドキュメントへ直接挿入するための「構造化された高品質な初稿（たたき台）」を作成してください。%s

### ルール:
- **出力言語の厳守（日本語）**: ユーザーの指示・トピック（Prompt）が英語等で書かれている場合であっても、特別な別言語指定がない限り、生成する初稿の本文は**必ず自然な日本語**で執筆してください。
- 前置きの挨拶（「はい、作成しました」等）やMarkdownのコードブロック記号（`+"```"+`）は一切出力せず、ドキュメント本文としてそのまま使えるテキストのみを出力してください。
- 見出しや段落を適切に分け、後から推敲しやすい論理的な文章構成にしてください。

### ユーザーの指示・トピック:
%s`, personaHeader, ragHeading, opts.CustomPrompt)
	} else {
		personaHeader := "You are an expert writing coach and editor."
		if effectivePersona != "" {
			personaHeader = fmt.Sprintf("[Custom AI Persona & Positioning]\n%s\n\nStrictly embodying the persona, expertise, and perspective defined above, act as an expert writing coach and editor.", effectivePersona)
		}
		langRule := ""
		if lang != "" && lang != "en" && lang != "auto" {
			langRule = fmt.Sprintf("\n- **Strict Output Language (%s)**: Unless explicitly instructed otherwise in the prompt, write the entire draft body in natural, idiomatic **%s**.", LanguageDisplayName(lang), LanguageDisplayName(lang))
		}
		promptText = fmt.Sprintf(`%s
The Google Document is currently blank. Based on the user's instructions/notes below (and any attached organization rulebooks or reference context files), write a well-structured, high-quality initial draft that can be inserted directly into the document.%s

### Rules:%s
- Output ONLY the document body text itself. Do NOT include conversational preambles or markdown code fences (`+"```"+`).
- Organize the draft with clear section headings and logical paragraphs so it is easy to review and refine.

### User Instructions / Topic:
%s`, personaHeader, ragHeading, langRule, opts.CustomPrompt)
	}

	var parts []map[string]any
	parts = append(parts, contextParts...)
	parts = append(parts, map[string]any{"text": promptText})

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": parts,
			},
		},
	}
	if SupportsTemperature(c.model) {
		payload["generationConfig"] = map[string]any{
			"temperature": 0.5,
		}
	}

	rawDraft, finishReason, err := c.callGenerateContentWithFinishReason(ctx, payload)
	if err != nil {
		return "", err
	}
	_ = finishReason
	cleaned := reLeadingCodeFence.ReplaceAllString(rawDraft, "")
	cleaned = reTrailingCodeFence.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned), nil
}

// BuildAgentProofreadPrompt builds the complete coaching prompt and JSON response schema for a host AI agent
// (e.g., Antigravity, Claude Code, Cursor) without invoking the Gemini API or requiring GEMINI_API_KEY.
func BuildAgentProofreadPrompt(opts ProofreadPromptOptions) (string, map[string]any) {
	lang := NormalizeLanguageCode(opts.Lang)
	isJa := lang == "ja"
	docData := opts.DocData
	if docData == nil {
		docData = &gdocs.ParsedDocument{}
	}
	visuals := opts.VisualsWithImages
	if len(visuals) == 0 && len(docData.VisualElements) > 0 {
		visuals = docData.VisualElements
	}
	ragBlock, ragHeading := formatAgentContextPrefix(opts.RagRules, opts.ReferenceFiles, isJa)
	effectivePersona := ResolveEffectivePersona(opts.Persona, opts.RagRules)
	existingSuggestionsSummary := formatExistingSuggestions(docData.ExistingSuggestions, isJa)
	existingCommentsSummary := formatExistingComments(docData.ExistingComments, isJa)
	var resolvedItems []gdocs.ResolvedReview
	if opts.IncludeResolved {
		resolvedItems = docData.ResolvedReviews
	}
	resolvedReviewsSummary := formatResolvedReviews(resolvedItems, opts.IncludeResolved, isJa)
	tablesSummary := formatTablesSummary(docData.Tables, isJa)
	visualsSummary := formatVisualsSummary(visuals, isJa)
	targetFilesBlock := formatTargetFilesXML(docData, opts.TargetFiles, opts.UseDriveConversion)

	promptText := buildProofreadSystemPrompt(
		lang,
		opts.HintOnly,
		docData.SelectionOnly,
		effectivePersona,
		opts.CustomPrompt,
		ragHeading,
		existingSuggestionsSummary,
		existingCommentsSummary,
		resolvedReviewsSummary,
		tablesSummary,
		visualsSummary,
		targetFilesBlock,
		len(opts.ReferenceFiles) > 0,
	)
	if ragBlock != "" {
		promptText = ragBlock + "\n\n" + promptText
	}
	return promptText, buildProofreadResponseSchema(lang)
}

// BuildAgentSummaryPrompt builds the complete learning summary prompt and JSON response schema for a host AI agent
// without invoking the Gemini API or requiring GEMINI_API_KEY.
func BuildAgentSummaryPrompt(opts SummaryPromptOptions) (string, map[string]any) {
	lang := NormalizeLanguageCode(opts.Lang)
	isJa := lang == "ja"
	docData := opts.DocData
	if docData == nil {
		docData = &gdocs.ParsedDocument{}
	}
	ragBlock, ragHeading := formatAgentContextPrefix(opts.RagRules, opts.ReferenceFiles, isJa)
	effectivePersona := ResolveEffectivePersona(opts.Persona, opts.RagRules)
	existingSuggestionsSummary := formatExistingSuggestions(docData.ExistingSuggestions, isJa)
	existingCommentsSummary := formatExistingComments(docData.ExistingComments, isJa)
	var resolvedItems []gdocs.ResolvedReview
	if opts.IncludeResolved {
		resolvedItems = docData.ResolvedReviews
	}
	resolvedReviewsSummary := formatResolvedReviews(resolvedItems, opts.IncludeResolved, isJa)

	promptText := buildSummarySystemPrompt(
		lang,
		opts.IncludeResolved,
		effectivePersona,
		opts.CustomPrompt,
		ragHeading,
		existingSuggestionsSummary,
		existingCommentsSummary,
		resolvedReviewsSummary,
		docData.AnnotatedText,
	)
	if ragBlock != "" {
		promptText = ragBlock + "\n\n" + promptText
	}
	return promptText, buildSummaryResponseSchema(lang)
}

// BuildAgentDraftPrompt builds the initial draft generation instructions for a host AI agent
// without invoking the Gemini API or requiring GEMINI_API_KEY.
func BuildAgentDraftPrompt(opts DraftPromptOptions) string {
	lang := NormalizeLanguageCode(opts.Lang)
	isJa := lang == "ja"
	ragBlock, ragHeading := formatAgentContextPrefix(opts.RagRules, opts.ReferenceFiles, isJa)
	effectivePersona := ResolveEffectivePersona(opts.Persona, opts.RagRules)
	var promptText string
	if isJa {
		personaHeader := "あなたはプロフェッショナルな執筆コーチ兼エディターです。"
		if effectivePersona != "" {
			personaHeader = fmt.Sprintf("【AIの位置づけ・立場（カスタムペルソナ）】\n%s\n\n上記の立場・専門性・視点を厳格に保ちながら、執筆コーチ兼エディターとして振る舞ってください。", effectivePersona)
		}
		promptText = fmt.Sprintf(`%s
以下のユーザーの指示・メモ（および添付のルールブックや参照コンテキスト）に基づき、Google ドキュメントへ直接挿入するための「構造化された高品質な初稿（たたき台）」を作成してください。%s

### ユーザーの指示・トピック:
%s`, personaHeader, ragHeading, opts.CustomPrompt)
	} else {
		personaHeader := "You are an expert writing coach and editor."
		if effectivePersona != "" {
			personaHeader = fmt.Sprintf("[Custom AI Persona & Positioning]\n%s\n\nStrictly embodying the persona, expertise, and perspective defined above, act as an expert writing coach and editor.", effectivePersona)
		}
		langInst := ""
		if lang != "" && lang != "en" && lang != "auto" {
			langInst = fmt.Sprintf("\n(Write the entire generated draft in natural, idiomatic **%s** unless the instructions specify otherwise.)", LanguageDisplayName(lang))
		}
		promptText = fmt.Sprintf(`%s
Based on the user's instructions/notes below (and any attached organization rulebooks or reference context files), write a well-structured, high-quality initial draft that can be inserted directly into the document.%s%s

### User Instructions / Topic:
%s`, personaHeader, ragHeading, langInst, opts.CustomPrompt)
	}
	if ragBlock != "" {
		promptText = ragBlock + "\n\n" + promptText
	}
	return promptText
}

func formatAgentContextPrefix(ragRules []workspace.RagRuleFile, referenceFiles []workspace.FileEntry, isJa bool) (string, string) {
	var sections []string
	ragHeading := ""
	if len(ragRules) > 0 {
		if isJa {
			ragHeading = "\n\n※最優先事項：提供された『組織・プロジェクト独自のガイドライン / ルールブック (RAG)』に厳格に従って推敲・評価を行ってください。"
		} else {
			ragHeading = "\n\n*HIGHEST PRIORITY: Strictly enforce the attached 'Organization / Project Guidelines & Rulebooks (RAG)' when reviewing and suggesting improvements.*"
		}
		var rsb strings.Builder
		rsb.WriteString("<rag_organization_rulebooks>\n")
		for _, r := range ragRules {
			rsb.WriteString(fmt.Sprintf("<rulebook title=%q source=%q id=%q>\n", r.Title, r.SourceType, r.ID))
			if strings.TrimSpace(r.Content) != "" {
				rsb.WriteString(r.Content)
				rsb.WriteString("\n")
			} else if r.ID != "" {
				rsb.WriteString(fmt.Sprintf("[Binary rulebook file at %s — inspect via read_file if needed]\n", r.ID))
			}
			rsb.WriteString("</rulebook>\n")
		}
		rsb.WriteString("</rag_organization_rulebooks>")
		sections = append(sections, rsb.String())
	}
	if len(referenceFiles) > 0 {
		var csb strings.Builder
		csb.WriteString("<reference_context_files readonly=\"true\">\n")
		for _, rf := range referenceFiles {
			csb.WriteString(fmt.Sprintf("<context_file path=%q>\n%s\n</context_file>\n", rf.DisplayPath, rf.Content))
		}
		csb.WriteString("</reference_context_files>")
		sections = append(sections, csb.String())
	}
	return strings.Join(sections, "\n\n"), ragHeading
}

func formatTargetFilesXML(docData *gdocs.ParsedDocument, targetFiles []workspace.FileEntry, useDriveConversion bool) string {
	var sb strings.Builder
	sb.WriteString("<target_files>\n")
	if len(targetFiles) > 0 {
		blocksByFile := make(map[string][]string)
		var allEditableLines []string
		for _, b := range docData.EditableBlocks {
			if !b.TargetEditable {
				continue
			}
			fp := b.FilePath
			if fp == "" {
				fp = "document"
			}
			blocksByFile[fp] = append(blocksByFile[fp], b.Text)
			allEditableLines = append(allEditableLines, b.Text)
		}

		// When documents (.md, .txt, .pdf, .docx) were converted to native Google Docs via Google Drive API,
		// the Google Docs text has native headings, tables (TABLE_N), and inline images (VISUAL_N) without raw
		// Markdown markers (`#`, backticks, etc.). Presenting docData.AnnotatedText / docData.EditableBlocks
		// guarantees that Gemini's `originalText` matches the converted Google Document 100% character-for-character.
		if useDriveConversion && len(targetFiles) == 1 {
			tf := targetFiles[0]
			hasFilter := len(tf.Filters) > 0 || docData.SelectionOnly
			sb.WriteString(fmt.Sprintf("<target_file path=%q converted_to_gdocs=\"true\" partial_scope=%q>\n", tf.DisplayPath, fmt.Sprintf("%v", hasFilter)))
			if hasFilter {
				editableLines := blocksByFile[tf.DisplayPath]
				if len(editableLines) == 0 {
					editableLines = allEditableLines
				}
				sb.WriteString("<editable_target_scope>\n")
				sb.WriteString(strings.Join(editableLines, "\n"))
				sb.WriteString("\n</editable_target_scope>\n")
				sb.WriteString("<full_file_context_readonly>\n")
				if docData.AnnotatedText != "" {
					sb.WriteString(docData.AnnotatedText)
				} else {
					sb.WriteString(tf.Content)
				}
				sb.WriteString("\n</full_file_context_readonly>\n")
			} else {
				if docData.AnnotatedText != "" {
					sb.WriteString(docData.AnnotatedText)
				} else {
					sb.WriteString(strings.Join(allEditableLines, "\n"))
				}
				sb.WriteString("\n")
			}
			sb.WriteString("</target_file>\n")
		} else {
			for _, tf := range targetFiles {
				hasFilter := len(tf.Filters) > 0
				sb.WriteString(fmt.Sprintf("<target_file path=%q partial_scope=%q>\n", tf.DisplayPath, fmt.Sprintf("%v", hasFilter)))
				if hasFilter {
					editableLines := blocksByFile[tf.DisplayPath]
					sb.WriteString("<editable_target_scope>\n")
					sb.WriteString(strings.Join(editableLines, "\n"))
					sb.WriteString("\n</editable_target_scope>\n")
					sb.WriteString("<full_file_context_readonly>\n")
					sb.WriteString(tf.Content)
					sb.WriteString("\n</full_file_context_readonly>\n")
				} else if useDriveConversion && len(blocksByFile[tf.DisplayPath]) > 0 {
					sb.WriteString(strings.Join(blocksByFile[tf.DisplayPath], "\n"))
					sb.WriteString("\n")
				} else {
					sb.WriteString(tf.Content)
					sb.WriteString("\n")
				}
				sb.WriteString("</target_file>\n")
			}
		}
	} else {
		sb.WriteString("<target_document>\n")
		sb.WriteString(docData.AnnotatedText)
		sb.WriteString("\n</target_document>\n")
	}
	sb.WriteString("</target_files>")
	return sb.String()
}

func formatExistingSuggestions(items []gdocs.ExistingSuggestion, isJa bool) string {
	if len(items) == 0 {
		if isJa {
			return "現在、未解決の提案（Suggested Edits）はありません。"
		}
		return "No pending inline suggestions."
	}
	var lines []string
	for idx, s := range items {
		del := s.DeletedText
		if del == "" {
			if isJa {
				del = "(なし)"
			} else {
				del = "(none)"
			}
		}
		ins := s.InsertedText
		if ins == "" {
			if isJa {
				ins = "(なし)"
			} else {
				ins = "(none)"
			}
		}
		var commentNotes []string
		for _, c := range s.Comments {
			var replyParts []string
			for _, r := range c.Replies {
				replyParts = append(replyParts, fmt.Sprintf("%s: %q", r.Author, r.Content))
			}
			repStr := ""
			if len(replyParts) > 0 {
				repStr = " [Replies: " + strings.Join(replyParts, " / ") + "]"
			}
			commentNotes = append(commentNotes, fmt.Sprintf("%s: %q%s", c.Author, c.Content, repStr))
		}
		cSuffix := ""
		if len(commentNotes) > 0 {
			cSuffix = " | Comments: " + strings.Join(commentNotes, "; ")
		}
		provLabel := "Human Reviewer"
		if s.IsAIGenerated {
			provLabel = "DocuMentor AI"
		}
		if isJa {
			if s.IsAIGenerated {
				provLabel = "AI推敲コーチ"
			} else {
				provLabel = "人間のレビュアー"
			}
			lines = append(lines, fmt.Sprintf("%d. [ID: %s | %s] 削除候補: %q -> 挿入候補: %q%s", idx+1, s.ID, provLabel, del, ins, cSuffix))
		} else {
			lines = append(lines, fmt.Sprintf("%d. [ID: %s | %s] Delete: %q -> Insert: %q%s", idx+1, s.ID, provLabel, del, ins, cSuffix))
		}
	}
	return strings.Join(lines, "\n")
}

func formatExistingComments(items []gdocs.ActiveComment, isJa bool) string {
	var standalone []gdocs.ActiveComment
	for _, c := range items {
		if c.SuggestionID == "" {
			standalone = append(standalone, c)
		}
	}
	if len(standalone) == 0 {
		if isJa {
			return "現在、独立した未解決コメントはありません。"
		}
		return "No standalone open comments."
	}
	var lines []string
	for idx, c := range standalone {
		var replyParts []string
		for _, r := range c.Replies {
			replyParts = append(replyParts, fmt.Sprintf("%s: %q", r.Author, r.Content))
		}
		repStr := ""
		if len(replyParts) > 0 {
			repStr = " | Replies: " + strings.Join(replyParts, " / ")
		}
		quote := ""
		if c.QuotedText != "" {
			if isJa {
				quote = fmt.Sprintf(" (対象テキスト: %q)", c.QuotedText)
			} else {
				quote = fmt.Sprintf(" (Quoted: %q)", c.QuotedText)
			}
		}
		lines = append(lines, fmt.Sprintf("%d. [Comment ID: %s]%s %s: %q%s", idx+1, c.CommentID, quote, c.Author, c.Content, repStr))
	}
	return strings.Join(lines, "\n")
}

func formatResolvedReviews(items []gdocs.ResolvedReview, includeResolved bool, isJa bool) string {
	if !includeResolved {
		if isJa {
			return "（解決済み履歴の参照はOFFに設定されています）"
		}
		return "(Resolved review history is excluded by setting.)"
	}
	if len(items) == 0 {
		if isJa {
			return "解決済みの提案・コメント履歴はありません。"
		}
		return "No resolved suggestions or comments."
	}
	var lines []string
	for idx, r := range items {
		var replyParts []string
		for _, rep := range r.Replies {
			act := ""
			if rep.Action != "" {
				act = fmt.Sprintf(" (%s)", rep.Action)
			}
			replyParts = append(replyParts, fmt.Sprintf("%s%s: %q", rep.Author, act, rep.Content))
		}
		repStr := ""
		if len(replyParts) > 0 {
			repStr = " | Thread: " + strings.Join(replyParts, " / ")
		}
		quote := ""
		if r.QuotedText != "" {
			quote = fmt.Sprintf(" (Target: %q)", r.QuotedText)
		}
		lines = append(lines, fmt.Sprintf("%d. [%s | %s]%s %s: %q%s", idx+1, r.ThreadType, r.ResolutionState, quote, r.Author, r.Content, repStr))
	}
	return strings.Join(lines, "\n")
}

func formatTablesSummary(tables []gdocs.TableElement, isJa bool) string {
	if len(tables) == 0 {
		if isJa {
			return "表（テーブル）は含まれていません。"
		}
		return "No tables in the target scope."
	}
	var parts []string
	for _, t := range tables {
		parts = append(parts, fmt.Sprintf("- %s (%d rows, %d cols):\n%s", t.TargetID, t.RowsCount, t.ColsCount, t.Markdown))
	}
	return strings.Join(parts, "\n\n")
}

func formatVisualsSummary(visuals []gdocs.VisualElement, isJa bool) string {
	if len(visuals) == 0 {
		if isJa {
			return "図やグラフ（VISUAL要素）は含まれていません。"
		}
		return "No figures or charts in the target scope."
	}
	var parts []string
	for _, v := range visuals {
		hasImg := len(v.ImageBytes) > 0
		parts = append(parts, fmt.Sprintf("- %s (%s): Title=%q, Description=%q, ImageAttached=%v", v.TargetID, v.Type, v.Title, v.Description, hasImg))
	}
	return strings.Join(parts, "\n")
}

func buildProofreadSystemPrompt(
	lang string,
	hintOnly bool,
	selectionOnly bool,
	effectivePersona string,
	customPrompt string,
	ragHeading string,
	existingSuggestionsSummary string,
	existingCommentsSummary string,
	resolvedReviewsSummary string,
	tablesSummary string,
	visualsSummary string,
	targetFilesBlock string,
	hasReferenceFiles bool,
) string {
	normLang := NormalizeLanguageCode(lang)
	isJa := normLang == "ja"
	if isJa {
		personaHeader := "あなたは熟練のシニア編集者・ソフトウェアアーキテクト兼ライティングコーチ（DocuMentor AI）です。"
		if strings.TrimSpace(effectivePersona) != "" {
			personaHeader = fmt.Sprintf("【AIの位置づけ・立場（カスタムペルソナ）】\n%s\n\n上記のAIの立場・専門性・視点・語調を最優先で体現しながら、DocuMentor AIの推敲・レビューコーチとして振る舞ってください。", strings.TrimSpace(effectivePersona))
		}

		modeInst := "各提案には修正後の具体的な文字列（`suggestedText`）と、なぜそう直すべきかの解説（`reason`）、今後活かせるライティング/設計のコツ（`learningPoint`）を必ず含めてください。"
		if hintOnly {
			modeInst = "【ヒントのみモード（ソクラテス式コーチング）】直接答え（書き換え文）を教えるのではなく、書き手が自分で考えて推敲できるよう、`reason`（なぜ見直しが必要か）と `learningPoint`（どう書き直すべきかの具体的なヒントや問いかけ）を充実させてください（`suggestedText` は参考案または空文字でも構いません）。"
		}
		scopeInst := ""
		if selectionOnly {
			scopeInst = "\n- **部分スコープ厳守**: `<editable_target_scope>` 内のテキストのみから `originalText` を抽出してください。`<full_file_context_readonly>` や `<reference_context_files>` 内のテキストは文脈・依存関係の理解専用（Read-Only）であり、そこに対する `suggestions` は絶対に出力しないでください。"
		} else if hasReferenceFiles {
			scopeInst = "\n- **参照コンテキストの区別**: `<reference_context_files>` 内のファイルは依存関係・整合性チェック用のRead-Onlyコンテキストです。`suggestions` の `originalText` は必ず `<target_files>` 内の編集対象ファイルから抽出してください。"
		}
		customBlock := ""
		if strings.TrimSpace(customPrompt) != "" {
			customBlock = fmt.Sprintf("\n\n### 🎯 ユーザーからの追加重点指示:\n%s", customPrompt)
		}

		return fmt.Sprintf(`%s
提供された原稿またはソースコードを推敲・レビューし、Google ドキュメント上で直接適用可能なインライン提案（またはヒントコメント）および「Writing Coach & Learning Summary」をJSON形式で出力してください。%s%s

### 厳守すべき出力ルール:
0. **【最重要】解説・講評・理由の言語統一（100%% 日本語出力の徹底）**:
   - 推敲対象の原稿やWeb記事、ソースコードが**英語やプログラミング言語で書かれている場合であっても**、原稿の抜粋である `+"`originalText`"+` と置換後の `+"`suggestedText`"+`（原稿・コードと同じ言語を維持）を除く**すべての説明文・講評・要約・カテゴリ名・理由・学習ポイント（`+"`overallFeedback`"+`, `+"`learningSummary.revisionsDigest`"+`, `+"`learningSummary.weakPointsAndHabits`"+`, `+"`learningSummary.actionableTips`"+`, `+"`existingSuggestionsNote`"+`, `+"`suggestions[].category`"+`, `+"`suggestions[].reason`"+`, `+"`suggestions[].learningPoint`"+`, `+"`elementSuggestions[].category`"+`, `+"`elementSuggestions[].suggestion`"+`, `+"`elementSuggestions[].reason`"+`, `+"`elementSuggestions[].learningPoint`"+`）は、必ず100%%自然な日本語で記述してください**。説明文を英語のまま出力することは厳禁です。
1. **正確な部分一致 (originalText)**:
   - originalText は <target_files> 内の確定済み本文（または <editable_target_scope>）に**1文字の狂いもなく完全一致する連続した部分文字列（1行内のフレーズまたは文）**でなければなりません。
   - 改行コード（\n）を跨ぐ複数行の文字列や、"=== [File: ...] ===" というファイルヘッダー行、"[VISUAL_1 ...]" や "[TABLE_1 ...]" といった要素マーカー、既存の提案マーカー（"[Existing Suggestion ...]"）を originalText に含めないでください。
   - 置換箇所が特定しやすいよう、1単語だけでなく前後の文脈を適度に含んだユニークなフレーズ（10〜120文字程度）を originalText に指定してください。%s
2. **モード指示**:
   - %s
3. **図・グラフ・表へのマルチモーダル推敲 (elementSuggestions)**:
   - 原稿内に VISUAL_1 等の図や TABLE_1 等の表がある場合、添付された実画像やテーブル構造と本文との整合性、キャプションやラベルの明確さ、データの読み取りやすさ等を検証し、改善点があれば elementSuggestions に targetId（例: "VISUAL_1", "TABLE_1"）を指定して**日本語で**出力してください。
4. **既存レビュー履歴との調和**:
   - 既存の未解決提案・コメントと重複・矛盾する提案は避け、必要に応じて existingSuggestionsNote で**日本語で**言及してください。
5. **Writing Coach & Learning Summary（すべて日本語で記述）**:
   - overallFeedback: 原稿・コード全体の良い点と改善の方向性を温かく論理的に**日本語で**総括。
   - learningSummary.revisionsDigest: 今回の主な推敲・レビュー内容の要約（**日本語**）。
   - learningSummary.weakPointsAndHabits: 書き手の文章・コードに見られる傾向やクセ（2〜4項目、**日本語**）。
   - learningSummary.actionableTips: 次回の執筆・実装からすぐ実践できる具体的なアドバイス（2〜4項目、**日本語**）。

### 既存の未解決提案（Suggested Edits）:
%s

### 既存の未解決コメント:
%s

### 解決済みレビュー履歴:
%s

### 対象内の表（Tables）:
%s

### 対象内の図・グラフ（Visual Elements）:
%s

### 推敲対象データ:
%s`, personaHeader, ragHeading, customBlock, scopeInst, modeInst, existingSuggestionsSummary, existingCommentsSummary, resolvedReviewsSummary, tablesSummary, visualsSummary, targetFilesBlock)
	}

	personaHeader := "You are an expert senior editor, software architect, and writing coach (DocuMentor AI)."
	if strings.TrimSpace(effectivePersona) != "" {
		personaHeader = fmt.Sprintf("[Custom AI Persona & Positioning]\n%s\n\nStrictly embodying the persona, expertise, perspective, and voice defined above, act as the DocuMentor AI writing and code review coach.", strings.TrimSpace(effectivePersona))
	}

	modeInst := "For each suggestion, provide the exact replacement (`suggestedText`), a clear explanation of why the change improves clarity, accuracy, or architecture (`reason`), and a reusable writing/engineering takeaway (`learningPoint`)."
	if hintOnly {
		modeInst = "[Hint-Only Socratic Coaching Mode] Instead of rewriting the text for the author, focus on rich coaching guidance in `reason` (what issue needs attention) and `learningPoint` (actionable hints and guiding questions so the author can revise it themselves)."
	}
	scopeInst := ""
	if selectionOnly {
		scopeInst = "\n- **Strict Partial Scope Boundary**: Extract `originalText` ONLY from `<editable_target_scope>`. Text inside `<full_file_context_readonly>` or `<reference_context_files>` is strictly read-only context for dependency and consistency verification—never emit `suggestions` targeting read-only context."
	} else if hasReferenceFiles {
		scopeInst = "\n- **Read-Only Reference Context**: Files inside `<reference_context_files>` are provided solely for cross-file symbol, API, and terminology consistency. Every `originalText` in `suggestions` MUST come strictly from `<target_files>`."
	}
	customBlock := ""
	if strings.TrimSpace(customPrompt) != "" {
		customBlock = fmt.Sprintf("\n\n### 🎯 Custom Focus Instructions from User:\n%s", customPrompt)
	}
	langRule0 := ""
	if normLang != "" && normLang != "en" {
		langDisplay := LanguageDisplayName(normLang)
		langRule0 = fmt.Sprintf(`0. **[CRITICAL] Output Language for Coaching Explanations (100%% %s)**:
   - While `+"`originalText`"+` and `+"`suggestedText`"+` MUST remain in the exact language/syntax of the target manuscript or source code, **ALL coaching explanations, summaries, categories, reasons, and learning points (`+"`overallFeedback`"+`, `+"`learningSummary.revisionsDigest`"+`, `+"`learningSummary.weakPointsAndHabits`"+`, `+"`learningSummary.actionableTips`"+`, `+"`existingSuggestionsNote`"+`, `+"`suggestions[].category`"+`, `+"`suggestions[].reason`"+`, `+"`suggestions[].learningPoint`"+`, `+"`elementSuggestions[].category`"+`, `+"`elementSuggestions[].suggestion`"+`, `+"`elementSuggestions[].reason`"+`, `+"`elementSuggestions[].learningPoint`"+`) MUST be written in natural, idiomatic %s**.
`, langDisplay, langDisplay)
	}

	return fmt.Sprintf(`%s
Review and proofread the provided manuscript or source code, and output structured inline suggestions (or coaching hints) along with a comprehensive "Writing Coach & Learning Summary" in JSON format.%s%s

### Strict Output Rules:
%s1. **Exact Substring Matching (originalText)**:
   - Each originalText MUST be an **exact, character-for-character substring within a single line** of the settled text in <target_files> (or <editable_target_scope> when partial scope is active).
   - NEVER span across newline characters (\n), NEVER include "=== [File: ...] ===" file separator headers, NEVER include "[VISUAL_1 ...]" or "[TABLE_1 ...]" structural markers, and NEVER include "[Existing Suggestion ...]" markers inside originalText.
   - Include enough surrounding words (typically 15–120 characters) so the phrase is uniquely identifiable within its line.%s
2. **Coaching Mode**:
   - %s
3. **Multimodal Figure, Chart & Table Review (elementSuggestions)**:
   - When figures/charts (VISUAL_1, etc.) or tables (TABLE_1, etc.) are present, inspect their attached image data and table structure to verify consistency with the body text, label clarity, and data presentation, and emit targeted feedback in elementSuggestions using their exact targetId.
4. **Harmony with Existing Review Threads**:
   - Do not duplicate or contradict existing unresolved suggestions or comments; summarize how your review complements them in existingSuggestionsNote.
5. **Writing Coach & Learning Summary**:
   - overallFeedback: Encouraging, high-signal executive summary of strengths and key improvements.
   - learningSummary.revisionsDigest: Concise digest of the primary revisions made in this pass.
   - learningSummary.weakPointsAndHabits: 2–4 recurring writing or coding habits observed in the work.
   - learningSummary.actionableTips: 2–4 concrete, actionable best practices for future writing or development.

### Existing Pending Suggestions:
%s

### Existing Open Comments:
%s

### Resolved Review History:
%s

### Tables in Scope:
%s

### Visual Elements (Figures / Charts) in Scope:
%s

### Target Manuscript / Code to Proofread:
%s`, personaHeader, ragHeading, customBlock, langRule0, scopeInst, modeInst, existingSuggestionsSummary, existingCommentsSummary, resolvedReviewsSummary, tablesSummary, visualsSummary, targetFilesBlock)
}

func buildSummarySystemPrompt(
	lang string,
	includeResolved bool,
	effectivePersona string,
	customPrompt string,
	ragHeading string,
	existingSuggestionsSummary string,
	existingCommentsSummary string,
	resolvedReviewsSummary string,
	annotatedText string,
) string {
	normLang := NormalizeLanguageCode(lang)
	isJa := normLang == "ja"
	customBlock := ""
	if strings.TrimSpace(customPrompt) != "" {
		if isJa {
			customBlock = fmt.Sprintf("\n\n### 🎯 ユーザーからの追加指示:\n%s", customPrompt)
		} else {
			customBlock = fmt.Sprintf("\n\n### 🎯 Additional User Instructions:\n%s", customPrompt)
		}
	}

	if isJa {
		personaHeader := "あなたは熟練のライティングコーチ兼シニアエディター（DocuMentor AI）です。"
		if strings.TrimSpace(effectivePersona) != "" {
			personaHeader = fmt.Sprintf("【AIの位置づけ・立場（カスタムペルソナ）】\n%s\n\n上記のAIの立場・専門性・視点・語調を最優先で体現しながら、DocuMentor AIのライティングコーチとして振る舞ってください。", strings.TrimSpace(effectivePersona))
		}
		resolvedNote := "未解決の提案および未解決のコメント"
		if includeResolved {
			resolvedNote = "未解決の提案・コメント、および解決済み（承認・却下・解決）のレビュー履歴"
		}
		return fmt.Sprintf(`%s
Google ドキュメント上に記録されている「%s」を総合的に分析し、書き手が自身の強み・弱点・改善パターンを体系的に学べる「Writing Coach & Learning Summary」および各レビュー項目の解説（`+"`analyzedItems`"+`）をJSON形式で作成してください。
※【言語指定の厳守（100%% 日本語）】原稿本文や既存の提案・コメントが英語等で書かれている場合であっても、出力する `+"`overallFeedback`"+`, `+"`learningSummary.*`"+`, および `+"`analyzedItems`"+` の `+"`category`"+`, `+"`reason`"+`, `+"`learningPoint`"+`, `+"`proposedChangeOrComment`"+` のすべての説明文は**必ず100%%自然な日本語**で記述してください。%s%s

### 既存の未解決提案（Suggested Edits）:
%s

### 既存の未解決コメント:
%s

### 解決済みレビュー履歴:
%s

### ドキュメント本文（参照用）:
%s`, personaHeader, resolvedNote, ragHeading, customBlock, existingSuggestionsSummary, existingCommentsSummary, resolvedReviewsSummary, annotatedText)
	}

	personaHeader := "You are an expert writing coach and senior editor (DocuMentor AI)."
	if strings.TrimSpace(effectivePersona) != "" {
		personaHeader = fmt.Sprintf("[Custom AI Persona & Positioning]\n%s\n\nStrictly embodying the persona, expertise, and perspective defined above, act as the DocuMentor AI writing coach.", strings.TrimSpace(effectivePersona))
	}
	resolvedNote := "open inline suggestions and open comment threads"
	if includeResolved {
		resolvedNote = "open inline suggestions, open comment threads, and resolved/accepted/rejected review history"
	}
	langDisplay := LanguageDisplayName(normLang)
	return fmt.Sprintf(`%s
Analyze the %s recorded on this Google Document and synthesize a comprehensive "Writing Coach & Learning Summary" along with an item-by-item educational breakdown (`+"`analyzedItems`"+`) in JSON format.
Note: Write all explanatory fields (`+"`overallFeedback`"+`, `+"`learningSummary.*`"+`, `+"`analyzedItems[].category`"+`, `+"`reason`"+`, `+"`learningPoint`"+`) in clear, natural %s.%s%s

### Existing Pending Suggestions:
%s

### Existing Open Comments:
%s

### Resolved Review History:
%s

### Document Text (Reference):
%s`, personaHeader, resolvedNote, langDisplay, ragHeading, customBlock, existingSuggestionsSummary, existingCommentsSummary, resolvedReviewsSummary, annotatedText)
}

func buildProofreadResponseSchema(lang string) map[string]any {
	normLang := NormalizeLanguageCode(lang)
	isJa := normLang == "ja"
	langDisplay := LanguageDisplayName(normLang)
	descOverall := fmt.Sprintf("High-level encouraging coaching summary of the manuscript or code (in %s).", langDisplay)
	descDigest := fmt.Sprintf("Concise summary of the key revisions and why they matter (in %s).", langDisplay)
	descHabits := fmt.Sprintf("Recurring writing or coding habits/weaknesses observed (in %s).", langDisplay)
	descTips := fmt.Sprintf("Concrete, actionable tips the author can apply immediately (in %s).", langDisplay)
	descExisting := fmt.Sprintf("Note on how existing suggestions/comments were respected or complemented (in %s).", langDisplay)
	descOrig := "Exact single-line substring from the editable target text to be replaced or annotated."
	descSug := "Proposed replacement text (keep in the same language/code syntax as originalText)."
	descCat := fmt.Sprintf("Category label in %s (e.g., Clarity, Grammar, Style Rule, Bug Risk, Naming, Performance).", langDisplay)
	descReason := fmt.Sprintf("Clear explanation in %s of why this revision is recommended.", langDisplay)
	descLearn := fmt.Sprintf("Educational takeaway or Socratic coaching hint in %s for the author.", langDisplay)
	descElCat := fmt.Sprintf("Review category in %s for the figure, chart, or table.", langDisplay)
	descElSug := fmt.Sprintf("Specific improvement suggestion in %s for the figure, chart, or table.", langDisplay)
	descElReason := fmt.Sprintf("Why this improvement helps readers understand the visual or tabular data (in %s).", langDisplay)
	descElLearn := fmt.Sprintf("Data visualization or table design takeaway in %s.", langDisplay)

	if isJa {
		descOverall = "【必ず日本語で記述】原稿またはソースコード全体に対する温かく論理的な全体総評（英語での記述は不可）。"
		descDigest = "【必ず日本語で記述】今回の主な推敲・改善ポイントと、なぜ重要かの要約（英語での記述は不可）。"
		descHabits = "【必ず日本語で記述】書き手の文章・コードに見られる傾向やクセ・弱点（各項目を必ず日本語で記述）。"
		descTips = "【必ず日本語で記述】次回の執筆・実装からすぐ実践できる具体的なアドバイス（各項目を必ず日本語で記述）。"
		descExisting = "【必ず日本語で記述】既存の提案・コメント履歴をどのように尊重・補完したかの補足メモ。"
		descOrig = "編集対象テキスト（<target_files> または <editable_target_scope>）内の1行から1文字も違わず抜粋した置換・指摘対象の文字列。"
		descSug = "置換後の修正案テキスト（原稿やソースコードと同じ言語・構文を維持すること）。"
		descCat = "【必ず日本語で記述】指摘カテゴリ名（例: 冗長表現, 文法・語法, 明確さ, 用語統一, バグの懸念, セキュリティ, 命名規則）。"
		descReason = "【必ず日本語で記述】なぜこの修正・見直しを推奨するのかの明確な理由（原稿が英語でも解説は必ず日本語で記述すること）。"
		descLearn = "【必ず日本語で記述】書き手が次回から活かせる学習ポイント、またはソクラテス式の推敲ヒント（必ず日本語で記述すること）。"
		descElCat = "【必ず日本語で記述】図・グラフ・表のレビューカテゴリ名（例: 本文との整合性, 視認性, ラベル・単位の明確化）。"
		descElSug = "【必ず日本語で記述】図・グラフ・表に対する具体的な改善提案（必ず日本語で記述すること）。"
		descElReason = "【必ず日本語で記述】なぜその図表改善が読者の理解を助けるのかの理由（必ず日本語で記述すること）。"
		descElLearn = "【必ず日本語で記述】データ可視化や表設計における学習ポイント（必ず日本語で記述すること）。"
	}

	return map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"overallFeedback": map[string]any{
				"type":        "STRING",
				"description": descOverall,
			},
			"learningSummary": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"revisionsDigest": map[string]any{
						"type":        "STRING",
						"description": descDigest,
					},
					"weakPointsAndHabits": map[string]any{
						"type": "ARRAY",
						"items": map[string]any{
							"type": "STRING",
						},
						"description": descHabits,
					},
					"actionableTips": map[string]any{
						"type": "ARRAY",
						"items": map[string]any{
							"type": "STRING",
						},
						"description": descTips,
					},
				},
				"required": []string{"revisionsDigest", "weakPointsAndHabits", "actionableTips"},
			},
			"existingSuggestionsNote": map[string]any{
				"type":        "STRING",
				"description": descExisting,
			},
			"suggestions": map[string]any{
				"type": "ARRAY",
				"items": map[string]any{
					"type": "OBJECT",
					"properties": map[string]any{
						"originalText": map[string]any{
							"type":        "STRING",
							"description": descOrig,
						},
						"suggestedText": map[string]any{
							"type":        "STRING",
							"description": descSug,
						},
						"category": map[string]any{
							"type":        "STRING",
							"description": descCat,
						},
						"reason": map[string]any{
							"type":        "STRING",
							"description": descReason,
						},
						"learningPoint": map[string]any{
							"type":        "STRING",
							"description": descLearn,
						},
					},
					"required": []string{"originalText", "suggestedText", "category", "reason", "learningPoint"},
				},
			},
			"elementSuggestions": map[string]any{
				"type": "ARRAY",
				"items": map[string]any{
					"type": "OBJECT",
					"properties": map[string]any{
						"targetId": map[string]any{
							"type":        "STRING",
							"description": "Identifier of the visual element or table (e.g., VISUAL_1, TABLE_1).",
						},
						"category": map[string]any{
							"type":        "STRING",
							"description": descElCat,
						},
						"suggestion": map[string]any{
							"type":        "STRING",
							"description": descElSug,
						},
						"reason": map[string]any{
							"type":        "STRING",
							"description": descElReason,
						},
						"learningPoint": map[string]any{
							"type":        "STRING",
							"description": descElLearn,
						},
					},
					"required": []string{"targetId", "category", "suggestion", "reason", "learningPoint"},
				},
			},
		},
		"required": []string{
			"overallFeedback",
			"learningSummary",
			"existingSuggestionsNote",
			"suggestions",
			"elementSuggestions",
		},
	}
}

func buildSummaryResponseSchema(lang string) map[string]any {
	normLang := NormalizeLanguageCode(lang)
	isJa := normLang == "ja"
	langDisplay := LanguageDisplayName(normLang)
	descOverall := fmt.Sprintf("Overall coaching summary in %s.", langDisplay)
	descDigest := fmt.Sprintf("Concise summary of key revisions in %s.", langDisplay)
	descHabits := fmt.Sprintf("Observed writing/coding habits in %s.", langDisplay)
	descTips := fmt.Sprintf("Actionable tips in %s.", langDisplay)
	descCat := fmt.Sprintf("Category in %s.", langDisplay)
	descReason := fmt.Sprintf("Explanation in %s of why the change/comment matters.", langDisplay)
	descLearn := fmt.Sprintf("Learning point in %s.", langDisplay)
	if isJa {
		descOverall = "【必ず日本語で記述】全体総評。"
		descDigest = "【必ず日本語で記述】主な推敲・改善ポイントの要約。"
		descHabits = "【必ず日本語で記述】見受けられた文章・コードの傾向やクセ。"
		descTips = "【必ず日本語で記述】次回からすぐ実践できる具体的なアドバイス。"
		descCat = "【必ず日本語で記述】指摘カテゴリ名。"
		descReason = "【必ず日本語で記述】修正・コメントの理由と背景の解説。"
		descLearn = "【必ず日本語で記述】次回から活かせる学習ポイント。"
	}
	return map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"overallFeedback": map[string]any{
				"type":        "STRING",
				"description": descOverall,
			},
			"learningSummary": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"revisionsDigest": map[string]any{
						"type":        "STRING",
						"description": descDigest,
					},
					"weakPointsAndHabits": map[string]any{
						"type":        "ARRAY",
						"items":       map[string]any{"type": "STRING"},
						"description": descHabits,
					},
					"actionableTips": map[string]any{
						"type":        "ARRAY",
						"items":       map[string]any{"type": "STRING"},
						"description": descTips,
					},
				},
				"required": []string{"revisionsDigest", "weakPointsAndHabits", "actionableTips"},
			},
			"analyzedItems": map[string]any{
				"type": "ARRAY",
				"items": map[string]any{
					"type": "OBJECT",
					"properties": map[string]any{
						"id":                      map[string]any{"type": "STRING"},
						"sourceType":              map[string]any{"type": "STRING"},
						"targetPreview":           map[string]any{"type": "STRING"},
						"proposedChangeOrComment": map[string]any{"type": "STRING"},
						"category":                map[string]any{"type": "STRING", "description": descCat},
						"reason":                  map[string]any{"type": "STRING", "description": descReason},
						"learningPoint":           map[string]any{"type": "STRING", "description": descLearn},
					},
					"required": []string{
						"id",
						"sourceType",
						"targetPreview",
						"proposedChangeOrComment",
						"category",
						"reason",
						"learningPoint",
					},
				},
			},
		},
		"required": []string{"overallFeedback", "learningSummary", "analyzedItems"},
	}
}
