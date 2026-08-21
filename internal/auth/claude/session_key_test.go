package claude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestValidateSessionKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "valid", key: "sk-ant-sid02-abc123", want: true},
		{name: "trimmed", key: "  sk-ant-sid02-abc123  ", want: true},
		{name: "surrounding ascii whitespace", key: "\t\r\nsk-ant-sid02-abc123\r\n\t", want: true},
		{name: "empty", key: "", want: false},
		{name: "prefix only", key: "sk-ant-sid02-", want: false},
		{name: "wrong prefix", key: "sk-ant-api03-abc123", want: false},
		{name: "unicode whitespace", key: "sk-ant-sid02-abc\u2003", want: false},
		{name: "semicolon delimiter", key: "sk-ant-sid02-abc;admin=true", want: false},
		{name: "comma delimiter", key: "sk-ant-sid02-abc,admin=true", want: false},
		{name: "internal newline", key: "sk-ant-sid02-ab\nc", want: false},
		{name: "control byte", key: "sk-ant-sid02-abc\x00", want: false},
		{name: "punctuation", key: "sk-ant-sid02-abc.def", want: false},
		{name: "safe alphabet", key: "sk-ant-sid02-AbC_123-xyz", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateSessionKey(tt.key); got != tt.want {
				t.Fatalf("ValidateSessionKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestSessionKeyScope(t *testing.T) {
	for _, tt := range []struct {
		name  string
		scope SessionKeyScope
		want  string
		valid bool
	}{
		{name: "empty", scope: "", want: ClaudeOAuthScope, valid: true},
		{name: "full", scope: SessionKeyScopeFull, want: ClaudeOAuthScope, valid: true},
		{name: "inference", scope: SessionKeyScopeInference, want: "user:inference", valid: true},
		{name: "unknown", scope: "wat", valid: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := oauthScope(tt.scope)
			if ok != tt.valid || got != tt.want {
				t.Fatalf("oauthScope(%q) = %q, %v; want %q, %v", tt.scope, got, ok, tt.want, tt.valid)
			}
		})
	}
}

func TestSessionKeyExchangeErrorMessagesAreStable(t *testing.T) {
	for _, tt := range []struct {
		code SessionKeyErrorCode
		want string
	}{
		{code: SessionKeyErrorInvalidInput, want: "Claude session key is invalid"},
		{code: SessionKeyErrorOrganizationLookupFailed, want: "Claude organization lookup failed"},
		{code: SessionKeyErrorAuthorizationFailed, want: "Claude authorization failed"},
		{code: SessionKeyErrorTokenExchangeFailed, want: "Claude token exchange failed"},
	} {
		errExchange := &SessionKeyExchangeError{Code: tt.code, cause: errors.New("upstream secret")}
		if got := errExchange.Error(); got != tt.want {
			t.Fatalf("error message for %q = %q, want %q", tt.code, got, tt.want)
		}
		if strings.Contains(errExchange.Error(), "upstream secret") {
			t.Fatalf("error message leaked cause: %q", errExchange.Error())
		}
	}
}

func TestExchangeSessionKeyHappyPath(t *testing.T) {
	const sid02 = "sk-ant-sid02-test-secret"
	var order []string
	var authorizeBody map[string]any
	var tokenBody map[string]any

	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		order = append(order, req.URL.String())
		switch req.URL.String() {
		case OrganizationsURL:
			if got := req.Header.Get("Cookie"); got != "sessionKey="+sid02 {
				t.Fatalf("organizations cookie = %q", got)
			}
			return sessionKeyJSONResponse(req, `[ {"uuid":"org-1","name":"Org One","raven_type":"claude"} ]`), nil
		case "https://claude.ai/v1/oauth/org-1/authorize":
			if got := req.Header.Get("Cookie"); got != "sessionKey="+sid02 {
				t.Fatalf("authorize cookie = %q", got)
			}
			body, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if errJSON := json.Unmarshal(body, &authorizeBody); errJSON != nil {
				t.Fatal(errJSON)
			}
			return sessionKeyJSONResponse(req, `{"redirect_uri":"https://platform.claude.com/oauth/code/callback?code=auth-code&state=`+authorizeBody["state"].(string)+`"}`), nil
		case TokenURL:
			if got := req.Header.Get("Cookie"); got != "" {
				t.Fatalf("token cookie = %q, want absent", got)
			}
			body, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if errJSON := json.Unmarshal(body, &tokenBody); errJSON != nil {
				t.Fatal(errJSON)
			}
			return sessionKeyJSONResponse(req, `{"access_token":"access","refresh_token":"refresh","expires_in":3600,"account":{"uuid":"acct","email_address":"user@example.com"}}`), nil
		case ProfileURL:
			return sessionKeyJSONResponse(req, `{"account":{"uuid":"acct","email":"user@example.com"}}`), nil
		case RolesURL:
			return sessionKeyJSONResponse(req, `{"roles":[]}`), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}}

	bundle, errExchange := auth.ExchangeSessionKey(context.Background(), sid02, SessionKeyScopeFull)
	if errExchange != nil {
		t.Fatalf("ExchangeSessionKey() error = %v", errExchange)
	}
	if got, want := order, []string{OrganizationsURL, "https://claude.ai/v1/oauth/org-1/authorize", TokenURL, ProfileURL, RolesURL}; !equalStrings(got, want) {
		t.Fatalf("request order = %#v, want %#v", got, want)
	}
	if got := authorizeBody["scope"]; got != ClaudeOAuthScope {
		t.Fatalf("authorize scope = %#v, want %q", got, ClaudeOAuthScope)
	}
	verifier, _ := tokenBody["code_verifier"].(string)
	if verifier == "" || tokenBody["code"] != "auth-code" || tokenBody["redirect_uri"] != PlatformRedirectURI {
		t.Fatalf("token body = %#v", tokenBody)
	}
	hash := sha256.Sum256([]byte(verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(hash[:])
	if got := authorizeBody["code_challenge"]; got != wantChallenge {
		t.Fatalf("authorize code challenge = %#v, want %q", got, wantChallenge)
	}
	if got := bundle.TokenData.OrganizationUUID; got != "org-1" {
		t.Fatalf("organization UUID = %q, want org-1", got)
	}
	if got := bundle.TokenData.OrganizationName; got != "Org One" {
		t.Fatalf("organization name = %q, want Org One", got)
	}
	if len(bundle.DeviceIDs) != ClaudeDevicePoolSize {
		t.Fatalf("device IDs = %#v, want %d entries", bundle.DeviceIDs, ClaudeDevicePoolSize)
	}
}

func TestExchangeSessionKeyUnknownScopeMakesNoNetworkCalls(t *testing.T) {
	called := false
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected network call")
	})}}
	_, errExchange := auth.ExchangeSessionKey(context.Background(), "sk-ant-sid02-valid", SessionKeyScope("unknown"))
	if called {
		t.Fatal("unknown scope made a network call")
	}
	var sessionErr *SessionKeyExchangeError
	if !errors.As(errExchange, &sessionErr) || sessionErr.Code != SessionKeyErrorInvalidInput {
		t.Fatalf("error = %T %v, want invalid_input", errExchange, errExchange)
	}
}

