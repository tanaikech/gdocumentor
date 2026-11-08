package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tanaikech/gdocumentor/internal/gdocs"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
	"github.com/tanaikech/gdocumentor/internal/workspace"
)

var reSafeFilename = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// resolveLocalOutputPath determines the local file path where proofread/hint/draft/summary results will be written.
// If opts.OutputPath (-o / --output) is explicitly set, it is used directly (in both Access-Token and --no-auth modes).
// Otherwise, when opts.NoAuth (--no-auth) or opts.SaveLocal (--save-local) is true, a deterministic default filename
// in WorkDir (or current dir) is returned.
func resolveLocalOutputPath(opts *Options, bundle *workspace.Bundle, docID string, isDraft bool, isSummary bool, hintOnly bool) string {
	explicit := workspace.NormalizeCrossPlatformPath(strings.TrimSpace(opts.OutputPath))
	workDir := workspace.NormalizeCrossPlatformPath(strings.TrimSpace(opts.WorkDir))
	if explicit != "" {
		if filepath.IsAbs(explicit) || workDir == "" {
			return explicit
		}
		return filepath.Join(workDir, explicit)
	}
	if !opts.NoAuth && !opts.SaveLocal {
		return ""
	}

	baseDir := workDir
	if baseDir == "" {
		baseDir = "."
	}

	if isDraft {
		return filepath.Join(baseDir, "gdocumentor_draft.md")
	}

	suffix := "_proofread.md"
	if isSummary {
		suffix = "_summary.md"
	} else if hintOnly {
		suffix = "_hint.md"
	}

	stem := "gdocumentor"
	if bundle != nil && len(bundle.TargetFiles) > 0 {
		disp := filepath.Base(bundle.TargetFiles[0].DisplayPath)
		ext := filepath.Ext(disp)
		rawStem := strings.TrimSuffix(disp, ext)
		cleaned := strings.Trim(reSafeFilename.ReplaceAllString(rawStem, "_"), "_.")
		if cleaned != "" && cleaned != "stdin" {
			stem = cleaned
		}
	} else if strings.TrimSpace(docID) != "" {
		cleaned := strings.Trim(reSafeFilename.ReplaceAllString(strings.TrimSpace(docID), "_"), "_.")
		if cleaned != "" {
			stem = cleaned
		}
	}
	return filepath.Join(baseDir, stem+suffix)
}

// buildLocalParsedDocument constructs a gdocs.ParsedDocument and multimodal VisualElement slice
// purely in memory from the local/URL workspace bundle (for --no-auth / Gemini-only mode).
func buildLocalParsedDocument(bundle *workspace.Bundle, title string) (*gdocs.ParsedDocument, []gdocs.VisualElement) {
	var editableBlocks []utf16idx.EditableBlock
	var selectedLines []string

	for _, b := range bundle.EditableBlocks {
		editableBlocks = append(editableBlocks, b)
		if b.TargetEditable && strings.TrimSpace(b.Text) != "" {
			selectedLines = append(selectedLines, b.Text)
		}
	}

	annotatedText := bundle.CombinedDocText
	selectedPreview := ""
	if bundle.HasPartialScope && len(selectedLines) > 0 {
		selectedPreview = strings.Join(selectedLines, "\n")
		annotatedText = selectedPreview
	}

	// Extract Markdown tables (TABLE_1, TABLE_2, ...) from CombinedDocText so Gemini can review tables in --no-auth mode
	tables := extractMarkdownTablesFromLocalText(bundle.CombinedDocText)

	// Convert embedded Markdown images into VisualElement (VISUAL_1, VISUAL_2, ...) with binary bytes for Gemini File API
	var visuals []gdocs.VisualElement
	for idx, img := range bundle.EmbeddedImages {
		if len(img.Data) == 0 {
			continue
		}
		targetID := fmt.Sprintf("VISUAL_%d", idx+1)
		startIdx := 1
		endIdx := 2
		if len(editableBlocks) > 0 {
			startIdx = editableBlocks[0].StartIndex
			endIdx = editableBlocks[0].EndIndex
		}
		visuals = append(visuals, gdocs.VisualElement{
			TargetID:       targetID,
			ObjectID:       targetID,
			Type:           "Figure/Image",
			Title:          img.AltText,
			Description:    img.OriginalRef,
			StartIndex:     startIdx,
			EndIndex:       endIdx,
			ParaStartIndex: startIdx,
			ParaEndIndex:   endIdx,
			ImageBytes:     img.Data,
			MimeType:       img.MimeType,
		})
	}

	charCount := utf8.RuneCountInString(strings.TrimSpace(annotatedText))
	docData := &gdocs.ParsedDocument{
		DocumentID:          "local",
		Title:               title,
		ActiveTabID:         "",
		ActiveTabTitle:      title,
		SelectionOnly:       bundle.HasPartialScope,
		SelectionRanges:     bundle.DocSelectionRanges,
		SelectedTextPreview: selectedPreview,
		IsEmpty:             charCount == 0 && len(tables) == 0 && len(visuals) == 0,
		CharCount:           charCount,
		AnnotatedText:       annotatedText,
		EditableBlocks:      editableBlocks,
		Tables:              tables,
		VisualElements:      visuals,
	}
	return docData, visuals
}

