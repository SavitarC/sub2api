package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const (
	feishuOAuthCookiePath          = "/api/v1/auth/oauth/feishu"
	feishuOAuthStateCookieName     = "feishu_oauth_state"
	feishuOAuthVerifierCookieName  = "feishu_oauth_verifier"
	feishuOAuthRedirectCookieName  = "feishu_oauth_redirect"
	feishuOAuthIntentCookieName    = "feishu_oauth_intent"
	feishuOAuthBindUserCookieName  = "feishu_oauth_bind_user"
	feishuOAuthAffiliateCookieName = "feishu_oauth_aff_code"
	feishuOAuthCookieMaxAgeSec     = 5 * 60
	feishuOAuthDefaultRedirectTo   = "/dashboard"
	feishuOAuthDefaultFrontendCB   = "/auth/feishu/callback"
	feishuOAuthResponseMaxBytes    = 1 << 20
)

type feishuTokenResponse struct {
	Code                  int    `json:"code"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
	AccessToken           string `json:"access_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Scope                 string `json:"scope"`
	TokenType             string `json:"token_type"`
}

type feishuUserInfo struct {
	Name            string `json:"name"`
	EnglishName     string `json:"en_name"`
	AvatarURL       string `json:"avatar_url"`
	AvatarThumb     string `json:"avatar_thumb"`
	OpenID          string `json:"open_id"`
	UnionID         string `json:"union_id"`
	UserID          string `json:"user_id"`
	Email           string `json:"email"`
	EnterpriseEmail string `json:"enterprise_email"`
	TenantKey       string `json:"tenant_key"`
}

type feishuUserInfoResponse struct {
	Code int            `json:"code"`
	Msg  string         `json:"msg"`
	Data feishuUserInfo `json:"data"`
}

type feishuAPIError struct {
	Operation  string
	StatusCode int
	Code       int
	Message    string
}

func (e *feishuAPIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"feishu %s failed: status=%d code=%d message=%s",
		e.Operation,
		e.StatusCode,
		e.Code,
		truncateLogValue(singleLine(e.Message), 1024),
	)
}

// FeishuOAuthStart starts a browser-bound Feishu OAuth authorization flow.
func (h *AuthHandler) FeishuOAuthStart(c *gin.Context) {
	cfg, err := h.getFeishuOAuthConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	state, err := oauth.GenerateState()
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_STATE_GEN_FAILED", "failed to generate oauth state").WithCause(err))
		return
	}
	browserSessionKey, err := generateOAuthPendingBrowserSession()
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BROWSER_SESSION_GEN_FAILED", "failed to generate oauth browser session").WithCause(err))
		return
	}
	redirectTo := sanitizeFrontendRedirectPath(c.Query("redirect"))
	if redirectTo == "" {
		redirectTo = feishuOAuthDefaultRedirectTo
	}
	secure := isRequestHTTPS(c)
	setFeishuOAuthCookie(c, feishuOAuthStateCookieName, encodeCookieValue(state), secure)
	setFeishuOAuthCookie(c, feishuOAuthRedirectCookieName, encodeCookieValue(redirectTo), secure)
	intent := normalizeOAuthIntent(c.Query("intent"))
	setFeishuOAuthCookie(c, feishuOAuthIntentCookieName, encodeCookieValue(intent), secure)
	affiliateCode := strings.TrimSpace(firstNonEmpty(c.Query("aff_code"), c.Query("aff")))
	if len(affiliateCode) > service.AffiliateCodeMaxLength {
		affiliateCode = ""
	}
	if affiliateCode != "" {
		setFeishuOAuthCookie(c, feishuOAuthAffiliateCookieName, encodeCookieValue(affiliateCode), secure)
	} else {
		clearFeishuOAuthCookie(c, feishuOAuthAffiliateCookieName, secure)
	}
	captureOAuthPromoCode(c, secure)
	setOAuthPendingBrowserCookie(c, browserSessionKey, secure)
	clearOAuthPendingSessionCookie(c, secure)
	if intent == oauthIntentBindCurrentUser {
		bindCookie, err := h.buildOAuthBindUserCookieFromContext(c)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		setFeishuOAuthCookie(c, feishuOAuthBindUserCookieName, encodeCookieValue(bindCookie), secure)
	} else {
		clearFeishuOAuthCookie(c, feishuOAuthBindUserCookieName, secure)
	}

	codeChallenge := ""
	if cfg.UsePKCE {
		verifier, err := oauth.GenerateCodeVerifier()
		if err != nil {
			response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_PKCE_GEN_FAILED", "failed to generate pkce verifier").WithCause(err))
			return
		}
		codeChallenge = oauth.GenerateCodeChallenge(verifier)
		setFeishuOAuthCookie(c, feishuOAuthVerifierCookieName, encodeCookieValue(verifier), secure)
	}
	authorizeURL, err := buildFeishuAuthorizeURL(cfg, state, codeChallenge)
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build oauth authorization url").WithCause(err))
		return
	}
	c.Redirect(http.StatusFound, authorizeURL)
}

