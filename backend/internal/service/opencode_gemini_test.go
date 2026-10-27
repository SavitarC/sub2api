package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func openCodeGeminiTestAccount() *Account {
	return &Account{ID: 71, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "opencode-secret", "account_mode": AccountModeZen,
			"model_mapping": map[string]any{"public-model": "gemini-3.7-flash"}}}
}

func TestOpenCodeGeminiModelProtocol(t *testing.T) {
	a := openCodeGeminiTestAccount()
	require.True(t, a.UsesGeminiUpstream(a.GetMappedModel("public-model")))
	require.False(t, a.UsesGeminiUpstream("claude-sonnet-4-6"))
	a.Credentials["account_mode"] = AccountModeGo
	require.False(t, a.UsesGeminiUpstream("gemini-3.7-flash"))
	a.Credentials["account_mode"] = AccountModeZen
	a.Credentials["api_protocol"] = APIProtocolChatCompletions
	require.False(t, a.UsesGeminiUpstream("gemini-3.7-flash"))
	delete(a.Credentials, "api_protocol")
	a.Credentials["protocol_rules"] = []any{map[string]any{"pattern": "gemini-*", "protocol": APIProtocolChatCompletions}}
	require.False(t, a.UsesGeminiUpstream("gemini-3.7-flash"))
}

func TestOpenCodeGeminiAPIKeyTarget(t *testing.T) {
	for _, tc := range []struct{ name, base, protocolBase, want string }{
		{"default", "", "", "https://opencode.ai/inference/google/v1beta/models/gemini-3.7-flash:streamGenerateContent?alt=sse"},
		{"legacy official", DefaultOpenCodeZenBaseURL, "", "https://opencode.ai/inference/google/v1beta/models/gemini-3.7-flash:streamGenerateContent?alt=sse"},
		{"custom relay", "https://relay.example/zen/v1", "", "https://relay.example/zen/v1/models/gemini-3.7-flash:streamGenerateContent?alt=sse"},
		{"protocol override", "https://relay.example/v1", "https://google-relay.example/google/v1beta", "https://google-relay.example/google/v1beta/models/gemini-3.7-flash:streamGenerateContent?alt=sse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := openCodeGeminiTestAccount()
			a.Credentials["base_url"] = tc.base
			a.Credentials["api_base_urls"] = map[string]any{APIProtocolGemini: tc.protocolBase}
			s := &GeminiMessagesCompatService{cfg: &config.Config{}}
			req, _, err := s.buildAPIKeyModelRequest(context.Background(), a, "gemini-3.7-flash", "streamGenerateContent", true, []byte(`{"contents":[]}`))
			require.NoError(t, err)
			require.Equal(t, tc.want, req.URL.String())
			require.Equal(t, "opencode-secret", req.Header.Get("x-goog-api-key"))
			require.Empty(t, req.Header.Get("Authorization"))
			_, _, err = s.buildAPIKeyModelRequest(context.Background(), a, "gemini-3.7-flash/../../secret", "generateContent", false, nil)
			require.Error(t, err)
			probe, err := (&AccountTestService{cfg: &config.Config{}}).buildGeminiAPIKeyRequest(context.Background(), a, "gemini-3.7-flash", []byte(`{}`))
			require.NoError(t, err)
			require.Equal(t, tc.want, probe.URL.String())
		})
	}
}

func TestOpenAIGatewayOpenCodeGeminiProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const response = `{"modelVersion":"gemini-3.7-flash","candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":2}}`
	for _, tc := range []struct{ protocol, body, responsePath string }{
		{APIProtocolAnthropic, `{"model":"public-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, "content.0.text"},
		{APIProtocolChatCompletions, `{"model":"public-model","messages":[{"role":"user","content":"hi"}]}`, "choices.0.message.content"},
		{APIProtocolResponses, `{"model":"public-model","input":"hi","store":false}`, "output.0.content.0.text"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}}
			compat := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
			s := &OpenAIGatewayService{cfg: &config.Config{}, geminiCompatService: compat}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+tc.protocol, strings.NewReader(tc.body))
			var result *OpenAIForwardResult
			var err error
			switch tc.protocol {
			case APIProtocolAnthropic:
				result, err = s.ForwardAsAnthropic(context.Background(), c, openCodeGeminiTestAccount(), []byte(tc.body), "", "")
			case APIProtocolChatCompletions:
				result, err = s.ForwardAsChatCompletions(context.Background(), c, openCodeGeminiTestAccount(), []byte(tc.body), "", "")
			default:
				result, err = s.Forward(context.Background(), c, openCodeGeminiTestAccount(), []byte(tc.body))
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "hello", gjson.Get(recorder.Body.String(), tc.responsePath).String())
			require.Equal(t, "public-model", result.Model)
			require.Equal(t, "gemini-3.7-flash", result.UpstreamModel)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.CacheReadInputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Contains(t, upstream.lastReq.URL.String(), "/inference/google/v1beta/models/gemini-3.7-flash:generateContent")
			require.Equal(t, "opencode-secret", upstream.lastReq.Header.Get("x-goog-api-key"))
		})
	}
}

func TestOpenCodeNativeGeminiPreservesAccountAndPayload(t *testing.T) {
	const body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"temperature":0.3}}`
	upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`))}}
	s := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/public-model:generateContent", strings.NewReader(body))
	a := openCodeGeminiTestAccount()
	result, err := s.ForwardNative(context.Background(), c, a, "public-model", "generateContent", false, []byte(body))
	require.NoError(t, err)
	require.Equal(t, PlatformOpenCodeGo, a.Platform)
	require.Equal(t, "gemini-3.7-flash", result.UpstreamModel)
	sent, err := io.ReadAll(upstream.lastReq.Body)
	require.NoError(t, err)
	require.JSONEq(t, body, string(sent))
}

func TestOpenAIGatewayOpenCodeGeminiMissingAdapterReturns500(t *testing.T) {
	for _, protocol := range []string{APIProtocolAnthropic, APIProtocolChatCompletions, APIProtocolResponses} {
		t.Run(protocol, func(t *testing.T) {
			s := &OpenAIGatewayService{cfg: &config.Config{}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil)
			result, err := s.forwardGeminiCompat(context.Background(), c, openCodeGeminiTestAccount(), nil, "gemini-3.7-flash", protocol)
			require.Nil(t, result)
			require.ErrorContains(t, err, "Gemini compatibility service is not configured")
			require.Equal(t, http.StatusInternalServerError, recorder.Code)
			require.Contains(t, recorder.Body.String(), "Gemini compatibility service is not configured")
			if protocol == APIProtocolAnthropic {
				require.Equal(t, "error", gjson.Get(recorder.Body.String(), "type").String())
				require.Equal(t, "api_error", gjson.Get(recorder.Body.String(), "error.type").String())
			} else {
				require.Equal(t, "server_error", gjson.Get(recorder.Body.String(), "error.type").String())
			}
		})
	}
}
