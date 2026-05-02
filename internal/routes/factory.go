package routes

import "fmt"

// Factory resolves a Provider by name. The runtime constructs a Factory at
// startup with whichever adapters were successfully configured, and the
// service layer asks for the active one based on the ROUTE_PROVIDER env var.
//
// Both adapters can be present simultaneously — the inactive one stays
// imported but unwired so swapping is a single env change.
type Factory struct {
	providers map[string]Provider
}

// NewFactory builds a Factory. Pass each configured adapter (or nil for ones
// you don't intend to support in this deployment). Provider names follow the
// adapter's Name() (e.g. "google_routes", "mapbox").
func NewFactory(provs ...Provider) *Factory {
	m := make(map[string]Provider, len(provs))
	for _, p := range provs {
		if p == nil {
			continue
		}
		m[p.Name()] = p
	}
	return &Factory{providers: m}
}

// Get returns the adapter for the given name. Names accepted:
//
//   - "google", "google_routes" → Google adapter
//   - "mapbox"                  → Mapbox adapter
//
// Unknown names return ErrUnknownProvider; known names whose adapter wasn't
// supplied to NewFactory return ErrProviderUnconfigured.
func (f *Factory) Get(name string) (Provider, error) {
	canonical, ok := canonicalProviderName(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
	}
	p, ok := f.providers[canonical]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrProviderUnconfigured, canonical)
	}
	return p, nil
}

// Names returns the configured provider names, useful for /readyz and
// startup logs.
func (f *Factory) Names() []string {
	out := make([]string, 0, len(f.providers))
	for k := range f.providers {
		out = append(out, k)
	}
	return out
}

// canonicalProviderName accepts user-friendly env values and returns the
// adapter's stable Name(). Keep this in lockstep with each adapter's Name().
func canonicalProviderName(name string) (string, bool) {
	switch name {
	case "google", "google_routes":
		return "google_routes", true
	case "mapbox":
		return "mapbox", true
	default:
		return "", false
	}
}
