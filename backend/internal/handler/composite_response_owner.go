package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CompositeResponseOwner pins a continuation to its actual account before platform dispatch.
func (h *OpenAIGatewayHandler) CompositeResponseOwner(c *gin.Context, responseID string) (string, error) {
	if c == nil || c.Request == nil || h == nil || h.gatewayService == nil {
		return "", service.ErrCompositeResponseOwnerUnavailable
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		return "", service.ErrCompositeResponseOwnerUnavailable
	}
	publicModel, _ := service.RequestedPublicModelFromContext(c.Request.Context())
	account, err := h.gatewayService.ResolveCompositeResponseAccount(c.Request.Context(), apiKey, responseID, publicModel)
	if err != nil {
		return "", err
	}
	c.Request = c.Request.WithContext(service.WithCompositeResponseAccount(c.Request.Context(), account.ID))
	return account.Platform, nil
}
