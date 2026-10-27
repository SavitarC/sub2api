package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "client-request-123", gotClientRequestID)
	require.Equal(t, "request-456", gotRequestID)
}

func TestOpenAISubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "openai-client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "openai-request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "openai-client-request-123", gotClientRequestID)
	require.Equal(t, "openai-request-456", gotRequestID)
}

type usageContextChannelRepo struct {
	service.ChannelRepository
	channels []service.Channel
}

func (r usageContextChannelRepo) ListAll(context.Context) ([]service.Channel, error) {
	return r.channels, nil
}

func (r usageContextChannelRepo) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{77: service.PlatformComposite}, nil
}

func TestUsageRecordContextPreservesAttemptPlatformPricingAndMapping(t *testing.T) {
	deepseekPrice, commandPrice := 1e-6, 4e-6
	channels := service.NewChannelService(usageContextChannelRepo{channels: []service.Channel{{
		ID: 1, Status: service.StatusActive, GroupIDs: []int64{77},
		ModelPricing: []service.ChannelModelPricing{
			{Platform: service.PlatformDeepseek, Models: []string{"shared-model"}, BillingMode: service.BillingModeToken, InputPrice: &deepseekPrice},
			{Platform: service.PlatformCommandCode, Models: []string{"shared-model"}, BillingMode: service.BillingModeToken, InputPrice: &commandPrice},
		},
		ModelMapping: map[string]map[string]string{
			service.PlatformDeepseek:    {"shared-model": "deepseek-flash"},
			service.PlatformCommandCode: {"shared-model": "command-flash"},
		},
	}}}, nil, nil, nil, nil)
	group := &service.Group{ID: 77, Platform: service.PlatformComposite}
	apiKey := &service.APIKey{GroupID: &group.ID, Group: group}
	billing := &service.BillingService{}
	for _, tc := range []struct {
		name, platform, forced, wantPlatform, upstream string
		cost                                           float64
	}{
		{"deepseek", service.PlatformDeepseek, "", service.PlatformDeepseek, "deepseek-flash", 1},
		{"command", service.PlatformCommandCode, "", service.PlatformCommandCode, "command-flash", 4},
		{"forced", service.PlatformDeepseek, service.PlatformCommandCode, service.PlatformCommandCode, "command-flash", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(context.Background())
			parent = service.WithCompositeRouteDecision(parent, service.CompositeRouteDecision{
				Matched: true, Source: service.CompositeRouteSourceAccount, TargetPlatform: tc.platform,
				PublicModel: "shared-model", UpstreamModel: "shared-model",
			})
			parent = context.WithValue(parent, ctxkey.ForcePlatform, tc.forced)
			task, _ := wrapUsageRecordTaskContext(parent, func(ctx context.Context) {
				require.NoError(t, ctx.Err(), "worker must not inherit request cancellation")
				_, hasDeadline := ctx.Deadline()
				require.True(t, hasDeadline, "worker deadline must survive")
				require.Equal(t, tc.wantPlatform, service.QuotaPlatform(ctx, apiKey))
				publicModel, ok := service.RequestedPublicModelFromContext(ctx)
				require.True(t, ok)
				require.Equal(t, "shared-model", publicModel)
				mapping := channels.ResolveChannelMapping(ctx, group.ID, publicModel)
				require.Equal(t, tc.upstream, mapping.MappedModel)
				cost, err := billing.CalculateCostUnified(service.CostInput{
					Ctx: ctx, Model: publicModel, GroupID: &group.ID, Group: group,
					Tokens: service.UsageTokens{InputTokens: 1_000_000}, RateMultiplier: 1,
					Resolver: service.NewModelPricingResolver(channels, billing),
				})
				require.NoError(t, err)
				require.InDelta(t, tc.cost, cost.ActualCost, 1e-9)
			})
			cancelParent()
			worker, cancelWorker := context.WithTimeout(context.Background(), time.Second)
			defer cancelWorker()
			task(worker)
		})
	}
}
