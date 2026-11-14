package cli

import (
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/tanaikech/gdocumentor/internal/gdocs"
	"github.com/tanaikech/gdocumentor/internal/gemini"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

const (
	ansiReset     = "\033[0m"
	ansiBold      = "\033[1m"
	ansiDim       = "\033[2m"
	ansiCyan      = "\033[36m"
	ansiGreen     = "\033[32m"
	ansiYellow    = "\033[33m"
	ansiBlue      = "\033[34m"
	ansiMagenta   = "\033[35m"
	ansiRed       = "\033[31m"
	ansiBgCyan    = "\033[46;30m"
	ansiBgGreen   = "\033[42;30m"
	ansiBgMagenta = "\033[45;37m"
	ansiBgBlue    = "\033[44;37m"
	ansiBgYellow  = "\033[43;30m"
	ansiBgRed     = "\033[41;37m"
)

// Spinner provides a graphical progress indicator with an animated spinner, pulse bar,
// step progress meter, and live elapsed timer. Completely disabled in --json / MCP mode.
type Spinner struct {
	w         io.Writer
	enabled   bool
	mu        sync.Mutex
	active    bool
	msg       string
	startedAt time.Time
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewSpinner creates a new Spinner. When enabled is false (e.g. in --json mode), all methods are no-ops.
func NewSpinner(w io.Writer, enabled bool) *Spinner {
	return &Spinner{
		w:       w,
		enabled: enabled,
	}
}

// stepProgressParams returns (colorCode, tauSeconds) for a given step message.
// Each individual step is displayed on its own 0% -> 100% scale: while running, the step's
// percentage advances smoothly from 0% toward 99% (`1 - exp(-elapsed / tau)`), and when Stop()
// is called, the completed step line is printed at 100% (`✔ ▐████████▌ 100%`).
func stepProgressParams(msg string) (string, float64) {
	switch {
	case strings.Contains(msg, "Step 1/") || strings.Contains(msg, "ステップ 1/"):
		return ansiCyan, 1.5
	case strings.Contains(msg, "Step 2/4") || strings.Contains(msg, "ステップ 2/4"),
		strings.Contains(msg, "Step 2/3") || strings.Contains(msg, "ステップ 2/3"),
		strings.Contains(msg, "Step 2/2") || strings.Contains(msg, "ステップ 2/2"):
		return ansiMagenta, 6.0
	case strings.Contains(msg, "Step 3/4") || strings.Contains(msg, "ステップ 3/4"):
		return ansiYellow, 0.8
	case strings.Contains(msg, "Step 4/4") || strings.Contains(msg, "ステップ 4/4"),
		strings.Contains(msg, "Step 3/3") || strings.Contains(msg, "ステップ 3/3"):
		return ansiGreen, 1.2
	default:
		return ansiCyan, 2.5
	}
}

func formatProgressBarAndPct(pct int, barColor string) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := (pct*8 + 50) / 100
	if pct > 0 && filled == 0 {
		filled = 1
	}
	if filled > 8 {
		filled = 8
	}
	bar := "▐" + strings.Repeat("█", filled) + strings.Repeat("░", 8-filled) + "▌"
	return fmt.Sprintf("%s%s%s %s%3d%%%s", barColor, bar, ansiReset, ansiBold, pct, ansiReset)
}

func stepProgressGraphic(msg string, elapsedSec float64) string {
	colorCode, tau := stepProgressParams(msg)
	progressRatio := 0.0
	if elapsedSec > 0 && tau > 0 {
		progressRatio = 1.0 - math.Exp(-elapsedSec/tau)
	}
	livePct := max(int(math.Round(99.0*progressRatio)), 0)
	if livePct > 99 {
		livePct = 99
	}
	return formatProgressBarAndPct(livePct, colorCode)
}

// Start begins displaying a graphical step progress indicator with animated spinner & timer.
func (s *Spinner) Start(stepMsg string) {
	if !s.enabled || s.w == nil {
		return
	}
	s.Stop("")
	s.mu.Lock()
	s.active = true
	s.msg = stepMsg
	s.startedAt = time.Now()
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	stopCh := s.stopCh
	doneCh := s.doneCh
	startedAt := s.startedAt
	w := s.w
	s.mu.Unlock()

	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	pulses := []string{"▰▱▱▱▱", "▰▰▱▱▱", "▰▰▰▱▱", "▰▰▰▰▱", "▰▰▰▰▰", "▱▰▰▰▰", "▱▱▰▰▰", "▱▱▱▰▰", "▱▱▱▱▰"}
	go func() {
		defer close(doneCh)
		idx := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				s.mu.Lock()
				curMsg := s.msg
				s.mu.Unlock()
				elapsed := time.Since(startedAt).Seconds()
				prog := stepProgressGraphic(curMsg, elapsed)
				pulse := pulses[idx%len(pulses)]
				frame := frames[idx%len(frames)]
				fmt.Fprintf(w, "\r\033[2K %s%s%s %s %s%s%s %s[%.1fs]%s %s",
					ansiBold+ansiCyan, frame, ansiReset,
					prog,
					ansiMagenta, pulse, ansiReset,
					ansiDim, elapsed, ansiReset,
					curMsg)
				idx++
			}
		}
	}()
}

// Stop stops the active spinner and prints a graphical 100% completion line for the step with elapsed duration.
func (s *Spinner) Stop(doneMsg string) {
	if !s.enabled || s.w == nil {
		return
	}
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		if doneMsg != "" {
			barStr := formatProgressBarAndPct(100, ansiGreen)
			fmt.Fprintf(s.w, " %s✔%s %s %s\n", ansiBold+ansiGreen, ansiReset, barStr, doneMsg)
		}
		return
	}
	s.active = false
	elapsed := time.Since(s.startedAt).Seconds()
	close(s.stopCh)
	doneCh := s.doneCh
	s.mu.Unlock()

	<-doneCh
	fmt.Fprintf(s.w, "\r\033[2K")
	if doneMsg != "" {
		barStr := formatProgressBarAndPct(100, ansiGreen)
		fmt.Fprintf(s.w, " %s✔%s %s %s[%.2fs]%s %s\n",
			ansiBold+ansiGreen, ansiReset,
			barStr,
			ansiDim, elapsed, ansiReset,
			doneMsg)
	}
}

