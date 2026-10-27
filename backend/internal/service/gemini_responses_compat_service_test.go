package service

import (
	"context"
	"encoding/json"
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

func TestGeminiResponsesFunctionToolsAndGenerationSettings(t *testing.T) {
	const body = `{"model":"public-model","input":[{"role":"user","content":"find the forecast"}],"tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],"tool_choice":{"type":"function","name":"weather"},"max_output_tokens":321,"temperature":0.2,"top_p":0.8,"reasoning":{"effort":"high"}}`
	const response = `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"weather","args":{"city":"Hangzhou"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"cachedContentTokenCount":3,"candidatesTokenCount":2}}`
	upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}}
	s := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	result, err := s.ForwardAsResponses(context.Background(), c, openCodeGeminiTestAccount(), []byte(body))
	require.NoError(t, err)
	require.Equal(t, "response", gjson.Get(recorder.Body.String(), "object").String())
	require.Equal(t, "function_call", gjson.Get(recorder.Body.String(), "output.0.type").String())
	require.Equal(t, "weather", gjson.Get(recorder.Body.String(), "output.0.name").String())
	require.JSONEq(t, `{"city":"Hangzhou"}`, gjson.Get(recorder.Body.String(), "output.0.arguments").String())
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)
	sent, err := io.ReadAll(upstream.lastReq.Body)
	require.NoError(t, err)
	require.Equal(t, "weather", gjson.GetBytes(sent, "tools.0.functionDeclarations.0.name").String())
	require.Equal(t, "ANY", gjson.GetBytes(sent, "toolConfig.functionCallingConfig.mode").String())
	require.Equal(t, "weather", gjson.GetBytes(sent, "toolConfig.functionCallingConfig.allowedFunctionNames.0").String())
	require.Equal(t, int64(321), gjson.GetBytes(sent, "generationConfig.maxOutputTokens").Int())
	require.Equal(t, 0.2, gjson.GetBytes(sent, "generationConfig.temperature").Float())
	require.Equal(t, 0.8, gjson.GetBytes(sent, "generationConfig.topP").Float())
	require.Equal(t, "high", gjson.GetBytes(sent, "generationConfig.thinkingConfig.thinkingLevel").String())
}

func TestGeminiResponsesStreamsTextThinkingAndTools(t *testing.T) {
	const body = `{"model":"public-model","input":"hi","stream":true,"reasoning":{"effort":"high"}}`
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"considering","thought":true}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"weather","args":{"city":"Hangzhou"}}},{"functionCall":{"name":"weather","args":{"city":"Shanghai"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"cachedContentTokenCount":3,"candidatesTokenCount":4}}`,
	}
	upstreamBody := "data: " + strings.Join(chunks, "\n\ndata: ") + "\n\n"
	upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamBody))}}
	s := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	result, err := s.ForwardAsResponses(context.Background(), c, openCodeGeminiTestAccount(), []byte(body))
	require.NoError(t, err)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, recorder.Body.String(), "response.output_text.delta")
	require.Contains(t, recorder.Body.String(), "response.reasoning_summary_text.delta")
	require.Contains(t, recorder.Body.String(), "considering")
	require.Contains(t, recorder.Body.String(), "hello")
	var completed gjson.Result
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			require.True(t, json.Valid([]byte(payload)))
			if gjson.Get(payload, "type").String() == "response.completed" {
				completed = gjson.Get(payload, "response")
			}
		}
	}
	require.True(t, completed.Exists())
	var calls []gjson.Result
	for _, item := range completed.Get("output").Array() {
		if item.Get("type").String() == "function_call" {
			calls = append(calls, item)
		}
	}
	require.Len(t, calls, 2)
	require.NotEqual(t, calls[0].Get("call_id").String(), calls[1].Get("call_id").String())
	require.JSONEq(t, `{"city":"Hangzhou"}`, calls[0].Get("arguments").String())
	require.JSONEq(t, `{"city":"Shanghai"}`, calls[1].Get("arguments").String())
	require.Equal(t, int64(8), completed.Get("usage.input_tokens").Int())
	require.Equal(t, 4, result.Usage.OutputTokens)
}

func TestGeminiResponsesRejectsUnsupportedStateAndFeatures(t *testing.T) {
	for _, fields := range []string{
		`"previous_response_id":"resp_x"`, `"conversation":"conv_x"`, `"background":true`, `"store":true`,
		`"include":["reasoning.encrypted_content"]`, `"text":{"format":{"type":"json_schema","schema":{}}}`,
		`"tools":[{"type":"web_search"}]`, `"input":[{"type":"item_reference","id":"item_x"}]`,
		`"tools":[{"type":"function","name":"weather"}],"parallel_tool_calls":false`,
	} {
		t.Run(fields, func(t *testing.T) {
			request := map[string]any{"model": "public-model", "input": "hi"}
			var extras map[string]any
			require.NoError(t, json.Unmarshal([]byte("{"+fields+"}"), &extras))
			for key, value := range extras {
				request[key] = value
			}
			encoded, err := json.Marshal(request)
			require.NoError(t, err)
			body := string(encoded)
			upstream := &geminiCompatHTTPUpstreamStub{}
			s := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			_, err = s.ForwardAsResponses(context.Background(), c, openCodeGeminiTestAccount(), []byte(body))
			require.Error(t, err)
			require.Equal(t, 400, recorder.Code)
			require.Zero(t, upstream.calls)
		})
	}
}
