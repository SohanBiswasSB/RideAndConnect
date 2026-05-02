package users

import "testing"

func TestClassifyIdentifier(t *testing.T) {
	cases := []struct {
		in   string
		want identKind
	}{
		{"alice@example.com", identEmail},
		{"7501223507", identPhone},
		{"+91 75012 23507", identPhone},
		{"WB 52 K 7661", identPlate},
		{"23 BH 3379L", identPlate},
		{"AB", identPlate},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := classifyIdentifier(c.in); got != c.want {
				t.Errorf("classifyIdentifier(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizePhoneCmp(t *testing.T) {
	cases := []struct{ in, want string }{
		{"+91 75012-23507", "+917501223507"},
		{"7501223507", "7501223507"},
		{"+91 (98) 765-43210", "+919876543210"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := normalizePhoneCmp(c.in); got != c.want {
				t.Errorf("normalizePhoneCmp(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
