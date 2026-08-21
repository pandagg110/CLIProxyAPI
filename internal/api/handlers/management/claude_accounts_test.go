package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListClaudeAccountsReturnsOnlySafeClaudeFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &memoryAuthStore{items: map[string]*coreauth.Auth{
		"claude.json": {
			ID: "claude.json", Provider: "claude", FileName: "claude.json", Label: "Team Claude", Status: coreauth.StatusActive,
			Metadata:  map[string]any{"email": "user@example.com", "organization_name": "Example Org", "access_token": "access-secret", "custom_secret": "hidden"},
			CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		},
		"codex.json": {ID: "codex.json", Provider: "codex", FileName: "codex.json", Metadata: map[string]any{"email": "other@example.com"}},
	}}
	h := NewHandler(&config.Config{}, "", nil)
	h.tokenStore = store

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude/accounts", nil)
	h.ListClaudeAccounts(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response claudeAccountListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 1 || len(response.Accounts) != 1 {
		t.Fatalf("total/accounts = %d/%d, want 1/1", response.Total, len(response.Accounts))
	}
	if !strings.Contains(response.Accounts[0].Account, "***@example.com") {
		t.Fatalf("account = %q, want masked email", response.Accounts[0].Account)
	}
	if response.Accounts[0].AuthID != maskClaudeFileIdentifier("claude.json") || response.Accounts[0].FileName != maskClaudeFileIdentifier("claude.json") {
		t.Fatalf("catalog identifiers = %q/%q, want masked stable identifier", response.Accounts[0].AuthID, response.Accounts[0].FileName)
	}
	if strings.Contains(recorder.Body.String(), "access-secret") || strings.Contains(recorder.Body.String(), "custom_secret") || strings.Contains(recorder.Body.String(), "user@example.com") {
		t.Fatalf("catalog leaked metadata: %s", recorder.Body.String())
	}
}

func TestListClaudeAccountsMergesManagerAndTokenStoreInventory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "manager-claude.json", Provider: "claude", FileName: "manager-claude.json",
		Metadata: map[string]any{"email": "manager@example.com", "account_uuid": "manager-account"},
	}); errRegister != nil {
		t.Fatalf("register manager auth: %v", errRegister)
	}
	store := &memoryAuthStore{items: map[string]*coreauth.Auth{
		"store-claude.json": {
			ID: "store-claude.json", Provider: "claude", FileName: "store-claude.json",
			Metadata: map[string]any{"email": "store@example.com", "account_uuid": "store-account"},
		},
	}}
	h := NewHandler(&config.Config{}, "", manager)
	h.tokenStore = store
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude/accounts", nil)
	h.ListClaudeAccounts(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response claudeAccountListResponse
	if errJSON := json.Unmarshal(recorder.Body.Bytes(), &response); errJSON != nil {
		t.Fatalf("decode response: %v", errJSON)
	}
	if response.Total != 2 || len(response.Accounts) != 2 {
		t.Fatalf("total/accounts = %d/%d, want 2/2", response.Total, len(response.Accounts))
	}
}

func TestListClaudeAccountsDeduplicatesManagerAndStoreByAccountUUID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "stale-manager.json", Provider: "claude", FileName: "stale-manager.json",
		Metadata: map[string]any{"email": "old@example.com", "account_uuid": "shared-account"},
	}); errRegister != nil {
		t.Fatalf("register manager auth: %v", errRegister)
	}
	store := &memoryAuthStore{items: map[string]*coreauth.Auth{
		"persisted.json": {
			ID: "persisted.json", Provider: "claude", FileName: "persisted.json", Label: "Persisted",
			Metadata: map[string]any{"email": "new@example.com", "account_uuid": "shared-account"},
		},
	}}
	h := NewHandler(&config.Config{}, "", manager)
	h.tokenStore = store
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude/accounts", nil)
	h.ListClaudeAccounts(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response claudeAccountListResponse
	if errJSON := json.Unmarshal(recorder.Body.Bytes(), &response); errJSON != nil {
		t.Fatalf("decode response: %v", errJSON)
	}
	if response.Total != 1 || len(response.Accounts) != 1 || response.Accounts[0].Label != "Persisted" {
		t.Fatalf("accounts = %#v, want one store-preferred account", response.Accounts)
	}
}

type failingClaudeAccountStore struct{}

func (failingClaudeAccountStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, context.Canceled
}
func (failingClaudeAccountStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", nil
}
func (failingClaudeAccountStore) Delete(context.Context, string) error { return nil }

func TestListClaudeAccountsReturnsServiceUnavailableOnStorageFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{}, "", nil)
	h.tokenStore = failingClaudeAccountStore{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude/accounts", nil)
	h.ListClaudeAccounts(c)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", recorder.Code, recorder.Body.String())
	}
}
