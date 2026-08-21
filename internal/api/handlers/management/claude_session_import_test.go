package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fakeClaudeSessionOAuthService struct {
	bundle *claude.ClaudeAuthBundle
	err    error
}

func (f *fakeClaudeSessionOAuthService) ExchangeSessionKey(context.Context, string, claude.SessionKeyScope) (*claude.ClaudeAuthBundle, error) {
	return f.bundle, f.err
}

func (f *fakeClaudeSessionOAuthService) CreateTokenStorage(bundle *claude.ClaudeAuthBundle) *claude.ClaudeTokenStorage {
	return (&claude.ClaudeAuth{}).CreateTokenStorage(bundle)
}

func TestImportClaudeSessionKeysPersistsSafeOrderedOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	h := NewHandler(&config.Config{AuthDir: authDir}, "", nil)
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	h.tokenStore = store
	h.claudeSessionOAuthFactory = func(*config.Config, string) claudeSessionOAuthService {
		return &fakeClaudeSessionOAuthService{bundle: &claude.ClaudeAuthBundle{
			TokenData: claude.ClaudeTokenData{
				AccessToken:      "access-secret",
				RefreshToken:     "refresh-secret",
				Email:            "User@Example.com",
				AccountUUID:      "12345678-1234-1234-1234-123456789abc",
				OrganizationUUID: "org-secret-uuid",
				OrganizationName: "Example Org",
			},
			DeviceIDs: []string{"device-secret"},
		}}
	}

	body := `{"text":"\nsk-ant-sid02-valid\nnot-a-session-key\n","scope":"full"}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude/session-import", strings.NewReader(body))
	h.ImportClaudeSessions(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response claudeSessionImportResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 2 || response.Succeeded != 1 || response.Failed != 1 {
		t.Fatalf("counts = (%d,%d,%d), want (2,1,1)", response.Total, response.Succeeded, response.Failed)
	}
	if response.Items[0].Line != 2 || response.Items[1].Line != 3 {
		t.Fatalf("lines = (%d,%d), want (2,3)", response.Items[0].Line, response.Items[1].Line)
	}
	if response.Items[0].Status != "created" {
		t.Fatalf("success status = %q, want created", response.Items[0].Status)
	}
	if response.Items[1].ErrorCode != "invalid_input" {
		t.Fatalf("invalid error code = %q", response.Items[1].ErrorCode)
	}
	if strings.Contains(recorder.Body.String(), "access-secret") || strings.Contains(recorder.Body.String(), "refresh-secret") || strings.Contains(recorder.Body.String(), "sk-ant-sid02") {
		t.Fatalf("response leaked a credential: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "User@Example.com") || strings.Contains(recorder.Body.String(), "12345678-1234-1234-1234-123456789abc") {
		t.Fatalf("response leaked raw account identity: %s", recorder.Body.String())
	}
	if response.Items[0].FileName != maskClaudeFileIdentifier("claude-user@example.com.json") || response.Items[0].AuthID != response.Items[0].FileName {
		t.Fatalf("response identifiers = %q/%q, want masked stable identifier", response.Items[0].AuthID, response.Items[0].FileName)
	}
	matches, errGlob := filepath.Glob(filepath.Join(authDir, "claude-*.json"))
	if errGlob != nil {
		t.Fatalf("glob persisted auth: %v", errGlob)
	}
	if len(matches) != 1 {
		t.Fatalf("persisted Claude files = %v, want one", matches)
	}
}

func TestImportClaudeSessionKeysDoesNotPersistSessionKeyInLabel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	h := NewHandler(&config.Config{AuthDir: authDir}, "", nil)
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	h.tokenStore = store
	h.claudeSessionOAuthFactory = func(*config.Config, string) claudeSessionOAuthService {
		return &fakeClaudeSessionOAuthService{bundle: &claude.ClaudeAuthBundle{TokenData: claude.ClaudeTokenData{
			AccessToken: "access", RefreshToken: "refresh", Email: "label@example.com", AccountUUID: "label-account",
		}}}
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude/session-import", strings.NewReader(`{"items":[{"sid02":"sk-ant-sid02-valid","label":"seller sk-ant-sid02-secret"}]}`))
	h.ImportClaudeSessions(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	raw, errRead := os.ReadFile(filepath.Join(authDir, "claude-label@example.com.json"))
	if errRead != nil {
		t.Fatalf("read persisted auth: %v", errRead)
	}
	if strings.Contains(string(raw), "sk-ant-sid02-secret") {
		t.Fatalf("persisted auth leaked session key in label: %s", raw)
	}
	if !strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("persisted label was not redacted: %s", raw)
	}
}

func TestImportClaudeSessionKeysRejectsTrailingJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{AuthDir: t.TempDir()}, "", nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude/session-import", strings.NewReader(`{"items":[]} {}`))
	h.ImportClaudeSessions(c)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestImportClaudeSessionKeysRejectsJSONNull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{AuthDir: t.TempDir()}, "", nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude/session-import", strings.NewReader(`null`))
	h.ImportClaudeSessions(c)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestImportClaudeSessionKeysCreatesOneOAuthServicePerRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	h := NewHandler(&config.Config{AuthDir: authDir}, "", nil)
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	h.tokenStore = store
	var factoryCalls atomic.Int32
	h.claudeSessionOAuthFactory = func(*config.Config, string) claudeSessionOAuthService {
		factoryCalls.Add(1)
		return &fakeClaudeSessionOAuthService{bundle: &claude.ClaudeAuthBundle{TokenData: claude.ClaudeTokenData{
			AccessToken: "access", RefreshToken: "refresh", Email: "user@example.com", AccountUUID: "12345678-1234-1234-1234-123456789abc",
		}}}
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude/session-import", strings.NewReader(`{"items":[{"sid02":"sk-ant-sid02-one"},{"sid02":"sk-ant-sid02-two"}]}`))
	h.ImportClaudeSessions(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("OAuth factory calls = %d, want 1", got)
	}
	var response claudeSessionImportResponse
	if errJSON := json.Unmarshal(recorder.Body.Bytes(), &response); errJSON != nil {
		t.Fatalf("decode response: %v", errJSON)
	}
	if response.Succeeded != 2 || response.Items[0].Status == response.Items[1].Status {
		t.Fatalf("statuses = %q/%q, want one created and one updated", response.Items[0].Status, response.Items[1].Status)
	}
}

func TestExchangeAndSaveClaudeSessionAcceptsNilContext(t *testing.T) {
	h := NewHandler(&config.Config{AuthDir: t.TempDir()}, "", nil)
	h.tokenStore = &memoryAuthStore{}
	service := &fakeClaudeSessionOAuthService{bundle: &claude.ClaudeAuthBundle{TokenData: claude.ClaudeTokenData{
		AccessToken: "access", RefreshToken: "refresh", Email: "nil-context@example.com", AccountUUID: "nil-context-account",
	}}}
	outcome := claudeSessionImportOutcome{}

	h.exchangeAndSaveClaudeSession(nil, service, claude.SessionKeyScopeFull, claudeSessionCredential{
		line: 1, sid02: "sk-ant-sid02-nil-context",
	}, &outcome)

	if outcome.Status != "created" {
		t.Fatalf("outcome = %#v, want created", outcome)
	}
}

func TestSaveTokenRecordUsesRequestScopedFileStore(t *testing.T) {
	requestAuthDir := t.TempDir()
	reloadedAuthDir := t.TempDir()
	sharedStore := sdkAuth.NewFileTokenStore()
	sharedStore.SetBaseDir(requestAuthDir)
	h := NewHandler(&config.Config{AuthDir: requestAuthDir}, "", nil)
	h.tokenStore = sharedStore
	h.SetPostAuthHook(func(_ context.Context, _ *coreauth.Auth) error {
		sharedStore.SetBaseDir(reloadedAuthDir)
		return nil
	})
	record := &coreauth.Auth{
		ID:       "claude-snapshot.json",
		Provider: "claude",
		FileName: "claude-snapshot.json",
		Storage: &claude.ClaudeTokenStorage{
			AccessToken: "access", RefreshToken: "refresh", Email: "snapshot@example.com",
		},
		Metadata: map[string]any{"type": "claude", "auth_kind": "oauth", "email": "snapshot@example.com"},
	}
	ctx := withManagementConfigSnapshot(context.Background(), &config.Config{AuthDir: requestAuthDir})
	if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
		t.Fatalf("saveTokenRecord() error = %v", errSave)
	}
	if _, errStat := os.Stat(filepath.Join(requestAuthDir, record.FileName)); errStat != nil {
		t.Fatalf("request snapshot file missing: %v", errStat)
	}
	if _, errStat := os.Stat(filepath.Join(reloadedAuthDir, record.FileName)); !os.IsNotExist(errStat) {
		t.Fatalf("credential escaped into reloaded auth directory: %v", errStat)
	}
}

func TestImportClaudeSessionsUsesStableConfigSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initialDir := t.TempDir()
	h := NewHandler(&config.Config{AuthDir: initialDir}, "", nil)
	h.tokenStore = &memoryAuthStore{}
	started := make(chan struct{})
	release := make(chan struct{})
	h.claudeSessionOAuthFactory = func(cfg *config.Config, _ string) claudeSessionOAuthService {
		if cfg == nil || cfg.AuthDir != initialDir {
			gotDir := "<nil>"
			if cfg != nil {
				gotDir = cfg.AuthDir
			}
			t.Errorf("factory config AuthDir = %q, want %q", gotDir, initialDir)
		}
		close(started)
		<-release
		return &fakeClaudeSessionOAuthService{bundle: &claude.ClaudeAuthBundle{TokenData: claude.ClaudeTokenData{
			AccessToken: "access", RefreshToken: "refresh", Email: "snapshot@example.com", AccountUUID: "snapshot-account",
		}}}
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude/session-import", strings.NewReader(`{"items":[{"sid02":"sk-ant-sid02-snapshot"}]}`))
	done := make(chan struct{})
	go func() {
		h.ImportClaudeSessions(c)
		close(done)
	}()
	<-started
	for index := 0; index < 20; index++ {
		h.SetConfig(&config.Config{AuthDir: filepath.Join(t.TempDir(), "replacement")})
	}
	close(release)
	<-done
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPersistImportedClaudeSessionPreservesExistingRecordAndOperatorMetadata(t *testing.T) {
	authDir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	existing := &coreauth.Auth{
		ID:       "operator-chosen.json",
		Provider: "claude",
		FileName: "operator-chosen.json",
		Label:    "Operator Label",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		Metadata: map[string]any{
			"type":         "claude",
			"auth_kind":    "oauth",
			"email":        "user@example.com",
			"account_uuid": "12345678-1234-1234-1234-123456789abc",
			"note":         "keep me",
			"priority":     float64(7),
			"label":        "Operator Label",
			"proxy_url":    "http://operator-proxy.example",
		},
	}
	existing.Disabled = false
	existing.Status = coreauth.StatusActive
	if _, errSave := store.Save(context.Background(), existing); errSave != nil {
		t.Fatalf("save existing auth: %v", errSave)
	}
	existing.Disabled = true
	existing.Status = coreauth.StatusDisabled
	if _, errSave := store.Save(context.Background(), existing); errSave != nil {
		t.Fatalf("disable existing auth: %v", errSave)
	}
	h := NewHandler(&config.Config{AuthDir: authDir}, "", nil)
	h.tokenStore = store
	hookPath := ""
	h.SetPostAuthHook(func(_ context.Context, auth *coreauth.Auth) error {
		hookPath = auth.Attributes[coreauth.AttributePath]
		return nil
	})
	bundle := &claude.ClaudeAuthBundle{
		TokenData: claude.ClaudeTokenData{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			Email:        "user@example.com",
			AccountUUID:  "12345678-1234-1234-1234-123456789abc",
		},
	}
	storage := (&claude.ClaudeAuth{}).CreateTokenStorage(bundle)
	record, updated, errPersist := h.persistImportedClaudeSession(context.Background(), storage, bundle, "New Label")
	if errPersist != nil {
		t.Fatalf("persist import: %v", errPersist)
	}
	if !updated {
		t.Fatal("persist import reported created, want updated")
	}
	if record.ID != existing.ID || record.FileName != existing.FileName {
		t.Fatalf("identity = %q/%q, want %q/%q", record.ID, record.FileName, existing.ID, existing.FileName)
	}
	firstIndex := record.EnsureIndex()
	if firstIndex == "" || record.EnsureIndex() != firstIndex {
		t.Fatalf("auth index = %q, want a stable non-empty index", firstIndex)
	}
	if !record.Disabled || record.Status != coreauth.StatusDisabled || record.Label != "New Label" {
		t.Fatalf("operator state not preserved: disabled=%t status=%q label=%q", record.Disabled, record.Status, record.Label)
	}
	if !filepath.IsAbs(hookPath) {
		t.Fatalf("post-auth hook path = %q, want absolute", hookPath)
	}
	raw, errRead := os.ReadFile(filepath.Join(authDir, existing.FileName))
	if errRead != nil {
		t.Fatalf("read persisted auth: %v", errRead)
	}
	var persisted map[string]any
	if errJSON := json.Unmarshal(raw, &persisted); errJSON != nil {
		t.Fatalf("decode persisted auth: %v", errJSON)
	}
	if persisted["note"] != "keep me" || persisted["priority"] != float64(7) || persisted["label"] != "New Label" || persisted["proxy_url"] != "http://operator-proxy.example" {
		t.Fatalf("operator metadata not preserved: %#v", persisted)
	}
}

func TestPersistImportedClaudeSessionDropsExistingSessionSecrets(t *testing.T) {
	authDir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	existing := &coreauth.Auth{
		ID:       "claude-existing.json",
		Provider: "claude",
		FileName: "claude-existing.json",
		Metadata: map[string]any{
			"type":          "claude",
			"auth_kind":     "oauth",
			"email":         "user@example.com",
			"account_uuid":  "12345678-1234-1234-1234-123456789abc",
			"sid02":         "sk-ant-sid02-existing-secret",
			"session_key":   "sk-ant-sid02-session-secret",
			"cookie":        "sessionKey=sk-ant-sid02-cookie-secret",
			"authorization": "Bearer existing-authorization-secret",
			"headers": map[string]any{
				"Authorization": "Bearer nested-authorization-secret",
				"Cookie":        "sessionKey=sk-ant-sid02-nested-cookie-secret",
				"X-Safe":        "keep-safe-header",
			},
			"note":     "keep me",
			"priority": float64(7),
		},
	}
	if _, errSave := store.Save(context.Background(), existing); errSave != nil {
		t.Fatalf("save existing auth: %v", errSave)
	}

	h := NewHandler(&config.Config{AuthDir: authDir}, "", nil)
	h.tokenStore = store
	h.SetPostAuthHook(func(_ context.Context, auth *coreauth.Auth) error {
		auth.Metadata["session_cookie"] = "sessionKey=sk-ant-sid02-hook-secret"
		return nil
	})
	bundle := &claude.ClaudeAuthBundle{TokenData: claude.ClaudeTokenData{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		Email:        "user@example.com",
		AccountUUID:  "12345678-1234-1234-1234-123456789abc",
	}}
	storage := (&claude.ClaudeAuth{}).CreateTokenStorage(bundle)
	if _, _, errPersist := h.persistImportedClaudeSession(context.Background(), storage, bundle, ""); errPersist != nil {
		t.Fatalf("persist import: %v", errPersist)
	}

	raw, errRead := os.ReadFile(filepath.Join(authDir, existing.FileName))
	if errRead != nil {
		t.Fatalf("read persisted auth: %v", errRead)
	}
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"sk-ant-sid02-",
		`"sid02"`,
		`"session_key"`,
		`"cookie"`,
		`"authorization"`,
		"existing-authorization-secret",
		"nested-authorization-secret",
	} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("persisted auth retained session secret %q: %s", forbidden, raw)
		}
	}
	var persisted map[string]any
	if errJSON := json.Unmarshal(raw, &persisted); errJSON != nil {
		t.Fatalf("decode persisted auth: %v", errJSON)
	}
	if persisted["note"] != "keep me" || persisted["priority"] != float64(7) {
		t.Fatalf("operator metadata not preserved: %#v", persisted)
	}
	headers, _ := persisted["headers"].(map[string]any)
	if headers["X-Safe"] != "keep-safe-header" {
		t.Fatalf("safe nested metadata not preserved: %#v", headers)
	}
}

func TestPersistImportedClaudeSessionNormalizesDisabledExistingStatus(t *testing.T) {
	store := &memoryAuthStore{items: map[string]*coreauth.Auth{
		"claude-existing.json": {
			ID: "claude-existing.json", Provider: "claude", FileName: "claude-existing.json",
			Status: coreauth.StatusDisabled, Disabled: false,
			Metadata: map[string]any{"email": "user@example.com", "account_uuid": "12345678-1234-1234-1234-123456789abc"},
		},
	}}
	h := NewHandler(&config.Config{AuthDir: t.TempDir()}, "", nil)
	h.tokenStore = store
	bundle := &claude.ClaudeAuthBundle{TokenData: claude.ClaudeTokenData{
		AccessToken: "access", RefreshToken: "refresh", Email: "user@example.com", AccountUUID: "12345678-1234-1234-1234-123456789abc",
	}}
	storage := (&claude.ClaudeAuth{}).CreateTokenStorage(bundle)
	record, updated, errPersist := h.persistImportedClaudeSession(context.Background(), storage, bundle, "")
	if errPersist != nil {
		t.Fatalf("persist import: %v", errPersist)
	}
	if !updated || !record.Disabled || record.Status != coreauth.StatusDisabled {
		t.Fatalf("state = updated=%t disabled=%t status=%q, want updated=true disabled=true status=disabled", updated, record.Disabled, record.Status)
	}
}
