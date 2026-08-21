package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	claudeSessionImportBodyLimit  = 1 << 20
	claudeSessionImportMaxEntries = 500
	claudeSessionExchangeTimeout  = 60 * time.Second
)

type claudeSessionOAuthService interface {
	ExchangeSessionKey(context.Context, string, claude.SessionKeyScope) (*claude.ClaudeAuthBundle, error)
	CreateTokenStorage(*claude.ClaudeAuthBundle) *claude.ClaudeTokenStorage
}

type claudeSessionImportItem struct {
	SID02 string `json:"sid02"`
	Label string `json:"label"`
}

type claudeSessionImportRequest struct {
	Text     string                    `json:"text"`
	Items    []claudeSessionImportItem `json:"items"`
	Scope    string                    `json:"scope"`
	ProxyURL string                    `json:"proxy_url"`
}

type claudeSessionImportOutcome struct {
	Line         int    `json:"line"`
	Status       string `json:"status"`
	AuthID       string `json:"auth_id,omitempty"`
	AuthIndex    string `json:"auth_index,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	Account      string `json:"account,omitempty"`
	Label        string `json:"label,omitempty"`
	Organization string `json:"organization,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Message      string `json:"message,omitempty"`
}

type claudeSessionImportResponse struct {
	Total     int                          `json:"total"`
	Succeeded int                          `json:"succeeded"`
	Failed    int                          `json:"failed"`
	Items     []claudeSessionImportOutcome `json:"items"`
}

type claudeSessionCredential struct {
	line  int
	sid02 string
	label string
}

type claudeImportAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type claudeImportAPIErrorEnvelope struct {
	Error claudeImportAPIError `json:"error"`
}

// ImportClaudeSessions exchanges Claude session keys and persists OAuth credentials.
func (h *Handler) ImportClaudeSessions(c *gin.Context) {
	if h == nil || c == nil || c.Request == nil {
		writeClaudeImportError(c, http.StatusServiceUnavailable, "service_unavailable", "Claude session import is unavailable.")
		return
	}
	configSnapshot := h.configSnapshot()
	requestContext := withManagementConfigSnapshot(c.Request.Context(), configSnapshot)
	requestContext = withManagementAuthManagerSnapshot(requestContext, h.authManagerSnapshot())
	if c.Request.Body == nil {
		writeClaudeImportError(c, http.StatusBadRequest, "invalid_request", "Request body must be one JSON object.")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, claudeSessionImportBodyLimit)
	decoder := json.NewDecoder(c.Request.Body)
	var request *claudeSessionImportRequest
	if errDecode := decoder.Decode(&request); errDecode != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(errDecode, &maxBytesError) {
			writeClaudeImportError(c, http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 1 MiB.")
			return
		}
		writeClaudeImportError(c, http.StatusBadRequest, "invalid_request", "Request body must be one JSON object.")
		return
	}
	if request == nil {
		writeClaudeImportError(c, http.StatusBadRequest, "invalid_request", "Request body must be one JSON object.")
		return
	}
	if errTrailing := requireJSONEOF(decoder); errTrailing != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(errTrailing, &maxBytesError) {
			writeClaudeImportError(c, http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 1 MiB.")
			return
		}
		writeClaudeImportError(c, http.StatusBadRequest, "invalid_request", "Request body must be one JSON object.")
		return
	}

	scope, okScope := normalizeClaudeSessionScope(request.Scope)
	if !okScope {
		writeClaudeImportError(c, http.StatusBadRequest, "invalid_scope", "Scope must be full or inference.")
		return
	}
	credentials := parseClaudeSessionCredentials(*request)
	if len(credentials) > claudeSessionImportMaxEntries {
		writeClaudeImportError(c, http.StatusBadRequest, "too_many_items", "A maximum of 500 session keys may be imported at once.")
		return
	}
	if len(credentials) == 0 {
		writeClaudeImportError(c, http.StatusBadRequest, "invalid_request", "At least one Claude session key is required.")
		return
	}

	response := claudeSessionImportResponse{
		Total: len(credentials),
		Items: make([]claudeSessionImportOutcome, len(credentials)),
	}
	jobs := make(chan int, len(credentials))
	for index := range credentials {
		credential := credentials[index]
		response.Items[index].Line = credential.line
		response.Items[index].Label = safeClaudeResponseText(credential.label)
		if !claude.ValidateSessionKey(credential.sid02) {
			setClaudeImportFailure(&response.Items[index], "invalid_input")
			continue
		}
		jobs <- index
	}
	close(jobs)
	var service claudeSessionOAuthService
	if len(jobs) > 0 && h.claudeSessionOAuthFactory != nil {
		service = h.claudeSessionOAuthFactory(configSnapshot, strings.TrimSpace(request.ProxyURL))
	}

	workerCount := 2
	if len(credentials) < workerCount {
		workerCount = len(credentials)
	}
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for index := range jobs {
				h.exchangeAndSaveClaudeSession(requestContext, service, scope, credentials[index], &response.Items[index])
			}
		}()
	}
	workers.Wait()

	for index := range response.Items {
		if response.Items[index].Status == "created" || response.Items[index].Status == "updated" {
			response.Succeeded++
		} else {
			response.Failed++
		}
	}
	c.JSON(http.StatusOK, response)
}

