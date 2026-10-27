package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type clineProbeHTTPStub struct {
	responses []*http.Response
	requests  []*http.Request
	bodies    [][]byte
	proxyURLs []string
	err       error
}

func (s *clineProbeHTTPStub) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, body)
	s.proxyURLs = append(s.proxyURLs, proxyURL)
	if s.err != nil {
		return nil, s.err
	}
	if len(s.responses) == 0 {
		return nil, errors.New("no stub response")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func (s *clineProbeHTTPStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, concurrency)
}

type clineDiscoveryRepoStub struct {
	AccountRepository
	calls   int
	updates map[string]any
}

func (s *clineDiscoveryRepoStub) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	s.calls++
	s.updates = updates
	return nil
}

func clineProbeTestAccount() *Account {
	return &Account{
		ID: 55, Platform: PlatformCline, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "cline_test_secret"},
	}
}

func clineProbeTestResponse(status int, body string, stream bool) *http.Response {
	contentType := "application/json"
	if stream {
		contentType = "text/event-stream"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func clineProbeTestService(upstream HTTPUpstream) *AccountTestService {
	return NewAccountTestService(nil, nil, nil, nil, nil, upstream,
		&config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}, nil)
}

const clinePlannerProbeReply = `{"data":{"choices":[{"message":{"content":"OK","provider_metadata":{"gateway":{"routing":{"finalProvider":"deepseek","canonicalSlug":"deepseek/deepseek-v4.1-flash","fallbacksAvailable":["novita"]}}}},"finish_reason":"stop"}]}}`

func TestClineUpstreamDiscoveryIsUnfilteredAndPersistsCandidates(t *testing.T) {
	upstream := &clineProbeHTTPStub{responses: []*http.Response{
		clineProbeTestResponse(200, clinePlannerProbeReply, false),
		clineProbeTestResponse(400, `{"error":"No allowed providers. Available providers are: deepseek, novita, vertex."}`, false),
	}}
	repo := &clineDiscoveryRepoStub{}
	svc := clineProbeTestService(upstream)
	svc.accountRepo = repo
	account := clineProbeTestAccount()
	account.Credentials[ClineUpstreamPinsCredentialKey] = map[string]ClineUpstreamPin{
		"cline-pass/model": {Pipeline: "planner", Upstream: "unavailable", Has: []string{"vision"}},
	}
	account.Extra = map[string]any{ClineUpstreamDiscoveryExtraKey: map[string]any{
		"old-model": ClineUpstreamDiscovery{Model: "old-model", Pipeline: "unknown"},
	}}

	result, err := svc.DiscoverClineUpstreams(context.Background(), account, "cline-pass/model", true)

	require.NoError(t, err)
	require.Equal(t, "planner", result.Pipeline)
	require.Equal(t, "deepseek", result.ActualProvider)
	require.Equal(t, "deepseek/deepseek-v4.1-flash", result.CanonicalModel)
	require.Equal(t, []ClineUpstreamProvider{{ID: "deepseek", Name: "deepseek"}, {ID: "novita", Name: "novita"}, {ID: "vertex", Name: "vertex"}}, result.Providers)
	require.Len(t, upstream.bodies, 2)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "providerOptions").Exists())
	require.Equal(t, "cline-pass/model", gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "__sub2api_probe__", gjson.GetBytes(upstream.bodies[1], "providerOptions.gateway.only.0").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "providerOptions.gateway.has").Exists())
	require.Equal(t, "https://api.cline.bot/api/v1/chat/completions", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer cline_test_secret", upstream.requests[0].Header.Get("Authorization"))
	deadline, bounded := upstream.requests[0].Context().Deadline()
	require.True(t, bounded)
	require.LessOrEqual(t, time.Until(deadline), clineProbeTimeout)
	require.Equal(t, 1, repo.calls)
	snapshots := repo.updates[ClineUpstreamDiscoveryExtraKey].(map[string]ClineUpstreamDiscovery)
	require.Contains(t, snapshots, "old-model")
	require.Contains(t, snapshots, "cline-pass/model")
	require.NotContains(t, account.Extra[ClineUpstreamDiscoveryExtraKey], "cline-pass/model")
}

