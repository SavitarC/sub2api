package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

type compositeRouteDecisionKey struct{}
type compositeRequestKey struct{}

type compositeRequest struct {
	body       []byte
	path       string
	background bool
}

func ValidateCompositeRequestBody(body []byte) error {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return fmt.Errorf("request body must be a JSON object")
	}
	if !requestJSONUnambiguous(gjson.ParseBytes(body)) {
		return fmt.Errorf("duplicate request fields are not allowed in automatic model routing")
	}
	if previous := gjson.GetBytes(body, "previous_response_id"); previous.Exists() && previous.Type != gjson.Null && previous.Type != gjson.String {
		return fmt.Errorf("previous_response_id must be a string")
	}
	return nil
}

// CompositeRequestHasStoredInput identifies upstream state without an account-owner binding.
func CompositeRequestHasStoredInput(body []byte) bool {
	if value := gjson.GetBytes(body, "conversation"); value.Exists() && value.Type != gjson.Null && value.Raw != `""` {
		return true
	}
	if gjson.GetBytes(body, "prompt.id").String() != "" {
		return true
	}
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "item_reference" {
				return true
			}
		}
	}
	return false
}

// WithCompositeRequest keeps the original request available for per-account capability checks.
func WithCompositeRequest(ctx context.Context, body []byte, path string) context.Context {
	return context.WithValue(ctx, compositeRequestKey{}, compositeRequest{body: body, path: path, background: gjson.GetBytes(body, "background").Bool()})
}

func compositeAccountSupportsRequest(ctx context.Context, account *Account, model string) bool {
	request, present := ctx.Value(compositeRequestKey{}).(compositeRequest)
	decision, routed := CompositeRouteDecisionFromContext(ctx)
	if !present || !routed {
		return true
	}
	usesGemini := account.Platform == PlatformGemini || account.UsesGeminiUpstream(model)
	if decision.Endpoint == CompositeRouteEndpointChatCompletions && usesGemini {
		return compositeGeminiChatRequestSupported(request.body)
	}
	if decision.Endpoint != CompositeRouteEndpointResponses {
		return true
	}
	if gjson.GetBytes(request.body, "background").Bool() || CompositeRequestHasStoredInput(request.body) {
		if !account.IsOpenAICompatible() || usesGemini || resolveUpstreamProtocol(account, APIProtocolResponses, model, nil) != APIProtocolResponses {
			return false
		}
	}
	if strings.Contains(strings.TrimRight(request.path, "/"), "/responses/") {
		return account.IsOpenAICompatible() && !usesGemini
	}
	return !usesGemini || validateGeminiResponsesRequest(request.body) == nil
}

func compositeGeminiChatRequestSupported(body []byte) bool {
	if gjson.GetBytes(body, "n").Int() > 1 || gjson.GetBytes(body, "logprobs").Bool() || gjson.GetBytes(body, "audio").Exists() {
		return false
	}
	for _, modality := range gjson.GetBytes(body, "modalities").Array() {
		if modality.String() != "text" {
			return false
		}
	}
	var chat apicompat.ChatCompletionsRequest
	if json.Unmarshal(body, &chat) != nil {
		return false
	}
	responses, err := apicompat.ChatCompletionsToResponses(&chat)
	if err != nil {
		return false
	}
	responses.Include = nil // The bridge adds encrypted reasoning for Responses backends, not at the client's request.
	converted, err := json.Marshal(responses)
	return err == nil && validateGeminiResponsesRequest(converted) == nil
}

// CompositeRouteDecisionFromContext preserves the candidate set before an attempt binds its provider.
func CompositeRouteDecisionFromContext(ctx context.Context) (CompositeRouteDecision, bool) {
	if ctx == nil {
		return CompositeRouteDecision{}, false
	}
	decision, ok := ctx.Value(compositeRouteDecisionKey{}).(CompositeRouteDecision)
	return decision, ok
}

func compositeAccountSupportedEndpoints(account *Account, model string) []string {
	if account == nil || !domain.IsConcretePlatform(account.Platform) || account.Platform == PlatformTypeSafe {
		return nil
	}
	var endpoints []string
	if !account.IsOpenAICompatible() || account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions) {
		endpoints = append(endpoints, CompositeRouteEndpointMessages, CompositeRouteEndpointChatCompletions, CompositeRouteEndpointResponses)
	}
	if (account.Platform == PlatformAnthropic && !account.IsBedrock()) || account.IsOpenAICompatible() {
		endpoints = append(endpoints, CompositeRouteEndpointCountTokens)
	}
	if account.Platform == PlatformGemini || account.Platform == PlatformAntigravity || account.UsesGeminiUpstream(model) {
		endpoints = append(endpoints, CompositeRouteEndpointGemini)
	}
	if account.Platform == PlatformOpenAI {
		if account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityEmbeddings) {
			endpoints = append(endpoints, CompositeRouteEndpointEmbeddings)
		}
		endpoints = append(endpoints, CompositeRouteEndpointImages)
	} else if account.Platform == PlatformGrok {
		endpoints = append(endpoints, CompositeRouteEndpointImages)
	}
	return endpoints
}

func compositeModelCandidates(accounts []Account, model string) []CompositeRouteCandidate {
	byPlatform := make(map[string]CompositeRouteCandidate)
	for i := range accounts {
		account := &accounts[i]
		if !explicitModelMappingClaims(*account, model) || !domain.IsConcretePlatform(account.Platform) {
			continue
		}
		candidate, found := byPlatform[account.Platform]
		if !found {
			candidate.TargetPlatform, candidate.Priority = account.Platform, account.Priority
		} else if account.Priority < candidate.Priority {
			candidate.Priority = account.Priority
		}
		mapped := stringMappingFromRaw(account.Credentials["model_mapping"])[model]
		for _, endpoint := range compositeAccountSupportedEndpoints(account, mapped) {
			if !slices.Contains(candidate.SupportedEndpoints, endpoint) {
				candidate.SupportedEndpoints = append(candidate.SupportedEndpoints, endpoint)
			}
		}
		byPlatform[account.Platform] = candidate
	}
	candidates := make([]CompositeRouteCandidate, 0, len(byPlatform))
	for _, candidate := range byPlatform {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].TargetPlatform < candidates[j].TargetPlatform
	})
	return candidates
}