// ImportClaudeSessionKeys is retained as an internal compatibility alias.
func (h *Handler) ImportClaudeSessionKeys(c *gin.Context) {
	h.ImportClaudeSessions(c)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	errDecode := decoder.Decode(&trailing)
	if errors.Is(errDecode, io.EOF) {
		return nil
	}
	if errDecode == nil {
		return errors.New("trailing JSON value")
	}
	return errDecode
}

func normalizeClaudeSessionScope(value string) (claude.SessionKeyScope, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(claude.SessionKeyScopeFull):
		return claude.SessionKeyScopeFull, true
	case string(claude.SessionKeyScopeInference):
		return claude.SessionKeyScopeInference, true
	default:
		return "", false
	}
}

func parseClaudeSessionCredentials(request claudeSessionImportRequest) []claudeSessionCredential {
	if request.Items != nil {
		credentials := make([]claudeSessionCredential, 0, len(request.Items))
		for index := range request.Items {
			credentials = append(credentials, claudeSessionCredential{
				line:  index + 1,
				sid02: strings.TrimSpace(request.Items[index].SID02),
				label: strings.TrimSpace(request.Items[index].Label),
			})
		}
		return credentials
	}
	lines := strings.Split(request.Text, "\n")
	credentials := make([]claudeSessionCredential, 0, len(lines))
	for index := range lines {
		value := strings.TrimSpace(lines[index])
		if value == "" {
			continue
		}
		credentials = append(credentials, claudeSessionCredential{line: index + 1, sid02: value})
	}
	return credentials
}

