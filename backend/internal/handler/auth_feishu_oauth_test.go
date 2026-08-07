package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/authidentity"
	"github.com/Wei-Shaw/sub2api/ent/pendingauthsession"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFeishuOAuthCallback_MockedFreshAccountCompletesTokenAndIdentity(t *testing.T) {
	server := newMockFeishuOAuthServer(t, "ou_mock_fresh", "fresh@example.com")
	defer server.Close()

	handler, client := newFeishuOAuthHandlerAndClient(t, false, mockFeishuConfig(server.URL))
	t.Cleanup(func() { _ = client.Close() })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = newFeishuCallbackRequest("code-fresh", "state-fresh", "browser-fresh")

	handler.FeishuOAuthCallback(c)

	require.Equal(t, http.StatusFound, recorder.Code)
	require.Equal(t, "/auth/feishu/callback", recorder.Header().Get("Location"))
	sessionCookie := findCookie(recorder.Result().Cookies(), oauthPendingSessionCookieName)
	require.NotNil(t, sessionCookie)

	ctx := context.Background()
	session, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.SessionTokenEQ(decodeCookieValueForTest(t, sessionCookie.Value))).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, oauthIntentLogin, session.Intent)
	require.NotNil(t, session.TargetUserID)
	require.Equal(t, "feishu", session.ProviderType)
	require.Equal(t, "feishu:cli_mock", session.ProviderKey)
	require.Equal(t, "ou_mock_fresh", session.ProviderSubject)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("feishu"),
			authidentity.ProviderKeyEQ("feishu:cli_mock"),
			authidentity.ProviderSubjectEQ("ou_mock_fresh"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, identity.UserID, *session.TargetUserID)

	userEntity, err := client.User.Get(ctx, identity.UserID)
	require.NoError(t, err)
	require.Equal(t, feishuSyntheticEmail("cli_mock", "ou_mock_fresh"), userEntity.Email)
	require.Equal(t, "feishu", userEntity.SignupSource)

	exchangeRecorder := httptest.NewRecorder()
	exchangeContext, _ := gin.CreateTestContext(exchangeRecorder)
	exchangeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	exchangeRequest.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: sessionCookie.Value})
	exchangeRequest.AddCookie(encodedCookie(oauthPendingBrowserCookieName, "browser-fresh"))
	exchangeContext.Request = exchangeRequest

	handler.ExchangePendingOAuthCompletion(exchangeContext)

	require.Equal(t, http.StatusOK, exchangeRecorder.Code)
	payload := decodeJSONResponseData(t, exchangeRecorder)
	accessToken, ok := payload["access_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, accessToken)
	require.NotEmpty(t, payload["refresh_token"])
	require.Equal(t, "/dashboard", payload["redirect"])
	claims, err := handler.authService.ValidateToken(accessToken)
	require.NoError(t, err)
	require.Equal(t, identity.UserID, claims.UserID)

	consumedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, consumedSession.ConsumedAt)
	for _, name := range []string{
		feishuOAuthStateCookieName,
		feishuOAuthVerifierCookieName,
		feishuOAuthRedirectCookieName,
		feishuOAuthIntentCookieName,
		feishuOAuthAffiliateCookieName,
	} {
		requireCookieCleared(t, recorder, name)
	}
}

