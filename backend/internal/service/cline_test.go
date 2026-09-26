//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// clineTestAccount 只带 api_key：端点与测试模型全部来自 provider profile。
func clineTestAccount(id int64, mode string) *Account {
	credentials := map[string]any{"api_key": "cline_test_key", "api_protocol": APIProtocolAdaptive}
	if mode != "" {
		credentials["account_mode"] = mode
	}
	return &Account{
		ID:          id,
		Name:        "cline",
		Platform:    PlatformCline,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: credentials,
	}
}

func TestClineProfileOnlyChatCompletions(t *testing.T) {
	account := clineTestAccount(1, "")
	require.True(t, account.IsMultiProtocolAPIKey())
	require.True(t, account.RoutesProtocolByInbound())
	require.False(t, account.routesByModel())
	require.Equal(t, APIProtocolAdaptive, account.GetAPIProtocol())
	require.Equal(t, DefaultClineBaseURL, account.GetOpenAIBaseURL())
	require.False(t, account.SupportsNativeCNResponses())
	require.False(t, account.providerSupportsProtocol(APIProtocolAnthropic))
	for _, inbound := range []string{APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic} {
		require.Equal(t, APIProtocolChatCompletions, resolveUpstreamProtocol(account, inbound, "anthropic/claude-sonnet-4-6", nil), inbound)
	}

	require.False(t, account.IsClinePass())
	require.Equal(t, DefaultClineTestModel, account.providerDefaultTestModel())
	pass := clineTestAccount(2, AccountModePass)
	require.True(t, pass.IsClinePass())
	require.Equal(t, DefaultClineBaseURL, pass.GetOpenAIBaseURL())
	require.Equal(t, DefaultClinePassTestModel, pass.providerDefaultTestModel())
}

// 经真实入口：三种入站都转换 / 直通到 Chat Completions，模型名带厂商前缀原样发往上游。
func TestClineGatewayForwardsToChatCompletions(t *testing.T) {
	for _, ingress := range routingMatrixIngresses() {
		upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		body := routingMatrixCase{ingress: ingress, model: "anthropic/claude-sonnet-4-6"}.body()
		_ = ingress.forward(svc, adaptiveProtocolTestContext(ingress.path, body), clineTestAccount(3, ""), body)

		require.NotEmpty(t, upstream.requests, ingress.name)
		require.Equal(t, "https://api.cline.bot/api/v1/chat/completions", upstream.requests[len(upstream.requests)-1].URL.String(), ingress.name)
		require.Equal(t, "Bearer cline_test_key", upstream.requests[len(upstream.requests)-1].Header.Get("Authorization"), ingress.name)
		require.True(t, gjson.GetBytes(upstream.lastBody, "messages").Exists(), ingress.name)
		require.Equal(t, "anthropic/claude-sonnet-4-6", gjson.GetBytes(upstream.lastBody, "model").String(), ingress.name)
	}
}

// 连接测试未指定模型时按接入模式取默认测试模型，自适应与固定 Chat Completions 一致。
func TestAccountTestService_ClineDefaultTestModelByMode(t *testing.T) {
	cases := []struct {
		mode, apiProtocol, want string
	}{
		{"", "", DefaultClineTestModel},
		{AccountModePass, "", DefaultClinePassTestModel},
		{AccountModePayG, APIProtocolChatCompletions, DefaultClineTestModel},
		{AccountModePass, APIProtocolChatCompletions, DefaultClinePassTestModel},
	}
	for i, tc := range cases {
		account := clineTestAccount(int64(600+i), tc.mode)
		if tc.apiProtocol != "" {
			account.Credentials["api_protocol"] = tc.apiProtocol
		}
		svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNChatTestResponse())
		c, recorder := newTestContext()

		err := svc.TestAccountConnection(c, account.ID, "", "hi", AccountTestModeDefault)

		require.NoError(t, err, tc)
		require.Len(t, upstream.requests, 1, tc)
		require.Equal(t, "https://api.cline.bot/api/v1/chat/completions", upstream.requests[0].URL.String(), tc)
		require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "model").String(), tc)
		require.Contains(t, recorder.Body.String(), `"type":"test_complete"`, tc)
	}
}

