//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type staticClaudeCodeVersionBounds struct {
	min   string
	max   string
	calls int
}

func (s *staticClaudeCodeVersionBounds) GetClaudeCodeVersionBounds(context.Context) (string, string) {
	s.calls++
	return s.min, s.max
}

func TestClaudeCodeVersionGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		path       string
		userAgent  string
		minVersion string
		maxVersion string
		wantStatus int
		wantCalls  int
		wantText   string
	}{
		{name: "non Claude client bypasses settings", path: "/v1/chat/completions", userAgent: "curl/8.0", maxVersion: "2.1.227", wantStatus: http.StatusNoContent},
		{name: "malformed Claude version bypasses settings", path: "/v1/responses", userAgent: "claude-cli/2.1", maxVersion: "2.1.227", wantStatus: http.StatusNoContent},
		{name: "count tokens remains exempt", path: "/v1/messages/count_tokens", userAgent: "claude-cli/9.0.0", maxVersion: "2.1.227", wantStatus: http.StatusNoContent},
		{name: "Responses input tokens remains exempt", path: "/v1/responses/input_tokens", userAgent: "claude-cli/9.0.0", maxVersion: "2.1.227", wantStatus: http.StatusNoContent},
		{name: "no configured bounds allows request", path: "/v1/messages", userAgent: "claude-cli/2.1.268 (external, cli)", wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "minimum boundary is inclusive", path: "/v1/messages", userAgent: "claude-cli/2.1.227", minVersion: "2.1.227", wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "below minimum is rejected", path: "/v1/chat/completions", userAgent: "claude-cli/2.1.226 (external, cli)", minVersion: "2.1.227", wantStatus: http.StatusBadRequest, wantCalls: 1, wantText: "below the minimum required version (2.1.227)"},
		{name: "maximum boundary is inclusive", path: "/v1/responses", userAgent: "claude-cli/2.1.227", maxVersion: "2.1.227", wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "above maximum is rejected case insensitively", path: "/v1/responses", userAgent: "Claude-CLI/2.1.268 (external, cli)", maxVersion: "2.1.227", wantStatus: http.StatusBadRequest, wantCalls: 1, wantText: "exceeds the maximum allowed version (2.1.227)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bounds := &staticClaudeCodeVersionBounds{min: tt.minVersion, max: tt.maxVersion}
			nextCalled := false
			router := gin.New()
			router.Use(claudeCodeVersionGuard(bounds.GetClaudeCodeVersionBounds, OpenAIInvalidRequestErrorWriter))
			router.POST("/*path", func(c *gin.Context) {
				nextCalled = true
				c.Status(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req.Header.Set("User-Agent", tt.userAgent)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code)
			require.Equal(t, tt.wantCalls, bounds.calls)
			require.Equal(t, tt.wantStatus == http.StatusNoContent, nextCalled)
			if tt.wantText != "" {
				require.Contains(t, w.Body.String(), tt.wantText)
				require.Contains(t, w.Body.String(), `"type":"invalid_request_error"`)
			}
		})
	}
}

func TestClaudeCodeVersionGuardNilSettingsFailsOpen(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ClaudeCodeVersionGuard(nil, AnthropicInvalidRequestErrorWriter))
	router.POST("/v1/messages", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/9.0.0")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestClaudeCodeVersionGuardMarksExpectedPolicyRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bounds := &staticClaudeCodeVersionBounds{max: "2.1.227"}
	var rejectReason IngressRejectReason
	var rejected bool
	var businessLimitedReason string

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		rejectReason, rejected = GetIngressRejectReason(c)
		businessLimitedReason = service.OpsClientBusinessLimitedReason(c)
	})
	router.Use(claudeCodeVersionGuard(bounds.GetClaudeCodeVersionBounds, OpenAIInvalidRequestErrorWriter))
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.268 (external, cli)")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.True(t, rejected)
	require.Equal(t, IngressRejectClaudeCodeVersion, rejectReason)
	require.Equal(t, service.OpsClientBusinessLimitedReasonLocalPolicyDenied, businessLimitedReason)
}

func TestClaudeCodeVersionErrorWritersUseEndpointProtocolShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		writer     GatewayErrorWriter
		wantPrefix string
	}{
		{name: "Anthropic", writer: AnthropicInvalidRequestErrorWriter, wantPrefix: `{"error":{"message":"blocked","type":"invalid_request_error"},"type":"error"}`},
		{name: "OpenAI", writer: OpenAIInvalidRequestErrorWriter, wantPrefix: `{"error":{"message":"blocked","type":"invalid_request_error"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			tt.writer(c, http.StatusBadRequest, "blocked")
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.JSONEq(t, tt.wantPrefix, w.Body.String())
		})
	}
}