func TestExchangeSessionKeyInvalidInputMakesNoNetworkCalls(t *testing.T) {
	for _, key := range []string{"", "not-a-session-key", "   ", "sk-ant-sid02-\u2003"} {
		t.Run(key, func(t *testing.T) {
			called := false
			auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				called = true
				return nil, errors.New("unexpected network call")
			})}}
			_, errExchange := auth.ExchangeSessionKey(context.Background(), key, SessionKeyScopeFull)
			if called {
				t.Fatal("invalid session key made a network call")
			}
			var sessionErr *SessionKeyExchangeError
			if !errors.As(errExchange, &sessionErr) || sessionErr.Code != SessionKeyErrorInvalidInput {
				t.Fatalf("error = %T %v, want invalid_input", errExchange, errExchange)
			}
		})
	}
}

func TestExchangeSessionKeyAdvisoryLookupFailuresStillSucceed(t *testing.T) {
	const sid02 = "sk-ant-sid02-advisory"
	var authorizeState string
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case OrganizationsURL:
			return sessionKeyJSONResponse(req, `[{"uuid":"org-1"}]`), nil
		case "https://claude.ai/v1/oauth/org-1/authorize":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			authorizeState, _ = payload["state"].(string)
			return sessionKeyJSONResponse(req, `{"redirect_uri":"https://platform.claude.com/oauth/code/callback?code=auth-code&state=`+authorizeState+`"}`), nil
		case TokenURL:
			return sessionKeyJSONResponse(req, `{"access_token":"access","refresh_token":"refresh","expires_in":3600}`), nil
		case ProfileURL, RolesURL:
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{"error":"temporary"}`)), Header: make(http.Header), Request: req}, nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}}
	bundle, errExchange := auth.ExchangeSessionKey(context.Background(), sid02, SessionKeyScopeInference)
	if errExchange != nil {
		t.Fatalf("advisory failures must not fail exchange: %v", errExchange)
	}
	if bundle.TokenData.AccessToken != "access" || bundle.TokenData.OrganizationUUID != "org-1" {
		t.Fatalf("bundle = %#v", bundle.TokenData)
	}
}

func TestExchangeSessionKeyOrganizationFailures(t *testing.T) {
	for _, tt := range []struct {
		name       string
		status     int
		body       string
		wantErrMsg string
	}{
		{name: "non2xx", status: http.StatusUnauthorized, body: `{"error":"sid02-secret"}`},
		{name: "malformed", status: http.StatusOK, body: `{`},
		{name: "empty", status: http.StatusOK, body: `[]`},
		{name: "no uuid", status: http.StatusOK, body: `[{"name":"Org"}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body)), Header: make(http.Header), Request: req}, nil
			})}}
			_, errExchange := auth.ExchangeSessionKey(context.Background(), "sk-ant-sid02-valid", SessionKeyScopeFull)
			var sessionErr *SessionKeyExchangeError
			if !errors.As(errExchange, &sessionErr) || sessionErr.Code != SessionKeyErrorOrganizationLookupFailed {
				t.Fatalf("error = %T %v, want organization_lookup_failed", errExchange, errExchange)
			}
			if strings.Contains(errExchange.Error(), "sid02-secret") {
				t.Fatalf("error leaked upstream body: %v", errExchange)
			}
		})
	}
}

