package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/tidwall/gjson"
)

const (
	ClineUpstreamDiscoveryExtraKey = "cline_upstream_discovery"
	clineProbeBodyLimit            = 2 << 20
	clineProbeTimeout              = 90 * time.Second
)

type ClineUpstreamProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ClineUpstreamDiscovery struct {
	Model          string                  `json:"model"`
	Pipeline       string                  `json:"pipeline"`
	CanonicalModel string                  `json:"canonical_model,omitempty"`
	ActualProvider string                  `json:"actual_provider,omitempty"`
	Providers      []ClineUpstreamProvider `json:"providers"`
	DiscoveredAt   string                  `json:"discovered_at"`
	Warning        string                  `json:"warning,omitempty"`
}

type ClineUpstreamVerification struct {
	Model          string   `json:"model"`
	Pipeline       string   `json:"pipeline"`
	CanonicalModel string   `json:"canonical_model,omitempty"`
	ActualProvider string   `json:"actual_provider,omitempty"`
	TargetProvider string   `json:"target_provider,omitempty"`
	Status         string   `json:"status"`
	Stream         bool     `json:"stream"`
	Completed      bool     `json:"completed"`
	CheckedAt      string   `json:"checked_at"`
	Message        string   `json:"message,omitempty"`
	Has            []string `json:"has,omitempty"`
}

type clineRoutingObservation struct {
	pipeline       string
	canonicalModel string
	provider       string
	providerName   string
	fallbacks      []string
}

var clineProviderSlug = regexp.MustCompile("^[a-z0-9][a-z0-9._/-]{0,127}$")

func clineDiscoveryIdentity(account *Account) string {
	if !account.IsCline() {
		return ""
	}
	identity, _ := json.Marshal([]any{
		account.GetOpenAIBaseURL(), account.GetOpenAIProtocolAPIKey(), account.ProxyID,
		account.Credentials[credKeyHeaderOverrideEnabled], account.Credentials[credKeyHeaderOverrides],
	})
	return string(identity)
}

func validateClineProbeModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 256 || strings.Contains(model, "*") || strings.IndexFunc(model, unicode.IsSpace) >= 0 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return "", infraerrors.BadRequest("INVALID_CLINE_MODEL", "A valid Cline model ID is required")
	}
	return model, nil
}

// Discovery deliberately sends no saved pin or capability filter.
func (s *AccountTestService) DiscoverClineUpstreams(ctx context.Context, account *Account, model string, persist bool) (*ClineUpstreamDiscovery, error) {
	model, err := validateClineProbeModel(model)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, clineProbeTimeout)
	defer cancel()
	body, _ := json.Marshal(clineProbePayload(model, false))
	raw, status, _, err := s.sendClineProbe(ctx, account, body, false)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 || clineProbeHasError(raw) {
		return nil, infraerrors.New(http.StatusBadGateway, "CLINE_DISCOVERY_FAILED", clineProbeErrorMessage(account, raw, status))
	}
	if !gjson.ValidBytes(raw) || !clineProbeJSON(raw).Get("choices").IsArray() {
		return nil, infraerrors.New(http.StatusBadGateway, "CLINE_DISCOVERY_FAILED", "Cline returned an invalid chat response")
	}
	observation := parseClineRouting(raw)
	result := &ClineUpstreamDiscovery{
		Model: model, Pipeline: observation.pipeline, CanonicalModel: observation.canonicalModel,
		ActualProvider: observation.provider, DiscoveredAt: time.Now().UTC().Format(time.RFC3339),
		Providers: make([]ClineUpstreamProvider, 0),
	}
	candidates := append([]string{observation.provider}, observation.fallbacks...)
	if observation.pipeline != "unknown" {
		probe := clineProbePayload(model, false)
		if observation.pipeline == "planner" {
			probe["providerOptions"] = map[string]any{"gateway": map[string]any{"only": []string{"__sub2api_probe__"}}}
		} else {
			probe["provider"] = map[string]any{"only": []string{"__sub2api_probe__"}}
		}
		probeBody, _ := json.Marshal(probe)
		if probeRaw, probeStatus, _, probeErr := s.sendClineProbe(ctx, account, probeBody, false); probeErr == nil {
			available := parseClineAvailableProviders(probeRaw)
			candidates = append(candidates, available...)
			if probeStatus >= 200 && probeStatus < 300 && !clineProbeHasError(probeRaw) {
				result.Warning = "Cline accepted the invalid-provider probe; provider restrictions may have been ignored"
			} else if len(available) == 0 {
				result.Warning = "Cline did not disclose a complete provider list; candidates include observed routing only"
			}
		} else if ctx.Err() != nil {
			return nil, infraerrors.New(http.StatusGatewayTimeout, "CLINE_DISCOVERY_TIMEOUT", "Cline discovery was canceled or timed out")
		} else {
			result.Warning = "The provider list probe failed; candidates include observed routing only"
		}
	} else {
		result.Warning = "Cline did not return a recognizable routing pipeline; providers can be entered manually"
	}
	seen := make(map[string]bool)
	for _, provider := range candidates {
		provider = strings.TrimSpace(provider)
		if !clineProviderSlug.MatchString(provider) || seen[provider] {
			continue
		}
		seen[provider] = true
		name := provider
		if provider == observation.provider && observation.providerName != "" {
			name = observation.providerName
		}
		result.Providers = append(result.Providers, ClineUpstreamProvider{ID: provider, Name: name})
	}
	sort.Slice(result.Providers, func(i, j int) bool { return result.Providers[i].ID < result.Providers[j].ID })
	if len(result.Providers) == 0 && result.Warning == "" {
		result.Warning = "Cline did not disclose available providers; enter a provider ID and verify it"
	}
	if persist && account.ID > 0 && s.accountRepo != nil {
		snapshots := make(map[string]ClineUpstreamDiscovery)
		if account.Extra != nil {
			if encoded, marshalErr := json.Marshal(account.Extra[ClineUpstreamDiscoveryExtraKey]); marshalErr == nil {
				_ = json.Unmarshal(encoded, &snapshots)
			}
		}
		if snapshots == nil {
			snapshots = make(map[string]ClineUpstreamDiscovery)
		}
		snapshots[model] = *result
		if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{ClineUpstreamDiscoveryExtraKey: snapshots}); err != nil {
			result.Warning = "Discovery succeeded, but its snapshot could not be saved"
		}
	}
	return result, nil
}