func TestFeishuOAuthCallback_FreshAccountRequiresMatchingBrowserSession(t *testing.T) {
	server := newMockFeishuOAuthServer(t, "ou_mock_browser_bound", "fresh@example.com")
	defer server.Close()

	handler, client := newFeishuOAuthHandlerAndClient(t, false, mockFeishuConfig(server.URL))
	t.Cleanup(func() { _ = client.Close() })

	callbackRecorder := httptest.NewRecorder()
	callbackContext, _ := gin.CreateTestContext(callbackRecorder)
	callbackContext.Request = newFeishuCallbackRequest("code-browser", "state-browser", "browser-owner")
	handler.FeishuOAuthCallback(callbackContext)

	sessionCookie := findCookie(callbackRecorder.Result().Cookies(), oauthPendingSessionCookieName)
	require.NotNil(t, sessionCookie)
	sessionToken := decodeCookieValueForTest(t, sessionCookie.Value)

	exchangeRecorder := httptest.NewRecorder()
	exchangeContext, _ := gin.CreateTestContext(exchangeRecorder)
	exchangeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	exchangeRequest.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: sessionCookie.Value})
	exchangeRequest.AddCookie(encodedCookie(oauthPendingBrowserCookieName, "browser-attacker"))
	exchangeContext.Request = exchangeRequest
	handler.ExchangePendingOAuthCompletion(exchangeContext)

	require.NotEqual(t, http.StatusOK, exchangeRecorder.Code)
	require.NotContains(t, exchangeRecorder.Body.String(), "access_token")
	session, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.SessionTokenEQ(sessionToken)).
		Only(context.Background())
	require.NoError(t, err)
	require.Nil(t, session.ConsumedAt)
}

func TestFeishuOAuthCallback_FreshAccountBindsAffiliateFromStartCookie(t *testing.T) {
	server := newMockFeishuOAuthServer(t, "ou_mock_affiliate", "fresh@example.com")
	defer server.Close()

	affiliateRepo := newOAuthEmailAffiliateRepoStub(map[string]int64{"AFF123": 1001})
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: map[string]string{
			service.SettingKeyAffiliateEnabled: "true",
		},
		affiliateFactory: func(_ *dbent.Client, settingSvc *service.SettingService) *service.AffiliateService {
			return service.NewAffiliateService(affiliateRepo, settingSvc, nil, nil)
		},
	})
	t.Cleanup(func() { _ = client.Close() })
	handler.settingSvc = nil
	handler.cfg = &config.Config{
		JWT: config.JWTConfig{
			Secret:                   "test-secret",
			ExpireHour:               1,
			AccessTokenExpireMinutes: 60,
			RefreshTokenExpireDays:   7,
		},
		Feishu: mockFeishuConfig(server.URL),
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = newFeishuCallbackRequest("code-affiliate", "state-affiliate", "browser-affiliate")
	c.Request.AddCookie(encodedCookie(feishuOAuthAffiliateCookieName, "AFF123"))
	handler.FeishuOAuthCallback(c)

	require.Equal(t, http.StatusFound, recorder.Code)
	require.Len(t, affiliateRepo.bindCalls, 1)
	require.Equal(t, int64(1001), affiliateRepo.bindCalls[0].inviterID)
	require.Positive(t, affiliateRepo.bindCalls[0].userID)
	requireCookieCleared(t, recorder, feishuOAuthAffiliateCookieName)
}

