package hscontrol

import (
	"testing"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/assert"
)

func TestApplyProviderEmail(t *testing.T) {
	const tenant = "https://login.microsoftonline.com/e644c415-7c35-4777-820d-379e907e7dd1/v2.0"

	for _, tc := range []struct {
		name     string
		in       types.OIDCClaims
		want     string
		verified bool
	}{
		{"entra member: UPN becomes the verified email", types.OIDCClaims{Iss: tenant, Username: "Manohar@Stayx.ai"}, "manohar@stayx.ai", true},
		{"entra: an editable email claim is replaced by the UPN", types.OIDCClaims{Iss: tenant, Username: "manohar@stayx.ai", Email: "ceo@victim.com"}, "manohar@stayx.ai", true},
		{"entra guest is not trusted", types.OIDCClaims{Iss: tenant, Username: "eve_gmail.com#EXT#@stayx.onmicrosoft.com"}, "", false},
		{"entra multi-tenant endpoint is not trusted", types.OIDCClaims{Iss: "https://login.microsoftonline.com/common/v2.0", Username: "a@b.com"}, "", false},
		{"personal Microsoft accounts are not trusted", types.OIDCClaims{Iss: "https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0", Username: "a@outlook.com"}, "", false},
		{"other providers are left alone", types.OIDCClaims{Iss: "https://accounts.google.com", Username: "a@b.com", Email: "a@b.com"}, "a@b.com", false},
		{"a verified email is kept", types.OIDCClaims{Iss: tenant, Username: "x@stayx.ai", Email: "manohar@stayx.ai", EmailVerified: true}, "manohar@stayx.ai", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.in
			applyProviderEmail(&c)
			assert.Equal(t, tc.want, c.Email)
			assert.Equal(t, tc.verified, bool(c.EmailVerified))
		})
	}
}