// extractMarkdownTablesFromLocalText scans combined document text for Markdown tables (`| ... |`)
// and constructs TableElement descriptors so Gemini can generate TABLE_N suggestions in --no-auth mode.
func extractMarkdownTablesFromLocalText(text string) []gdocs.TableElement {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var tables []gdocs.TableElement
	utf16Cursor := 1

	var curRows []string
	curStartUTF16 := 1

	flushTable := func(endUTF16 int) {
		if len(curRows) >= 2 {
			tblIdx := len(tables) + 1
			tblID := fmt.Sprintf("TABLE_%d", tblIdx)
			cols := 0
			for part := range strings.SplitSeq(strings.Trim(curRows[0], "| "), "|") {
				if strings.TrimSpace(part) != "" {
					cols++
				}
			}
			md := fmt.Sprintf("[%s (%d rows)]\n%s", tblID, len(curRows), strings.Join(curRows, "\n"))
			tables = append(tables, gdocs.TableElement{
				TargetID:        tblID,
				TableNumber:     tblIdx,
				RowsCount:       len(curRows),
				ColsCount:       cols,
				Markdown:        md,
				StartIndex:      curStartUTF16,
				EndIndex:        endUTF16,
				TableStartIndex: curStartUTF16,
				TableEndIndex:   endUTF16,
			})
		}
		curRows = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lineUTF16Len := utf16idx.UTF16Len(line) + 1
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") && strings.Count(trimmed, "|") >= 2 {
			if len(curRows) == 0 {
				curStartUTF16 = utf16Cursor
			}
			curRows = append(curRows, trimmed)
		} else if len(curRows) > 0 {
			flushTable(utf16Cursor)
		}
		utf16Cursor += lineUTF16Len
	}
	if len(curRows) > 0 {
		flushTable(utf16Cursor)
	}
	return tables
}

