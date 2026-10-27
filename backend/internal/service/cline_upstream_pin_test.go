//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestNormalizeClineUpstreamPinCredentials(t *testing.T) {
	require.NoError(t, NormalizeClineUpstreamPinCredentials(nil))
	credentials := map[string]any{
		"api_key": "kept",
		ClineUpstreamPinsCredentialKey: map[string]any{
			" cline-pass/deepseek-v4.1-flash ": map[string]any{
				"pipeline": " Planner ", "upstream": " deepseek ", "has": []string{"vision", "vision", "implicit-caching"},
			},
			"cline-pass/glm-5.3-flash": map[string]any{
				"pipeline": "direct", "upstream": "z-ai", "has": []string{"vision", "implicit-caching"},
			},
		},
	}
	require.NoError(t, NormalizeClineUpstreamPinCredentials(credentials))
	encoded, err := json.Marshal(credentials)
	require.NoError(t, err)
	require.Equal(t, "kept", credentials["api_key"])
	require.Equal(t, "planner", gjson.GetBytes(encoded, "cline_upstream_pins.cline-pass/deepseek-v4\\.1-flash.pipeline").String())
	pins, err := parseClineUpstreamPins(credentials[ClineUpstreamPinsCredentialKey])
	require.NoError(t, err)
	require.Equal(t, []string{"vision", "implicit-caching"}, pins["cline-pass/deepseek-v4.1-flash"].Has)
	require.Empty(t, pins["cline-pass/glm-5.3-flash"].Has)
	require.Equal(t, "z-ai", pins["cline-pass/glm-5.3-flash"].Upstream)

	for name, raw := range map[string]any{
		"not an object":   []string{"model"},
		"null pin":        map[string]any{"model": nil},
		"unknown field":   map[string]any{"model": map[string]any{"upsteam": "deepseek"}},
		"bad pipeline":    map[string]any{"model": map[string]any{"pipeline": "invalid"}},
		"bad capability":  map[string]any{"model": map[string]any{"has": []string{"unknown"}}},
		"bad provider":    map[string]any{"model": map[string]any{"upstream": "deepseek,novita"}},
		"wildcard":        map[string]any{"cline-pass/*": map[string]any{}},
		"blank model":     map[string]any{" ": map[string]any{}},
		"duplicate model": map[string]any{"model": map[string]any{}, " model ": map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, NormalizeClineUpstreamPinCredentials(map[string]any{ClineUpstreamPinsCredentialKey: raw}))
		})
	}
	credentials[ClineUpstreamPinsCredentialKey] = nil
	require.NoError(t, NormalizeClineUpstreamPinCredentials(credentials))
	require.Contains(t, credentials, ClineUpstreamPinsCredentialKey, "bulk JSONB merges need the explicit null")
	require.Nil(t, credentials[ClineUpstreamPinsCredentialKey])
}

func TestApplyClineUpstreamPin(t *testing.T) {
	body := []byte(`{"model":"cline-pass/test-model","messages":[{"role":"user","content":"hello"}],"provider":{"only":["other"],"order":["other"],"ignore":["target"],"allow_fallbacks":true,"data_collection":"deny"},"providerOptions":{"vendor":{"setting":true},"gateway":{"only":["other"],"order":["other"],"has":["vision"],"sort":"ttft"}}}`)
	for _, pipeline := range []string{"planner", "direct", "unknown"} {
		t.Run(pipeline, func(t *testing.T) {
			account := clineTestAccount(1)
			account.Credentials[ClineUpstreamPinsCredentialKey] = map[string]ClineUpstreamPin{
				"cline-pass/test-model": {Pipeline: pipeline, Upstream: "target", Has: []string{"vision", "implicit-caching"}},
			}
			out, err := ApplyClineUpstreamPin(account, body)
			require.NoError(t, err)
			require.Equal(t, "cline-pass/test-model", gjson.GetBytes(out, "model").String())
			require.Equal(t, "hello", gjson.GetBytes(out, "messages.0.content").String())
			require.True(t, gjson.GetBytes(out, "providerOptions.vendor.setting").Bool())
			if pipeline != "direct" {
				require.JSONEq(t, `["target"]`, gjson.GetBytes(out, "providerOptions.gateway.only").Raw)
				require.JSONEq(t, `["vision","implicit-caching"]`, gjson.GetBytes(out, "providerOptions.gateway.has").Raw)
				require.False(t, gjson.GetBytes(out, "providerOptions.gateway.order").Exists())
				require.Equal(t, "ttft", gjson.GetBytes(out, "providerOptions.gateway.sort").String())
			} else {
				require.False(t, gjson.GetBytes(out, "providerOptions.gateway.has").Exists())
			}
			if pipeline != "planner" {
				require.JSONEq(t, `["target"]`, gjson.GetBytes(out, "provider.only").Raw)
				require.False(t, gjson.GetBytes(out, "provider.allow_fallbacks").Bool())
				require.False(t, gjson.GetBytes(out, "provider.order").Exists())
				require.False(t, gjson.GetBytes(out, "provider.ignore").Exists())
				require.Equal(t, "deny", gjson.GetBytes(out, "provider.data_collection").String())
			}
			again, err := ApplyClineUpstreamPin(account, out)
			require.NoError(t, err)
			require.JSONEq(t, string(out), string(again))
			require.Equal(t, "other", gjson.GetBytes(body, "provider.only.0").String(), "input must remain intact for account retry")
		})
	}
}

