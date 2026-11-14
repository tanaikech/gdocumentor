package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type staticTokenSource struct {
	token string
}

func (s staticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{
		AccessToken: s.token,
		Expiry:      time.Now().Add(1 * time.Hour),
	}, nil
}

func TestResolveModelAndAPIKey(t *testing.T) {
	t.Setenv(EnvGeminiAPIKey, "env-secret-key")
	t.Setenv(EnvModel, "")

	m1 := NewManager(Options{})
	key, err := m1.ResolveGeminiAPIKey()
	if err != nil || key != "env-secret-key" {
		t.Fatalf("expected env-secret-key, got %q (err=%v)", key, err)
	}
	if mod := m1.ResolveModel(); mod != DefaultModel {
		t.Fatalf("expected default model %q, got %q", DefaultModel, mod)
	}

	// Custom arbitrary model and explicit API key
	m2 := NewManager(Options{
		GeminiAPIKey: "flag-key",
		Model:        "gemini-custom-experimental-99",
	})
	key2, err := m2.ResolveGeminiAPIKey()
	if err != nil || key2 != "flag-key" {
		t.Fatalf("expected flag-key, got %q (err=%v)", key2, err)
	}
	if mod2 := m2.ResolveModel(); mod2 != "gemini-custom-experimental-99" {
		t.Fatalf("expected custom model passed transparently, got %q", mod2)
	}

	for alias, want := range map[string]string{
		"hydrogen":          "gemini-4-hydrogen",
		"carbon":            "gemini-4-carbon",
		"models/gemini-4-carbon": "gemini-4-carbon",
	} {
		mAlias := NewManager(Options{Model: alias})
		if got := mAlias.ResolveModel(); got != want {
			t.Fatalf("ResolveModel(%q): expected %q, got %q", alias, want, got)
		}
	}
}

func TestGetAccessTokenPriorityAndFallback(t *testing.T) {
	ctx := context.Background()
	t.Setenv(EnvAccessToken, "")

	// 1. Flag priority
	mFlag := NewManager(Options{AccessToken: "flag-oauth-token"})
	tok, err := mFlag.GetAccessToken(ctx)
	if err != nil || tok != "flag-oauth-token" {
		t.Fatalf("expected flag-oauth-token, got %q (err=%v)", tok, err)
	}

	// 2. Native ADC priority
	mADC := NewManager(Options{})
	mADC.findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return &google.Credentials{
			TokenSource: staticTokenSource{token: "native-adc-token"},
			JSON:        []byte(`{"type":"authorized_user","quota_project_id":"adc-quota-proj"}`),
		}, nil
	}
	tok2, err := mADC.GetAccessToken(ctx)
	if err != nil || tok2 != "native-adc-token" {
		t.Fatalf("expected native-adc-token, got %q (err=%v)", tok2, err)
	}
	if qp := mADC.ResolveQuotaProject(ctx); qp != "adc-quota-proj" {
		t.Fatalf("expected quota project adc-quota-proj, got %q", qp)
	}

	// 3. Fallback to gcloud CLI when native ADC fails
	mFallback := NewManager(Options{})
	mFallback.findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return nil, errors.New("no ADC")
	}
	mFallback.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "application-default" {
			return []byte("gcloud-adc-fallback-token\n"), nil
		}
		return []byte("gcloud-user-fallback-token\n"), nil
	}
	tok3, err := mFallback.GetAccessToken(ctx)
	if err != nil || tok3 != "gcloud-adc-fallback-token" {
		t.Fatalf("expected gcloud-adc-fallback-token, got %q (err=%v)", tok3, err)
	}

	// 4. When gcloud CLI is not installed (exec.ErrNotFound), error includes installation instructions (EN & JA)
	mMissingEn := NewManager(Options{Lang: "en"})
	mMissingEn.findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return nil, errors.New("no ADC")
	}
	mMissingEn.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	}
	if _, err := mMissingEn.GetAccessToken(ctx); err == nil {
		t.Fatalf("expected error when ADC and gcloud fail")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "not installed") || !strings.Contains(msg, "https://cloud.google.com/sdk/docs/install") || !strings.Contains(msg, "gcloud auth application-default login") || !strings.Contains(msg, "--no-auth") {
			t.Fatalf("expected English gcloud installation guide in error, got:\n%s", msg)
		}
	}

	mMissingJa := NewManager(Options{Lang: "ja"})
	mMissingJa.findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return nil, errors.New("no ADC")
	}
	mMissingJa.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	}
	if _, err := mMissingJa.GetAccessToken(ctx); err == nil {
		t.Fatalf("expected error when ADC and gcloud fail (ja)")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "インストール") || !strings.Contains(msg, "https://cloud.google.com/sdk/docs/install") || !strings.Contains(msg, "gcloud auth application-default login") || !strings.Contains(msg, "--no-auth") {
			t.Fatalf("expected Japanese gcloud installation guide in error, got:\n%s", msg)
		}
	}
}

