package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type clineHandlerUpstream struct {
	responses []*http.Response
	requests  []*http.Request
	bodies    [][]byte
	proxies   []string
}

func (s *clineHandlerUpstream) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	s.requests = append(s.requests, req)
	s.proxies = append(s.proxies, proxyURL)
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	s.bodies = append(s.bodies, body)
	if len(s.responses) == 0 {
		return nil, errors.New("no stub response")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func (s *clineHandlerUpstream) DoWithTLS(req *http.Request, proxyURL string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, id, concurrency)
}

func clineHandlerResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func clineHandlerAccount() *service.Account {
	proxyID := int64(4)
	return &service.Account{
		ID: 9, Platform: service.PlatformCline, Type: service.AccountTypeAPIKey, ProxyID: &proxyID, Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "saved-cline-key", "api_protocol": service.APIProtocolAdaptive,
			"base_url":      "https://api.cline.bot/api/v1",
			"api_base_urls": map[string]any{service.APIProtocolChatCompletions: "https://api.cline.bot/api/v1"},
		},
	}
}

func clineHandlerRouter(account *service.Account, upstream *clineHandlerUpstream) *gin.Engine {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.getAccountResult = account
	adminSvc.proxies = []service.Proxy{{ID: 4, Protocol: "http", Host: "proxy.example", Port: 8080, Status: service.StatusActive}}
	testSvc := service.NewAccountTestService(nil, nil, nil, nil, nil, upstream,
		&config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}, nil)
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, testSvc, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/discover", handler.DiscoverClineUpstreams)
	router.POST("/verify", handler.VerifyClineUpstream)
	router.POST("/models", handler.SyncUpstreamModelsPreview)
	return router
}

func clineHandlerPost(router *gin.Engine, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func TestClineProbeConnectionUsesDraftWithoutMutatingSavedAccount(t *testing.T) {
	source := clineHandlerAccount()
	source.Extra = map[string]any{"existing": true}
	adminSvc := newStubAdminService()
	adminSvc.getAccountResult = source
	handler := &AccountHandler{adminService: adminSvc}
	proxyNone := int64(0)
	draft, persist, err := handler.resolveClineProbeAccount(context.Background(), clineProbeConnection{
		AccountID: 9, APIKey: "draft-key", BaseURL: "https://relay.example/v1", ProxyID: &proxyNone,
	})
	require.NoError(t, err)
	require.False(t, persist)
	require.Equal(t, "draft-key", draft.GetOpenAIProtocolAPIKey())
	require.Equal(t, "https://relay.example/v1", draft.GetOpenAIBaseURL())
	require.Nil(t, draft.ProxyID)
	require.Nil(t, draft.Proxy)
	require.Equal(t, "saved-cline-key", source.GetOpenAIProtocolAPIKey())
	require.Equal(t, "https://api.cline.bot/api/v1", source.GetOpenAIBaseURL())
	require.Equal(t, int64(4), *source.ProxyID)
	draft.Extra["new-snapshot"] = true
	require.NotContains(t, source.Extra, "new-snapshot")

	_, persist, err = handler.resolveClineProbeAccount(context.Background(), clineProbeConnection{
		AccountID: 9, APIKey: "saved-cline-key", BaseURL: "https://api.cline.bot/api/v1/",
	})
	require.NoError(t, err)
	require.True(t, persist)
}

func TestClineVerificationHandlerUsesSavedKeyAndClearsDirectCapabilities(t *testing.T) {
	upstream := &clineHandlerUpstream{responses: []*http.Response{
		clineHandlerResponse(`{"provider":"Z.AI","choices":[{"message":{"content":"OK"}}]}`),
	}}
	router := clineHandlerRouter(clineHandlerAccount(), upstream)
	rec := clineHandlerPost(router, "/verify", `{"account_id":9,"model":"cline-pass/glm-5.3-flash","pin":{"pipeline":"direct","upstream":"z-ai","has":["vision","implicit-caching"]}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var envelope struct {
		Data service.ClineUpstreamVerification `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, "matched", envelope.Data.Status)
	require.Empty(t, envelope.Data.Has)
	require.True(t, envelope.Data.Completed)
	require.Equal(t, "Bearer saved-cline-key", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "http://proxy.example:8080", upstream.proxies[0])
	require.Equal(t, "z-ai", gjson.GetBytes(upstream.bodies[0], "provider.only.0").String())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "providerOptions.gateway.has").Exists())
	require.NotContains(t, rec.Body.String(), "saved-cline-key")
}

func TestClineProbeHandlerRejectsOtherPlatformsAndMissingKey(t *testing.T) {
	other := clineHandlerAccount()
	other.Platform = service.PlatformOpenAI
	upstream := &clineHandlerUpstream{}
	router := clineHandlerRouter(other, upstream)
	for _, body := range []string{
		`{"account_id":9,"model":"m"}`,
		`{"model":"m"}`,
		`{"account_id":-1,"api_key":"key","model":"m"}`,
		`{"api_key":"key","proxy_id":-1,"model":"m"}`,
	} {
		rec := clineHandlerPost(router, "/discover", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	require.Empty(t, upstream.requests)
}

func TestClineModelPreviewUsesSavedKeyWithDraftURLAndProxy(t *testing.T) {
	upstream := &clineHandlerUpstream{responses: []*http.Response{
		clineHandlerResponse(`{"data":[{"id":"cline-pass/test","reasoning":false,"input_modalities":["text"],"context_window":32768}]}`),
	}}
	source := clineHandlerAccount()
	rec := clineHandlerPost(clineHandlerRouter(source, upstream), "/models",
		`{"platform":"cline","type":"apikey","account_id":9,"base_url":"https://draft.example/v1","proxy_id":0}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, upstream.requests)
	require.Equal(t, "https://draft.example/v1/models", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer saved-cline-key", upstream.requests[0].Header.Get("Authorization"))
	require.Empty(t, upstream.proxies[0])
	require.Equal(t, "https://api.cline.bot/api/v1", source.GetOpenAIBaseURL())
	require.Equal(t, "cline-pass/test", gjson.Get(rec.Body.String(), "data.models.0").String())
}

func TestModelPreviewStillRequiresAPIKeyOutsideCline(t *testing.T) {
	upstream := &clineHandlerUpstream{}
	rec := clineHandlerPost(clineHandlerRouter(clineHandlerAccount(), upstream), "/models",
		`{"platform":"openai","type":"apikey","account_id":9}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, upstream.requests)
}
