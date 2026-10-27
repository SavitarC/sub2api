package routes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func compositeCandidatesDecision() service.CompositeRouteDecision {
	return service.CompositeRouteDecision{
		Matched: true, Source: service.CompositeRouteSourceAccount, GroupID: 7,
		PublicModel: "shared-model", UpstreamModel: "shared-model", TargetPlatform: service.PlatformAnthropic,
		Endpoint: service.CompositeRouteEndpointMessages,
		Candidates: []service.CompositeRouteCandidate{
			{TargetPlatform: service.PlatformAnthropic, Priority: 1},
			{TargetPlatform: service.PlatformCline, Priority: 2},
		},
	}
}

func TestCompositeCandidateHandlerIsolatesProviderAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	decision := compositeCandidatesDecision()
	router := gin.New()
	var finalPlatform string
	router.Use(func(c *gin.Context) {
		c.Set("auth", "original")
		c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
		c.Header("X-Request-Id", "client-request")
		c.Next()
		finalPlatform, _ = service.ResolvedTargetPlatformFromContext(c.Request.Context())
		require.Equal(t, "success", c.GetString("attempt"))
	})
	calls := 0
	var firstContext context.Context
	router.POST("/v1/messages", compositeCandidateHandler(func(c *gin.Context) {
		calls++
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"shared-model","messages":[]}`, string(body))
		require.Equal(t, "original", c.GetString("auth"))
		require.Empty(t, c.Request.Header.Get("X-Upstream-Only"))
		platform, _ := service.ResolvedTargetPlatformFromContext(c.Request.Context())
		if calls == 1 {
			firstContext = c.Request.Context()
			require.Equal(t, service.PlatformAnthropic, platform)
			c.Set("attempt", "failed")
			c.Request.Header.Set("X-Upstream-Only", "secret")
			c.Header("Retry-After", "60")
			require.True(t, service.TryCompositeFailover(c))
			return
		}
		require.Equal(t, service.PlatformCline, platform)
		require.Equal(t, service.PlatformCline, service.QuotaPlatform(c.Request.Context(), nil))
		require.Empty(t, c.GetString("attempt"))
		require.Empty(t, c.Writer.Header().Get("Retry-After"))
		require.Equal(t, service.GatewayTokenRequestPricingAtFromContext(firstContext), service.GatewayTokenRequestPricingAtFromContext(c.Request.Context()))
		c.Set("attempt", "success")
		c.JSON(http.StatusOK, gin.H{"model": "shared-model", "answer": "ok"})
	}, nil))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"shared-model","messages":[]}`)))
	require.Equal(t, 2, calls)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, service.PlatformCline, finalPlatform)
	require.Equal(t, "client-request", w.Header().Get("X-Request-Id"))
	require.Empty(t, w.Header().Get("Retry-After"))
	// Detached work from the first provider retains that provider's identity.
	platform, _ := service.ResolvedTargetPlatformFromContext(firstContext)
	require.Equal(t, service.PlatformAnthropic, platform)
}

func TestCompositeCandidateHandlerStopsAtOutputOrClientError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"stream output", http.StatusOK},
		{"invalid client request", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), compositeCandidatesDecision()))
			})
			calls := 0
			router.POST("/v1/messages", compositeCandidateHandler(func(c *gin.Context) {
				calls++
				c.String(tc.status, "original output")
				require.False(t, service.TryCompositeFailover(c))
			}, nil))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"shared-model"}`)))
			require.Equal(t, 1, calls)
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, "original output", w.Body.String())
		})
	}
}

func TestCompositeCandidateHandlerPinsContinuationToOwner(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "known owner", false: "unknown owner"}[known], func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), compositeCandidatesDecision()))
			})
			calls := 0
			router.POST("/v1/responses", compositeCandidateHandler(func(c *gin.Context) {
				calls++
				platform, _ := service.ResolvedTargetPlatformFromContext(c.Request.Context())
				require.Equal(t, service.PlatformCline, platform)
				require.Equal(t, "owner", c.Request.Header.Get("X-Owner-Pin"))
				require.False(t, service.TryCompositeFailover(c))
				c.Status(http.StatusNoContent)
			}, func(c *gin.Context, id string) (string, error) {
				require.Equal(t, "resp_bound", id)
				if !known {
					return "", errors.New("unknown owner")
				}
				c.Request.Header.Set("X-Owner-Pin", "owner")
				return service.PlatformCline, nil
			}))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"shared-model","previous_response_id":"resp_bound"}`)))
			if known {
				require.Equal(t, 1, calls)
				require.Equal(t, http.StatusNoContent, w.Code)
			} else {
				require.Zero(t, calls)
				require.Equal(t, http.StatusBadRequest, w.Code)
			}
		})
	}
}

