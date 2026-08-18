package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUpstreamRequestProcessor_InjectsProviderSpecificUserIdentity(t *testing.T) {
	account := &Account{
		Platform: PlatformDeepseek,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser},
	}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	processor := NewUpstreamRequestProcessor(&config.Config{
		Gateway: config.GatewayConfig{UserIsolation: config.GatewayUserIsolationConfig{Secret: "test-secret"}},
	}, nil)

	tests := []struct {
		name     string
		protocol UpstreamProtocol
		body     string
		path     string
	}{
		{name: "responses", protocol: UpstreamProtocolResponses, body: `{"user":"client-user","model":"deepseek-reasoner"}`, path: "user"},
		{name: "chat completions", protocol: UpstreamProtocolChatCompletions, body: `{"user_id":"client-user","model":"deepseek-reasoner"}`, path: "user_id"},
		{name: "anthropic", protocol: UpstreamProtocolAnthropic, body: `{"metadata":{"user_id":"client-user"},"model":"deepseek-reasoner"}`, path: "metadata.user_id"},
	}

	var first string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processed, err := processor.Process(ctx, UpstreamRequest{
				Account:  account,
				Protocol: tt.protocol,
				Model:    "deepseek-reasoner",
				Body:     []byte(tt.body),
			})
			require.NoError(t, err)
			identity := gjson.GetBytes(processed.Body, tt.path).String()
			require.NotEmpty(t, identity)
			require.NotEqual(t, "client-user", identity)
			require.True(t, len(identity) > len("s2u_v1_"))
			if first == "" {
				first = identity
			} else {
				require.Equal(t, first, identity, "the same user keeps one provider-scoped identity across protocols")
			}
		})
	}

	other, err := processor.Process(context.WithValue(context.Background(), ctxkey.UserID, int64(43)), UpstreamRequest{
		Account:  account,
		Protocol: UpstreamProtocolResponses,
		Model:    "deepseek-reasoner",
		Body:     []byte(`{"user":"client-user"}`),
	})
	require.NoError(t, err)
	require.NotEqual(t, first, gjson.GetBytes(other.Body, "user").String())
}

func TestUpstreamRequestProcessor_IsolationDisabledOrUnsupportedLeavesBodyUnchanged(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	processor := NewUpstreamRequestProcessor(&config.Config{
		Gateway: config.GatewayConfig{UserIsolation: config.GatewayUserIsolationConfig{Secret: "test-secret"}},
	}, nil)

	for _, account := range []*Account{
		{Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Extra: map[string]any{UserIsolationExtraKey: UserIsolationModeOff}},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser}},
	} {
		input := []byte(`{"user":"client-user"}`)
		processed, err := processor.Process(ctx, UpstreamRequest{Account: account, Protocol: UpstreamProtocolResponses, Body: input})
		require.NoError(t, err)
		require.JSONEq(t, string(input), string(processed.Body))
	}
}

func TestUpstreamRequestProcessor_PreservesDeepSeekMaxAcrossProtocols(t *testing.T) {
	account := &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
	processor := NewUpstreamRequestProcessor(&config.Config{JWT: config.JWTConfig{Secret: "jwt-secret"}}, nil)
	for _, tt := range []struct {
		protocol UpstreamProtocol
		path     string
		body     string
	}{
		{UpstreamProtocolResponses, "reasoning.effort", `{"reasoning":{"effort":"xhigh"}}`},
		{UpstreamProtocolChatCompletions, "reasoning_effort", `{"reasoning_effort":"xhigh"}`},
		{UpstreamProtocolAnthropic, "output_config.effort", `{"output_config":{"effort":"xhigh"}}`},
	} {
		processed, err := processor.Process(context.Background(), UpstreamRequest{
			Account:                  account,
			Protocol:                 tt.protocol,
			Model:                    "deepseek-reasoner",
			Body:                     []byte(tt.body),
			RequestedReasoningEffort: "max",
		})
		require.NoError(t, err)
		require.Equal(t, "max", gjson.GetBytes(processed.Body, tt.path).String())
		require.NotNil(t, processed.ReasoningEffort)
		require.Equal(t, "max", *processed.ReasoningEffort)
	}
}

func TestUpstreamRequestProcessor_PolicyRunsBeforeIsolation(t *testing.T) {
	account := &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Extra: map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser}}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	processor := NewUpstreamRequestProcessor(&config.Config{Gateway: config.GatewayConfig{UserIsolation: config.GatewayUserIsolationConfig{Secret: "secret"}}}, func(_ context.Context, _ *Account, _ string, body []byte) ([]byte, error) {
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		payload["service_tier"] = "priority"
		return json.Marshal(payload)
	})
	processed, err := processor.Process(ctx, UpstreamRequest{Account: account, Protocol: UpstreamProtocolResponses, Model: "deepseek-reasoner", Body: []byte(`{"user":"old"}`)})
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(processed.Body, "service_tier").String())
	require.NotEqual(t, "old", gjson.GetBytes(processed.Body, "user").String())
	require.NotNil(t, processed.ServiceTier)
}
