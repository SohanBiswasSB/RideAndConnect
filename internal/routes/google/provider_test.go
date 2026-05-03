package google

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SohanBiswasSB/rideandconnect/internal/routes"
)

// recordedHappyPath is a representative computeRoutes response with the
// minimum fields set in our X-Goog-FieldMask. Locks the response mapping
// so a future refactor can't silently break the parser.
const recordedHappyPath = `{
  "routes": [{
    "distanceMeters": 12345,
    "duration": "1234s",
    "polyline": { "encodedPolyline": "abc_xyz" }
  }]
}`

func TestGoogle_Compute_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Goog-Api-Key"); got != "test-key" {
			t.Errorf("missing/wrong api key header: %q", got)
		}
		if got := r.Header.Get("X-Goog-FieldMask"); got == "" {
			t.Errorf("missing field mask header")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"travelMode":"TWO_WHEELER"`) {
			t.Errorf("expected TWO_WHEELER in body; got %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(recordedHappyPath))
	}))
	defer srv.Close()

	p, err := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin:      routes.Point{Latitude: 12.97, Longitude: 77.59},
		Destination: routes.Point{Latitude: 13.37, Longitude: 77.68},
		TravelMode:  routes.TravelModeTwoWheeler,
	})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if resp.DistanceMeters != 12345 {
		t.Errorf("distance = %d, want 12345", resp.DistanceMeters)
	}
	if resp.DurationSeconds != 1234 {
		t.Errorf("duration = %d, want 1234", resp.DurationSeconds)
	}
	if resp.EncodedPolyline != "abc_xyz" {
		t.Errorf("polyline = %q, want abc_xyz", resp.EncodedPolyline)
	}
	if resp.Provider != "google_routes" {
		t.Errorf("provider = %q", resp.Provider)
	}
}

func TestGoogle_Compute_NoRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"routes":[]}`))
	}))
	defer srv.Close()
	p, _ := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	_, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin: routes.Point{Latitude: 1, Longitude: 2}, Destination: routes.Point{Latitude: 3, Longitude: 4}, TravelMode: routes.TravelModeBicycle,
	})
	if !errors.Is(err, routes.ErrNoRoute) {
		t.Errorf("want ErrNoRoute, got %v", err)
	}
}

func TestGoogle_Compute_AuthFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p, _ := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	_, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin: routes.Point{Latitude: 1, Longitude: 2}, Destination: routes.Point{Latitude: 3, Longitude: 4}, TravelMode: routes.TravelModeBicycle,
	})
	if !errors.Is(err, routes.ErrUpstreamAuth) {
		t.Errorf("want ErrUpstreamAuth, got %v", err)
	}
}

func TestGoogle_Compute_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	p, _ := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	_, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin: routes.Point{Latitude: 1, Longitude: 2}, Destination: routes.Point{Latitude: 3, Longitude: 4}, TravelMode: routes.TravelModeBicycle,
	})
	if !errors.Is(err, routes.ErrUpstream) {
		t.Errorf("want ErrUpstream, got %v", err)
	}
}

func TestGoogle_Compute_TwoWheelerFallsBackToBicycleOn400(t *testing.T) {
	// First call: TWO_WHEELER → 400. Second call: BICYCLE → success.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if calls == 1 {
			if !strings.Contains(string(body), `"TWO_WHEELER"`) {
				t.Errorf("first call expected TWO_WHEELER")
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(string(body), `"BICYCLE"`) {
			t.Errorf("second call expected BICYCLE; got %s", string(body))
		}
		_, _ = w.Write([]byte(recordedHappyPath))
	}))
	defer srv.Close()
	p, _ := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	resp, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin: routes.Point{Latitude: 1, Longitude: 2}, Destination: routes.Point{Latitude: 3, Longitude: 4}, TravelMode: routes.TravelModeTwoWheeler,
	})
	if err != nil {
		t.Fatalf("Compute (with fallback): %v", err)
	}
	if resp.TravelMode != routes.TravelModeBicycle {
		t.Errorf("TravelMode after fallback = %q, want BICYCLE", resp.TravelMode)
	}
	if calls != 2 {
		t.Errorf("expected 2 provider calls (try + fallback), got %d", calls)
	}
}
