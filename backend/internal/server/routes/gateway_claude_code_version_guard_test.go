package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type claudeCodeVersionRouteSettingRepo struct {
	values map[string]string
}

func (r *claudeCodeVersionRouteSettingRepo) Get(_ context.Context, key string) (*service.Setting, error) {
	value, ok := r.values[key]
	if !ok {
		return nil, service.ErrSettingNotFound
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (r *claudeCodeVersionRouteSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *claudeCodeVersionRouteSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *claudeCodeVersionRouteSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[key] = r.values[key]
	}
	return values, nil
}

func (r *claudeCodeVersionRouteSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *claudeCodeVersionRouteSettingRepo) GetAll(context.Context) (map[string]string, error) {
	values := make(map[string]string, len(r.values))
	for key, value := range r.values {
		values[key] = value
	}
	return values, nil
}

func (r *claudeCodeVersionRouteSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func newClaudeCodeVersionGuardRouteTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxBodySize: 1024 * 1024, TextMaxBodySize: 1024 * 1024}}
	repo := &claudeCodeVersionRouteSettingRepo{values: make(map[string]string)}
	settings := service.NewSettingService(repo, cfg)
	require.NoError(t, settings.UpdateSettings(context.Background(), &service.SystemSettings{MaxClaudeCodeVersion: "2.1.227"}))
	t.Cleanup(func() {
		require.NoError(t, settings.UpdateSettings(context.Background(), &service.SystemSettings{}))
	})

	router := gin.New()
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
			AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			groupID := int64(1)
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				GroupID: &groupID,
				Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
			})
			c.Next()
		}),
		nil,
		nil,
		nil,
		settings,
		nil,
		cfg,
	)
	return router
}

func TestGatewayRoutesRejectOutOfRangeClaudeCodeAcrossTextProtocols(t *testing.T) {
	router := newClaudeCodeVersionGuardRouteTestRouter(t)
	tests := []struct {
		path      string
		anthropic bool
	}{
		{path: "/v1/messages", anthropic: true},
		{path: "/v1/chat/completions"},
		{path: "/v1/responses"},
		{path: "/v1/responses/compact"},
		{path: "/chat/completions"},
		{path: "/responses"},
		{path: "/responses/compact"},
		{path: "/backend-api/codex/responses"},
		{path: "/backend-api/codex/responses/compact"},
		{path: "/antigravity/v1/messages", anthropic: true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{"model":"test"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "claude-cli/2.1.268 (external, cli)")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), "exceeds the maximum allowed version (2.1.227)")
			require.Contains(t, w.Body.String(), `"type":"invalid_request_error"`)
			if tt.anthropic {
				require.Contains(t, w.Body.String(), `"type":"error"`)
			} else {
				require.NotContains(t, w.Body.String(), `,"type":"error"`)
			}
		})
	}
}

func TestGatewayRoutesMountClaudeCodeVersionGuardOnTextIngress(t *testing.T) {
	routeSource, err := os.ReadFile("gateway.go")
	require.NoError(t, err)
	source := string(routeSource)

	for _, route := range []string{
		`gateway.POST("/messages", claudeCodeVersionAnthropic`,
		`gateway.POST("/responses", claudeCodeVersionOpenAI`,
		`gateway.POST("/responses/*subpath", claudeCodeVersionOpenAI`,
		`gateway.POST("/chat/completions", claudeCodeVersionOpenAI`,
		`rootTextRoute(http.MethodPost, "/responses", responsesHandler, claudeCodeVersionOpenAI)`,
		`rootTextRoute(http.MethodPost, "/responses/*subpath", guardResponsesSubpath(responsesHandler), claudeCodeVersionOpenAI)`,
		`codexDirect.POST("/responses", claudeCodeVersionOpenAI`,
		`codexDirect.POST("/responses/*subpath", claudeCodeVersionOpenAI`,
		`antigravityV1.POST("/messages", claudeCodeVersionAnthropic`,
	} {
		require.Contains(t, source, route, "text ingress must use the shared Claude Code version guard")
	}
	require.Contains(t, source,
		`r.Handle(method, path, bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, versionGuard, handler)`,
		"root text aliases must run the version guard after authentication and before their handler")
	require.Regexp(t,
		regexp.MustCompile(`rootTextRoute\(http\.MethodPost, "/chat/completions",[\s\S]{0,500}?\}, claudeCodeVersionOpenAI\)`),
		source,
		"root /chat/completions alias must pass the shared version guard")

	require.NotContains(t, source, `gateway.POST("/messages/count_tokens", claudeCodeVersionAnthropic`,
		"count_tokens must remain exempt from Claude Code version enforcement")
	require.NotContains(t, source, `antigravityV1.POST("/messages/count_tokens", claudeCodeVersionAnthropic`,
		"Antigravity count_tokens must remain exempt from Claude Code version enforcement")
}