func TestFeishuOAuthCallback_MockedExistingIdentityExchangesPendingForToken(t *testing.T) {
	server := newMockFeishuOAuthServer(t, "ou_mock_existing", "existing@example.com")
	defer server.Close()

	handler, client := newFeishuOAuthHandlerAndClient(t, false, mockFeishuConfig(server.URL))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	existingUser, err := client.User.Create().
		SetEmail("existing@example.com").
		SetUsername("existing-feishu-user").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AuthIdentity.Create().
		SetUserID(existingUser.ID).
		SetProviderType("feishu").
		SetProviderKey("feishu:cli_mock").
		SetProviderSubject("ou_mock_existing").
		SetMetadata(map[string]any{"open_id": "ou_mock_existing"}).
		Save(ctx)
	require.NoError(t, err)

	callbackRecorder := httptest.NewRecorder()
	callbackContext, _ := gin.CreateTestContext(callbackRecorder)
	callbackContext.Request = newFeishuCallbackRequest("code-existing", "state-existing", "browser-existing")

	handler.FeishuOAuthCallback(callbackContext)

	require.Equal(t, http.StatusFound, callbackRecorder.Code)
	require.Equal(t, "/auth/feishu/callback", callbackRecorder.Header().Get("Location"))
	sessionCookie := findCookie(callbackRecorder.Result().Cookies(), oauthPendingSessionCookieName)
	require.NotNil(t, sessionCookie)

	session, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.SessionTokenEQ(decodeCookieValueForTest(t, sessionCookie.Value))).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, oauthIntentLogin, session.Intent)
	require.NotNil(t, session.TargetUserID)
	require.Equal(t, existingUser.ID, *session.TargetUserID)
	require.Equal(t, "feishu", session.ProviderType)
	require.Equal(t, "feishu:cli_mock", session.ProviderKey)
	require.Equal(t, "ou_mock_existing", session.ProviderSubject)

	exchangeRecorder := httptest.NewRecorder()
	exchangeContext, _ := gin.CreateTestContext(exchangeRecorder)
	exchangeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	exchangeRequest.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: sessionCookie.Value})
	exchangeRequest.AddCookie(encodedCookie(oauthPendingBrowserCookieName, "browser-existing"))
	exchangeContext.Request = exchangeRequest

	handler.ExchangePendingOAuthCompletion(exchangeContext)

	require.Equal(t, http.StatusOK, exchangeRecorder.Code)
	payload := decodeJSONResponseData(t, exchangeRecorder)
	accessToken, ok := payload["access_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, accessToken)
	require.NotEmpty(t, payload["refresh_token"])
	require.Equal(t, "/dashboard", payload["redirect"])

	claims, err := handler.authService.ValidateToken(accessToken)
	require.NoError(t, err)
	require.Equal(t, existingUser.ID, claims.UserID)

	consumedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, consumedSession.ConsumedAt)
}

func TestFeishuOAuthCallback_CompatEmailChoiceCannotBindThroughExchange(t *testing.T) {
	server := newMockFeishuOAuthServer(t, "ou_mock_compat", "owner@example.com")
	defer server.Close()

	handler, client := newFeishuOAuthHandlerAndClient(t, false, mockFeishuConfig(server.URL))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	existingUser, err := client.User.Create().
		SetEmail("owner@example.com").
		SetUsername("existing-owner").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	callbackRecorder := httptest.NewRecorder()
	callbackContext, _ := gin.CreateTestContext(callbackRecorder)
	callbackContext.Request = newFeishuCallbackRequest("code-compat", "state-compat", "browser-compat")
	handler.FeishuOAuthCallback(callbackContext)

	require.Equal(t, http.StatusFound, callbackRecorder.Code)
	sessionCookie := findCookie(callbackRecorder.Result().Cookies(), oauthPendingSessionCookieName)
	require.NotNil(t, sessionCookie)
	session, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.SessionTokenEQ(decodeCookieValueForTest(t, sessionCookie.Value))).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, session.TargetUserID)
	require.Equal(t, existingUser.ID, *session.TargetUserID)
	completion, ok := readCompletionResponse(session.LocalFlowState)
	require.True(t, ok)
	require.Equal(t, oauthPendingChoiceStep, completion["step"])
	require.Equal(t, false, completion["create_account_allowed"])

	exchangeRecorder := httptest.NewRecorder()
	exchangeContext, _ := gin.CreateTestContext(exchangeRecorder)
	exchangeRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/oauth/pending/exchange",
		bytes.NewBufferString(`{"adopt_display_name":false,"adopt_avatar":false}`),
	)
	exchangeRequest.Header.Set("Content-Type", "application/json")
	exchangeRequest.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: sessionCookie.Value})
	exchangeRequest.AddCookie(encodedCookie(oauthPendingBrowserCookieName, "browser-compat"))
	exchangeContext.Request = exchangeRequest
	handler.ExchangePendingOAuthCompletion(exchangeContext)

	require.Equal(t, http.StatusOK, exchangeRecorder.Code)
	payload := decodeJSONResponseData(t, exchangeRecorder)
	require.Equal(t, oauthPendingChoiceStep, payload["step"])
	require.Equal(t, false, payload["create_account_allowed"])
	require.NotContains(t, payload, "access_token")

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("feishu"),
			authidentity.ProviderKeyEQ("feishu:cli_mock"),
			authidentity.ProviderSubjectEQ("ou_mock_compat"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)
	decisionCount, err := client.IdentityAdoptionDecision.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, decisionCount)
	storedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.Nil(t, storedSession.ConsumedAt)
}