// BannerAuthStatus holds optional readiness status for Gemini API key and Google OAuth credentials.
type BannerAuthStatus struct {
	KeyState   string // "READY", "BYPASSED", "MISSING"
	HasKey     bool
	KeySource  string
	OAuthState string // "READY", "NO_SCOPE", "NO_TOKEN", "INVALID", "BYPASSED"
	OAuthDesc  string
}

func truncateRunes(s string, maxRunes int) (string, int) {
	runes := []rune(s)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes), len(runes)
}

// RenderHeaderBanner prints the graphical CLI header banner in interactive mode.
func RenderHeaderBanner(w io.Writer, subcommand, model, mode, lang string, authStatus ...BannerAuthStatus) {
	if w == nil {
		return
	}
	topLine := strings.Repeat("━", 76)
	midLine := strings.Repeat("─", 76)
	fmt.Fprintf(w, "\n%s┏%s┓%s\n", ansiCyan, topLine, ansiReset)
	fmt.Fprintf(w, "%s┃%s  %s🎓 gdocumentor — DocuMentor AI%s %s(Go CLI · MCP Server · Antigravity v%-5s)%s %s┃%s\n",
		ansiCyan, ansiReset, ansiBold+ansiCyan, ansiReset, ansiDim, Version, ansiReset, ansiCyan, ansiReset)
	fmt.Fprintf(w, "%s┠%s┨%s\n", ansiCyan, midLine, ansiReset)

	cmdDisp, _ := truncateRunes(subcommand, 10)
	modelDisp, _ := truncateRunes(model, 17)
	modeDisp, _ := truncateRunes(mode, 8)
	langDisp, _ := truncateRunes(strings.ToUpper(lang), 2)

	fmt.Fprintf(w, "%s┃%s  %s CMD %s %s%-10s%s │ %s MODEL %s %s%-17s%s │ %s MODE %s %s%-8s%s │ %s %-2s %s     %s┃%s\n",
		ansiCyan, ansiReset,
		ansiBgYellow, ansiReset, ansiBold+ansiYellow, cmdDisp, ansiReset,
		ansiBgGreen, ansiReset, ansiBold+ansiGreen, modelDisp, ansiReset,
		ansiBgMagenta, ansiReset, ansiBold+ansiMagenta, modeDisp, ansiReset,
		ansiBgCyan, langDisp, ansiReset,
		ansiCyan, ansiReset)

	if len(authStatus) > 0 {
		st := authStatus[0]
		keyState := st.KeyState
		if keyState == "" {
			if st.HasKey {
				keyState = "READY"
			} else {
				keyState = "MISSING"
			}
		}

		var keyRaw string
		var keyColor string
		switch keyState {
		case "READY":
			keyRaw = "READY (" + st.KeySource + ")"
			keyColor = ansiGreen
		case "BYPASSED":
			src := st.KeySource
			if src == "" {
				src = "0 tokens"
			}
			keyRaw = "BYPASSED (" + src + ")"
			keyColor = ansiCyan
		default:
			keyRaw = "MISSING (GEMINI_API_KEY)"
			keyColor = ansiRed
		}
		keyDisp, keyRuneLen := truncateRunes(keyRaw, 25)
		padKey := strings.Repeat(" ", 25-keyRuneLen)
		keyText := keyColor + keyDisp + ansiReset

		var authRaw string
		var authColor string
		switch st.OAuthState {
		case "BYPASSED":
			authRaw = "BYPASSED (--no-auth)"
			authColor = ansiCyan
		case "NO_TOKEN":
			authRaw = "NO TOKEN (gcloud login)"
			authColor = ansiYellow
		case "NO_SCOPE":
			desc := st.OAuthDesc
			if desc == "" {
				desc = "missing docs"
			}
			authRaw = "NO SCOPE (" + desc + ")"
			authColor = ansiMagenta
		case "INVALID":
			desc := st.OAuthDesc
			if desc == "" {
				desc = "invalid token"
			}
			authRaw = "INVALID (" + desc + ")"
			authColor = ansiRed
		default:
			authRaw = "READY (" + st.OAuthDesc + ")"
			authColor = ansiGreen
		}
		authDisp, authRuneLen := truncateRunes(authRaw, 31)
		padAuth := strings.Repeat(" ", 31-authRuneLen)
		authText := authColor + authDisp + ansiReset

		fmt.Fprintf(w, "%s┃%s  %s KEY %s %s%s%s │ %s AUTH %s %s%s%s  %s┃%s\n",
			ansiCyan, ansiReset,
			ansiBgBlue, ansiReset, keyText, padKey, ansiReset,
			ansiBgBlue, ansiReset, authText, padAuth, ansiReset,
			ansiCyan, ansiReset)
	}

	fmt.Fprintf(w, "%s┗%s┛%s\n\n", ansiCyan, topLine, ansiReset)
}

