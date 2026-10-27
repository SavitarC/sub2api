package service

import (
	"context"
	"errors"
	"strings"
)

var ErrCompositeResponseOwnerUnavailable = errors.New("previous_response_id is not available for this user, group, and model")

type compositeResponseAccountKey struct{}

func WithCompositeResponseAccount(ctx context.Context, accountID int64) context.Context {
	return context.WithValue(ctx, compositeResponseAccountKey{}, accountID)
}

func CompositeResponseAccountFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	id, ok := ctx.Value(compositeResponseAccountKey{}).(int64)
	return id, ok && id > 0
}

// ResolveCompositeResponseAccount reads the durable owner without changing affinity when it is temporarily unavailable.
func (s *OpenAIGatewayService) ResolveCompositeResponseAccount(ctx context.Context, apiKey *APIKey, responseID, publicModel string) (*Account, error) {
	if s == nil || s.accountRepo == nil || apiKey == nil || apiKey.User == nil ||
		apiKey.GroupID == nil || *apiKey.GroupID <= 0 || strings.TrimSpace(publicModel) == "" {
		return nil, ErrCompositeResponseOwnerUnavailable
	}
	owned, err := s.ValidateOpenAIHTTPResponseOwner(ctx, *apiKey.GroupID, responseID, apiKey.User.ID, apiKey.ID)
	if err != nil || !owned {
		return nil, ErrCompositeResponseOwnerUnavailable
	}
	accountID, err := s.getOpenAIWSStateStore().GetResponseAccount(ctx, *apiKey.GroupID, responseID)
	if err != nil || accountID <= 0 {
		return nil, ErrCompositeResponseOwnerUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil || !account.IsOpenAICompatible() ||
		!openAIStickyAccountMatchesGroup(account, apiKey.GroupID) {
		return nil, ErrCompositeResponseOwnerUnavailable
	}
	modelSupported := explicitModelMappingClaims(*account, publicModel)
	if source, _ := CompositeRouteSourceFromContext(ctx); source == CompositeRouteSourceDetector {
		modelSupported = account.IsModelSupported(publicModel)
	}
	if !modelSupported {
		return nil, ErrCompositeResponseOwnerUnavailable
	}
	return account, nil
}
