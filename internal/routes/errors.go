package routes

import "errors"

// Sentinel errors returned by adapters and the factory. Service code uses
// errors.Is to discriminate; resolvers map these to API.md §1.3
// extensions.code values.
var (
	// ErrInvalidCoordinate — origin/destination outside WGS84 bounds or NaN/Inf.
	ErrInvalidCoordinate = errors.New("routes: invalid coordinate")

	// ErrInvalidTravelMode — travel mode is not one of the documented enum values.
	ErrInvalidTravelMode = errors.New("routes: invalid travel mode")

	// ErrZeroLengthRoute — origin equals destination after validation.
	ErrZeroLengthRoute = errors.New("routes: zero-length route requested")

	// ErrNoRoute — provider returned no route for an otherwise valid input.
	// Maps to GraphQL "UNPROCESSABLE".
	ErrNoRoute = errors.New("routes: no route returned by provider")

	// ErrUpstream — provider returned 5xx, 429, network or timeout failure.
	// Maps to GraphQL "UPSTREAM_UNAVAILABLE". Retryable from the caller's
	// perspective; circuit breaker increments on this.
	ErrUpstream = errors.New("routes: upstream provider error")

	// ErrUpstreamAuth — provider rejected the API key/token (401/403). Pages
	// oncall in production. Maps to GraphQL "INTERNAL" (we never tell the
	// client our key is bad).
	ErrUpstreamAuth = errors.New("routes: upstream auth failed")

	// ErrUpstreamValidation — provider rejected the request as malformed
	// (400/422). Indicates a bug in the adapter or a previously-unvalidated
	// edge case. Maps to GraphQL "INTERNAL" — never surface provider error
	// text to clients.
	ErrUpstreamValidation = errors.New("routes: upstream rejected request")

	// ErrCircuitOpen — circuit breaker is open for the active provider.
	// Maps to GraphQL "UPSTREAM_UNAVAILABLE" with Retry-After.
	ErrCircuitOpen = errors.New("routes: circuit open")

	// ErrUnknownProvider — factory could not resolve the requested provider name.
	ErrUnknownProvider = errors.New("routes: unknown provider")

	// ErrProviderUnconfigured — factory found a known name but the adapter
	// is not wired (missing API key/token at startup).
	ErrProviderUnconfigured = errors.New("routes: provider not configured")
)
