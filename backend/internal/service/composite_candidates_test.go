package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompositeModelCandidatesRouteConfiguredProvidersAcrossGateways(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	accounts := []Account{
		{ID: 1, Platform: PlatformAnthropic, Priority: 20, Credentials: map[string]any{"model_mapping": map[string]any{"shared-claude": "claude-sonnet"}}},
		{ID: 2, Platform: PlatformCline, Type: AccountTypeAPIKey, Priority: 10, ExpiresAt: &expired, Credentials: map[string]any{"model_mapping": map[string]any{"shared-claude": "anthropic/claude-sonnet"}}},
		{ID: 3, Platform: PlatformCommandCode, Credentials: map[string]any{"model_mapping": map[string]any{"*": "claude-sonnet"}}},
	}
	service := &GatewayService{accountRepo: &compositeOwnershipAccountRepo{accounts: accounts}}
	resolver := NewCompositeRouteResolver(nil)
	resolver.SetModelOwnershipResolver(service.resolveCompositeModelOwnership)
	decision, err := resolver.Resolve(context.Background(), 7, "shared-claude", CompositeRouteEndpointMessages)
	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Equal(t, CompositeRouteSourceAccount, decision.Source)
	require.Equal(t, PlatformCline, decision.TargetPlatform)
	require.Equal(t, []string{PlatformCline, PlatformAnthropic}, []string{decision.Candidates[0].TargetPlatform, decision.Candidates[1].TargetPlatform})
	// Runtime expiry is enforced by the account scheduler, without changing model ownership.
	require.Len(t, decision.Candidates, 2)
	ctx := WithCompositeRouteDecision(context.Background(), decision)
	resolved, matched, err := service.resolveCompositeRouteDecision(ctx, &Group{ID: 7, Platform: PlatformComposite}, "mapped-alias", "")
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, decision, resolved)
}

func TestCompositeModelCandidatesFilterEndpointBeforeDispatch(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformCline, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"shared-gemini": "google/gemini-pro"}}},
		{ID: 2, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"shared-gemini": "gemini-pro"}}},
	}
	service := &GatewayService{accountRepo: &compositeOwnershipAccountRepo{accounts: accounts}}
	resolver := NewCompositeRouteResolver(nil)
	resolver.SetModelOwnershipResolver(service.resolveCompositeModelOwnership)
	decision, err := resolver.Resolve(context.Background(), 7, "shared-gemini", CompositeRouteEndpointGemini)
	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Len(t, decision.Candidates, 1)
	require.Equal(t, PlatformGemini, decision.TargetPlatform)
	unsupported, err := resolver.Resolve(context.Background(), 7, "shared-gemini", CompositeRouteEndpointEmbeddings)
	require.NoError(t, err)
	require.False(t, unsupported.Matched)
	require.Equal(t, CompositeRouteReasonUnsupportedEndpoint, unsupported.Reason)
}

func TestCompositeCandidatesRespectRequestCapabilities(t *testing.T) {
	model := "shared-model"
	decision := CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceAccount, PublicModel: model, Endpoint: CompositeRouteEndpointResponses, Candidates: []CompositeRouteCandidate{{TargetPlatform: PlatformGemini}, {TargetPlatform: PlatformOpenAI}}}
	gemini := &Account{ID: 1, Platform: PlatformGemini, Credentials: map[string]any{"model_mapping": map[string]any{model: "gemini-pro"}}}
	openAI := &Account{ID: 2, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{model: "gpt-model"}}}
	for _, tc := range []struct{ path, body string }{
		{"/v1/responses", `{"model":"shared-model","input":"hi","text":{"format":{"type":"json_schema"}}}`},
		{"/v1/responses", `{"model":"shared-model","input":"hi","tools":[{"type":"web_search"}]}`},
		{"/v1/responses/input_tokens", `{"model":"shared-model","input":"hi"}`},
		{"/v1/responses/compact", `{"model":"shared-model","input":"hi"}`},
	} {
		ctx := WithCompositeRequest(context.Background(), []byte(tc.body), tc.path)
		decision.TargetPlatform = PlatformGemini
		require.False(t, compositeAccountMatchesRoute(WithCompositeRouteDecision(ctx, decision), gemini), tc.path)
		decision.TargetPlatform = PlatformOpenAI
		require.True(t, compositeAccountMatchesRoute(WithCompositeRouteDecision(ctx, decision), openAI), tc.path)
	}
}