func TestQuotaProjectResolutionAndFallbackRetry(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	adcFile := filepath.Join(tmpDir, "adc.json")
	if err := os.WriteFile(adcFile, []byte(`{"type":"authorized_user","quota_project_id":"bad-quota-proj"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		qp := r.Header.Get("X-Goog-User-Project")
		if qp == "bad-quota-proj" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"status":"PERMISSION_DENIED","message":"Caller does not have required permission to use project bad-quota-proj. Grant the caller the roles/serviceusage.serviceUsageConsumer role, or a custom role with the serviceusage.services.use permission"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	m := NewManager(Options{
		AccessToken: "test-token",
		ADCFilePath: adcFile,
	})
	m.SetHTTPClient(srv.Client())

	status, body, _, err := m.DoGoogleRequest(ctx, http.MethodGet, srv.URL, nil, "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200 after retry without X-Goog-User-Project, got %d: %s", status, string(body))
	}
	if callCount != 2 {
		t.Fatalf("expected 2 calls (1st with quota project, 2nd fallback without), got %d", callCount)
	}
}

func TestCheckAuthStatus(t *testing.T) {
	ctx := context.Background()

	origEnvKey := os.Getenv(EnvGeminiAPIKey)
	origEnvTok := os.Getenv(EnvAccessToken)
	_ = os.Unsetenv(EnvGeminiAPIKey)
	_ = os.Unsetenv(EnvAccessToken)
	defer func() {
		if origEnvKey != "" {
			_ = os.Setenv(EnvGeminiAPIKey, origEnvKey)
		}
		if origEnvTok != "" {
			_ = os.Setenv(EnvAccessToken, origEnvTok)
		}
	}()

	// 1. Test NoAuth mode
	mNoAuth := NewManager(Options{
		GeminiAPIKey: "fake-key-123",
	})
	resNoAuth := mNoAuth.CheckAuthStatus(ctx, AuthCheckOptions{NoAuth: true, RequireGemini: true})
	if !resNoAuth.HasGeminiKey || resNoAuth.GeminiStatus != "READY" || resNoAuth.GeminiKeySource != "--api-key" {
		t.Fatalf("expected Gemini key READY from flag, got %+v", resNoAuth)
	}
	if resNoAuth.OAuthStatus != "BYPASSED" {
		t.Fatalf("expected BYPASSED OAuth in NoAuth mode, got %+v", resNoAuth)
	}

	// 2. Test 0-token subcommand (RequireGemini=false) without Gemini key
	mZeroTok := NewManager(Options{})
	resZero := mZeroTok.CheckAuthStatus(ctx, AuthCheckOptions{NoAuth: true, RequireGemini: false})
	if resZero.GeminiStatus != "BYPASSED" || resZero.GeminiKeySource != "0 tokens" {
		t.Fatalf("expected GeminiStatus=BYPASSED (0 tokens) when RequireGemini=false, got %+v", resZero)
	}

	// 3. Test Missing Key and No Token
	mEmpty := NewManager(Options{})
	mEmpty.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("command failed")
	}
	mEmpty.findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return nil, errors.New("no ADC")
	}
	resEmpty := mEmpty.CheckAuthStatus(ctx, AuthCheckOptions{RequireGemini: true})
	if resEmpty.HasGeminiKey || resEmpty.GeminiStatus != "MISSING" {
		t.Fatalf("expected HasGeminiKey=false & MISSING, got %+v", resEmpty)
	}
	if resEmpty.OAuthStatus != "NO_TOKEN" {
		t.Fatalf("expected NO_TOKEN, got %+v", resEmpty)
	}

	// 4. Test Token with Documents + Drive scopes, Docs-only scope, Cloud-only scope, and Invalid token via mock tokeninfo server
	tokeninfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		tok := r.URL.Query().Get("access_token")
		switch tok {
		case "tok-full":
			_, _ = w.Write([]byte(`{"scope":"https://www.googleapis.com/auth/documents https://www.googleapis.com/auth/drive https://www.googleapis.com/auth/cloud-platform","expires_in":"3599"}`))
		case "tok-docs-only":
			_, _ = w.Write([]byte(`{"scope":"https://www.googleapis.com/auth/documents","expires_in":"3599"}`))
		case "tok-cloud-only":
			_, _ = w.Write([]byte(`{"scope":"https://www.googleapis.com/auth/cloud-platform","expires_in":"3599"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
		}
	}))
	defer tokeninfoSrv.Close()

	// 4a: Full scopes via gcloud ADC fallback
	mADC := NewManager(Options{GeminiAPIKey: "k"})
	mADC.SetHTTPClient(tokeninfoSrv.Client())
	mADC.SetTokenInfoURL(tokeninfoSrv.URL)
	mADC.findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return nil, errors.New("no in-process ADC")
	}
	mADC.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "application-default" {
			return []byte("tok-full\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	resADC := mADC.CheckAuthStatus(ctx, AuthCheckOptions{RequireGemini: true, RequireDrive: true})
	if resADC.OAuthStatus != "READY" || resADC.OAuthSource != "gcloud ADC" || resADC.OAuthDetails != "gcloud ADC · Docs/Drive" {
		t.Fatalf("expected READY (gcloud ADC · Docs/Drive), got %+v", resADC)
	}

	// 4b: Docs-only scope when RequireDrive=false vs RequireDrive=true
	mDocsOnly := NewManager(Options{AccessToken: "tok-docs-only", GeminiAPIKey: "k"})
	mDocsOnly.SetHTTPClient(tokeninfoSrv.Client())
	mDocsOnly.SetTokenInfoURL(tokeninfoSrv.URL)
	resDocsOK := mDocsOnly.CheckAuthStatus(ctx, AuthCheckOptions{RequireGemini: true, RequireDrive: false})
	if resDocsOK.OAuthStatus != "READY" || resDocsOK.OAuthDetails != "--access-token · Docs only" {
		t.Fatalf("expected READY (--access-token · Docs only), got %+v", resDocsOK)
	}
	resDocsNoDrive := mDocsOnly.CheckAuthStatus(ctx, AuthCheckOptions{RequireGemini: true, RequireDrive: true})
	if resDocsNoDrive.OAuthStatus != "NO_SCOPE" || resDocsNoDrive.OAuthDetails != "missing drive scope" {
		t.Fatalf("expected NO_SCOPE (missing drive scope), got %+v", resDocsNoDrive)
	}

	// 4c: Cloud-platform-only scope (missing documents scope)
	mCloudOnly := NewManager(Options{AccessToken: "tok-cloud-only", GeminiAPIKey: "k"})
	mCloudOnly.SetHTTPClient(tokeninfoSrv.Client())
	mCloudOnly.SetTokenInfoURL(tokeninfoSrv.URL)
	resCloudOnly := mCloudOnly.CheckAuthStatus(ctx, AuthCheckOptions{RequireGemini: true})
	if resCloudOnly.OAuthStatus != "NO_SCOPE" || resCloudOnly.OAuthDetails != "missing docs scope" {
		t.Fatalf("expected NO_SCOPE (missing docs scope) for cloud-platform-only token, got %+v", resCloudOnly)
	}

	// 4d: Invalid/expired token
	mInvalid := NewManager(Options{AccessToken: "tok-expired", GeminiAPIKey: "k"})
	mInvalid.SetHTTPClient(tokeninfoSrv.Client())
	mInvalid.SetTokenInfoURL(tokeninfoSrv.URL)
	resInvalid := mInvalid.CheckAuthStatus(ctx, AuthCheckOptions{RequireGemini: true})
	if resInvalid.OAuthStatus != "INVALID" {
		t.Fatalf("expected INVALID for expired token, got %+v", resInvalid)
	}
}

