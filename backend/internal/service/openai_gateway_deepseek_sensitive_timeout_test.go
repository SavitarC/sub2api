package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRawDeepSeekChatStreamDataIntervalTimeoutPreservesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamDataIntervalTimeout: 1,
		MaxLineSize:               defaultMaxLineSize,
	}}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	go func() {
		_, _ = io.WriteString(writer,
			"data: {\"id\":\"chat_timeout\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"+
				"data: {\"id\":\"chat_timeout\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n",
		)
	}()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}

	result, err := svc.streamRawChatCompletions(
		c,
		resp,
		deepSeekForwardTestAccount(),
		"deepseek-chat",
		"deepseek-chat",
		"deepseek-chat",
		nil,
		nil,
		time.Now(),
		1,
	)

	require.ErrorIs(t, err, errDeepSeekSSEDataIntervalTimeout)
	require.ErrorContains(t, err, "DeepSeek Chat stream data interval timeout")
	require.NotNil(t, result)
	require.Equal(t, 2, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
	require.Contains(t, recorder.Body.String(), "hello")
}

func TestRawDeepSeekChatStreamDataIntervalTimeoutDiscardsPartialEventBeforeFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamDataIntervalTimeout: 1,
		MaxLineSize:               defaultMaxLineSize,
	}}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	go func() {
		_, _ = io.WriteString(writer,
			"data: {\"id\":\"chat_partial_timeout\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"must stay buffered\"}}]}\n",
		)
	}()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}

	result, err := svc.streamRawChatCompletions(
		c,
		resp,
		deepSeekSSEGuardTestAccount(),
		"deepseek-chat",
		"deepseek-chat",
		"deepseek-chat",
		nil,
		nil,
		time.Now(),
		1,
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Contains(t, string(failoverErr.ResponseBody), "DeepSeek Chat stream data interval timeout")
	require.Empty(t, recorder.Body.Bytes())
	require.False(t, c.Writer.Written())
}
