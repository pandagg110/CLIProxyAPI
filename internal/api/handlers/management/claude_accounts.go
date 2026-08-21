package management

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type claudeAccountEntry struct {
	AuthID       string `json:"auth_id"`
	AuthIndex    string `json:"auth_index"`
	FileName     string `json:"file_name"`
	Account      string `json:"account"`
	Label        string `json:"label"`
	Organization string `json:"organization"`
	Provider     string `json:"provider"`
	Status       string `json:"status"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	UsageJoinKey string `json:"usage_join_key"`
}

type claudeAccountListResponse struct {
	Accounts []claudeAccountEntry `json:"accounts"`
	Total    int                  `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

// ListClaudeAccounts returns a safe, paginated catalog of Claude OAuth accounts.
func (h *Handler) ListClaudeAccounts(c *gin.Context) {
	if h == nil || c == nil || c.Request == nil {
		writeClaudeImportError(c, http.StatusServiceUnavailable, "service_unavailable", "Claude account catalog is unavailable.")
		return
	}
	requestContext := withManagementConfigSnapshot(c.Request.Context(), h.configSnapshot())
	requestContext = withManagementAuthManagerSnapshot(requestContext, h.authManagerSnapshot())
	page, okPage := parseClaudeAccountPage(c.Query("page"), 1, 0)
	pageSize, okPageSize := parseClaudeAccountPage(c.Query("page_size"), 50, 200)
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	if status == "" {
		status = "all"
	}
	if !okPage || !okPageSize || status != "all" && status != "active" && status != "disabled" {
		writeClaudeImportError(c, http.StatusBadRequest, "invalid_query", "Claude account query is invalid.")
		return
	}

	auths, errInventory := h.claudeAccountInventory(requestContext)
	if errInventory != nil {
		writeClaudeImportError(c, http.StatusServiceUnavailable, "catalog_unavailable", "Claude account catalog is unavailable.")
		return
	}
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	entries := make([]claudeAccountEntry, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			continue
		}
		entry := buildClaudeAccountEntry(auth)
		if authIndex != "" && entry.AuthIndex != authIndex {
			continue
		}
		if (status == "active" && entry.Disabled) || (status == "disabled" && !entry.Disabled) {
			continue
		}
		if search != "" && !claudeAccountMatchesSearch(entry, search) {
			continue
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(left, right int) bool {
		leftCreated := parseClaudeCatalogTime(entries[left].CreatedAt)
		rightCreated := parseClaudeCatalogTime(entries[right].CreatedAt)
		if !leftCreated.Equal(rightCreated) {
			return leftCreated.After(rightCreated)
		}
		return strings.ToLower(entries[left].FileName) < strings.ToLower(entries[right].FileName)
	})

	total := len(entries)
	start := total
	if page <= total/pageSize+1 {
		start = (page - 1) * pageSize
	}
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageEntries := entries[start:end]
	if pageEntries == nil {
		pageEntries = make([]claudeAccountEntry, 0)
	}
	c.JSON(http.StatusOK, claudeAccountListResponse{Accounts: pageEntries, Total: total, Page: page, PageSize: pageSize})
}

func (h *Handler) claudeAccountInventory(ctx context.Context) ([]*coreauth.Auth, error) {
	combined := make([]*coreauth.Auth, 0)
	seen := make(map[string]struct{})
	manager, managerSnapshotted := managementAuthManagerSnapshotFromContext(ctx)
	if !managerSnapshotted {
		manager = h.authManagerSnapshot()
	}
	store := h.tokenStoreForInventory(ctx)
	if store != nil {
		stored, errList := store.List(ctx)
		if errList == nil {
			// Prefer persisted records so a freshly imported file is not
			// shadowed by a stale runtime-manager snapshot.
			for _, auth := range stored {
				appendUniqueClaudeInventory(&combined, seen, auth)
			}
		} else if manager == nil && !errors.Is(errList, os.ErrNotExist) {
			return nil, errList
		}
	} else if manager == nil {
		return nil, errors.New("token store unavailable")
	}
	if manager != nil {
		for _, auth := range manager.List() {
			appendUniqueClaudeInventory(&combined, seen, auth)
		}
	}
	if len(combined) == 0 && store == nil && manager == nil {
		return nil, errors.New("token store unavailable")
	}
	return combined, nil
}

func buildClaudeAccountEntry(auth *coreauth.Auth) claudeAccountEntry {
	status := strings.TrimSpace(string(auth.Status))
	disabled := auth.Disabled || auth.Status == coreauth.StatusDisabled
	if rawDisabled, ok := auth.Metadata["disabled"].(bool); ok && rawDisabled {
		disabled = true
	}
	if disabled {
		status = string(coreauth.StatusDisabled)
	} else if status == "" || auth.Status == coreauth.StatusUnknown {
		status = string(coreauth.StatusActive)
	}
	email := claudeAuthIdentityValue(auth, "email")
	accountUUID := claudeAuthIdentityValue(auth, "account_uuid")
	label := strings.TrimSpace(auth.Label)
	if label == "" {
		label = claudeMetadataString(auth.Metadata, "label")
	}
	organization := claudeOrganizationDisplay(
		claudeMetadataString(auth.Metadata, "organization_name"),
		claudeMetadataString(auth.Metadata, "organization_uuid"),
	)
	fileName := strings.TrimSpace(auth.FileName)
	if fileName == "" {
		fileName = strings.TrimSpace(auth.ID)
	}
	createdAt := claudeAuthTimestamp(auth.Metadata, "created_at", auth.CreatedAt)
	updatedAt := claudeAuthTimestamp(auth.Metadata, "updated_at", auth.UpdatedAt)
	return claudeAccountEntry{
		AuthID:       maskClaudeFileIdentifier(auth.ID),
		AuthIndex:    safeClaudeResponseText(lockedAuthIndex(auth)),
		FileName:     maskClaudeFileIdentifier(fileName),
		Account:      claudeMaskedAccount(email, accountUUID),
		Label:        safeClaudeResponseText(label),
		Organization: safeClaudeResponseText(organization),
		Provider:     "claude",
		Status:       status,
		Disabled:     disabled,
		CreatedAt:    formatClaudeCatalogTime(createdAt),
		UpdatedAt:    formatClaudeCatalogTime(updatedAt),
		UsageJoinKey: "auth_index",
	}
}

func parseClaudeAccountPage(value string, defaultValue, maximum int) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, true
	}
	parsed, errParse := strconv.Atoi(value)
	if errParse != nil || parsed < 1 || maximum > 0 && parsed > maximum {
		return 0, false
	}
	return parsed, true
}

func claudeAccountMatchesSearch(entry claudeAccountEntry, search string) bool {
	values := []string{entry.Account, entry.Label, entry.Organization, entry.FileName, entry.AuthIndex}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), search) {
			return true
		}
	}
	return false
}

func claudeMaskedAccount(email, accountUUID string) string {
	email = strings.TrimSpace(email)
	if separator := strings.LastIndex(email, "@"); separator > 0 && separator < len(email)-1 {
		local := email[:separator]
		first := local[:1]
		return safeClaudeResponseText(first + "***@" + email[separator+1:])
	}
	accountUUID = strings.TrimSpace(accountUUID)
	if len(accountUUID) > 8 {
		accountUUID = accountUUID[:4] + "..." + accountUUID[len(accountUUID)-4:]
	}
	return safeClaudeResponseText(accountUUID)
}

func claudeAuthTimestamp(metadata map[string]any, key string, fallback time.Time) time.Time {
	if value := claudeMetadataString(metadata, key); value != "" {
		if parsed, errParse := time.Parse(time.RFC3339Nano, value); errParse == nil {
			return parsed.UTC()
		}
	}
	return fallback.UTC()
}

func formatClaudeCatalogTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseClaudeCatalogTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