// writeLocalProofreadReport writes the structured proofreading or Socratic hint coaching report to outputPath.
func writeLocalProofreadReport(
	outputPath string,
	out *CLIOutput,
	bundle *workspace.Bundle,
	docData *gdocs.ParsedDocument,
	usedDriveConversion bool,
	built *gdocs.BuiltRequestData,
	isJa bool,
) error {
	if strings.TrimSpace(outputPath) == "" {
		return nil
	}
	if dir := filepath.Dir(outputPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create output directory %q: %w", dir, err)
		}
	}

	var sb strings.Builder

	// 1. Report Header
	modeLabel := "Suggest Mode (Inline Revisions + Educational Rationales)"
	if out.HintOnly {
		modeLabel = "Socratic Hint-Only Mode (Questions & Hints Without Direct Rewrites)"
	}
	if isJa {
		if out.HintOnly {
			modeLabel = "ソクラテス式ヒントのみモード（直接の書き換え案を伏せた推敲ヒント）"
		} else {
			modeLabel = "提案モード（修正案 ＋ 推敲理由・学習ポイント）"
		}
		sb.WriteString("# 🎓 DocuMentor AI — 推敲＆コーチングレポート\n\n")
		sb.WriteString(fmt.Sprintf("- **タイトル**: %s\n", out.DocumentTitle))
		if strings.TrimSpace(out.DocumentURL) != "" {
			sb.WriteString(fmt.Sprintf("- **Google ドキュメント URL**: [%s](%s) (`%s`)\n", out.DocumentURL, out.DocumentURL, out.DocumentID))
		}
		sb.WriteString(fmt.Sprintf("- **使用モデル**: `%s`\n", out.Model))
		sb.WriteString(fmt.Sprintf("- **コーチングモード**: %s\n", modeLabel))
		if len(out.TargetFiles) > 0 {
			sb.WriteString(fmt.Sprintf("- **対象ファイル**: `%s`\n", strings.Join(out.TargetFiles, "`, `")))
		}
		if out.ActivePersona != "" {
			sb.WriteString(fmt.Sprintf("- **🎭 AIの位置づけ（ペルソナ）**: %s\n", out.ActivePersona))
		}
		sb.WriteString(fmt.Sprintf("- **検出・適用件数**: テキスト指摘 %d 件 / 図表指摘 %d 件（計 %d 件）\n\n",
			out.AppliedTextSuggestionsCount, out.AppliedElementCommentsCount, out.AppliedSuggestionsCount))
	} else {
		sb.WriteString("# 🎓 DocuMentor AI — Proofreading & Coaching Report\n\n")
		sb.WriteString(fmt.Sprintf("- **Title**: %s\n", out.DocumentTitle))
		if strings.TrimSpace(out.DocumentURL) != "" {
			sb.WriteString(fmt.Sprintf("- **Google Document URL**: [%s](%s) (`%s`)\n", out.DocumentURL, out.DocumentURL, out.DocumentID))
		}
		sb.WriteString(fmt.Sprintf("- **Model**: `%s`\n", out.Model))
		sb.WriteString(fmt.Sprintf("- **Coaching Mode**: %s\n", modeLabel))
		if len(out.TargetFiles) > 0 {
			sb.WriteString(fmt.Sprintf("- **Target Files**: `%s`\n", strings.Join(out.TargetFiles, "`, `")))
		}
		if out.ActivePersona != "" {
			sb.WriteString(fmt.Sprintf("- **🎭 Active AI Persona**: %s\n", out.ActivePersona))
		}
		sb.WriteString(fmt.Sprintf("- **Total Coaching Items**: %d text item(s) / %d figure-table item(s) (%d total)\n\n",
			out.AppliedTextSuggestionsCount, out.AppliedElementCommentsCount, out.AppliedSuggestionsCount))
	}

	sb.WriteString("---\n\n")

	// 2. Writing Coach & Learning Summary
	if isJa {
		sb.WriteString("## 1. 🎓 ライティングコーチ＆学習サマリー (Writing Coach & Learning Summary)\n\n")
		if out.OverallFeedback != "" {
			sb.WriteString(fmt.Sprintf("### 💬 全体総評\n%s\n\n", out.OverallFeedback))
		}
		if out.LearningSummary != nil {
			if out.LearningSummary.RevisionsDigest != "" {
				sb.WriteString(fmt.Sprintf("### 📌 主な推敲・改善ポイントの要約\n%s\n\n", out.LearningSummary.RevisionsDigest))
			}
			if len(out.LearningSummary.WeakPointsAndHabits) > 0 {
				sb.WriteString("### 🔍 見受けられた文章・コードのクセや弱点\n")
				for _, w := range out.LearningSummary.WeakPointsAndHabits {
					sb.WriteString(fmt.Sprintf("- %s\n", w))
				}
				sb.WriteString("\n")
			}
			if len(out.LearningSummary.ActionableTips) > 0 {
				sb.WriteString("### 🚀 次回から意識すべき具体的なアドバイス\n")
				for _, t := range out.LearningSummary.ActionableTips {
					sb.WriteString(fmt.Sprintf("- %s\n", t))
				}
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("## 1. 🎓 Writing Coach & Learning Summary\n\n")
		if out.OverallFeedback != "" {
			sb.WriteString(fmt.Sprintf("### 💬 Overall Coaching Feedback\n%s\n\n", out.OverallFeedback))
		}
		if out.LearningSummary != nil {
			if out.LearningSummary.RevisionsDigest != "" {
				sb.WriteString(fmt.Sprintf("### 📌 Revisions Digest\n%s\n\n", out.LearningSummary.RevisionsDigest))
			}
			if len(out.LearningSummary.WeakPointsAndHabits) > 0 {
				sb.WriteString("### 🔍 Observed Habits & Weak Points\n")
				for _, w := range out.LearningSummary.WeakPointsAndHabits {
					sb.WriteString(fmt.Sprintf("- %s\n", w))
				}
				sb.WriteString("\n")
			}
			if len(out.LearningSummary.ActionableTips) > 0 {
				sb.WriteString("### 🚀 Actionable Tips for Next Time\n")
				for _, t := range out.LearningSummary.ActionableTips {
					sb.WriteString(fmt.Sprintf("- %s\n", t))
				}
				sb.WriteString("\n")
			}
		}
	}

	sb.WriteString("---\n\n")

	// Sort matched suggestions in top-to-bottom reading order (StartIndex ascending) for the human report
	var readingOrder []utf16idx.MatchedSuggestion
	if built != nil && len(built.MatchedItems) > 0 {
		readingOrder = make([]utf16idx.MatchedSuggestion, len(built.MatchedItems))
		copy(readingOrder, built.MatchedItems)
		sort.Slice(readingOrder, func(i, j int) bool {
			return readingOrder[i].StartIndex < readingOrder[j].StartIndex
		})
	}

	// 3. Detailed Suggestions or Socratic Hints
	if out.HintOnly {
		if isJa {
			sb.WriteString("## 2. 💡 ソクラテス式推敲ヒント一覧（自分で書き直してみましょう！）\n\n")
		} else {
			sb.WriteString("## 2. 💡 Socratic Coaching Hints (Try Rewriting Yourself!)\n\n")
		}
		if len(readingOrder) == 0 {
			if isJa {
				sb.WriteString("_対象範囲内で指摘事項は見つかりませんでした。_\n\n")
			} else {
				sb.WriteString("_No issues flagged in the target scope._\n\n")
			}
		}
		for idx, item := range readingOrder {
			loc := formatItemLocation(item)
			if isJa {
				sb.WriteString(fmt.Sprintf("### 💡 ヒント #%d — [%s] %s\n", idx+1, item.Category, loc))
				sb.WriteString(fmt.Sprintf("- **🎯 対象箇所**: `%s`\n", item.OriginalText))
				sb.WriteString(fmt.Sprintf("- **💡 見直しの理由（問題点）**: %s\n", item.Reason))
				sb.WriteString(fmt.Sprintf("- **🎓 推敲のヒント**: %s\n\n", item.LearningPoint))
			} else {
				sb.WriteString(fmt.Sprintf("### 💡 Hint #%d — [%s] %s\n", idx+1, item.Category, loc))
				sb.WriteString(fmt.Sprintf("- **🎯 Flagged Phrase**: `%s`\n", item.OriginalText))
				sb.WriteString(fmt.Sprintf("- **💡 Issue to Check**: %s\n", item.Reason))
				sb.WriteString(fmt.Sprintf("- **🎓 Coaching Hint**: %s\n\n", item.LearningPoint))
			}
		}
	} else {
		if isJa {
			sb.WriteString("## 2. ✏️ インライン推敲提案＆教育的解説一覧\n\n")
		} else {
			sb.WriteString("## 2. ✏️ Inline Suggested Edits & Educational Rationales\n\n")
		}
		if len(readingOrder) == 0 {
			if isJa {
				sb.WriteString("_対象範囲内で修正提案はありませんでした。_\n\n")
			} else {
				sb.WriteString("_No inline revisions needed in the target scope._\n\n")
			}
		}
		for idx, item := range readingOrder {
			loc := formatItemLocation(item)
			sb.WriteString(fmt.Sprintf("### ✏️ #%d — [%s] %s\n", idx+1, item.Category, loc))
			sb.WriteString("```diff\n")
			sb.WriteString(fmt.Sprintf("- %s\n", item.OriginalText))
			sb.WriteString(fmt.Sprintf("+ %s\n", item.SuggestedText))
			sb.WriteString("```\n")
			if isJa {
				sb.WriteString(fmt.Sprintf("- **💡 推敲の理由**: %s\n", item.Reason))
				sb.WriteString(fmt.Sprintf("- **🎓 学習ポイント**: %s\n\n", item.LearningPoint))
			} else {
				sb.WriteString(fmt.Sprintf("- **💡 Reason for Revision**: %s\n", item.Reason))
				sb.WriteString(fmt.Sprintf("- **🎓 Learning Point**: %s\n\n", item.LearningPoint))
			}
		}
	}

	// 4. Multimodal Element Reviews (Figures & Tables)
	if built != nil && len(built.MatchedElementItems) > 0 {
		sb.WriteString("---\n\n")
		if isJa {
			sb.WriteString("## 3. 🖼️ 図表・テーブルのマルチモーダル推敲コメント\n\n")
		} else {
			sb.WriteString("## 3. 🖼️ Multimodal Figure & Table Review Comments\n\n")
		}
		for idx, el := range built.MatchedElementItems {
			if isJa {
				sb.WriteString(fmt.Sprintf("### 📊 #%d — `%s` [%s]\n", idx+1, el.TargetID, el.Category))
				sb.WriteString(fmt.Sprintf("- **指摘・改善案**: %s\n", el.Suggestion))
				sb.WriteString(fmt.Sprintf("- **💡 理由**: %s\n", el.Reason))
				sb.WriteString(fmt.Sprintf("- **🎓 学習ポイント**: %s\n\n", el.LearningPoint))
			} else {
				sb.WriteString(fmt.Sprintf("### 📊 #%d — `%s` [%s]\n", idx+1, el.TargetID, el.Category))
				sb.WriteString(fmt.Sprintf("- **Suggestion**: %s\n", el.Suggestion))
				sb.WriteString(fmt.Sprintf("- **💡 Reason**: %s\n", el.Reason))
				sb.WriteString(fmt.Sprintf("- **🎓 Learning Point**: %s\n\n", el.LearningPoint))
			}
		}
	}

	// 5. Annotated Document Text & Clean Revised Text Preview
	annotatedBody, revisedBody := applyLocalAnnotationsAndRevisions(bundle, docData, usedDriveConversion, built, out.HintOnly)
	if strings.TrimSpace(annotatedBody) != "" {
		sb.WriteString("---\n\n")
		if out.HintOnly {
			if isJa {
				sb.WriteString("## 4. 📄 ヒントアンカー付き原文\n\n")
			} else {
				sb.WriteString("## 4. 📄 Original Text with Anchored Socratic Hint Markers\n\n")
			}
			sb.WriteString("```markdown\n")
			sb.WriteString(strings.TrimRight(annotatedBody, "\n") + "\n")
			sb.WriteString("```\n")
		} else {
			if isJa {
				sb.WriteString("## 4. 📄 インライン差分注釈付きテキスト (`[-原文-]{+修正案+}`)\n\n")
			} else {
				sb.WriteString("## 4. 📄 Annotated Text with Inline Suggested Edits (`[-orig-]{+suggested+}`)\n\n")
			}
			sb.WriteString("```markdown\n")
			sb.WriteString(strings.TrimRight(annotatedBody, "\n") + "\n")
			sb.WriteString("```\n\n")

			if isJa {
				sb.WriteString("## 5. ✅ 全提案承認後の推敲済みテキストプレビュー\n\n")
			} else {
				sb.WriteString("## 5. ✅ Clean Revised Text Preview (All Suggestions Accepted)\n\n")
			}
			sb.WriteString("```markdown\n")
			sb.WriteString(strings.TrimRight(revisedBody, "\n") + "\n")
			sb.WriteString("```\n")
		}
	}

	return os.WriteFile(outputPath, []byte(sb.String()), 0o644)
}

// writeLocalSummaryReport writes the standalone 'summary' subcommand output to outputPath.
func writeLocalSummaryReport(outputPath string, out *CLIOutput, isJa bool) error {
	if strings.TrimSpace(outputPath) == "" {
		return nil
	}
	if dir := filepath.Dir(outputPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create output directory %q: %w", dir, err)
		}
	}

	var sb strings.Builder
	if isJa {
		sb.WriteString("# 🎓 DocuMentor AI — 既存レビュー履歴の学習サマリーレポート\n\n")
		sb.WriteString(fmt.Sprintf("- **タイトル**: %s\n", out.DocumentTitle))
		if strings.TrimSpace(out.DocumentURL) != "" {
			sb.WriteString(fmt.Sprintf("- **Google ドキュメント URL**: [%s](%s) (`%s`)\n", out.DocumentURL, out.DocumentURL, out.DocumentID))
		}
		sb.WriteString(fmt.Sprintf("- **使用モデル**: `%s`\n", out.Model))
		sb.WriteString(fmt.Sprintf("- **分析スレッド件数**: %d 件\n\n", len(out.AnalyzedItems)))
		sb.WriteString("---\n\n")
		sb.WriteString("## 1. 🎓 ライティングコーチ＆学習サマリー\n\n")
		if out.OverallFeedback != "" {
			sb.WriteString(fmt.Sprintf("### 💬 全体総評\n%s\n\n", out.OverallFeedback))
		}
		if out.LearningSummary != nil {
			if out.LearningSummary.RevisionsDigest != "" {
				sb.WriteString(fmt.Sprintf("### 📌 主な推敲・改善ポイントの要約\n%s\n\n", out.LearningSummary.RevisionsDigest))
			}
			if len(out.LearningSummary.WeakPointsAndHabits) > 0 {
				sb.WriteString("### 🔍 見受けられた文章・コードのクセや弱点\n")
				for _, w := range out.LearningSummary.WeakPointsAndHabits {
					sb.WriteString(fmt.Sprintf("- %s\n", w))
				}
				sb.WriteString("\n")
			}
			if len(out.LearningSummary.ActionableTips) > 0 {
				sb.WriteString("### 🚀 次回から意識すべき具体的なアドバイス\n")
				for _, t := range out.LearningSummary.ActionableTips {
					sb.WriteString(fmt.Sprintf("- %s\n", t))
				}
				sb.WriteString("\n")
			}
		}
		if len(out.AnalyzedItems) > 0 {
			sb.WriteString("---\n\n## 2. 🔍 既存の提案・コメント分析一覧\n\n")
			for idx, item := range out.AnalyzedItems {
				sb.WriteString(fmt.Sprintf("### #%d — [%s | %s] `%s`\n", idx+1, item.SourceType, item.Category, item.TargetPreview))
				sb.WriteString(fmt.Sprintf("- **💡 理由**: %s\n", item.Reason))
				sb.WriteString(fmt.Sprintf("- **🎓 学習ポイント**: %s\n\n", item.LearningPoint))
			}
		}
	} else {
		sb.WriteString("# 🎓 DocuMentor AI — Review History & Learning Summary Report\n\n")
		sb.WriteString(fmt.Sprintf("- **Title**: %s\n", out.DocumentTitle))
		if strings.TrimSpace(out.DocumentURL) != "" {
			sb.WriteString(fmt.Sprintf("- **Google Document URL**: [%s](%s) (`%s`)\n", out.DocumentURL, out.DocumentURL, out.DocumentID))
		}
		sb.WriteString(fmt.Sprintf("- **Model**: `%s`\n", out.Model))
		sb.WriteString(fmt.Sprintf("- **Analyzed Review Threads**: %d item(s)\n\n", len(out.AnalyzedItems)))
		sb.WriteString("---\n\n")
		sb.WriteString("## 1. 🎓 Writing Coach & Learning Summary\n\n")
		if out.OverallFeedback != "" {
			sb.WriteString(fmt.Sprintf("### 💬 Overall Coaching Feedback\n%s\n\n", out.OverallFeedback))
		}
		if out.LearningSummary != nil {
			if out.LearningSummary.RevisionsDigest != "" {
				sb.WriteString(fmt.Sprintf("### 📌 Revisions Digest\n%s\n\n", out.LearningSummary.RevisionsDigest))
			}
			if len(out.LearningSummary.WeakPointsAndHabits) > 0 {
				sb.WriteString("### 🔍 Observed Habits & Weak Points\n")
				for _, w := range out.LearningSummary.WeakPointsAndHabits {
					sb.WriteString(fmt.Sprintf("- %s\n", w))
				}
				sb.WriteString("\n")
			}
			if len(out.LearningSummary.ActionableTips) > 0 {
				sb.WriteString("### 🚀 Actionable Tips for Next Time\n")
				for _, t := range out.LearningSummary.ActionableTips {
					sb.WriteString(fmt.Sprintf("- %s\n", t))
				}
				sb.WriteString("\n")
			}
		}
		if len(out.AnalyzedItems) > 0 {
			sb.WriteString("---\n\n## 2. 🔍 Analyzed Existing Suggestions & Comments\n\n")
			for idx, item := range out.AnalyzedItems {
				sb.WriteString(fmt.Sprintf("### #%d — [%s | %s] `%s`\n", idx+1, item.SourceType, item.Category, item.TargetPreview))
				sb.WriteString(fmt.Sprintf("- **💡 Reason**: %s\n", item.Reason))
				sb.WriteString(fmt.Sprintf("- **🎓 Learning Point**: %s\n\n", item.LearningPoint))
			}
		}
	}

	return os.WriteFile(outputPath, []byte(sb.String()), 0o644)
}

