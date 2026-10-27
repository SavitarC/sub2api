package service

import (
	"context"
	"slices"
)

// Automatic model routes may use only the declaring accounts on this attempt's platform.
func compositeAccountMatchesRoute(ctx context.Context, account *Account) bool {
	if account != nil && !CompositeAccountAttemptAllowed(ctx, account.ID) {
		return false
	}
	if accountID, pinned := CompositeResponseAccountFromContext(ctx); pinned && (account == nil || account.ID != accountID) {
		return false
	}
	if source, _ := CompositeRouteSourceFromContext(ctx); source != CompositeRouteSourceAccount {
		return true
	}
	platform, platformOK := ResolvedTargetPlatformFromContext(ctx)
	model, modelOK := RequestedPublicModelFromContext(ctx)
	if account == nil || !platformOK || !modelOK || account.Platform != platform || !explicitModelMappingClaims(*account, model) {
		return false
	}
	if decision, ok := CompositeRouteDecisionFromContext(ctx); ok && len(decision.Candidates) > 0 {
		if endpoint := normalizeCompositeRouteEndpoint(decision.Endpoint); endpoint != CompositeRouteEndpointAny {
			mapped := stringMappingFromRaw(account.Credentials["model_mapping"])[model]
			return slices.Contains(compositeAccountSupportedEndpoints(account, mapped), endpoint) && compositeAccountSupportsRequest(ctx, account, mapped)
		}
	}
	return true
}
