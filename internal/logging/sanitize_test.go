package logging

import (
	"testing"
)

func TestSanitize_DeniesKnownKeys(t *testing.T) {
	in := map[string]any{
		"latitude":         12.34,
		"longitude":        56.78,
		"lat":              12.34,
		"lng":              56.78,
		"encoded_polyline": "abcdef",
		"password":         "secret",
		"refresh_token":    "abc",
		"access_token":     "xyz",
		"authorization":    "Bearer ...",
		"sdp":              "v=0\r\n...",
		"candidate":        "candidate:842...",
		"mapbox":           "tk.eyJ...",

		// Should pass through.
		"user_id": "u1",
		"ride_id": "r1",
		"name":    "Alice",
	}

	out := Sanitize(in)
	for _, k := range []string{
		"latitude", "longitude", "lat", "lng", "encoded_polyline",
		"password", "refresh_token", "access_token", "authorization",
		"sdp", "candidate", "mapbox",
	} {
		if got := out[k]; got != redactedPlaceholder {
			t.Errorf("key %q: want %q, got %v", k, redactedPlaceholder, got)
		}
	}
	for _, k := range []string{"user_id", "ride_id", "name"} {
		if out[k] != in[k] {
			t.Errorf("key %q passthrough failed: got %v", k, out[k])
		}
	}
}

func TestSanitize_PrefixMatch(t *testing.T) {
	in := map[string]any{
		"password_old": "x",
		"token_v2":     "y",
	}
	out := Sanitize(in)
	if out["password_old"] != redactedPlaceholder {
		t.Errorf("password_old not redacted")
	}
	if out["token_v2"] != redactedPlaceholder {
		t.Errorf("token_v2 not redacted")
	}
}

func TestSanitize_RecursesIntoNestedMaps(t *testing.T) {
	in := map[string]any{
		"meta": map[string]any{
			"latitude": 1.2,
			"safe":     "ok",
		},
	}
	out := Sanitize(in)
	nested, ok := out["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta should be a map, got %T", out["meta"])
	}
	if nested["latitude"] != redactedPlaceholder {
		t.Errorf("nested latitude not redacted")
	}
	if nested["safe"] != "ok" {
		t.Errorf("nested safe field altered")
	}
}

func TestMaskEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"alice@example.com", "a***@example.com"},
		{"a@example.com", "a***@example.com"},
		{"", ""},
		{"no-at-sign", ""},
		{"@no-local", ""},
	}
	for _, c := range cases {
		if got := MaskEmail(c.in); got != c.want {
			t.Errorf("MaskEmail(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	cases := []struct{ in, want string }{
		// 12 digits → mask 10, keep last 2.
		{"+919876543210", "+**********10"},
		// 10 digits → mask 8, keep last 2.
		{"9876543210", "********10"},
		// 12 digits with separators → mask 10, keep last 2; non-digits preserved.
		{"+91 (98) 765-43210", "+** (**) ***-***10"},
		{"", ""},
		{"12", "12"},
		{"1", "1"},
	}
	for _, c := range cases {
		if got := MaskPhone(c.in); got != c.want {
			t.Errorf("MaskPhone(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}