func (s *AccountTestService) VerifyClineUpstream(ctx context.Context, account *Account, model string, pin ClineUpstreamPin, stream bool) (*ClineUpstreamVerification, error) {
	model, err := validateClineProbeModel(model)
	if err != nil {
		return nil, err
	}
	pin, err = NormalizeClineUpstreamPin(pin)
	if err != nil {
		return nil, err
	}
	if account == nil || !account.IsCline() || account.Type != AccountTypeAPIKey {
		return nil, infraerrors.BadRequest("INVALID_CLINE_ACCOUNT", "A Cline API key account is required")
	}
	probeAccount := *account
	probeAccount.Credentials = cloneClineProbeCredentials(account.Credentials)
	probeAccount.Credentials[ClineUpstreamPinsCredentialKey] = map[string]ClineUpstreamPin{model: pin}
	body, _ := json.Marshal(clineProbePayload(model, stream))
	body, err = ApplyClineUpstreamPin(&probeAccount, body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, clineProbeTimeout)
	defer cancel()
	raw, status, sse, err := s.sendClineProbe(ctx, &probeAccount, body, stream)
	if err != nil {
		return nil, err
	}
	result := &ClineUpstreamVerification{
		Model: model, Pipeline: pin.Pipeline, TargetProvider: pin.Upstream,
		Status: "unavailable", Stream: stream, CheckedAt: time.Now().UTC().Format(time.RFC3339), Has: pin.Has,
	}
	if status < 200 || status >= 300 || (!sse && clineProbeHasError(raw)) {
		result.Message = clineProbeErrorMessage(account, raw, status)
		return result, nil
	}
	if stream && !sse {
		result.Message = "Cline returned JSON instead of the requested event stream"
		return result, nil
	}
	var observation clineRoutingObservation
	if sse {
		observation, result.Completed = parseClineProbeSSE(raw)
	} else {
		observation = parseClineRouting(raw)
		result.Completed = gjson.ValidBytes(raw) && len(clineProbeJSON(raw).Get("choices").Array()) > 0
	}
	if observation.pipeline != "unknown" {
		result.Pipeline = observation.pipeline
	}
	result.ActualProvider = observation.provider
	result.CanonicalModel = observation.canonicalModel
	if !result.Completed {
		result.Message = "The upstream response did not complete successfully"
		return result, nil
	}
	result.Status = "unknown"
	if result.ActualProvider == "" {
		result.Message = "The request completed without actual-provider metadata"
	} else if pin.Upstream == "" || clineProviderMatches(pin.Upstream, result.ActualProvider) {
		result.Status = "matched"
	} else if strings.HasPrefix(pin.Upstream, result.ActualProvider+"/") {
		result.Message = "The upstream reported a provider without the requested endpoint variant"
	} else {
		result.Status = "mismatch"
		result.Message = "The actual provider differs from the requested provider"
	}
	return result, nil
}