// RenderCoachingReport prints the rich TUI report for Proofreading / Summary / Draft / Manage runs.
func RenderCoachingReport(w io.Writer, out *CLIOutput) {
	if w == nil || out == nil {
		return
	}
	isJa := out.Language == "ja"
	sep := strings.Repeat("─", 74)

	if out.RequiresAgentAI && out.PreparedContext != nil {
		fmt.Fprintf(w, "\n%s%s%s\n", ansiCyan, sep, ansiReset)
		if isJa {
			fmt.Fprintf(w, "%s🤖 エージェントAI推敲・コンテキスト準備完了 (Phase 1 — 0 Gemini Tokens)%s\n", ansiBold+ansiCyan, ansiReset)
			fmt.Fprintf(w, "   編集対象ブロック: %s%d件%s │ 表: %s%d件%s │ 図: %s%d件%s\n",
				ansiGreen, len(out.PreparedContext.EditableBlocks), ansiReset,
				ansiYellow, len(out.PreparedContext.Tables), ansiReset,
				ansiMagenta, len(out.PreparedContext.VisualElements), ansiReset)
		} else {
			fmt.Fprintf(w, "%s🤖 Host-AI Context Prepared (Phase 1 — 0 Gemini Tokens)%s\n", ansiBold+ansiCyan, ansiReset)
			fmt.Fprintf(w, "   Editable Blocks: %s%d%s │ Tables: %s%d%s │ Visuals: %s%d%s\n",
				ansiGreen, len(out.PreparedContext.EditableBlocks), ansiReset,
				ansiYellow, len(out.PreparedContext.Tables), ansiReset,
				ansiMagenta, len(out.PreparedContext.VisualElements), ansiReset)
		}
		fmt.Fprintf(w, "   %s\n", out.PreparedContext.NextStepInstructions)
		RenderDocURLBox(w, out.DocumentURL, out.DocumentID, isJa)
		return
	}

	if out.Command == "edit" && out.EditResult != nil {
		er := out.EditResult
		fmt.Fprintf(w, "\n%s%s%s\n", ansiCyan, sep, ansiReset)
		if isJa {
			fmt.Fprintf(w, "%s✏️  Google ドキュメント直接編集の完了%s\n", ansiBold, ansiReset)
			fmt.Fprintf(w, "   操作: %s%s%s │ モード: %s%s%s │ 対象件数: %s%d件%s",
				ansiYellow, strings.Join(er.Operations, "+"), ansiReset,
				ansiMagenta, er.WriteMode, ansiReset,
				ansiGreen, er.AffectedCount, ansiReset)
			if er.InsertedChars > 0 || er.DeletedChars > 0 {
				fmt.Fprintf(w, " │ 挿入: %s+%d文字%s │ 削除: %s-%d文字%s",
					ansiGreen, er.InsertedChars, ansiReset, ansiRed, er.DeletedChars, ansiReset)
			}
			fmt.Fprintln(w)
		} else {
			fmt.Fprintf(w, "%s✏️  Google Document Direct Edit Complete%s\n", ansiBold, ansiReset)
			fmt.Fprintf(w, "   Operation: %s%s%s │ WriteMode: %s%s%s │ Affected: %s%d%s",
				ansiYellow, strings.Join(er.Operations, "+"), ansiReset,
				ansiMagenta, er.WriteMode, ansiReset,
				ansiGreen, er.AffectedCount, ansiReset)
			if er.InsertedChars > 0 || er.DeletedChars > 0 {
				fmt.Fprintf(w, " │ Inserted: %s+%d chars%s │ Deleted: %s-%d chars%s",
					ansiGreen, er.InsertedChars, ansiReset, ansiRed, er.DeletedChars, ansiReset)
			}
			fmt.Fprintln(w)
		}
		if len(er.AffectedRanges) > 0 {
			for idx, r := range er.AffectedRanges {
				if idx >= 10 {
					break
				}
				if r.Preview != "" {
					fmt.Fprintf(w, "   %s#%d%s │ %s@%d:%d%s │ %s\n",
						ansiYellow, idx+1, ansiReset, ansiGreen, r.StartIndex, r.EndIndex, ansiReset, r.Preview)
				} else {
					fmt.Fprintf(w, "   %s#%d%s │ %s@%d:%d%s\n",
						ansiYellow, idx+1, ansiReset, ansiGreen, r.StartIndex, r.EndIndex, ansiReset)
				}
			}
		}
		if !er.DeletedDocument {
			RenderDocURLBox(w, out.DocumentURL, out.DocumentID, isJa)
		} else {
			fmt.Fprintln(w)
		}
		return
	}

	if out.Command == "manage" {
		fmt.Fprintf(w, "\n%s%s%s\n", ansiCyan, sep, ansiReset)
		if isJa {
			fmt.Fprintf(w, "%s🛠️  Google ドキュメント・レビュー管理処理の完了%s\n", ansiBold, ansiReset)
			fmt.Fprintf(w, "   実行アクション: %s%s%s │ 対象件数: %s%d件%s\n",
				ansiYellow, out.ManageAction, ansiReset, ansiGreen, out.ManagedCount, ansiReset)
		} else {
			fmt.Fprintf(w, "%s🛠️  Document Review Management Complete%s\n", ansiBold, ansiReset)
			fmt.Fprintf(w, "   Action: %s%s%s │ Affected Items: %s%d%s\n",
				ansiYellow, out.ManageAction, ansiReset, ansiGreen, out.ManagedCount, ansiReset)
		}
		if out.DecisionRecord != nil {
			renderDecisionRecordSection(w, out.DecisionRecord, isJa)
		}
		RenderDocURLBox(w, out.DocumentURL, out.DocumentID, isJa)
		return
	}

	if out.Command == "inspect" && out.InspectedDoc != nil {
		doc := out.InspectedDoc
		fmt.Fprintf(w, "\n%s%s%s\n", ansiCyan, sep, ansiReset)
		if isJa {
			fmt.Fprintf(w, "%s🔎 Google ドキュメント座標・レビュースレッド検査 (Inspector):%s %s\n", ansiBold, ansiReset, doc.Title)
			fmt.Fprintf(w, "%s%s%s\n", ansiDim, sep, ansiReset)
			fmt.Fprintf(w, "%s段落・編集可能ブロック一覧 (%d件):%s  （部分指定は %s--range \"N-M\"%s または %s--range \"@start:end\"%s）\n",
				ansiBold+ansiCyan, len(doc.EditableBlocks), ansiReset, ansiYellow, ansiReset, ansiGreen, ansiReset)
		} else {
			fmt.Fprintf(w, "%s🔎 Google Document Coordinate & Review Inspector:%s %s\n", ansiBold, ansiReset, doc.Title)
			fmt.Fprintf(w, "%s%s%s\n", ansiDim, sep, ansiReset)
			fmt.Fprintf(w, "%sParagraphs / Editable Blocks (%d):%s  (Use %s--range \"N-M\"%s or %s--range \"@start:end\"%s)\n",
				ansiBold+ansiCyan, len(doc.EditableBlocks), ansiReset, ansiYellow, ansiReset, ansiGreen, ansiReset)
		}
		for idx, blk := range doc.EditableBlocks {
			preview := strings.ReplaceAll(strings.TrimSpace(blk.Text), "\n", " ")
			if len([]rune(preview)) > 68 {
				preview = string([]rune(preview)[:68]) + "..."
			}
			fmt.Fprintf(w, "  %s#%-3d%s │ %s@%d:%-6d%s │ %s\n",
				ansiYellow, idx+1, ansiReset,
				ansiGreen, blk.StartIndex, blk.EndIndex, ansiReset,
				preview)
		}
		if len(doc.VisualElements) > 0 {
			if isJa {
				fmt.Fprintf(w, "\n%s図・画像一覧 (%d件):%s  （画像操作は %s--replace-image \"VISUAL_N\" --with-image <file>%s / %s--delete-image \"VISUAL_N\"%s）\n",
					ansiBold+ansiCyan, len(doc.VisualElements), ansiReset, ansiYellow, ansiReset, ansiRed, ansiReset)
			} else {
				fmt.Fprintf(w, "\n%sFigures / Images (%d):%s  (Use %s--replace-image \"VISUAL_N\" --with-image <file>%s or %s--delete-image \"VISUAL_N\"%s)\n",
					ansiBold+ansiCyan, len(doc.VisualElements), ansiReset, ansiYellow, ansiReset, ansiRed, ansiReset)
			}
			for _, v := range doc.VisualElements {
				posTag := "inline"
				if v.IsPositioned {
					posTag = "positioned"
				}
				fmt.Fprintf(w, "  %s%-8s%s │ %s@%d:%-6d%s │ objectId=%s%-16s%s │ %-10s │ %s\n",
					ansiYellow, v.TargetID, ansiReset,
					ansiGreen, v.StartIndex, v.EndIndex, ansiReset,
					ansiCyan, v.ObjectID, ansiReset,
					posTag, v.Title)
			}
		}
		if len(doc.Tables) > 0 {
			if isJa {
				fmt.Fprintf(w, "\n%s表・テーブル一覧 (%d件):%s\n", ansiBold+ansiCyan, len(doc.Tables), ansiReset)
			} else {
				fmt.Fprintf(w, "\n%sTables (%d):%s\n", ansiBold+ansiCyan, len(doc.Tables), ansiReset)
			}
			for _, t := range doc.Tables {
				fmt.Fprintf(w, "  %s%-8s%s │ %s@%d:%-6d%s │ %dx%d\n",
					ansiYellow, t.TargetID, ansiReset,
					ansiGreen, t.TableStartIndex, t.TableEndIndex, ansiReset,
					t.RowsCount, t.ColsCount)
			}
		}
		if len(doc.ExistingSuggestions) > 0 {
			if isJa {
				fmt.Fprintf(w, "\n%s未解決のインライン提案 (%d件):%s\n", ansiBold+ansiMagenta, len(doc.ExistingSuggestions), ansiReset)
			} else {
				fmt.Fprintf(w, "\n%sPending Inline Suggestions (%d):%s\n", ansiBold+ansiMagenta, len(doc.ExistingSuggestions), ansiReset)
			}
			for i, s := range doc.ExistingSuggestions {
				provTag := "👤 Human"
				if isJa {
					provTag = "👤 人間"
				}
				if s.IsAIGenerated {
					provTag = "🤖 AI"
				}
				fmt.Fprintf(w, "  %sS#%-2d%s │ %s@%d:%-6d%s │ %-8s │ %q -> %q\n",
					ansiMagenta, i+1, ansiReset, ansiGreen, s.StartIndex, s.EndIndex, ansiReset,
					provTag, s.DeletedText, s.InsertedText)
			}
		}
		if len(doc.ExistingComments) > 0 {
			if isJa {
				fmt.Fprintf(w, "\n%s未解決のコメントスレッド (%d件):%s\n", ansiBold+ansiBlue, len(doc.ExistingComments), ansiReset)
			} else {
				fmt.Fprintf(w, "\n%sOpen Comment Threads (%d):%s\n", ansiBold+ansiBlue, len(doc.ExistingComments), ansiReset)
			}
			for i, c := range doc.ExistingComments {
				rangeStr := "unanchored"
				if isJa {
					rangeStr = "アンカー無"
				}
				if len(c.AnchorRanges) > 0 {
					rangeStr = fmt.Sprintf("@%d:%d", c.AnchorRanges[0].StartIndex, c.AnchorRanges[0].EndIndex)
				}
				fmt.Fprintf(w, "  %sC#%-2d%s │ %s%-9s%s │ %q: %s\n",
					ansiBlue, i+1, ansiReset, ansiGreen, rangeStr, ansiReset,
					c.QuotedText, c.Content)
			}
		}
		if out.DecisionRecord != nil {
			renderDecisionRecordSection(w, out.DecisionRecord, isJa)
		}
		RenderDocURLBox(w, out.DocumentURL, out.DocumentID, isJa)
		return
	}

	if out.Command == "draft" {
		fmt.Fprintf(w, "\n%s%s%s\n", ansiCyan, sep, ansiReset)
		if isJa {
			fmt.Fprintf(w, "%s📝 初期ドラフト（初稿）の生成・出力完了%s (%d 文字)\n", ansiBold, ansiReset, len([]rune(out.GeneratedDraft)))
		} else {
			fmt.Fprintf(w, "%s📝 Initial Draft Generated & Inserted%s (%d chars)\n", ansiBold, ansiReset, len(out.GeneratedDraft))
		}
		fmt.Fprintf(w, "%s%s%s\n", ansiDim, sep, ansiReset)
		preview := out.GeneratedDraft
		if len(preview) > 600 {
			if isJa {
				preview = preview[:600] + "\n... [ターミナルプレビューでは省略されています。全文は出力先をご確認ください]"
			} else {
				preview = preview[:600] + "\n... [truncated in terminal preview; full text inserted into Google Docs]"
			}
		}
		fmt.Fprintf(w, "%s\n", preview)
		RenderDocURLBox(w, out.DocumentURL, out.DocumentID, isJa)
		RenderLocalOutputBox(w, out.OutputFilePath, out.LocalMode, isJa)
		return
	}

	// Proofread or Summary Mode: Render Writing Coach & Learning Summary
	title := "🎓 Writing Coach & Learning Summary"
	if isJa {
		title = "🎓 ライティングコーチ＆学習サマリー (Writing Coach & Learning Summary)"
	}
	fmt.Fprintf(w, "\n%s┏%s┓%s\n", ansiMagenta, strings.Repeat("━", 74), ansiReset)
	fmt.Fprintf(w, "%s┃%s %s%s%s\n", ansiMagenta, ansiReset, ansiBold, title, ansiReset)
	fmt.Fprintf(w, "%s┗%s┛%s\n", ansiMagenta, strings.Repeat("━", 74), ansiReset)

	if out.DecisionRecord != nil {
		renderDecisionRecordSection(w, out.DecisionRecord, isJa)
	}

	if strings.TrimSpace(out.ActivePersona) != "" {
		lbl := "🎭 Active AI Coach Persona:"
		if isJa {
			lbl = "🎭 AIの位置づけ・立場 (Active AI Persona):"
		}
		fmt.Fprintf(w, "\n%s%s%s\n%s\n", ansiBold+ansiYellow, lbl, ansiReset, indentLines(out.ActivePersona, "   "))
	}

	if strings.TrimSpace(out.OverallFeedback) != "" {
		lbl := "💬 Overall Assessment:"
		if isJa {
			lbl = "💬 全体講評 (Overall Assessment):"
		}
		fmt.Fprintf(w, "\n%s%s%s\n%s\n", ansiBold+ansiCyan, lbl, ansiReset, indentLines(out.OverallFeedback, "   "))
	}

	if out.LearningSummary != nil {
		renderLearningSummaryDetails(w, out.LearningSummary, isJa)
	}

	if strings.TrimSpace(out.ExistingSuggestionsNote) != "" {
		lbl := "📌 Note on Existing Review History:"
		if isJa {
			lbl = "📌 既存の提案・コメント履歴との調和:"
		}
		fmt.Fprintf(w, "\n%s%s%s\n%s\n", ansiBold+ansiBlue, lbl, ansiReset, indentLines(out.ExistingSuggestionsNote, "   "))
	}

	if out.RevisionControl != nil && (out.RevisionControl.ReanchoredCount > 0 || out.RevisionControl.StaleSkippedCount > 0 || out.RevisionControl.RevisionRetries > 0) {
		if isJa {
			fmt.Fprintf(w, "\n%s🔒 楽観的リビジョン制御 & UTF-16 再アンカリング:%s リトライ=%d回, 再配置=%d件, 並行変更によりスキップ=%d件\n",
				ansiBold+ansiCyan, ansiReset,
				out.RevisionControl.RevisionRetries, out.RevisionControl.ReanchoredCount, out.RevisionControl.StaleSkippedCount)
		} else {
			fmt.Fprintf(w, "\n%s🔒 Optimistic Revision Control & UTF-16 Re-Anchoring:%s retries=%d, re-anchored=%d, stale-skipped=%d\n",
				ansiBold+ansiCyan, ansiReset,
				out.RevisionControl.RevisionRetries, out.RevisionControl.ReanchoredCount, out.RevisionControl.StaleSkippedCount)
		}
	}

	if len(out.MatchedSuggestions) > 0 {
		lbl := fmt.Sprintf("✏️  Applied Inline Suggestions / Coaching Hints (%d matched):", len(out.MatchedSuggestions))
		if isJa {
			lbl = fmt.Sprintf("✏️  適用されたインライン推敲提案・ヒント (%d件):", len(out.MatchedSuggestions))
		}
		fmt.Fprintf(w, "\n%s%s%s\n", ansiBold+ansiGreen, lbl, ansiReset)
		for idx, item := range out.MatchedSuggestions {
			locMeta := fmt.Sprintf("UTF-16 [%d..%d]", item.StartIndex, item.EndIndex)
			if item.FilePath != "" {
				if item.LineNumber > 0 {
					locMeta = fmt.Sprintf("%s:%d (%s)", item.FilePath, item.LineNumber, locMeta)
				} else {
					locMeta = fmt.Sprintf("%s (%s)", item.FilePath, locMeta)
				}
			}
			fmt.Fprintf(w, " %s╭─%s %s#%d [%s]%s %s── %s%s\n",
				ansiCyan, ansiReset, ansiBold+ansiYellow, idx+1, item.Category, ansiReset, ansiDim, locMeta, ansiReset)
			if isJa {
				fmt.Fprintf(w, " %s│%s  %s- 修正前 (Original) :%s %q\n", ansiCyan, ansiReset, ansiRed, ansiReset, item.OriginalText)
				if !out.HintOnly && item.SuggestedText != "" {
					fmt.Fprintf(w, " %s│%s  %s+ 修正案 (Suggested):%s %q\n", ansiCyan, ansiReset, ansiGreen, ansiReset, item.SuggestedText)
				}
				fmt.Fprintf(w, " %s│%s  %s💡 理由 (Reason)    :%s %s\n", ansiCyan, ansiReset, ansiCyan, ansiReset, item.Reason)
				fmt.Fprintf(w, " %s╰─%s %s🎓 学習 (Learning)  :%s %s\n\n", ansiCyan, ansiReset, ansiMagenta, ansiReset, item.LearningPoint)
			} else {
				fmt.Fprintf(w, " %s│%s  %s- Original :%s %q\n", ansiCyan, ansiReset, ansiRed, ansiReset, item.OriginalText)
				if !out.HintOnly && item.SuggestedText != "" {
					fmt.Fprintf(w, " %s│%s  %s+ Suggested:%s %q\n", ansiCyan, ansiReset, ansiGreen, ansiReset, item.SuggestedText)
				}
				fmt.Fprintf(w, " %s│%s  %s💡 Reason  :%s %s\n", ansiCyan, ansiReset, ansiCyan, ansiReset, item.Reason)
				fmt.Fprintf(w, " %s╰─%s %s🎓 Learning:%s %s\n\n", ansiCyan, ansiReset, ansiMagenta, ansiReset, item.LearningPoint)
			}
		}
	}

	if len(out.MatchedElementSuggestions) > 0 {
		lbl := fmt.Sprintf("🖼️  Applied Figure / Table Review Comments (%d matched):", len(out.MatchedElementSuggestions))
		if isJa {
			lbl = fmt.Sprintf("🖼️  適用された図表レビューコメント (%d件):", len(out.MatchedElementSuggestions))
		}
		fmt.Fprintf(w, "\n%s%s%s\n", ansiBold+ansiGreen, lbl, ansiReset)
		for idx, el := range out.MatchedElementSuggestions {
			fmt.Fprintf(w, " %s╭─%s %s#%d [%s | %s]%s\n", ansiMagenta, ansiReset, ansiBold+ansiYellow, idx+1, el.TargetID, el.Category, ansiReset)
			if isJa {
				fmt.Fprintf(w, " %s│%s  %s💬 改善提案 (Suggestion):%s %s\n", ansiMagenta, ansiReset, ansiGreen, ansiReset, el.Suggestion)
				fmt.Fprintf(w, " %s│%s  %s💡 理由 (Reason)        :%s %s\n", ansiMagenta, ansiReset, ansiCyan, ansiReset, el.Reason)
				fmt.Fprintf(w, " %s╰─%s %s🎓 学習 (Learning)      :%s %s\n\n", ansiMagenta, ansiReset, ansiMagenta, ansiReset, el.LearningPoint)
			} else {
				fmt.Fprintf(w, " %s│%s  %s💬 Suggestion:%s %s\n", ansiMagenta, ansiReset, ansiGreen, ansiReset, el.Suggestion)
				fmt.Fprintf(w, " %s│%s  %s💡 Reason    :%s %s\n", ansiMagenta, ansiReset, ansiCyan, ansiReset, el.Reason)
				fmt.Fprintf(w, " %s╰─%s %s🎓 Learning  :%s %s\n\n", ansiMagenta, ansiReset, ansiMagenta, ansiReset, el.LearningPoint)
			}
		}
	}

	if len(out.AnalyzedItems) > 0 {
		lbl := fmt.Sprintf("🔍 Analyzed Existing Suggestions & Comments (%d items):", len(out.AnalyzedItems))
		if isJa {
			lbl = fmt.Sprintf("🔍 分析された既存の提案・コメント (%d件):", len(out.AnalyzedItems))
		}
		fmt.Fprintf(w, "\n%s%s%s\n", ansiBold+ansiGreen, lbl, ansiReset)
		for idx, item := range out.AnalyzedItems {
			fmt.Fprintf(w, " %s#%d [%s | %s]%s %q\n",
				ansiBold+ansiYellow, idx+1, item.SourceType, item.Category, ansiReset, item.TargetPreview)
			if isJa {
				fmt.Fprintf(w, "   %s💡 理由 (Reason)  :%s %s\n", ansiCyan, ansiReset, item.Reason)
				fmt.Fprintf(w, "   %s🎓 学習 (Learning):%s %s\n\n", ansiMagenta, ansiReset, item.LearningPoint)
			} else {
				fmt.Fprintf(w, "   %s💡 Reason  :%s %s\n", ansiCyan, ansiReset, item.Reason)
				fmt.Fprintf(w, "   %s🎓 Learning:%s %s\n\n", ansiMagenta, ansiReset, item.LearningPoint)
			}
		}
	}

	RenderDocURLBox(w, out.DocumentURL, out.DocumentID, isJa)
	RenderSummaryTabBox(w, out.SummaryTabTitle, out.SummaryTabID, out.SummaryTabURL, out.SummaryTabCreatedNew, isJa)
	RenderLocalOutputBox(w, out.OutputFilePath, out.LocalMode, isJa)
}

