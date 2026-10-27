//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func compositeAccountTestContext(platform string) context.Context {
	return WithCompositeRouteDecision(context.Background(), CompositeRouteDecision{
		Matched: true, Source: CompositeRouteSourceAccount, PublicModel: "shared-model",
		TargetPlatform: platform, UpstreamModel: "shared-model",
	})
}

func TestCompositeAccountMatchesRouteRequiresExactMappingOnAttemptPlatform(t *testing.T) {
	owner := &Account{Platform: PlatformDeepseek, Credentials: map[string]any{
		"model_mapping": map[string]any{"shared-model": "deepseek-flash"},
	}}
	ctx := compositeAccountTestContext(PlatformDeepseek)
	require.True(t, compositeAccountMatchesRoute(ctx, owner))
	require.False(t, compositeAccountMatchesRoute(ctx, nil))
	for _, mapping := range []map[string]any{nil, {"shared-*": "deepseek-flash"}, {"shared-model": " "}} {
		nonOwner := *owner
		nonOwner.Credentials = map[string]any{"model_mapping": mapping}
		require.False(t, compositeAccountMatchesRoute(ctx, &nonOwner))
	}
	foreign := *owner
	foreign.Platform = PlatformCommandCode
	require.False(t, compositeAccountMatchesRoute(ctx, &foreign))
	explicit := WithCompositeRouteDecision(context.Background(), CompositeRouteDecision{
		Matched: true, Source: CompositeRouteSourceExplicit, PublicModel: "shared-model", TargetPlatform: PlatformDeepseek,
	})
	require.True(t, compositeAccountMatchesRoute(explicit, &foreign), "explicit routes retain their scheduling policy")
}

func TestCompositeAccountMatchesRouteChecksMappedModelEndpoint(t *testing.T) {
	owner := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"account_mode": AccountModeZen, "api_protocol": APIProtocolAdaptive,
		"model_mapping": map[string]any{"shared-model": "gemini-2.5-flash"},
	}}
	chatOnly := *owner
	chatOnly.Credentials = map[string]any{
		"account_mode": AccountModeZen, "api_protocol": APIProtocolChatCompletions,
		"model_mapping": map[string]any{"shared-model": "gemini-2.5-flash"},
	}
	decision := CompositeRouteDecision{
		Matched: true, Source: CompositeRouteSourceAccount, PublicModel: "shared-model", TargetPlatform: PlatformOpenCodeGo,
		Endpoint: CompositeRouteEndpointGemini, Candidates: []CompositeRouteCandidate{{TargetPlatform: PlatformOpenCodeGo}},
	}
	ctx := WithCompositeRouteDecision(context.Background(), decision)
	require.True(t, compositeAccountMatchesRoute(ctx, owner), "capability uses the mapped upstream model")
	require.False(t, compositeAccountMatchesRoute(ctx, &chatOnly), "same-platform accounts can support different endpoints")
	decision.Endpoint = CompositeRouteEndpointAny
	require.True(t, compositeAccountMatchesRoute(WithCompositeRouteDecision(context.Background(), decision), &chatOnly))
	decision.Endpoint, decision.Candidates = CompositeRouteEndpointGemini, nil
	require.True(t, compositeAccountMatchesRoute(WithCompositeRouteDecision(context.Background(), decision), &chatOnly), "single-owner routes retain their existing policy")
}

func TestCompositeAccountMatchesRoutePreservesResponseOwnerPin(t *testing.T) {
	ctx := WithCompositeResponseAccount(context.Background(), 12)
	require.True(t, compositeAccountMatchesRoute(ctx, &Account{ID: 12}))
	require.False(t, compositeAccountMatchesRoute(ctx, &Account{ID: 13}))
	require.False(t, compositeAccountMatchesRoute(ctx, nil))
}

func TestCompositeAccountMatchesRouteHonorsSharedSwitchBudget(t *testing.T) {
	ctx := WithCompositeAccountAttemptBudget(context.Background())
	require.True(t, AdmitCompositeAccountAttempt(ctx, 12, 0))
	require.True(t, compositeAccountMatchesRoute(ctx, &Account{ID: 12}))
	require.False(t, compositeAccountMatchesRoute(ctx, &Account{ID: 13}))
}

func TestGatewayCompositeAccountRouteRejectsNonOwnerAndMixedPlatformSticky(t *testing.T) {
	const groupID int64 = 876
	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformAntigravity} {
		for _, stickyID := range []int64{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/sticky=%d", platform, stickyID), func(t *testing.T) {
				owner := Account{
					ID: 3, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
					Priority: 10, GroupIDs: []int64{groupID}, AccountGroups: []AccountGroup{{GroupID: groupID}},
					Credentials: map[string]any{"model_mapping": map[string]any{"shared-model": "claude-sonnet-4-5"}},
				}
				nonOwner := owner
				nonOwner.ID, nonOwner.Priority, nonOwner.Credentials = 1, 0, nil
				foreign := owner
				foreign.ID, foreign.Priority, foreign.Platform = 2, 0, PlatformAntigravity
				if platform == PlatformAntigravity {
					foreign.Platform = PlatformAnthropic
				}
				foreign.Extra = map[string]any{"mixed_scheduling": true}
				repo := &mockAccountRepoForPlatform{accounts: []Account{nonOwner, foreign, owner}, accountsByID: map[int64]*Account{1: &nonOwner, 2: &foreign, 3: &owner}}
				cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"session": stickyID}}
				svc := &GatewayService{
					accountRepo: repo, cache: cache, cfg: testConfig(),
					groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{
						groupID: {ID: groupID, Platform: PlatformComposite, Status: StatusActive, Hydrated: true},
					}},
				}
				selection, err := svc.SelectAccountWithLoadAwareness(compositeAccountTestContext(platform), ptrInt64(groupID), "session", "shared-model", nil, "", 0)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Equal(t, owner.ID, selection.Account.ID)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}