func TestFeishuOAuthCallback_MockedCurrentUserBindCompletesIdentityOwnership(t *testing.T) {
	server := newMockFeishuOAuthServer(t, "ou_mock_bind", "bind@example.com")
	defer server.Close()

	handler, client := newFeishuOAuthHandlerAndClient(t, false, mockFeishuConfig(server.URL))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	currentUser, err := client.User.Create().
		SetEmail("current@example.com").
		SetUsername("current-feishu-user").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	callbackRecorder := httptest.NewRecorder()
	callbackContext, _ := gin.CreateTestContext(callbackRecorder)
	callbackRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/feishu/callback?code=code-bind&state=state-bind", nil)
	callbackRequest.AddCookie(encodedCookie(feishuOAuthStateCookieName, "state-bind"))
	callbackRequest.AddCookie(encodedCookie(feishuOAuthRedirectCookieName, "/settings/profile"))
	callbackRequest.AddCookie(encodedCookie(feishuOAuthVerifierCookieName, "verifier-bind"))
	callbackRequest.AddCookie(encodedCookie(feishuOAuthIntentCookieName, oauthIntentBindCurrentUser))
	callbackRequest.AddCookie(encodedCookie(feishuOAuthBindUserCookieName, buildEncodedOAuthBindUserCookie(t, currentUser.ID, "test-secret")))
	callbackRequest.AddCookie(encodedCookie(oauthPendingBrowserCookieName, "browser-bind"))
	callbackContext.Request = callbackRequest

	handler.FeishuOAuthCallback(callbackContext)

	require.Equal(t, http.StatusFound, callbackRecorder.Code)
	require.Equal(t, "/auth/feishu/callback", callbackRecorder.Header().Get("Location"))
	sessionCookie := findCookie(callbackRecorder.Result().Cookies(), oauthPendingSessionCookieName)
	require.NotNil(t, sessionCookie)

	session, err := client.PendingAuthSession.Query().
		Where(pendingauthsession.SessionTokenEQ(decodeCookieValueForTest(t, sessionCookie.Value))).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, oauthIntentBindCurrentUser, session.Intent)
	require.NotNil(t, session.TargetUserID)
	require.Equal(t, currentUser.ID, *session.TargetUserID)
	require.Equal(t, "feishu", session.ProviderType)
	require.Equal(t, "feishu:cli_mock", session.ProviderKey)
	require.Equal(t, "ou_mock_bind", session.ProviderSubject)

	previewRecorder := httptest.NewRecorder()
	previewContext, _ := gin.CreateTestContext(previewRecorder)
	previewRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/exchange", nil)
	previewRequest.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: sessionCookie.Value})
	previewRequest.AddCookie(encodedCookie(oauthPendingBrowserCookieName, "browser-bind"))
	previewContext.Request = previewRequest
	handler.ExchangePendingOAuthCompletion(previewContext)
	require.Equal(t, http.StatusOK, previewRecorder.Code)
	preview := decodeJSONResponseData(t, previewRecorder)
	require.Equal(t, true, preview["adoption_required"])

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("feishu"),
			authidentity.ProviderKeyEQ("feishu:cli_mock"),
			authidentity.ProviderSubjectEQ("ou_mock_bind"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	finalizeRecorder := httptest.NewRecorder()
	finalizeContext, _ := gin.CreateTestContext(finalizeRecorder)
	finalizeRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/oauth/pending/exchange",
		bytes.NewBufferString(`{"adopt_display_name":false,"adopt_avatar":false}`),
	)
	finalizeRequest.Header.Set("Content-Type", "application/json")
	finalizeRequest.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: sessionCookie.Value})
	finalizeRequest.AddCookie(encodedCookie(oauthPendingBrowserCookieName, "browser-bind"))
	finalizeContext.Request = finalizeRequest
	handler.ExchangePendingOAuthCompletion(finalizeContext)

	require.Equal(t, http.StatusOK, finalizeRecorder.Code)
	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("feishu"),
			authidentity.ProviderKeyEQ("feishu:cli_mock"),
			authidentity.ProviderSubjectEQ("ou_mock_bind"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, currentUser.ID, identity.UserID)
	require.Equal(t, "ou_mock_bind", identity.Metadata["open_id"])

	storedUser, err := client.User.Get(ctx, currentUser.ID)
	require.NoError(t, err)
	require.Equal(t, "current-feishu-user", storedUser.Username)

	consumedSession, err := client.PendingAuthSession.Get(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, consumedSession.ConsumedAt)
}