func clineProbePayload(model string, stream bool) map[string]any {
	payload := map[string]any{
		"model": model, "stream": stream, "max_tokens": 256,
		"messages": []map[string]string{{"role": "user", "content": "Reply with the word OK."}},
	}
	if stream {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	return payload
}

func cloneClineProbeCredentials(credentials map[string]any) map[string]any {
	cloned := make(map[string]any, len(credentials)+1)
	for key, value := range credentials {
		cloned[key] = value
	}
	return cloned
}

func (s *AccountTestService) sendClineProbe(ctx context.Context, account *Account, body []byte, stream bool) ([]byte, int, bool, error) {
	if account == nil || !account.IsCline() || account.Type != AccountTypeAPIKey {
		return nil, 0, false, infraerrors.BadRequest("INVALID_CLINE_ACCOUNT", "A Cline API key account is required")
	}
	if s == nil || s.httpUpstream == nil {
		return nil, 0, false, infraerrors.New(http.StatusServiceUnavailable, "CLINE_PROBE_UNAVAILABLE", "Account testing is unavailable")
	}
	token := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
	if token == "" {
		return nil, 0, false, infraerrors.BadRequest("CLINE_API_KEY_REQUIRED", "A Cline API key is required")
	}
	baseURL, err := s.validateUpstreamBaseURL(account.GetOpenAIBaseURL())
	if err != nil {
		return nil, 0, false, infraerrors.BadRequest("INVALID_CLINE_BASE_URL", "Invalid Cline base URL")
	}
	req, err := http.NewRequestWithContext(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAI), http.MethodPost, buildOpenAIChatCompletionsURL(baseURL), bytes.NewReader(body))
	if err != nil {
		return nil, 0, false, infraerrors.BadRequest("INVALID_CLINE_BASE_URL", "Invalid Cline base URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if userAgent := account.GetOpenAIUserAgent(); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	account.ApplyHeaderOverrides(req.Header)
	resp, err := s.doUpstreamModelsRequest(req, upstreamModelsProxyURL(account), account)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, false, infraerrors.New(http.StatusGatewayTimeout, "CLINE_PROBE_TIMEOUT", "Cline probe was canceled or timed out")
		}
		return nil, 0, false, infraerrors.New(http.StatusBadGateway, "CLINE_PROBE_FAILED", "Cline upstream request failed")
	}
	defer resp.Body.Close()
	isSSE := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	raw, isSSE, err := readClineProbeBody(resp.Body, isSSE)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, false, infraerrors.New(http.StatusGatewayTimeout, "CLINE_PROBE_TIMEOUT", "Cline probe was canceled or timed out")
		}
		return nil, 0, false, infraerrors.New(http.StatusBadGateway, "CLINE_PROBE_RESPONSE_INVALID", "Cline response was interrupted or exceeded the size limit")
	}
	return raw, resp.StatusCode, isSSE, nil
}

