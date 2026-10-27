package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompositeFailoverAttemptRequiresUncommittedLiveRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hasNext bool
		prepare func(*gin.Context)
		want    bool
	}{
		{name: "available", hasNext: true, want: true},
		{name: "last platform"},
		{name: "canceled", hasNext: true, prepare: func(c *gin.Context) {
			ctx, cancel := context.WithCancel(c.Request.Context())
			cancel()
			c.Request = c.Request.WithContext(ctx)
		}},
		{name: "headers committed", hasNext: true, prepare: func(c *gin.Context) { c.Writer.WriteHeaderNow() }},
		{name: "stream output", hasNext: true, prepare: func(c *gin.Context) { _, _ = c.Writer.WriteString("data: output\n\n") }},
		{name: "response marked", hasNext: true, prepare: MarkResponseCommitted},
		{name: "bound cache", hasNext: true, prepare: func(c *gin.Context) {
			c.Request = c.Request.WithContext(WithGeminiCachedContentBound(c.Request.Context()))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			ctx, attempt := WithCompositeFailoverAttempt(context.Background(), tc.hasNext)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			if tc.prepare != nil {
				tc.prepare(c)
			}
			before := w.Body.String()
			require.Equal(t, tc.want, TryCompositeFailover(c))
			require.Equal(t, tc.want, attempt.RetryRequested)
			require.Equal(t, before, w.Body.String())
		})
	}
	require.False(t, TryCompositeFailover(nil))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.False(t, TryCompositeFailover(c), "ordinary requests must keep their existing error path")
}

func TestCompositeFailoverOnlyRetriesPlatformQuotaErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{ErrUserPlatformDailyQuotaExhausted, true},
		{ErrUserPlatformWeeklyQuotaExhausted, true},
		{ErrUserPlatformMonthlyQuotaExhausted, true},
		{ErrInsufficientBalance, false},
		{ErrAPIKeyRateLimit5hExceeded, false},
		{ErrUserRPMExceeded, false},
		{ErrGroupRPMExceeded, false},
		{ErrBillingServiceUnavailable, false},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			ctx, attempt := WithCompositeFailoverAttempt(context.Background(), true)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			require.Equal(t, tc.want, TryCompositePlatformQuotaFailover(c, tc.err))
			require.Equal(t, tc.want, attempt.RetryRequested)
		})
	}
}

type compositeRPMCache struct {
	UserRPMCache
	userCalls  int
	groupCalls int
	count      int
}

func (c *compositeRPMCache) IncrementUserRPM(context.Context, int64) (int, error) {
	c.userCalls++
	return c.count, nil
}

func (c *compositeRPMCache) IncrementUserGroupRPM(context.Context, int64, int64) (int, error) {
	c.groupCalls++
	return c.count, nil
}

func TestCompositeBillingRPMAdmissionIsSharedAcrossAttempts(t *testing.T) {
	cache := &compositeRPMCache{count: 1}
	svc := &BillingCacheService{userRPMCache: cache}
	user, group := &User{ID: 3, RPMLimit: 1}, &Group{ID: 7, RPMLimit: 1}
	ctx := WithBillingRPMAdmission(context.Background())
	require.Same(t, ctx, WithBillingRPMAdmission(ctx))
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attemptCtx, _ := WithCompositeFailoverAttempt(ctx, true)
			results <- svc.checkRequestRPM(attemptCtx, user, group)
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Equal(t, 1, cache.userCalls)
	require.Equal(t, 1, cache.groupCalls)
	require.NoError(t, svc.checkRequestRPM(WithBillingRPMAdmission(context.Background()), user, group))
	require.Equal(t, 2, cache.userCalls, "a separate client request must consume RPM")
}

func TestCompositeBillingRPMAdmissionPreservesDenial(t *testing.T) {
	cache := &compositeRPMCache{count: 2}
	svc := &BillingCacheService{userRPMCache: cache}
	ctx := WithBillingRPMAdmission(context.Background())
	for i := 0; i < 2; i++ {
		require.ErrorIs(t, svc.checkRequestRPM(ctx, &User{ID: 3, RPMLimit: 1}, nil), ErrUserRPMExceeded)
	}
	require.Equal(t, 1, cache.userCalls)
}

type compositeBillingCache struct {
	BillingCache
	balance       float64
	balanceChecks int
	quotaChecks   []string
	quotas        map[string]*UserPlatformQuotaCacheEntry
}

func (c *compositeBillingCache) GetUserBalance(context.Context, int64) (float64, error) {
	c.balanceChecks++
	return c.balance, nil
}

func (c *compositeBillingCache) GetUserPlatformQuotaCache(_ context.Context, _ int64, platform string) (*UserPlatformQuotaCacheEntry, bool, error) {
	c.quotaChecks = append(c.quotaChecks, platform)
	return c.quotas[platform], true, nil
}

