package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadDeepSeekCompactResponsesStreamAcceptsReasoningSummaryPartLifecycle(t *testing.T) {
	c, _ := newDeepSeekRemoteCompactTestContext(t, deepSeekRemoteCompactTestRequestBody())
	svc := newDeepSeekRemoteCompactTestService(&httpUpstreamRecorder{})
	completed := deepSeekRemoteCompactCompletedPayload(deepSeekRemoteCompactTestSummary, true, nil)
	wire := deepSeekRemoteCompactSSEWire(
		`{"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"private summary"}}`,
		completed,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(wire)),
	}

	result, err := svc.readDeepSeekCompactResponsesStream(c, resp, deepSeekForwardTestAccount(), time.Now())

	require.NoError(t, err)
	require.True(t, result.Completed)
	require.Equal(t, deepSeekRemoteCompactTestSummary, result.Summary)
	require.True(t, hasBillableOpenAIUsage(result.Usage))
}

func TestReadDeepSeekCompactResponsesStreamDataIntervalTimeoutPreservesUsage(t *testing.T) {
	c, _ := newDeepSeekRemoteCompactTestContext(t, deepSeekRemoteCompactTestRequestBody())
	svc := newDeepSeekRemoteCompactTestService(&httpUpstreamRecorder{})
	svc.cfg.Gateway.StreamDataIntervalTimeout = 1
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	go func() {
		_, _ = io.WriteString(writer, deepSeekRemoteCompactSSEWire(
			`{"type":"response.in_progress","response":{"id":"resp_timeout","usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
		))
	}()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}

	result, err := svc.readDeepSeekCompactResponsesStream(c, resp, deepSeekForwardTestAccount(), time.Now())

	require.ErrorIs(t, err, errDeepSeekSSEDataIntervalTimeout)
	require.ErrorContains(t, err, "DeepSeek compact stream data interval timeout")
	require.True(t, result.UpstreamFailed)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
}
