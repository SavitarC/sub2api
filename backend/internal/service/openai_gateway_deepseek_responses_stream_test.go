package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHandleDeepSeekResponsesStreamDataIntervalTimeoutStopsReader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := newDeepSeekBlockingTailReadCloser(
		"event: response.created\n" +
			`data: {"type":"response.created","response":{"id":"resp_timeout"}}` + "\n\n",
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamDataIntervalTimeout: 1,
		MaxLineSize:               defaultMaxLineSize,
	}}}

	started := time.Now()
	result, err := svc.handleDeepSeekResponsesStream(
		context.Background(), resp, c, deepSeekForwardTestAccount(), started,
	)

	require.ErrorIs(t, err, errDeepSeekSSEDataIntervalTimeout)
	require.ErrorContains(t, err, "stream data interval timeout")
	require.NotNil(t, result, "already-forwarded events retain their accounting result")
	require.Less(t, time.Since(started), 3*time.Second)
	requireClosedChannel(t, body.readBlocked)
	requireClosedChannel(t, body.closed)
	requireClosedChannel(t, body.readerDone)
	require.Contains(t, recorder.Body.String(), "response.created")
}