// 响应体样例取自 Cline 客户端的判定文案与官方错误文档。
const (
	clineInsufficientCreditsBody = `{"error":{"code":"insufficient_credits","message":"Insufficient credits","details":{"current_balance":0}}}`
	clineSpendLimitBody          = `{"error":{"code":"SPEND_LIMIT_EXCEEDED","message":"Organization spend limit exceeded"}}`
	clineInferenceCapBody        = `{"error":{"code":"INFERENCE_CAP_ERROR","message":"Inference cap reached"}}`
	clinePass5hBody              = `{"error":{"code":429,"message":"You have reached your 5-hour ClinePass limit. Please try again later."}}`
	clinePassWeeklyBody          = `{"error":{"code":429,"message":"You have reached your weekly ClinePass limit. Please try again later."}}`
	clinePassMonthlyBody         = `{"error":{"code":429,"message":"You have reached your monthly ClinePass limit. Please try again later."}}`
	clineNotSubscribedBody       = `{"error":{"code":403,"message":"The user is not subscribed to required model plan"}}`
	clineOrgPassBody             = `{"error":{"code":403,"message":"Organization accounts cannot use individual model inference subscriptions"}}`
	clineFreeModelLimitBody      = `{"error":{"code":429,"message":"Free limit reached on model cline-free/kimi-k3. Try again in 3h 20m"}}`
	clineRateLimitBody           = `{"error":{"code":429,"message":"Rate limit exceeded"}}`
)

func TestClassifyClineError(t *testing.T) {
	cases := map[string]clineErrorKind{
		clineInsufficientCreditsBody: clineCreditsExhausted,
		clineSpendLimitBody:          clineSpendLimit,
		clineInferenceCapBody:        clineSpendLimit,
		clinePass5hBody:              clinePassLimit,
		clinePassWeeklyBody:          clinePassLimit,
		clineNotSubscribedBody:       clinePassUnavailable,
		clineOrgPassBody:             clinePassUnavailable,
		clineFreeModelLimitBody:      clineFreeModelLimit,
		clineRateLimitBody:           clineErrorNone,
		`{"error":{"message":"You have reached your limit. Please try again later."}}`: clineErrorNone,
	}
	for body, want := range cases {
		require.Equal(t, want, classifyClineError([]byte(body)), body)
	}

	require.Equal(t, clineLimitRecheck, clinePassLimitCooldown([]byte(clinePass5hBody)))
	require.Equal(t, clinePassWeeklyRecheck, clinePassLimitCooldown([]byte(clinePassWeeklyBody)))
	require.Equal(t, clinePassMonthlyRecheck, clinePassLimitCooldown([]byte(clinePassMonthlyBody)))
	require.Equal(t, 3*time.Hour+20*time.Minute, clineFreeModelResetAfter([]byte(clineFreeModelLimitBody)))
	require.Zero(t, clineFreeModelResetAfter([]byte(`Free limit reached on model x. Try again in 3 weeks`)))
}

// 错误所属计费方式与接入模式一致：整号冷却；否则只限当前模型。
func TestHandleUpstreamError_ClineScopeFollowsAccountMode(t *testing.T) {
	ctx := withTempUnschedulableModel(context.Background(), []string{"cline-pass/glm-5.3"})

	// ClinePass 账号：ClinePass 超限整号冷却到窗口重新确认点，429 不置位 shouldDisable。
	pass := &commandCodeRateLimitRepo{}
	shouldDisable := NewRateLimitService(pass, nil, &config.Config{}, nil, nil).HandleUpstreamError(ctx, clineTestAccount(10, AccountModePass),
		http.StatusTooManyRequests, http.Header{}, []byte(clinePassWeeklyBody))
	require.False(t, shouldDisable)
	require.Empty(t, pass.modelLimits)
	require.Equal(t, 1, pass.tempCalls)
	require.True(t, strings.HasPrefix(pass.lastTempReason, clinePassLimitReason), pass.lastTempReason)
	require.WithinDuration(t, time.Now().Add(clinePassWeeklyRecheck), pass.tempUntil, time.Minute)
	require.Equal(t, 0, pass.setErrorCalls)
	require.Equal(t, 0, pass.rateLimitedCalls)

	// 按量计费账号：ClinePass 超限只限当前模型。
	payg := &commandCodeRateLimitRepo{}
	shouldDisable = NewRateLimitService(payg, nil, &config.Config{}, nil, nil).HandleUpstreamError(ctx, clineTestAccount(11, AccountModePayG),
		http.StatusTooManyRequests, http.Header{}, []byte(clinePass5hBody))
	require.False(t, shouldDisable)
	require.Equal(t, 0, payg.tempCalls)
	require.WithinDuration(t, time.Now().Add(clineLimitRecheck), payg.modelLimits["cline-pass/glm-5.3"], time.Minute)
	require.Equal(t, clinePassLimitReason, payg.reasons["cline-pass/glm-5.3"])

	// ClinePass 账号请求按量模型遇到积分不足：只限当前模型，不按余额停调整号。
	paidCtx := withTempUnschedulableModel(context.Background(), []string{"anthropic/claude-sonnet-4-6"})
	passCredits := &commandCodeRateLimitRepo{}
	shouldDisable = NewRateLimitService(passCredits, nil, &config.Config{}, nil, nil).HandleUpstreamError(paidCtx, clineTestAccount(12, AccountModePass),
		http.StatusPaymentRequired, http.Header{}, []byte(clineInsufficientCreditsBody))
	require.False(t, shouldDisable)
	require.Equal(t, 0, passCredits.tempCalls)
	require.Equal(t, clineCreditsModelLimitReason, passCredits.reasons["anthropic/claude-sonnet-4-6"])

	// 模型未知时整号冷却。
	unknown := &commandCodeRateLimitRepo{}
	NewRateLimitService(unknown, nil, &config.Config{}, nil, nil).HandleUpstreamError(context.Background(), clineTestAccount(13, AccountModePayG),
		http.StatusForbidden, http.Header{}, []byte(clineNotSubscribedBody))
	require.Equal(t, 1, unknown.tempCalls)
	require.True(t, strings.HasPrefix(unknown.lastTempReason, clinePassUnavailableReason), unknown.lastTempReason)
}