func TestFeishuOAuthClient_MockedAccount(t *testing.T) {
	const verifier = "mock-verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/v3/token":
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "application/json; charset=utf-8", r.Header.Get("Content-Type"))
			require.Empty(t, r.Header.Get("Authorization"))
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "authorization_code", body["grant_type"])
			require.Equal(t, "cli_mock", body["client_id"])
			require.Equal(t, "secret_mock", body["client_secret"])
			require.Equal(t, "code_mock", body["code"])
			require.Equal(t, "https://api.example.com/api/v1/auth/oauth/feishu/callback", body["redirect_uri"])
			require.Equal(t, verifier, body["code_verifier"])
			_, _ = w.Write([]byte(`{"code":0,"access_token":"u-token-mock","expires_in":7200,"refresh_token":"r-token-mock","token_type":"Bearer"}`))
		case "/open-apis/authen/v1/user_info":
			require.Equal(t, http.MethodGet, r.Method)
			require.Equal(t, "Bearer u-token-mock", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"name":"飞书 Mock 用户","avatar_url":"https://example.com/mock.png","open_id":"ou_mock_001","union_id":"on_mock_001","user_id":"mock-user-id","tenant_key":"mock-tenant","email":"mock@example.com"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := mockFeishuConfig(server.URL)
	token, err := feishuExchangeCode(context.Background(), cfg, "code_mock", verifier)
	require.NoError(t, err)
	require.Equal(t, "u-token-mock", token.AccessToken)
	require.Equal(t, int64(7200), token.ExpiresIn)

	profile, err := feishuFetchUserInfo(context.Background(), cfg, token.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "飞书 Mock 用户", profile.Name)
	require.Equal(t, "ou_mock_001", profile.OpenID)
	require.Equal(t, "on_mock_001", profile.UnionID)
	require.Equal(t, "mock@example.com", profile.Email)
}

func TestFeishuOAuthClient_RejectsHTTP200BusinessErrors(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":20003,"error":"invalid_grant","error_description":"authorization code expired"}`))
		}))
		defer server.Close()

		_, err := feishuExchangeCode(context.Background(), mockFeishuConfig(server.URL), "expired", "verifier")
		require.Error(t, err)
		var apiErr *feishuAPIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, 20003, apiErr.Code)
	})

	t.Run("userinfo", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":20005,"msg":"invalid user access token"}`))
		}))
		defer server.Close()

		_, err := feishuFetchUserInfo(context.Background(), mockFeishuConfig(server.URL), "invalid")
		require.Error(t, err)
		var apiErr *feishuAPIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, 20005, apiErr.Code)
	})
}

