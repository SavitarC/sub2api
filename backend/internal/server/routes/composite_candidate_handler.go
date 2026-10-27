package routes

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/cespare/xxhash/v2"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// compositeCandidateHandler retries complete provider attempts before any response is committed.
func compositeCandidateHandler(next gin.HandlerFunc, responseOwner func(*gin.Context, string) (string, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		decision, ok := service.CompositeRouteDecisionFromContext(c.Request.Context())
		if !ok || decision.Source == service.CompositeRouteSourceExplicit {
			next(c)
			return
		}
		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			status := http.StatusBadRequest
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				status = http.StatusRequestEntityTooLarge
			}
			c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Failed to read request body"}})
			return
		}
		requestmodel.ResetRequestBody(c.Request, body)
		if err := service.ValidateCompositeRequestBody(body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": err.Error()}})
			return
		}
		previous := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
		if len(decision.Candidates) > 1 && previous == "" && service.CompositeRequestHasStoredInput(body) {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Stored upstream input requires an explicit model route or previous_response_id with a known account"}})
			return
		}
		if len(decision.Candidates) == 0 {
			if previous == "" {
				next(c)
				return
			}
			decision.Candidates = []service.CompositeRouteCandidate{{TargetPlatform: decision.TargetPlatform}}
		}
		candidates := orderCompositeCandidates(c, decision, body)
		if decision.Endpoint == service.CompositeRouteEndpointResponses && strings.Contains(strings.TrimRight(c.Request.URL.Path, "/"), "/responses/") {
			filtered := candidates[:0]
			for _, candidate := range candidates {
				if domain.UsesOpenAIGateway(candidate.TargetPlatform) {
					filtered = append(filtered, candidate)
				}
			}
			candidates = filtered
			if len(candidates) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "No model provider supports this Responses operation"}})
				return
			}
		}
		if previous != "" {
			platform := ""
			if responseOwner != nil {
				platform, err = responseOwner(c, previous)
			}
			if err != nil || platform == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Cannot resolve the original account for previous_response_id; use an explicit model route or resend the full conversation"}})
				return
			}
			candidates = candidatesForCompositeOwner(candidates, platform)
			if len(candidates) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "The original response provider no longer exposes this model in the group"}})
				return
			}
		}
		baseCtx := service.WithCompositeRequest(c.Request.Context(), body, c.Request.URL.Path)
		baseCtx = service.WithBillingRPMAdmission(baseCtx)
		baseCtx = service.WithCompositeAccountAttemptBudget(baseCtx)
		if decision.Endpoint != service.CompositeRouteEndpointCountTokens {
			baseCtx, _ = service.WithGatewayTokenRequestPricing(baseCtx)
		}
		baseRequest := c.Request.WithContext(baseCtx)
		baseHeaders := c.Writer.Header().Clone()
		baseStatus := c.Writer.Status()
		for i, candidate := range candidates {
			attempt := c.Copy()
			attempt.Writer = c.Writer
			attemptDecision := decision
			attemptDecision.TargetPlatform = candidate.TargetPlatform
			attemptCtx := service.WithCompositeRouteDecision(baseCtx, attemptDecision)
			attemptCtx, state := service.WithCompositeFailoverAttempt(attemptCtx, i+1 < len(candidates))
			attempt.Request = baseRequest.Clone(attemptCtx)
			requestmodel.ResetRequestBody(attempt.Request, body)
			if i > 0 {
				headers := c.Writer.Header()
				clear(headers)
				for key, values := range baseHeaders {
					headers[key] = append([]string(nil), values...)
				}
				c.Writer.WriteHeader(baseStatus)
			}
			next(attempt)
			if state.RetryRequested && !c.Writer.Written() && baseCtx.Err() == nil {
				continue
			}
			// Outer middleware observes only the final attempt; detached usage work owns its copy.
			c.Request = attempt.Request
			c.Keys = attempt.Copy().Keys
			c.Errors = append(c.Errors, attempt.Errors...)
			return
		}
	}
}

func candidatesForCompositeOwner(candidates []service.CompositeRouteCandidate, platform string) []service.CompositeRouteCandidate {
	for _, candidate := range candidates {
		if candidate.TargetPlatform == platform {
			return []service.CompositeRouteCandidate{candidate}
		}
	}
	return nil
}

func orderCompositeCandidates(c *gin.Context, decision service.CompositeRouteDecision, body []byte) []service.CompositeRouteCandidate {
	candidates := append([]service.CompositeRouteCandidate(nil), decision.Candidates...)
	session := service.ExtractClientSessionID(c)
	if session == "" {
		for _, path := range []string{"prompt_cache_key", "metadata.user_id", "session_id"} {
			if value := gjson.GetBytes(body, path).String(); value != "" {
				session = value
				break
			}
		}
	}
	if session == "" {
		session = fmt.Sprint(rand.Uint64())
	}
	seed := fmt.Sprintf("%d:%s:%s:", decision.GroupID, decision.PublicModel, session)
	// Equal-priority providers share traffic while explicit sessions retain a stable order.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return xxhash.Sum64String(seed+candidates[i].TargetPlatform) > xxhash.Sum64String(seed+candidates[j].TargetPlatform)
	})
	return candidates
}
