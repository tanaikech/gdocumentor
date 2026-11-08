package workspace

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseTargetSpecAndStandaloneRange(t *testing.T) {
	tf1, err := ParseTargetSpec("internal/app.go:15-45")
	if err != nil {
		t.Fatal(err)
	}
	if tf1.FilePath != "internal/app.go" || tf1.StartLine != 15 || tf1.EndLine != 45 {
		t.Fatalf("unexpected line target filter: %+v", tf1)
	}

	tf2, err := ParseTargetSpec("docs/article.md@100:250")
	if err != nil {
		t.Fatal(err)
	}
	if tf2.FilePath != "docs/article.md" || tf2.StartChar != 100 || tf2.EndChar != 250 {
		t.Fatalf("unexpected char target filter: %+v", tf2)
	}

	sr1, err := ParseStandaloneRange("100:250")
	if err != nil || sr1.StartChar != 100 || sr1.EndChar != 250 {
		t.Fatalf("unexpected standalone char range: %+v (err=%v)", sr1, err)
	}

	sr2, err := ParseStandaloneRange("L10-L20")
	if err != nil || sr2.StartLine != 10 || sr2.EndLine != 20 {
		t.Fatalf("unexpected standalone line range: %+v (err=%v)", sr2, err)
	}

	// Test GitHub URL with #L15-L45 anchor
	ghTF, err := ParseTargetSpec("https://github.com/tanaikech/DocuMentorAI/blob/master/src/code.js#L15-L45")
	if err != nil {
		t.Fatal(err)
	}
	if ghTF.FilePath != "https://github.com/tanaikech/DocuMentorAI/blob/master/src/code.js" || ghTF.StartLine != 15 || ghTF.EndLine != 45 {
		t.Fatalf("unexpected GitHub #L anchor filter: %+v", ghTF)
	}

	// Test plain HTTPS URL without range (must not split on https:)
	plainURLTF, err := ParseTargetSpec("https://example.com/blog/post")
	if err != nil {
		t.Fatal(err)
	}
	if plainURLTF.FilePath != "https://example.com/blog/post" || plainURLTF.StartLine != 0 || plainURLTF.StartChar != -1 {
		t.Fatalf("unexpected plain URL target filter: %+v", plainURLTF)
	}
}

func TestRemoteURLFetchAndHTMLExtraction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Ignored</title><script>console.log("ignore");</script></head>
<body>
  <nav>Menu</nav>
  <article>
    <h1>Introduction to gdocumentor</h1>
    <p>This is the first paragraph of the web article.</p>
    <p>This is the second paragraph to proofread.</p>
  </article>
  <footer>Copyright</footer>
