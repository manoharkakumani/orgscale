package hscontrol

import (
	"encoding/json"
	"fmt"
)

// userError is an error whose message is meant for a person as written: a
// validation problem shown in the console, a sync problem shown in its
// status, or a SCIM error returned to the identity provider. Callers can
// tell it apart with errors.As.
type userError string

func (e userError) Error() string { return string(e) }

// userErrorf formats a [userError].
func userErrorf(format string, a ...any) error {
	return userError(fmt.Sprintf(format, a...))
}

// mustJSON encodes v, which must be plain data (strings, slices, structs of
// them) that encoding/json cannot fail on.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encoding %T: %v", v, err))
	}

	return string(b)
}
