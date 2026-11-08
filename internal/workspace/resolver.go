package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

const (
	MimeTypePDF      = "application/pdf"
	MimeTypeDOCX     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MimeTypeMarkdown = "text/markdown"
	MimeTypePlain    = "text/plain"
)

var (
	reStripMdPrefix = regexp.MustCompile(`^(?:#{1,6}\s+|[-*+]\s+|\d+\.\s+|>\s+)+`)
	reStripMdInline = regexp.MustCompile("(\\*\\*|__|\\*|_|`|~~)")
)

// TargetFilter specifies an optional sub-range within a target file to proofread.
type TargetFilter struct {
	FilePath  string `json:"filePath"`
	StartLine int    `json:"startLine,omitempty"` // 1-based inclusive (0 means no line start restriction)
	EndLine   int    `json:"endLine,omitempty"`   // 1-based inclusive (0 means no line end restriction)
	StartChar int    `json:"startChar,omitempty"` // 0-based rune offset inclusive (-1 means unset)
	EndChar   int    `json:"endChar,omitempty"`   // 0-based rune offset exclusive (-1 means unset)
}

// FileEntry represents a loaded target or reference file from the local filesystem, URL, or stdin.
type FileEntry struct {
	Path               string          `json:"path"`
	DisplayPath        string          `json:"displayPath"`
	Content            string          `json:"content,omitempty"`
	RawBytes           []byte          `json:"-"`
	MimeType           string          `json:"mimeType,omitempty"`
	IsConvertibleDoc   bool            `json:"isConvertibleDoc"`
	ConvertedMarkdown  string          `json:"-"`
	EmbeddedImageCount int             `json:"embeddedImageCount,omitempty"`
	EmbeddedImages     []EmbeddedImage `json:"-"`
	Filters            []TargetFilter  `json:"filters,omitempty"`
}

// RagRuleFile represents an organization/project guideline or rulebook file (from local disk or Google Drive).
type RagRuleFile struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	SourceType string `json:"sourceType"` // "LOCAL_TEXT", "LOCAL_BINARY", "GOOGLE_DOC", "DRIVE_TEXT", "REMOTE_TEXT", "REMOTE_BINARY"
	MimeType   string `json:"mimeType"`
	Content    string `json:"content,omitempty"`
	Bytes      []byte `json:"-"`
	IsBinary   bool   `json:"isBinary"`
	CharCount  int    `json:"charCount"`
}

// Bundle holds all resolved target files, reference context files, RAG rule files,
// Google Drive conversion payloads, and the deterministic Google Docs mapping for local-to-Docs proofreading.
type Bundle struct {
	TargetFiles        []FileEntry              `json:"targetFiles"`
	ReferenceFiles     []FileEntry              `json:"referenceFiles"`
	RagRules           []RagRuleFile            `json:"ragRules"`
	CombinedDocText    string                   `json:"combinedDocText"`
	EditableBlocks     []utf16idx.EditableBlock `json:"editableBlocks"`
	HasPartialScope    bool                     `json:"hasPartialScope"`
	DocSelectionRanges []utf16idx.RangeInterval `json:"docSelectionRanges,omitempty"`
	UseDriveConversion bool                     `json:"useDriveConversion"`
	ConversionMimeType string                   `json:"conversionMimeType,omitempty"`
	ConversionBytes    []byte                   `json:"-"`
	EmbeddedImages     []EmbeddedImage          `json:"-"`
	ExtractedPersona   string                   `json:"extractedPersona,omitempty"`
}

// InputConfig defines the parameters for resolving local files, remote URLs, GitHub repositories, ranges, dependencies, and rules.
type InputConfig struct {
	RepoURL      string   `json:"repoUrl,omitempty"` // Optional GitHub repository URL (--repo) to shallow-clone as workspace
	Paths        []string `json:"paths"`
	Targets      []string `json:"targets"`      // e.g. "main.go:15-45", "article.md@100:250", or "https://github.com/.../code.js#L15-L45"
	RangeSpec    string   `json:"rangeSpec"`    // e.g. "100:250" or "L15:L45" or "15-45"
	ContextPaths []string `json:"contextPaths"` // Explicit reference context files/dirs/URLs
	AutoDeps     bool     `json:"autoDeps"`
	RulePaths    []string `json:"rulePaths"`
	RulesDir     string   `json:"rulesDir"`
	StdinContent string   `json:"stdinContent,omitempty"`
	WorkDir      string   `json:"workDir,omitempty"`
}

// ClassifyTargetFile determines whether a file should be converted to Google Docs via Google Drive API
// (Markdown .md/.markdown, Text .txt, PDF .pdf, DOCX .docx) or inserted as raw text into a newly created
// Google Doc (scripts and source code files such as .go, .js, .py, .sh, etc.).
func ClassifyTargetFile(pathOrName string) (isConvertibleDoc bool, mimeType string) {
	if pathOrName == "stdin" || pathOrName == "-" {
		return true, MimeTypeMarkdown
	}
	lower := strings.ToLower(filepath.Base(pathOrName))
	if qIdx := strings.IndexByte(lower, '?'); qIdx >= 0 {
		lower = lower[:qIdx]
	}
	ext := strings.ToLower(filepath.Ext(lower))
	switch ext {
	case ".md", ".markdown":
		return true, MimeTypeMarkdown
	case ".txt", ".text":
		return true, MimeTypeMarkdown
	case ".pdf":
		return true, MimeTypePDF
	case ".docx":
		return true, MimeTypeDOCX
	default:
		return false, MimeTypePlain
	}
}

// NormalizeCrossPlatformPath normalizes file paths across Windows, WSL2/Linux, and macOS so that:
// - When running on Linux/WSL2/macOS, Windows drive paths ("C:\Users\..." or "C:/Users/...") are converted to "/mnt/c/Users/..."
//   and Windows WSL UNC paths ("\\wsl.localhost\Ubuntu\home\..." or "\\wsl$\Ubuntu\home\...") are converted to "/home/...".
// - When running on Windows, WSL mount paths ("/mnt/c/Users/...") are converted to "C:\Users\...".
func NormalizeCrossPlatformPath(raw string) string {
	return NormalizeCrossPlatformPathForOS(raw, runtime.GOOS)
}

