package workspace

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// EmbeddedImage holds metadata and binary bytes for a local or remote image referenced inside a Markdown document.
type EmbeddedImage struct {
	OriginalRef string `json:"originalRef"`
	AltText     string `json:"altText"`
	Title       string `json:"title,omitempty"`
	MimeType    string `json:"mimeType"`
	Data        []byte `json:"-"`
}

var (
	// Matches ![alt](src) or ![alt](src "title") or ![alt](<src>)
	reMarkdownImage = regexp.MustCompile(`!\[([^\]]*)\]\(\s*(?:<([^>]+)>|([^\s\)]+))(?:\s+("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'))?\s*\)`)
	// Matches <img ... src="src" ...> or <img ... src='src' ...>
	reHTMLImgTag = regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc\s*=\s*)(?:"([^"]+)"|'([^']+)')([^>]*>)`)
)

// EmbedMarkdownImagesAsBase64 scans a Markdown string for local or remote image references
// (`![alt](path)` and `<img src="path">`), loads/fetches all unique images in parallel,
// and embeds them as inline `data:<mime>;base64,...` URIs so Google Drive API v3 natively
// converts them into inline Google Docs images (`inlineObjects` / `VISUAL_N`).
func EmbedMarkdownImagesAsBase64(mdContent string, mdFileDir string, workDir string) (string, []EmbeddedImage) {
	if strings.TrimSpace(mdContent) == "" {
		return mdContent, nil
	}

	// Split by lines so we do not rewrite image syntax inside fenced code blocks (``` or ~~~)
	lines := strings.Split(mdContent, "\n")
	inFence := false
	fenceMarker := ""

	type refKey struct {
		rawRef string
		alt    string
		title  string
	}
	var discoveredOrder []refKey
	uniqueRefs := make(map[string]bool)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if !inFence {
				inFence = true
				fenceMarker = marker
			} else if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
				fenceMarker = ""
			}
			continue
		}
		if inFence {
			continue
		}

		for _, m := range reMarkdownImage.FindAllStringSubmatch(line, -1) {
			alt := m[1]
			src := m[2]
			if src == "" {
				src = m[3]
			}
			title := strings.Trim(m[4], `"'`)
			src = strings.TrimSpace(src)
			if src == "" || strings.HasPrefix(strings.ToLower(src), "data:image/") {
				continue
			}
			discoveredOrder = append(discoveredOrder, refKey{rawRef: src, alt: alt, title: title})
			uniqueRefs[src] = true
		}

		for _, m := range reHTMLImgTag.FindAllStringSubmatch(line, -1) {
			src := m[2]
			if src == "" {
				src = m[3]
			}
			src = strings.TrimSpace(src)
			if src == "" || strings.HasPrefix(strings.ToLower(src), "data:image/") {
				continue
			}
			discoveredOrder = append(discoveredOrder, refKey{rawRef: src})
			uniqueRefs[src] = true
		}
	}

	if len(uniqueRefs) == 0 {
		return mdContent, nil
	}

	type resolvedImg struct {
		mimeType string
		data     []byte
		dataURI  string
	}
	resolvedMap := make(map[string]resolvedImg)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for rawSrc := range uniqueRefs {
		wg.Add(1)
		go func(ref string) {
			defer wg.Done()
			data, mime := loadReferencedImageBytes(ref, mdFileDir, workDir)
			if len(data) == 0 {
				return
			}
			b64 := base64.StdEncoding.EncodeToString(data)
			uri := "data:" + mime + ";base64," + b64
			mu.Lock()
			resolvedMap[ref] = resolvedImg{
				mimeType: mime,
				data:     data,
				dataURI:  uri,
			}
			mu.Unlock()
		}(rawSrc)
	}
	wg.Wait()

	if len(resolvedMap) == 0 {
		return mdContent, nil
	}

	var embeddedList []EmbeddedImage
	for _, item := range discoveredOrder {
		if res, ok := resolvedMap[item.rawRef]; ok {
			embeddedList = append(embeddedList, EmbeddedImage{
				OriginalRef: item.rawRef,
				AltText:     item.alt,
				Title:       item.title,
				MimeType:    res.mimeType,
				Data:        res.data,
			})
		}
	}

	// Perform replacement outside fenced code blocks
	inFence = false
	fenceMarker = ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if !inFence {
				inFence = true
				fenceMarker = marker
			} else if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
				fenceMarker = ""
			}
			continue
		}
		if inFence {
			continue
		}

		if strings.Contains(line, "![") {
			line = reMarkdownImage.ReplaceAllStringFunc(line, func(full string) string {
				m := reMarkdownImage.FindStringSubmatch(full)
				if len(m) == 0 {
					return full
				}
				alt := m[1]
				src := m[2]
				if src == "" {
					src = m[3]
				}
				titlePart := m[4]
				res, ok := resolvedMap[strings.TrimSpace(src)]
				if !ok {
					return full
				}
				if titlePart != "" {
					return "![" + alt + "](" + res.dataURI + " " + titlePart + ")"
				}
				return "![" + alt + "](" + res.dataURI + ")"
			})
		}

		if strings.Contains(strings.ToLower(line), "<img") {
			line = reHTMLImgTag.ReplaceAllStringFunc(line, func(full string) string {
				m := reHTMLImgTag.FindStringSubmatch(full)
				if len(m) == 0 {
					return full
				}
				prefix := m[1]
				src := m[2]
				if src == "" {
					src = m[3]
				}
				suffix := m[4]
				res, ok := resolvedMap[strings.TrimSpace(src)]
				if !ok {
					return full
				}
				return prefix + `"` + res.dataURI + `"` + suffix
			})
		}

		lines[i] = line
	}

	return strings.Join(lines, "\n"), embeddedList
}