// 按量积分不足（402）是可恢复状态：临时停调并标记 balance_low，不能永久置 error。
func TestHandleUpstreamError_Cline402PausesInsteadOfError(t *testing.T) {
	for _, body := range []string{clineInsufficientCreditsBody, `{"error":{"code":402,"message":"Payment required"}}`} {
		repo := &rateLimitAccountRepoStub{}
		service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		ctx := withTempUnschedulableModel(context.Background(), []string{"anthropic/claude-sonnet-4-6"})

		shouldDisable := service.HandleUpstreamError(ctx, clineTestAccount(20, ""), http.StatusPaymentRequired, http.Header{}, []byte(body))

		require.True(t, shouldDisable, body)
		require.Equal(t, 0, repo.setErrorCalls, body)
		require.Equal(t, 1, repo.tempCalls, body)
		require.Contains(t, repo.lastTempReason, cnBalanceLowReasonPrefix, body)
		require.Equal(t, true, repo.lastExtraUpdates["cline_balance_low"], body)
	}
}

func TestHandleUpstreamError_ClineSpendLimitAndFreeModel(t *testing.T) {
	ctx := withTempUnschedulableModel(context.Background(), []string{"cline-free/kimi-k3"})

	spend := &commandCodeRateLimitRepo{}
	NewRateLimitService(spend, nil, &config.Config{}, nil, nil).HandleUpstreamError(ctx, clineTestAccount(30, ""),
		http.StatusTooManyRequests, http.Header{}, []byte(clineSpendLimitBody))
	require.Equal(t, 1, spend.tempCalls)
	require.True(t, strings.HasPrefix(spend.lastTempReason, clineSpendLimitReason), spend.lastTempReason)
	require.WithinDuration(t, time.Now().Add(clineLimitRecheck), spend.tempUntil, time.Minute)

	// 免费模型的每日额度只限该模型（与接入模式无关），冷却到文案中的时间。
	free := &commandCodeRateLimitRepo{}
	shouldDisable := NewRateLimitService(free, nil, &config.Config{}, nil, nil).HandleUpstreamError(ctx, clineTestAccount(31, AccountModePass),
		http.StatusTooManyRequests, http.Header{}, []byte(clineFreeModelLimitBody))
	require.False(t, shouldDisable)
	require.Equal(t, 0, free.tempCalls)
	require.WithinDuration(t, time.Now().Add(3*time.Hour+20*time.Minute), free.modelLimits["cline-free/kimi-k3"], time.Minute)

	// 普通频率限制交回默认 429 逻辑。
	plain := &commandCodeRateLimitRepo{}
	NewRateLimitService(plain, nil, &config.Config{}, nil, nil).HandleUpstreamError(ctx, clineTestAccount(32, ""),
		http.StatusTooManyRequests, http.Header{}, []byte(clineRateLimitBody))
	require.Empty(t, plain.modelLimits)
	require.Equal(t, 0, plain.tempCalls)
}

func newClineAccountUpstream(me, balance string) *commandCodeAlphaUpstream {
	return &commandCodeAlphaUpstream{responses: map[string]commandCodeAlphaResponse{
		"/api/v1/users/me":          {status: http.StatusOK, body: me},
		"/api/v1/users/u-1/balance": {status: http.StatusOK, body: balance},
	}}
}