func TestCompositeCandidateOrderPreservesSessionAndPriority(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("X-Claude-Code-Session-Id", "stable-session")
	decision := compositeCandidatesDecision()
	decision.Candidates[1].Priority = 1
	first := orderCompositeCandidates(c, decision, nil)
	for range 20 {
		require.Equal(t, first, orderCompositeCandidates(c, decision, nil))
	}
	decision.Candidates[1].Priority = 0
	require.Equal(t, service.PlatformCline, orderCompositeCandidates(c, decision, nil)[0].TargetPlatform)
	require.Equal(t, service.PlatformAnthropic, decision.Candidates[0].TargetPlatform)
}

func TestCompositeCandidateHandlerDoesNotReplayBackgroundJobs(t *testing.T) {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), compositeCandidatesDecision()))
	})
	calls := 0
	router.POST("/v1/responses", compositeCandidateHandler(func(c *gin.Context) {
		calls++
		require.True(t, service.AdmitCompositeAccountAttempt(c.Request.Context(), 1, 3))
		require.False(t, service.TryCompositeFailover(c))
		c.JSON(http.StatusBadGateway, gin.H{"error": "job creation failed"})
	}, nil))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"shared-model","background":true}`)))
	require.Equal(t, 1, calls)
	require.Equal(t, http.StatusBadGateway, w.Code)
}

func TestCompositeCandidateHandlerChecksContinuationAfterOwnerPoolChanges(t *testing.T) {
	for _, source := range []string{service.CompositeRouteSourceAccount, service.CompositeRouteSourceDetector} {
		for _, ownerAvailable := range []bool{true, false} {
			t.Run(source+map[bool]string{true: "/owner available", false: "/owner removed"}[ownerAvailable], func(t *testing.T) {
				decision := compositeCandidatesDecision()
				decision.Source, decision.Candidates = source, nil
				decision.TargetPlatform = service.PlatformCline
				if !ownerAvailable {
					decision.TargetPlatform = service.PlatformAnthropic
				}
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
				})
				ownerLookups, forwards := 0, 0
				router.POST("/v1/responses", compositeCandidateHandler(func(c *gin.Context) {
					forwards++
					accountID, pinned := service.CompositeResponseAccountFromContext(c.Request.Context())
					require.True(t, pinned)
					require.Equal(t, int64(91), accountID)
					require.False(t, service.TryCompositeFailover(c))
					c.Status(http.StatusNoContent)
				}, func(c *gin.Context, responseID string) (string, error) {
					ownerLookups++
					require.Equal(t, "resp_bound", responseID)
					c.Request = c.Request.WithContext(service.WithCompositeResponseAccount(c.Request.Context(), 91))
					return service.PlatformCline, nil
				}))
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"shared-model","previous_response_id":"resp_bound"}`)))
				require.Equal(t, 1, ownerLookups, "a reduced candidate set must still authorize and locate the previous response")
				if ownerAvailable {
					require.Equal(t, 1, forwards)
					require.Equal(t, http.StatusNoContent, recorder.Code)
				} else {
					require.Zero(t, forwards, "do not send another provider an account-bound response ID")
					require.Equal(t, http.StatusBadRequest, recorder.Code)
				}
			})
		}
	}
}
