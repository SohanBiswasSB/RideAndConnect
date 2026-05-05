package routes

import (
	"context"
	"errors"
	"fmt"
)

// MultiProvider tries each wrapped provider in order until one succeeds.
// Used to fall back from Google to Mapbox (or vice versa) on upstream
// failures so a single provider outage doesn't 503 the whole route flow.
//
// Failures classed as "config / validation" (non-retryable) short-circuit
// — there's no point asking another provider for the same bad input.
// Upstream / circuit / timeout failures fall through to the next adapter.
type MultiProvider struct {
	providers []Provider
}

// NewMulti returns a MultiProvider over the supplied adapters in order.
// Nil entries are skipped so callers can pass `NewMulti(google, mapbox)`
// without having to filter unconfigured adapters first.
func NewMulti(provs ...Provider) *MultiProvider {
	out := make([]Provider, 0, len(provs))
	for _, p := range provs {
		if p != nil {
			out = append(out, p)
		}
	}
	return &MultiProvider{providers: out}
}

// Name returns a stable identifier. The actual provider that served any
// given request is reflected in ComputeResponse.Provider.
func (m *MultiProvider) Name() string { return "multi" }

// Compute tries each provider in order. Returns the first successful
// response. If all providers fail, returns the last error wrapped with
// the count of attempts.
func (m *MultiProvider) Compute(ctx context.Context, req ComputeRequest) (ComputeResponse, error) {
	if len(m.providers) == 0 {
		return ComputeResponse{}, ErrProviderUnconfigured
	}
	var lastErr error
	for i, p := range m.providers {
		resp, err := p.Compute(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// Don't bother retrying unrecoverable errors against a fresh
		// adapter — the input is the problem, not the upstream.
		if errors.Is(err, ErrInvalidCoordinate) ||
			errors.Is(err, ErrInvalidTravelMode) ||
			errors.Is(err, ErrZeroLengthRoute) ||
			errors.Is(err, ErrNoRoute) {
			return ComputeResponse{}, err
		}
		_ = i
	}
	return ComputeResponse{}, fmt.Errorf("multi: all %d providers failed: %w", len(m.providers), lastErr)
}

// Capabilities returns the union of underlying adapters' capabilities —
// any feature supported by at least one wrapped provider is reported as
// supported. The MultiProvider can always satisfy the request when at
// least one inner provider can.
func (m *MultiProvider) Capabilities() Capabilities {
	var c Capabilities
	for _, p := range m.providers {
		pc := p.Capabilities()
		c.SupportsTwoWheeler = c.SupportsTwoWheeler || pc.SupportsTwoWheeler
		c.SupportsCycling = c.SupportsCycling || pc.SupportsCycling
		c.SupportsWalking = c.SupportsWalking || pc.SupportsWalking
	}
	return c
}
