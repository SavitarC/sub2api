package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIFastPolicyRepoStub struct {
	values        map[string]string
	getValueCalls atomic.Int64
}

type orderedOpenAIFastPolicyRepo struct {
	*openAIFastPolicyRepoStub
	setCalls       atomic.Int64
	firstPersisted chan struct{}
	releaseFirst   chan struct{}
	secondEntered  chan struct{}
}

type blockingOpenAIFastPolicyReadRepo struct {
	*openAIFastPolicyRepoStub
	enteredOnce sync.Once
	entered     chan struct{}
	release     chan struct{}
	calls       atomic.Int64
}

func (s *blockingOpenAIFastPolicyReadRepo) GetValue(ctx context.Context, key string) (string, error) {
	s.calls.Add(1)
	s.enteredOnce.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return s.openAIFastPolicyRepoStub.GetValue(context.Background(), key)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *orderedOpenAIFastPolicyRepo) Set(ctx context.Context, key, value string) error {
	call := s.setCalls.Add(1)
	if err := s.openAIFastPolicyRepoStub.Set(ctx, key, value); err != nil {
		return err
	}
	if call == 1 {
		close(s.firstPersisted)
		<-s.releaseFirst
	} else if call == 2 {
		close(s.secondEntered)
	}
	return nil
}

func (s *openAIFastPolicyRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get call")
}

func (s *openAIFastPolicyRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	s.getValueCalls.Add(1)
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (s *openAIFastPolicyRepoStub) Set(ctx context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func (s *openAIFastPolicyRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (s *openAIFastPolicyRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *openAIFastPolicyRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *openAIFastPolicyRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func newOpenAIGatewayServiceWithSettings(t *testing.T, settings *OpenAIFastPolicySettings) *OpenAIGatewayService {
	t.Helper()
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	if settings != nil {
		raw, err := json.Marshal(settings)
		require.NoError(t, err)
		repo.values[SettingKeyOpenAIFastPolicySettings] = string(raw)
	}
	return &OpenAIGatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
}

func openAIFastFilterPriorityPolicy() *OpenAIFastPolicySettings {
	return &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionFilter,
			Scope:          BetaPolicyScopeAll,
			ModelWhitelist: []string{},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
}

func openAIFastForcePriorityFallbackBlockPolicy() *OpenAIFastPolicySettings {
	return &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:          OpenAIFastTierAny,
			Action:               OpenAIFastPolicyActionForcePriority,
			Scope:                BetaPolicyScopeAll,
			ModelWhitelist:       []string{"gpt-5.5"},
			FallbackAction:       BetaPolicyActionBlock,
			FallbackErrorMessage: "model is not eligible for priority routing",
		}},
	}
}

func TestEvaluateOpenAIFastPolicy_DefaultPassesKnownTiers(t *testing.T) {
	require.Empty(t, DefaultOpenAIFastPolicySettings().Rules, "default policy must not rewrite service_tier unless admin configured rules")

	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5-turbo", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierFlex)
	require.Equal(t, BetaPolicyActionPass, action)

	// empty tier → pass
	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", "")
	require.Equal(t, BetaPolicyActionPass, action)
}

func TestEvaluateOpenAIFastPolicy_BlockRuleCarriesMessage(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionBlock,
			Scope:          BetaPolicyScopeAll,
			ErrorMessage:   "fast mode is not allowed",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	action, msg := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionBlock, action)
	require.Equal(t, "fast mode is not allowed", msg)
}

func TestEvaluateOpenAIFastPolicy_ScopeFiltersOAuth(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      BetaPolicyActionFilter,
			Scope:       BetaPolicyScopeOAuth,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)

	// OAuth account → rule matches
	oauthAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), oauthAccount, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionFilter, action)

	// API Key account → rule skipped → pass
	apiKeyAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), apiKeyAccount, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)
}

func TestEvaluateOpenAIFastPolicy_UserScopedRuleOverridesGlobalRule(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      BetaPolicyActionFilter,
				Scope:       BetaPolicyScopeAll,
			},
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      BetaPolicyActionPass,
				Scope:       BetaPolicyScopeAll,
				UserIDs:     []int64{42},
			},
		},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	allowedUserCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	action, _ := svc.evaluateOpenAIFastPolicy(allowedUserCtx, account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	otherUserCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(43))
	action, _ = svc.evaluateOpenAIFastPolicy(otherUserCtx, account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionFilter, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionFilter, action)
}

