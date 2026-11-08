package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	rePersonaHeading = regexp.MustCompile(`(?i)^#{1,4}\s*(?:AIの?(?:位置づけ|立場|役割|ペルソナ)|推敲者の?(?:位置づけ|立場|役割)|ペルソナ|ロール|AI\s*(?:Persona|Role|Positioning|Identity)|(?:Reviewer|Coach)\s*(?:Persona|Role)|Persona|Role)\s*$`)
	reAnyHeading     = regexp.MustCompile(`^#{1,6}\s+\S`)
	rePersonaKV      = regexp.MustCompile(`(?i)^[-*]?\s*\**(?:AIの?(?:位置づけ|立場|役割|ペルソナ)|推敲者の?(?:位置づけ|立場|役割)|ペルソナ|ロール|AI\s*(?:Persona|Role|Positioning|Identity)|(?:Reviewer|Coach)\s*(?:Persona|Role)|Persona|Role)\**\s*[:：]\s*(.+)$`)
	rePersonaDirect  = regexp.MustCompile(`(?i)^(?:あなたは[^\n]+|あなたの(?:立場|役割|位置づけ)は[^\n]+|You\s+are\s+(?:a|an|the)\s+[^\n]+|Act\s+as\s+(?:a|an|the)\s+[^\n]+)$`)
)

