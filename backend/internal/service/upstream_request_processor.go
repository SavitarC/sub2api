package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// UserIsolationExtraKey is the account extra field that controls upstream
// user identity derivation.
const UserIsolationExtraKey = "user_isolation_mode"

const (
	UserIsolationModeOff               = "off"
	UserIsolationModeAuthenticatedUser = "authenticated_user"
)

// UpstreamProtocol identifies the final wire protocol sent to an upstream
// provider. It is deliberately independent of the client-facing endpoint.
type UpstreamProtocol string

const (
	UpstreamProtocolResponses       UpstreamProtocol = "responses"
	UpstreamProtocolChatCompletions UpstreamProtocol = "chat_completions"
	UpstreamProtocolAnthropic       UpstreamProtocol = "anthropic"
)

// UpstreamRequest is the final request candidate after protocol conversion and
// model mapping, but before the request is sent to the upstream.
type UpstreamRequest struct {
	Account                  *Account
	Protocol                 UpstreamProtocol
	Model                    string
	Body                     []byte
	RequestedReasoningEffort string
}

// ProcessedUpstreamRequest contains the body and metadata that must be used by
// both the sender and usage recording for one account attempt.
type ProcessedUpstreamRequest struct {
	Body            []byte
	ServiceTier     *string
	ReasoningEffort *string
}

// UpstreamRequestPolicy is the policy hook used by the shared processor. It is
// intentionally small so both the OpenAI and compatibility gateway services
// can reuse the same processing order without coupling the processor to a
// concrete service.
type UpstreamRequestPolicy func(context.Context, *Account, string, []byte) ([]byte, error)

// UpstreamRequestProcessor applies final provider-aware request processing.
// isolationKey is derived once from process configuration and never emitted in
// logs or sent to an upstream provider.
type UpstreamRequestProcessor struct {
	isolationKey []byte
	policy       UpstreamRequestPolicy
}

// NewUpstreamRequestProcessor constructs a processor. An empty user isolation
// secret falls back to a domain-separated key derived from jwt.secret.
func NewUpstreamRequestProcessor(cfg *config.Config, policy UpstreamRequestPolicy) *UpstreamRequestProcessor {
	return &UpstreamRequestProcessor{
		isolationKey: upstreamUserIsolationKey(cfg),
		policy:       policy,
	}
}

// Process applies provider wire normalization, the configured fast/flex policy,
// and optional authenticated-user isolation in that order. Callers must invoke
// it again whenever the selected account or final wire protocol changes.
func (p *UpstreamRequestProcessor) Process(ctx context.Context, req UpstreamRequest) (*ProcessedUpstreamRequest, error) {
	if p == nil {
		p = &UpstreamRequestProcessor{}
	}
	body := append([]byte(nil), req.Body...)

	var err error
	body, err = normalizeProviderWireRequest(req, body)
	if err != nil {
		return nil, err
	}
	if p.policy != nil {
		body, err = p.policy(ctx, req.Account, strings.TrimSpace(req.Model), body)
		if err != nil {
			return nil, err
		}
	}
	body, err = p.applyUserIsolation(ctx, req, body)
	if err != nil {
		return nil, err
	}

	effort := p.reasoningEffort(req, body)
	return &ProcessedUpstreamRequest{
		Body:            body,
		ServiceTier:     extractOpenAIServiceTierFromBody(body),
		ReasoningEffort: stringPointerOrNil(effort),
	}, nil
}

func normalizeProviderWireRequest(req UpstreamRequest, body []byte) ([]byte, error) {
	if req.Account == nil || !req.Account.IsDeepseek() {
		return body, nil
	}

	// The DeepSeek Responses/Chat/Anthropic endpoints accept max. Existing
	// generic compatibility code intentionally folds max to xhigh, so the
	// processor restores max only when the request's original effort was max or
	// the final wire body already explicitly carries max.
	if !isDeepSeekMaxEffort(req, body) {
		return body, nil
	}

	path := ""
	switch req.Protocol {
	case UpstreamProtocolResponses:
		path = "reasoning.effort"
	case UpstreamProtocolChatCompletions:
		path = "reasoning_effort"
	case UpstreamProtocolAnthropic:
		path = "output_config.effort"
	default:
		return body, nil
	}
	updated, err := sjson.SetBytes(body, path, "max")
	if err != nil {
		return body, fmt.Errorf("preserve deepseek max reasoning effort: %w", err)
	}
	return updated, nil
}