func TestClineUpstreamDiscoveryDirectAndUnknown(t *testing.T) {
	upstream := &clineProbeHTTPStub{responses: []*http.Response{
		clineProbeTestResponse(200, `{"provider":"Z.AI","model":"z-ai/glm-5.3-flash","choices":[{"message":{"content":"OK"}}]}`, false),
		clineProbeTestResponse(404, `{"error":{"metadata":{"available_providers":["z-ai","novita","invalid provider"]}}}`, false),
	}}
	svc := clineProbeTestService(upstream)
	repo := &clineDiscoveryRepoStub{}
	svc.accountRepo = repo
	result, err := svc.DiscoverClineUpstreams(context.Background(), clineProbeTestAccount(), "cline-pass/glm-5.3-flash", false)
	require.NoError(t, err)
	require.Equal(t, "direct", result.Pipeline)
	require.Equal(t, "z-ai", result.ActualProvider)
	require.Equal(t, []ClineUpstreamProvider{{ID: "novita", Name: "novita"}, {ID: "z-ai", Name: "Z.AI"}}, result.Providers)
	require.True(t, gjson.GetBytes(upstream.bodies[1], "provider.only").IsArray())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "providerOptions").Exists())
	require.Zero(t, repo.calls)

	upstream.responses = []*http.Response{clineProbeTestResponse(200, `{"choices":[{"message":{"content":"OK"}}]}`, false)}
	result, err = svc.DiscoverClineUpstreams(context.Background(), clineProbeTestAccount(), "unknown", false)
	require.NoError(t, err)
	require.Equal(t, "unknown", result.Pipeline)
	require.Empty(t, result.Providers)
	require.NotEmpty(t, result.Warning)
	require.Len(t, upstream.requests, 3)
}

func TestClineUpstreamVerificationChecksActualProviderAndCompletion(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"provider_metadata\":{\"gateway\":{\"routing\":{\"finalProvider\":\"deepseek\"}}},\"choices\":[]}\n\n"
	tests := []struct {
		name, body, pipeline, upstream, want string
		status                               int
		stream, requestStream, complete      bool
	}{
		{"planner match", clinePlannerProbeReply, "planner", "deepseek", "matched", 200, false, false, true},
		{"planner mismatch", clinePlannerProbeReply, "planner", "novita", "mismatch", 200, false, false, true},
		{"missing metadata", `{"choices":[{"message":{"content":"OK"}}]}`, "planner", "deepseek", "unknown", 200, false, false, true},
		{"direct display name", `{"provider":"Z.AI","choices":[{"message":{"content":"OK"}}]}`, "direct", "z-ai", "matched", 200, false, false, true},
		{"variant unknown", `{"provider":"Z.AI","choices":[{"message":{"content":"OK"}}]}`, "direct", "z-ai/fp8", "unknown", 200, false, false, true},
		{"complete SSE", stream + "data: [DONE]\n\n", "planner", "deepseek", "matched", 200, true, true, true},
		{"truncated SSE", stream, "planner", "deepseek", "unavailable", 200, true, true, false},
		{"SSE terminal error", stream + "event: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\ndata: [DONE]\n\n", "planner", "deepseek", "unavailable", 200, true, true, false},
		{"JSON instead of SSE", clinePlannerProbeReply, "planner", "deepseek", "unavailable", 200, false, true, false},
		{"HTTP failure", `{"error":{"message":"rate limited"}}`, "planner", "deepseek", "unavailable", 429, false, false, false},
		{"200 error", `{"error":{"message":"failed"}}`, "planner", "deepseek", "unavailable", 200, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			httpStub := &clineProbeHTTPStub{responses: []*http.Response{clineProbeTestResponse(tc.status, tc.body, tc.stream)}}
			account := clineProbeTestAccount()
			pin := ClineUpstreamPin{Pipeline: tc.pipeline, Upstream: tc.upstream, Has: []string{"vision", "implicit-caching"}}
			result, err := clineProbeTestService(httpStub).VerifyClineUpstream(context.Background(), account, "cline-pass/model", pin, tc.requestStream)
			require.NoError(t, err)
			require.Equal(t, tc.want, result.Status)
			require.Equal(t, tc.complete, result.Completed)
			require.NotContains(t, account.Credentials, ClineUpstreamPinsCredentialKey)
			if tc.pipeline == "direct" {
				require.Empty(t, result.Has)
				require.False(t, gjson.GetBytes(httpStub.bodies[0], "providerOptions.gateway.has").Exists())
			} else {
				require.Len(t, gjson.GetBytes(httpStub.bodies[0], "providerOptions.gateway.has").Array(), 2)
			}
		})
	}
}

