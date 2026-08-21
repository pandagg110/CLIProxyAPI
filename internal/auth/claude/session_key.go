package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

const (
	OrganizationsURL              = "https://claude.ai/api/organizations"
	SessionAuthorizeURLFormat     = "https://claude.ai/v1/oauth/%s/authorize"
	claudeSessionBrowserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// SessionKeyScope controls the OAuth permissions requested for a Claude session key.
type SessionKeyScope string

const (
	SessionKeyScopeFull      SessionKeyScope = "full"
	SessionKeyScopeInference SessionKeyScope = "inference"
)

// SessionKeyErrorCode identifies the stage at which a session-key exchange failed.
type SessionKeyErrorCode string

const (
	SessionKeyErrorInvalidInput             SessionKeyErrorCode = "invalid_input"
	SessionKeyErrorOrganizationLookupFailed SessionKeyErrorCode = "organization_lookup_failed"
	SessionKeyErrorAuthorizationFailed      SessionKeyErrorCode = "authorization_failed"
	SessionKeyErrorTokenExchangeFailed      SessionKeyErrorCode = "token_exchange_failed"
)

// SessionKeyExchangeError is a stable, user-safe error for a sid02 exchange.
// Its cause is retained for internal inspection but never included in Error().
type SessionKeyExchangeError struct {
	Code  SessionKeyErrorCode
	cause error
}

func (e *SessionKeyExchangeError) Error() string {
	if e == nil {
		return "Claude session key exchange failed"
	}
	switch e.Code {
	case SessionKeyErrorInvalidInput:
		return "Claude session key is invalid"
	case SessionKeyErrorOrganizationLookupFailed:
		return "Claude organization lookup failed"
	case SessionKeyErrorAuthorizationFailed:
		return "Claude authorization failed"
	case SessionKeyErrorTokenExchangeFailed:
		return "Claude token exchange failed"
	default:
		return "Claude session key exchange failed"
	}
}

func (e *SessionKeyExchangeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newSessionKeyError(code SessionKeyErrorCode, cause error) error {
	return &SessionKeyExchangeError{Code: code, cause: cause}
}

// ValidateSessionKey reports whether sessionKey is a non-empty sid02 key.
// ASCII whitespace around a key is tolerated; the suffix is restricted to its
// observed cookie-safe ASCII alphabet.
func ValidateSessionKey(sessionKey string) bool {
	trimmed := strings.Trim(sessionKey, " \t\r\n")
	if trimmed == "" || !strings.HasPrefix(trimmed, "sk-ant-sid02-") {
		return false
	}
	value := strings.TrimPrefix(trimmed, "sk-ant-sid02-")
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func oauthScope(scope SessionKeyScope) (string, bool) {
	switch scope {
	case "", SessionKeyScopeFull:
		return ClaudeOAuthScope, true
	case SessionKeyScopeInference:
		return "user:inference", true
	default:
		return "", false
	}
}

func (scope SessionKeyScope) oauthScope() (string, bool) {
	return oauthScope(scope)
}

type sessionKeyOrganization struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	RavenType string `json:"raven_type"`
}

type sessionKeyAuthorizeRequest struct {
	ResponseType        string `json:"response_type"`
	ClientID            string `json:"client_id"`
	OrganizationUUID    string `json:"organization_uuid"`
	RedirectURI         string `json:"redirect_uri"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
}

type sessionKeyAuthorizeResponse struct {
	RedirectURI string `json:"redirect_uri"`
}

// ExchangeSessionKey exchanges a Claude sid02 session key for an OAuth bundle.
// scope may be empty (the full Claude OAuth scope), full, or inference.
func (o *ClaudeAuth) ExchangeSessionKey(ctx context.Context, sessionKey string, scope SessionKeyScope) (*ClaudeAuthBundle, error) {
	if !ValidateSessionKey(sessionKey) {
		return nil, newSessionKeyError(SessionKeyErrorInvalidInput, fmt.Errorf("session key format is invalid"))
	}
	oauthScopeValue, okScope := oauthScope(scope)
	if !okScope {
		return nil, newSessionKeyError(SessionKeyErrorInvalidInput, fmt.Errorf("session key scope is invalid"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if o == nil || o.httpClient == nil {
		return nil, newSessionKeyError(SessionKeyErrorOrganizationLookupFailed, fmt.Errorf("Claude HTTP client is nil"))
	}
	key := strings.Trim(sessionKey, " \t\r\n")
	organization, errOrganization := o.lookupSessionKeyOrganization(ctx, key)
	if errOrganization != nil {
		return nil, newSessionKeyError(SessionKeyErrorOrganizationLookupFailed, errOrganization)
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		return nil, newSessionKeyError(SessionKeyErrorAuthorizationFailed, errState)
	}
	pkceCodes, errPKCE := GeneratePKCECodes()
	if errPKCE != nil {
		return nil, newSessionKeyError(SessionKeyErrorAuthorizationFailed, errPKCE)
	}
	authorizeURL := fmt.Sprintf(SessionAuthorizeURLFormat, url.PathEscape(organization.UUID))
	authorizeBody := sessionKeyAuthorizeRequest{
		ResponseType:        "code",
		ClientID:            ClientID,
		OrganizationUUID:    organization.UUID,
		RedirectURI:         PlatformRedirectURI,
		Scope:               oauthScopeValue,
		State:               state,
		CodeChallenge:       pkceCodes.CodeChallenge,
		CodeChallengeMethod: "S256",
	}
	code, returnedState, errAuthorize := o.authorizeSessionKey(ctx, key, authorizeURL, authorizeBody, state)
	if errAuthorize != nil {
		return nil, newSessionKeyError(SessionKeyErrorAuthorizationFailed, errAuthorize)
	}

	bundle, errExchange := o.exchangeCodeForTokens(ctx, code, returnedState, pkceCodes, PlatformRedirectURI)
	if errExchange != nil {
		return nil, newSessionKeyError(SessionKeyErrorTokenExchangeFailed, errExchange)
	}
	if bundle == nil || strings.TrimSpace(bundle.TokenData.AccessToken) == "" || strings.TrimSpace(bundle.TokenData.RefreshToken) == "" {
		return nil, newSessionKeyError(SessionKeyErrorTokenExchangeFailed, fmt.Errorf("token response is missing access or refresh token"))
	}
	if strings.TrimSpace(bundle.TokenData.OrganizationUUID) == "" {
		bundle.TokenData.OrganizationUUID = organization.UUID
	}
	if strings.TrimSpace(bundle.TokenData.OrganizationName) == "" {
		bundle.TokenData.OrganizationName = organization.Name
	}
	return bundle, nil
}

func (o *ClaudeAuth) lookupSessionKeyOrganization(ctx context.Context, sessionKey string) (*sessionKeyOrganization, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, OrganizationsURL, nil)
	if errRequest != nil {
		return nil, errRequest
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Origin", "https://claude.ai")
	req.Header.Set("Referer", "https://claude.ai/")
	req.Header.Set("User-Agent", claudeSessionBrowserUserAgent)
	req.AddCookie(&http.Cookie{Name: "sessionKey", Value: sessionKey})
	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("organizations response body is nil")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Error("failed to close Claude session key organizations response body")
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("organizations request returned status %d", resp.StatusCode)
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, errRead
	}
	var organizations []sessionKeyOrganization
	if errJSON := json.Unmarshal(body, &organizations); errJSON != nil {
		return nil, errJSON
	}
	for index := range organizations {
		if uuid := strings.TrimSpace(organizations[index].UUID); uuid != "" {
			organizations[index].UUID = uuid
			organizations[index].Name = strings.TrimSpace(organizations[index].Name)
			return &organizations[index], nil
		}
	}
	return nil, fmt.Errorf("organizations response contains no organization UUID")
}

func (o *ClaudeAuth) authorizeSessionKey(ctx context.Context, sessionKey, endpoint string, payload sessionKeyAuthorizeRequest, expectedState string) (string, string, error) {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return "", "", errMarshal
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if errRequest != nil {
		return "", "", errRequest
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://claude.ai")
	req.Header.Set("Referer", "https://claude.ai/new")
	req.Header.Set("User-Agent", claudeSessionBrowserUserAgent)
	req.AddCookie(&http.Cookie{Name: "sessionKey", Value: sessionKey})
	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return "", "", errDo
	}
	if resp == nil || resp.Body == nil {
		return "", "", fmt.Errorf("authorize response body is nil")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Error("failed to close Claude session key authorize response body")
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("authorize request returned status %d", resp.StatusCode)
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", "", errRead
	}
	var authorizeResponse sessionKeyAuthorizeResponse
	if errJSON := json.Unmarshal(body, &authorizeResponse); errJSON != nil {
		return "", "", errJSON
	}
	code, state, errRedirect := parseSessionKeyRedirect(authorizeResponse.RedirectURI)
	if errRedirect != nil {
		return "", "", errRedirect
	}
	if code == "" || state == "" || state != expectedState {
		return "", "", fmt.Errorf("authorize redirect code or state is invalid")
	}
	return code, state, nil
}

func parseSessionKeyRedirect(raw string) (string, string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", "", fmt.Errorf("authorize redirect is empty")
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil {
		return "", "", errParse
	}
	query, errQuery := url.ParseQuery(parsed.RawQuery)
	if errQuery != nil {
		return "", "", errQuery
	}
	code := strings.TrimSpace(query.Get("code"))
	state := query.Get("state")
	fragment := parsed.Fragment
	if fragment != "" && strings.Contains(fragment, "=") {
		fragmentQuery, errFragment := url.ParseQuery(fragment)
		if errFragment != nil {
			return "", "", errFragment
		}
		if code == "" {
			code = strings.TrimSpace(fragmentQuery.Get("code"))
		}
		if state == "" {
			state = fragmentQuery.Get("state")
		}
	} else if fragment != "" && state == "" {
		state = fragment
	}
	if strings.Contains(code, "#") {
		parts := strings.SplitN(code, "#", 2)
		code = strings.TrimSpace(parts[0])
		if state == "" {
			state = parts[1]
		}
	}
	if code == "" || state == "" {
		return "", "", fmt.Errorf("authorize redirect is missing code or state")
	}
	return code, state, nil
}
