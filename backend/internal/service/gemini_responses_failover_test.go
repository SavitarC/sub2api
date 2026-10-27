package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiResponsesSubresourcesNeverGenerate(t *testing.T) {
	for _, path := range []string{"/v1/responses/compact", "/responses/input_tokens", "/backend-api/codex/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			upstream := &geminiCompatHTTPUpstreamStub{}
			s := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
			body := []byte(`{"model":"public-model","input":"hi"}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
			result, err := s.ForwardAsResponses(context.Background(), c, openCodeGeminiTestAccount(), body)
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, 400, recorder.Code)
			require.Zero(t, upstream.calls)
		})
	}
}

func TestGeminiResponsesPreOutputFailuresAllowFailover(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stream  bool
		payload string
	}{
		{"empty JSON", false, `{}`},
		{"HTTP 200 error envelope", false, `{"error":{"code":503,"status":"UNAVAILABLE","message":"busy"}}`},
		{"empty stream", true, ""},
		{"comment-only stream", true, ": ping\n\n"},
		{"stream error envelope", true, "data: {\"error\":{\"code\":503,\"status\":\"UNAVAILABLE\",\"message\":\"busy\"}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.payload))}}
			s := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
			body := `{"model":"public-model","input":"hi","stream":false}`
			if tc.stream {
				body = `{"model":"public-model","input":"hi","stream":true}`
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			result, err := s.ForwardAsResponses(context.Background(), c, openCodeGeminiTestAccount(), []byte(body))
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Nil(t, result)
			require.Empty(t, recorder.Body.String())
			require.False(t, c.Writer.Written())
		})
	}
}

func TestGeminiResponsesFailureAfterOutputRetainsUsage(t *testing.T) {
	first := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"cachedContentTokenCount\":3,\"candidatesTokenCount\":2}}\n\n"
	for _, tail := range []string{"", "data: {\"error\":{\"code\":503,\"status\":\"UNAVAILABLE\",\"message\":\"upstream interrupted\"}}\n\n"} {
		t.Run(tail, func(t *testing.T) {
			upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(first + tail))}}
			s := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
			body := []byte(`{"model":"public-model","input":"hi","stream":true}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
			result, err := s.ForwardAsResponses(context.Background(), c, openCodeGeminiTestAccount(), body)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.NotNil(t, result)
			require.Equal(t, 7, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.CacheReadInputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Contains(t, recorder.Body.String(), "partial")
			require.Contains(t, recorder.Body.String(), "event: error")
			require.NotContains(t, recorder.Body.String(), "response.completed")
		})
	}
}

func TestOpenCodeGeminiPreservesProtocolOnlyCustomBaseURL(t *testing.T) {
	a := openCodeGeminiTestAccount()
	a.Credentials["api_base_urls"] = map[string]any{APIProtocolChatCompletions: "https://custom-relay.example/provider/v1"}
	s := &GeminiMessagesCompatService{cfg: &config.Config{}}
	request, _, err := s.buildAPIKeyModelRequest(context.Background(), a, "gemini-3.7-flash", "generateContent", false, nil)
	require.NoError(t, err)
	require.Equal(t, "https://custom-relay.example/provider/v1/models/gemini-3.7-flash:generateContent", request.URL.String())
}

func TestGeminiResponsesRejectsDuplicateInputAndUnsupportedContent(t *testing.T) {
	for _, body := range []string{
		`{"model":"public-model","input":"hi","input":[{"type":"item_reference","id":"i"}]}`,
		`{"model":"public-model","input":[{"role":"user","content":[{"type":"input_file","file_id":"file-x"}]}]}`,
		`{"model":"public-model","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://external.example/img.png"}]}]}`,
		`{"model":"public-model","input":"hi","tools":[{"type":"function","name":"weather","strict":true}]}`,
		`{"model":"public-model","input":"hi","generation_config":{"response_modalities":["IMAGE"]}}`,
	} {
		require.Error(t, validateGeminiResponsesRequest([]byte(body)), body)
	}
}

func TestGeminiResponsesInBandErrorsUseExistingFailoverPolicy(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503} {
		a := openCodeGeminiTestAccount()
		a.Credentials["custom_error_codes_enabled"] = true
		a.Credentials["custom_error_codes"] = []any{float64(400)}
		a.Credentials["pool_mode"] = true
		a.Credentials["pool_mode_retry_status_codes"] = []any{float64(status)}
		s := &GeminiMessagesCompatService{}
		result := s.geminiResponsesSignalFailover(a, geminiResponseSignal{Kind: geminiSignalError, Status: status}, []byte(`{"error":{}}`))
		require.NotNil(t, result, "custom codes control account penalties, not failover")
		require.Equal(t, status, result.StatusCode)
		require.True(t, result.RetryableOnSameAccount)
		require.Nil(t, s.geminiResponsesSignalFailover(a, geminiResponseSignal{Kind: geminiSignalPromptBlocked, Status: 400}, nil))
	}
}

func TestGeminiResponsesKeepsObservedModelForBilling(t *testing.T) {
	for _, stream := range []bool{false, true} {
		payload := `{"modelVersion":"gemini-3.7-flash-actual","candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":2}}`
		request := `{"model":"public-model","input":"hi"}`
		if stream {
			payload = "data: " + payload + "\n\n"
			request = `{"model":"public-model","input":"hi","stream":true}`
		}
		upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}}
		compat := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
		s := &OpenAIGatewayService{cfg: &config.Config{}, geminiCompatService: compat}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(request))
		result, err := s.Forward(context.Background(), c, openCodeGeminiTestAccount(), []byte(request))
		require.NoError(t, err)
		require.Equal(t, "gemini-3.7-flash-actual", result.UpstreamResponseModel)
		require.Equal(t, "gemini-3.7-flash", result.BillingModel)
		require.Equal(t, "public-model", result.Model)
		require.Equal(t, 10, result.Usage.InputTokens)
		require.Equal(t, 3, result.Usage.CacheReadInputTokens)
	}
}