func TestApplyOpenAIFastPolicyToBody_DefaultPassesPriorityAndFast(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority","messages":[]}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))

	body = []byte(`{"model":"gpt-5.5","service_tier":"fast"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())

	body = []byte(`{"model":"gpt-4","service_tier":"priority"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-4", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))

	// No service_tier → no-op
	body = []byte(`{"model":"gpt-5.5"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIFastPolicyToBody_ExplicitFilterRemovesField(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, openAIFastFilterPriorityPolicy())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority","messages":[]}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.NotContains(t, string(updated), `"service_tier"`)

	body = []byte(`{"model":"gpt-5.5","service_tier":"fast"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.NotContains(t, string(updated), `"service_tier"`)
}

func TestApplyOpenAIFastPolicyToBody_UserScopedRuleOverridesGlobalRule(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      BetaPolicyActionFilter,
				Scope:       BetaPolicyScopeAll,
			},
			{
				ServiceTier: OpenAIFastTierPriority,
				Action:      BetaPolicyActionPass,
				Scope:       BetaPolicyScopeAll,
				UserIDs:     []int64{42},
			},
		},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-5.5","service_tier":"priority"}`)

	allowedUserCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	updated, err := svc.applyOpenAIFastPolicyToBody(allowedUserCtx, account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())

	otherUserCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(43))
	updated, err = svc.applyOpenAIFastPolicyToBody(otherUserCtx, account, "gpt-5.5", body)
	require.NoError(t, err)
	require.NotContains(t, string(updated), `"service_tier"`)
}

func TestApplyOpenAIFastPolicyToBody_ForcePriorityRewritesKnownTier(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      OpenAIFastPolicyActionForcePriority,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tier := range []string{"flex", "auto", "default", "scale", "fast", "priority"} {
		body := []byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err)
		require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String(),
			"tier %q should be forced to priority", tier)
	}
}

func TestApplyOpenAIFastPolicyToBody_ForcePriorityInjectsMissingTier(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			// Legacy/current UI configurations may still store priority here.
			// The force action itself must be unconditional on the input tier.
			ServiceTier: OpenAIFastTierPriority,
			Action:      OpenAIFastPolicyActionForcePriority,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-5.5","messages":[]}`)

	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String())
}

func TestApplyOpenAIFastPolicyToBody_ForcePriorityFallbackInjectsMissingTier(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionPass,
			Scope:          BetaPolicyScopeAll,
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: OpenAIFastPolicyActionForcePriority,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-4.1","messages":[]}`)

	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-4.1", body)
	require.NoError(t, err)
	require.Equal(t, OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String())
}

// TestApplyOpenAIFastPolicyToBody_OfficialTiersBypassDefaultRule 验证默认配置
// 下客户端显式发送的 OpenAI 官方合法 tier 能透传到上游而不被静默剥离。
func TestApplyOpenAIFastPolicyToBody_OfficialTiersBypassDefaultRule(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tier := range []string{"auto", "default", "scale"} {
		body := []byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err, "tier %q should pass without error", tier)
		require.Contains(t, string(updated), `"service_tier":"`+tier+`"`,
			"tier %q should be preserved in body under default policy", tier)
	}

	// evaluate 层也应判定为 pass（默认配置没有内置规则）。
	for _, tier := range []string{"auto", "default", "scale"} {
		action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", tier)
		require.Equal(t, BetaPolicyActionPass, action, "tier %q should evaluate to pass", tier)
	}
}

// TestApplyOpenAIFastPolicyToBody_AllRuleStripsOfficialTiers 验证管理员显式配置
// ServiceTier=all + Action=filter 规则后，auto/default/scale 等官方 tier 也会
// 被剥离。这是符合预期的——首条匹配 short-circuit，"all" 覆盖任意已识别 tier。
func TestApplyOpenAIFastPolicyToBody_AllRuleStripsOfficialTiers(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      BetaPolicyActionFilter,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tier := range []string{"auto", "default", "scale", "priority", "flex"} {
		body := []byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err)
		require.NotContains(t, string(updated), `"service_tier"`,
			"tier %q should be stripped under ServiceTier=all + filter rule", tier)
	}
}

