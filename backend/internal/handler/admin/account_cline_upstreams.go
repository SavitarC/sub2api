package admin

import (
	"context"
	"maps"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type clineProbeConnection struct {
	AccountID int64  `json:"account_id"`
	APIKey    string `json:"api_key"`
	BaseURL   string `json:"base_url"`
	ProxyID   *int64 `json:"proxy_id"`
}

func (h *AccountHandler) resolveClineProbeAccount(ctx context.Context, input clineProbeConnection) (*service.Account, bool, error) {
	if input.AccountID < 0 || input.ProxyID != nil && *input.ProxyID < 0 {
		return nil, false, infraerrors.BadRequest("INVALID_CLINE_PROBE", "Account and proxy IDs must be non-negative")
	}
	account := &service.Account{Platform: service.PlatformCline, Type: service.AccountTypeAPIKey, Concurrency: 1, Credentials: make(map[string]any)}
	persist := false
	if input.AccountID > 0 {
		source, err := h.adminService.GetAccount(ctx, input.AccountID)
		if err != nil || source == nil {
			return nil, false, infraerrors.New(http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Account not found")
		}
		if !source.IsCline() || source.Type != service.AccountTypeAPIKey {
			return nil, false, infraerrors.BadRequest("INVALID_CLINE_ACCOUNT", "A Cline API key account is required")
		}
		copy := *source
		copy.Credentials = maps.Clone(source.Credentials)
		copy.Extra = maps.Clone(source.Extra)
		if copy.Credentials == nil {
			copy.Credentials = make(map[string]any)
		}
		account = &copy
		persist = true
	}
	if key := strings.TrimSpace(input.APIKey); key != "" {
		persist = persist && key == strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
		account.Credentials["api_key"] = key
	}
	if strings.TrimSpace(account.GetOpenAIProtocolAPIKey()) == "" {
		return nil, false, infraerrors.BadRequest("CLINE_API_KEY_REQUIRED", "A Cline API key is required")
	}
	if baseURL := strings.TrimSpace(input.BaseURL); baseURL != "" {
		persist = persist && strings.TrimRight(baseURL, "/") == strings.TrimRight(account.GetOpenAIBaseURL(), "/")
		account.Credentials["base_url"] = baseURL
		account.Credentials["api_base_urls"] = map[string]string{service.APIProtocolChatCompletions: baseURL}
	}
	if input.ProxyID != nil {
		previous := int64(0)
		if account.ProxyID != nil {
			previous = *account.ProxyID
		}
		persist = persist && previous == *input.ProxyID
		account.Proxy = nil
		account.ProxyID = nil
		if *input.ProxyID > 0 {
			id := *input.ProxyID
			account.ProxyID = &id
		}
	}
	if account.ProxyID != nil && account.Proxy == nil {
		proxy, err := h.adminService.GetProxy(ctx, *account.ProxyID)
		if err != nil || proxy == nil {
			return nil, false, infraerrors.New(http.StatusNotFound, "PROXY_NOT_FOUND", "Proxy not found")
		}
		account.Proxy = proxy
	}
	return account, persist, nil
}

func (h *AccountHandler) DiscoverClineUpstreams(c *gin.Context) {
	var req struct {
		clineProbeConnection
		Model string `json:"model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid Cline discovery request")
		return
	}
	if h.accountTestService == nil {
		response.InternalError(c, "Account test service is not configured")
		return
	}
	account, persist, err := h.resolveClineProbeAccount(c.Request.Context(), req.clineProbeConnection)
	if response.ErrorFrom(c, err) {
		return
	}
	discovery, err := h.accountTestService.DiscoverClineUpstreams(c.Request.Context(), account, req.Model, persist)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, discovery)
}

func (h *AccountHandler) VerifyClineUpstream(c *gin.Context) {
	var req struct {
		clineProbeConnection
		Model  string                   `json:"model"`
		Pin    service.ClineUpstreamPin `json:"pin"`
		Stream bool                     `json:"stream"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid Cline verification request")
		return
	}
	if h.accountTestService == nil {
		response.InternalError(c, "Account test service is not configured")
		return
	}
	account, _, err := h.resolveClineProbeAccount(c.Request.Context(), req.clineProbeConnection)
	if response.ErrorFrom(c, err) {
		return
	}
	verification, err := h.accountTestService.VerifyClineUpstream(c.Request.Context(), account, req.Model, req.Pin, req.Stream)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, verification)
}
