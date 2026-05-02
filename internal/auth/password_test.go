package auth

import "testing"

func TestHashAndVerify_Roundtrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil {
		t.Fatalf("VerifyPassword (match): %v", err)
	}
	if !ok {
		t.Errorf("expected match, got false")
	}
	wrong, err := VerifyPassword(hash, "wrong password")
	if err != nil {
		t.Fatalf("VerifyPassword (mismatch): %v", err)
	}
	if wrong {
		t.Errorf("expected mismatch, got true")
	}
}

func TestHashPassword_Empty(t *testing.T) {
	_, err := HashPassword("")
	if err == nil {
		t.Errorf("expected error for empty password, got nil")
	}
}

func TestVerifyPassword_BadHash(t *testing.T) {
	cases := []string{"", "not-a-phc-string", "$argon2id$short", "$argon2id$v=19$m=65536,t=3,p=1$YQ$YQ$extra"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := VerifyPassword(c, "x")
			if err == nil {
				t.Errorf("expected ErrInvalidHash for %q", c)
			}
		})
	}
}

func TestHashPassword_DistinctSalts(t *testing.T) {
	a, _ := HashPassword("samepassword")
	b, _ := HashPassword("samepassword")
	if a == b {
		t.Errorf("two hashes of the same password should differ (random salt)")
	}
}
