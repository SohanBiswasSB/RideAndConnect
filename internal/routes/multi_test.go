package routes

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubProvider struct {
	name string
	resp ComputeResponse
	err  error
	hits int
}

func (s *stubProvider) Name() string             { return s.name }
func (s *stubProvider) Capabilities() Capabilities { return Capabilities{SupportsTwoWheeler: true} }
func (s *stubProvider) Compute(_ context.Context, _ ComputeRequest) (ComputeResponse, error) {
	s.hits++
	return s.resp, s.err
}

func TestMulti_FallsBackOnUpstreamError(t *testing.T) {
	primary := &stubProvider{name: "p1", err: ErrUpstream}
	secondary := &stubProvider{name: "p2", resp: ComputeResponse{Provider: "p2", DistanceMeters: 100, ComputedAt: time.Now()}}
	m := NewMulti(primary, secondary)
	got, err := m.Compute(context.Background(), ComputeRequest{
		Origin:      Point{Latitude: 1, Longitude: 1},
		Destination: Point{Latitude: 2, Longitude: 2},
		TravelMode:  TravelModeTwoWheeler,
	})
	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}
	if got.Provider != "p2" {
		t.Fatalf("provider=%q want p2", got.Provider)
	}
	if primary.hits != 1 || secondary.hits != 1 {
		t.Fatalf("hits primary=%d secondary=%d", primary.hits, secondary.hits)
	}
}

func TestMulti_DoesNotRetryNonRecoverable(t *testing.T) {
	primary := &stubProvider{name: "p1", err: ErrInvalidCoordinate}
	secondary := &stubProvider{name: "p2"}
	m := NewMulti(primary, secondary)
	_, err := m.Compute(context.Background(), ComputeRequest{
		Origin:      Point{Latitude: 0, Longitude: 0},
		Destination: Point{Latitude: 0, Longitude: 1},
		TravelMode:  TravelModeTwoWheeler,
	})
	if !errors.Is(err, ErrInvalidCoordinate) {
		t.Fatalf("err=%v want ErrInvalidCoordinate", err)
	}
	if secondary.hits != 0 {
		t.Fatal("secondary should not be called on validation error")
	}
}

func TestPolylineBBox_DecodesAndExpands(t *testing.T) {
	// Reference encoded polyline from Google's docs: three points.
	// "_p~iF~ps|U_ulLnnqC_mqNvxq`@" → (38.5,-120.2)(40.7,-120.95)(43.252,-126.453)
	enc := "_p~iF~ps|U_ulLnnqC_mqNvxq`@"
	b, ok := PolylineBBox(enc, 0.001)
	if !ok {
		t.Fatal("decode failed")
	}
	if b.MinLat > 38.5-0.001+1e-6 || b.MaxLat < 43.252+0.001-1e-6 {
		t.Fatalf("lat bounds wrong: %+v", b)
	}
	if b.MinLng > -126.453-0.001+1e-6 || b.MaxLng < -120.2+0.001-1e-6 {
		t.Fatalf("lng bounds wrong: %+v", b)
	}
}

func TestPolylineBBox_EmptyReturnsFalse(t *testing.T) {
	if _, ok := PolylineBBox("", 0.001); ok {
		t.Fatal("empty input should return ok=false")
	}
}
