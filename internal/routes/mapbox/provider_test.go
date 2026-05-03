package mapbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SohanBiswasSB/rideandconnect/internal/routes"
)

const recordedHappyPath = `{
  "code": "Ok",
  "routes": [{
    "distance": 12345.49,
    "duration": 1234.7,
    "geometry": "encoded_polyline_xyz"
  }]
}`

func TestMapbox_Compute_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// access_token must be in the query string.
		if r.URL.Query().Get("access_token") != "test-token" {
			t.Errorf("missing access_token query")
		}
		// Profile must be in the path; cycling is the default for TWO_WHEELER.
		if !strings.Contains(r.URL.Path, "/cycling/") {
			t.Errorf("expected cycling profile in path; got %s", r.URL.Path)
		}
		// Coordinates: lon,lat;lon,lat
		if !strings.Contains(r.URL.Path, "77.59,12.97") {
			t.Errorf("expected lon,lat ordering; got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(recordedHappyPath))
	}))
	defer srv.Close()

	p, err := New(Config{AccessToken: "test-token", BaseURL: srv.URL})
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
		t.Errorf("distance = %d (rounded from 12345.49), want 12345", resp.DistanceMeters)
	}
	if resp.DurationSeconds != 1235 {
		t.Errorf("duration = %d (rounded from 1234.7), want 1235", resp.DurationSeconds)
	}
	if resp.EncodedPolyline != "encoded_polyline_xyz" {
		t.Errorf("polyline = %q", resp.EncodedPolyline)
	}
	if resp.Provider != "mapbox" {
		t.Errorf("provider = %q", resp.Provider)
	}
	if resp.TravelMode != routes.TravelModeTwoWheeler {
		t.Errorf("travel_mode = %q (Mapbox echoes input verbatim)", resp.TravelMode)
	}
}

func TestMapbox_Compute_NoRoute_Code(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"NoRoute","routes":[]}`))
	}))
	defer srv.Close()
	p, _ := New(Config{AccessToken: "t", BaseURL: srv.URL})
	_, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin: routes.Point{Latitude: 1, Longitude: 2}, Destination: routes.Point{Latitude: 3, Longitude: 4}, TravelMode: routes.TravelModeBicycle,
	})
	if !errors.Is(err, routes.ErrNoRoute) {
		t.Errorf("want ErrNoRoute, got %v", err)
	}
}

func TestMapbox_Compute_Auth401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p, _ := New(Config{AccessToken: "t", BaseURL: srv.URL})
	_, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin: routes.Point{Latitude: 1, Longitude: 2}, Destination: routes.Point{Latitude: 3, Longitude: 4}, TravelMode: routes.TravelModeBicycle,
	})
	if !errors.Is(err, routes.ErrUpstreamAuth) {
		t.Errorf("want ErrUpstreamAuth, got %v", err)
	}
}

func TestMapbox_Compute_429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p, _ := New(Config{AccessToken: "t", BaseURL: srv.URL})
	_, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin: routes.Point{Latitude: 1, Longitude: 2}, Destination: routes.Point{Latitude: 3, Longitude: 4}, TravelMode: routes.TravelModeBicycle,
	})
	if !errors.Is(err, routes.ErrUpstream) {
		t.Errorf("want ErrUpstream, got %v", err)
	}
}

func TestMapbox_ConfigPicksDriving(t *testing.T) {
	// Operator override: TWO_WHEELER → driving (motorcycle apps).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/driving/") {
			t.Errorf("expected driving profile in path; got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(recordedHappyPath))
	}))
	defer srv.Close()
	p, _ := New(Config{AccessToken: "t", BaseURL: srv.URL, ProfileTwoWheeler: "driving"})
	if _, err := p.Compute(context.Background(), routes.ComputeRequest{
		Origin: routes.Point{Latitude: 12.97, Longitude: 77.59},
		Destination: routes.Point{Latitude: 13.37, Longitude: 77.68},
		TravelMode: routes.TravelModeTwoWheeler,
	}); err != nil {
		t.Fatalf("Compute: %v", err)
	}
}
