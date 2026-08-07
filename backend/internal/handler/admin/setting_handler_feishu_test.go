package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingsPUT_FeishuRoundTripAndSecretPreservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	h := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	putFeishuSettings(t, h, map[string]any{
		"feishu_connect_enabled":       true,
		"feishu_connect_client_id":     "cli_mock",
		"feishu_connect_client_secret": "secret_mock",
		"feishu_connect_redirect_url":  "https://api.example.com/api/v1/auth/oauth/feishu/callback",
	}, func(rec *httptest.ResponseRecorder) {
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotContains(t, rec.Body.String(), "secret_mock")
		var envelope response.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		data, ok := envelope.Data.(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, data["feishu_connect_enabled"])
		require.Equal(t, "cli_mock", data["feishu_connect_client_id"])
		require.Equal(t, true, data["feishu_connect_client_secret_configured"])
	})
	require.Equal(t, "secret_mock", repo.values[service.SettingKeyFeishuConnectClientSecret])

	putFeishuSettings(t, h, map[string]any{
		"feishu_connect_enabled":       true,
		"feishu_connect_client_id":     "cli_mock",
		"feishu_connect_client_secret": "",
		"feishu_connect_redirect_url":  "https://api.example.com/api/v1/auth/oauth/feishu/callback",
	}, func(rec *httptest.ResponseRecorder) {
		require.Equal(t, http.StatusOK, rec.Code)
	})
	require.Equal(t, "secret_mock", repo.values[service.SettingKeyFeishuConnectClientSecret])
	require.NotContains(t, repo.lastUpdates, service.SettingKeyFeishuConnectClientSecret)

	changed := diffSettings(
		&service.SystemSettings{FeishuConnectClientSecret: "secret_mock"},
		&service.SystemSettings{},
		nil,
		nil,
		UpdateSettingsRequest{FeishuConnectClientSecret: ""},
	)
	require.NotContains(t, changed, "feishu_connect_client_secret")
}

func TestSettingsPUT_FeishuConfigFallbackSecretIsNeverPersisted(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name          string
		includeSecret bool
		secret        string
	}{
		{name: "omitted"},
		{name: "empty", includeSecret: true, secret: ""},
		{name: "whitespace", includeSecret: true, secret: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &settingHandlerRepoStub{values: map[string]string{}}
			cfg := &config.Config{
				Default: config.DefaultConfig{UserConcurrency: 5},
				Feishu: config.FeishuConnectConfig{
					Enabled:      true,
					ClientID:     "cli_config",
					ClientSecret: "secret_from_config",
					RedirectURL:  "https://api.example.com/api/v1/auth/oauth/feishu/callback",
				},
			}
			svc := service.NewSettingService(repo, cfg)
			h := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)
			body := map[string]any{
				"feishu_connect_enabled":      true,
				"feishu_connect_client_id":    "cli_config",
				"feishu_connect_redirect_url": "https://api.example.com/api/v1/auth/oauth/feishu/callback",
			}
			if tc.includeSecret {
				body["feishu_connect_client_secret"] = tc.secret
			}

			putFeishuSettings(t, h, body, func(rec *httptest.ResponseRecorder) {
				require.Equal(t, http.StatusOK, rec.Code)
			})

			require.NotContains(t, repo.lastUpdates, service.SettingKeyFeishuConnectClientSecret)
			require.NotContains(t, repo.values, service.SettingKeyFeishuConnectClientSecret)
		})
	}
}

func TestSettingsPUT_FeishuEnabledRequiresAbsoluteRedirectURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	h := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	putFeishuSettings(t, h, map[string]any{
		"feishu_connect_enabled":       true,
		"feishu_connect_client_id":     "cli_mock",
		"feishu_connect_client_secret": "secret_mock",
		"feishu_connect_redirect_url":  "/relative/callback",
	}, func(rec *httptest.ResponseRecorder) {
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func putFeishuSettings(t *testing.T, h *SettingHandler, body map[string]any, assert func(*httptest.ResponseRecorder)) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateSettings(c)
	assert(recorder)
}
