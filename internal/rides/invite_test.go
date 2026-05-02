package rides

import "testing"

func TestNewInviteCode_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 200; i++ {
		c, err := NewInviteCode()
		if err != nil {
			t.Fatalf("NewInviteCode: %v", err)
		}
		if err := ValidateInviteCode(c); err != nil {
			t.Errorf("invalid code %q: %v", c, err)
		}
		if _, dup := seen[c]; dup {
			t.Errorf("duplicate invite code generated: %q", c)
		}
		seen[c] = struct{}{}
	}
}

func TestValidateInviteCode_Rejects(t *testing.T) {
	bad := []string{"", "ABCDE", "ABCDEFG", "ABCDE1", "ABCDE0", "ABCDEI", "ABCDEL", "ABCDEO", "abcdef"}
	for _, c := range bad {
		t.Run(c, func(t *testing.T) {
			if err := ValidateInviteCode(c); err == nil {
				t.Errorf("expected reject for %q", c)
			}
		})
	}
}
