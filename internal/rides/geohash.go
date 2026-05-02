package rides

import "strings"

// geohashAlphabet is the standard base32 alphabet used by geohash.
const geohashAlphabet = "0123456789bcdefghjkmnpqrstuvwxyz"

// Geohash7 returns the 7-character geohash of (lat, lng). Precision 7 ≈
// 153 m × 153 m bucket — appropriate for the route cache key per
// docs/ROUTE_PROVIDER.md §7.
func Geohash7(lat, lng float64) string { return geohashEncode(lat, lng, 7) }

// geohashEncode encodes a lat/lng pair to a geohash of the requested
// length. Longitude bits and latitude bits alternate.
func geohashEncode(lat, lng float64, length int) string {
	if lat < -90 {
		lat = -90
	}
	if lat > 90 {
		lat = 90
	}
	if lng < -180 {
		lng = -180
	}
	if lng > 180 {
		lng = 180
	}
	var b strings.Builder
	b.Grow(length)
	latLo, latHi := -90.0, 90.0
	lngLo, lngHi := -180.0, 180.0
	even := true
	bits := 0
	bit := 0
	for b.Len() < length {
		if even {
			mid := (lngLo + lngHi) / 2
			if lng >= mid {
				bits = (bits << 1) | 1
				lngLo = mid
			} else {
				bits = bits << 1
				lngHi = mid
			}
		} else {
			mid := (latLo + latHi) / 2
			if lat >= mid {
				bits = (bits << 1) | 1
				latLo = mid
			} else {
				bits = bits << 1
				latHi = mid
			}
		}
		even = !even
		bit++
		if bit == 5 {
			b.WriteByte(geohashAlphabet[bits])
			bits = 0
			bit = 0
		}
	}
	return b.String()
}
