package workspace

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	reGitHubBlobURL = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/blob/([^/]+)/(.+)$`)
	reGitHubRawURL  = regexp.MustCompile(`^https?://raw\.githubusercontent\.com/([^/]+)/([^/]+)/([^/]+)/(.+)$`)
	reGitHubRepoURL = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/#?]+?)(?:\.git)?(?:/tree/([^/#?]+)(?:/(.*))?)?/?$`)

	reStripBlockTags = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<noscript[^>]*>.*?</noscript>|<svg[^>]*>.*?</svg>|<nav[^>]*>.*?</nav>|<footer[^>]*>.*?</footer>|<iframe[^>]*>.*?</iframe>`)
	reHeadingTag     = regexp.MustCompile(`(?is)<h([1-6])[^>]*>(.*?)</h[1-6]>`)
	reListItemTag    = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)
	reBlockBreakTag  = regexp.MustCompile(`(?i)</?(p|div|section|article|main|header|blockquote|pre|tr|table|ul|ol|br|hr)[^>]*>`)
	reAllHTMLTags    = regexp.MustCompile(`(?s)<[^>]+>`)
	reMultiSpaces    = regexp.MustCompile(`[ \t]+`)
	reMultiNewlines  = regexp.MustCompile(`\n{3,}`)
)

// RemoteHTTPClient is the HTTP client used to fetch remote URLs (can be overridden in tests).
var RemoteHTTPClient = &http.Client{Timeout: 45 * time.Second}

// IsHTTPURL returns true if raw starts with http:// or https://.
func IsHTTPURL(raw string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")
}

// GitHubBlobSpec holds parsed metadata for a GitHub blob URL.
type GitHubBlobSpec struct {
	Owner    string
	Repo     string
	Ref      string
	FilePath string
	RepoURL  string
	RawURL   string
}

// ParseGitHubBlobURL checks if rawURL is a GitHub blob URL (https://github.com/owner/repo/blob/ref/path)
// and returns its components.
func ParseGitHubBlobURL(rawURL string) (*GitHubBlobSpec, bool) {
	trimmed := strings.TrimSpace(rawURL)
	if m := reGitHubBlobURL.FindStringSubmatch(trimmed); len(m) == 5 {
		owner, repo, ref, filePath := m[1], m[2], m[3], m[4]
		return &GitHubBlobSpec{
			Owner:    owner,
			Repo:     repo,
			Ref:      ref,
			FilePath: filePath,
			RepoURL:  fmt.Sprintf("https://github.com/%s/%s.git", owner, repo),
			RawURL:   fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, ref, filePath),
		}, true
	}
	return nil, false
}

// CloneGitHubRepoShallow clones a GitHub repository URL into a temporary directory with --depth 1
// and returns the temp directory path, an optional sub-directory path if the URL was a /tree/branch/subdir URL,
// and a cleanup function.
func CloneGitHubRepoShallow(rawRepoURL string) (cloneDir string, subPath string, cleanup func(), err error) {
	trimmed := strings.TrimSpace(rawRepoURL)
	cloneURL := trimmed
	branch := ""

	if m := reGitHubRepoURL.FindStringSubmatch(trimmed); len(m) >= 3 {
		owner, repo := m[1], m[2]
		cloneURL = fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
		if len(m) >= 4 && m[3] != "" {
			branch = m[3]
		}
		if len(m) >= 5 && m[4] != "" {
			subPath = strings.Trim(m[4], "/")
		}
	}

	tmpDir, err := os.MkdirTemp("", "gdocumentor-repo-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create temp directory for git clone: %w", err)
	}
	cleanupFn := func() {
		_ = os.RemoveAll(tmpDir)
	}

	args := []string{"clone", "--depth", "1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, cloneURL, tmpDir)

	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, runErr := cmd.CombinedOutput()
	if runErr != nil && branch != "" {
		// Fallback: if the specified branch (e.g. "master" vs "main") does not exist on origin,
		// retry cloning the repository's default branch.
		_ = os.RemoveAll(tmpDir)
		_ = os.MkdirAll(tmpDir, 0o755)
		fbCmd := exec.Command("git", "clone", "--depth", "1", cloneURL, tmpDir)
		fbCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if fbOut, fbErr := fbCmd.CombinedOutput(); fbErr == nil {
			out, runErr = fbOut, nil
		}
	}
	if runErr != nil {
		cleanupFn()
		return "", "", nil, fmt.Errorf("git clone failed for %q: %v (%s)", rawRepoURL, runErr, strings.TrimSpace(string(out)))
	}

	return tmpDir, subPath, cleanupFn, nil
}

// FetchRemoteURLContent fetches a remote HTTP/HTTPS URL (supporting GitHub blob URLs, raw code/text URLs,
// and HTML web articles) and returns its normalized display name, extracted UTF-8 text content, raw bytes, and MIME type.
func FetchRemoteURLContent(rawURL string) (displayName string, textContent string, rawBytes []byte, mimeType string, err error) {
	targetURL := strings.TrimSpace(rawURL)
	displayName = deriveURLDisplayName(targetURL)

	if blobSpec, ok := ParseGitHubBlobURL(targetURL); ok {
		targetURL = blobSpec.RawURL
		displayName = fmt.Sprintf("%s/%s/%s", blobSpec.Owner, blobSpec.Repo, blobSpec.FilePath)
	} else if m := reGitHubRawURL.FindStringSubmatch(targetURL); len(m) == 5 {
		displayName = fmt.Sprintf("%s/%s/%s", m[1], m[2], m[4])
	}

	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return "", "", nil, "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", "gdocumentor/1.1.0 (+https://github.com/tanaikech/gdocumentor)")
	req.Header.Set("Accept", "text/markdown, text/plain, text/html, application/xhtml+xml, application/pdf, */*;q=0.8")

	resp, err := RemoteHTTPClient.Do(req)
	if err != nil {
		return "", "", nil, "", fmt.Errorf("failed to fetch URL %q: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", nil, "", fmt.Errorf("HTTP %d when fetching URL %q", resp.StatusCode, rawURL)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", "", nil, "", fmt.Errorf("failed to read response body from %q: %w", rawURL, err)
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if semi := strings.IndexByte(contentType, ';'); semi >= 0 {
		contentType = strings.TrimSpace(contentType[:semi])
	}

	if contentType == "application/pdf" || strings.HasSuffix(strings.ToLower(targetURL), ".pdf") {
		return displayName, "", data, "application/pdf", nil
	}

	if !utf8.Valid(data) {
		return "", "", nil, "", fmt.Errorf("content at URL %q is not valid UTF-8 text", rawURL)
	}

	rawStr := string(data)
	isHTML := contentType == "text/html" ||
		contentType == "application/xhtml+xml" ||
		looksLikeHTMLDocument(rawStr)

	if isHTML {
		extracted := ExtractReadableTextFromHTML(rawStr)
		if strings.TrimSpace(extracted) == "" {
			return "", "", nil, "", fmt.Errorf("no readable article text could be extracted from HTML URL %q", rawURL)
		}
		return displayName, normalizeNewlines(extracted), data, "text/html", nil
	}

	return displayName, normalizeNewlines(rawStr), data, firstNonEmptyMime(contentType, "text/plain"), nil
}

// ExtractReadableTextFromHTML extracts clean, human-readable prose and headings from an HTML page,
// stripping scripts, styles, navigation bars, footers, and HTML tags while preserving paragraph structure.
func ExtractReadableTextFromHTML(rawHTML string) string {
	cleaned := reStripBlockTags.ReplaceAllString(rawHTML, "\n")

	// Convert <h1>..<h6> to Markdown-like headings so section structure is preserved in Google Docs
	cleaned = reHeadingTag.ReplaceAllStringFunc(cleaned, func(match string) string {
		sub := reHeadingTag.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		level := len(sub[1])
		hashes := strings.Repeat("#", level)
		inner := strings.TrimSpace(reAllHTMLTags.ReplaceAllString(sub[2], ""))
		inner = html.UnescapeString(inner)
		if inner == "" {
			return ""
		}
		return fmt.Sprintf("\n\n%s %s\n\n", hashes, inner)
	})

	// Convert <li>...</li> to bullet lines
	cleaned = reListItemTag.ReplaceAllStringFunc(cleaned, func(match string) string {
		sub := reListItemTag.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		inner := strings.TrimSpace(reAllHTMLTags.ReplaceAllString(sub[1], " "))
		inner = html.UnescapeString(inner)
		if inner == "" {
			return ""
		}
		return fmt.Sprintf("\n- %s\n", inner)
	})

	cleaned = reBlockBreakTag.ReplaceAllString(cleaned, "\n")
	cleaned = reAllHTMLTags.ReplaceAllString(cleaned, " ")
	cleaned = html.UnescapeString(cleaned)
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")

	rawLines := strings.Split(cleaned, "\n")
	var outLines []string
	for _, line := range rawLines {
		collapsed := strings.TrimSpace(reMultiSpaces.ReplaceAllString(line, " "))
		if collapsed != "" {
			outLines = append(outLines, collapsed)
		} else if len(outLines) > 0 && outLines[len(outLines)-1] != "" {
			outLines = append(outLines, "")
		}
	}

	joined := strings.TrimSpace(strings.Join(outLines, "\n"))
	return reMultiNewlines.ReplaceAllString(joined, "\n\n")
}

func looksLikeHTMLDocument(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s))
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") || strings.Contains(head, "<body")
}

func deriveURLDisplayName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	cleanPath := strings.Trim(u.Path, "/")
	if cleanPath == "" {
		return u.Host
	}
	base := path.Base(cleanPath)
	if base != "" && base != "." && base != "/" {
		return fmt.Sprintf("%s/%s", u.Host, base)
	}
	return fmt.Sprintf("%s/%s", u.Host, cleanPath)
}

func firstNonEmptyMime(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}
