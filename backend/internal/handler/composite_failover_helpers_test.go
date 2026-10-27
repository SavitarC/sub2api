package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func compositeFailoverTestContext(hasNext bool) (*gin.Context, *httptest.ResponseRecorder, *service.CompositeFailoverAttempt) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, attempt := service.WithCompositeFailoverAttempt(context.Background(), hasNext)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	return c, w, attempt
}

func TestCompositeSelectionFailoverRequiresTypedEmptyPool(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil selection", nil, true},
		{"empty pool", fmt.Errorf("select: %w", service.ErrNoAvailableAccounts), true},
		{"compact", service.ErrNoAvailableCompactAccounts, true},
		{"database failure", errors.New("database unavailable"), false},
		{"untyped message", errors.New("no available accounts"), false},
		{"canceled", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w, attempt := compositeFailoverTestContext(true)
			require.Equal(t, tc.want, tryCompositeSelectionFailover(c, tc.err))
			require.Equal(t, tc.want, attempt.RetryRequested)
			require.Empty(t, w.Body.String())
		})
	}
}

func TestCompositeFailoverExhaustionSignalsBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		handle func(*gin.Context, *service.UpstreamFailoverError)
	}{
		{"messages", func(c *gin.Context, e *service.UpstreamFailoverError) {
			(&GatewayHandler{}).handleFailoverExhausted(c, e, service.PlatformAnthropic, false)
		}},
		{"responses", func(c *gin.Context, e *service.UpstreamFailoverError) {
			(&GatewayHandler{}).handleResponsesFailoverExhausted(c, e, false)
		}},
		{"chat", func(c *gin.Context, e *service.UpstreamFailoverError) {
			(&GatewayHandler{}).handleCCFailoverExhausted(c, e, false)
		}},
		{"openai messages", func(c *gin.Context, e *service.UpstreamFailoverError) {
			(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, e, false)
		}},
		{"openai responses/chat", func(c *gin.Context, e *service.UpstreamFailoverError) {
			(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, e, false)
		}},
		{"gemini", func(c *gin.Context, e *service.UpstreamFailoverError) {
			(&GatewayHandler{}).handleGeminiFailoverExhausted(c, e)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, hasNext := range []bool{true, false} {
				c, w, attempt := compositeFailoverTestContext(hasNext)
				tc.handle(c, &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable})
				require.Equal(t, hasNext, attempt.RetryRequested)
				if hasNext {
					require.False(t, c.Writer.Written())
					require.Empty(t, w.Body.String())
				} else {
					require.True(t, c.Writer.Written())
					require.NotEmpty(t, w.Body.String())
				}
			}
		})
	}
}

func TestCompositeFailoverPreservesTerminalAndBoundFailures(t *testing.T) {
	for _, failover := range []*service.UpstreamFailoverError{
		{StatusCode: http.StatusServiceUnavailable, NextAccountAction: service.NextAccountStop},
		{StatusCode: http.StatusServiceUnavailable, BoundUpstreamPassthrough: true},
		{StatusCode: http.StatusBadRequest, Reason: service.OpenAIHTTPContinuationUnsupportedReason},
	} {
		c, w, attempt := compositeFailoverTestContext(true)
		require.False(t, tryCompositeUpstreamFailover(c, failover))
		require.False(t, attempt.RetryRequested)
		require.Empty(t, w.Body.String())
	}
	c, _, attempt := compositeFailoverTestContext(true)
	_, _ = c.Writer.WriteString("data: partial answer\n\n")
	require.False(t, tryCompositeUpstreamFailover(c, &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}))
	require.False(t, attempt.RetryRequested)
}

func TestCompositeEmptySlotSelectionSignalsWithoutResponse(t *testing.T) {
	for _, selection := range []*service.AccountSelectionResult{nil, {Account: &service.Account{ID: 8}}} {
		c, w, attempt := compositeFailoverTestContext(true)
		started := false
		release, result := (&OpenAIGatewayHandler{}).acquireResponsesAccountSlot(c, nil, "", selection, false, &started, nil)
		require.Nil(t, release)
		require.Equal(t, openAISlotAcquireFailed, result)
		require.True(t, attempt.RetryRequested)
		require.Empty(t, w.Body.String())
	}
}

func TestCompositeConcurrencyFailoverKeepsUserLimitsAndInfrastructureErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		slot string
		want bool
	}{
		{"account timeout", &ConcurrencyError{SlotType: "account", IsTimeout: true}, "account", true},
		{"account queue", &WaitQueueFullError{SlotType: "account"}, "account", true},
		{"user timeout", &ConcurrencyError{SlotType: "user", IsTimeout: true}, "user", false},
		{"user queue", &WaitQueueFullError{SlotType: "user"}, "user", false},
		{"misclassified user", &WaitQueueFullError{SlotType: "user"}, "account", false},
		{"infrastructure", errors.New("redis unavailable"), "account", false},
		{"canceled", context.Canceled, "account", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, attempt := compositeFailoverTestContext(true)
			require.Equal(t, tc.want, tryCompositeAccountConcurrencyFailover(c, tc.err, tc.slot))
			require.Equal(t, tc.want, attempt.RetryRequested)
		})
	}
}

func TestCompositeFailoverExhaustedBudgetKeepsTerminalResponse(t *testing.T) {
	c, w, attempt := compositeFailoverTestContext(true)
	c.Request = c.Request.WithContext(service.WithCompositeAccountAttemptBudget(c.Request.Context()))
	require.True(t, admitCompositeAccountAttempt(c, 10, 1, nil))
	require.True(t, admitCompositeAccountAttempt(c, 11, 1, nil))
	released := false
	require.False(t, admitCompositeAccountAttempt(c, 20, 3, func() { released = true }))
	require.True(t, released)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}, false)
	require.False(t, attempt.RetryRequested)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.NotEmpty(t, w.Body.String())
}

func TestCompositeMessagesDispatchRestrictionSkipsOnlyThisProvider(t *testing.T) {
	for _, endpoint := range []struct {
		name string
		run  func(*gin.Context)
	}{
		{"messages", (&OpenAIGatewayHandler{}).Messages},
		{"count_tokens", (&OpenAIGatewayHandler{}).CountTokens},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, hasNext := range []bool{true, false} {
				c, w, attempt := compositeFailoverTestContext(hasNext)
				c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), service.PlatformOpenAI))
				apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite, AllowMessagesDispatch: false}}
				c.Set(string(middleware.ContextKeyAPIKey), apiKey)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
				endpoint.run(c)
				require.Equal(t, hasNext, attempt.RetryRequested)
				if hasNext {
					require.False(t, c.Writer.Written())
					c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), service.PlatformCommandCode))
					require.True(t, allowOpenAICompatibleMessagesDispatch(c, apiKey))
				} else {
					require.Equal(t, http.StatusForbidden, w.Code)
				}
			}
		})
	}
}