// A relay may keep its body open after the terminal SSE event.
func readClineProbeBody(body io.Reader, isSSE bool) ([]byte, bool, error) {
	reader := bufio.NewReader(io.LimitReader(body, clineProbeBodyLimit+1))
	var raw bytes.Buffer
	var parser openAICompatSSEFrameParser
	for {
		line, err := reader.ReadString('\n')
		raw.WriteString(line)
		if raw.Len() > clineProbeBodyLimit {
			return nil, isSSE, fmt.Errorf("Cline response exceeds %d bytes", clineProbeBodyLimit)
		}
		if line != "" {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if _, dataLine := extractOpenAISSEDataLine(line); dataLine {
				isSSE = true
			}
			if isSSE {
				if frame, ready := parser.AddLine(line); ready && strings.TrimSpace(frame.Data) == "[DONE]" {
					return raw.Bytes(), true, nil
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return raw.Bytes(), isSSE, nil
			}
			return nil, isSSE, err
		}
	}
}

func clineProbeJSON(raw []byte) gjson.Result {
	data := gjson.ParseBytes(raw)
	if wrapped := data.Get("data"); wrapped.IsObject() && wrapped.Get("choices").Exists() {
		return wrapped
	}
	return data
}

func parseClineRouting(raw []byte) clineRoutingObservation {
	root := clineProbeJSON(raw)
	observation := clineRoutingObservation{pipeline: "unknown"}
	for _, path := range []string{
		"provider_metadata.gateway.routing",
		"choices.0.message.provider_metadata.gateway.routing",
		"choices.0.delta.provider_metadata.gateway.routing",
	} {
		routing := root.Get(path)
		if provider := strings.TrimSpace(routing.Get("finalProvider").String()); provider != "" {
			observation.pipeline = "planner"
			observation.provider = clineProviderID(provider)
			observation.providerName = provider
			observation.canonicalModel = routing.Get("canonicalSlug").String()
			for _, fallback := range routing.Get("fallbacksAvailable").Array() {
				if fallback.Type == gjson.String {
					observation.fallbacks = append(observation.fallbacks, clineProviderID(fallback.String()))
				}
			}
			return observation
		}
	}
	if provider := root.Get("provider"); provider.Type == gjson.String && strings.TrimSpace(provider.String()) != "" {
		observation.pipeline = "direct"
		observation.providerName = provider.String()
		observation.provider = clineProviderID(provider.String())
	}
	observation.canonicalModel = root.Get("model").String()
	return observation
}

func clineProviderID(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return strings.NewReplacer(" ", "-", ".", "-").Replace(provider)
}

func clineProviderMatches(target, actual string) bool {
	target, actual = clineProviderID(target), clineProviderID(actual)
	return target == actual || strings.HasPrefix(actual, target+"/")
}

func parseClineAvailableProviders(raw []byte) []string {
	var result []string
	appendJSON := func(value gjson.Result) {
		for _, provider := range value.Array() {
			if provider.Type == gjson.String {
				result = append(result, strings.TrimSpace(provider.String()))
			}
		}
	}
	appendJSON(gjson.GetBytes(raw, "error.metadata.available_providers"))
	appendJSON(gjson.GetBytes(raw, "metadata.available_providers"))
	message := gjson.GetBytes(raw, "error").String()
	if i := strings.IndexByte(message, '{'); i >= 0 {
		appendJSON(gjson.Get(message[i:], "error.metadata.available_providers"))
	}
	const marker = "Available providers are:"
	if i := strings.Index(message, marker); i >= 0 {
		rest := strings.TrimSpace(message[i+len(marker):])
		if end := strings.IndexAny(rest, ".\n\"}"); end >= 0 {
			rest = rest[:end]
		}
		for _, provider := range strings.Split(rest, ",") {
			if provider = strings.TrimSpace(provider); clineProviderSlug.MatchString(provider) {
				result = append(result, provider)
			}
		}
	}
	return result
}

func clineProbeHasError(raw []byte) bool {
	root := clineProbeJSON(raw)
	err := root.Get("error")
	return err.Exists() && err.Type != gjson.Null
}

func clineProbeErrorMessage(account *Account, raw []byte, status int) string {
	message := logredact.RedactText(sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(raw)))
	if account != nil {
		if token := account.GetOpenAIProtocolAPIKey(); token != "" {
			message = strings.ReplaceAll(message, token, "[redacted]")
		}
	}
	if message == "" {
		return fmt.Sprintf("Cline returned HTTP %d", status)
	}
	return truncateString(message, 500)
}

func parseClineProbeSSE(raw []byte) (clineRoutingObservation, bool) {
	observation := clineRoutingObservation{pipeline: "unknown"}
	parser := openAICompatSSEFrameParser{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), clineProbeBodyLimit)
	sawDone, sawChoices, failed := false, false, false
	observe := func(frame openAICompatSSEFrame) {
		if strings.TrimSpace(frame.Data) == "[DONE]" {
			sawDone = true
			return
		}
		payload := []byte(frame.Data)
		if !gjson.ValidBytes(payload) || clineProbeHasError(payload) || frame.EventType == "error" {
			failed = true
			return
		}
		sawChoices = sawChoices || len(clineProbeJSON(payload).Get("choices").Array()) > 0
		current := parseClineRouting(payload)
		if current.provider != "" {
			observation = current
		}
	}
	for scanner.Scan() {
		if frame, ready := parser.AddLine(scanner.Text()); ready {
			observe(frame)
		}
	}
	if frame, ready := parser.Finish(); ready {
		observe(frame)
	}
	return observation, scanner.Err() == nil && sawDone && sawChoices && !failed
}