func (h *Handler) exchangeAndSaveClaudeSession(ctx context.Context, service claudeSessionOAuthService, scope claude.SessionKeyScope, credential claudeSessionCredential, outcome *claudeSessionImportOutcome) {
	if outcome == nil {
		return
	}
	if service == nil {
		setClaudeImportFailure(outcome, "token_exchange_failed")
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	exchangeContext, cancel := context.WithTimeout(ctx, claudeSessionExchangeTimeout)
	bundle, errExchange := service.ExchangeSessionKey(exchangeContext, credential.sid02, scope)
	cancel()
	if errExchange != nil {
		setClaudeImportExchangeFailure(outcome, errExchange)
		return
	}
	if ctx != nil && ctx.Err() != nil {
		setClaudeImportFailure(outcome, "token_exchange_failed")
		return
	}
	if bundle == nil {
		setClaudeImportFailure(outcome, "token_exchange_failed")
		return
	}
	email := strings.ToLower(strings.TrimSpace(bundle.TokenData.Email))
	accountUUID := strings.TrimSpace(bundle.TokenData.AccountUUID)
	if email == "" && accountUUID == "" {
		setClaudeImportFailure(outcome, "identity_missing")
		return
	}
	storage := service.CreateTokenStorage(bundle)
	if storage == nil {
		setClaudeImportFailure(outcome, "token_exchange_failed")
		return
	}
	if ctx != nil && ctx.Err() != nil {
		setClaudeImportFailure(outcome, "token_exchange_failed")
		return
	}
	record, updated, errRecord := h.persistImportedClaudeSession(ctx, storage, bundle, credential.label)
	if errRecord != nil {
		setClaudeImportFailure(outcome, "save_failed")
		return
	}
	outcome.Status = "created"
	if updated {
		outcome.Status = "updated"
	}
	outcome.AuthID = maskClaudeFileIdentifier(record.ID)
	outcome.AuthIndex = safeClaudeResponseText(record.EnsureIndex())
	outcome.FileName = maskClaudeFileIdentifier(record.FileName)
	outcome.Account = claudeMaskedAccount(email, accountUUID)
	outcome.Label = safeClaudeResponseText(record.Label)
	outcome.Organization = safeClaudeResponseText(claudeOrganizationDisplay(bundle.TokenData.OrganizationName, bundle.TokenData.OrganizationUUID))
}

func setClaudeImportExchangeFailure(outcome *claudeSessionImportOutcome, errExchange error) {
	var exchangeError *claude.SessionKeyExchangeError
	if errors.As(errExchange, &exchangeError) && exchangeError != nil {
		switch exchangeError.Code {
		case claude.SessionKeyErrorInvalidInput:
			setClaudeImportFailure(outcome, "invalid_input")
		case claude.SessionKeyErrorOrganizationLookupFailed:
			setClaudeImportFailure(outcome, "organization_lookup_failed")
		case claude.SessionKeyErrorAuthorizationFailed:
			setClaudeImportFailure(outcome, "authorization_failed")
		case claude.SessionKeyErrorTokenExchangeFailed:
			setClaudeImportFailure(outcome, "token_exchange_failed")
		default:
			setClaudeImportFailure(outcome, "token_exchange_failed")
		}
		return
	}
	setClaudeImportFailure(outcome, "token_exchange_failed")
}

func setClaudeImportFailure(outcome *claudeSessionImportOutcome, code string) {
	if outcome == nil {
		return
	}
	outcome.Status = "failed"
	outcome.ErrorCode = code
	switch code {
	case "invalid_input":
		outcome.Message = "Invalid Claude session key."
	case "organization_lookup_failed":
		outcome.Message = "Claude organization lookup failed."
	case "authorization_failed":
		outcome.Message = "Claude authorization failed."
	case "identity_missing":
		outcome.Message = "Claude account identity is missing."
	case "save_failed":
		outcome.Message = "Claude account could not be saved."
	default:
		outcome.ErrorCode = "token_exchange_failed"
		outcome.Message = "Claude token exchange failed."
	}
}

func writeClaudeImportError(c *gin.Context, status int, code, message string) {
	if c == nil {
		return
	}
	c.JSON(status, claudeImportAPIErrorEnvelope{Error: claudeImportAPIError{Code: code, Message: message}})
}

func (h *Handler) persistImportedClaudeSession(ctx context.Context, storage *claude.ClaudeTokenStorage, bundle *claude.ClaudeAuthBundle, requestedLabel string) (*coreauth.Auth, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	h.claudeImportSaveMu.Lock()
	defer h.claudeImportSaveMu.Unlock()
	if ctx != nil && ctx.Err() != nil {
		return nil, false, ctx.Err()
	}

	configSnapshot, configSnapshotted := managementConfigSnapshotValue(ctx)
	if configSnapshotted && configSnapshot == nil {
		return nil, false, errors.New("Claude auth directory is unavailable")
	}
	inventory, errInventory := h.claudeImportInventory(ctx)
	if errInventory != nil {
		return nil, false, errors.New("Claude account inventory is unavailable")
	}
	email := strings.ToLower(strings.TrimSpace(bundle.TokenData.Email))
	accountUUID := strings.TrimSpace(bundle.TokenData.AccountUUID)
	existing := findExistingClaudeIdentity(inventory, email, accountUUID)

	fileName := ""
	if existing != nil {
		fileName = strings.TrimSpace(existing.FileName)
		if fileName == "" {
			fileName = strings.TrimSpace(existing.ID)
		}
	} else {
		var errFileName error
		fileName, errFileName = h.newClaudeCredentialFileNameWithConfig(configSnapshot, inventory, email, accountUUID)
		if errFileName != nil {
			return nil, false, errFileName
		}
	}
	if existing == nil {
		if !safeClaudeCredentialFileName(fileName) {
			return nil, false, errors.New("Claude credential filename is unsafe")
		}
	} else if strings.Contains(strings.ToLower(fileName), "sk-ant-sid02-") {
		// Never reopen a legacy path that would expose a session key through the
		// credential-save log or filesystem metadata.
		return nil, false, errors.New("Claude credential filename contains session material")
	} else if _, errExistingPath := h.absoluteClaudeCredentialPathWithConfig(configSnapshot, fileName); errExistingPath != nil {
		return nil, false, errExistingPath
	}

	now := time.Now().UTC()
	createdAt := now
	label := sanitizeClaudeLabel(requestedLabel)
	effectiveProxyURL := ""
	status := coreauth.StatusActive
	disabled := false
	attributes := make(map[string]string)
	if existing != nil {
		if existingCreatedAt := claudeAuthTimestamp(existing.Metadata, "created_at", existing.CreatedAt); !existingCreatedAt.IsZero() {
			createdAt = existingCreatedAt
		}
		if label == "" {
			if strings.TrimSpace(existing.Label) != "" {
				label = sanitizeClaudeLabel(existing.Label)
			} else if existingLabel := claudeMetadataString(existing.Metadata, "label"); existingLabel != "" {
				label = sanitizeClaudeLabel(existingLabel)
			}
		}
		if strings.TrimSpace(existing.ProxyURL) != "" {
			effectiveProxyURL = strings.TrimSpace(existing.ProxyURL)
		} else if existingProxy := claudeMetadataString(existing.Metadata, "proxy_url"); existingProxy != "" {
			effectiveProxyURL = existingProxy
		}
		status = existing.Status
		if status == coreauth.StatusUnknown || status == "" {
			status = coreauth.StatusActive
		}
		disabled = existing.Disabled
		if existing.Disabled || existing.Status == coreauth.StatusDisabled {
			disabled = true
			status = coreauth.StatusDisabled
		}
		for key, value := range existing.Attributes {
			attributes[key] = value
		}
	}
	absolutePath, errPath := h.absoluteClaudeCredentialPathWithConfig(configSnapshot, fileName)
	if errPath != nil {
		return nil, false, errPath
	}
	attributes[coreauth.AttributePath] = absolutePath
	attributes[coreauth.AttributeSource] = absolutePath
	attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceFile

	metadata := map[string]any{
		"type":              "claude",
		"auth_kind":         "oauth",
		"email":             email,
		"account_uuid":      accountUUID,
		"organization_uuid": strings.TrimSpace(bundle.TokenData.OrganizationUUID),
		"organization_name": strings.TrimSpace(bundle.TokenData.OrganizationName),
		"claude_device_ids": append([]string(nil), bundle.DeviceIDs...),
		"created_at":        createdAt.Format(time.RFC3339Nano),
		"updated_at":        now.Format(time.RFC3339Nano),
	}
	if label != "" {
		metadata["label"] = label
	}
	if effectiveProxyURL != "" {
		metadata["proxy_url"] = effectiveProxyURL
	}
	record := &coreauth.Auth{
		ID:         fileName,
		Provider:   "claude",
		FileName:   fileName,
		Storage:    storage,
		Label:      label,
		Status:     status,
		Disabled:   disabled,
		ProxyURL:   effectiveProxyURL,
		Attributes: attributes,
		Metadata:   metadata,
		CreatedAt:  createdAt,
		UpdatedAt:  now,
	}
	if existing != nil {
		record.ID = existing.ID
		if strings.TrimSpace(record.ID) == "" {
			record.ID = fileName
		}
		// Preserve an explicitly assigned runtime index when updating an
		// existing record; normal file-backed records derive the same value from
		// their stable absolute path below.
		record.Index = existing.Index
		coreauth.MergeExistingAuthMetadata(record, existing.Metadata)
	}
	if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
		return nil, false, errors.New("Claude account could not be saved")
	}
	record.EnsureIndex()
	return record, existing != nil, nil
}