func TestExchangeSessionKeyAuthorizationFailuresDoNotCallToken(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "missing redirect", body: `{}`},
		{name: "invalid json", body: `{`},
		{name: "missing code", body: `{"redirect_uri":"https://platform.claude.com/callback?state=wrong"}`},
		{name: "missing state", body: `{"redirect_uri":"https://platform.claude.com/callback?code=abc"}`},
		{name: "wrong state", body: `{"redirect_uri":"https://platform.claude.com/callback?code=abc&state=wrong"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tokenCalled := false
			auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case OrganizationsURL:
					return sessionKeyJSONResponse(req, `[{"uuid":"org-1"}]`), nil
				case "https://claude.ai/v1/oauth/org-1/authorize":
					if tt.name == "wrong state" {
						return sessionKeyJSONResponse(req, tt.body), nil
					}
					body := tt.body
					if tt.name == "missing code" || tt.name == "missing state" || tt.name == "missing redirect" || tt.name == "invalid json" {
						return sessionKeyJSONResponse(req, body), nil
					}
					return sessionKeyJSONResponse(req, body), nil
				case TokenURL:
					tokenCalled = true
					return sessionKeyJSONResponse(req, `{"access_token":"access","refresh_token":"refresh"}`), nil
				default:
					t.Fatalf("unexpected request %s", req.URL)
					return nil, nil
				}
			})}}
			_, errExchange := auth.ExchangeSessionKey(context.Background(), "sk-ant-sid02-valid", SessionKeyScopeFull)
			var sessionErr *SessionKeyExchangeError
			if !errors.As(errExchange, &sessionErr) || sessionErr.Code != SessionKeyErrorAuthorizationFailed {
				t.Fatalf("error = %T %v, want authorization_failed", errExchange, errExchange)
			}
			if tokenCalled {
				t.Fatal("authorization failure called token endpoint")
			}
		})
	}
}

func TestExchangeSessionKeyTokenFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		code int
	}{
		{name: "non2xx", body: `{"error":"sid02-secret"}`, code: http.StatusUnauthorized},
		{name: "invalid json", body: `{`, code: http.StatusOK},
		{name: "missing access", body: `{"refresh_token":"refresh"}`, code: http.StatusOK},
		{name: "missing refresh", body: `{"access_token":"access"}`, code: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case OrganizationsURL:
					return sessionKeyJSONResponse(req, `[{"uuid":"org-1"}]`), nil
				case "https://claude.ai/v1/oauth/org-1/authorize":
					body, _ := io.ReadAll(req.Body)
					var payload map[string]any
					_ = json.Unmarshal(body, &payload)
					state, _ := payload["state"].(string)
					return sessionKeyJSONResponse(req, `{"redirect_uri":"https://platform.claude.com/callback?code=abc&state=`+state+`"}`), nil
				case TokenURL:
					return &http.Response{StatusCode: tt.code, Body: io.NopCloser(strings.NewReader(tt.body)), Header: make(http.Header), Request: req}, nil
				case ProfileURL, RolesURL:
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: req}, nil
				default:
					t.Fatalf("unexpected request %s", req.URL)
					return nil, nil
				}
			})}}
			_, errExchange := auth.ExchangeSessionKey(context.Background(), "sk-ant-sid02-valid", SessionKeyScopeFull)
			var sessionErr *SessionKeyExchangeError
			if !errors.As(errExchange, &sessionErr) || sessionErr.Code != SessionKeyErrorTokenExchangeFailed {
				t.Fatalf("error = %T %v, want token_exchange_failed", errExchange, errExchange)
			}
			if strings.Contains(errExchange.Error(), "sid02-secret") {
				t.Fatalf("error leaked upstream body: %v", errExchange)
			}
		})
	}
}

func TestParseSessionKeyRedirect(t *testing.T) {
	for _, tt := range []struct {
		name      string
		redirect  string
		wantCode  string
		wantState string
		wantErr   bool
	}{
		{name: "query", redirect: "https://platform.claude.com/callback?code=abc&state=xyz", wantCode: "abc", wantState: "xyz"},
		{name: "fragment query", redirect: "https://platform.claude.com/callback#code=abc&state=xyz", wantCode: "abc", wantState: "xyz"},
		{name: "code fragment state", redirect: "https://platform.claude.com/callback?code=abc#xyz", wantCode: "abc", wantState: "xyz"},
		{name: "missing state", redirect: "https://platform.claude.com/callback?code=abc", wantErr: true},
		{name: "missing code", redirect: "https://platform.claude.com/callback?state=xyz", wantErr: true},
		{name: "empty", redirect: "", wantErr: true},
		{name: "state is preserved exactly", redirect: "https://platform.claude.com/callback?code=abc&state=%20xyz%20", wantCode: "abc", wantState: " xyz ", wantErr: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotCode, gotState, errParse := parseSessionKeyRedirect(tt.redirect)
			if (errParse != nil) != tt.wantErr {
				t.Fatalf("parseSessionKeyRedirect() error = %v, want error=%v", errParse, tt.wantErr)
			}
			if !tt.wantErr && (gotCode != tt.wantCode || gotState != tt.wantState) {
				t.Fatalf("parseSessionKeyRedirect() = %q/%q, want %q/%q", gotCode, gotState, tt.wantCode, tt.wantState)
			}
		})
	}
}

func TestExchangeSessionKeyErrorsDoNotExposeSessionKey(t *testing.T) {
	const sid02 = "sk-ant-sid02-do-not-leak"
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"error":"` + sid02 + `"}`)), Header: make(http.Header), Request: req}, nil
	})}}
	_, errExchange := auth.ExchangeSessionKey(context.Background(), sid02, "")
	if errExchange == nil || strings.Contains(errExchange.Error(), sid02) {
		t.Fatalf("error = %v, must not contain session key", errExchange)
	}
}

