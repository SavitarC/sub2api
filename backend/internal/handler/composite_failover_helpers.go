package handler

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func tryCompositeSelectionFailover(c *gin.Context, err error) bool {
	if err != nil && !errors.Is(err, service.ErrNoAvailableAccounts) && !errors.Is(err, service.ErrNoAvailableCompactAccounts) {
		return false
	}
	return service.TryCompositeFailover(c)
}

func tryCompositeUpstreamFailover(c *gin.Context, err *service.UpstreamFailoverError) bool {
	if err == nil || !err.ShouldRetryNextAccount() || err.BoundUpstreamPassthrough ||
		err.IsOpenAIRequestBodyTooLarge() || err.Reason == service.OpenAIHTTPContinuationUnsupportedReason {
		return false
	}
	return service.TryCompositeFailover(c)
}

func tryCompositeAccountConcurrencyFailover(c *gin.Context, err error, slotType string) bool {
	if slotType != "account" {
		return false
	}
	var limit *ConcurrencyError
	var queue *WaitQueueFullError
	if (errors.As(err, &limit) && limit.SlotType == "account") || (errors.As(err, &queue) && queue.SlotType == "account") {
		return service.TryCompositeFailover(c)
	}
	return false
}

func admitCompositeAccountAttempt(c *gin.Context, accountID int64, maxSwitches int, release func()) bool {
	if service.AdmitCompositeAccountAttempt(c.Request.Context(), accountID, maxSwitches) {
		return true
	}
	if release != nil {
		release()
	}
	return false
}