func isDeepSeekMaxEffort(req UpstreamRequest, body []byte) bool {
	if strings.EqualFold(strings.TrimSpace(req.RequestedReasoningEffort), "max") {
		// A group policy can deliberately lower the final effort. Do not undo
		// that decision when the converted body already contains another known
		// level; xhigh is the conversion marker that represents original max.
		final := extractWireReasoningEffort(body, req.Protocol)
		return final == "" || final == "xhigh" || final == "max"
	}
	return strings.EqualFold(extractWireReasoningEffort(body, req.Protocol), "max")
}

func (p *UpstreamRequestProcessor) reasoningEffort(req UpstreamRequest, body []byte) string {
	if req.Account != nil && req.Account.IsDeepseek() {
		raw := strings.TrimSpace(req.RequestedReasoningEffort)
		if raw == "" {
			raw = extractWireReasoningEffort(body, req.Protocol)
		}
		return normalizeReasoningEffortPreservingMax(raw)
	}
	raw := extractWireReasoningEffort(body, req.Protocol)
	if raw == "" {
		raw = strings.TrimSpace(req.RequestedReasoningEffort)
	}
	return normalizeOpenAIReasoningEffortForModel(raw, req.Model)
}

func extractWireReasoningEffort(body []byte, protocol UpstreamProtocol) string {
	if len(body) == 0 {
		return ""
	}
	switch protocol {
	case UpstreamProtocolAnthropic:
		return strings.TrimSpace(gjson.GetBytes(body, "output_config.effort").String())
	case UpstreamProtocolChatCompletions:
		value := strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
		if value == "" {
			value = strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
		}
		return value
	default:
		value := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
		if value == "" {
			value = strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
		}
		return value
	}
}

func normalizeReasoningEffortPreservingMax(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	switch value {
	case "low", "medium", "high", "xhigh", "extrahigh", "max":
		if value == "extrahigh" {
			return "xhigh"
		}
		return value
	case "none", "minimal", "":
		return ""
	default:
		return ""
	}
}

func stringPointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (p *UpstreamRequestProcessor) applyUserIsolation(ctx context.Context, req UpstreamRequest, body []byte) ([]byte, error) {
	if p == nil || len(p.isolationKey) == 0 || req.Account == nil ||
		!req.Account.IsDeepseek() || req.Account.Type != AccountTypeAPIKey ||
		userIsolationMode(req.Account) != UserIsolationModeAuthenticatedUser {
		return body, nil
	}
	userID := authenticatedUserID(ctx)
	if userID <= 0 {
		return body, nil
	}

	derived := deriveUpstreamUserID(p.isolationKey, req.Account.Platform, userID)
	path := ""
	switch req.Protocol {
	case UpstreamProtocolResponses:
		path = "user"
	case UpstreamProtocolChatCompletions:
		path = "user_id"
	case UpstreamProtocolAnthropic:
		path = "metadata.user_id"
	default:
		return body, nil
	}
	updated, err := sjson.SetBytes(body, path, derived)
	if err != nil {
		return body, fmt.Errorf("set upstream user identity: %w", err)
	}
	return updated, nil
}

func userIsolationMode(account *Account) string {
	if account == nil || account.Extra == nil {
		return UserIsolationModeOff
	}
	value, _ := account.Extra[UserIsolationExtraKey].(string)
	switch strings.ToLower(strings.TrimSpace(value)) {
	case UserIsolationModeAuthenticatedUser:
		return UserIsolationModeAuthenticatedUser
	default:
		return UserIsolationModeOff
	}
}

func authenticatedUserID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	switch value := ctx.Value(ctxkey.UserID).(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case int32:
		return int64(value)
	default:
		return 0
	}
}

func upstreamUserIsolationKey(cfg *config.Config) []byte {
	if cfg == nil {
		return nil
	}
	secret := strings.TrimSpace(cfg.Gateway.UserIsolation.Secret)
	if secret != "" {
		return []byte(secret)
	}
	jwtSecret := strings.TrimSpace(cfg.JWT.Secret)
	if jwtSecret == "" {
		return nil
	}
	digest := sha256.Sum256([]byte("sub2api:upstream-user:isolation-key:v1:" + jwtSecret))
	return digest[:]
}

func deriveUpstreamUserID(key []byte, provider string, userID int64) string {
	message := "sub2api:upstream-user:v1:" + strings.TrimSpace(provider) + ":" + strconv.FormatInt(userID, 10)
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(message))
	return "s2u_v1_" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
