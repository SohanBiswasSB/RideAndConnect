package rides

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
)

// inviteAlphabet excludes ambiguous chars (0,O,1,I,L) so codes are
// unambiguous when read aloud or scanned by OCR.
const inviteAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

const inviteLength = 6

// inviteCodeRegex is the validation pattern; matches what NewInviteCode
// emits. The character class is the unambiguous alphabet from
// inviteAlphabet — letters A-Z minus I, L, O plus digits 2-9.
var inviteCodeRegex = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{6}$`)

// NewInviteCode returns a fresh invite code. Cryptographic randomness
// is used so codes can't be predicted from prior values. With 31 chars
// and length 6 the keyspace is ~887M; uniqueness is enforced by the
// partial unique index, and the join service retries on collision.
func NewInviteCode() (string, error) {
	b := make([]byte, inviteLength)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rides: invite code rand: %w", err)
	}
	for i, x := range b {
		b[i] = inviteAlphabet[int(x)%len(inviteAlphabet)]
	}
	return string(b), nil
}

// ValidateInviteCode rejects malformed user input. Returns nil on success.
func ValidateInviteCode(code string) error {
	if !inviteCodeRegex.MatchString(code) {
		return errors.New("rides: invalid invite code")
	}
	return nil
}
