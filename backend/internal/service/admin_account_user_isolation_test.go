//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeUserIsolationExtra(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		accountType string
		extra       map[string]any
		defaultMode string
		want        map[string]any
	}{
		{
			name:        "new DeepSeek API key defaults to authenticated user",
			platform:    PlatformDeepseek,
			accountType: AccountTypeAPIKey,
			defaultMode: UserIsolationModeAuthenticatedUser,
			want:        map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser},
		},
		{
			name:        "explicit off is preserved",
			platform:    PlatformDeepseek,
			accountType: AccountTypeAPIKey,
			extra:       map[string]any{UserIsolationExtraKey: UserIsolationModeOff, "keep": "value"},
			defaultMode: UserIsolationModeAuthenticatedUser,
			want:        map[string]any{UserIsolationExtraKey: UserIsolationModeOff, "keep": "value"},
		},
		{
			name:        "duplicate mode does not default a legacy account",
			platform:    PlatformDeepseek,
			accountType: AccountTypeAPIKey,
			extra:       map[string]any{"keep": "value"},
			want:        map[string]any{"keep": "value"},
		},
		{
			name:        "non DeepSeek removes the provider owned key",
			platform:    PlatformOpenAI,
			accountType: AccountTypeAPIKey,
			extra:       map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser, "keep": "value"},
			want:        map[string]any{"keep": "value"},
		},
		{
			name:        "non API key removes the provider owned key",
			platform:    PlatformDeepseek,
			accountType: AccountTypeOAuth,
			extra:       map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser, "keep": "value"},
			want:        map[string]any{"keep": "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeUserIsolationExtra(tt.platform, tt.accountType, tt.extra, tt.defaultMode)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeUserIsolationExtraRejectsUnsupportedMode(t *testing.T) {
	_, err := normalizeUserIsolationExtra(PlatformDeepseek, AccountTypeAPIKey, map[string]any{
		UserIsolationExtraKey: "always_on",
	}, UserIsolationModeAuthenticatedUser)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "USER_ISOLATION_MODE_INVALID", infraerrors.Reason(err))
}

func TestNormalizeUserIsolationUpdateExtraPreservesOmittedMode(t *testing.T) {
	account := &Account{
		Platform: PlatformDeepseek,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			UserIsolationExtraKey: UserIsolationModeAuthenticatedUser,
		},
	}
	input := &UpdateAccountInput{Extra: map[string]any{"keep": "updated"}}

	normalized, err := normalizeUserIsolationUpdateExtra(account, input, input.Extra)

	require.NoError(t, err)
	require.Equal(t, UserIsolationModeAuthenticatedUser, normalized[UserIsolationExtraKey])
	require.Equal(t, "updated", normalized["keep"])
}

func TestNormalizeUserIsolationUpdateExtraTreatsMissingLegacyModeAsOff(t *testing.T) {
	account := &Account{
		Platform: PlatformDeepseek,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"keep": "old"},
	}
	input := &UpdateAccountInput{Extra: map[string]any{"keep": "updated"}}

	normalized, err := normalizeUserIsolationUpdateExtra(account, input, input.Extra)

	require.NoError(t, err)
	require.NotContains(t, normalized, UserIsolationExtraKey)
	require.Equal(t, "updated", normalized["keep"])
}

func TestAdminServiceCreateAccountDefaultsDeepSeekUserIsolation(t *testing.T) {
	repo := &longContextBillingRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Platform:             PlatformDeepseek,
		Type:                 AccountTypeAPIKey,
		Extra:                map[string]any{"preserved": "value"},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.Equal(t, UserIsolationModeAuthenticatedUser, account.Extra[UserIsolationExtraKey])
	require.Equal(t, "value", account.Extra["preserved"])
}

func TestAdminServiceUpdateAccountPreservesUserIsolationAndCleansUnsupportedAccount(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		accountType string
		extra       map[string]any
		wantMode    any
	}{
		{
			name:        "DeepSeek API key keeps omitted mode",
			platform:    PlatformDeepseek,
			accountType: AccountTypeAPIKey,
			extra:       map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser},
			wantMode:    UserIsolationModeAuthenticatedUser,
		},
		{
			name:        "non DeepSeek removes mode",
			platform:    PlatformOpenAI,
			accountType: AccountTypeAPIKey,
			extra:       map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser},
		},
		{
			name:        "non API key removes mode",
			platform:    PlatformDeepseek,
			accountType: AccountTypeOAuth,
			extra:       map[string]any{UserIsolationExtraKey: UserIsolationModeAuthenticatedUser},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &longContextBillingRepoStub{account: &Account{
				ID:       1,
				Platform: tt.platform,
				Type:     tt.accountType,
				Extra:    tt.extra,
			}}
			svc := &adminServiceImpl{accountRepo: repo}

			updated, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{
				Extra: map[string]any{"preserved": "updated"},
			})

			require.NoError(t, err)
			require.Equal(t, "updated", updated.Extra["preserved"])
			if tt.wantMode == nil {
				require.NotContains(t, updated.Extra, UserIsolationExtraKey)
			} else {
				require.Equal(t, tt.wantMode, updated.Extra[UserIsolationExtraKey])
			}
		})
	}
}

func TestDuplicateAccountPreservesConfiguredUserIsolationWithoutEnablingLegacyAccount(t *testing.T) {
	tests := []struct {
		name     string
		extra    map[string]any
		wantMode any
	}{
		{
			name:     "configured mode is inherited",
			extra:    map[string]any{UserIsolationExtraKey: UserIsolationModeOff, "preserved": "value"},
			wantMode: UserIsolationModeOff,
		},
		{
			name:  "missing mode stays effectively off",
			extra: map[string]any{"preserved": "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newDuplicateAccountRepoStub()
			svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}
			source := &Account{
				Name:        "deepseek",
				Platform:    PlatformDeepseek,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "secret"},
				Extra:       tt.extra,
			}
			require.NoError(t, repo.Create(ctx, source))

			duplicate, err := svc.DuplicateAccount(ctx, source.ID, "admin:1", "")

			require.NoError(t, err)
			require.Equal(t, "value", duplicate.Extra["preserved"])
			if tt.wantMode == nil {
				require.NotContains(t, duplicate.Extra, UserIsolationExtraKey)
			} else {
				require.Equal(t, tt.wantMode, duplicate.Extra[UserIsolationExtraKey])
			}
		})
	}
}