func TestClineUpstreamProbeRejectsInvalidConfigurationAndBoundsBody(t *testing.T) {
	for _, model := range []string{"", "\n", "model name", "model\u00a0name", "model\u0000name", "model*", strings.Repeat("x", 257)} {
		upstream := &clineProbeHTTPStub{}
		_, err := clineProbeTestService(upstream).DiscoverClineUpstreams(context.Background(), clineProbeTestAccount(), model, false)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		require.Empty(t, upstream.requests)
	}
	upstream := &clineProbeHTTPStub{}
	account := clineProbeTestAccount()
	account.Credentials["base_url"] = "http://unsafe.example.com"
	_, err := clineProbeTestService(upstream).DiscoverClineUpstreams(context.Background(), account, "model", false)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Empty(t, upstream.requests)

	upstream.responses = []*http.Response{clineProbeTestResponse(200, strings.Repeat("x", clineProbeBodyLimit+1), false)}
	_, err = clineProbeTestService(upstream).DiscoverClineUpstreams(context.Background(), clineProbeTestAccount(), "model", false)
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
}

func TestClineProbeErrorRedactsCredential(t *testing.T) {
	upstream := &clineProbeHTTPStub{responses: []*http.Response{clineProbeTestResponse(401, `{"error":{"message":"Invalid key cline_test_secret"}}`, false)}}
	result, err := clineProbeTestService(upstream).VerifyClineUpstream(context.Background(), clineProbeTestAccount(), "model", ClineUpstreamPin{}, false)
	require.NoError(t, err)
	require.Equal(t, "unavailable", result.Status)
	require.NotContains(t, result.Message, "cline_test_secret")
}

func TestClineProbeSSEMultilineAndWrappedMetadata(t *testing.T) {
	raw := "data: {\"data\":{\"choices\":[{\"delta\":{\"content\":\"OK\",\n" +
		"data: \"provider_metadata\":{\"gateway\":{\"routing\":{\"finalProvider\":\"deepseek\"}}}},\"finish_reason\":\"stop\"}]}}\n\n" +
		"data: [DONE]\n\n"
	observation, completed := parseClineProbeSSE([]byte(raw))
	require.True(t, completed)
	require.Equal(t, "deepseek", observation.provider)
	require.Equal(t, "planner", observation.pipeline)
}

func TestClineDiscoveryWarnsWhenProviderRestrictionIsIgnored(t *testing.T) {
	upstream := &clineProbeHTTPStub{responses: []*http.Response{
		clineProbeTestResponse(200, clinePlannerProbeReply, false),
		clineProbeTestResponse(200, clinePlannerProbeReply, false),
	}}
	discovery, err := clineProbeTestService(upstream).DiscoverClineUpstreams(context.Background(), clineProbeTestAccount(), "model", false)
	require.NoError(t, err)
	require.Contains(t, discovery.Warning, "ignored")
	require.NotEmpty(t, discovery.Providers)
}

func TestClineProbePreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	upstream := &clineProbeHTTPStub{err: context.Canceled}
	_, err := clineProbeTestService(upstream).VerifyClineUpstream(ctx, clineProbeTestAccount(), "model", ClineUpstreamPin{}, false)
	require.Equal(t, http.StatusGatewayTimeout, infraerrors.Code(err))
	require.Len(t, upstream.requests, 1)
	require.ErrorIs(t, upstream.requests[0].Context().Err(), context.Canceled)
}

type clineProbeOpenStream struct {
	*strings.Reader
	readPastTerminal bool
	closed           bool
}

func (s *clineProbeOpenStream) Read(p []byte) (int, error) {
	if s.Reader.Len() == 0 {
		s.readPastTerminal = true
		return 0, errors.New("relay still open after terminal event")
	}
	return s.Reader.Read(p)
}

func (s *clineProbeOpenStream) Close() error {
	s.closed = true
	return nil
}

func TestClineProbeSSEStopsAtDoneWithoutEOF(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", ""} {
		t.Run(contentType, func(t *testing.T) {
			body := &clineProbeOpenStream{Reader: strings.NewReader(
				"data: {\"provider\":\"DeepSeek\",\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\r\n\r\n" +
					"data: [DONE]\r\n\r\n")}
			upstream := &clineProbeHTTPStub{responses: []*http.Response{{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: body,
			}}}
			result, err := clineProbeTestService(upstream).VerifyClineUpstream(context.Background(), clineProbeTestAccount(),
				"model", ClineUpstreamPin{Pipeline: "direct", Upstream: "deepseek"}, true)
			require.NoError(t, err)
			require.True(t, result.Completed)
			require.Equal(t, "matched", result.Status)
			require.False(t, body.readPastTerminal)
			require.True(t, body.closed)
		})
	}
}