func TestSessionKeyResponseCloseErrorsAreLoggedWithoutSessionKey(t *testing.T) {
	const sid02 = "sk-ant-sid02-close-test-secret"
	const accessToken = "fake-access-token-close-secret"
	const refreshToken = "fake-refresh-token-close-secret"
	output := captureSessionKeyLogOutput(t)
	closeError := errors.New("synthetic close failure " + sid02 + " " + accessToken + " " + refreshToken)
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.String() {
		case OrganizationsURL:
			body = `[{"uuid":"org-1"}]`
		case "https://claude.ai/v1/oauth/org-1/authorize":
			body = `{"redirect_uri":"https://platform.claude.com/callback?code=abc&state=state-value"}`
		default:
			t.Fatalf("unexpected request %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &sessionKeyCloseErrorBody{Reader: strings.NewReader(body), Err: closeError},
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}}

	if _, errLookup := auth.lookupSessionKeyOrganization(context.Background(), sid02); errLookup != nil {
		t.Fatalf("lookupSessionKeyOrganization() error = %v", errLookup)
	}
	payload := sessionKeyAuthorizeRequest{State: "state-value"}
	if _, _, errAuthorize := auth.authorizeSessionKey(context.Background(), sid02, "https://claude.ai/v1/oauth/org-1/authorize", payload, "state-value"); errAuthorize != nil {
		t.Fatalf("authorizeSessionKey() error = %v", errAuthorize)
	}

	logOutput := output.String()
	if !strings.Contains(logOutput, "organizations response body") || !strings.Contains(logOutput, "authorize response body") {
		t.Fatalf("close-error logs = %q, want endpoint context", logOutput)
	}
	for _, secret := range []string{sid02, accessToken, refreshToken} {
		if strings.Contains(logOutput, secret) {
			t.Fatalf("close-error logs leaked %q: %q", secret, logOutput)
		}
	}
}

