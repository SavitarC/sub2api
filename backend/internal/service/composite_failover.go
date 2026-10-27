package service

import (
	"context"
	"errors"
	"sync"

	"github.com/gin-gonic/gin"
)

type compositeFailoverAttemptKey struct{}

// CompositeFailoverAttempt lets a handler return control before sending its terminal error.
type CompositeFailoverAttempt struct {
	hasNext        bool
	RetryRequested bool
}

func WithCompositeFailoverAttempt(ctx context.Context, hasNext bool) (context.Context, *CompositeFailoverAttempt) {
	attempt := &CompositeFailoverAttempt{hasNext: hasNext}
	return context.WithValue(ctx, compositeFailoverAttemptKey{}, attempt), attempt
}

// TryCompositeFailover is called only for exhausted account pools and explicitly retryable failures.
func TryCompositeFailover(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Writer == nil {
		return false
	}
	ctx := c.Request.Context()
	attempt, _ := ctx.Value(compositeFailoverAttemptKey{}).(*CompositeFailoverAttempt)
	if attempt == nil || !attempt.hasNext || ctx.Err() != nil || geminiCachedContentBoundFromContext(ctx) {
		return false
	}
	if budget, _ := ctx.Value(compositeAccountAttemptBudgetKey{}).(*compositeAccountAttemptBudget); budget != nil {
		request, _ := ctx.Value(compositeRequestKey{}).(compositeRequest)
		if !budget.canSwitch(!request.background) {
			return false
		}
	}
	if StopOpenAICompactSSEKeepaliveCommitted(c) || c.Writer.Written() || IsResponseCommitted(c) {
		return false
	}
	attempt.RetryRequested = true
	return true
}

// TryCompositePlatformQuotaFailover preserves request-wide billing failures while trying another platform's quota.
func TryCompositePlatformQuotaFailover(c *gin.Context, err error) bool {
	if !errors.Is(err, ErrUserPlatformDailyQuotaExhausted) &&
		!errors.Is(err, ErrUserPlatformWeeklyQuotaExhausted) &&
		!errors.Is(err, ErrUserPlatformMonthlyQuotaExhausted) {
		return false
	}
	return TryCompositeFailover(c)
}

type compositeAccountAttemptBudgetKey struct{}

type compositeAccountAttemptBudget struct {
	mu            sync.Mutex
	lastAccountID int64
	maxSwitches   int
	switches      int
}

// WithCompositeAccountAttemptBudget shares the first forwarding handler's switch limit across platforms.
func WithCompositeAccountAttemptBudget(ctx context.Context) context.Context {
	if _, ok := ctx.Value(compositeAccountAttemptBudgetKey{}).(*compositeAccountAttemptBudget); ok {
		return ctx
	}
	return context.WithValue(ctx, compositeAccountAttemptBudgetKey{}, &compositeAccountAttemptBudget{})
}

// AdmitCompositeAccountAttempt counts only accounts about to forward, preserving same-account retries.
func AdmitCompositeAccountAttempt(ctx context.Context, accountID int64, maxSwitches int) bool {
	budget, _ := ctx.Value(compositeAccountAttemptBudgetKey{}).(*compositeAccountAttemptBudget)
	if budget == nil {
		return true
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.lastAccountID == accountID {
		return true
	}
	if budget.lastAccountID == 0 {
		budget.lastAccountID, budget.maxSwitches = accountID, max(0, maxSwitches)
		return true
	}
	if budget.switches >= budget.maxSwitches {
		return false
	}
	budget.switches++
	budget.lastAccountID = accountID
	return true
}

// CompositeAccountAttemptAllowed keeps an exhausted request from binding a new account during selection.
func CompositeAccountAttemptAllowed(ctx context.Context, accountID int64) bool {
	budget, _ := ctx.Value(compositeAccountAttemptBudgetKey{}).(*compositeAccountAttemptBudget)
	if budget == nil {
		return true
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.lastAccountID == 0 || budget.lastAccountID == accountID || budget.switches < budget.maxSwitches
}

func (b *compositeAccountAttemptBudget) canSwitch(allowReplay bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastAccountID == 0 || (allowReplay && b.switches < b.maxSwitches)
}

type billingRPMAdmissionKey struct{}

type billingRPMAdmission struct {
	mu      sync.Mutex
	results map[[2]int64]error
}

// WithBillingRPMAdmission counts a client request once when platform attempts re-enter billing checks.
func WithBillingRPMAdmission(ctx context.Context) context.Context {
	if _, ok := ctx.Value(billingRPMAdmissionKey{}).(*billingRPMAdmission); ok {
		return ctx
	}
	return context.WithValue(ctx, billingRPMAdmissionKey{}, &billingRPMAdmission{results: make(map[[2]int64]error)})
}

func (s *BillingCacheService) checkRequestRPM(ctx context.Context, user *User, group *Group) error {
	admission, _ := ctx.Value(billingRPMAdmissionKey{}).(*billingRPMAdmission)
	if admission == nil || user == nil {
		return s.checkRPM(ctx, user, group)
	}
	key := [2]int64{user.ID, 0}
	if group != nil {
		key[1] = group.ID
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if err, checked := admission.results[key]; checked {
		return err
	}
	err := s.checkRPM(ctx, user, group)
	admission.results[key] = err
	return err
}
