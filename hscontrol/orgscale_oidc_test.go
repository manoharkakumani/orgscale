package hscontrol

import (
	"testing"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/assert"
)

func TestApplyProviderEmail(t *testing.T) {
	const (
		tid    = "e644c415-7c35-4777-820d-379e907e7dd1"
		tenant = "https://login.microsoftonline.com/" + tid + "/v2.0"
	)

	member, guest := 0, 1

	for _, tc := range []struct {
		name     string
		in       types.OIDCClaims
		want     string
		verified bool
	}{
		{"member: UPN becomes the verified email", types.OIDCClaims{Iss: tenant, TenantID: tid, Username: "Manohar@Stayx.ai"}, "manohar@stayx.ai", true},
		{"member: the upn claim is preferred", types.OIDCClaims{Iss: tenant, TenantID: tid, UPN: "manohar@stayx.ai", Username: "other@stayx.ai"}, "manohar@stayx.ai", true},
		{"member with acct=0", types.OIDCClaims{Iss: tenant, TenantID: tid, Acct: &member, Username: "a@stayx.ai"}, "a@stayx.ai", true},
		{"an editable email claim is replaced by the UPN", types.OIDCClaims{Iss: tenant, TenantID: tid, Username: "manohar@stayx.ai", Email: "ceo@victim.com"}, "manohar@stayx.ai", true},
		{"guest by acct is refused", types.OIDCClaims{Iss: tenant, TenantID: tid, Acct: &guest, Username: "eve@othercorp.com"}, "", false},
		{"guest by idp (home email in preferred_username) is refused", types.OIDCClaims{Iss: tenant, TenantID: tid, IDP: "https://sts.windows.net/11111111-2222-3333-4444-555555555555/", Username: "eve@othercorp.com"}, "", false},
		{"personal account by idp is refused", types.OIDCClaims{Iss: tenant, TenantID: tid, IDP: "live.com", Username: "eve@outlook.com"}, "", false},
		{"#EXT# UPN is refused", types.OIDCClaims{Iss: tenant, TenantID: tid, Username: "eve_gmail.com#EXT#@stayx.onmicrosoft.com"}, "", false},
		{"missing tid is refused", types.OIDCClaims{Iss: tenant, Username: "a@stayx.ai"}, "", false},
		{"another tenant's tid is refused", types.OIDCClaims{Iss: tenant, TenantID: "11111111-2222-3333-4444-555555555555", Username: "a@stayx.ai"}, "", false},
		{"multi-tenant endpoint is refused", types.OIDCClaims{Iss: "https://login.microsoftonline.com/common/v2.0", TenantID: tid, Username: "a@b.com"}, "", false},
		{"personal Microsoft accounts tenant is refused", types.OIDCClaims{Iss: "https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0", TenantID: "9188040d-6c67-4c5b-b112-36a304b66dad", Username: "a@outlook.com"}, "", false},
		{"other providers are left alone", types.OIDCClaims{Iss: "https://accounts.google.com", Username: "a@b.com", Email: "a@b.com"}, "a@b.com", false},
		{"a verified email is kept", types.OIDCClaims{Iss: tenant, TenantID: tid, Username: "x@stayx.ai", Email: "manohar@stayx.ai", EmailVerified: true}, "manohar@stayx.ai", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.in
			applyProviderEmail(&c)
			assert.Equal(t, tc.want, c.Email)
			assert.Equal(t, tc.verified, bool(c.EmailVerified))
		})
	}
}
