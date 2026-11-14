package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	DefaultModel     = "gemini-3.8-flash"
	DeepModel        = "gemini-4-argon"
	HydrogenModel    = "gemini-4-hydrogen"
	CarbonModel      = "gemini-4-carbon"
	EnvGeminiAPIKey  = "GEMINI_API_KEY"
	EnvModel         = "DOCUMENTOR_MODEL"
	EnvQuotaProject  = "GOOGLE_CLOUD_QUOTA_PROJECT"
	EnvAccessToken   = "GOOGLE_OAUTH_ACCESS_TOKEN"
	EnvRagFolderID   = "RAG_FOLDER_ID"
	ScopeDocuments   = "https://www.googleapis.com/auth/documents"
	ScopeDrive       = "https://www.googleapis.com/auth/drive"
	ScopeCloud       = "https://www.googleapis.com/auth/cloud-platform"
)

// Options configures how Google Workspace OAuth credentials, Quota Project, and Gemini API keys are resolved.
type Options struct {
	AccessToken  string
	QuotaProject string
	GeminiAPIKey string
	Model        string
	Lang         string // "en" or "ja" for localized error/help messages
	ADCFilePath  string // Optional override for testing ADC JSON parsing
}

// Manager handles OAuth2 token acquisition, caching, Quota Project resolution, and HTTP request execution.
type Manager struct {
	mu             sync.Mutex
	opts           Options
	tokenSource    oauth2.TokenSource
	cachedToken    *oauth2.Token
	lastTokenSrc   string
	quotaProject   string
	quotaResolved  bool
	httpClient     *http.Client
	tokenInfoURL   string

	// Hooks for unit testing
	findDefaultCredentials func(ctx context.Context, scopes ...string) (*google.Credentials, error)
	execCommand            func(ctx context.Context, name string, args ...string) ([]byte, error)
}

