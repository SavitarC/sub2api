package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
)

const (
	APIProtocolGemini            = "gemini"
	defaultOpenCodeGeminiBaseURL = "https://opencode.ai/inference/google/v1beta"
)

func geminiAccountPlatform(account *Account) string {
	if account != nil && account.Platform != "" {
		return account.Platform
	}
	return PlatformGemini
}

// UsesGeminiUpstream identifies a model that uses the provider's native Google endpoint.
func (a *Account) UsesGeminiUpstream(model string) bool {
	if a == nil || !a.IsOpenCodeZen() || a.Type != AccountTypeAPIKey || !a.IsAdaptiveAPIProtocol() {
		return false
	}
	if _, configured := a.configuredProtocolRules(); configured {
		return false
	}
	return strings.HasPrefix(normalizeOpenCodeGoModelID(model), "gemini-")
}

func (a *Account) openCodeGeminiBaseURL() string {
	if urls, ok := a.Credentials["api_base_urls"].(map[string]any); ok {
		if base, ok := urls[APIProtocolGemini].(string); ok && strings.TrimSpace(base) != "" {
			return strings.TrimRight(strings.TrimSpace(base), "/")
		}
	}
	base := strings.TrimRight(strings.TrimSpace(a.GetOpenAIBaseURL()), "/")
	if base == "" || base == strings.TrimRight(DefaultOpenCodeZenBaseURL, "/") {
		return defaultOpenCodeGeminiBaseURL
	}
	return base
}

func (s *GeminiMessagesCompatService) buildAPIKeyModelRequest(ctx context.Context, account *Account, model, action string, stream bool, body []byte) (*http.Request, string, error) {
	apiKey := account.GetCredential("api_key")
	if strings.TrimSpace(apiKey) == "" {
		return nil, "", errors.New("gemini api_key not configured")
	}
	baseURL := account.GetGeminiBaseURL(geminicli.AIStudioBaseURL)
	modelPath := "/v1beta/models/"
	if account.IsOpenCodeGo() {
		if !account.UsesGeminiUpstream(model) {
			return nil, "", errors.New("account does not support this native Gemini model")
		}
		baseURL = account.openCodeGeminiBaseURL()
		model = normalizeOpenCodeGoModelID(model)
		modelPath = "/models/"
	}
	baseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return nil, "", err
	}
	fullURL, err := buildGeminiModelActionURL(baseURL, modelPath, model, action, stream)
	if err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-goog-api-key", apiKey)
	account.ApplyHeaderOverrides(request.Header)
	return request, "x-request-id", nil
}
