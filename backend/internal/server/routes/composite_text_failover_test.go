//go:build unit

package routes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type compositeTextAccounts struct {
	service.AccountRepository
	accounts []service.Account
}

func (r *compositeTextAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range r.accounts {
		if account.ID == id {
			return &account, nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

func (r *compositeTextAccounts) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.list([]string{platform}), nil
}

func (r *compositeTextAccounts) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (r *compositeTextAccounts) ListSchedulableByGroupIDAndPlatforms(_ context.Context, _ int64, platforms []string) ([]service.Account, error) {
	return r.list(platforms), nil
}

func (r *compositeTextAccounts) list(platforms []string) []service.Account {
	var accounts []service.Account
	for _, account := range r.accounts {
		if slices.Contains(platforms, account.Platform) && account.IsSchedulable() {
			accounts = append(accounts, account)
		}
	}
	return accounts
}

func (r *compositeTextAccounts) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	return r.accounts, nil
}

func (r *compositeTextAccounts) SetError(_ context.Context, id int64, _ string) error {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].Status = service.StatusError
		}
	}
	return nil
}

func (r *compositeTextAccounts) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

type compositeTextGroups struct {
	service.GroupRepository
	group *service.Group
}

func (r compositeTextGroups) GetByIDLite(context.Context, int64) (*service.Group, error) {
	return r.group, nil
}

type compositeTextUsers struct{ service.UserRepository }

func (compositeTextUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Balance: 100, Status: service.StatusActive}, nil
}

type compositeTextChannels struct {
	service.ChannelRepository
	channel service.Channel
}

func (r compositeTextChannels) ListAll(context.Context) ([]service.Channel, error) {
	return []service.Channel{r.channel}, nil
}

func (r compositeTextChannels) GetGroupPlatforms(_ context.Context, ids []int64) (map[int64]string, error) {
	return map[int64]string{ids[0]: service.PlatformComposite}, nil
}

type compositeTextUsage struct {
	service.UsageLogRepository
	service.UsageBillingRepository
	mu        sync.Mutex
	logs      []*service.UsageLog
	commands  []service.UsageBillingCommand
	platforms []string
	done      chan struct{}
}

func (r *compositeTextUsage) Create(_ context.Context, usage *service.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, usage)
	select {
	case r.done <- struct{}{}:
	default:
	}
	return true, nil
}

func (r *compositeTextUsage) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, *cmd)
	platform, _ := service.ResolvedTargetPlatformFromContext(ctx)
	r.platforms = append(r.platforms, platform)
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

type compositeTextHTTP struct {
	client *http.Client
	server *url.URL
}

func (u compositeTextHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = u.server.Scheme, u.server.Host
	req.Host = u.server.Host
	return u.client.Do(req)
}

