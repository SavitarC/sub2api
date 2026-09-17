package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ClaudeCodeVersionGuard enforces the configured Claude Code version range for
// any HTTP protocol carrying an official claude-cli User-Agent.
func ClaudeCodeVersionGuard(settings *service.SettingService, writeError GatewayErrorWriter) gin.HandlerFunc {
	if settings == nil {
		return claudeCodeVersionGuard(nil, writeError)
	}
	return claudeCodeVersionGuard(settings.GetClaudeCodeVersionBounds, writeError)
}

func claudeCodeVersionGuard(getBounds func(context.Context) (min, max string), writeError GatewayErrorWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if getBounds == nil || c.Request == nil || strings.HasSuffix(c.Request.URL.Path, "/messages/count_tokens") ||
			service.IsOpenAIResponsesInputTokensRequestPath(c) {
			c.Next()
			return
		}

		clientVersion := service.ExtractCLIVersion(c.GetHeader("User-Agent"))
		if clientVersion == "" {
			c.Next()
			return
		}

		minVersion, maxVersion := getBounds(c.Request.Context())
		message := ""
		switch {
		case minVersion != "" && service.CompareVersions(clientVersion, minVersion) < 0:
			message = fmt.Sprintf(
				"Your Claude Code version (%s) is below the minimum required version (%s). Please update: npm update -g @anthropic-ai/claude-code",
				clientVersion,
				minVersion,
			)
		case maxVersion != "" && service.CompareVersions(clientVersion, maxVersion) > 0:
			message = fmt.Sprintf(
				"Your Claude Code version (%s) exceeds the maximum allowed version (%s). Please downgrade: npm install -g @anthropic-ai/claude-code@%s && set CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 to prevent auto-upgrade",
				clientVersion,
				maxVersion,
				maxVersion,
			)
		}

		if message == "" {
			c.Next()
			return
		}

		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		MarkIngressRejected(c, IngressRejectClaudeCodeVersion)
		if writeError == nil {
			OpenAIInvalidRequestErrorWriter(c, http.StatusBadRequest, message)
		} else {
			writeError(c, http.StatusBadRequest, message)
		}
		c.Abort()
	}
}

// AnthropicInvalidRequestErrorWriter writes an Anthropic-compatible request error.
func AnthropicInvalidRequestErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "invalid_request_error",
			"message": message,
		},
	})
}

// OpenAIInvalidRequestErrorWriter writes an OpenAI-compatible request error.
func OpenAIInvalidRequestErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    "invalid_request_error",
			"message": message,
		},
	})
}
