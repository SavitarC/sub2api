package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForwardDeepSeekResponsesCyberPolicyMarksJSONUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"deepseek-v4-pro","input":"hello","stream":false}`)
	upstreamBody := `{"id":"resp_ds_cyber_json","status":"failed","error":{"code":"cyber_policy","message":"blocked"},"usage":{"input_tokens":9,"output_tokens":2}}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: deepSeekForwardTestConfig(), httpUpstream: upstream}

	result, err := svc.Forward(context.Background(), c, deepSeekForwardTestAccount(), body)
	require.Error(t, err)
	require.NotNil(t, result)
	mark := GetOpsCyberPolicy(c)
	require.NotNil(t, mark)
	require.Equal(t, "blocked", mark.Message)
	require.Equal(t, http.StatusOK, mark.UpstreamStatus)
	require.Equal(t, 9, mark.UpstreamInTok)
	require.Equal(t, 2, mark.UpstreamOutTok)
	require.Equal(t, upstreamBody, recorder.Body.String())
}