// writeLocalDraftFile writes the generated initial draft to outputPath.
func writeLocalDraftFile(outputPath string, out *CLIOutput) error {
	if strings.TrimSpace(outputPath) == "" {
		return nil
	}
	if dir := filepath.Dir(outputPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create output directory %q: %w", dir, err)
		}
	}
	content := strings.TrimRight(out.GeneratedDraft, "\n") + "\n"
	return os.WriteFile(outputPath, []byte(content), 0o644)
}

func formatItemLocation(item utf16idx.MatchedSuggestion) string {
	if item.FilePath != "" && item.LineNumber > 0 {
		return fmt.Sprintf("(`%s:%d`)", item.FilePath, item.LineNumber)
	}
	if item.FilePath != "" {
		return fmt.Sprintf("(`%s`)", item.FilePath)
	}
	if item.LineNumber > 0 {
		return fmt.Sprintf("(Line %d)", item.LineNumber)
	}
	return fmt.Sprintf("(`@%d:%d`)", item.StartIndex, item.EndIndex)
}

// applyLocalAnnotationsAndRevisions applies bottom-to-top UTF-16 replacements to produce both
// an annotated inline diff/hint view and a clean revised preview.
//   - When bundle.CombinedDocText is available and Drive conversion was NOT used (--no-auth or InsertInitialText),
//     UTF-16 index 1 corresponds directly to offset 0 of bundle.CombinedDocText.
//   - When Drive conversion WAS used (.md/.docx/.pdf converted to Google Docs) or when proofreading an existing
//     Google Document (-d <docId>), replacements are applied per paragraph/block in docData.EditableBlocks
//     using block-relative UTF-16 coordinates (it.StartIndex - blk.StartIndex) so offsets never drift.
func applyLocalAnnotationsAndRevisions(
	bundle *workspace.Bundle,
	docData *gdocs.ParsedDocument,
	usedDriveConversion bool,
	built *gdocs.BuiltRequestData,
	hintOnly bool,
) (string, string) {
	type numberedItem struct {
		num  int
		item utf16idx.MatchedSuggestion
	}
	var desc []numberedItem
	if built != nil && len(built.MatchedItems) > 0 {
		asc := make([]utf16idx.MatchedSuggestion, len(built.MatchedItems))
		copy(asc, built.MatchedItems)
		sort.Slice(asc, func(i, j int) bool {
			return asc[i].StartIndex < asc[j].StartIndex
		})
		numByStart := make(map[int]int, len(asc))
		for i, it := range asc {
			numByStart[it.StartIndex] = i + 1
		}

		desc = make([]numberedItem, len(built.MatchedItems))
		for i, it := range built.MatchedItems {
			n := numByStart[it.StartIndex]
			if n == 0 {
				n = i + 1
			}
			desc[i] = numberedItem{num: n, item: it}
		}
		sort.Slice(desc, func(i, j int) bool {
			return desc[i].item.StartIndex > desc[j].item.StartIndex
		})
	}

	// Path A: Raw CombinedDocText directly matches 1-based UTF-16 indices (--no-auth or script InsertInitialText)
	if bundle != nil && strings.TrimSpace(bundle.CombinedDocText) != "" && !usedDriveConversion {
		combinedText := bundle.CombinedDocText
		if len(desc) == 0 {
			return combinedText, combinedText
		}
		annotated := combinedText
		revised := combinedText

		for _, entry := range desc {
			it := entry.item
			uStart := it.StartIndex - 1
			uEnd := it.EndIndex - 1
			if uStart < 0 || uEnd < uStart {
				continue
			}
			bStart := utf16idx.UTF16OffsetToByteOffset(revised, uStart)
			bEnd := utf16idx.UTF16OffsetToByteOffset(revised, uEnd)
			if bStart < 0 || bEnd > len(revised) || bStart > bEnd {
				continue
			}

			if hintOnly {
				marker := fmt.Sprintf("«🎯 HINT #%d [%s]: %s»", entry.num, it.Category, it.OriginalText)
				annotated = annotated[:bStart] + marker + annotated[bEnd:]
			} else {
				inlineDiff := fmt.Sprintf("[-%s-]{+%s+} /* 💬 #%d %s */", it.OriginalText, it.SuggestedText, entry.num, it.Category)
				annotated = annotated[:bStart] + inlineDiff + annotated[bEnd:]
				revised = revised[:bStart] + it.SuggestedText + revised[bEnd:]
			}
		}
		return annotated, revised
	}

	// Path B: Converted Google Doc (.md, .docx, .pdf) or existing Google Doc (-d <docId>)
	if docData != nil && len(docData.EditableBlocks) > 0 {
		var annLines, revLines []string
		for _, blk := range docData.EditableBlocks {
			annBlk := blk.Text
			revBlk := blk.Text
			for _, entry := range desc {
				it := entry.item
				if it.StartIndex < blk.StartIndex || it.EndIndex > blk.EndIndex {
					continue
				}
				relUStart := it.StartIndex - blk.StartIndex
				relUEnd := it.EndIndex - blk.StartIndex
				if relUStart < 0 || relUEnd < relUStart {
					continue
				}
				bStart := utf16idx.UTF16OffsetToByteOffset(revBlk, relUStart)
				bEnd := utf16idx.UTF16OffsetToByteOffset(revBlk, relUEnd)
				if bStart < 0 || bEnd > len(revBlk) || bStart > bEnd {
					continue
				}
				if hintOnly {
					marker := fmt.Sprintf("«🎯 HINT #%d [%s]: %s»", entry.num, it.Category, it.OriginalText)
					annBlk = annBlk[:bStart] + marker + annBlk[bEnd:]
				} else {
					inlineDiff := fmt.Sprintf("[-%s-]{+%s+} /* 💬 #%d %s */", it.OriginalText, it.SuggestedText, entry.num, it.Category)
					annBlk = annBlk[:bStart] + inlineDiff + annBlk[bEnd:]
					revBlk = revBlk[:bStart] + it.SuggestedText + revBlk[bEnd:]
				}
			}
			annLines = append(annLines, annBlk)
			revLines = append(revLines, revBlk)
		}
		return strings.Join(annLines, "\n\n"), strings.Join(revLines, "\n\n")
	}

	return "", ""
}