func (h *Handler) claudeImportInventory(ctx context.Context) ([]*coreauth.Auth, error) {
	combined := make([]*coreauth.Auth, 0)
	seen := make(map[string]struct{})
	manager, managerSnapshotted := managementAuthManagerSnapshotFromContext(ctx)
	if !managerSnapshotted {
		manager = h.authManagerSnapshot()
	}
	store := h.tokenStoreForInventory(ctx)
	if store == nil {
		if manager == nil {
			return nil, errors.New("token store unavailable")
		}
	} else {
		stored, errList := store.List(ctx)
		if errList != nil {
			if !errors.Is(errList, os.ErrNotExist) {
				return nil, errList
			}
		} else {
			// Prefer persisted records so an older runtime-manager snapshot cannot
			// shadow the identity and filename just written to disk.
			for _, auth := range stored {
				appendUniqueClaudeInventory(&combined, seen, auth)
			}
		}
	}
	if manager != nil {
		for _, auth := range manager.List() {
			appendUniqueClaudeInventory(&combined, seen, auth)
		}
	}
	return combined, nil
}

func appendUniqueClaudeInventory(combined *[]*coreauth.Auth, seen map[string]struct{}, auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	keys := claudeInventoryKeys(auth)
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			return
		}
	}
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	*combined = append(*combined, auth)
}

