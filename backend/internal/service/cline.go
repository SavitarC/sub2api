package service

import (
	"net/url"
	"strings"
)

// Cline API（https://docs.cline.bot/api/overview）：OpenAI 兼容的多模型聚合端点，
// 基址 https://api.cline.bot/api/v1，只提供 Chat Completions（Cline 自己的客户端同样
// 只走该端点），模型名带厂商前缀（如 anthropic/claude-sonnet-4-6）。Responses 与
// Anthropic 入站由网关转换为 Chat Completions。
//
// 同一个 API Key 下有两种计费方式，由模型名决定：
//   - 按量计费（Usage-Billing）：消耗账户积分，积分余额可经账号接口查询；
//   - ClinePass 订阅：cline-pass/* 模型，按 5 小时滚动 / 每周 / 每月三档用量上限限流，
//     上游没有查询用量的接口，只能在超限报错后被动冷却。
//
// 账号的接入模式（payg / pass）表示该账号主要承担哪种计费的流量：与错误所属计费方式
// 一致的超限整号冷却，否则只限当前模型（见 ratelimit_cline.go）。

const (
	// DefaultClineTestModel 是按量计费账号连接测试的默认模型（价格低、长期在售）。
	DefaultClineTestModel = "deepseek/deepseek-v4-flash"
	// DefaultClinePassTestModel 是 ClinePass 账号连接测试的默认模型。
	DefaultClinePassTestModel = "cline-pass/glm-5.3-flash"

	clineAPIHost = "api.cline.bot"
)

// IsCline 报告账号是否为 Cline 平台账号。
func (a *Account) IsCline() bool {
	return a != nil && a.Platform == PlatformCline
}

// IsClinePass 报告 Cline 账号是否以 ClinePass 订阅接入。
func (a *Account) IsClinePass() bool {
	return a.IsCline() && strings.TrimSpace(a.GetCredential("account_mode")) == AccountModePass
}

// isOfficialClineHost 报告 URL 是否指向官方 Cline API 主机。
func isOfficialClineHost(target string) bool {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), clineAPIHost)
}

// clineBalanceSupported 报告账号能否查询积分余额：按量计费的 API Key 账号且推理端点
// 指向官方主机。自定义中转的 Key 不发往官方账号接口；ClinePass 账号的可用性与积分无关。
func (a *Account) clineBalanceSupported() bool {
	return a.IsCline() && !a.IsClinePass() && a.Type == AccountTypeAPIKey && isOfficialClineHost(a.GetOpenAIBaseURL())
}