func TestClineUpstreamPinAutomaticAndUnconfigured(t *testing.T) {
	body := []byte(`{"model":"cline-pass/test-model","messages":[]}`)
	account := clineTestAccount(1)
	for _, candidate := range []*Account{nil, account, {Platform: PlatformOpenAI, Credentials: map[string]any{ClineUpstreamPinsCredentialKey: "invalid"}}} {
		out, err := ApplyClineUpstreamPin(candidate, body)
		require.NoError(t, err)
		require.Equal(t, body, out)
	}
	account.Credentials[ClineUpstreamPinsCredentialKey] = map[string]ClineUpstreamPin{
		"cline-pass/test-model": {Pipeline: "planner", Has: []string{"implicit-caching"}},
	}
	out, err := ApplyClineUpstreamPin(account, body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "providerOptions.gateway.only").Exists())
	require.JSONEq(t, `["implicit-caching"]`, gjson.GetBytes(out, "providerOptions.gateway.has").Raw)
	out, err = ApplyClineUpstreamPin(account, []byte(`{"model":"unconfigured"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"unconfigured"}`, string(out))
	for _, invalid := range []string{
		`{"model":"cline-pass/test-model","providerOptions":{"gateway":[]}}`,
		`{"model":"cline-pass/test-model","providerOptions":{"gateway":{"has":"vision"}}}`,
		`{"model":"cline-pass/test-model","providerOptions":{"gateway":{"has":[1]}}}`,
	} {
		_, err = ApplyClineUpstreamPin(account, []byte(invalid))
		require.Error(t, err)
	}
	account.Credentials[ClineUpstreamPinsCredentialKey] = map[string]ClineUpstreamPin{}
	out, err = ApplyClineUpstreamPin(account, body)
	require.NoError(t, err)
	require.Equal(t, body, out, "clearing configuration restores ordinary forwarding")
}

func TestClineGatewayPinsFinalMappedModelAllIngresses(t *testing.T) {
	const model = "cline-pass/deepseek-v4.1-flash-1017"
	for _, ingress := range routingMatrixIngresses() {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", ingress.name, stream), func(t *testing.T) {
				account := clineTestAccount(3)
				account.Credentials["model_mapping"] = map[string]any{"friendly-model": model}
				account.Credentials[ClineUpstreamPinsCredentialKey] = map[string]ClineUpstreamPin{
					model: {Pipeline: "planner", Upstream: "deepseek", Has: []string{"implicit-caching"}},
				}
				upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				body := routingMatrixCase{ingress: ingress, model: "friendly-model"}.body()
				body, err := sjson.SetBytes(body, "stream", stream)
				require.NoError(t, err)
				body, err = sjson.SetBytes(body, "providerOptions.gateway.has", []string{"vision"})
				require.NoError(t, err)
				_ = ingress.forward(svc, adaptiveProtocolTestContext(ingress.path, body), account, body)
				require.NotEmpty(t, upstream.requests)
				require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "deepseek", gjson.GetBytes(upstream.lastBody, "providerOptions.gateway.only.0").String())
				require.JSONEq(t, `["vision","implicit-caching"]`, gjson.GetBytes(upstream.lastBody, "providerOptions.gateway.has").Raw)
				require.Equal(t, stream, gjson.GetBytes(upstream.lastBody, "stream").Bool())
			})
		}
	}
}