func loadReferencedImageBytes(ref, mdFileDir, workDir string) ([]byte, string) {
	if IsHTTPURL(ref) {
		client := &http.Client{Timeout: 30 * time.Second}
		req, err := http.NewRequest(http.MethodGet, ref, nil)
		if err != nil {
			return nil, ""
		}
		req.Header.Set("User-Agent", "gdocumentor/1.0")
		resp, err := client.Do(req)
		if err != nil {
			return nil, ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, ""
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
		if err != nil || len(data) == 0 {
			return nil, ""
		}
		return data, detectImageMimeType(ref, data, resp.Header.Get("Content-Type"))
	}

	var candidates []string
	if filepath.IsAbs(ref) {
		candidates = append(candidates, filepath.Clean(ref))
	} else {
		if mdFileDir != "" {
			candidates = append(candidates, filepath.Clean(filepath.Join(mdFileDir, filepath.FromSlash(ref))))
		}
		if workDir != "" && workDir != mdFileDir {
			candidates = append(candidates, filepath.Clean(filepath.Join(workDir, filepath.FromSlash(ref))))
		}
		candidates = append(candidates, filepath.Clean(filepath.FromSlash(ref)))
	}

	for _, c := range candidates {
		info, err := os.Stat(c)
		if err != nil || info.IsDir() || info.Size() == 0 || info.Size() > 20*1024*1024 {
			continue
		}
		data, err := os.ReadFile(c)
		if err != nil || len(data) == 0 {
			continue
		}
		return data, detectImageMimeType(c, data, "")
	}
	return nil, ""
}

func detectImageMimeType(pathOrURL string, data []byte, headerMime string) string {
	ext := strings.ToLower(filepath.Ext(pathOrURL))
	if qIdx := strings.IndexByte(ext, '?'); qIdx >= 0 {
		ext = ext[:qIdx]
	}
	if ext == ".svg" {
		return "image/svg+xml"
	}

	if len(data) > 0 {
		detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
		if strings.HasPrefix(detected, "image/") {
			return detected
		}
	}

	cleanHeader := strings.ToLower(strings.TrimSpace(strings.Split(headerMime, ";")[0]))
	if strings.HasPrefix(cleanHeader, "image/") {
		return cleanHeader
	}

	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return "image/png"
	}
}
