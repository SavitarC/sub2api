package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

// Cline 积分余额来自账号接口（Cline 扩展的账户页与第三方用量工具同源），使用账号
// API Key（Bearer）：
//
//	GET /api/v1/users/me            -> {id, email, ...}
//	GET /api/v1/users/{id}/balance  -> {balance, userId}
//
// 响应可能包在 {success, error, data} 信封里。balance 单位为美分（Cline 扩展按
// balance / 100 显示美元余额）。ClinePass 的用量上限没有查询接口。

const clineAPIBase = "https://" + clineAPIHost

var errClineInvalidResponse = errors.New("invalid cline account response")

// clineHTTPError 是账号接口的非 2xx 响应。
type clineHTTPError struct {
	status int
	body   string
}

func (e *clineHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

// clineAccountGet 请求一个账号接口，返回拆掉 {success, error, data} 信封后的 JSON。
func (s *CNProviderBalanceService) clineAccountGet(ctx context.Context, account *Account, apiKey, proxyURL, path string) (gjson.Result, error) {
	validatedURL, err := cnValidateProbeURL(s.cfg, clineAPIBase+path)
	if err != nil {
		return gjson.Result{}, infraerrors.New(http.StatusForbidden, "CN_BALANCE_URL_REJECTED", err.Error())
	}
	callCtx, cancel := context.WithTimeout(ctx, cnQuotaUpstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, validatedURL, nil)
	if err != nil {
		return gjson.Result{}, infraerrors.Newf(http.StatusInternalServerError, "CN_BALANCE_REQUEST_BUILD_FAILED", "build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return gjson.Result{}, fmt.Errorf("upstream request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, cnQuotaMaxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return gjson.Result{}, &clineHTTPError{status: resp.StatusCode, body: truncate(strings.TrimSpace(string(body)), 240)}
	}
	if !gjson.ValidBytes(body) {
		return gjson.Result{}, fmt.Errorf("%w: %s", errClineInvalidResponse, path)
	}
	parsed := gjson.ParseBytes(body)
	if success := parsed.Get("success"); success.Exists() {
		if !success.Bool() {
			return gjson.Result{}, &clineHTTPError{status: resp.StatusCode, body: truncate(parsed.Get("error").String(), 240)}
		}
		return parsed.Get("data"), nil
	}
	return parsed, nil
}

// queryClineBalance 查询 Cline 积分余额（美元）并落余额快照。
func (s *CNProviderBalanceService) queryClineBalance(ctx context.Context, account *Account) (*CNProviderBalanceResult, error) {
	apiKey := strings.TrimSpace(account.GetCNAPIKey())
	if apiKey == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CN_BALANCE_NO_APIKEY", "account api_key is empty")
	}
	proxyURL := s.resolveProxyURL(ctx, account)
	now := time.Now().UTC()
	failure := func(err error) (*CNProviderBalanceResult, error) {
		var httpErr *clineHTTPError
		switch {
		case errors.As(err, &httpErr):
			return &CNProviderBalanceResult{
				Provider:   PlatformCline,
				StatusCode: httpErr.status,
				FetchedAt:  now.Unix(),
				Available:  true,
				Error:      fmt.Sprintf("Balance query failed (HTTP %d): %s", httpErr.status, httpErr.body),
			}, nil
		case errors.Is(err, errClineInvalidResponse):
			return &CNProviderBalanceResult{Provider: PlatformCline, StatusCode: http.StatusOK, FetchedAt: now.Unix(), Available: true, Error: "Invalid balance response"}, nil
		case isApplicationError(err):
			return nil, err
		default:
			return nil, infraerrors.Newf(http.StatusBadGateway, "CN_BALANCE_REQUEST_FAILED", "%v", err)
		}
	}

	me, err := s.clineAccountGet(ctx, account, apiKey, proxyURL, "/api/v1/users/me")
	if err != nil {
		return failure(err)
	}
	userID := strings.TrimSpace(me.Get("id").String())
	if userID == "" {
		return failure(fmt.Errorf("%w: missing user id", errClineInvalidResponse))
	}
	balance, err := s.clineAccountGet(ctx, account, apiKey, proxyURL, "/api/v1/users/"+url.PathEscape(userID)+"/balance")
	if err != nil {
		return failure(err)
	}
	cents := balance.Get("balance")
	if !cents.Exists() || (cents.Type != gjson.Number && cents.Type != gjson.String) {
		return failure(fmt.Errorf("%w: missing balance", errClineInvalidResponse))
	}
	usd := math.Round(cents.Float()*100) / 10000
	result := &CNProviderBalanceResult{
		Provider:   PlatformCline,
		Success:    true,
		Balance:    usd,
		Currency:   "USD",
		Balances:   []CNProviderBalanceEntry{{Currency: "USD", Balance: usd}},
		Available:  true,
		StatusCode: http.StatusOK,
		FetchedAt:  now.Unix(),
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, cnBalanceExtraUpdates(PlatformCline, result, now)); err != nil {
		slog.Warn("cn_balance_persist_failed", "account_id", account.ID, "provider", PlatformCline, "error", err)
	} else {
		result.Persisted = true
	}
	return result, nil
}
