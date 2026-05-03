package auth

import "testing"

// TestDummyHash_VerifyReturnsFalseNotError pins the contract that
// VerifyPassword(dummyHash, ...) is a *non-error mismatch* — i.e. the
// constant-time login path actually runs the argon2id work and returns
// (false, nil) rather than failing on a malformed hash.
//
// Without this guard the timing-equalization in Login becomes a no-op,
// because VerifyPassword would short-circuit on ErrInvalidHash before
// hashing anything.
func TestDummyHash_VerifyReturnsFalseNotError(t *testing.T) {
	ok, err := VerifyPassword(dummyHash, "anything")
	if err != nil {
		t.Fatalf("dummyHash should be a valid PHC string; got error: %v", err)
	}
	if ok {
		t.Errorf("dummyHash should NEVER match any input password; got ok=true")
	}
}