// LoadLocalRagRules loads organization/project rulebook files from explicit file paths (--rules)
// and/or a rulebook directory (--rules-dir) in parallel.
// Text rulebooks (.md, .txt, .json, .yaml, .docx, etc.) are loaded as inline UTF-8 strings for zero-latency prompting,
// while binary rulebooks (such as .pdf files) are loaded as binary blobs for Gemini File API upload.
func LoadLocalRagRules(workDir string, rulePaths []string, rulesDir string) ([]RagRuleFile, error) {
	var candidates []string
	seen := make(map[string]bool)

	addCandidate := func(absPath string) {
		clean := absPath
		if !IsHTTPURL(absPath) {
			clean = filepath.Clean(absPath)
		}
		if !seen[clean] {
			seen[clean] = true
			candidates = append(candidates, clean)
		}
	}

	for _, rp := range rulePaths {
		trimmed := strings.TrimSpace(rp)
		if trimmed == "" {
			continue
		}
		if IsHTTPURL(trimmed) {
			addCandidate(trimmed)
			continue
		}
		abs := resolveAbs(workDir, trimmed)
		info, err := os.Stat(abs)
		if err != nil && !filepath.IsAbs(trimmed) {
			sampleFallback := resolveAbs(workDir, filepath.Join("sample", filepath.Clean(trimmed)))
			if fbInfo, fbErr := os.Stat(sampleFallback); fbErr == nil {
				abs = sampleFallback
				info = fbInfo
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("unable to access RAG rule path %q: %w", rp, err)
		}
		if info.IsDir() {
			if err := walkRagDir(abs, addCandidate); err != nil {
				return nil, err
			}
		} else {
			addCandidate(abs)
		}
	}

	if strings.TrimSpace(rulesDir) != "" {
		absDir := resolveAbs(workDir, strings.TrimSpace(rulesDir))
		info, err := os.Stat(absDir)
		if err != nil {
			return nil, fmt.Errorf("unable to access RAG rules directory %q: %w", rulesDir, err)
		}
		if !info.IsDir() {
			addCandidate(absDir)
		} else {
			if err := walkRagDir(absDir, addCandidate); err != nil {
				return nil, err
			}
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	type indexedRuleResult struct {
		idx  int
		rule *RagRuleFile
		err  error
	}

	resCh := make(chan indexedRuleResult, len(candidates))
	var wg sync.WaitGroup

	for idx, absPath := range candidates {
		wg.Add(1)
		go func(i int, targetPath string) {
			defer wg.Done()
			if IsHTTPURL(targetPath) {
				disp, txt, rawBytes, mime, fErr := FetchRemoteURLContent(targetPath)
				if fErr != nil {
					resCh <- indexedRuleResult{idx: i, err: fmt.Errorf("unable to fetch remote RAG rule URL %q: %w", targetPath, fErr)}
					return
				}
				if mime == "application/pdf" {
					resCh <- indexedRuleResult{
						idx: i,
						rule: &RagRuleFile{
							Title:      disp,
							SourceType: "REMOTE_BINARY",
							MimeType:   "application/pdf",
							Bytes:      rawBytes,
							IsBinary:   true,
							CharCount:  len(rawBytes),
						},
					}
				} else if strings.TrimSpace(txt) != "" {
					resCh <- indexedRuleResult{
						idx: i,
						rule: &RagRuleFile{
							Title:      disp,
							SourceType: "REMOTE_TEXT",
							MimeType:   "text/markdown",
							Content:    strings.TrimSpace(txt),
							IsBinary:   false,
							CharCount:  utf8.RuneCountInString(txt),
						},
					}
				}
				return
			}

			data, err := os.ReadFile(targetPath)
			if err != nil || len(data) == 0 {
				return
			}
			baseName := filepath.Base(targetPath)
			ext := strings.ToLower(filepath.Ext(baseName))

			if ext == ".pdf" {
				resCh <- indexedRuleResult{
					idx: i,
					rule: &RagRuleFile{
						Title:      baseName,
						SourceType: "LOCAL_BINARY",
						MimeType:   "application/pdf",
						Bytes:      data,
						IsBinary:   true,
						CharCount:  len(data),
					},
				}
				return
			}

			if ext == ".docx" {
				if docxTxt, dErr := ExtractTextFromDOCXBytes(data); dErr == nil && strings.TrimSpace(docxTxt) != "" {
					resCh <- indexedRuleResult{
						idx: i,
						rule: &RagRuleFile{
							Title:      baseName,
							SourceType: "LOCAL_TEXT",
							MimeType:   "text/plain",
							Content:    strings.TrimSpace(docxTxt),
							IsBinary:   false,
							CharCount:  utf8.RuneCountInString(docxTxt),
						},
					}
				}
				return
			}

			if utf8.Valid(data) && !isBinaryData(data) {
				txt := strings.TrimSpace(normalizeNewlines(string(data)))
				if txt != "" {
					mime := "text/plain"
					if ext == ".md" || ext == ".markdown" {
						mime = "text/markdown"
					}
					resCh <- indexedRuleResult{
						idx: i,
						rule: &RagRuleFile{
							Title:      baseName,
							SourceType: "LOCAL_TEXT",
							MimeType:   mime,
							Content:    txt,
							IsBinary:   false,
							CharCount:  utf8.RuneCountInString(txt),
						},
					}
				}
			}
		}(idx, absPath)
	}

	wg.Wait()
	close(resCh)

	var collected []indexedRuleResult
	for r := range resCh {
		if r.err != nil {
			return nil, r.err
		}
		if r.rule != nil {
			collected = append(collected, r)
		}
	}

	sort.Slice(collected, func(i, j int) bool {
		return collected[i].idx < collected[j].idx
	})

	var rules []RagRuleFile
	for idx, item := range collected {
		item.rule.ID = fmt.Sprintf("RAG_RULE_%d", idx+1)
		rules = append(rules, *item.rule)
	}

	return rules, nil
}

// ExtractPersonaFromRagRules inspects loaded RAG rulebooks and extracts any custom AI persona, role,
// or pedagogical positioning (e.g. "あなたは大学で日本語を教えている日本人です。" or "# AI Persona").
func ExtractPersonaFromRagRules(rules []RagRuleFile) string {
	var extractedParts []string
	seen := make(map[string]bool)

	addPart := func(s string) {
		clean := strings.TrimSpace(s)
		if clean != "" && !seen[clean] {
			seen[clean] = true
			extractedParts = append(extractedParts, clean)
		}
	}

	for _, r := range rules {
		if r.IsBinary || strings.TrimSpace(r.Content) == "" {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(r.Content, "\r\n", "\n"), "\n")

		// 1. Check for dedicated Persona/Role heading section
		inPersonaSection := false
		var sectionLines []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if rePersonaHeading.MatchString(trimmed) {
				inPersonaSection = true
				continue
			}
			if inPersonaSection {
				if reAnyHeading.MatchString(trimmed) {
					break
				}
				if trimmed != "" {
					sectionLines = append(sectionLines, trimmed)
				}
			}
		}
		if len(sectionLines) > 0 {
			addPart(strings.Join(sectionLines, "\n"))
		}

		// 2. Check for Key-Value Persona lines or direct second-person persona statements
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if m := rePersonaKV.FindStringSubmatch(trimmed); len(m) > 1 {
				addPart(m[1])
				continue
			}
			if rePersonaDirect.MatchString(trimmed) {
				addPart(trimmed)
			}
		}
	}

	return strings.Join(extractedParts, "\n")
}

func walkRagDir(absDir string, addFn func(string)) error {
	var found []string
	err := filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != absDir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if IsSupportedSourceOrTextFile(d.Name()) || ext == ".pdf" || ext == ".docx" {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(found)
	for _, f := range found {
		addFn(f)
	}
	return nil
}