type adcJSON struct {
	Type           string `json:"type"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	RefreshToken   string `json:"refresh_token"`
	QuotaProjectID string `json:"quota_project_id"`
}

// NewManager creates a new authentication and configuration manager.
func NewManager(opts Options) *Manager {
	return &Manager{
		opts:                   opts,
		httpClient:             &http.Client{Timeout: 120 * time.Second},
		tokenInfoURL:           "https://oauth2.googleapis.com/tokeninfo",
		findDefaultCredentials: google.FindDefaultCredentials,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			resolvedName := resolveExecutablePath(name)
			cmd := exec.CommandContext(ctx, resolvedName, args...)
			return cmd.Output()
		},
	}
}

// resolveExecutablePath locates an executable (such as `gcloud`) even when launched from GUI applications
// (Claude Desktop, Cursor, VS Code, Windsurf) that do not inherit the user's interactive shell PATH.
func resolveExecutablePath(name string) string {
	if p, err := exec.LookPath(name); err == nil && p != "" {
		return p
	}
	if name != "gcloud" {
		return name
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			candidates = append(candidates, filepath.Join(localAppData, "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
		if progFiles86 := os.Getenv("ProgramFiles(x86)"); progFiles86 != "" {
			candidates = append(candidates, filepath.Join(progFiles86, "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
		if progFiles := os.Getenv("ProgramFiles"); progFiles != "" {
			candidates = append(candidates, filepath.Join(progFiles, "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"))
		}
		if home != "" {
			candidates = append(candidates,
				filepath.Join(home, "AppData", "Local", "Google", "Cloud SDK", "google-cloud-sdk", "bin", "gcloud.cmd"),
				filepath.Join(home, "scoop", "apps", "gcloud", "current", "bin", "gcloud.cmd"),
			)
		}
	} else {
		if home != "" {
			candidates = append(candidates,
				filepath.Join(home, "google-cloud-sdk", "bin", "gcloud"),
				filepath.Join(home, ".local", "bin", "gcloud"),
			)
		}
		candidates = append(candidates,
			"/opt/homebrew/bin/gcloud",
			"/opt/homebrew/share/google-cloud-sdk/bin/gcloud",
			"/usr/local/bin/gcloud",
			"/usr/local/share/google-cloud-sdk/bin/gcloud",
			"/usr/bin/gcloud",
			"/snap/bin/gcloud",
		)
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return name
}

// ResolveGeminiAPIKey resolves the Gemini API key from flag options or GEMINI_API_KEY environment variable.
func (m *Manager) ResolveGeminiAPIKey() (string, error) {
	if key := strings.TrimSpace(m.opts.GeminiAPIKey); key != "" {
		return key, nil
	}
	if key := strings.TrimSpace(os.Getenv(EnvGeminiAPIKey)); key != "" {
		return key, nil
	}
	return "", errors.New("Gemini API key is not configured: pass --api-key or set the GEMINI_API_KEY environment variable")
}

// ResolveModel resolves the target Gemini model name with shorthand alias expansion.
// Any custom model name string is passed transparently to the Gemini API endpoint without rejection.
func (m *Manager) ResolveModel() string {
	raw := strings.TrimSpace(m.opts.Model)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(EnvModel))
	}
	if raw == "" {
		return DefaultModel
	}
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "models/") {
		raw = strings.TrimSpace(raw[7:])
	}
	if raw == "" {
		return DefaultModel
	}
	lower := strings.ToLower(raw)
	switch lower {
	case "fast", "flash", "default":
		return DefaultModel
	case "deep", "argon", "pro":
		return DeepModel
	case "hydrogen":
		return HydrogenModel
	case "carbon":
		return CarbonModel
	default:
		if strings.HasPrefix(lower, "gemini-") {
			return lower
		}
		return raw
	}
}

// GetAccessToken returns a valid Google OAuth2 access token using:
// 1. Explicit --access-token flag (or GOOGLE_OAUTH_ACCESS_TOKEN env var)
// 2. Native in-process Application Default Credentials (golang.org/x/oauth2/google)
// 3. Fallback to `gcloud auth application-default print-access-token` -> `gcloud auth print-access-token`
func (m *Manager) GetAccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tok := strings.TrimSpace(m.opts.AccessToken); tok != "" {
		m.lastTokenSrc = "--access-token"
		return tok, nil
	}
	if tok := strings.TrimSpace(os.Getenv(EnvAccessToken)); tok != "" {
		m.lastTokenSrc = "ENV token"
		return tok, nil
	}

	if m.cachedToken != nil && m.cachedToken.Valid() {
		return m.cachedToken.AccessToken, nil
	}

	if m.tokenSource == nil {
		creds, err := m.findDefaultCredentials(ctx, ScopeDocuments, ScopeDrive, ScopeCloud)
		if err == nil && creds != nil && creds.TokenSource != nil {
			m.tokenSource = oauth2.ReuseTokenSource(nil, creds.TokenSource)
			if !m.quotaResolved && len(creds.JSON) > 0 {
				var parsed adcJSON
				if jsonErr := json.Unmarshal(creds.JSON, &parsed); jsonErr == nil && strings.TrimSpace(parsed.QuotaProjectID) != "" {
					if strings.TrimSpace(m.opts.QuotaProject) == "" && strings.TrimSpace(os.Getenv(EnvQuotaProject)) == "" {
						m.quotaProject = strings.TrimSpace(parsed.QuotaProjectID)
						m.quotaResolved = true
					}
				}
			}
		}
	}

	if m.tokenSource != nil {
		tok, err := m.tokenSource.Token()
		if err == nil && tok != nil && strings.TrimSpace(tok.AccessToken) != "" {
			m.cachedToken = tok
			m.lastTokenSrc = "gcloud ADC"
			return strings.TrimSpace(tok.AccessToken), nil
		}
	}

	// Fallback 1: gcloud auth application-default print-access-token
	if out, err := m.execCommand(ctx, "gcloud", "auth", "application-default", "print-access-token"); err == nil {
		if tok := strings.TrimSpace(string(out)); tok != "" {
			m.cachedToken = &oauth2.Token{
				AccessToken: tok,
				Expiry:      time.Now().Add(45 * time.Minute),
			}
			m.lastTokenSrc = "gcloud ADC"
			return tok, nil
		}
	}

	// Fallback 2: gcloud auth print-access-token
	out, err := m.execCommand(ctx, "gcloud", "auth", "print-access-token")
	if err != nil {
		return "", fmt.Errorf("failed to obtain Google OAuth2 access token via ADC or gcloud CLI (%v)\n%s", err, FormatGcloudInstallAndAuthHelp(m.opts.Lang, err))
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("received empty access token from gcloud CLI\n%s", FormatGcloudInstallAndAuthHelp(m.opts.Lang, nil))
	}
	m.cachedToken = &oauth2.Token{
		AccessToken: tok,
		Expiry:      time.Now().Add(45 * time.Minute),
	}
	m.lastTokenSrc = "gcloud CLI"
	return tok, nil
}

// FormatGcloudInstallAndAuthHelp returns concise installation and authentication instructions
// when Google OAuth2 credentials cannot be obtained via ADC or the gcloud CLI.
func FormatGcloudInstallAndAuthHelp(lang string, cause error) string {
	isNotFound := cause != nil && (errors.Is(cause, exec.ErrNotFound) || strings.Contains(strings.ToLower(cause.Error()), "executable file not found") || strings.Contains(strings.ToLower(cause.Error()), "not found"))
	if strings.EqualFold(strings.TrimSpace(lang), "ja") {
		header := "💡 Google OAuth2 アクセストークンを取得できませんでした。"
		if isNotFound {
			header = "💡 Google Cloud SDK (gcloud CLI) がインストールされていないか、PATH に見つかりません。"
		}
		return fmt.Sprintf(`%s

【対処法 1】gcloud CLI をインストールして認証する（Google ドキュメント / Drive 連携モード）:
  1. gcloud CLI のインストール:
     • Linux / macOS (公式ワンライナー):
       curl https://sdk.cloud.google.com | bash
       exec -l $SHELL
     • Ubuntu / Debian (apt):
       sudo apt-get update && sudo apt-get install -y google-cloud-cli
     • macOS (Homebrew):
       brew install --cask google-cloud-sdk
     • Windows (winget):
       winget install Google.CloudSDK
     • 公式インストールガイド: https://cloud.google.com/sdk/docs/install
  2. Application Default Credentials (ADC) でログイン:
     gcloud auth application-default login --scopes="%s,%s,%s"

【対処法 2】gcloud CLI を使わずに実行する:
  • ローカルファイル出力モード（GEMINI_API_KEY のみで動作・Google 認証不要）:
    gdocumentor --no-auth -i <入力ファイル> -o report.md
  • アクセストークンを直接指定する場合:
    --access-token "<TOKEN>" フラグ または 環境変数 GOOGLE_OAUTH_ACCESS_TOKEN を設定`, header, ScopeDocuments, ScopeDrive, ScopeCloud)
	}

	header := "💡 Could not obtain a Google OAuth2 access token."
	if isNotFound {
		header = "💡 Google Cloud SDK (gcloud CLI) is not installed or not found in your PATH."
	}
	return fmt.Sprintf(`%s

[Option 1] Install & authenticate gcloud CLI (for Google Docs & Drive integration):
  1. Install gcloud CLI:
     • Linux / macOS (official 1-liner):
       curl https://sdk.cloud.google.com | bash
       exec -l $SHELL
     • Ubuntu / Debian (apt):
       sudo apt-get update && sudo apt-get install -y google-cloud-cli
     • macOS (Homebrew):
       brew install --cask google-cloud-sdk
     • Windows (winget):
       winget install Google.CloudSDK
     • Official guide: https://cloud.google.com/sdk/docs/install
  2. Log in with Application Default Credentials (ADC):
     gcloud auth application-default login --scopes="%s,%s,%s"

[Option 2] Run without gcloud CLI:
  • Access-Token-Free Local Mode (requires ONLY GEMINI_API_KEY):
    gdocumentor --no-auth -i <input-file> -o report.md
  • Or pass a token directly via --access-token "<TOKEN>" or GOOGLE_OAUTH_ACCESS_TOKEN`, header, ScopeDocuments, ScopeDrive, ScopeCloud)
}

// ResolveQuotaProject resolves the Quota Project ID (`X-Goog-User-Project`) using:
// 1. --quota-project flag
// 2. GOOGLE_CLOUD_QUOTA_PROJECT environment variable
// 3. quota_project_id in Application Default Credentials JSON file
func (m *Manager) ResolveQuotaProject(ctx context.Context) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.quotaResolved {
		return m.quotaProject
	}
	m.quotaResolved = true

	if qp := strings.TrimSpace(m.opts.QuotaProject); qp != "" {
		m.quotaProject = qp
		return m.quotaProject
	}
	if qp := strings.TrimSpace(os.Getenv(EnvQuotaProject)); qp != "" {
		m.quotaProject = qp
		return m.quotaProject
	}

	adcPath := m.opts.ADCFilePath
	if adcPath == "" {
		adcPath = defaultADCFilePath()
	}
	if adcPath != "" {
		if data, err := os.ReadFile(adcPath); err == nil {
			var parsed adcJSON
			if json.Unmarshal(data, &parsed) == nil && strings.TrimSpace(parsed.QuotaProjectID) != "" {
				m.quotaProject = strings.TrimSpace(parsed.QuotaProjectID)
				return m.quotaProject
			}
		}
	}

	return m.quotaProject
}

func defaultADCFilePath() string {
	if envPath := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); envPath != "" {
		return envPath
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData != "" {
			return filepath.Join(appData, "gcloud", "application_default_credentials.json")
		}
	}
	return filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
}

// DoGoogleRequest executes an authenticated HTTP request to a Google Workspace API endpoint.
// It automatically injects Authorization and X-Goog-User-Project headers, and if the API rejects
// the quota project header (HTTP 403/400 quota project permission error), transparently retries without it.
func (m *Manager) DoGoogleRequest(ctx context.Context, method, urlStr string, bodyBytes []byte, contentType string) (int, []byte, http.Header, error) {
	token, err := m.GetAccessToken(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	quotaProj := m.ResolveQuotaProject(ctx)

	status, respBody, hdr, err := m.doSingleRequest(ctx, method, urlStr, bodyBytes, contentType, token, quotaProj)
	if err != nil {
		return 0, nil, nil, err
	}

	// Automatic fallback retry without X-Goog-User-Project if rejected due to quota project permissions
	if quotaProj != "" && (status == http.StatusForbidden || status == http.StatusBadRequest) && isQuotaProjectError(respBody) {
		return m.doSingleRequest(ctx, method, urlStr, bodyBytes, contentType, token, "")
	}

	return status, respBody, hdr, nil
}

func (m *Manager) doSingleRequest(ctx context.Context, method, urlStr string, bodyBytes []byte, contentType, token, quotaProj string) (int, []byte, http.Header, error) {
	var bodyReader io.Reader
	if bodyBytes != nil {
		bodyReader = strings.NewReader(string(bodyBytes))
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if quotaProj != "" {
		req.Header.Set("X-Goog-User-Project", quotaProj)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}
	return resp.StatusCode, data, resp.Header, nil
}

func isQuotaProjectError(respBody []byte) bool {
	lower := strings.ToLower(string(respBody))
	return strings.Contains(lower, "user_project_denied") ||
		strings.Contains(lower, "x-goog-user-project") ||
		strings.Contains(lower, "serviceusage.services.use") ||
		strings.Contains(lower, "quota project")
}

// SetHTTPClient sets a custom HTTP client (primarily for testing).
func (m *Manager) SetHTTPClient(client *http.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if client != nil {
		m.httpClient = client
	}
}

// SetTokenInfoURL sets a custom tokeninfo endpoint URL (for testing).
func (m *Manager) SetTokenInfoURL(endpoint string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(endpoint) != "" {
		m.tokenInfoURL = strings.TrimSpace(endpoint)
	}
}

// TokenInfoError represents an explicit HTTP rejection from the tokeninfo endpoint (e.g. HTTP 400 invalid_token).
type TokenInfoError struct {
	StatusCode int
	ErrCode    string
}

func (e *TokenInfoError) Error() string {
	return fmt.Sprintf("tokeninfo error: %s (status %d)", e.ErrCode, e.StatusCode)
}

// TokenInfoResponse represents the JSON returned by oauth2.googleapis.com/tokeninfo.
type TokenInfoResponse struct {
	Scope     string `json:"scope"`
	ExpiresIn string `json:"expires_in"`
	Email     string `json:"email"`
	Error     string `json:"error"`
}

// InspectAccessTokenInfo inspects the access token by calling the tokeninfo endpoint.
func (m *Manager) InspectAccessTokenInfo(ctx context.Context, token string) (*TokenInfoResponse, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("empty access token")
	}

	m.mu.Lock()
	endpoint := m.tokenInfoURL
	client := m.httpClient
	m.mu.Unlock()
	if endpoint == "" {
		endpoint = "https://oauth2.googleapis.com/tokeninfo"
	}
	if client == nil {
		client = http.DefaultClient
	}

	reqURL := endpoint + "?access_token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var info TokenInfoResponse
	if err := json.Unmarshal(bodyBytes, &info); err != nil {
		if resp.StatusCode != http.StatusOK {
			return nil, &TokenInfoError{StatusCode: resp.StatusCode, ErrCode: "invalid_response"}
		}
		return nil, err
	}
	if info.Error != "" || resp.StatusCode != http.StatusOK {
		errCode := info.Error
		if errCode == "" {
			errCode = "invalid_token"
		}
		return nil, &TokenInfoError{StatusCode: resp.StatusCode, ErrCode: errCode}
	}
	return &info, nil
}

// AuthCheckOptions specifies the context of the current CLI invocation for accurate status reporting.
type AuthCheckOptions struct {
	NoAuth         bool
	RequireGemini  bool // false for 0-token subcommands (edit, manage, inspect)
	RequireDrive   bool // true when operation requires Drive API scope (file conversion, --drive-rag, --delete-doc, --parent-folder)
	SkipNetworkVal bool // true when running against a local mock server in tests
}

// AuthCheckResult contains the detected readiness status of Gemini API and Google OAuth2 credentials.
type AuthCheckResult struct {
	GeminiStatus    string // "READY", "BYPASSED", "MISSING"
	HasGeminiKey    bool
	GeminiKeySource string // "--api-key", "GEMINI_API_KEY", "0 tokens", or ""
	OAuthStatus     string // "READY", "NO_SCOPE", "NO_TOKEN", "INVALID", "BYPASSED"
	OAuthSource     string // "--access-token", "ENV token", "gcloud ADC", "gcloud CLI", ""
	OAuthDetails    string // e.g. "gcloud ADC · Docs/Drive", "missing drive scope", etc.
}

// CheckAuthStatus evaluates the readiness of both Gemini API Key and Google OAuth2 access token.
func (m *Manager) CheckAuthStatus(ctx context.Context, opts AuthCheckOptions) AuthCheckResult {
	res := AuthCheckResult{}

	// 1. Check Gemini API Key
	if strings.TrimSpace(m.opts.GeminiAPIKey) != "" {
		res.HasGeminiKey = true
		res.GeminiStatus = "READY"
		res.GeminiKeySource = "--api-key"
	} else if strings.TrimSpace(os.Getenv(EnvGeminiAPIKey)) != "" {
		res.HasGeminiKey = true
		res.GeminiStatus = "READY"
		res.GeminiKeySource = EnvGeminiAPIKey
	} else if !opts.RequireGemini {
		res.GeminiStatus = "BYPASSED"
		res.GeminiKeySource = "0 tokens"
	} else {
		res.GeminiStatus = "MISSING"
	}

	// 2. Check OAuth2 status
	if opts.NoAuth {
		res.OAuthStatus = "BYPASSED"
		res.OAuthDetails = "--no-auth"
		return res
	}

	// Attempt to obtain access token via Manager (which records lastTokenSrc safely under mutex)
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	token, tokErr := m.GetAccessToken(checkCtx)

	m.mu.Lock()
	source := m.lastTokenSrc
	m.mu.Unlock()

	if tokErr != nil || token == "" {
		res.OAuthStatus = "NO_TOKEN"
		res.OAuthDetails = "gcloud login"
		return res
	}

	res.OAuthSource = source

	if opts.SkipNetworkVal {
		res.OAuthStatus = "READY"
		res.OAuthDetails = source + " · Docs/Drive"
		return res
	}

	// Inspect token scopes via tokeninfo endpoint
	infoCtx, infoCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer infoCancel()
	info, err := m.InspectAccessTokenInfo(infoCtx, token)
	if err != nil {
		var tiErr *TokenInfoError
		if errors.As(err, &tiErr) && (tiErr.StatusCode == http.StatusBadRequest || tiErr.StatusCode == http.StatusUnauthorized) {
			res.OAuthStatus = "INVALID"
			res.OAuthDetails = "expired/invalid token"
			return res
		}
		// Network offline or timeout: token was acquired locally, report READY with source
		res.OAuthStatus = "READY"
		res.OAuthDetails = source
		return res
	}

	// Check scopes: Google Docs API specifically requires ScopeDocuments ("https://www.googleapis.com/auth/documents").
	// Google Drive API operations (conversion, --drive-rag, --delete-doc, --parent-folder) additionally require ScopeDrive.
	scopes := strings.Fields(info.Scope)
	hasDocs := false
	hasDrive := false
	for _, sc := range scopes {
		if sc == ScopeDocuments || sc == "https://www.googleapis.com/auth/documents.readonly" {
			if sc == ScopeDocuments {
				hasDocs = true
			}
		}
		if sc == ScopeDrive || sc == "https://www.googleapis.com/auth/drive.file" {
			if sc == ScopeDrive {
				hasDrive = true
			}
		}
	}

	if !hasDocs {
		res.OAuthStatus = "NO_SCOPE"
		res.OAuthDetails = "missing docs scope"
		return res
	}
	if opts.RequireDrive && !hasDrive {
		res.OAuthStatus = "NO_SCOPE"
		res.OAuthDetails = "missing drive scope"
		return res
	}

	res.OAuthStatus = "READY"
	if hasDocs && hasDrive {
		res.OAuthDetails = source + " · Docs/Drive"
	} else {
		res.OAuthDetails = source + " · Docs only"
	}

	return res
}

