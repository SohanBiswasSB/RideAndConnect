package rides

import "testing"

func TestGeohash7_Length(t *testing.T) {
	h := Geohash7(12.9716, 77.5946)
	if len(h) != 7 {
		t.Fatalf("expected length 7, got %d (%q)", len(h), h)
	}
}

func TestGeohash7_BucketEquality(t *testing.T) {
	// Two points within ~150 m should share the same hash; clearly
	// distant points should not.
	a := Geohash7(12.9716, 77.5946)
	near := Geohash7(12.9717, 77.5947) // ~15 m east
	far := Geohash7(13.5, 77.5946)     // ~58 km north
	if a != near {
		t.Errorf("close points should share geohash; got %q vs %q", a, near)
	}
	if a == far {
		t.Errorf("far points should differ; both %q", a)
	}
}

func TestGeohash7_AlphabetOnly(t *testing.T) {
	h := Geohash7(0, 0)
	for _, r := range h {
		ok := false
		for _, a := range geohashAlphabet {
			if r == a {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("char %q not in geohash alphabet", string(r))
		}
	}
}
