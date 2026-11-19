package state

import (
	"database/sql"
	"testing"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/assert"
)

func TestPolicyUserTokensAreUnambiguous(t *testing.T) {
	users := []types.User{
		{Name: "alice", Email: "alice@x.com"},
		{Name: "alice2", Email: "dup@x.com"},
		{Name: "alice3", Email: "dup@x.com"},
		{Name: "oidc", Email: "dup@x.com", ProviderIdentifier: sql.NullString{String: "https://idp/123", Valid: true}},
		{Name: "bob"},
		{Name: "carol"},
		{Name: "carol", ProviderIdentifier: sql.NullString{String: "https://idp/c", Valid: true}},
		{Name: "dave@x.com"},
		{Name: "eve", Email: "dave@x.com"},
		{Name: "frank", Email: "no at sign"},
	}
	for i := range users {
		users[i].ID = uint(i + 1)
	}

	got := policyUserTokens(users)

	assert.Equal(t, map[types.UserID]string{
		1:  "alice@x.com",
		2:  "alice2@", // shared email, unique name: named by username
		3:  "alice3@",
		4:  "https://idp/123@", // provider identifier wins, even with a shared email
		5:  "bob@",
		7:  "https://idp/c@",
		9:  "dave@x.com", // the email is unique, so it matches eve before any name
		10: "frank@",     // an unusable email falls back to the unique name
	}, got, "6 (shared name) and 8 (name equals another's email) are left out")
}
