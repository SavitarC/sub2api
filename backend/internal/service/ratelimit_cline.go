package service

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Cline 上游错误的响应式处理。依据官方错误说明（https://docs.cline.bot/api/errors）与
// Cline 客户端的错误分类（cline/cline：sdk/packages/llms/src/providers/errors.ts、
// apps/vscode/src/services/error/ClineError.ts）：
//
//   - 402 insufficient_credits：按量积分不足；
//   - 429 SPEND_LIMIT_EXCEEDED：组织为成员设的花费上限；
//   - INFERENCE_CAP_ERROR：账户推理额度上限；
//   - "You have reached your … ClinePass limit … Please try again later."：ClinePass
//     5 小时滚动 / 每周 / 每月用量上限，文案不带重置时间；
//   - "the user is not subscribed to required model plan" 与 "organization accounts
//     cannot use individual model inference subscriptions"：没有可用的 ClinePass 订阅；
//   - "Free limit reached on model … try again in …"：免费模型的每日额度，只限该模型。
//
// 错误码只出现在响应体里（状态码对应关系文档没有写全），按文案分类；未识别的错误交回
// 默认逻辑（普通频率限制走默认 429）。
//
// 作用范围：错误所属的计费方式（按量积分 / ClinePass）与账号接入模式一致时整号冷却，
// 否则只限当前模型——同一个 Key 仍可服务另一种计费的模型。

type clineErrorKind int

const (
	clineErrorNone clineErrorKind = iota
	// clineCreditsExhausted 按量积分不足：可恢复停调，积分恢复后由余额检测解除。
	clineCreditsExhausted
	// clineSpendLimit 组织花费上限 / 账户推理额度上限：冷却后由下一次请求重新确认。
	clineSpendLimit
	// clinePassLimit ClinePass 用量上限：按文案中的窗口冷却。
	clinePassLimit
	// clinePassUnavailable 没有可用的 ClinePass 订阅（未订阅或组织账户）。
	clinePassUnavailable
	// clineFreeModelLimit 免费模型的每日额度：只限当前模型。
	clineFreeModelLimit
)

const (
	clinePassLimitReason         = "cline_pass_limit"
	clinePassUnavailableReason   = "cline_pass_unavailable"
	clineSpendLimitReason        = "cline_spend_limit"
	clineFreeModelLimitReason    = "cline_free_model_limit"
	clineCreditsModelLimitReason = "cline_credits_exhausted"

	// clineLimitRecheck 是无法得知重置时间时的冷却：到期后由下一次请求重新确认
	// （管理员可能调高上限、订阅可能续费、5 小时滚动窗口逐步释放）。
	clineLimitRecheck = time.Hour
	// ClinePass 每周 / 每月上限按自然周 / 月重置，上游不给重置时间；冷却较长但不等到
	// 周期结束，避免重置边界（时区、周起始日）判断错误时长时间闲置。
	clinePassWeeklyRecheck  = 6 * time.Hour
	clinePassMonthlyRecheck = 24 * time.Hour
	// clineFreeModelMaxCooldown 是从免费模型文案解析出的重置时长上限（每日额度）。
	clineFreeModelMaxCooldown = 25 * time.Hour
)

var (
	// clinePassLimitPattern 与 Cline 客户端的判定一致："you have reached your" 开头、
	// "please try again later." 结尾，中间含 "clinepass limit"。
	clinePassLimitPattern = regexp.MustCompile(`(?is)you have reached your(.*?)clinepass limit.*?please try again later\.`)
	clineTryAgainPattern  = regexp.MustCompile(`(?i)\btry\s+again\s+in\s+`)
)

func classifyClineError(body []byte) clineErrorKind {
	text := strings.ToLower(string(body))
	switch {
	case strings.Contains(text, "organization accounts cannot use individual model inference subscriptions"),
		strings.Contains(text, "the user is not subscribed to required model plan"):
		return clinePassUnavailable
	case strings.Contains(text, "free limit reached on model"):
		return clineFreeModelLimit
	case clinePassLimitPattern.Match(body):
		return clinePassLimit
	case strings.Contains(text, "spend_limit_exceeded"),
		strings.Contains(text, "inference_cap_error"):
		return clineSpendLimit
	case strings.Contains(text, "insufficient_credits"),
		strings.Contains(text, "insufficient credits"),
		cnProviderResponseIndicatesInsufficientBalance(body):
		return clineCreditsExhausted
	default:
		return clineErrorNone
	}
}

