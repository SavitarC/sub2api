package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

type geminiForwardModelKey struct{}

func geminiForwardModel(ctx context.Context, account *Account, model string) string {
	if mapped, ok := ctx.Value(geminiForwardModelKey{}).(string); ok && mapped != "" {
		return mapped
	}
	if account.Type == AccountTypeAPIKey || account.Type == AccountTypeServiceAccount {
		return account.GetMappedModel(model)
	}
	return model
}

func (s *OpenAIGatewayService) SetGeminiCompatService(compat *GeminiMessagesCompatService) {
	s.geminiCompatService = compat
}

func (s *OpenAIGatewayService) forwardGeminiCompat(ctx context.Context, c *gin.Context, account *Account, body []byte, mappedModel, inbound string) (*OpenAIForwardResult, error) {
	if s.geminiCompatService == nil {
		err := errors.New("Gemini compatibility service is not configured")
		if inbound == APIProtocolAnthropic {
			writeAnthropicError(c, http.StatusInternalServerError, "api_error", err.Error())
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "server_error", "message": err.Error()}})
		}
		return nil, err
	}
	ctx = context.WithValue(ctx, geminiForwardModelKey{}, mappedModel)
	SetActualOpenAIUpstreamEndpoint(c, "/v1beta/models")
	var result *ForwardResult
	var err error
	switch inbound {
	case APIProtocolAnthropic:
		result, err = s.geminiCompatService.Forward(ctx, c, account, body)
	case APIProtocolChatCompletions:
		result, err = s.geminiCompatService.ForwardAsChatCompletions(ctx, c, account, body)
	case APIProtocolResponses:
		result, err = s.geminiCompatService.ForwardAsResponses(ctx, c, account, body)
	default:
		return nil, errors.New("unsupported Gemini compatibility protocol")
	}
	return geminiOpenAIForwardResult(result), err
}

func geminiOpenAIForwardResult(result *ForwardResult) *OpenAIForwardResult {
	if result == nil {
		return nil
	}
	usage := claudeUsageToOpenAIUsage(&result.Usage)
	usage.ImageOutputTokens = result.Usage.ImageOutputTokens
	return &OpenAIForwardResult{
		RequestID: result.RequestID, UpstreamHeaders: result.UpstreamHeaders,
		Usage: usage, Model: result.Model, BillingModel: result.UpstreamModel,
		UpstreamModel: result.UpstreamModel, UpstreamEndpoint: "/v1beta/models",
		UpstreamResponseModel:         result.UpstreamResponseModel,
		UpstreamResponseModelConflict: result.UpstreamResponseModelConflict,
		UpstreamResponseServiceTier:   result.UpstreamResponseServiceTier,
		ServiceTier:                   result.ServiceTier, ReasoningEffort: result.ReasoningEffort,
		RequestedReasoningEffort: result.RequestedReasoningEffort,
		Stream:                   result.Stream, Duration: result.Duration, FirstTokenMs: result.FirstTokenMs,
		ClientDisconnect: result.ClientDisconnect, ImageCount: result.ImageCount,
		ImageEndpoint: result.ImageEndpoint, ImageSize: result.ImageSize,
		ImageInputSize: result.ImageInputSize, ImageOutputSize: result.ImageOutputSize,
		ImageOutputSizes: result.ImageOutputSizes, ImageSizeSource: result.ImageSizeSource,
		ImageSizeBreakdown: result.ImageSizeBreakdown, SearchCount: result.SearchCount,
		AudioUsage: result.AudioUsage,
	}
}
