package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type CompositeRouteResolver struct {
	repo CompositeModelRouteRepository
}

func NewCompositeRouteResolver(repo CompositeModelRouteRepository) *CompositeRouteResolver {
	return &CompositeRouteResolver{repo: repo}
}

func (r *CompositeRouteResolver) Resolve(ctx context.Context, groupID int64, model, endpoint string) (CompositeRouteDecision, error) {
	model = strings.TrimSpace(model)
	endpoint = normalizeCompositeRouteEndpoint(endpoint)
	decision := CompositeRouteDecision{
		GroupID:     groupID,
		PublicModel: model,
		Endpoint:    endpoint,
	}
	if model == "" {
		decision.Reason = "model is required"
		return decision, nil
	}
	if !isKnownCompositeRouteEndpoint(endpoint) {
		decision.Reason = "unsupported endpoint"
		return decision, nil
	}

	var unsupportedRoute *CompositeModelRoute
	if r != nil && r.repo != nil && groupID > 0 {
		routes, err := r.repo.ListByGroup(ctx, groupID, false)
		if err != nil {
			return decision, fmt.Errorf("list composite routes: %w", err)
		}
		if route, supported, ok := matchCompositeRoute(routes, model, endpoint); ok && supported {
			upstreamModel := strings.TrimSpace(route.UpstreamModel)
			if upstreamModel == "" {
				upstreamModel = model
			}
			return CompositeRouteDecision{
				Matched:        true,
				Source:         CompositeRouteSourceExplicit,
				GroupID:        groupID,
				PublicModel:    model,
				TargetPlatform: route.TargetPlatform,
				UpstreamModel:  upstreamModel,
				Endpoint:       endpoint,
				Route:          &route,
			}, nil
		} else if ok {
			unsupportedRoute = &route
		}
	}
	// An explicit model mapping remains authoritative even when its target
	// cannot serve this endpoint. Do not silently fall back to the model-name
	// detector and change provider/cost; return a rejectable decision instead.
	if unsupportedRoute != nil {
		decision.Source = CompositeRouteSourceExplicit
		decision.TargetPlatform = unsupportedRoute.TargetPlatform
		decision.Route = unsupportedRoute
		decision.Reason = "endpoint is not supported by target platform"
		return decision, nil
	}

	if platform, ok := DetectModelPlatform(model); ok {
		if !CompositeRouteEndpointSupported(platform, endpoint) {
			decision.Source = CompositeRouteSourceDetector
			decision.TargetPlatform = platform
			decision.Reason = "endpoint is not supported by detected platform"
			return decision, nil
		}
		return CompositeRouteDecision{
			Matched:        true,
			Source:         CompositeRouteSourceDetector,
			GroupID:        groupID,
			PublicModel:    model,
			TargetPlatform: platform,
			UpstreamModel:  model,
			Endpoint:       endpoint,
		}, nil
	}
	decision.Reason = "no explicit route or built-in detector match"
	return decision, nil
}

func matchCompositeRoute(routes []CompositeModelRoute, model, endpoint string) (CompositeModelRoute, bool, bool) {
	if len(routes) == 0 {
		return CompositeModelRoute{}, false, false
	}

	type candidate struct {
		route          CompositeModelRoute
		supported      bool
		matchStrength  int
		endpointWeight int
		prefixLen      int
	}
	candidates := make([]candidate, 0, len(routes))
	for _, route := range routes {
		route.Endpoint = normalizeCompositeRouteEndpoint(route.Endpoint)
		if route.Endpoint != endpoint && route.Endpoint != CompositeRouteEndpointAny {
			continue
		}
		route.MatchType = normalizeCompositeRouteMatchType(route.MatchType)
		publicModel := strings.TrimSpace(route.PublicModel)
		if publicModel == "" {
			continue
		}

		matchStrength := 0
		prefixLen := len(publicModel)
		switch route.MatchType {
		case CompositeRouteMatchExact:
			if publicModel != model {
				continue
			}
			matchStrength = 2
		case CompositeRouteMatchPrefix:
			if !strings.HasPrefix(model, publicModel) {
				continue
			}
			matchStrength = 1
		default:
			continue
		}
		endpointWeight := 0
		if route.Endpoint == endpoint {
			endpointWeight = 1
		}
		candidates = append(candidates, candidate{
			route:          route,
			supported:      CompositeRouteEndpointSupported(route.TargetPlatform, endpoint),
			matchStrength:  matchStrength,
			endpointWeight: endpointWeight,
			prefixLen:      prefixLen,
		})
	}
	if len(candidates) == 0 {
		return CompositeModelRoute{}, false, false
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		// A wildcard on a capability-constrained provider must not shadow a
		// route that can actually serve this endpoint. Keep an unsupported
		// candidate only as the final fallback so the gateway can reject it
		// explicitly when no valid route exists.
		if a.supported != b.supported {
			return a.supported
		}
		if a.matchStrength != b.matchStrength {
			return a.matchStrength > b.matchStrength
		}
		if a.endpointWeight != b.endpointWeight {
			return a.endpointWeight > b.endpointWeight
		}
		if a.prefixLen != b.prefixLen {
			return a.prefixLen > b.prefixLen
		}
		if a.route.Priority != b.route.Priority {
			return a.route.Priority < b.route.Priority
		}
		return a.route.ID < b.route.ID
	})
	return candidates[0].route, candidates[0].supported, true
}