</body>
</html>`))
	}))
	defer srv.Close()

	bundle, err := ResolveBundle(InputConfig{
		Targets:  []string{srv.URL + "/article.html:1-5"},
		AutoDeps: false,
	})
	if err != nil {
		t.Fatalf("ResolveBundle on remote HTML URL failed: %v", err)
	}
	if len(bundle.TargetFiles) != 1 {
		t.Fatalf("expected 1 target file from remote URL, got %d", len(bundle.TargetFiles))
	}
	content := bundle.TargetFiles[0].Content
	if !strings.Contains(content, "# Introduction to gdocumentor") || !strings.Contains(content, "first paragraph") {
		t.Fatalf("unexpected extracted HTML text:\n%s", content)
	}
	if strings.Contains(content, "console.log") || strings.Contains(content, "Menu") {
		t.Fatalf("expected script and nav tags to be stripped, got:\n%s", content)
	}
}

func TestResolveBundleMultiFilePartialTargetAndAutoDeps(t *testing.T) {
	tmpDir := t.TempDir()

	// Create go.mod
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/testproj\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create internal/helper/helper.go (imported dependency)
	helperDir := filepath.Join(tmpDir, "internal", "helper")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	helperCode := "package helper\n\nfunc FormatGreeting(name string) string {\n\treturn \"Hello, \" + name\n}\n"
	if err := os.WriteFile(filepath.Join(helperDir, "helper.go"), []byte(helperCode), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create main.go importing example.com/testproj/internal/helper
	mainCode := "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/testproj/internal/helper\"\n)\n\nfunc main() {\n\tmsg := helper.FormatGreeting(\"World\")\n\tfmt.Println(msg)\n}\n"
	mainPath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(mainPath, []byte(mainCode), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create local RAG rule file
	rulesPath := filepath.Join(tmpDir, "style_guide.md")
	if err := os.WriteFile(rulesPath, []byte("# Coding Rules\n- Always avoid hardcoded secrets.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Target only lines 8-10 of main.go
	bundle, err := ResolveBundle(InputConfig{
		WorkDir:   tmpDir,
		Targets:   []string{"main.go:8-10"},
		AutoDeps:  true,
		RulePaths: []string{"style_guide.md"},
	})
	if err != nil {
		t.Fatalf("ResolveBundle failed: %v", err)
	}

	if len(bundle.TargetFiles) != 1 {
		t.Fatalf("expected 1 target file, got %d", len(bundle.TargetFiles))
	}
	if !bundle.HasPartialScope {
		t.Fatalf("expected HasPartialScope=true")
	}

	// Verify auto-deps resolved internal/helper/helper.go
	if len(bundle.ReferenceFiles) != 1 || bundle.ReferenceFiles[0].DisplayPath != "internal/helper/helper.go" {
		t.Fatalf("expected auto-discovered reference file internal/helper/helper.go, got %+v", bundle.ReferenceFiles)
	}

	// Verify RAG rule loaded
	if len(bundle.RagRules) != 1 || bundle.RagRules[0].Title != "style_guide.md" || bundle.RagRules[0].IsBinary {
		t.Fatalf("unexpected RAG rules: %+v", bundle.RagRules)
	}

	// Verify only lines 8-10 are marked TargetEditable=true
	editableCount := 0
	for _, blk := range bundle.EditableBlocks {
		if blk.TargetEditable {
			editableCount++
			if blk.LineNumber < 8 || blk.LineNumber > 10 {
				t.Fatalf("block on line %d should not be TargetEditable", blk.LineNumber)
			}
		}
	}
	if editableCount != 3 {
		t.Fatalf("expected 3 editable lines (lines 8, 9, 10), got %d", editableCount)
	}
}

func TestSampleArticleMarkdownBase64ImageEmbedding(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	sampleMD := filepath.Join(repoRoot, "sample", "sample_article.md")

	bundle, err := ResolveBundle(InputConfig{
		WorkDir:  repoRoot,
		Paths:    []string{sampleMD},
		AutoDeps: false,
	})
	if err != nil {
		t.Fatalf("ResolveBundle on sample/sample_article.md failed: %v", err)
	}
	if !bundle.UseDriveConversion || bundle.ConversionMimeType != MimeTypeMarkdown {
		t.Fatalf("expected UseDriveConversion=true and MimeTypeMarkdown, got use=%v mime=%q", bundle.UseDriveConversion, bundle.ConversionMimeType)
	}
	if len(bundle.EmbeddedImages) != 1 {
		t.Fatalf("expected 1 embedded image from sample_article.md, got %d", len(bundle.EmbeddedImages))
	}
	img := bundle.EmbeddedImages[0]
	if img.OriginalRef != "sample_figure.png" || !strings.HasPrefix(img.MimeType, "image/") || len(img.Data) == 0 {
		t.Fatalf("unexpected embedded image metadata: ref=%q mime=%q bytes=%d", img.OriginalRef, img.MimeType, len(img.Data))
	}
	convStr := string(bundle.ConversionBytes)
	if !strings.Contains(convStr, "![GAS Workflow Automation and Gemini AI Integration](data:"+img.MimeType+";base64,") {
		t.Fatalf("expected ConversionBytes to contain embedded base64 data URI for sample_figure.png")
	}
}

func TestFileClassificationDOCXAndPDFSupport(t *testing.T) {
	convCases := []string{"doc.md", "notes.markdown", "readme.txt", "paper.pdf", "manuscript.docx"}
	for _, name := range convCases {
		isConv, _ := ClassifyTargetFile(name)
		if !isConv {
			t.Fatalf("expected %q to be classified as convertible document (IsConvertibleDoc=true)", name)
		}
	}

	scriptCases := []string{"main.go", "code.js", "app.ts", "run.py", "build.sh", "server.rs", "config.yaml"}
	for _, name := range scriptCases {
		isConv, _ := ClassifyTargetFile(name)
		if isConv {
			t.Fatalf("expected script/code file %q to have IsConvertibleDoc=false", name)
		}
	}

	// Build an in-memory .docx file and verify ResolveBundle & ExtractTextFromDOCXBytes
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("word/document.xml")
	_, _ = f.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Hello from DOCX manuscript.</w:t></w:r></w:p>
  </w:body>
</w:document>`))
	_ = zw.Close()

	tmpDir := t.TempDir()
	docxPath := filepath.Join(tmpDir, "test.docx")
	if err := os.WriteFile(docxPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, err := ResolveBundle(InputConfig{
		WorkDir:  tmpDir,
		Paths:    []string{"test.docx"},
		AutoDeps: false,
	})
	if err != nil {
		t.Fatalf("ResolveBundle on .docx failed: %v", err)
	}
	if !bundle.UseDriveConversion || bundle.ConversionMimeType != MimeTypeDOCX {
		t.Fatalf("expected DOCX UseDriveConversion=true and MimeTypeDOCX, got %v / %q", bundle.UseDriveConversion, bundle.ConversionMimeType)
	}
	if !strings.Contains(bundle.TargetFiles[0].Content, "Hello from DOCX manuscript.") {
		t.Fatalf("expected extracted DOCX text, got %q", bundle.TargetFiles[0].Content)
	}
}