func claudeInventoryKeys(auth *coreauth.Auth) []string {
	if auth == nil {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	keys := make([]string, 0, 3)
	if path := claudeAuthPathValue(auth); path != "" {
		keys = append(keys, provider+"|path|"+path)
	}
	if accountUUID := strings.ToLower(strings.TrimSpace(claudeAuthIdentityValue(auth, "account_uuid"))); accountUUID != "" {
		keys = append(keys, provider+"|account_uuid|"+accountUUID)
	} else if email := strings.ToLower(strings.TrimSpace(claudeAuthIdentityValue(auth, "email"))); email != "" {
		keys = append(keys, provider+"|email|"+email)
	}
	if len(keys) == 0 {
		keys = append(keys, provider+"|id|"+strings.ToLower(strings.TrimSpace(auth.ID))+"|"+strings.ToLower(strings.TrimSpace(auth.FileName)))
	}
	return keys
}

func claudeAuthPathValue(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	path := ""
	if auth.Attributes != nil {
		path = strings.TrimSpace(auth.Attributes[coreauth.AttributePath])
		if path == "" {
			path = strings.TrimSpace(auth.Attributes[coreauth.AttributeSource])
		}
	}
	if path == "" {
		path = strings.TrimSpace(auth.FileName)
	}
	if path == "" {
		path = strings.TrimSpace(auth.ID)
	}
	if path == "" {
		return ""
	}
	if absolute, errAbsolute := filepath.Abs(path); errAbsolute == nil {
		path = absolute
	}
	return strings.ToLower(filepath.Clean(path))
}

func findExistingClaudeIdentity(inventory []*coreauth.Auth, email, accountUUID string) *coreauth.Auth {
	for _, auth := range inventory {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			continue
		}
		existingUUID := claudeAuthIdentityValue(auth, "account_uuid")
		if accountUUID != "" && existingUUID != "" && strings.EqualFold(accountUUID, existingUUID) {
			return auth
		}
	}
	if accountUUID != "" || email == "" {
		return nil
	}
	for _, auth := range inventory {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			continue
		}
		if claudeAuthIdentityValue(auth, "account_uuid") != "" {
			continue
		}
		if strings.EqualFold(email, claudeAuthIdentityValue(auth, "email")) {
			return auth
		}
	}
	return nil
}

func (h *Handler) newClaudeCredentialFileName(inventory []*coreauth.Auth, email, accountUUID string) (string, error) {
	return h.newClaudeCredentialFileNameWithConfig(nil, inventory, email, accountUUID)
}