func TestAccountConnectionUsesClineUpstreamPin(t *testing.T) {
	account := clineTestAccount(42)
	account.Credentials[ClineUpstreamPinsCredentialKey] = map[string]ClineUpstreamPin{
		DefaultClineTestModel: {Pipeline: "direct", Upstream: "deepseek", Has: []string{"vision", "implicit-caching"}},
	}
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNChatTestResponse())
	c, recorder := newTestContext()
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "hello", AccountTestModeDefault))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "deepseek", gjson.GetBytes(upstream.lastBody, "provider.only.0").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "providerOptions.gateway.has").Exists())
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestClineUpstreamPinRejectsAmbiguousRoutingFields(t *testing.T) {
	account := clineTestAccount(1)
	for _, pipeline := range []string{"planner", "direct", "unknown"} {
		account.Credentials[ClineUpstreamPinsCredentialKey] = map[string]ClineUpstreamPin{
			"model": {Pipeline: pipeline, Upstream: "deepseek"},
		}
		for _, body := range []string{
			`{"model":"unconfigured","model":"model"}`,
			`{"model":"model","provider":{"only":["other"]},"provider":{"only":["novita"]}}`,
			`{"model":"model","providerOptions":{},"providerOptions":{"gateway":{"only":["novita"]}}}`,
			`{"model":"model","providerOptions":{"gateway":{},"gateway":{"only":["novita"]}}}`,
			`{"model":"model","providerOptions":{"gateway":{"only":["other"],"only":["novita"]}}}`,
		} {
			_, err := ApplyClineUpstreamPin(account, []byte(body))
			require.Error(t, err, pipeline+": "+body)
		}
	}
}

func TestClineDiscoverySnapshotFollowsAccountConnection(t *testing.T) {
	for _, tc := range []struct {
		name         string
		input        UpdateAccountInput
		wantSnapshot bool
	}{
		{"unrelated edit", UpdateAccountInput{Name: "renamed"}, true},
		{"same key", UpdateAccountInput{Credentials: map[string]any{"api_key": "cline_test_key", "api_protocol": APIProtocolAdaptive}}, true},
		{"new key", UpdateAccountInput{Credentials: map[string]any{"api_key": "new-key"}}, false},
		{"protocol address", UpdateAccountInput{Credentials: map[string]any{
			"api_protocol": APIProtocolAdaptive, "api_base_urls": map[string]any{APIProtocolChatCompletions: "https://other.example/v1"},
		}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := clineTestAccount(77)
			account.Extra = map[string]any{ClineUpstreamDiscoveryExtraKey: map[string]any{"model": "snapshot"}}
			repo := &upstreamBillingProbeAdminRepo{&upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}}
			updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), account.ID, &tc.input)
			require.NoError(t, err)
			_, present := updated.Extra[ClineUpstreamDiscoveryExtraKey]
			require.Equal(t, tc.wantSnapshot, present)
		})
	}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{77: clineTestAccount(77)}}
	_, err := (&adminServiceImpl{accountRepo: repo}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{77}, Credentials: map[string]any{"api_base_urls": nil, ClineUpstreamPinsCredentialKey: nil},
	})
	require.NoError(t, err)
	require.Len(t, repo.bulkUpdates, 1)
	require.Contains(t, repo.bulkUpdates[0].Credentials, ClineUpstreamPinsCredentialKey)
	require.Nil(t, repo.bulkUpdates[0].Credentials[ClineUpstreamPinsCredentialKey])
	require.Contains(t, repo.bulkUpdates[0].Extra, ClineUpstreamDiscoveryExtraKey)
	require.Nil(t, repo.bulkUpdates[0].Extra[ClineUpstreamDiscoveryExtraKey])
}