// NormalizeCrossPlatformPathForOS normalizes a path for a specific target GOOS ("windows", "linux", "darwin").
func NormalizeCrossPlatformPathForOS(raw, goos string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "-" || IsHTTPURL(trimmed) || strings.HasPrefix(strings.ToLower(trimmed), "file://") {
		return trimmed
	}

	if goos != "windows" {
		// 1. Handle WSL UNC paths from Windows host: \\wsl.localhost\<Distro>\path or \\wsl$\<Distro>\path
		slashed := strings.ReplaceAll(trimmed, "\\", "/")
		lowerSlashed := strings.ToLower(slashed)
		for _, prefix := range []string{"//wsl.localhost/", "//wsl$/"} {
			if strings.HasPrefix(lowerSlashed, prefix) {
				rest := slashed[len(prefix):]
				if slashIdx := strings.IndexByte(rest, '/'); slashIdx >= 0 {
					sub := rest[slashIdx:]
					if sub == "" {
						return "/"
					}
					return sub
				}
				return "/"
			}
		}

		// 2. Handle Windows drive letter paths: C:\Users\... or C:/Users/...
		if len(trimmed) >= 2 && isASCIILetter(trimmed[0]) && trimmed[1] == ':' && (len(trimmed) == 2 || trimmed[2] == '\\' || trimmed[2] == '/') {
			drive := strings.ToLower(string(trimmed[0]))
			rest := strings.ReplaceAll(trimmed[2:], "\\", "/")
			rest = strings.TrimPrefix(rest, "/")
			if rest == "" {
				return "/mnt/" + drive
			}
			return "/mnt/" + drive + "/" + rest
		}

		// 3. Handle relative Windows backslash paths (e.g. "sample\sample_article.md")
		if strings.Contains(trimmed, "\\") && !strings.Contains(trimmed, "/") {
			return strings.ReplaceAll(trimmed, "\\", "/")
		}
		return trimmed
	}

	// On Windows (goos == "windows"): convert WSL /mnt/<drive>/... to <DRIVE>:\...
	lowerTrimmed := strings.ToLower(trimmed)
	if strings.HasPrefix(lowerTrimmed, "/mnt/") && len(trimmed) >= 6 && isASCIILetter(trimmed[5]) && (len(trimmed) == 6 || trimmed[6] == '/') {
		drive := strings.ToUpper(string(trimmed[5]))
		if len(trimmed) == 6 || trimmed[6:] == "/" {
			return drive + `:\`
		}
		rest := strings.ReplaceAll(trimmed[6:], "/", `\`)
		return drive + ":" + rest
	}
	return trimmed
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ParseTargetSpec parses a target specification string such as:
// - "path/to/file.go:15-45" (1-based line range 15 to 45 inclusive)
// - "path/to/file.go:L15-L45" or "path/to/file.go:15:45"
// - "https://github.com/owner/repo/blob/main/src/code.js#L15-L45" (GitHub line anchor)
// - "path/to/article.md@100:250" or "https://example.com/post@100:250" (0-based rune offset range [100, 250))
// - "path/to/file.go" or "https://example.com/post" (entire file or URL)
func ParseTargetSpec(spec string) (TargetFilter, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return TargetFilter{}, errors.New("empty target specification")
	}

	filter := TargetFilter{
		StartChar: -1,
		EndChar:   -1,
	}

	// 1. Check for GitHub-style #L15-L45 or #L15 line anchor at the end
	if hashIdx := strings.LastIndex(trimmed, "#"); hashIdx > 0 {
		filePart := strings.TrimSpace(trimmed[:hashIdx])
		fragPart := strings.TrimSpace(trimmed[hashIdx+1:])
		if strings.HasPrefix(strings.ToUpper(fragPart), "L") {
			sLine, eLine, ok := parseNumericPair(fragPart, true)
			if ok {
				filter.FilePath = NormalizeCrossPlatformPath(filePart)
				filter.StartLine = sLine
				filter.EndLine = eLine
				return filter, nil
			}
		}
	}

	// 2. Check for @start:end or @start-end character offset range
	if atIdx := strings.LastIndex(trimmed, "@"); atIdx > 0 {
		filePart := strings.TrimSpace(trimmed[:atIdx])
		rangePart := strings.TrimSpace(trimmed[atIdx+1:])
		sChar, eChar, ok := parseNumericPair(rangePart, false)
		if ok {
			filter.FilePath = NormalizeCrossPlatformPath(filePart)
			filter.StartChar = sChar
			filter.EndChar = eChar
			return filter, nil
		}
	}

	// 3. Check for :start-end or :Lstart-Lend line range (ignoring "http:" / "https:" scheme colons and Windows "C:\" drive colons)
	schemeEnd := 0
	if idx := strings.Index(trimmed, "://"); idx >= 0 {
		schemeEnd = idx + 3
	} else if len(trimmed) >= 2 && isASCIILetter(trimmed[0]) && trimmed[1] == ':' {
		schemeEnd = 2
	}
	if colonIdx := strings.LastIndex(trimmed, ":"); colonIdx > schemeEnd {
		filePart := strings.TrimSpace(trimmed[:colonIdx])
		rangePart := strings.TrimSpace(trimmed[colonIdx+1:])
		// Handle "file.go:15:45" where there are two colons after schemeEnd
		if prevColon := strings.LastIndex(filePart, ":"); prevColon > schemeEnd {
			maybeLine1 := strings.TrimPrefix(strings.ToUpper(filePart[prevColon+1:]), "L")
			if _, err1 := strconv.Atoi(maybeLine1); err1 == nil {
				combinedRange := filePart[prevColon+1:] + "-" + rangePart
				filePart = filePart[:prevColon]
				rangePart = combinedRange
			}
		}
		sLine, eLine, ok := parseNumericPair(rangePart, true)
		if ok {
			filter.FilePath = NormalizeCrossPlatformPath(filePart)
			filter.StartLine = sLine
			filter.EndLine = eLine
			return filter, nil
		}
	}

	filter.FilePath = NormalizeCrossPlatformPath(trimmed)
	return filter, nil
}

// ParseStandaloneRange parses a standalone --range flag value:
// - "L15-L45" or "15-45" -> line range (StartLine=15, EndLine=45)
// - "100:250" or "@100:250" -> character offset range (StartChar=100, EndChar=250)
func ParseStandaloneRange(rangeSpec string) (TargetFilter, error) {
	trimmed := strings.TrimSpace(rangeSpec)
	if trimmed == "" {
		return TargetFilter{StartChar: -1, EndChar: -1}, nil
	}

	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "L") || (strings.Contains(trimmed, "-") && !strings.HasPrefix(trimmed, "@")) {
		sLine, eLine, ok := parseNumericPair(trimmed, true)
		if !ok {
			return TargetFilter{}, fmt.Errorf("invalid line range specification %q (expected e.g. 15-45 or L15:L45)", rangeSpec)
		}
		return TargetFilter{
			StartLine: sLine,
			EndLine:   eLine,
			StartChar: -1,
			EndChar:   -1,
		}, nil
	}

	cleanChar := strings.TrimPrefix(trimmed, "@")
	sChar, eChar, ok := parseNumericPair(cleanChar, false)
	if !ok {
		return TargetFilter{}, fmt.Errorf("invalid character range specification %q (expected e.g. 100:250 or 15-45)", rangeSpec)
	}
	return TargetFilter{
		StartChar: sChar,
		EndChar:   eChar,
	}, nil
}

func parseNumericPair(raw string, isLine bool) (int, int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, 0, false
	}
	var parts []string
	if strings.Contains(s, "-") {
		parts = strings.SplitN(s, "-", 2)
	} else if strings.Contains(s, ":") {
		parts = strings.SplitN(s, ":", 2)
	} else {
		// Single line number e.g. "file.go:25"
		if isLine {
			p := strings.TrimPrefix(strings.ToUpper(s), "L")
			v, err := strconv.Atoi(p)
			if err == nil && v >= 1 {
				return v, v, true
			}
		}
		return 0, 0, false
	}
	if len(parts) != 2 {
		return 0, 0, false
	}
	p1 := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(parts[0])), "L")
	p2 := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(parts[1])), "L")
	v1, err1 := strconv.Atoi(p1)
	v2, err2 := strconv.Atoi(p2)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	if isLine {
		if v1 < 1 || v2 < v1 {
			return 0, 0, false
		}
	} else {
		if v1 < 0 || v2 <= v1 {
			return 0, 0, false
		}
	}
	return v1, v2, true
}

// ResolveBundle resolves all target files, remote URLs, GitHub repositories, target sub-ranges,
// explicit/auto-discovered context files, and local/remote RAG rules in parallel, and builds the
// Google Drive conversion payload or deterministic Google Docs text & UTF-16 block mapping.
func ResolveBundle(cfg InputConfig) (*Bundle, error) {
	workDir := NormalizeCrossPlatformPath(cfg.WorkDir)
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}

	// If --auto-deps is enabled and no explicit --repo was passed, check if any target/input is a GitHub Blob URL
	if strings.TrimSpace(cfg.RepoURL) == "" && cfg.AutoDeps {
		for _, rawSpec := range append(append([]string{}, cfg.Targets...), cfg.Paths...) {
			if tf, pErr := ParseTargetSpec(rawSpec); pErr == nil {
				if blobSpec, ok := ParseGitHubBlobURL(tf.FilePath); ok {
					cfg.RepoURL = fmt.Sprintf("https://github.com/%s/%s/tree/%s", blobSpec.Owner, blobSpec.Repo, blobSpec.Ref)
					break
				}
			}
		}
	}

	// If --repo <github-repo-url> is specified (or inferred above), shallow-clone the repository into a temporary workspace
	if strings.TrimSpace(cfg.RepoURL) != "" {
		cloneDir, subPath, cleanup, err := CloneGitHubRepoShallow(cfg.RepoURL)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		workDir = cloneDir
		if len(cfg.Paths) == 0 && len(cfg.Targets) == 0 {
			if subPath != "" {
				cfg.Paths = []string{subPath}
			} else {
				cfg.Paths = []string{"."}
			}
		}
	}

	targetMap := make(map[string]*FileEntry)
	var targetOrder []string

	addTargetEntry := func(entry FileEntry, filter *TargetFilter) {
		entry.Content = normalizeNewlines(entry.Content)
		existing, exists := targetMap[entry.Path]
		if !exists {
			copyEntry := entry
			copyEntry.Filters = nil
			existing = &copyEntry
			targetMap[entry.Path] = existing
			targetOrder = append(targetOrder, entry.Path)
		}
		if filter != nil && (filter.StartLine > 0 || filter.EndLine > 0 || filter.StartChar >= 0) {
			existing.Filters = append(existing.Filters, *filter)
		}
	}

	standaloneFilter, err := ParseStandaloneRange(cfg.RangeSpec)
	if err != nil {
		return nil, err
	}
	hasStandaloneFilter := standaloneFilter.StartLine > 0 || standaloneFilter.EndLine > 0 || standaloneFilter.StartChar >= 0

	// 1. Process --target specifications in parallel
	if len(cfg.Targets) > 0 {
		type targetLoadRes struct {
			idx    int
			entry  FileEntry
			filter TargetFilter
			err    error
		}
		tCh := make(chan targetLoadRes, len(cfg.Targets))
		var tWg sync.WaitGroup
		for idx, tSpec := range cfg.Targets {
			tWg.Add(1)
			go func(i int, spec string) {
				defer tWg.Done()
				tf, pErr := ParseTargetSpec(spec)
				if pErr != nil {
					tCh <- targetLoadRes{idx: i, err: pErr}
					return
				}
				entry, rErr := readTargetFileEntry(workDir, tf.FilePath)
				if rErr != nil {
					tCh <- targetLoadRes{idx: i, err: rErr}
					return
				}
				tf.FilePath = entry.DisplayPath
				tCh <- targetLoadRes{idx: i, entry: entry, filter: tf}
			}(idx, tSpec)
		}
		tWg.Wait()
		close(tCh)

		orderedT := make([]targetLoadRes, len(cfg.Targets))
		for r := range tCh {
			if r.err != nil {
				return nil, r.err
			}
			orderedT[r.idx] = r
		}
		for _, r := range orderedT {
			fCopy := r.filter
			addTargetEntry(r.entry, &fCopy)
		}
	}

	// 2. Process positional/input paths in parallel
	if len(cfg.Paths) > 0 {
		type pathLoadRes struct {
			idx     int
			entries []FileEntry
			filters []*TargetFilter
			err     error
		}
		pCh := make(chan pathLoadRes, len(cfg.Paths))
		var pWg sync.WaitGroup

		for idx, p := range cfg.Paths {
			pWg.Add(1)
			go func(i int, rawP string) {
				defer pWg.Done()
				trimmed := strings.TrimSpace(rawP)
				if trimmed == "" {
					pCh <- pathLoadRes{idx: i}
					return
				}
				if trimmed == "-" {
					if strings.TrimSpace(cfg.StdinContent) == "" {
						pCh <- pathLoadRes{idx: i, err: errors.New("stdin ('-') was specified, but stdin content is empty")}
						return
					}
					var fPtr *TargetFilter
					if hasStandaloneFilter {
						fCopy := standaloneFilter
						fCopy.FilePath = "stdin"
						fPtr = &fCopy
					}
					pCh <- pathLoadRes{
						idx: i,
						entries: []FileEntry{{
							Path:             "stdin",
							DisplayPath:      "stdin",
							Content:          cfg.StdinContent,
							MimeType:         MimeTypeMarkdown,
							IsConvertibleDoc: true,
						}},
						filters: []*TargetFilter{fPtr},
					}
					return
				}

				// Allow inline target syntax in positional args (e.g., `gdocumentor main.go:10-20`)
				if _, statErr := os.Stat(resolveAbs(workDir, trimmed)); statErr != nil {
					if tf, parseErr := ParseTargetSpec(trimmed); parseErr == nil && tf.FilePath != trimmed {
						if entry, readErr := readTargetFileEntry(workDir, tf.FilePath); readErr == nil {
							tf.FilePath = entry.DisplayPath
							pCh <- pathLoadRes{
								idx:     i,
								entries: []FileEntry{entry},
								filters: []*TargetFilter{&tf},
							}
							return
						}
					}
				}

				files, cErr := collectFilesFromPath(workDir, trimmed)
				if cErr != nil {
					pCh <- pathLoadRes{idx: i, err: cErr}
					return
				}
				filters := make([]*TargetFilter, len(files))
				for fIdx, f := range files {
					if hasStandaloneFilter && f.MimeType != MimeTypePDF && f.MimeType != MimeTypeDOCX {
						fCopy := standaloneFilter
						fCopy.FilePath = f.DisplayPath
						filters[fIdx] = &fCopy
					}
				}
				pCh <- pathLoadRes{idx: i, entries: files, filters: filters}
			}(idx, p)
		}
		pWg.Wait()
		close(pCh)

		orderedP := make([]pathLoadRes, len(cfg.Paths))
		for r := range pCh {
			if r.err != nil {
				return nil, r.err
			}
			orderedP[r.idx] = r
		}
		for _, r := range orderedP {
			for fIdx, f := range r.entries {
				addTargetEntry(f, r.filters[fIdx])
			}
		}
	}

	// Support pure stdin when no paths or targets are provided but StdinContent is non-empty
	if len(targetOrder) == 0 && strings.TrimSpace(cfg.StdinContent) != "" {
		var fPtr *TargetFilter
		if hasStandaloneFilter {
			fCopy := standaloneFilter
			fCopy.FilePath = "stdin"
			fPtr = &fCopy
		}
		addTargetEntry(FileEntry{
			Path:             "stdin",
			DisplayPath:      "stdin",
			Content:          cfg.StdinContent,
			MimeType:         MimeTypeMarkdown,
			IsConvertibleDoc: true,
		}, fPtr)
	}

	var targetFiles []FileEntry
	targetPathSet := make(map[string]bool)
	for _, k := range targetOrder {
		entry := targetMap[k]
		targetFiles = append(targetFiles, *entry)
		targetPathSet[entry.Path] = true
	}

	// Run 3 major resolution stages in parallel:
	// Branch 1: Embed Base64 images in Markdown/Text target files & build document mapping / conversion payload
	// Branch 2: Load explicit --context files/dirs in parallel + resolve --auto-deps
	// Branch 3: Load local/remote RAG rules (--rules / --rules-dir) in parallel
	var (
		referenceFiles []FileEntry
		refErr         error
		ragRules       []RagRuleFile
		ragErr         error
		combinedText   string
		blocks         []utf16idx.EditableBlock
		hasPartial     bool
		selRanges      []utf16idx.RangeInterval
		useDriveConv   bool
		convMime       string
		convBytes      []byte
		allEmbeddedImg []EmbeddedImage
	)

	var mainWg sync.WaitGroup
	targetFilesSnapshot := make([]FileEntry, len(targetFiles))
	copy(targetFilesSnapshot, targetFiles)

	// Branch 1: Target file Markdown image embedding & conversion preparation
	mainWg.Go(func() {
		if len(targetFiles) == 0 {
			return
		}

		// Embed local/remote images as Base64 in parallel across all Markdown/Text target files
		var imgWg sync.WaitGroup
		for i := range targetFiles {
			if targetFiles[i].IsConvertibleDoc && targetFiles[i].MimeType == MimeTypeMarkdown && targetFiles[i].Content != "" {
				imgWg.Add(1)
				go func(idx int) {
					defer imgWg.Done()
					mdDir := workDir
					if targetFiles[idx].Path != "stdin" && !IsHTTPURL(targetFiles[idx].Path) {
						mdDir = filepath.Dir(targetFiles[idx].Path)
					}
					convMD, imgs := EmbedMarkdownImagesAsBase64(targetFiles[idx].Content, mdDir, workDir)
					targetFiles[idx].ConvertedMarkdown = convMD
					targetFiles[idx].EmbeddedImages = imgs
					targetFiles[idx].EmbeddedImageCount = len(imgs)
				}(i)
			}
		}
		imgWg.Wait()

		for _, tf := range targetFiles {
			if len(tf.EmbeddedImages) > 0 {
				allEmbeddedImg = append(allEmbeddedImg, tf.EmbeddedImages...)
			}
		}

		combinedText, blocks, hasPartial, selRanges = BuildDocumentTextAndMapping(targetFiles)

		// Determine if Google Drive conversion should be used:
		// - Single PDF or DOCX file -> convert binary directly to Google Docs
		// - All target files are convertible Markdown/Text files -> convert Markdown (with Base64 images) to Google Docs
		// - Script / source code files -> UseDriveConversion = false (insert text into newly created Google Doc)
		if len(targetFiles) == 1 && (targetFiles[0].MimeType == MimeTypePDF || targetFiles[0].MimeType == MimeTypeDOCX) && len(targetFiles[0].RawBytes) > 0 {
			useDriveConv = true
			convMime = targetFiles[0].MimeType
			convBytes = targetFiles[0].RawBytes
		} else {
			allConvertibleText := len(targetFiles) > 0
			for _, tf := range targetFiles {
				if !tf.IsConvertibleDoc || tf.MimeType == MimeTypePDF || tf.MimeType == MimeTypeDOCX {
					allConvertibleText = false
					break
				}
			}
			if allConvertibleText {
				useDriveConv = true
				convMime = MimeTypeMarkdown
				var convBuilder strings.Builder
				for fIdx, tf := range targetFiles {
					if fIdx > 0 {
						convBuilder.WriteString("\n\n")
					}
					if len(targetFiles) > 1 {
						convBuilder.WriteString(fmt.Sprintf("=== [File: %s] ===\n\n", tf.DisplayPath))
					}
					mdBody := tf.ConvertedMarkdown
					if mdBody == "" {
						mdBody = tf.Content
					}
					convBuilder.WriteString(mdBody)
				}
				convBytes = []byte(convBuilder.String())
			}
		}
	})

	// Branch 2: Context files + AutoDeps
	mainWg.Go(func() {
		refMap := make(map[string]FileEntry)
		var refOrder []string
		addRefFile := func(f FileEntry) {
			if targetPathSet[f.Path] {
				return
			}
			if _, exists := refMap[f.Path]; !exists {
				refMap[f.Path] = f
				refOrder = append(refOrder, f.Path)
			}
		}

		if len(cfg.ContextPaths) > 0 {
			type ctxLoadRes struct {
				idx   int
				files []FileEntry
				err   error
			}
			cCh := make(chan ctxLoadRes, len(cfg.ContextPaths))
			var cWg sync.WaitGroup
			for idx, cp := range cfg.ContextPaths {
				cWg.Add(1)
				go func(i int, rawCP string) {
					defer cWg.Done()
					files, cErr := collectFilesFromPath(workDir, rawCP)
					if cErr != nil {
						cCh <- ctxLoadRes{idx: i, err: fmt.Errorf("failed to load context path %q: %w", rawCP, cErr)}
						return
					}
					cCh <- ctxLoadRes{idx: i, files: files}
				}(idx, cp)
			}
			cWg.Wait()
			close(cCh)

			orderedC := make([]ctxLoadRes, len(cfg.ContextPaths))
			for r := range cCh {
				if r.err != nil {
					refErr = r.err
					return
				}
				orderedC[r.idx] = r
			}
			for _, r := range orderedC {
				for _, f := range r.files {
					addRefFile(f)
				}
			}
		}

		if cfg.AutoDeps && len(targetFiles) > 0 {
			autoRefs := ResolveRelatedDependencies(workDir, targetFilesSnapshot, targetPathSet)
			for _, rf := range autoRefs {
				addRefFile(rf)
			}
		}

		for _, k := range refOrder {
			referenceFiles = append(referenceFiles, refMap[k])
		}
	})

	// Branch 3: Local/Remote RAG rules
	mainWg.Go(func() {
		ragRules, ragErr = LoadLocalRagRules(workDir, cfg.RulePaths, cfg.RulesDir)
	})

	mainWg.Wait()

	if refErr != nil {
		return nil, refErr
	}
	if ragErr != nil {
		return nil, ragErr
	}

	extractedPersona := ExtractPersonaFromRagRules(ragRules)

	return &Bundle{
		TargetFiles:        targetFiles,
		ReferenceFiles:     referenceFiles,
		RagRules:           ragRules,
		CombinedDocText:    combinedText,
		EditableBlocks:     blocks,
		HasPartialScope:    hasPartial,
		DocSelectionRanges: selRanges,
		UseDriveConversion: useDriveConv,
		ConversionMimeType: convMime,
		ConversionBytes:    convBytes,
		EmbeddedImages:     allEmbeddedImg,
		ExtractedPersona:   extractedPersona,
	}, nil
}

// BuildDocumentTextAndMapping constructs the combined manuscript text to be inserted into a new Google Doc
// at index 1, along with the deterministic UTF-16 EditableBlocks mapping that links every line and character
// span back to its originating file, line number, rune offsets, and target scope status.
func BuildDocumentTextAndMapping(targetFiles []FileEntry) (string, []utf16idx.EditableBlock, bool, []utf16idx.RangeInterval) {
	if len(targetFiles) == 0 {
		return "", nil, false, nil
	}

	multiFile := len(targetFiles) > 1
	var sb strings.Builder
	var blocks []utf16idx.EditableBlock
	var selRanges []utf16idx.RangeInterval
	hasPartial := false

	// In Google Docs API v1, body text inserted at location {index: 1} begins at UTF-16 index 1.
	currentUTF16Index := 1

	for fIdx, tf := range targetFiles {
		if len(tf.Filters) > 0 {
			hasPartial = true
		}

		if fIdx > 0 {
			sb.WriteString("\n")
			currentUTF16Index += 1
		}

		if multiFile {
			headerLine := fmt.Sprintf("=== [File: %s] ===", tf.DisplayPath)
			sb.WriteString(headerLine)
			sb.WriteString("\n")
			currentUTF16Index += utf16idx.UTF16Len(headerLine) + 1
		}

		lines := strings.Split(tf.Content, "\n")
		fileRuneCursor := 0

		for lineZeroIdx, lineText := range lines {
			lineNum := lineZeroIdx + 1
			lineRuneLen := utf8.RuneCountInString(lineText)
			lineRuneStart := fileRuneCursor
			lineRuneEnd := fileRuneCursor + lineRuneLen

			lineUTF16Len := utf16idx.UTF16Len(lineText)
			lineDocStart := currentUTF16Index
			lineDocEnd := lineDocStart + lineUTF16Len

			sb.WriteString(lineText)
			if lineZeroIdx < len(lines)-1 {
				sb.WriteString("\n")
			}

			if strings.TrimSpace(lineText) != "" {
				if len(tf.Filters) == 0 {
					blocks = append(blocks, utf16idx.EditableBlock{
						Text:           lineText,
						StartIndex:     lineDocStart,
						EndIndex:       lineDocEnd,
						Source:         "body",
						FilePath:       tf.DisplayPath,
						LineNumber:     lineNum,
						FileRuneStart:  lineRuneStart,
						FileRuneEnd:    lineRuneEnd,
						TargetEditable: true,
					})
				} else {
					subBlocks := extractScopedSubBlocksForLine(
						lineText,
						lineNum,
						lineRuneStart,
						lineRuneEnd,
						lineDocStart,
						tf.DisplayPath,
						tf.Filters,
					)
					if len(subBlocks) > 0 {
						for _, sbBlk := range subBlocks {
							blocks = append(blocks, sbBlk)
							if sbBlk.TargetEditable {
								selRanges = append(selRanges, utf16idx.RangeInterval{
									StartIndex: sbBlk.StartIndex,
									EndIndex:   sbBlk.EndIndex,
								})
							}
						}
					} else {
						blocks = append(blocks, utf16idx.EditableBlock{
							Text:           lineText,
							StartIndex:     lineDocStart,
							EndIndex:       lineDocEnd,
							Source:         "body",
							FilePath:       tf.DisplayPath,
							LineNumber:     lineNum,
							FileRuneStart:  lineRuneStart,
							FileRuneEnd:    lineRuneEnd,
							TargetEditable: false,
						})
					}
				}
			}

			fileRuneCursor = lineRuneEnd + 1
			if lineZeroIdx < len(lines)-1 {
				currentUTF16Index = lineDocEnd + 1
			} else {
				currentUTF16Index = lineDocEnd
			}
		}
	}

	return sb.String(), blocks, hasPartial, selRanges
}

func extractScopedSubBlocksForLine(
	lineText string,
	lineNum int,
	lineRuneStart int,
	lineRuneEnd int,
	lineDocStart int,
	displayPath string,
	filters []TargetFilter,
) []utf16idx.EditableBlock {
	runes := []rune(lineText)
	lineRuneCount := len(runes)

	for _, f := range filters {
		// 1. Line-based filter
		if f.StartLine > 0 || f.EndLine > 0 {
			sLine := f.StartLine
			if sLine <= 0 {
				sLine = 1
			}
			eLine := f.EndLine
			if eLine <= 0 {
				eLine = 1<<30 - 1
			}
			if lineNum >= sLine && lineNum <= eLine {
				return []utf16idx.EditableBlock{
					{
						Text:           lineText,
						StartIndex:     lineDocStart,
						EndIndex:       lineDocStart + utf16idx.UTF16Len(lineText),
						Source:         "body",
						FilePath:       displayPath,
						LineNumber:     lineNum,
						FileRuneStart:  lineRuneStart,
						FileRuneEnd:    lineRuneEnd,
						TargetEditable: true,
					},
				}
			}
		}

		// 2. Character (rune) offset filter [StartChar, EndChar)
		if f.StartChar >= 0 && f.EndChar > f.StartChar {
			overlapRuneStart := maxInt(lineRuneStart, f.StartChar)
			overlapRuneEnd := minInt(lineRuneEnd, f.EndChar)
			if overlapRuneEnd > overlapRuneStart {
				relStart := overlapRuneStart - lineRuneStart
				relEnd := overlapRuneEnd - lineRuneStart
				if relStart >= 0 && relEnd <= lineRuneCount && relEnd > relStart {
					subStr := string(runes[relStart:relEnd])
					if strings.TrimSpace(subStr) != "" {
						prefixUTF16 := utf16idx.UTF16Len(string(runes[:relStart]))
						subUTF16 := utf16idx.UTF16Len(subStr)
						return []utf16idx.EditableBlock{
							{
								Text:           subStr,
								StartIndex:     lineDocStart + prefixUTF16,
								EndIndex:       lineDocStart + prefixUTF16 + subUTF16,
								Source:         "body",
								FilePath:       displayPath,
								LineNumber:     lineNum,
								FileRuneStart:  overlapRuneStart,
								FileRuneEnd:    overlapRuneEnd,
								TargetEditable: true,
							},
						}
					}
				}
			}
		}
	}
	return nil
}

// ReconcileConvertedBlocksWithLocalScope aligns Google Docs EditableBlocks from a Drive-converted document
// (.md, .txt, .pdf, .docx) with local file metadata and any partial line/character scope filters.
func ReconcileConvertedBlocksWithLocalScope(
	fetchedBlocks []utf16idx.EditableBlock,
	localMappedBlocks []utf16idx.EditableBlock,
	targetFiles []FileEntry,
	hasPartialScope bool,
) ([]utf16idx.EditableBlock, []utf16idx.RangeInterval) {
	defaultFilePath := ""
	if len(targetFiles) == 1 {
		defaultFilePath = targetFiles[0].DisplayPath
	}

	// If full document is in scope (no partial line/char filters), keep ALL converted Google Docs blocks
	// (headings, paragraphs, table cells) with their exact converted text and UTF-16 coordinates.
	if !hasPartialScope {
		var out []utf16idx.EditableBlock
		currentFile := defaultFilePath
		for idx, fb := range fetchedBlocks {
			if strings.HasPrefix(fb.Text, "=== [File: ") && strings.HasSuffix(fb.Text, "] ===") {
				currentFile = strings.TrimSuffix(strings.TrimPrefix(fb.Text, "=== [File: "), "] ===")
				continue
			}
			fb.TargetEditable = true
			if fb.FilePath == "" {
				fb.FilePath = currentFile
			}
			// Match approximate source line number if possible
			matchedLine := 0
			for _, lb := range localMappedBlocks {
				if lb.Text == fb.Text || stripMarkdownFormatting(lb.Text) == fb.Text {
					matchedLine = lb.LineNumber
					if fb.FilePath == "" {
						fb.FilePath = lb.FilePath
					}
					break
				}
			}
			if matchedLine > 0 {
				fb.LineNumber = matchedLine
			} else if fb.LineNumber == 0 {
				fb.LineNumber = idx + 1
			}
			out = append(out, fb)
		}
		return out, nil
	}

	// Partial scope on a converted Markdown/Text document:
	var reconciled []utf16idx.EditableBlock
	var selRanges []utf16idx.RangeInterval
	for _, lb := range localMappedBlocks {
		if !lb.TargetEditable {
			continue
		}
		matched := false
		// 1. Exact UTF-16 offset match (e.g., when plain text or mock doc preserves offsets)
		for _, fb := range fetchedBlocks {
			if lb.StartIndex >= fb.StartIndex && lb.EndIndex <= fb.EndIndex {
				relByteStart := utf16idx.UTF16OffsetToByteOffset(fb.Text, lb.StartIndex-fb.StartIndex)
				relByteEnd := utf16idx.UTF16OffsetToByteOffset(fb.Text, lb.EndIndex-fb.StartIndex)
				if relByteEnd >= relByteStart && relByteEnd <= len(fb.Text) && fb.Text[relByteStart:relByteEnd] == lb.Text {
					reconciled = append(reconciled, lb)
					selRanges = append(selRanges, utf16idx.RangeInterval{StartIndex: lb.StartIndex, EndIndex: lb.EndIndex})
					matched = true
					break
				}
			}
		}
		if matched {
			continue
		}

		// 2. Substring match on raw or Markdown-stripped text inside fetchedBlocks
		candidates := []string{lb.Text}
		if stripped := stripMarkdownFormatting(lb.Text); stripped != "" && stripped != lb.Text {
			candidates = append(candidates, stripped)
		}
		for _, cand := range candidates {
			for _, fb := range fetchedBlocks {
				if strings.HasPrefix(fb.Text, "=== [File: ") {
					continue
				}
				if sUTF16, eUTF16, _, ok := utf16idx.FindSubstringUTF16(fb.Text, cand, 0); ok {
					copyBlk := lb
					copyBlk.Text = cand
					copyBlk.StartIndex = fb.StartIndex + sUTF16
					copyBlk.EndIndex = fb.StartIndex + eUTF16
					copyBlk.Source = fb.Source
					reconciled = append(reconciled, copyBlk)
					selRanges = append(selRanges, utf16idx.RangeInterval{StartIndex: copyBlk.StartIndex, EndIndex: copyBlk.EndIndex})
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}

	return reconciled, selRanges
}

func stripMarkdownFormatting(s string) string {
	cleaned := reStripMdPrefix.ReplaceAllString(strings.TrimSpace(s), "")
	cleaned = reStripMdInline.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned)
}

// ReconcileFetchedBlocksWithLocalMapping aligns freshly parsed Google Docs EditableBlocks
// (from documents.get after script/code insertion) with the local file/line/scope mapping.
func ReconcileFetchedBlocksWithLocalMapping(fetchedBlocks, localMappedBlocks []utf16idx.EditableBlock) []utf16idx.EditableBlock {
	if len(localMappedBlocks) == 0 {
		for i := range fetchedBlocks {
			fetchedBlocks[i].TargetEditable = true
		}
		return fetchedBlocks
	}

	hasPartial := false
	for _, lb := range localMappedBlocks {
		if !lb.TargetEditable {
			hasPartial = true
			break
		}
	}

	var reconciled []utf16idx.EditableBlock
	for _, lb := range localMappedBlocks {
		if !lb.TargetEditable {
			continue
		}
		matchedInFetched := false
		for _, fb := range fetchedBlocks {
			if lb.StartIndex >= fb.StartIndex && lb.EndIndex <= fb.EndIndex {
				relByteStart := utf16idx.UTF16OffsetToByteOffset(fb.Text, lb.StartIndex-fb.StartIndex)
				relByteEnd := utf16idx.UTF16OffsetToByteOffset(fb.Text, lb.EndIndex-fb.StartIndex)
				if relByteEnd >= relByteStart && relByteEnd <= len(fb.Text) && fb.Text[relByteStart:relByteEnd] == lb.Text {
					reconciled = append(reconciled, lb)
					matchedInFetched = true
					break
				}
			}
		}
		if !matchedInFetched {
			for _, fb := range fetchedBlocks {
				if strings.HasPrefix(fb.Text, "=== [File: ") {
					continue
				}
				if sUTF16, eUTF16, _, ok := utf16idx.FindSubstringUTF16(fb.Text, lb.Text, 0); ok {
					copyBlk := lb
					copyBlk.StartIndex = fb.StartIndex + sUTF16
					copyBlk.EndIndex = fb.StartIndex + eUTF16
					reconciled = append(reconciled, copyBlk)
					break
				}
			}
		}
	}

	if len(reconciled) > 0 {
		return reconciled
	}

	if !hasPartial {
		for i := range fetchedBlocks {
			if !strings.HasPrefix(fetchedBlocks[i].Text, "=== [File: ") {
				fetchedBlocks[i].TargetEditable = true
			}
		}
	}
	return fetchedBlocks
}

func readTargetFileEntry(workDir, rawPath string) (FileEntry, error) {
	if IsHTTPURL(rawPath) {
		if workDir != "" {
			if blobSpec, ok := ParseGitHubBlobURL(rawPath); ok {
				candidates := []string{
					filepath.Join(workDir, filepath.FromSlash(blobSpec.FilePath)),
					filepath.Join(workDir, "src", filepath.Base(blobSpec.FilePath)),
				}
				if strings.EqualFold(filepath.Base(blobSpec.FilePath), "main.js") {
					candidates = append(candidates, filepath.Join(workDir, "src", "code.js"))
				}
				for _, candidateLocal := range candidates {
					if info, statErr := os.Stat(candidateLocal); statErr == nil && !info.IsDir() {
						return readLocalTargetFileEntry(workDir, candidateLocal, rawPath)
					}
				}
			}
		}
		disp, txt, rawBytes, mime, fErr := FetchRemoteURLContent(rawPath)
		if fErr != nil {
			return FileEntry{}, fErr
		}
		if mime == MimeTypePDF {
			return FileEntry{
				Path:             rawPath,
				DisplayPath:      disp,
				RawBytes:         rawBytes,
				MimeType:         MimeTypePDF,
				IsConvertibleDoc: true,
			}, nil
		}
		isConv, classifiedMime := ClassifyTargetFile(disp)
		if mime == "text/markdown" && !strings.Contains(disp, ".") {
			// Web article URL converted to text/markdown
			isConv = true
			classifiedMime = MimeTypeMarkdown
		}
		return FileEntry{
			Path:             rawPath,
			DisplayPath:      disp,
			Content:          txt,
			MimeType:         classifiedMime,
			IsConvertibleDoc: isConv,
		}, nil
	}

	absPath := resolveAbs(workDir, rawPath)
	return readLocalTargetFileEntry(workDir, absPath, rawPath)
}

func readLocalTargetFileEntry(workDir, absPath, rawPath string) (FileEntry, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return FileEntry{}, fmt.Errorf("unable to read target file %q: %w", rawPath, err)
	}
	displayPath := makeDisplayPath(workDir, absPath)
	isConv, mime := ClassifyTargetFile(absPath)

	if mime == MimeTypePDF {
		return FileEntry{
			Path:             absPath,
			DisplayPath:      displayPath,
			RawBytes:         data,
			MimeType:         MimeTypePDF,
			IsConvertibleDoc: true,
		}, nil
	}
	if mime == MimeTypeDOCX {
		docxTxt, _ := ExtractTextFromDOCXBytes(data)
		return FileEntry{
			Path:             absPath,
			DisplayPath:      displayPath,
			Content:          normalizeNewlines(docxTxt),
			RawBytes:         data,
			MimeType:         MimeTypeDOCX,
			IsConvertibleDoc: true,
		}, nil
	}

	if !utf8.Valid(data) || isBinaryData(data) {
		return FileEntry{}, fmt.Errorf("file %q is not a supported UTF-8 text, PDF, or DOCX file", rawPath)
	}
	return FileEntry{
		Path:             absPath,
		DisplayPath:      displayPath,
		Content:          normalizeNewlines(string(data)),
		MimeType:         mime,
		IsConvertibleDoc: isConv,
	}, nil
}

func collectFilesFromPath(workDir, rawPath string) ([]FileEntry, error) {
	if IsHTTPURL(rawPath) {
		if _, isBlob := ParseGitHubBlobURL(rawPath); !isBlob && reGitHubRepoURL.MatchString(strings.TrimSpace(rawPath)) {
			cloneDir, subPath, cleanup, err := CloneGitHubRepoShallow(rawPath)
			if err != nil {
				return nil, err
			}
			defer cleanup()
			targetSub := "."
			if subPath != "" {
				targetSub = subPath
			}
			return collectFilesFromPath(cloneDir, targetSub)
		}

		entry, err := readTargetFileEntry(workDir, rawPath)
		if err != nil {
			return nil, err
		}
		return []FileEntry{entry}, nil
	}

	absRoot := resolveAbs(workDir, rawPath)
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		entry, err := readLocalTargetFileEntry(workDir, absRoot, rawPath)
		if err != nil {
			return nil, err
		}
		return []FileEntry{entry}, nil
	}

	var candidateFiles []string
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && path != absRoot {
				return filepath.SkipDir
			}
			if name == "node_modules" || name == "vendor" || name == "dist" || name == "build" || name == "temp" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if !IsSupportedSourceOrTextFile(name) {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.Size() == 0 || fi.Size() > 2*1024*1024 {
			return nil
		}
		candidateFiles = append(candidateFiles, path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(candidateFiles) == 0 {
		return nil, nil
	}

	type indexedFile struct {
		idx   int
		entry *FileEntry
	}
	resCh := make(chan indexedFile, len(candidateFiles))
	var wg sync.WaitGroup

	for idx, p := range candidateFiles {
		wg.Add(1)
		go func(i int, filePath string) {
			defer wg.Done()
			data, err := os.ReadFile(filePath)
			if err != nil || !utf8.Valid(data) || isBinaryData(data) {
				return
			}
			isConv, mime := ClassifyTargetFile(filePath)
			resCh <- indexedFile{
				idx: i,
				entry: &FileEntry{
					Path:             filePath,
					DisplayPath:      makeDisplayPath(workDir, filePath),
					Content:          normalizeNewlines(string(data)),
					MimeType:         mime,
					IsConvertibleDoc: isConv,
				},
			}
		}(idx, p)
	}
	wg.Wait()
	close(resCh)

	var entries []FileEntry
	for r := range resCh {
		if r.entry != nil {
			entries = append(entries, *r.entry)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].DisplayPath < entries[j].DisplayPath
	})
	return entries, nil
}

// IsSupportedSourceOrTextFile checks if a filename has a supported text, markdown, config, or source code extension.
func IsSupportedSourceOrTextFile(name string) bool {
	lower := strings.ToLower(filepath.Base(name))
	if lower == "makefile" || lower == "dockerfile" || lower == "go.mod" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(lower))
	switch ext {
	case ".md", ".markdown", ".txt", ".rst", ".tex", ".adoc",
		".go", ".js", ".ts", ".jsx", ".tsx", ".gs", ".mjs", ".cjs",
		".py", ".java", ".c", ".cpp", ".cc", ".h", ".hpp", ".rs", ".rb", ".php", ".cs", ".swift", ".kt", ".scala",
		".sql", ".sh", ".bash", ".zsh", ".ps1",
		".json", ".yaml", ".yml", ".toml", ".xml", ".html", ".css", ".scss", ".csv", ".tsv", ".proto", ".graphql":
		return true
	default:
		return false
	}
}

func resolveAbs(workDir, p string) string {
	workDir = NormalizeCrossPlatformPath(workDir)
	p = NormalizeCrossPlatformPath(p)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if workDir != "" {
		joined := filepath.Clean(filepath.Join(workDir, p))
		if _, err := os.Stat(joined); err == nil {
			if abs, aErr := filepath.Abs(joined); aErr == nil {
				return abs
			}
			return joined
		}
		if _, err := os.Stat(p); err == nil {
			if abs, aErr := filepath.Abs(p); aErr == nil {
				return abs
			}
		}
		if abs, aErr := filepath.Abs(joined); aErr == nil {
			return abs
		}
		return joined
	}
	abs, err := filepath.Abs(p)
	if err == nil {
		return abs
	}
	return filepath.Clean(p)
}

func makeDisplayPath(workDir, absPath string) string {
	if workDir != "" {
		if rel, err := filepath.Rel(workDir, absPath); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(absPath)
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimRight(s, "\n")
}

func isBinaryData(data []byte) bool {
	checkLen := min(len(data), 8000)
	for i := 0; i < checkLen; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
