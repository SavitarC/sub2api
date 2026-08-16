package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesDeepSeekCyberFailedUsageRecordedOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body string
		wire string
	}{
		{
			name: "json",
			body: `{"model":"deepseek-v4-flash","input":"hello","stream":false}`,
			wire: `{"id":"resp_cyber_json","status":"failed","error":{"code":"cyber_policy","message":"blocked"},"usage":{"input_tokens":9,"output_tokens":2}}`,
		},
		{
			name: "sse",
			body: `{"model":"deepseek-v4-flash","input":"hello","stream":true}`,
			wire: "event: response.failed\n" +
				`data: {"type":"response.failed","response":{"id":"resp_cyber_sse","status":"failed","error":{"code":"cyber_policy","message":"blocked"},"usage":{"input_tokens":9,"output_tokens":2}}}` + "\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, usageRepo, apiKey, upstream := newDeepSeekPartialUsageHandler(t, tt.wire)
			usageRepo.created = make(chan *service.UsageLog, 2)
			c, _ := deepSeekPartialUsageContext("/v1/responses", tt.body, apiKey)
			if tt.name == "json" {
				upstream.statusCode = http.StatusOK
			}

			h.Responses(c)

			select {
			case log := <-usageRepo.created:
				require.Equal(t, service.RequestTypeCyberBlocked, log.RequestType)
				require.Equal(t, 9, log.InputTokens)
				require.Equal(t, 2, log.OutputTokens)
			case <-time.After(time.Second):
				t.Fatal("expected DeepSeek cyber usage to be recorded")
			}
			select {
			case extra := <-usageRepo.created:
				t.Fatalf("expected one cyber usage row, got duplicate request_id=%q", extra.RequestID)
			case <-time.After(250 * time.Millisecond):
			}
			require.Equal(t, 1, upstream.calls)
		})
	}
}
