//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestGetFeishuConnectOAuthConfig_FallsBackToConfig(t *testing.T) {
	cfg := feishuSettingTestConfig()
	svc := NewSettingService(&settingOIDCRepoStub{values: map[string]string{}}, cfg)

	got, err := svc.GetFeishuConnectOAuthConfig(context.Background())
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.Equal(t, "cli_config", got.ClientID)
	require.Equal(t, "secret_config", got.ClientSecret)
	require.True(t, got.UsePKCE)
}

func TestGetFeishuConnectOAuthConfig_DatabaseOverridesConfig(t *testing.T) {
	cfg := feishuSettingTestConfig()
	svc := NewSettingService(&settingOIDCRepoStub{values: map[string]string{
		SettingKeyFeishuConnectEnabled:      "true",
		SettingKeyFeishuConnectClientID:     "cli_database",
		SettingKeyFeishuConnectClientSecret: "secret_database",
		SettingKeyFeishuConnectRedirectURL:  "https://db.example.com/api/v1/auth/oauth/feishu/callback",
	}}, cfg)

	got, err := svc.GetFeishuConnectOAuthConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "cli_database", got.ClientID)
	require.Equal(t, "secret_database", got.ClientSecret)
	require.Equal(t, "https://db.example.com/api/v1/auth/oauth/feishu/callback", got.RedirectURL)
	// Provider endpoints and the secure PKCE default remain deployment config.
	require.Equal(t, cfg.Feishu.TokenURL, got.TokenURL)
	require.True(t, got.UsePKCE)
}

func TestGetFeishuConnectOAuthConfig_StoredDisableOverridesConfig(t *testing.T) {
	svc := NewSettingService(&settingOIDCRepoStub{values: map[string]string{
		SettingKeyFeishuConnectEnabled: "false",
	}}, feishuSettingTestConfig())

	_, err := svc.GetFeishuConnectOAuthConfig(context.Background())
	require.Error(t, err)
	require.Equal(t, "OAUTH_DISABLED", infraerrors.Reason(err))
}

func TestGetFeishuConnectOAuthConfig_FailsClosedWithoutSecret(t *testing.T) {
	cfg := feishuSettingTestConfig()
	cfg.Feishu.ClientSecret = ""
	svc := NewSettingService(&settingOIDCRepoStub{values: map[string]string{}}, cfg)

	_, err := svc.GetFeishuConnectOAuthConfig(context.Background())
	require.Error(t, err)
}

func TestParseSettings_FeishuUsesConfigFallbackAndNeverExposesSecretFlagIncorrectly(t *testing.T) {
	cfg := feishuSettingTestConfig()
	svc := NewSettingService(&settingOIDCRepoStub{values: map[string]string{}}, cfg)

	got := svc.parseSettings(map[string]string{})
	require.True(t, got.FeishuConnectEnabled)
	require.Equal(t, "cli_config", got.FeishuConnectClientID)
	require.Equal(t, "secret_config", got.FeishuConnectClientSecret)
	require.True(t, got.FeishuConnectClientSecretConfigured)
}

func TestGetPublicSettings_FeishuUsesConfigFallback(t *testing.T) {
	svc := NewSettingService(&settingOIDCRepoStub{values: map[string]string{}}, feishuSettingTestConfig())

	got, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, got.FeishuOAuthEnabled)

	injectionValue, err := svc.GetPublicSettingsForInjection(context.Background())
	require.NoError(t, err)
	injection, ok := injectionValue.(*PublicSettingsInjectionPayload)
	require.True(t, ok)
	require.True(t, injection.FeishuOAuthEnabled)
}

func feishuSettingTestConfig() *config.Config {
	return &config.Config{Feishu: config.FeishuConnectConfig{
		Enabled:             true,
		ClientID:            "cli_config",
		ClientSecret:        "secret_config",
		AuthorizeURL:        "https://accounts.feishu.cn/open-apis/authen/v1/authorize",
		TokenURL:            "https://open.feishu.cn/open-apis/authen/v2/oauth/token",
		UserInfoURL:         "https://open.feishu.cn/open-apis/authen/v1/user_info",
		RedirectURL:         "https://api.example.com/api/v1/auth/oauth/feishu/callback",
		FrontendRedirectURL: "/auth/feishu/callback",
		UsePKCE:             true,
	}}
}