// FeishuOAuthCallback exchanges the one-time code and enters the generic
// browser-bound pending identity login/create/bind flow.
func (h *AuthHandler) FeishuOAuthCallback(c *gin.Context) {
	cfg, err := h.getFeishuOAuthConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	frontendCallback := strings.TrimSpace(cfg.FrontendRedirectURL)
	if frontendCallback == "" {
		frontendCallback = feishuOAuthDefaultFrontendCB
	}
	secure := isRequestHTTPS(c)
	state := strings.TrimSpace(c.Query("state"))
	expectedState, stateErr := readCookieDecoded(c, feishuOAuthStateCookieName)
	if stateErr != nil || expectedState == "" || state == "" || state != expectedState {
		// Do not clear the legitimate browser flow when an unrelated callback
		// arrives with a missing or mismatched state.
		redirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth state", "")
		return
	}
	clearFlowCookies := func() {
		clearFeishuOAuthCookie(c, feishuOAuthStateCookieName, secure)
		clearFeishuOAuthCookie(c, feishuOAuthVerifierCookieName, secure)
		clearFeishuOAuthCookie(c, feishuOAuthRedirectCookieName, secure)
		clearFeishuOAuthCookie(c, feishuOAuthIntentCookieName, secure)
		clearFeishuOAuthCookie(c, feishuOAuthBindUserCookieName, secure)
		clearFeishuOAuthCookie(c, feishuOAuthAffiliateCookieName, secure)
		clearOAuthPromoCodeCookie(c, secure)
	}
	redirectError := func(code, message, description string) {
		clearFlowCookies()
		redirectOAuthError(c, frontendCallback, code, message, description)
	}
	redirectFrontend := func() {
		clearFlowCookies()
		redirectToFrontendCallback(c, frontendCallback)
	}
	if providerErr := strings.TrimSpace(c.Query("error")); providerErr != "" {
		redirectError("provider_error", providerErr, c.Query("error_description"))
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		redirectError("missing_params", "missing code", "")
		return
	}
	redirectTo, _ := readCookieDecoded(c, feishuOAuthRedirectCookieName)
	redirectTo = sanitizeFrontendRedirectPath(redirectTo)
	if redirectTo == "" {
		redirectTo = feishuOAuthDefaultRedirectTo
	}
	browserSessionKey, _ := readOAuthPendingBrowserCookie(c)
	if strings.TrimSpace(browserSessionKey) == "" {
		redirectError("missing_browser_session", "missing oauth browser session", "")
		return
	}
	intent, _ := readCookieDecoded(c, feishuOAuthIntentCookieName)
	intent = normalizeOAuthIntent(intent)
	verifier := ""
	if cfg.UsePKCE {
		verifier, _ = readCookieDecoded(c, feishuOAuthVerifierCookieName)
		if verifier == "" {
			redirectError("missing_verifier", "missing pkce verifier", "")
			return
		}
	}

	token, err := feishuExchangeCode(c.Request.Context(), cfg, code, verifier)
	if err != nil {
		slog.Warn("feishu oauth token exchange failed", "error", err)
		redirectError("token_exchange_failed", "failed to exchange oauth code", "")
		return
	}
	profile, err := feishuFetchUserInfo(c.Request.Context(), cfg, token.AccessToken)
	if err != nil {
		slog.Warn("feishu oauth userinfo failed", "error", err)
		redirectError("userinfo_failed", "failed to fetch user info", "")
		return
	}

	providerSubject := strings.TrimSpace(profile.OpenID)
	if providerSubject == "" {
		redirectError("userinfo_invalid", "feishu user info missing stable identity", "")
		return
	}
	identity := service.PendingAuthIdentityKey{
		ProviderType:    "feishu",
		ProviderKey:     "feishu:" + strings.TrimSpace(cfg.ClientID),
		ProviderSubject: providerSubject,
	}
	syntheticEmail := feishuSyntheticEmail(cfg.ClientID, providerSubject)
	compatEmail := firstNonEmpty(profile.EnterpriseEmail, profile.Email)
	username := feishuSyntheticUsername(cfg.ClientID, providerSubject)
	displayName := firstNonEmpty(profile.Name, profile.EnglishName, username)
	claims := map[string]any{
		"email":                  syntheticEmail,
		"username":               username,
		"open_id":                strings.TrimSpace(profile.OpenID),
		"union_id":               strings.TrimSpace(profile.UnionID),
		"user_id":                strings.TrimSpace(profile.UserID),
		"tenant_key":             strings.TrimSpace(profile.TenantKey),
		"suggested_display_name": displayName,
		"suggested_avatar_url":   strings.TrimSpace(profile.AvatarURL),
	}
	if compatEmail != "" {
		claims["compat_email"] = compatEmail
	}

	if intent == oauthIntentBindCurrentUser {
		targetUserID, err := h.readOAuthBindUserIDFromCookie(c, feishuOAuthBindUserCookieName)
		if err != nil {
			redirectError("invalid_state", "invalid oauth bind target", "")
			return
		}
		if err := h.createOAuthPendingSession(c, oauthPendingSessionPayload{
			Intent: oauthIntentBindCurrentUser, Identity: identity, TargetUserID: &targetUserID,
			ResolvedEmail: syntheticEmail, RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey,
			UpstreamIdentityClaims: claims, CompletionResponse: map[string]any{"redirect": redirectTo},
		}); err != nil {
			redirectError("session_error", "failed to continue oauth bind", "")
			return
		}
		redirectFrontend()
		return
	}

	existingIdentityUser, err := h.findOAuthIdentityUser(c.Request.Context(), identity)
	if err != nil {
		redirectError("session_error", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}
	if existingIdentityUser != nil {
		if err := h.createOAuthPendingSession(c, oauthPendingSessionPayload{
			Intent: oauthIntentLogin, Identity: identity, TargetUserID: &existingIdentityUser.ID,
			ResolvedEmail: existingIdentityUser.Email, RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey,
			UpstreamIdentityClaims: claims, CompletionResponse: map[string]any{"redirect": redirectTo},
		}); err != nil {
			redirectError("session_error", "failed to continue oauth login", "")
			return
		}
		redirectFrontend()
		return
	}

	compatEmailUser, err := h.findFeishuCompatEmailUser(c.Request.Context(), compatEmail)
	if err != nil {
		redirectError("session_error", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}
	emailVerificationRequired := h.authService != nil && h.authService.IsEmailVerifyEnabled(c.Request.Context())
	forceEmailOnSignup := h.isForceEmailOnThirdPartySignup(c.Request.Context())
	signupBlocked := h.settingSvc != nil && !h.settingSvc.IsRegistrationEnabled(c.Request.Context())
	if compatEmailUser == nil && !emailVerificationRequired && !forceEmailOnSignup && !signupBlocked {
		if err := h.ensureBackendModeAllowsNewUserLogin(c.Request.Context()); err != nil {
			redirectError("session_error", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
		user, registerErr := h.authService.LoginOrRegisterOAuthUserWithPromoCode(
			c.Request.Context(), syntheticEmail, username, "", readFeishuOAuthAffiliateCode(c), readOAuthPromoCode(c), "feishu",
		)
		if registerErr == nil {
			if err := applyPendingOAuthBinding(c.Request.Context(), h.entClient(), h.authService, h.userService, &dbent.PendingAuthSession{
				Intent: oauthIntentLogin, ProviderType: identity.ProviderType, ProviderKey: identity.ProviderKey,
				ProviderSubject: identity.ProviderSubject, ResolvedEmail: syntheticEmail, UpstreamIdentityClaims: claims,
			}, nil, &user.ID, true, false); err != nil {
				redirectError("session_error", "failed to bind oauth identity", "")
				return
			}
			if err := h.createOAuthPendingSession(c, oauthPendingSessionPayload{
				Intent: oauthIntentLogin, Identity: identity, TargetUserID: &user.ID,
				ResolvedEmail: syntheticEmail, RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey,
				UpstreamIdentityClaims: claims, CompletionResponse: map[string]any{"redirect": redirectTo},
			}); err != nil {
				redirectError("session_error", "failed to continue oauth login", "")
				return
			}
			redirectFrontend()
			return
		}
		if !errors.Is(registerErr, service.ErrOAuthInvitationRequired) && !errors.Is(registerErr, service.ErrRegistrationDisabled) {
			redirectError("session_error", infraerrors.Reason(registerErr), infraerrors.Message(registerErr))
			return
		}
		if errors.Is(registerErr, service.ErrRegistrationDisabled) {
			signupBlocked = true
		}
		if errors.Is(registerErr, service.ErrOAuthInvitationRequired) {
			// Feishu uses the shared pending account endpoint, whose request carries
			// the invitation code together with a real local email/password.
			forceEmailOnSignup = true
		}
	}
	if err := h.createFeishuOAuthChoicePendingSession(c, identity, syntheticEmail, redirectTo, browserSessionKey, claims,
		compatEmail, compatEmailUser, emailVerificationRequired, forceEmailOnSignup, signupBlocked); err != nil {
		redirectError("session_error", "failed to continue oauth login", "")
		return
	}
	redirectFrontend()
}

func (h *AuthHandler) getFeishuOAuthConfig(ctx context.Context) (config.FeishuConnectConfig, error) {
	if h != nil && h.settingSvc != nil {
		return h.settingSvc.GetFeishuConnectOAuthConfig(ctx)
	}
	if h == nil || h.cfg == nil {
		return config.FeishuConnectConfig{}, infraerrors.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}
	if !h.cfg.Feishu.Enabled {
		return config.FeishuConnectConfig{}, infraerrors.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	}
	return h.cfg.Feishu, nil
}

func buildFeishuAuthorizeURL(cfg config.FeishuConnectConfig, state, codeChallenge string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.AuthorizeURL))
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", strings.TrimSpace(cfg.ClientID))
	q.Set("response_type", "code")
	q.Set("redirect_uri", strings.TrimSpace(cfg.RedirectURL))
	q.Set("state", strings.TrimSpace(state))
	if scope := strings.TrimSpace(cfg.Scopes); scope != "" {
		q.Set("scope", scope)
	}
	if codeChallenge = strings.TrimSpace(codeChallenge); codeChallenge != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func feishuExchangeCode(ctx context.Context, cfg config.FeishuConnectConfig, code, codeVerifier string) (*feishuTokenResponse, error) {
	payload := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     strings.TrimSpace(cfg.ClientID),
		"client_secret": strings.TrimSpace(cfg.ClientSecret),
		"code":          strings.TrimSpace(code),
		"redirect_uri":  strings.TrimSpace(cfg.RedirectURL),
	}
	if verifier := strings.TrimSpace(codeVerifier); verifier != "" {
		payload["code_verifier"] = verifier
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(cfg.TokenURL), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, feishuOAuthResponseMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	var token feishuTokenResponse
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, &feishuAPIError{Operation: "token exchange", StatusCode: resp.StatusCode, Message: "invalid json response"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || token.Code != 0 {
		return nil, &feishuAPIError{Operation: "token exchange", StatusCode: resp.StatusCode, Code: token.Code, Message: firstNonEmpty(token.ErrorDescription, token.Error)}
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, &feishuAPIError{Operation: "token exchange", StatusCode: resp.StatusCode, Message: "missing access_token"}
	}
	if strings.TrimSpace(token.TokenType) == "" {
		token.TokenType = "Bearer"
	}
	return &token, nil
}

func feishuFetchUserInfo(ctx context.Context, cfg config.FeishuConnectConfig, accessToken string) (*feishuUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(cfg.UserInfoURL), nil)
	if err != nil {
		return nil, fmt.Errorf("create userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request userinfo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, feishuOAuthResponseMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("read userinfo response: %w", err)
	}
	var result feishuUserInfoResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, &feishuAPIError{Operation: "userinfo", StatusCode: resp.StatusCode, Message: "invalid json response"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || result.Code != 0 {
		return nil, &feishuAPIError{Operation: "userinfo", StatusCode: resp.StatusCode, Code: result.Code, Message: result.Msg}
	}
	if strings.TrimSpace(result.Data.OpenID) == "" {
		return nil, &feishuAPIError{Operation: "userinfo", StatusCode: resp.StatusCode, Message: "missing stable identity"}
	}
	return &result.Data, nil
}

func feishuSyntheticIdentitySlug(clientID, subject string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(clientID) + "\x00" + strings.TrimSpace(subject)))
	return hex.EncodeToString(digest[:12])
}

func feishuSyntheticEmail(clientID, subject string) string {
	return "feishu-" + feishuSyntheticIdentitySlug(clientID, subject) + service.FeishuConnectSyntheticEmailDomain
}

func feishuSyntheticUsername(clientID, subject string) string {
	return "feishu_" + feishuSyntheticIdentitySlug(clientID, subject)[:16]
}

func readFeishuOAuthAffiliateCode(c *gin.Context) string {
	code, err := readCookieDecoded(c, feishuOAuthAffiliateCookieName)
	if err != nil {
		return ""
	}
	code = strings.TrimSpace(code)
	if len(code) > service.AffiliateCodeMaxLength {
		return ""
	}
	return code
}

func (h *AuthHandler) findFeishuCompatEmailUser(ctx context.Context, email string) (*dbent.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" ||
		strings.HasSuffix(email, service.LinuxDoConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, service.OIDCConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, service.WeChatConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, service.DingTalkConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, service.FeishuConnectSyntheticEmailDomain) {
		return nil, nil
	}
	user, err := findUserByNormalizedEmail(ctx, h.entClient(), email)
	if errors.Is(err, service.ErrUserNotFound) {
		return nil, nil
	}
	return user, err
}

func (h *AuthHandler) createFeishuOAuthChoicePendingSession(
	c *gin.Context,
	identity service.PendingAuthIdentityKey,
	syntheticEmail, redirectTo, browserSessionKey string,
	claims map[string]any,
	compatEmail string,
	compatEmailUser *dbent.User,
	emailVerificationRequired, forceEmailOnSignup, signupBlocked bool,
) error {
	completion := map[string]any{
		"step": oauthPendingChoiceStep, "adoption_required": true, "redirect": redirectTo,
		"email": syntheticEmail, "resolved_email": syntheticEmail,
		"existing_account_email": "", "existing_account_bindable": false,
		"create_account_allowed": !signupBlocked, "force_email_on_signup": forceEmailOnSignup,
		"choice_reason": "third_party_signup",
	}
	resolvedEmail := syntheticEmail
	if strings.TrimSpace(compatEmail) != "" {
		completion["compat_email"] = strings.TrimSpace(compatEmail)
	}
	var targetUserID *int64
	if compatEmailUser != nil {
		resolvedEmail = strings.TrimSpace(compatEmailUser.Email)
		targetUserID = &compatEmailUser.ID
		completion["email"] = resolvedEmail
		completion["existing_account_email"] = resolvedEmail
		completion["existing_account_bindable"] = true
		completion["create_account_allowed"] = false
		completion["choice_reason"] = "compat_email_match"
	}
	if (emailVerificationRequired || forceEmailOnSignup) && compatEmailUser == nil {
		completion["step"] = "create_account_required"
		completion["email_binding_required"] = true
		completion["force_email_on_signup"] = true
		completion["choice_reason"] = "email_verification_required"
		delete(completion, "email")
		delete(completion, "resolved_email")
		resolvedEmail = ""
	}
	if signupBlocked {
		completion["step"] = "bind_login_required"
		completion["existing_account_bindable"] = true
		completion["create_account_allowed"] = false
		completion["choice_reason"] = "signup_blocked_redirect_to_bind"
	}
	return h.createOAuthPendingSession(c, oauthPendingSessionPayload{
		Intent: oauthIntentLogin, Identity: identity, TargetUserID: targetUserID, ResolvedEmail: resolvedEmail,
		RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey, UpstreamIdentityClaims: claims,
		CompletionResponse: completion,
	})
}

func setFeishuOAuthCookie(c *gin.Context, name, value string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: value, Path: feishuOAuthCookiePath, MaxAge: feishuOAuthCookieMaxAgeSec,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func clearFeishuOAuthCookie(c *gin.Context, name string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: "", Path: feishuOAuthCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}