// clineErrorIsPassBilling 报告错误是否属于 ClinePass 订阅（否则属于按量积分）。
func clineErrorIsPassBilling(kind clineErrorKind) bool {
	return kind == clinePassLimit || kind == clinePassUnavailable
}

// clinePassLimitCooldown 按文案中的窗口给出冷却时长。
func clinePassLimitCooldown(body []byte) time.Duration {
	match := clinePassLimitPattern.FindSubmatch(body)
	if match == nil {
		return clineLimitRecheck
	}
	window := strings.ToLower(string(match[1]))
	switch {
	case strings.Contains(window, "month"):
		return clinePassMonthlyRecheck
	case strings.Contains(window, "week"):
		return clinePassWeeklyRecheck
	default:
		return clineLimitRecheck
	}
}

// clineFreeModelResetAfter 解析 "try again in 3h 20m"；无法解析或超出上限时返回 0。
func clineFreeModelResetAfter(body []byte) time.Duration {
	text := string(body)
	loc := clineTryAgainPattern.FindStringIndex(text)
	if loc == nil {
		return 0
	}
	after := parseOpenCodeGoUsageLimitResetDuration("resets in " + text[loc[1]:])
	if after <= 0 || after > clineFreeModelMaxCooldown {
		return 0
	}
	return after
}

// handleClineError 处理已识别的 Cline 上游错误；handled=false 时由调用方继续走默认
// 逻辑。shouldDisable 与同类状态码的既有口径一致：429 不置位；只限单个模型时也不置位。
func (s *RateLimitService) handleClineError(
	ctx context.Context,
	account *Account,
	statusCode int,
	responseBody []byte,
	upstreamMsg string,
) (handled, shouldDisable bool) {
	kind := classifyClineError(responseBody)
	if kind == clineErrorNone {
		return false, false
	}
	now := time.Now()
	modelKey := modelRateLimitKeyForUpstreamModelNotFound(ctx, account, tempUnschedulableModel(ctx, nil))
	accountWide := kind != clineFreeModelLimit && clineErrorIsPassBilling(kind) == account.IsClinePass()
	if modelKey == "" {
		accountWide = true
	}
	disable := accountWide && statusCode != http.StatusTooManyRequests

	var cooldown time.Duration
	var reason string
	switch kind {
	case clineCreditsExhausted:
		if accountWide {
			s.handleCNProviderInsufficientBalance(ctx, account, upstreamMsg)
			return true, disable
		}
		cooldown, reason = s.cnBalanceCooldownDuration(), clineCreditsModelLimitReason
	case clineSpendLimit:
		cooldown, reason = clineLimitRecheck, clineSpendLimitReason
	case clinePassLimit:
		cooldown, reason = clinePassLimitCooldown(responseBody), clinePassLimitReason
	case clinePassUnavailable:
		cooldown, reason = clineLimitRecheck, clinePassUnavailableReason
	case clineFreeModelLimit:
		cooldown, reason = clineFreeModelResetAfter(responseBody), clineFreeModelLimitReason
		if cooldown <= 0 {
			cooldown = clineLimitRecheck
		}
	}
	until := now.Add(cooldown)

	if !accountWide {
		if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, modelKey, until, reason); err != nil {
			slog.Warn("cline_model_limit_set_failed", "account_id", account.ID, "model", modelKey, "reason", reason, "error", err)
		}
		slog.Info("cline_model_limit", "account_id", account.ID, "model", modelKey, "reason", reason, "status_code", statusCode, "until", until.UTC())
		return true, false
	}

	fullReason := reason
	if msg := strings.TrimSpace(upstreamMsg); msg != "" {
		fullReason += ": " + msg
	}
	s.notifyAccountSchedulingBlocked(account, until, reason)
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, fullReason); err != nil {
		slog.Warn("cline_account_limit_set_failed", "account_id", account.ID, "reason", reason, "error", err)
		return false, false
	}
	slog.Info("cline_account_limit", "account_id", account.ID, "reason", reason, "status_code", statusCode, "until", until.UTC())
	return true, disable
}
