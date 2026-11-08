package utf16idx

import (
	"testing"
)

func TestUTF16LenAndOffsets(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantUnits int
	}{
		{"empty", "", 0},
		{"ascii", "Hello, World!", 13},
		{"japanese_bmp", "日本語の推敲テスト", 9},
		{"surrogate_emoji_single", "🚀", 2},
		{"mixed_jp_emoji_code", "🚀 推敲コーチ (DocuMentor AI) 🎓💡!", 30},
		{"cjk_extension_b_surrogate", "𠮟る", 3}, // 𠮟 (U+20B9F) is 2 UTF-16 units + る (1 unit) = 3
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16Len(tc.input)
			if got != tc.wantUnits {
				t.Fatalf("UTF16Len(%q) = %d, want %d", tc.input, got, tc.wantUnits)
			}
		})
	}
}

func TestFindSubstringUTF16WithMultibyteAndSurrogates(t *testing.T) {
	// "🚀 " = 2 + 1 = 3 UTF-16 units
	// "日本語" = 3 UTF-16 units (starts at 3, ends at 6)
	// "と🎓" = 1 + 2 = 3 UTF-16 units (starts at 6, ends at 9)
	// "テスト" = 3 UTF-16 units (starts at 9, ends at 12)
	text := "🚀 日本語と🎓テスト"

	start, end, _, ok := FindSubstringUTF16(text, "日本語", 0)
	if !ok || start != 3 || end != 6 {
		t.Fatalf("expected 日本語 at [3, 6), got [%d, %d) ok=%v", start, end, ok)
	}

	start2, end2, _, ok2 := FindSubstringUTF16(text, "テスト", 0)
	if !ok2 || start2 != 9 || end2 != 12 {
		t.Fatalf("expected テスト at [9, 12), got [%d, %d) ok=%v", start2, end2, ok2)
	}
}

func TestMatchSuggestionsToBlocksBottomToTopAndDuplicateAvoidance(t *testing.T) {
	// Block 1 starts at Google Docs index 1: "🚀 foo bar foo"
	// "🚀 " = 3 units -> first "foo" is at [1+3, 1+6) = [4, 7)
	// " bar " = 5 units -> second "foo" is at [1+11, 1+14) = [12, 15)
	// Block 2 (TargetEditable=false, e.g., outside --target range): starts at 20: "ignored target"
	// Block 3 (TargetEditable=true): starts at 50: "日本語の𠮟る表現を改善する"
	// "日本語の" = 4 units -> "𠮟る表現" = 2 + 1 + 2 = 5 units -> [50+4, 50+9) = [54, 59)
	blocks := []EditableBlock{
		{
			Text:           "🚀 foo bar foo",
			StartIndex:     1,
			EndIndex:       15,
			Source:         "body",
			FilePath:       "main.go",
			LineNumber:     10,
			TargetEditable: true,
		},
		{
			Text:           "ignored target",
			StartIndex:     20,
			EndIndex:       34,
			Source:         "body",
			FilePath:       "main.go",
			LineNumber:     11,
			TargetEditable: false, // Outside user's target range!
		},
		{
			Text:           "日本語の𠮟る表現を改善する",
			StartIndex:     50,
			EndIndex:       64,
			Source:         "body",
			FilePath:       "doc.md",
			LineNumber:     25,
			TargetEditable: true,
		},
	}

	suggestions := []RawSuggestion{
		{
			OriginalText:  "foo",
			SuggestedText: "baz",
			Category:      "Naming",
			Reason:        "First foo replacement",
			LearningPoint: "Use descriptive names",
		},
		{
			OriginalText:  "foo",
			SuggestedText: "qux",
			Category:      "Naming",
			Reason:        "Second foo replacement (must not overlap first)",
			LearningPoint: "Avoid duplicate placeholders",
		},
		{
			OriginalText:  "ignored target",
			SuggestedText: "should not match because TargetEditable is false",
			Category:      "Out of Scope",
		},
		{
			OriginalText:  "𠮟る表現",
			SuggestedText: "適切な表現",
			Category:      "語彙",
			Reason:        "サロゲートペア文字を含む箇所の置換",
			LearningPoint: "UTF-16コードユニット計算の確認",
		},
	}

	matched := MatchSuggestionsToBlocks(blocks, suggestions, "t.0", false, "Revision")
	if len(matched) != 3 {
		t.Fatalf("expected 3 matched suggestions (excluding non-editable block), got %d", len(matched))
	}

	// Verify descending StartIndex order (bottom-to-top):
	// 1st in slice: [54, 59) ("𠮟る表現")
	// 2nd in slice: [12, 15) (second "foo")
	// 3rd in slice: [4, 7) (first "foo")
	if matched[0].StartIndex != 54 || matched[0].EndIndex != 59 {
		t.Fatalf("expected matched[0] at [54, 59), got [%d, %d)", matched[0].StartIndex, matched[0].EndIndex)
	}
	if matched[1].StartIndex != 12 || matched[1].EndIndex != 15 {
		t.Fatalf("expected matched[1] at [12, 15), got [%d, %d)", matched[1].StartIndex, matched[1].EndIndex)
	}
	if matched[2].StartIndex != 4 || matched[2].EndIndex != 7 {
		t.Fatalf("expected matched[2] at [4, 7), got [%d, %d)", matched[2].StartIndex, matched[2].EndIndex)
	}
}