type compositeQuotaRepo struct{ UserPlatformQuotaRepository }

func TestCompositeBillingRechecksBalanceAndActualPlatformQuota(t *testing.T) {
	now, limit := time.Now(), 1.0
	entry := func(usage float64) *UserPlatformQuotaCacheEntry {
		return &UserPlatformQuotaCacheEntry{
			SchemaVersion: UserPlatformQuotaCacheSchemaV1, DailyLimitUSD: &limit, DailyUsageUSD: usage,
			DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &now,
		}
	}
	cache := &compositeBillingCache{balance: 5, quotas: map[string]*UserPlatformQuotaCacheEntry{
		PlatformAnthropic: entry(0), PlatformCommandCode: entry(1), PlatformCline: entry(0),
	}}
	rpm := &compositeRPMCache{count: 1}
	svc := &BillingCacheService{cache: cache, userRPMCache: rpm, userPlatformQuotaRepo: &compositeQuotaRepo{}}
	user, group := &User{ID: 3, RPMLimit: 1}, &Group{ID: 7, RPMLimit: 1}
	ctx := WithBillingRPMAdmission(context.Background())
	require.NoError(t, svc.CheckBillingEligibility(ctx, user, nil, group, nil, PlatformAnthropic))
	require.ErrorIs(t, svc.CheckBillingEligibility(ctx, user, nil, group, nil, PlatformCommandCode), ErrUserPlatformDailyQuotaExhausted)
	require.NoError(t, svc.CheckBillingEligibility(ctx, user, nil, group, nil, PlatformCline))
	cache.balance = 0
	require.ErrorIs(t, svc.CheckBillingEligibility(ctx, user, nil, group, nil, PlatformAnthropic), ErrInsufficientBalance)
	require.Equal(t, []string{PlatformAnthropic, PlatformCommandCode, PlatformCline}, cache.quotaChecks)
	require.Equal(t, 4, cache.balanceChecks)
	require.Equal(t, 1, rpm.userCalls)
	require.Equal(t, 1, rpm.groupCalls)
}

func TestCompositeAccountAttemptBudgetSpansPlatformsAndPreservesSameAccountRetries(t *testing.T) {
	base := WithCompositeAccountAttemptBudget(context.Background())
	require.Same(t, base, WithCompositeAccountAttemptBudget(base))
	for i := 0; i < 3; i++ {
		ctx, state := WithCompositeFailoverAttempt(base, true)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
		require.True(t, TryCompositeFailover(c), "empty platforms do not consume account attempts")
		require.True(t, state.RetryRequested)
	}
	first, _ := WithCompositeFailoverAttempt(base, true)
	require.True(t, AdmitCompositeAccountAttempt(first, 10, 1))
	require.True(t, AdmitCompositeAccountAttempt(first, 10, 1), "same-account retries retain their own budget")
	require.True(t, CompositeAccountAttemptAllowed(first, 11))
	require.True(t, AdmitCompositeAccountAttempt(first, 11, 1))
	second, state := WithCompositeFailoverAttempt(base, true)
	require.False(t, CompositeAccountAttemptAllowed(second, 20))
	require.False(t, AdmitCompositeAccountAttempt(second, 20, 10), "later handler defaults cannot increase the request budget")
	require.True(t, CompositeAccountAttemptAllowed(second, 11))
	require.True(t, AdmitCompositeAccountAttempt(second, 11, 10))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(second)
	require.False(t, TryCompositeFailover(c))
	require.False(t, state.RetryRequested)
}

func TestCompositeAccountAttemptBudgetCountsThePlatformSwitch(t *testing.T) {
	base := WithCompositeAccountAttemptBudget(context.Background())
	first, _ := WithCompositeFailoverAttempt(base, true)
	require.True(t, AdmitCompositeAccountAttempt(first, 10, 1))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(first)
	require.True(t, TryCompositeFailover(c))
	second, _ := WithCompositeFailoverAttempt(base, true)
	require.True(t, AdmitCompositeAccountAttempt(second, 20, 1))
	require.False(t, AdmitCompositeAccountAttempt(second, 21, 1))
	require.False(t, AdmitCompositeAccountAttempt(second, 30, 1))
}

func TestCompositeBackgroundOnlySwitchesBeforeForwarding(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := WithCompositeRequest(context.Background(), []byte(`{"background":true}`), "/v1/responses")
	ctx = WithCompositeAccountAttemptBudget(ctx)
	ctx, _ = WithCompositeFailoverAttempt(ctx, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	require.True(t, TryCompositeFailover(c), "an incompatible provider can be skipped before a job is submitted")
	ctx, _ = WithCompositeFailoverAttempt(ctx, true)
	c.Request = c.Request.WithContext(ctx)
	require.True(t, AdmitCompositeAccountAttempt(ctx, 10, 3))
	require.False(t, TryCompositeFailover(c), "a submitted background job cannot be replayed on another provider")
}