func (u compositeTextHTTP) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func TestCompositeMessagesFailoverPreservesProtocolAuthenticationAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []struct {
		name, platform string
		expired        bool
	}{
		{"anthropic_auth_failure", service.PlatformAnthropic, false},
		{"cline_auth_failure", service.PlatformCline, false},
		{"anthropic_expired", service.PlatformAnthropic, true},
		{"cline_expired", service.PlatformCline, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			firstPlatform := scenario.platform
			const publicModel = "shared-claude"
			groupID := int64(51)
			group := &service.Group{ID: groupID, Platform: service.PlatformComposite, Status: service.StatusActive, RateMultiplier: 1}
			groupRepo := compositeTextGroups{group: group}
			accounts := &compositeTextAccounts{accounts: []service.Account{
				{ID: 1, Name: "native", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{
					"api_key": "native-key", "base_url": "https://upstream.example/anthropic", "model_mapping": map[string]any{publicModel: "claude-sonnet-4-6"},
				}},
				{ID: 2, Name: "aggregator", Platform: service.PlatformCline, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 2, GroupIDs: []int64{groupID}, Credentials: map[string]any{
					"api_key": "cline-key", "base_url": "https://upstream.example/cline/v1", "model_mapping": map[string]any{publicModel: "anthropic/claude-sonnet-4-6"},
				}},
			}}
			winnerID, winnerPlatform, expectedPrice := int64(2), service.PlatformCline, 3.0
			if firstPlatform == service.PlatformCline {
				accounts.accounts[0].Priority, accounts.accounts[1].Priority = 2, 1
				winnerID, winnerPlatform, expectedPrice = 1, service.PlatformAnthropic, 19.0
			}
			if scenario.expired {
				expiredAt := time.Now().Add(-time.Hour)
				for i := range accounts.accounts {
					if accounts.accounts[i].Platform == firstPlatform {
						accounts.accounts[i].ExpiresAt = &expiredAt
						accounts.accounts[i].AutoPauseOnExpired = true
					}
				}
			}
			var upstreamMu sync.Mutex
			var paths, auth, models []string
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				platform := service.PlatformCline
				credential := req.Header.Get("Authorization")
				if strings.Contains(req.URL.Path, "/anthropic/") {
					platform, credential = service.PlatformAnthropic, req.Header.Get("X-Api-Key")
					require.Empty(t, req.Header.Get("Authorization"))
				} else {
					require.Empty(t, req.Header.Get("X-Api-Key"))
				}
				upstreamMu.Lock()
				paths, auth, models = append(paths, req.URL.Path), append(auth, credential), append(models, gjson.GetBytes(body, "model").String())
				upstreamMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-Id", "winning-request")
				if platform == firstPlatform {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"fixture credential rejected"}}`)
					return
				}
				if platform == service.PlatformAnthropic {
					_, _ = io.WriteString(w, `{"id":"msg_win","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"healthy"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":5}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"chatcmpl_win","object":"chat.completion","model":"anthropic/claude-sonnet-4-6","choices":[{"index":0,"message":{"role":"assistant","content":"healthy"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}`)
			}))
			t.Cleanup(upstreamServer.Close)
			serverURL, err := url.Parse(upstreamServer.URL)
			require.NoError(t, err)
			upstream := compositeTextHTTP{client: upstreamServer.Client(), server: serverURL}
			cfg := &config.Config{}
			cfg.Gateway.MaxAccountSwitches = 1
			users := compositeTextUsers{}
			billingCache := service.NewBillingCacheService(nil, users, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCache.Stop)
			nativePrice, clinePrice := 19.0, 3.0
			channels := service.NewChannelService(compositeTextChannels{channel: service.Channel{
				ID: 61, Status: service.StatusActive, GroupIDs: []int64{groupID}, BillingModelSource: service.BillingModelSourceRequested,
				ModelPricing: []service.ChannelModelPricing{
					{Platform: service.PlatformAnthropic, Models: []string{publicModel}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &nativePrice},
					{Platform: service.PlatformCline, Models: []string{publicModel}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &clinePrice},
				},
			}}, groupRepo, nil, nil, nil)
			billing := service.NewBillingService(cfg, nil)
			pricing := service.NewModelPricingResolver(channels, billing)
			usage := &compositeTextUsage{done: make(chan struct{}, 1)}
			rateLimits := service.NewRateLimitService(accounts, nil, cfg, nil, nil)
			resolver := service.NewCompositeRouteResolver(nil)
			gateway := service.NewGatewayService(accounts, groupRepo, usage, usage, users, nil, nil, nil, cfg, nil, nil, billing, rateLimits, billingCache, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, channels, pricing, resolver, nil, nil)
			openAI := service.NewOpenAIGatewayService(accounts, usage, usage, users, nil, nil, nil, cfg, nil, nil, billing, rateLimits, billingCache, upstream, &service.DeferredService{}, nil, nil, pricing, channels, nil, nil, nil)
			pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 4, TaskTimeout: 5 * time.Second})
			defer pool.Stop()
			blocked, unblock := make(chan struct{}), make(chan struct{})
			var unblockOnce sync.Once
			defer unblockOnce.Do(func() { close(unblock) })
			pool.Submit(func(context.Context) { close(blocked); <-unblock })
			<-blocked
			concurrency := service.NewConcurrencyService(nil)
			apiKeys := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			nativeHandler := handler.NewGatewayHandler(gateway, openAI, nil, nil, nil, concurrency, billingCache, nil, apiKeys, pool, nil, nil, nil, cfg, nil)
			openAIHandler := handler.NewOpenAIGatewayHandler(openAI, concurrency, billingCache, apiKeys, pool, nil, nil, nil, cfg)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 62, GroupID: &groupID, Group: group, User: &service.User{ID: 63, Balance: 100}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 63})
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
				c.Next()
			})
			router.Use(compositeTargetPlatformMiddleware(resolver))
			router.POST("/v1/messages", compositeCandidateHandler(func(c *gin.Context) {
				platform, _ := service.ResolvedTargetPlatformFromContext(c.Request.Context())
				if domain.UsesOpenAIGateway(platform) {
					openAIHandler.Messages(c)
				} else {
					nativeHandler.Messages(c)
				}
			}, openAIHandler.CompositeResponseOwner))
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"shared-claude","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, "message", gjson.GetBytes(recorder.Body.Bytes(), "type").String())
			require.Contains(t, recorder.Body.String(), "healthy")
			usage.mu.Lock()
			require.Empty(t, usage.logs, "usage must remain queued until after handler context completion")
			usage.mu.Unlock()
			unblockOnce.Do(func() { close(unblock) })
			select {
			case <-usage.done:
			case <-time.After(5 * time.Second):
				t.Fatal("usage was not recorded")
			}
			upstreamMu.Lock()
			if scenario.expired {
				require.Len(t, paths, 1, "expired accounts must be skipped before contacting upstream")
				if winnerPlatform == service.PlatformCline {
					require.Equal(t, []string{"/cline/v1/chat/completions"}, paths)
					require.Equal(t, []string{"Bearer cline-key"}, auth)
				} else {
					require.Equal(t, []string{"/anthropic/v1/messages"}, paths)
					require.Equal(t, []string{"native-key"}, auth)
				}
			} else if firstPlatform == service.PlatformAnthropic {
				require.Equal(t, []string{"/anthropic/v1/messages", "/cline/v1/chat/completions"}, paths)
				require.Equal(t, []string{"native-key", "Bearer cline-key"}, auth)
				require.Equal(t, []string{"claude-sonnet-4-6", "anthropic/claude-sonnet-4-6"}, models)
			} else {
				require.Equal(t, []string{"/cline/v1/chat/completions", "/anthropic/v1/messages"}, paths)
				require.Equal(t, []string{"Bearer cline-key", "native-key"}, auth)
			}
			upstreamMu.Unlock()
			usage.mu.Lock()
			defer usage.mu.Unlock()
			require.Len(t, usage.logs, 1)
			require.Len(t, usage.commands, 1, "failed attempts must not bill")
			require.Equal(t, winnerID, usage.logs[0].AccountID)
			require.Equal(t, publicModel, usage.logs[0].RequestedModel)
			require.Equal(t, []string{winnerPlatform}, usage.platforms)
			require.InDelta(t, expectedPrice, usage.logs[0].ActualCost, 1e-8)
			require.InDelta(t, expectedPrice, usage.commands[0].BalanceCost, 1e-8)
		})
	}
}