// buildGoogleDocsSummaryTabContent builds the formatted coaching summary text and UTF-16 heading style
// ranges to be inserted into the Google Document Summary Tab (`--summary-tab`).
func buildGoogleDocsSummaryTabContent(out *CLIOutput, isJa bool) (string, []gdocs.SummaryTabHeadingRange) {
	var sb strings.Builder
	curUTF16Idx := 1
	var headings []gdocs.SummaryTabHeadingRange

	appendLine := func(line string) {
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		sb.WriteString(line)
		curUTF16Idx += utf16idx.UTF16Len(line)
	}
	appendHeading := func(style string, title string) {
		line := strings.TrimSpace(title) + "\n"
		startIdx := curUTF16Idx
		endIdx := startIdx + utf16idx.UTF16Len(line)
		sb.WriteString(line)
		curUTF16Idx = endIdx
		headings = append(headings, gdocs.SummaryTabHeadingRange{
			StartIndex: startIdx,
			EndIndex:   endIdx,
			NamedStyle: style,
		})
	}

	nowStr := time.Now().Format("2006-01-02 15:04:05")
	if isJa {
		appendHeading("HEADING_1", "🎓 DocuMentor AI — ライティングコーチ＆学習サマリー")
		appendLine(fmt.Sprintf("• 最終更新日時: %s", nowStr))
		appendLine(fmt.Sprintf("• ドキュメント名: %s", out.DocumentTitle))
		appendLine(fmt.Sprintf("• 使用モデル: %s  |  実行コマンド: %s (%s)", out.Model, out.Command, out.Mode))
		if strings.TrimSpace(out.ActivePersona) != "" {
			appendLine(fmt.Sprintf("• 推敲AIの位置づけ: %s", strings.TrimSpace(out.ActivePersona)))
		}
		if out.Command == "summary" {
			appendLine(fmt.Sprintf("• 分析スレッド件数: %d 件", len(out.AnalyzedItems)))
		} else {
			appendLine(fmt.Sprintf("• 検出・適用件数: テキスト指摘 %d 件 / 図表指摘 %d 件（計 %d 件）",
				out.AppliedTextSuggestionsCount, out.AppliedElementCommentsCount, out.AppliedSuggestionsCount))
		}
		appendLine("")

		if strings.TrimSpace(out.OverallFeedback) != "" {
			appendHeading("HEADING_2", "1. 💬 全体総評 (Executive Assessment)")
			appendLine(strings.TrimSpace(out.OverallFeedback))
			appendLine("")
		}

		if out.LearningSummary != nil {
			if strings.TrimSpace(out.LearningSummary.RevisionsDigest) != "" {
				appendHeading("HEADING_2", "2. 📌 主な推敲・改善ポイントの要約 (Summary of Key Revisions)")
				appendLine(strings.TrimSpace(out.LearningSummary.RevisionsDigest))
				appendLine("")
			}
			if len(out.LearningSummary.WeakPointsAndHabits) > 0 {
				appendHeading("HEADING_2", "3. 🔍 見受けられた文章・コードの癖や傾向 (Key Writing & Coding Patterns)")
				for _, w := range out.LearningSummary.WeakPointsAndHabits {
					appendLine("• " + strings.TrimSpace(w))
				}
				appendLine("")
			}
			if len(out.LearningSummary.ActionableTips) > 0 {
				appendHeading("HEADING_2", "4. 🚀 今後の執筆・実装に向けた実践アドバイス (Top Actionable Next Steps)")
				for i, t := range out.LearningSummary.ActionableTips {
					appendLine(fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(t)))
				}
				appendLine("")
			}
		}

		if dr := out.DecisionRecord; dr != nil {
			appendHeading("HEADING_2", "5. 📈 AI推敲コーチの採否・編集記録 (Coach Impact & Decision Record)")
			c := dr.Cumulative
			if c.ImpactRate != nil {
				appendLine(fmt.Sprintf("• ドラフト改善貢献率 (Helpful Rate): %d%% [%s] （採用+編集 %d / 判定済 %d 件）",
					*c.ImpactRate, dr.VerdictState, c.Constructive, c.Decided))
			} else {
				appendLine(fmt.Sprintf("• ドラフト改善貢献率 (Helpful Rate): 集計待ち [%s] （未解決のAI提案 %d 件）",
					dr.VerdictState, dr.PendingAICount))
			}
			appendLine(fmt.Sprintf("• 累計内訳: 発行 %d 件 | ✅ そのまま採用 %d | ✏️ 加筆して採用 %d | ❌ 却下 %d | 💨 文脈消滅 %d | ⏳ 未解決AI提案 %d",
				c.Issued, c.Accepted, c.Edited, c.Rejected, c.Expired, dr.PendingAICount))
			if len(dr.RecentDecisions) > 0 {
				appendLine("• 直近の採否・編集履歴:")
				for _, d := range dr.RecentDecisions {
					if d.Status == "EDITED" && d.FinalSnippet != "" {
						appendLine(fmt.Sprintf("  - [%s | %s] %q ➔ AI案 %q ➔ 最終採用文 %q", d.Status, d.Category, d.OrigSnippet, d.SugSnippet, d.FinalSnippet))
					} else {
						appendLine(fmt.Sprintf("  - [%s | %s] %q ➔ %q", d.Status, d.Category, d.OrigSnippet, d.SugSnippet))
					}
				}
			}
			appendLine("")
		}

		if len(out.MatchedSuggestions) > 0 || len(out.MatchedElementSuggestions) > 0 {
			appendHeading("HEADING_2", "6. 📝 今回の指摘・ヒント一覧 (Digest of Coaching Items)")
			for i, item := range out.MatchedSuggestions {
				if out.HintOnly {
					appendLine(fmt.Sprintf("#%d [%s] 対象箇所: %q", i+1, item.Category, item.OriginalText))
					appendLine(fmt.Sprintf("   💡 見直しの理由: %s", item.Reason))
					appendLine(fmt.Sprintf("   🎓 推敲のヒント: %s", item.LearningPoint))
				} else {
					appendLine(fmt.Sprintf("#%d [%s] %q ➔ %q", i+1, item.Category, item.OriginalText, item.SuggestedText))
					appendLine(fmt.Sprintf("   💡 理由: %s", item.Reason))
					appendLine(fmt.Sprintf("   🎓 学習ポイント: %s", item.LearningPoint))
				}
			}
			for i, el := range out.MatchedElementSuggestions {
				appendLine(fmt.Sprintf("#E%d [%s | %s] %s", i+1, el.TargetID, el.Category, el.Suggestion))
				appendLine(fmt.Sprintf("   💡 理由: %s | 🎓 学習ポイント: %s", el.Reason, el.LearningPoint))
			}
		} else if len(out.AnalyzedItems) > 0 {
			appendHeading("HEADING_2", "6. 📝 分析された提案・コメント一覧 (Analyzed Review Threads)")
			for i, item := range out.AnalyzedItems {
				appendLine(fmt.Sprintf("#%d [%s | %s] %q", i+1, item.SourceType, item.Category, item.TargetPreview))
				appendLine(fmt.Sprintf("   💡 理由: %s | 🎓 学習ポイント: %s", item.Reason, item.LearningPoint))
			}
		}
	} else {
		appendHeading("HEADING_1", "🎓 DocuMentor AI — Writing Coach & Learning Summary")
		appendLine(fmt.Sprintf("• Updated At: %s", nowStr))
		appendLine(fmt.Sprintf("• Document Title: %s", out.DocumentTitle))
		appendLine(fmt.Sprintf("• Model: %s  |  Command: %s (%s)", out.Model, out.Command, out.Mode))
		if strings.TrimSpace(out.ActivePersona) != "" {
			appendLine(fmt.Sprintf("• Active AI Persona: %s", strings.TrimSpace(out.ActivePersona)))
		}
		if out.Command == "summary" {
			appendLine(fmt.Sprintf("• Analyzed Review Threads: %d item(s)", len(out.AnalyzedItems)))
		} else {
			appendLine(fmt.Sprintf("• Coaching Items Applied: %d text item(s) / %d figure & table item(s) (%d total)",
				out.AppliedTextSuggestionsCount, out.AppliedElementCommentsCount, out.AppliedSuggestionsCount))
		}
		appendLine("")

		if strings.TrimSpace(out.OverallFeedback) != "" {
			appendHeading("HEADING_2", "1. 💬 Executive Assessment")
			appendLine(strings.TrimSpace(out.OverallFeedback))
			appendLine("")
		}

		if out.LearningSummary != nil {
			if strings.TrimSpace(out.LearningSummary.RevisionsDigest) != "" {
				appendHeading("HEADING_2", "2. 📌 Summary of Key Revisions")
				appendLine(strings.TrimSpace(out.LearningSummary.RevisionsDigest))
				appendLine("")
			}
			if len(out.LearningSummary.WeakPointsAndHabits) > 0 {
				appendHeading("HEADING_2", "3. 🔍 Key Writing & Coding Patterns Observed")
				for _, w := range out.LearningSummary.WeakPointsAndHabits {
					appendLine("• " + strings.TrimSpace(w))
				}
				appendLine("")
			}
			if len(out.LearningSummary.ActionableTips) > 0 {
				appendHeading("HEADING_2", "4. 🚀 Top Actionable Next Steps")
				for i, t := range out.LearningSummary.ActionableTips {
					appendLine(fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(t)))
				}
				appendLine("")
			}
		}

		if dr := out.DecisionRecord; dr != nil {
			appendHeading("HEADING_2", "5. 📈 Coach Impact & Decision Record")
			c := dr.Cumulative
			if c.ImpactRate != nil {
				appendLine(fmt.Sprintf("• Draft Improvement Rate (Helpful Rate): %d%% [%s] (%d constructive / %d decided)",
					*c.ImpactRate, dr.VerdictState, c.Constructive, c.Decided))
			} else {
				appendLine(fmt.Sprintf("• Draft Improvement Rate (Helpful Rate): Pending Review [%s] (%d pending AI suggestion(s))",
					dr.VerdictState, dr.PendingAICount))
			}
			appendLine(fmt.Sprintf("• Cumulative Breakdown: Issued %d | ✅ Accepted %d | ✏️ Edited %d | ❌ Rejected %d | 💨 Expired %d | ⏳ Pending AI %d",
				c.Issued, c.Accepted, c.Edited, c.Rejected, c.Expired, dr.PendingAICount))
			if len(dr.RecentDecisions) > 0 {
				appendLine("• Recent Suggestion Decisions:")
				for _, d := range dr.RecentDecisions {
					if d.Status == "EDITED" && d.FinalSnippet != "" {
						appendLine(fmt.Sprintf("  - [%s | %s] %q ➔ AI %q ➔ Final %q", d.Status, d.Category, d.OrigSnippet, d.SugSnippet, d.FinalSnippet))
					} else {
						appendLine(fmt.Sprintf("  - [%s | %s] %q ➔ %q", d.Status, d.Category, d.OrigSnippet, d.SugSnippet))
					}
				}
			}
			appendLine("")
		}

		if len(out.MatchedSuggestions) > 0 || len(out.MatchedElementSuggestions) > 0 {
			appendHeading("HEADING_2", "6. 📝 Digest of Coaching Items")
			for i, item := range out.MatchedSuggestions {
				if out.HintOnly {
					appendLine(fmt.Sprintf("#%d [%s] Flagged Phrase: %q", i+1, item.Category, item.OriginalText))
					appendLine(fmt.Sprintf("   💡 Issue to Check: %s", item.Reason))
					appendLine(fmt.Sprintf("   🎓 Coaching Hint: %s", item.LearningPoint))
				} else {
					appendLine(fmt.Sprintf("#%d [%s] %q ➔ %q", i+1, item.Category, item.OriginalText, item.SuggestedText))
					appendLine(fmt.Sprintf("   💡 Reason: %s", item.Reason))
					appendLine(fmt.Sprintf("   🎓 Learning Point: %s", item.LearningPoint))
				}
			}
			for i, el := range out.MatchedElementSuggestions {
				appendLine(fmt.Sprintf("#E%d [%s | %s] %s", i+1, el.TargetID, el.Category, el.Suggestion))
				appendLine(fmt.Sprintf("   💡 Reason: %s | 🎓 Learning Point: %s", el.Reason, el.LearningPoint))
			}
		} else if len(out.AnalyzedItems) > 0 {
			appendHeading("HEADING_2", "6. 📝 Analyzed Review Threads")
			for i, item := range out.AnalyzedItems {
				appendLine(fmt.Sprintf("#%d [%s | %s] %q", i+1, item.SourceType, item.Category, item.TargetPreview))
				appendLine(fmt.Sprintf("   💡 Reason: %s | 🎓 Learning Point: %s", item.Reason, item.LearningPoint))
			}
		}
	}

	return sb.String(), headings
}
