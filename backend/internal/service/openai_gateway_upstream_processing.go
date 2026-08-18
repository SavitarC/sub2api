package service

import "context"

func (s *OpenAIGatewayService) processUpstreamRequest(ctx context.Context, req UpstreamRequest) (*ProcessedUpstreamRequest, error) {
	return s.processUpstreamRequestWithPolicy(ctx, req, true)
}

func (s *OpenAIGatewayService) processUpstreamRequestWithoutPolicy(ctx context.Context, req UpstreamRequest) (*ProcessedUpstreamRequest, error) {
	return s.processUpstreamRequestWithPolicy(ctx, req, false)
}

func (s *OpenAIGatewayService) processUpstreamRequestWithPolicy(ctx context.Context, req UpstreamRequest, usePolicy bool) (*ProcessedUpstreamRequest, error) {
	if s == nil {
		return NewUpstreamRequestProcessor(nil, nil).Process(ctx, req)
	}
	processor := s.upstreamRequestProcessor
	if processor == nil || !usePolicy {
		// Tests and small tools often construct OpenAIGatewayService with a
		// struct literal. Keep that usage compatible without mutating the
		// service from a concurrent request path.
		var policy UpstreamRequestPolicy
		if usePolicy {
			policy = s.applyOpenAIFastPolicyToBody
		}
		processor = NewUpstreamRequestProcessor(s.cfg, policy)
	}
	return processor.Process(ctx, req)
}