func TestExtractPersonaFromRagRules(t *testing.T) {
	rules := []RagRuleFile{
		{
			ID:         "RAG_1",
			Title:      "japanese_prof.md",
			SourceType: "LOCAL_TEXT",
			MimeType:   "text/markdown",
			Content: `# AIの位置づけ
あなたは大学で日本語を教えている日本人です。学生や研究者が書いた文章を自然で学術的に正確な日本語へ導いてください。

# 表記ルール
- 「〜することができる」のような冗長表現を避けること。`,
		},
	}

	persona := ExtractPersonaFromRagRules(rules)
	if !strings.Contains(persona, "あなたは大学で日本語を教えている日本人です。") {
		t.Fatalf("expected Japanese professor persona to be extracted from RAG rules, got %q", persona)
	}
	if strings.Contains(persona, "冗長表現を避けること") {
		t.Fatalf("expected non-persona style rules to be excluded from persona header, got %q", persona)
	}
}

func TestSampleRulesFallbackLoading(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	rules, err := LoadLocalRagRules(repoRoot, []string{"./rules/writing_guidelines.md", "./rules/coding_standards.md"}, "")
	if err != nil {
		t.Fatalf("expected LoadLocalRagRules to resolve ./rules/writing_guidelines.md and ./rules/coding_standards.md via sample/rules fallback, got err: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 loaded sample RAG rules, got %d", len(rules))
	}
}

func TestNormalizeCrossPlatformPathAndWindowsWSLTargetSpecs(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		targetOS string
		want     string
	}{
		{
			name:     "Windows backslash drive path on Linux/WSL2",
			raw:      `C:\Users\alice\project\sample\sample_article.md`,
			targetOS: "linux",
			want:     "/mnt/c/Users/alice/project/sample/sample_article.md",
		},
		{
			name:     "Windows forward-slash drive path on Linux/WSL2",
			raw:      "D:/Work/repo/main.go",
			targetOS: "linux",
			want:     "/mnt/d/Work/repo/main.go",
		},
		{
			name:     "WSL UNC wsl.localhost path on Linux/WSL2",
			raw:      `\\wsl.localhost\Ubuntu-24.04\home\alice\GitHub\gdocumentor\README.md`,
			targetOS: "linux",
			want:     "/home/alice/GitHub/gdocumentor/README.md",
		},
		{
			name:     "WSL UNC wsl$ path on Linux/WSL2",
			raw:      `\\wsl$\Ubuntu\home\alice\main.go`,
			targetOS: "linux",
			want:     "/home/alice/main.go",
		},
		{
			name:     "Relative Windows backslash path on Linux/macOS",
			raw:      `sample\rules\writing_guidelines.md`,
			targetOS: "darwin",
			want:     "sample/rules/writing_guidelines.md",
		},
		{
			name:     "WSL /mnt/c path on native Windows",
			raw:      "/mnt/c/Users/alice/project/main.go",
			targetOS: "windows",
			want:     `C:\Users\alice\project\main.go`,
		},
		{
			name:     "URLs are never altered",
			raw:      "https://github.com/tanaikech/gdocumentor/blob/master/main.go",
			targetOS: "linux",
			want:     "https://github.com/tanaikech/gdocumentor/blob/master/main.go",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeCrossPlatformPathForOS(tc.raw, tc.targetOS)
			if got != tc.want {
				t.Fatalf("NormalizeCrossPlatformPathForOS(%q, %q) = %q, want %q", tc.raw, tc.targetOS, got, tc.want)
			}
		})
	}

	// Verify ParseTargetSpec properly separates Windows drive letter C:\... from :start-end line ranges and @start:end char ranges
	tsLine, err := ParseTargetSpec(`C:\Users\alice\repo\main.go:15-45`)
	if err != nil {
		t.Fatalf("ParseTargetSpec Windows path with line range failed: %v", err)
	}
	if tsLine.StartLine != 15 || tsLine.EndLine != 45 {
		t.Fatalf("expected lines 15-45, got %d-%d (path=%q)", tsLine.StartLine, tsLine.EndLine, tsLine.FilePath)
	}
	if runtime.GOOS != "windows" && tsLine.FilePath != "/mnt/c/Users/alice/repo/main.go" {
		t.Fatalf("expected WSL normalized path '/mnt/c/Users/alice/repo/main.go', got %q", tsLine.FilePath)
	}

	tsChar, err := ParseTargetSpec(`\\wsl.localhost\Ubuntu\home\alice\article.md@100:250`)
	if err != nil {
		t.Fatalf("ParseTargetSpec WSL UNC path with char range failed: %v", err)
	}
	if tsChar.StartChar != 100 || tsChar.EndChar != 250 {
		t.Fatalf("expected chars 100:250, got %d:%d", tsChar.StartChar, tsChar.EndChar)
	}
	if runtime.GOOS != "windows" && tsChar.FilePath != "/home/alice/article.md" {
		t.Fatalf("expected normalized path '/home/alice/article.md', got %q", tsChar.FilePath)
	}
}