func TestClineBalanceProbeConvertsCents(t *testing.T) {
	for _, tc := range []struct{ me, balance string }{
		{`{"id":"u-1","email":"a@example.com"}`, `{"balance":1234,"userId":"u-1"}`},
		{`{"success":true,"data":{"id":"u-1"}}`, `{"success":true,"data":{"balance":"1234","userId":"u-1"}}`},
	} {
		account := clineTestAccount(40, "")
		upstream := newClineAccountUpstream(tc.me, tc.balance)
		repo := &cnBalanceProbeRepo{account: account}
		svc := NewCNProviderBalanceService(repo, nil, upstream, &config.Config{})

		result, err := svc.QueryBalance(context.Background(), account.ID)

		require.NoError(t, err)
		require.True(t, result.Success)
		require.True(t, result.Persisted)
		require.InDelta(t, 12.34, result.Balance, 1e-9)
		require.Equal(t, "USD", result.Currency)
		require.Len(t, upstream.requests, 2)
		require.Equal(t, "Bearer cline_test_key", upstream.requests[0].Header.Get("Authorization"))
		require.InDelta(t, 12.34, repo.extraWrites[0]["cline_balance"], 1e-9)
	}
}

func TestClineBalanceProbeFailures(t *testing.T) {
	account := clineTestAccount(41, "")
	upstream := &commandCodeAlphaUpstream{responses: map[string]commandCodeAlphaResponse{
		"/api/v1/users/me": {status: http.StatusUnauthorized, body: `{"error":"Unauthorized"}`},
	}}
	repo := &cnBalanceProbeRepo{account: account}
	result, err := NewCNProviderBalanceService(repo, nil, upstream, &config.Config{}).QueryBalance(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, http.StatusUnauthorized, result.StatusCode)
	require.Contains(t, result.Error, "HTTP 401")
	require.Empty(t, repo.extraWrites)

	envelope := newClineAccountUpstream(`{"success":false,"error":"token expired"}`, `{}`)
	result, err = NewCNProviderBalanceService(&cnBalanceProbeRepo{account: account}, nil, envelope, &config.Config{}).QueryBalance(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "token expired")
}

// 只查官方主机上的按量计费 API Key 账号：ClinePass 账号与自定义中转不发往官方账号接口。
func TestClineBalanceRequiresUsageBillingOnOfficialHost(t *testing.T) {
	require.NoError(t, validatePayGAccount(clineTestAccount(50, "")))
	require.Error(t, validatePayGAccount(clineTestAccount(51, AccountModePass)))
	relay := clineTestAccount(52, "")
	relay.Credentials["base_url"] = "https://relay.example.com/v1"
	relay.Credentials["api_protocol"] = APIProtocolChatCompletions
	require.Error(t, validatePayGAccount(relay))
}

// clineCheckRepo 为周期检测提供 Cline 账号并记录停调。
type clineCheckRepo struct {
	AccountRepository
	accounts []Account
	mu       sync.Mutex
	paused   []int64
}

func (r *clineCheckRepo) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	if platform == PlatformCline {
		return r.accounts, nil
	}
	return nil, nil
}

func (r *clineCheckRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, errors.New("not found")
}

func (r *clineCheckRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

func (r *clineCheckRepo) SetTempUnschedulable(_ context.Context, id int64, _ time.Time, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = append(r.paused, id)
	return nil
}

func TestCNProviderBalanceCheckRunOnceProbesClineUsageBilling(t *testing.T) {
	payg := *clineTestAccount(1, AccountModePayG)
	pass := *clineTestAccount(2, AccountModePass)
	manualOff := *clineTestAccount(3, "")
	manualOff.Schedulable = false
	relay := *clineTestAccount(4, "")
	relay.Credentials["base_url"] = "https://relay.example.com/v1"
	relay.Credentials["api_protocol"] = APIProtocolChatCompletions

	repo := &clineCheckRepo{accounts: []Account{payg, pass, manualOff, relay}}
	upstream := newClineAccountUpstream(`{"id":"u-1"}`, `{"balance":20,"userId":"u-1"}`)
	cfg := &config.Config{}
	cfg.Gateway.CNProviders.BalanceThreshold = 0.5
	svc := &CNProviderBalanceCheckService{accountRepo: repo, balanceService: NewCNProviderBalanceService(repo, nil, upstream, cfg), cfg: cfg}

	svc.runOnce()

	require.Len(t, upstream.requests, 2, "only the usage-billing account on the official host is probed")
	require.Equal(t, []int64{1}, repo.paused)
}

// 官方主机不做上游计费探测：中转站的计费接口在官方 API 上不存在，只会把 Key 发往官方主机。
func TestClineOfficialHostSkipsUpstreamBillingProbe(t *testing.T) {
	require.True(t, upstreamBillingProbeTargetIsOfficialAPI(DefaultClineBaseURL))
	require.False(t, upstreamBillingProbeTargetIsOfficialAPI("https://relay.example.com/v1"))
}