// TestApplyOpenAIFastPolicyToBody_UnknownTierStripped 验证真未知 tier 仍被剥离
// （normalize 返回 nil → normalizeResponsesBodyServiceTier 删除字段；
// applyOpenAIFastPolicyToBody 在 normTier 为空时直接 no-op，因为字段已不可能存在
// 于经过前置归一化的请求里。这里直接调 apply 验证它对未识别值不会异常）。
func TestApplyOpenAIFastPolicyToBody_UnknownTierStripped(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	// normalize 阶段会将未知值剥离
	require.Nil(t, normalizeOpenAIServiceTier("xxx"))

	// applyOpenAIFastPolicyToBody 收到未识别 tier 时不报错，body 透传不变
	// （不属于本函数职责——上层 normalizeResponsesBodyServiceTier 已剥离）
	body := []byte(`{"model":"gpt-5.5","service_tier":"xxx"}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIFastPolicyToBody_BlockReturnsTypedError(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionBlock,
			Scope:          BetaPolicyScopeAll,
			ErrorMessage:   "fast mode is blocked for gpt-5.5",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority"}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.Error(t, err)
	var blocked *OpenAIFastBlockedError
	require.True(t, errors.As(err, &blocked))
	require.Contains(t, blocked.Message, "fast mode is blocked")
	require.Equal(t, string(body), string(updated)) // body not mutated on block
}

func TestApplyOpenAIFastPolicyToBody_ForcePriorityFallbackBlockWithoutTier(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, openAIFastForcePriorityFallbackBlockPolicy())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-4","input":"hello"}`)

	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-4", body)
	require.Error(t, err)
	var blocked *OpenAIFastBlockedError
	require.ErrorAs(t, err, &blocked)
	require.Equal(t, "model is not eligible for priority routing", blocked.Message)
	require.Equal(t, string(body), string(updated))
}

func TestOpenAIFastPolicySettingsCached_CollapsesReadsAndPublishesSet(t *testing.T) {
	initial := openAIFastForcePriorityFallbackBlockPolicy()
	raw, err := json.Marshal(initial)
	require.NoError(t, err)
	repo := &openAIFastPolicyRepoStub{values: map[string]string{
		SettingKeyOpenAIFastPolicySettings: string(raw),
	}}
	settingSvc := NewSettingService(repo, &config.Config{})

	const readers = 32
	var wg sync.WaitGroup
	wg.Add(readers)
	for range readers {
		go func() {
			defer wg.Done()
			settings, getErr := settingSvc.getOpenAIFastPolicySettingsCached(context.Background())
			require.NoError(t, getErr)
			require.Len(t, settings.Rules, 1)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, repo.getValueCalls.Load(), "concurrent hot-path reads should share one DB load")

	updated := &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{
		ServiceTier: OpenAIFastTierAny,
		Action:      OpenAIFastPolicyActionForcePriority,
		Scope:       BetaPolicyScopeAll,
	}}}
	require.NoError(t, settingSvc.SetOpenAIFastPolicySettings(context.Background(), updated))
	// The cache owns a deep copy, so a caller cannot mutate the published snapshot.
	updated.Rules[0].Action = BetaPolicyActionPass

	cached, err := settingSvc.getOpenAIFastPolicySettingsCached(context.Background())
	require.NoError(t, err)
	require.Equal(t, OpenAIFastPolicyActionForcePriority, cached.Rules[0].Action)
	require.EqualValues(t, 1, repo.getValueCalls.Load(), "successful writes should update the local cache without another DB read")
}

