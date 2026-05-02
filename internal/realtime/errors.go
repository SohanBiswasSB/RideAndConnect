package realtime

import "errors"

// wsClose is the internal "close with this code" error returned by
// dispatch handlers when the connection should terminate.
type wsClose struct {
	code   int
	reason string
}

func (e wsClose) Error() string { return e.reason }

func wsErr(code int, reason string) error { return wsClose{code: code, reason: reason} }

func asWSClose(err error, target *wsClose) bool {
	var c wsClose
	if errors.As(err, &c) {
		*target = c
		return true
	}
	return false
}
