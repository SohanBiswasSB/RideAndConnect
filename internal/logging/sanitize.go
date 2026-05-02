package logging

import "strings"

// denyList enumerates field names that must never appear in production
// logs. See docs/SECURITY.md §8.2. Keep this sorted by category so it's
// easy to audit.
var denyList = map[string]struct{}{
	// Coordinates & route geometry.
	"latitude":         {},
	"longitude":        {},
	"lat":              {},
	"lng":              {},
	"coords":           {},
	"coordinates":      {},
	"encoded_polyline": {},
	"polyline":         {},

	// Auth secrets.
	"password":      {},
	"password_hash": {},
	"token":         {},
	"refresh_token": {},
	"access_token":  {},
	"authorization": {},

	// WebRTC signaling payloads.
	"sdp":       {},
	"candidate": {},

	// Provider keys / tokens.
	"x_goog_api_key":      {},
	"mapbox_access_token": {},
	"mapbox":              {},
}

// redactedPlaceholder is what we substitute for any value whose key
// matches the deny list. It's intentionally non-empty so that the redaction
// is visible in logs (vs an absence that could be mistaken for a bug).
const redactedPlaceholder = "<redacted>"

// Sanitize returns a copy of `fields` with deny-listed values replaced by
// the redacted placeholder. Comparison is case-insensitive and matches
// either exact field names or `<denied>_…` prefixes (e.g. `password_old`).
//
// Use this on any caller-controlled map before passing it to the logger.
func Sanitize(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return fields
	}
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if isDenied(k) {
			out[k] = redactedPlaceholder
			continue
		}
		// Recurse into nested maps; leave other types as-is. This keeps the
		// helper allocation-light for the common single-level case.
		if nested, ok := v.(map[string]any); ok {
			out[k] = Sanitize(nested)
			continue
		}
		out[k] = v
	}
	return out
}

// MaskEmail returns an email with the local part masked: "alice@x.com" →
// "a***@x.com". Empty input returns empty.
func MaskEmail(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	local, domain := email[:at], email[at:]
	if len(local) == 1 {
		return local + "***" + domain
	}
	return string(local[0]) + "***" + domain
}

// MaskPhone returns a phone with all but the last 2 digits replaced by
// "*". Non-digit chars are preserved (so country-code parens still show).
func MaskPhone(phone string) string {
	if phone == "" {
		return ""
	}
	digits := 0
	for i := len(phone) - 1; i >= 0; i-- {
		if phone[i] >= '0' && phone[i] <= '9' {
			digits++
		}
	}
	if digits <= 2 {
		return phone
	}
	out := []byte(phone)
	keep := 2
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] < '0' || out[i] > '9' {
			continue
		}
		if keep > 0 {
			keep--
			continue
		}
		out[i] = '*'
	}
	return string(out)
}

func isDenied(name string) bool {
	lc := strings.ToLower(name)
	if _, ok := denyList[lc]; ok {
		return true
	}
	// Prefix match: `password_old`, `access_token_v2`, etc.
	for k := range denyList {
		if strings.HasPrefix(lc, k+"_") {
			return true
		}
	}
	return false
}