func TestGatewayCompositeAccountHydrationRechecksEligibilityAndReleasesSlot(t *testing.T) {
	for _, mutation := range []string{"mapping_deleted", "platform_changed", "expired"} {
		t.Run(mutation, func(t *testing.T) {
			candidate := &Account{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"model_mapping": map[string]any{"shared-model": "claude-sonnet-4-5"}},
			}
			latest := *candidate
			switch mutation {
			case "mapping_deleted":
				latest.Credentials = nil
			case "platform_changed":
				latest.Platform = PlatformAntigravity
			case "expired":
				expired := time.Now().Add(-time.Minute)
				latest.ExpiresAt, latest.AutoPauseOnExpired = &expired, true
			}
			svc := &GatewayService{schedulerSnapshot: NewSchedulerSnapshotService(&snapshotHydrationCache{
				accounts: map[int64]*Account{1: &latest},
			}, nil, nil, nil, nil)}
			releases := 0
			selection, err := svc.newSelectionResult(compositeAccountTestContext(PlatformAnthropic), candidate, true, func() { releases++ }, nil)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.Nil(t, selection)
			require.Equal(t, 1, releases)
		})
	}
}

func TestOpenAICompositeAccountDBRecheckRejectsChangedEligibility(t *testing.T) {
	const groupID int64 = 877
	for _, mutation := range []string{"mapping_deleted", "platform_changed", "expired"} {
		t.Run(mutation, func(t *testing.T) {
			candidate := &Account{ID: 1, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				GroupIDs: []int64{groupID}, Credentials: map[string]any{"model_mapping": map[string]any{"shared-model": "deepseek-flash"}},
			}
			latest := *candidate
			switch mutation {
			case "mapping_deleted":
				latest.Credentials = nil
			case "platform_changed":
				latest.Platform = PlatformCommandCode
			case "expired":
				expired := time.Now().Add(-time.Minute)
				latest.ExpiresAt, latest.AutoPauseOnExpired = &expired, true
			}
			svc := &OpenAIGatewayService{
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{latest}},
				schedulerSnapshot: NewSchedulerSnapshotService(&snapshotHydrationCache{
					accounts: map[int64]*Account{1: candidate},
				}, nil, nil, nil, nil),
			}
			got := svc.recheckSelectedOpenAIAccountFromDB(compositeAccountTestContext(PlatformDeepseek), candidate, ptrInt64(groupID), PlatformDeepseek, "shared-model", false, OpenAIEndpointCapabilityChatCompletions)
			require.Nil(t, got)
		})
	}
}

func TestOpenAICompositePinnedResponseSchedulerKeepsBinding(t *testing.T) {
	const groupID int64 = 878
	for _, advanced := range []string{"true", "false"} {
		for _, paused := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%s/paused=%t", advanced, paused), func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				owner := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
					Schedulable: !paused, Concurrency: 1, Priority: 10, GroupIDs: []int64{groupID},
					Credentials: map[string]any{"model_mapping": map[string]any{"shared-model": "gpt-5.1"}},
				}
				other := owner
				other.ID, other.Priority, other.Schedulable = 2, 0, true
				store := NewOpenAIWSStateStore(nil)
				require.NoError(t, store.BindResponseAccount(context.Background(), groupID, "resp_owned", owner.ID, time.Hour))
				svc := &OpenAIGatewayService{
					accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{owner, other}},
					cache:       &schedulerTestGatewayCache{}, cfg: testConfig(), openaiWSStateStore: store,
					rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(advanced),
					concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
				}
				ctx := WithCompositeResponseAccount(compositeAccountTestContext(PlatformOpenAI), owner.ID)
				selection, _, err := svc.SelectAccountWithScheduler(ctx, ptrInt64(groupID), "resp_owned", "", "shared-model", nil, OpenAIUpstreamTransportAny, false)
				if paused {
					require.Error(t, err)
					require.Nil(t, selection, "must not move continuation to the healthy alternate account")
				} else {
					require.NoError(t, err)
					require.Equal(t, owner.ID, selection.Account.ID)
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				}
				bound, err := store.GetResponseAccount(context.Background(), groupID, "resp_owned")
				require.NoError(t, err)
				require.Equal(t, owner.ID, bound)
			})
		}
	}
}