func TestSetOpenAIFastPolicySettings_SerializesPersistenceAndCachePublication(t *testing.T) {
	repo := &orderedOpenAIFastPolicyRepo{
		openAIFastPolicyRepoStub: &openAIFastPolicyRepoStub{values: map[string]string{}},
		firstPersisted:           make(chan struct{}),
		releaseFirst:             make(chan struct{}),
		secondEntered:            make(chan struct{}),
	}
	settingSvc := NewSettingService(repo, &config.Config{})
	first := &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{
		ServiceTier: OpenAIFastTierAny,
		Action:      OpenAIFastPolicyActionForcePriority,
		Scope:       BetaPolicyScopeAll,
	}}}
	second := &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{
		ServiceTier: OpenAIFastTierAny,
		Action:      BetaPolicyActionBlock,
		Scope:       BetaPolicyScopeAll,
	}}}

	firstErr := make(chan error, 1)
	go func() { firstErr <- settingSvc.SetOpenAIFastPolicySettings(context.Background(), first) }()
	<-repo.firstPersisted
	secondErr := make(chan error, 1)
	go func() { secondErr <- settingSvc.SetOpenAIFastPolicySettings(context.Background(), second) }()
	select {
	case <-repo.secondEntered:
		t.Fatal("second policy write entered the repository before the first cache publication")
	case <-time.After(50 * time.Millisecond):
	}
	close(repo.releaseFirst)
	require.NoError(t, <-firstErr)
	require.NoError(t, <-secondErr)

	var persisted OpenAIFastPolicySettings
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyOpenAIFastPolicySettings]), &persisted))
	require.Equal(t, BetaPolicyActionBlock, persisted.Rules[0].Action)
	cached, err := settingSvc.getOpenAIFastPolicySettingsCached(context.Background())
	require.NoError(t, err)
	require.Equal(t, BetaPolicyActionBlock, cached.Rules[0].Action)
}

func TestOpenAIFastPolicySettingsCached_ColdWaitHonorsCallerCancellation(t *testing.T) {
	repo := &blockingOpenAIFastPolicyReadRepo{
		openAIFastPolicyRepoStub: &openAIFastPolicyRepoStub{values: map[string]string{}},
		entered:                  make(chan struct{}),
		release:                  make(chan struct{}),
	}
	settingSvc := NewSettingService(repo, &config.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := settingSvc.getOpenAIFastPolicySettingsCached(ctx)
		result <- err
	}()
	<-repo.entered
	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cold cache caller remained blocked after its context was canceled")
	}
	close(repo.release)
}

func TestOpenAIFastPolicySettingsCached_StaleRequestsStartOneBackgroundRefresh(t *testing.T) {
	updated := openAIFastForcePriorityFallbackBlockPolicy()
	raw, err := json.Marshal(updated)
	require.NoError(t, err)
	repo := &blockingOpenAIFastPolicyReadRepo{
		openAIFastPolicyRepoStub: &openAIFastPolicyRepoStub{values: map[string]string{
			SettingKeyOpenAIFastPolicySettings: string(raw),
		}},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	settingSvc := NewSettingService(repo, &config.Config{})
	settingSvc.openAIFastPolicyCache.Store(&cachedOpenAIFastPolicySettings{
		settings: OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
		}}},
		expiresAt: time.Now().Add(-time.Second).UnixNano(),
	})

	for range 256 {
		settings, getErr := settingSvc.getOpenAIFastPolicySettingsCached(context.Background())
		require.NoError(t, getErr)
		require.Equal(t, BetaPolicyActionPass, settings.Rules[0].Action)
	}
	<-repo.entered
	require.EqualValues(t, 1, repo.calls.Load(), "stale traffic must not enqueue one refresh waiter per request")
	close(repo.release)
	require.Eventually(t, func() bool {
		cached := settingSvc.openAIFastPolicyCache.Load()
		return cached != nil && len(cached.settings.Rules) == 1 && cached.settings.Rules[0].FallbackAction == BetaPolicyActionBlock
	}, time.Second, 10*time.Millisecond)
}

func TestSetOpenAIFastPolicySettings_Validation(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})

	// Invalid action rejected
	err := svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      "bogus",
			Scope:       BetaPolicyScopeAll,
		}},
	})
	require.Error(t, err)

	// Invalid service_tier rejected
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: "turbo",
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
		}},
	})
	require.Error(t, err)

	// Non-positive and duplicate user IDs are rejected.
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
			UserIDs:     []int64{0},
		}},
	})
	require.Error(t, err)

	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
			UserIDs:     []int64{42, 42},
		}},
	})
	require.Error(t, err)

	// Valid settings persisted
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      OpenAIFastPolicyActionForcePriority,
			Scope:       BetaPolicyScopeAll,
			UserIDs:     []int64{42, 43},
		}},
	})
	require.NoError(t, err)

	got, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Len(t, got.Rules, 1)
	require.Equal(t, OpenAIFastTierAny, got.Rules[0].ServiceTier)
	require.Equal(t, OpenAIFastPolicyActionForcePriority, got.Rules[0].Action)
	require.Equal(t, []int64{42, 43}, got.Rules[0].UserIDs)
}
