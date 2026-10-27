package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ForwardAsResponses preserves the Responses envelope while using Gemini's content API.
func (s *GeminiMessagesCompatService) ForwardAsResponses(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	startTime := time.Now()
	if rawOpenAIResponsesRequestPathSuffix(c) != "" {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "Gemini adapter supports generation at /responses; native Responses subresources are unsupported")
	}
	if err := validateGeminiResponsesRequest(body); err != nil {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	var request apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
	}
	claudeRequest, err := apicompat.ResponsesToAnthropicRequest(&request)
	if err != nil {
		return nil, s.writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	claudeRequest.Stream = request.Stream
	claudeBody, err := json.Marshal(claudeRequest)
	if err != nil {
		return nil, err
	}
	return s.forwardClaudeBodyAsOpenAI(ctx, c, account, claudeBody, request.Model, request.Stream, true, true, startTime)
}

func validateGeminiResponsesRequest(body []byte) error {
	if !gjson.ValidBytes(body) {
		return fmt.Errorf("invalid JSON request")
	}
	if !requestJSONUnambiguous(gjson.ParseBytes(body)) {
		return fmt.Errorf("duplicate JSON fields are not supported by the Gemini Responses adapter")
	}
	var unsupported string
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "model", "input", "instructions", "max_output_tokens", "temperature", "top_p", "stream", "tools", "tool_choice", "reasoning", "text", "parallel_tool_calls", "store", "background", "include", "service_tier", "previous_response_id", "conversation", "prompt", "prompt_cache_options":
			return true
		}
		if value.Type != gjson.Null {
			unsupported = key.String()
			return false
		}
		return true
	})
	if unsupported != "" {
		return fmt.Errorf("%s is not supported by the Gemini Responses adapter", unsupported)
	}
	for _, key := range []string{"previous_response_id", "conversation", "prompt", "prompt_cache_options"} {
		if v := gjson.GetBytes(body, key); v.Exists() && v.Type != gjson.Null && v.Raw != `""` {
			return fmt.Errorf("%s is not supported by the Gemini Responses adapter; send the complete conversation", key)
		}
	}
	for _, key := range []string{"background", "store"} {
		if gjson.GetBytes(body, key).Bool() {
			return fmt.Errorf("%s=true is not supported by the Gemini Responses adapter", key)
		}
	}
	if len(gjson.GetBytes(body, "include").Array()) > 0 {
		return fmt.Errorf("include is not supported by the Gemini Responses adapter")
	}
	if format := gjson.GetBytes(body, "text.format.type").String(); format != "" && format != "text" {
		return fmt.Errorf("text.format %q is not supported by the Gemini Responses adapter", format)
	}
	if gjson.GetBytes(body, "text.verbosity").String() != "" {
		return fmt.Errorf("text.verbosity is not supported by the Gemini Responses adapter")
	}
	if parallel := gjson.GetBytes(body, "parallel_tool_calls"); parallel.Exists() && !parallel.Bool() && len(gjson.GetBytes(body, "tools").Array()) > 0 {
		return fmt.Errorf("parallel_tool_calls=false is not supported by the Gemini Responses adapter")
	}
	if tier := gjson.GetBytes(body, "service_tier").String(); tier != "" && tier != "auto" && tier != "default" {
		return fmt.Errorf("service_tier %q is not supported by the Gemini Responses adapter", tier)
	}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() != "function" {
			return fmt.Errorf("Gemini Responses adapter supports function tools; tool type %q is unsupported", tool.Get("type").String())
		}
		if tool.Get("strict").Bool() {
			return fmt.Errorf("strict function tools are not supported by the Gemini Responses adapter")
		}
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}
	for _, item := range input.Array() {
		if item.Get("encrypted_content").String() != "" || item.Get("type").String() == "item_reference" {
			return fmt.Errorf("upstream-bound input cannot be replayed through the Gemini Responses adapter")
		}
		switch item.Get("type").String() {
		case "", "message", "function_call", "function_call_output":
		default:
			return fmt.Errorf("input item type %q is not supported by the Gemini Responses adapter", item.Get("type").String())
		}
		content := item.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "input_text", "output_text":
			case "input_image":
				if !strings.HasPrefix(part.Get("image_url").String(), "data:") {
					return fmt.Errorf("Gemini Responses adapter requires inline image data")
				}
			default:
				return fmt.Errorf("content type %q is not supported by the Gemini Responses adapter", part.Get("type").String())
			}
		}
	}
	return nil
}

func (s *GeminiMessagesCompatService) checkGeminiResponsesBody(c *gin.Context, account *Account, body []byte, requestID string) error {
	signal, found := detectGeminiResponseSignalInBody(body)
	if !found && isGeminiEmptyResponseBody(body) {
		signal = geminiResponseSignal{Kind: geminiSignalError, Status: http.StatusBadGateway, Reason: "EMPTY_RESPONSE", Message: "Gemini upstream returned an empty response"}
		found = true
	}
	if !found {
		return nil
	}
	s.markGeminiResponseSignal(c, account, signal, false, requestID)
	if failover := s.geminiResponsesSignalFailover(account, signal, body); failover != nil {
		return failover
	}
	return s.writeChatCompletionsError(c, signal.Status, "upstream_error", sanitizeUpstreamErrorMessage(signal.Message))
}

func (s *GeminiMessagesCompatService) geminiResponsesSignalFailover(account *Account, signal geminiResponseSignal, body []byte) *UpstreamFailoverError {
	if signal.Kind != geminiSignalError || !s.shouldFailoverGeminiUpstreamError(signal.Status) {
		return nil
	}
	return &UpstreamFailoverError{
		StatusCode: signal.Status, ResponseBody: body,
		RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(signal.Status),
	}
}
