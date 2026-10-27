package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type compositeResponseOwnerRepo struct {
	AccountRepository
	account *Account
}

func (r compositeResponseOwnerRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestCompositeResponseOwnerKeepsAccountAffinityAndAuthorization(t *testing.T) {
	for _, scenario := range []string{"healthy", "temporarily unavailable", "same user new key", "detected default model", "other user", "other group", "removed mapping", "unknown response"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			groupID := int64(42)
			account := &Account{
				ID: 11, Platform: PlatformCommandCode, Type: AccountTypeAPIKey,
				Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
				Credentials: map[string]any{"model_mapping": map[string]any{"shared-model": "gpt-upstream"}},
			}
			store := NewOpenAIWSStateStore(nil)
			require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_owned", account.ID, time.Hour))
			require.NoError(t, store.BindHTTPResponseOwner(ctx, groupID, "resp_owned", 7, 8, time.Hour))
			svc := &OpenAIGatewayService{accountRepo: compositeResponseOwnerRepo{account: account}, openaiWSStateStore: store}
			apiKey := &APIKey{ID: 8, GroupID: &groupID, User: &User{ID: 7}}
			responseID := "resp_owned"
			switch scenario {
			case "temporarily unavailable":
				account.Schedulable = false
				until := time.Now().Add(time.Hour)
				account.RateLimitResetAt = &until
			case "same user new key":
				apiKey.ID = 9
			case "detected default model":
				account.Platform = PlatformOpenAI
				account.Credentials = nil
				ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceDetector, TargetPlatform: PlatformOpenAI})
			case "other user":
				apiKey.User.ID = 10
			case "other group":
				account.GroupIDs = []int64{99}
			case "removed mapping":
				account.Credentials["model_mapping"] = map[string]any{"another-model": "gpt-upstream"}
			case "unknown response":
				responseID = "resp_unknown"
			}
			resolved, err := svc.ResolveCompositeResponseAccount(ctx, apiKey, responseID, "shared-model")
			if scenario == "healthy" || scenario == "temporarily unavailable" || scenario == "same user new key" || scenario == "detected default model" {
				require.NoError(t, err)
				require.Same(t, account, resolved)
			} else {
				require.ErrorIs(t, err, ErrCompositeResponseOwnerUnavailable)
				require.Nil(t, resolved)
			}
			bound, err := store.GetResponseAccount(ctx, groupID, "resp_owned")
			require.NoError(t, err)
			require.Equal(t, account.ID, bound, "lookup must preserve the durable continuation binding")
		})
	}
}