func renderDecisionRecordSection(w io.Writer, dr *gdocs.DecisionRecordSummary, isJa bool) {
	if dr == nil {
		return
	}
	cum := dr.Cumulative
	if cum.Decided == 0 && dr.PendingAICount == 0 && cum.Issued == 0 && dr.ActiveHumanSuggestionsCount == 0 {
		return
	}

	lblTitle := "📈 Coach Impact & Decision Record:"
	if isJa {
		lblTitle = "📈 AI推敲コーチの採否・編集記録 (Coach Impact & Decision Record):"
	}

	rateText := "Pending Review"
	if isJa {
		rateText = "集計待ち"
	}
	rateBar := "▐░░░░░░░░░░░░░░░░▌"
	if cum.ImpactRate != nil {
		rateText = fmt.Sprintf("%d%%", *cum.ImpactRate)
		filled := max((*cum.ImpactRate*16)/100, 0)
		if filled > 16 {
			filled = 16
		}
		barColor := ansiGreen
		if *cum.ImpactRate < 40 {
			barColor = ansiYellow
		}
		rateBar = barColor + "▐" + strings.Repeat("█", filled) + strings.Repeat("░", 16-filled) + "▌" + ansiReset
	}

	verdictBadge := dr.VerdictState
	switch dr.VerdictState {
	case "IMPROVING_DRAFT":
		if isJa {
			verdictBadge = "🟢 ドラフト改善に貢献 (Improving Draft)"
		} else {
			verdictBadge = "🟢 Improving Draft"
		}
	case "BALANCED_REVIEW":
		if isJa {
			verdictBadge = "🟡 適度な取捨選択 (Balanced Review)"
		} else {
			verdictBadge = "🟡 Balanced Review"
		}
	case "HIGH_REVIEW_WORK":
		if isJa {
			verdictBadge = "🟠 レビュー負荷高め (High Review Work)"
		} else {
			verdictBadge = "🟠 High Review Overhead"
		}
	case "AWAITING_REVIEW":
		if isJa {
			verdictBadge = "⏳ レビュー待ち (Awaiting Review)"
		} else {
			verdictBadge = "⏳ Awaiting Review"
		}
	}

	fmt.Fprintf(w, "\n%s%s%s\n", ansiBold+ansiGreen, lblTitle, ansiReset)
	if isJa {
		fmt.Fprintf(w, "   ドラフト改善貢献率: %s %s%s%s [%s]\n", rateBar, ansiBold+ansiCyan, rateText, ansiReset, verdictBadge)
		fmt.Fprintf(w, "   ✅ 採用: %d件 │ ✏️ 手動編集: %d件 │ ❌ 却下: %d件 │ ⏳ 未決AI提案: %d件",
			cum.Accepted, cum.Edited, cum.Rejected, dr.PendingAICount)
		if dr.ActiveHumanSuggestionsCount > 0 {
			fmt.Fprintf(w, " │ 👤 人間の手動提案: %d件", dr.ActiveHumanSuggestionsCount)
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintf(w, "   Draft Improvement Rate: %s %s%s%s [%s]\n", rateBar, ansiBold+ansiCyan, rateText, ansiReset, verdictBadge)
		fmt.Fprintf(w, "   ✅ Accepted: %d │ ✏️ Edited: %d │ ❌ Rejected: %d │ ⏳ Pending AI: %d",
			cum.Accepted, cum.Edited, cum.Rejected, dr.PendingAICount)
		if dr.ActiveHumanSuggestionsCount > 0 {
			fmt.Fprintf(w, " │ 👤 Human Suggestions: %d", dr.ActiveHumanSuggestionsCount)
		}
		fmt.Fprintln(w)
	}

	if len(dr.RecentDecisions) > 0 {
		maxShow := min(len(dr.RecentDecisions), 4)
		for i := 0; i < maxShow; i++ {
			d := dr.RecentDecisions[i]
			icon := "✅"
			if d.Status == "EDITED" {
				icon = "✏️"
			} else if d.Status == "REJECTED" {
				icon = "❌"
			}
			fmt.Fprintf(w, "     %s [%s | %s] %q -> %q\n", icon, d.Status, d.Category, d.OrigSnippet, d.FinalSnippet)
		}
	}
}

func renderLearningSummaryDetails(w io.Writer, ls *gemini.LearningSummary, isJa bool) {
	if strings.TrimSpace(ls.RevisionsDigest) != "" {
		lbl := "📋 Revisions Digest:"
		if isJa {
			lbl = "📋 主な推敲・改善ポイントの要約 (Revisions Digest):"
		}
		fmt.Fprintf(w, "\n%s%s%s\n%s\n", ansiBold+ansiYellow, lbl, ansiReset, indentLines(ls.RevisionsDigest, "   "))
	}
	if len(ls.WeakPointsAndHabits) > 0 {
		lbl := "🔍 Observed Habits & Areas for Growth:"
		if isJa {
			lbl = "🔍 見られた傾向・クセ (Weak Points & Habits):"
		}
		fmt.Fprintf(w, "\n%s%s%s\n", ansiBold+ansiRed, lbl, ansiReset)
		for _, wp := range ls.WeakPointsAndHabits {
			fmt.Fprintf(w, "   • %s\n", wp)
		}
	}
	if len(ls.ActionableTips) > 0 {
		lbl := "🚀 Actionable Writing & Engineering Tips:"
		if isJa {
			lbl = "🚀 次回からすぐ実践できるコツ (Actionable Tips):"
		}
		fmt.Fprintf(w, "\n%s%s%s\n", ansiBold+ansiGreen, lbl, ansiReset)
		for _, tip := range ls.ActionableTips {
			fmt.Fprintf(w, "   ✔ %s\n", tip)
		}
	}
}

// RenderDocURLBox prints a highlighted box containing the Google Docs URL and Document ID.
func RenderDocURLBox(w io.Writer, docURL, docID string, isJa bool) {
	if w == nil || strings.TrimSpace(docURL) == "" {
		return
	}
	border := strings.Repeat("═", 76)
	label := "🔗 Google Document URL (Open to review inline suggestions & comments):"
	docIDLabel := "Document ID"
	formatTag := "📄 Format: Pageless Mode (DOCUMENT_MODE = PAGELESS)"
	if isJa {
		label = "🔗 Google ドキュメント URL（クリックして提案・コメントを確認）:"
		docIDLabel = "ドキュメント ID"
		formatTag = "📄 ページ設定: ページ分け無し形式 (Pageless Mode)"
	}
	fmt.Fprintf(w, "\n%s╔%s╗%s\n", ansiGreen, border, ansiReset)
	fmt.Fprintf(w, "%s║%s %s%s%s\n", ansiGreen, ansiReset, ansiBold, label, ansiReset)
	fmt.Fprintf(w, "%s║%s   %s%s%s\n", ansiGreen, ansiReset, ansiBold+ansiCyan, docURL, ansiReset)
	fmt.Fprintf(w, "%s║%s   %s: %s%s%s  │  %s%s%s\n", ansiGreen, ansiReset, docIDLabel, ansiYellow, docID, ansiReset, ansiDim, formatTag, ansiReset)
	fmt.Fprintf(w, "%s╚%s╝%s\n\n", ansiGreen, border, ansiReset)
}

// RenderSummaryTabBox prints a highlighted box when a Google Document Coaching Summary Tab (--summary-tab) was created or updated.
func RenderSummaryTabBox(w io.Writer, tabTitle, tabID, tabURL string, createdNew, isJa bool) {
	if w == nil || strings.TrimSpace(tabURL) == "" {
		return
	}
	border := strings.Repeat("═", 76)
	statusStr := "Updated existing tab in-place"
	if createdNew {
		statusStr = "Created new Document Tab (Pageless Mode)"
	}
	label := fmt.Sprintf("📑 Google Docs Coaching Summary Tab (%s):", statusStr)
	tabNameLbl := "Tab Name"
	tabIDLbl := "Tab ID"
	if isJa {
		if createdNew {
			statusStr = "新規タブを作成（ページ分け無し形式）"
		} else {
			statusStr = "同名タブを検索して最新内容へアップデート完了"
		}
		label = fmt.Sprintf("📑 Google ドキュメント・まとめタブ (%s):", statusStr)
		tabNameLbl = "タブ名"
		tabIDLbl = "タブ ID"
	}
	fmt.Fprintf(w, "%s╔%s╗%s\n", ansiMagenta, border, ansiReset)
	fmt.Fprintf(w, "%s║%s %s%s%s\n", ansiMagenta, ansiReset, ansiBold, label, ansiReset)
	fmt.Fprintf(w, "%s║%s   %s: %s%s%s  (%s: %s%s%s)\n", ansiMagenta, ansiReset, tabNameLbl, ansiBold+ansiYellow, tabTitle, ansiReset, tabIDLbl, ansiCyan, tabID, ansiReset)
	fmt.Fprintf(w, "%s║%s   %s%s%s\n", ansiMagenta, ansiReset, ansiBold+ansiGreen, tabURL, ansiReset)
	fmt.Fprintf(w, "%s╚%s╝%s\n\n", ansiMagenta, border, ansiReset)
}

// RenderLocalOutputBox prints a highlighted box containing the saved local output file path.
func RenderLocalOutputBox(w io.Writer, outputPath string, localMode, isJa bool) {
	if w == nil || strings.TrimSpace(outputPath) == "" {
		return
	}
	border := strings.Repeat("═", 76)
	label := "💾 Saved Local Proofreading & Coaching File:"
	tag := "⚡ Mode: Gemini-Only Local File Mode (--no-auth / No Google OAuth2 Token Used)"
	if !localMode {
		tag = "📄 Local Markdown Report Copy (-o / --output)"
	}
	if isJa {
		label = "💾 ローカル出力ファイル（推敲提案・ヒント・学習サマリーを保存）:"
		if localMode {
			tag = "⚡ モード: アクセストークン不使用・Gemini API ローカル出力モード (--no-auth)"
		} else {
			tag = "📄 ローカル Markdown レポート出力 (-o / --output)"
		}
	}
	fmt.Fprintf(w, "\n%s╔%s╗%s\n", ansiCyan, border, ansiReset)
	fmt.Fprintf(w, "%s║%s %s%s%s\n", ansiCyan, ansiReset, ansiBold, label, ansiReset)
	fmt.Fprintf(w, "%s║%s   %s%s%s\n", ansiCyan, ansiReset, ansiBold+ansiGreen, outputPath, ansiReset)
	fmt.Fprintf(w, "%s║%s   %s%s%s\n", ansiCyan, ansiReset, ansiDim, tag, ansiReset)
	fmt.Fprintf(w, "%s╚%s╝%s\n\n", ansiCyan, border, ansiReset)
}

func indentLines(s, prefix string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// PreparedVisualSummary provides metadata and optional local file path for a visual element (VISUAL_1, etc.)
// so an external AI agent can inspect the image using its own multimodal read_file tool.
type PreparedVisualSummary struct {
	TargetID    string `json:"targetId"`
	ObjectID    string `json:"objectId,omitempty"`
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	StartIndex  int    `json:"startIndex,omitempty"`
	EndIndex    int    `json:"endIndex,omitempty"`
	ImagePath   string `json:"imagePath,omitempty"`
}

// AgentPreparedContext holds the deterministic Step 1 context prepared by gdocumentor when running
// in Host-AI / Agent-Side LLM mode (MCP default or --prepare-only), allowing the client AI agent's
// own LLM to generate the review/summary/draft without calling the internal Gemini API.
type AgentPreparedContext struct {
	Phase                string                     `json:"phase"`
	NextStepInstructions string                     `json:"nextStepInstructions"`
	DocumentID           string                     `json:"documentId,omitempty"`
	RevisionID           string                     `json:"revisionId,omitempty"`
	DocumentURL          string                     `json:"documentUrl,omitempty"`
	ActiveTabID          string                     `json:"activeTabId,omitempty"`
	UsedDriveConversion  bool                       `json:"usedDriveConversion,omitempty"`
	EditableBlocks       []utf16idx.EditableBlock   `json:"editableBlocks,omitempty"`
	Tables               []gdocs.TableElement       `json:"tables,omitempty"`
	VisualElements       []PreparedVisualSummary    `json:"visualElements,omitempty"`
	ExistingSuggestions  []gdocs.ExistingSuggestion `json:"existingSuggestions,omitempty"`
	ExistingComments     []gdocs.ActiveComment      `json:"existingComments,omitempty"`
	ResolvedReviews      []gdocs.ResolvedReview     `json:"resolvedReviews,omitempty"`
	CoachingPrompt       string                     `json:"coachingPrompt"`
	ExpectedJSONSchema   map[string]any             `json:"expectedJsonSchema,omitempty"`
}

// CLIOutput is the top-level structured output object serialized to stdout in --json mode
// and rendered by RenderCoachingReport in interactive TUI mode.
type CLIOutput struct {
	Success                     bool                             `json:"success"`
	Command                     string                           `json:"command"`
	Model                       string                           `json:"model"`
	Mode                        string                           `json:"mode"`
	HintOnly                    bool                             `json:"hintOnly"`
	Language                    string                           `json:"language"`
	LocalMode                   bool                             `json:"localMode,omitempty"`
	RequiresAgentAI             bool                             `json:"requiresAgentAI,omitempty"`
	PreparedContext             *AgentPreparedContext            `json:"preparedContext,omitempty"`
	OutputFilePath              string                           `json:"outputFilePath,omitempty"`
	DocumentID                  string                           `json:"documentId"`
	RevisionID                  string                           `json:"revisionId,omitempty"`
	DocumentURL                 string                           `json:"documentUrl"`
	DocumentTitle               string                           `json:"documentTitle,omitempty"`
	ActiveTabID                 string                           `json:"activeTabId,omitempty"`
	SummaryTabID                string                           `json:"summaryTabId,omitempty"`
	SummaryTabTitle             string                           `json:"summaryTabTitle,omitempty"`
	SummaryTabURL               string                           `json:"summaryTabUrl,omitempty"`
	SummaryTabCreatedNew        bool                             `json:"summaryTabCreatedNew,omitempty"`
	CreatedNewDocument          bool                             `json:"createdNewDocument"`
	SelectionOnly               bool                             `json:"selectionOnly"`
	TargetFiles                 []string                         `json:"targetFiles,omitempty"`
	ReferenceContextFiles       []string                         `json:"referenceContextFiles,omitempty"`
	RagRuleFiles                []string                         `json:"ragRuleFiles,omitempty"`
	ActivePersona               string                           `json:"activePersona,omitempty"`
	OverallFeedback             string                           `json:"overallFeedback,omitempty"`
	LearningSummary             *gemini.LearningSummary          `json:"learningSummary,omitempty"`
	ExistingSuggestionsNote     string                           `json:"existingSuggestionsNote,omitempty"`
	AppliedSuggestionsCount     int                              `json:"appliedSuggestionsCount"`
	AppliedTextSuggestionsCount int                              `json:"appliedTextSuggestionsCount"`
	AppliedElementCommentsCount int                              `json:"appliedElementCommentsCount"`
	MatchedSuggestions          []utf16idx.MatchedSuggestion     `json:"matchedSuggestions,omitempty"`
	SkippedStaleSuggestions     []utf16idx.MatchedSuggestion     `json:"skippedStaleSuggestions,omitempty"`
	MatchedElementSuggestions   []gdocs.MatchedElementSuggestion `json:"matchedElementSuggestions,omitempty"`
	RevisionControl             *gdocs.RevisionControlSummary    `json:"revisionControl,omitempty"`
	AnalyzedItems               []gemini.AnalyzedReviewItem      `json:"analyzedItems,omitempty"`
	GeneratedDraft              string                           `json:"generatedDraft,omitempty"`
	ManageAction                string                           `json:"manageAction,omitempty"`
	ManagedCount                int                              `json:"managedCount,omitempty"`
	EditResult                  *gdocs.EditDocumentResult        `json:"editResult,omitempty"`
	InspectedDoc                *gdocs.ParsedDocument            `json:"inspectedDoc,omitempty"`
	DecisionRecord              *gdocs.DecisionRecordSummary     `json:"decisionRecord,omitempty"`
}