func TestTokenExchangeResponseCloseErrorLogOmitsSecrets(t *testing.T) {
	const sid02 = "sk-ant-sid02-token-close-secret"
	const accessToken = "fake-access-token"
	const refreshToken = "fake-refresh-token"
	output := captureSessionKeyLogOutput(t)
	closeError := errors.New("synthetic close failure " + sid02 + " " + accessToken + " " + refreshToken)
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case TokenURL:
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: &sessionKeyCloseErrorBody{
					Reader: strings.NewReader(`{"access_token":"` + accessToken + `","refresh_token":"` + refreshToken + `","expires_in":3600}`),
					Err:    closeError,
				},
				Header:  make(http.Header),
				Request: req,
			}, nil
		case ProfileURL:
			return sessionKeyJSONResponse(req, `{"account":{"uuid":"account-1","email":"user@example.com"}}`), nil
		case RolesURL:
			return sessionKeyJSONResponse(req, `{"roles":[]}`), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}}

	if _, errExchange := auth.exchangeCodeForTokens(context.Background(), "code", "state", &PKCECodes{CodeVerifier: "verifier"}, PlatformRedirectURI); errExchange != nil {
		t.Fatalf("exchangeCodeForTokens() error = %v", errExchange)
	}
	logOutput := output.String()
	if !strings.Contains(logOutput, "token exchange response body") {
		t.Fatalf("close-error logs = %q, want token endpoint context", logOutput)
	}
	for _, secret := range []string{sid02, accessToken, refreshToken} {
		if strings.Contains(logOutput, secret) {
			t.Fatalf("close-error logs leaked %q: %q", secret, logOutput)
		}
	}
}

func TestControlPlaneResponseCloseErrorLogOmitsSecrets(t *testing.T) {
	const sid02 = "sk-ant-sid02-control-plane-close-secret"
	const accessToken = "fake-control-plane-access-secret"
	const refreshToken = "fake-control-plane-refresh-secret"
	output := captureSessionKeyLogOutput(t)
	closeError := errors.New("synthetic close failure " + sid02 + " " + accessToken + " " + refreshToken)
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: &sessionKeyCloseErrorBody{
				Reader: strings.NewReader(`{"account":{"uuid":"account-1"}}`),
				Err:    closeError,
			},
			Header:  make(http.Header),
			Request: req,
		}, nil
	})}}

	if _, errFetch := auth.fetchOAuthControlPlaneJSON(context.Background(), ProfileURL, accessToken, "profile"); errFetch != nil {
		t.Fatalf("fetchOAuthControlPlaneJSON() error = %v", errFetch)
	}
	logOutput := output.String()
	wantMessage := "failed to close Claude OAuth " + ProfileURL + " response body"
	if !strings.Contains(logOutput, wantMessage) {
		t.Fatalf("close-error logs = %q, want %q", logOutput, wantMessage)
	}
	for _, secret := range []string{sid02, accessToken, refreshToken} {
		if strings.Contains(logOutput, secret) {
			t.Fatalf("close-error logs leaked %q: %q", secret, logOutput)
		}
	}
}

func sessionKeyJSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type sessionKeyCloseErrorBody struct {
	io.Reader
	Err error
}

func (b *sessionKeyCloseErrorBody) Close() error {
	return b.Err
}

func captureSessionKeyLogOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	logger := log.StandardLogger()
	oldOutput := logger.Out
	oldFormatter := logger.Formatter
	oldLevel := logger.Level
	var output bytes.Buffer
	logger.SetOutput(&output)
	logger.SetFormatter(&log.TextFormatter{DisableColors: true, DisableTimestamp: true})
	logger.SetLevel(log.ErrorLevel)
	t.Cleanup(func() {
		logger.SetOutput(oldOutput)
		logger.SetFormatter(oldFormatter)
		logger.SetLevel(oldLevel)
	})
	return &output
}