func TestCompositeGeminiChatCapabilitiesPreserveRequestedFeatures(t *testing.T) {
	base := `{"model":"shared-gemini","messages":[{"role":"user","content":"hi"}]}`
	require.True(t, compositeGeminiChatRequestSupported([]byte(base)))
	for _, body := range []string{
		`{"model":"shared-gemini","messages":[],"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}}`,
		`{"model":"shared-gemini","messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}],"parallel_tool_calls":false}`,
		`{"model":"shared-gemini","messages":[],"modalities":["audio"]}`,
		`{"model":"shared-gemini","messages":[],"n":2}`,
	} {
		require.False(t, compositeGeminiChatRequestSupported([]byte(body)), body)
	}
}

func TestCompositeRequestRejectsAmbiguousRoutingFields(t *testing.T) {
	for _, body := range []string{
		`{"model":"a","model":"b"}`,
		`{"previous_response_id":"","previous_response_id":"resp_foreign"}`,
		`{"previous_response_id":123}`,
		`{"previous_response_id":{"id":"resp_foreign"}}`,
		`{"prompt":{"id":"","id":"foreign_saved_prompt"}}`,
		`{"input":[{"type":"message","type":"item_reference","id":"foreign_item"}]}`,
		`[]`,
	} {
		require.Error(t, ValidateCompositeRequestBody([]byte(body)), body)
	}
	require.NoError(t, ValidateCompositeRequestBody([]byte(`{"model":"shared","previous_response_id":null}`)))
}

func TestCompositeCountTokensOnlyAdvertisesSupportedAccounts(t *testing.T) {
	require.NotContains(t, compositeAccountSupportedEndpoints(&Account{Platform: PlatformAntigravity}, "claude"), CompositeRouteEndpointCountTokens)
	require.NotContains(t, compositeAccountSupportedEndpoints(&Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}, "claude"), CompositeRouteEndpointCountTokens)
	require.Contains(t, compositeAccountSupportedEndpoints(&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, "claude"), CompositeRouteEndpointCountTokens)
}

func TestCompositeStoredInputPreservesLegacyPromptAndSelfContainedContext(t *testing.T) {
	for _, body := range []string{
		`{"prompt":"legacy input"}`,
		`{"input":[{"type":"reasoning","encrypted_content":"anthropic-thinking-v1:self-contained"}]}`,
		`{"include":["reasoning.encrypted_content"]}`,
	} {
		require.False(t, CompositeRequestHasStoredInput([]byte(body)), body)
	}
	for _, body := range []string{`{"prompt":{"id":"saved"}}`, `{"conversation":"conv_owner"}`, `{"input":[{"type":"item_reference","id":"item_owner"}]}`} {
		require.True(t, CompositeRequestHasStoredInput([]byte(body)), body)
	}
	decision := CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceAccount, Endpoint: CompositeRouteEndpointResponses}
	ctx := WithCompositeRequest(WithCompositeRouteDecision(context.Background(), decision), []byte(`{"background":true}`), "/v1/responses")
	require.False(t, compositeAccountSupportsRequest(ctx, &Account{Platform: PlatformCline, Type: AccountTypeAPIKey}, "model"))
	require.False(t, compositeAccountSupportsRequest(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, "model"))
	require.True(t, compositeAccountSupportsRequest(ctx, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, "gpt-model"))
}