func TestFeishuOAuthClient_RequiresOpenID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"name":"飞书 Mock 用户","union_id":"on_mock_001"}}`))
	}))
	defer server.Close()

	_, err := feishuFetchUserInfo(context.Background(), mockFeishuConfig(server.URL), "token")
	require.Error(t, err)
}

func TestFeishuAPIError_TruncatesUpstreamMessage(t *testing.T) {
	message := (&feishuAPIError{
		Operation:  "token exchange",
		StatusCode: http.StatusBadRequest,
		Code:       20003,
		Message:    strings.Repeat("x", 4096),
	}).Error()

	require.Less(t, len(message), 1200)
	require.NotContains(t, message, strings.Repeat("x", 1025))
}

func TestFeishuOAuthStart_DefaultPKCEAndFiveMinuteCookies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := mockFeishuConfig("https://feishu.example.com")
	h := &AuthHandler{cfg: &config.Config{Feishu: cfg}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/feishu/start?redirect=/settings/profile&aff_code=AFF123", nil)

	h.FeishuOAuthStart(c)

	require.Equal(t, http.StatusFound, recorder.Code)
	location, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "/open-apis/authen/v1/authorize", location.Path)
	require.Equal(t, "cli_mock", location.Query().Get("client_id"))
	require.Equal(t, "code", location.Query().Get("response_type"))
	require.Equal(t, cfg.RedirectURL, location.Query().Get("redirect_uri"))
	require.NotEmpty(t, location.Query().Get("state"))
	require.NotEmpty(t, location.Query().Get("code_challenge"))
	require.Equal(t, "S256", location.Query().Get("code_challenge_method"))
	require.NotContains(t, location.Query(), "scope")

	for _, name := range []string{feishuOAuthStateCookieName, feishuOAuthVerifierCookieName, feishuOAuthRedirectCookieName, feishuOAuthIntentCookieName, feishuOAuthAffiliateCookieName} {
		cookie := findCookie(recorder.Result().Cookies(), name)
		require.NotNil(t, cookie, name)
		require.Equal(t, feishuOAuthCookiePath, cookie.Path)
		require.Equal(t, feishuOAuthCookieMaxAgeSec, cookie.MaxAge)
		require.True(t, cookie.HttpOnly)
	}
	affiliateCookie := findCookie(recorder.Result().Cookies(), feishuOAuthAffiliateCookieName)
	require.Equal(t, "AFF123", decodeCookieValueForTest(t, affiliateCookie.Value))
}

func TestFeishuOAuthCallback_InvalidStateDoesNotCallUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := mockFeishuConfig(server.URL)
	h := &AuthHandler{cfg: &config.Config{Feishu: cfg}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/feishu/callback?code=code_mock&state=attacker-state", nil)
	c.Request.AddCookie(&http.Cookie{Name: feishuOAuthStateCookieName, Value: encodeCookieValue("expected-state"), Path: feishuOAuthCookiePath})

	h.FeishuOAuthCallback(c)

	require.Equal(t, http.StatusFound, recorder.Code)
	require.Zero(t, upstreamCalls.Load())
	require.Contains(t, recorder.Header().Get("Location"), "error=invalid_state")
	require.Nil(t, findCookie(recorder.Result().Cookies(), feishuOAuthStateCookieName))
}

func TestFeishuOAuthCallback_ProviderErrorRequiresMatchingState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := mockFeishuConfig("https://feishu.example.com")
	h := &AuthHandler{cfg: &config.Config{Feishu: cfg}}

	t.Run("mismatch preserves legitimate flow", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/feishu/callback?error=access_denied&state=attacker-state", nil)
		c.Request.AddCookie(&http.Cookie{Name: feishuOAuthStateCookieName, Value: encodeCookieValue("expected-state"), Path: feishuOAuthCookiePath})

		h.FeishuOAuthCallback(c)

		require.Equal(t, http.StatusFound, recorder.Code)
		require.Contains(t, recorder.Header().Get("Location"), "error=invalid_state")
		require.Nil(t, findCookie(recorder.Result().Cookies(), feishuOAuthStateCookieName))
	})

	t.Run("match reports provider error and consumes flow", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/feishu/callback?error=access_denied&state=expected-state", nil)
		c.Request.AddCookie(&http.Cookie{Name: feishuOAuthStateCookieName, Value: encodeCookieValue("expected-state"), Path: feishuOAuthCookiePath})

		h.FeishuOAuthCallback(c)

		require.Equal(t, http.StatusFound, recorder.Code)
		require.Contains(t, recorder.Header().Get("Location"), "error=provider_error")
		stateCookie := findCookie(recorder.Result().Cookies(), feishuOAuthStateCookieName)
		require.NotNil(t, stateCookie)
		require.Equal(t, -1, stateCookie.MaxAge)
	})
}

func TestClearOAuthLogoutCookies_ClearsFeishuFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)

	clearOAuthLogoutCookies(c)

	for _, name := range []string{
		feishuOAuthStateCookieName,
		feishuOAuthVerifierCookieName,
		feishuOAuthRedirectCookieName,
		feishuOAuthIntentCookieName,
		feishuOAuthBindUserCookieName,
		feishuOAuthAffiliateCookieName,
	} {
		cookie := findCookie(recorder.Result().Cookies(), name)
		require.NotNil(t, cookie, name)
		require.Equal(t, feishuOAuthCookiePath, cookie.Path)
		require.Equal(t, -1, cookie.MaxAge)
	}
}

func TestFeishuSyntheticIdentityIncludesClientID(t *testing.T) {
	emailA := feishuSyntheticEmail("cli_a", "ou_mock_001")
	emailB := feishuSyntheticEmail("cli_b", "ou_mock_001")
	require.NotEqual(t, emailA, emailB)
	require.True(t, strings.HasSuffix(emailA, "@feishu-connect.invalid"))
	require.Equal(t, emailA, feishuSyntheticEmail("cli_a", "ou_mock_001"))
}

func newMockFeishuOAuthServer(t *testing.T, openID, email string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/v3/token":
			_, _ = w.Write([]byte(`{"code":0,"access_token":"u-token-mock","expires_in":7200,"refresh_token":"r-token-mock","token_type":"Bearer"}`))
		case "/open-apis/authen/v1/user_info":
			responseBody, err := json.Marshal(feishuUserInfoResponse{
				Code: 0,
				Msg:  "success",
				Data: feishuUserInfo{
					Name:      "飞书 Mock 用户",
					AvatarURL: "https://example.com/mock.png",
					OpenID:    openID,
					UnionID:   "union_" + openID,
					TenantKey: "mock-tenant",
					Email:     email,
				},
			})
			if err != nil {
				t.Errorf("marshal mock Feishu user info: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(responseBody)
		default:
			http.NotFound(w, r)
		}
	}))
}

func newFeishuCallbackRequest(code, state, browserSession string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/feishu/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
	req.AddCookie(encodedCookie(feishuOAuthStateCookieName, state))
	req.AddCookie(encodedCookie(feishuOAuthRedirectCookieName, "/dashboard"))
	req.AddCookie(encodedCookie(feishuOAuthVerifierCookieName, "verifier-mock"))
	req.AddCookie(encodedCookie(feishuOAuthIntentCookieName, oauthIntentLogin))
	req.AddCookie(encodedCookie(oauthPendingBrowserCookieName, browserSession))
	return req
}

func newFeishuOAuthHandlerAndClient(t *testing.T, invitationEnabled bool, oauthCfg config.FeishuConnectConfig) (*AuthHandler, *dbent.Client) {
	t.Helper()
	handler, client := newOAuthPendingFlowTestHandler(t, invitationEnabled)
	handler.settingSvc = nil
	handler.cfg = &config.Config{
		JWT: config.JWTConfig{
			Secret:                   "test-secret",
			ExpireHour:               1,
			AccessTokenExpireMinutes: 60,
			RefreshTokenExpireDays:   7,
		},
		Feishu: oauthCfg,
	}
	return handler, client
}

func mockFeishuConfig(baseURL string) config.FeishuConnectConfig {
	return config.FeishuConnectConfig{
		Enabled:             true,
		ClientID:            "cli_mock",
		ClientSecret:        "secret_mock",
		AuthorizeURL:        strings.TrimRight(baseURL, "/") + "/open-apis/authen/v1/authorize",
		TokenURL:            strings.TrimRight(baseURL, "/") + "/oauth/v3/token",
		UserInfoURL:         strings.TrimRight(baseURL, "/") + "/open-apis/authen/v1/user_info",
		Scopes:              "",
		RedirectURL:         "https://api.example.com/api/v1/auth/oauth/feishu/callback",
		FrontendRedirectURL: "/auth/feishu/callback",
		UsePKCE:             true,
	}
}