func (h *Handler) newClaudeCredentialFileNameWithConfig(cfg *config.Config, inventory []*coreauth.Auth, email, accountUUID string) (string, error) {
	identity := email
	if identity == "" {
		identity = accountUUID
	}
	segment := normalizeClaudeFileSegment(identity)
	if segment == "" {
		return "", errors.New("Claude account identity cannot form a safe filename")
	}
	base := "claude-" + segment + ".json"
	if !h.claudeCredentialFileOccupiedWithConfig(cfg, inventory, base) {
		return base, nil
	}
	for _, suffix := range claudeCollisionSuffixes(email, accountUUID) {
		candidate := "claude-" + segment + "-" + suffix + ".json"
		if safeClaudeCredentialFileName(candidate) && !h.claudeCredentialFileOccupiedWithConfig(cfg, inventory, candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("Claude credential filename collision")
}

func (h *Handler) claudeCredentialFileOccupied(inventory []*coreauth.Auth, fileName string) bool {
	return h.claudeCredentialFileOccupiedWithConfig(nil, inventory, fileName)
}

func (h *Handler) claudeCredentialFileOccupiedWithConfig(cfg *config.Config, inventory []*coreauth.Auth, fileName string) bool {
	for _, auth := range inventory {
		if auth == nil {
			continue
		}
		candidate := strings.TrimSpace(auth.FileName)
		if candidate == "" {
			candidate = strings.TrimSpace(auth.ID)
		}
		if strings.EqualFold(filepath.Base(candidate), fileName) {
			return true
		}
	}
	path, errPath := h.absoluteClaudeCredentialPathWithConfig(cfg, fileName)
	if errPath != nil {
		return true
	}
	_, errStat := os.Stat(path)
	return errStat == nil || !os.IsNotExist(errStat)
}

func normalizeClaudeFileSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if sidIndex := strings.Index(value, "sk-ant-sid02-"); sidIndex >= 0 {
		value = value[:sidIndex] + "credential"
	}
	var builder strings.Builder
	lastSeparator := false
	for _, character := range value {
		allowed := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '@' || character == '.' || character == '_' || character == '-'
		if allowed {
			builder.WriteRune(character)
			lastSeparator = false
			continue
		}
		if !lastSeparator {
			builder.WriteByte('-')
			lastSeparator = true
		}
	}
	segment := strings.Trim(builder.String(), ".-_@")
	if len(segment) > 120 {
		segment = strings.Trim(segment[:120], ".-_@")
	}
	return segment
}

func claudeCollisionSuffixes(email, accountUUID string) []string {
	identity := strings.TrimSpace(accountUUID)
	if identity != "" {
		identity = strings.ReplaceAll(normalizeClaudeFileSegment(identity), "-", "")
	} else if strings.TrimSpace(email) != "" {
		digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
		identity = hex.EncodeToString(digest[:])
	}
	if identity == "" {
		return nil
	}
	lengths := []int{8, 12, len(identity)}
	result := make([]string, 0, len(lengths))
	seen := make(map[string]struct{}, len(lengths))
	for _, length := range lengths {
		if length > len(identity) {
			length = len(identity)
		}
		suffix := identity[:length]
		if suffix == "" {
			continue
		}
		if _, exists := seen[suffix]; exists {
			continue
		}
		seen[suffix] = struct{}{}
		result = append(result, suffix)
	}
	return result
}

func safeClaudeCredentialFileName(fileName string) bool {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" || filepath.IsAbs(fileName) || filepath.Base(fileName) != fileName || len(fileName) > 180 {
		return false
	}
	return strings.HasPrefix(strings.ToLower(fileName), "claude-") && strings.HasSuffix(strings.ToLower(fileName), ".json")
}

func (h *Handler) absoluteClaudeCredentialPath(fileName string) (string, error) {
	return h.absoluteClaudeCredentialPathWithConfig(nil, fileName)
}

func (h *Handler) absoluteClaudeCredentialPathWithConfig(cfg *config.Config, fileName string) (string, error) {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" || !strings.HasSuffix(strings.ToLower(filepath.Base(fileName)), ".json") {
		return "", errors.New("Claude credential filename is unsafe")
	}
	path := fileName
	if cfg == nil {
		cfg = h.configSnapshot()
	}
	if !filepath.IsAbs(path) {
		if filepath.Base(path) != path {
			return "", errors.New("Claude credential filename is unsafe")
		}
		if cfg == nil || strings.TrimSpace(cfg.AuthDir) == "" {
			return "", errors.New("Claude auth directory is unavailable")
		}
		path = filepath.Join(cfg.AuthDir, path)
	}
	absolutePath, errAbsolute := filepath.Abs(path)
	if errAbsolute != nil {
		return "", errors.New("Claude credential path is unavailable")
	}
	absolutePath = filepath.Clean(absolutePath)
	if cfg != nil && strings.TrimSpace(cfg.AuthDir) != "" {
		authDir, errAuthDir := filepath.Abs(cfg.AuthDir)
		if errAuthDir != nil {
			return "", errors.New("Claude auth directory is unavailable")
		}
		relative, errRelative := filepath.Rel(filepath.Clean(authDir), absolutePath)
		if errRelative != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", errors.New("Claude credential filename is unsafe")
		}
	}
	return absolutePath, nil
}

func claudeMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func claudeAuthIdentityValue(auth *coreauth.Auth, key string) string {
	if auth == nil {
		return ""
	}
	if value := claudeMetadataString(auth.Metadata, key); value != "" {
		return value
	}
	if auth.Attributes != nil {
		return strings.TrimSpace(auth.Attributes[key])
	}
	return ""
}

func claudeOrganizationDisplay(name, uuid string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	uuid = strings.TrimSpace(uuid)
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}

func safeClaudeResponseText(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	for {
		index := strings.Index(lower, "sk-ant-sid02-")
		if index < 0 {
			break
		}
		end := index
		for end < len(value) && !unicode.IsSpace(rune(value[end])) {
			end++
		}
		value = value[:index] + "[redacted]" + value[end:]
		lower = strings.ToLower(value)
	}
	return value
}

// sanitizeClaudeLabel keeps operator labels useful while ensuring an accidentally
// pasted session key is never persisted as part of an imported credential.
func sanitizeClaudeLabel(value string) string {
	return safeClaudeResponseText(strings.TrimSpace(value))
}

// stripClaudeOAuthSessionSecrets removes browser-session material that may
// have been copied from an existing auth file by the generic metadata merge.
// Safe operator metadata remains untouched.
func stripClaudeOAuthSessionSecrets(record *coreauth.Auth) {
	if record == nil || !strings.EqualFold(strings.TrimSpace(record.Provider), "claude") || record.AuthKind() != coreauth.AuthKindOAuth {
		return
	}
	if storage, ok := record.Storage.(*claude.ClaudeTokenStorage); ok && storage != nil {
		storage.Email = stripClaudeOAuthSessionSecretString(storage.Email)
		storage.AccountUUID = stripClaudeOAuthSessionSecretString(storage.AccountUUID)
		storage.OrganizationUUID = stripClaudeOAuthSessionSecretString(storage.OrganizationUUID)
		storage.OrganizationName = stripClaudeOAuthSessionSecretString(storage.OrganizationName)
	}
	for key, value := range record.Metadata {
		if claudeOAuthSessionSecretKey(key) {
			delete(record.Metadata, key)
			continue
		}
		record.Metadata[key] = stripClaudeOAuthSessionSecretValue(value)
	}
}

func claudeOAuthSessionSecretKey(key string) bool {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(key)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	value := normalized.String()
	return strings.Contains(value, "sid02") || strings.Contains(value, "sessionkey") || strings.Contains(value, "cookie") || strings.Contains(value, "authorization")
}

func stripClaudeOAuthSessionSecretValue(value any) any {
	switch typed := value.(type) {
	case string:
		return stripClaudeOAuthSessionSecretString(typed)
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, nested := range typed {
			if claudeOAuthSessionSecretKey(key) {
				continue
			}
			clean[key] = stripClaudeOAuthSessionSecretValue(nested)
		}
		return clean
	case map[string]string:
		clean := make(map[string]string, len(typed))
		for key, nested := range typed {
			if claudeOAuthSessionSecretKey(key) {
				continue
			}
			clean[key] = stripClaudeOAuthSessionSecretString(nested)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index := range typed {
			clean[index] = stripClaudeOAuthSessionSecretValue(typed[index])
		}
		return clean
	case []string:
		clean := make([]string, len(typed))
		for index := range typed {
			clean[index] = stripClaudeOAuthSessionSecretString(typed[index])
		}
		return clean
	default:
		return value
	}
}

func stripClaudeOAuthSessionSecretString(value string) string {
	if !strings.Contains(strings.ToLower(value), "sk-ant-sid02-") {
		return value
	}
	return safeClaudeResponseText(value)
}

func claudeOAuthStorageContainsSessionKey(record *coreauth.Auth) bool {
	if record == nil {
		return false
	}
	storage, ok := record.Storage.(*claude.ClaudeTokenStorage)
	if !ok || storage == nil {
		return false
	}
	for _, value := range []string{storage.AccessToken, storage.RefreshToken, storage.IDToken} {
		if strings.Contains(strings.ToLower(value), "sk-ant-sid02-") {
			return true
		}
	}
	return false
}

// maskClaudeFileIdentifier converts a persisted filename or ID into a stable
// opaque identifier suitable for management responses. The actual filename is
// intentionally never exposed because it may contain an email or account UUID.
func maskClaudeFileIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return "claude-" + hex.EncodeToString(digest[:4]) + ".json"
}
