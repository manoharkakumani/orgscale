package hscontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// adminHarness is an orgscale with the admin console on, behind a mock OIDC
// provider, driven by cookie-keeping browsers.
type adminHarness struct {
	app *Orgscale
	idp *mockoidc.MockOIDC
	srv *httptest.Server
}

func newAdminHarness(t *testing.T) *adminHarness {
	t.Helper()

	idp, err := mockoidc.Run()
	require.NoError(t, err)
	t.Cleanup(func() { _ = idp.Shutdown() })

	app := createTestApp(t)

	var router http.Handler

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	app.cfg.ServerURL = srv.URL
	app.cfg.Admin = types.AdminConfig{
		Enabled:        true,
		Users:          []string{"boss@example.com"},
		Groups:         []string{"it-admins"},
		SessionTimeout: time.Hour,
	}

	provider, err := NewAuthProviderOIDC(context.Background(), app, srv.URL, &types.OIDCConfig{
		Issuer:   types.HTTPURL(idp.Issuer()),
		ClientID: idp.ClientID,
		Scope:    []string{"openid", "profile", "email", "groups"},
		PKCE:     types.PKCEConfig{Enabled: true, Method: types.PKCEMethodS256},
	}, idp.ClientSecret)
	require.NoError(t, err)
	t.Cleanup(provider.authCache.Close)

	app.authProvider = provider
	app.admin = newAdminAuth(app)
	router = app.HTTPHandler()

	return &adminHarness{app: app, idp: idp, srv: srv}
}

type adminBrowser struct {
	h    *adminHarness
	c    *http.Client
	csrf string
}

func (h *adminHarness) browser(t *testing.T) *adminBrowser {
	t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	return &adminBrowser{h: h, c: &http.Client{Jar: jar}}
}

// login signs in as user and returns the final status.
func (b *adminBrowser) login(t *testing.T, user *mockoidc.MockUser) int {
	t.Helper()

	b.h.idp.QueueUser(user)

	resp, err := b.c.Get(b.h.srv.URL + "/admin/auth/login") //nolint:noctx
	require.NoError(t, err)

	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK {
		var me map[string]any

		code := b.do(t, http.MethodGet, "/admin/api/me", nil, &me)
		require.Equal(t, http.StatusOK, code)

		b.csrf, _ = me["csrfToken"].(string)
	}

	return resp.StatusCode
}

func (b *adminBrowser) do(t *testing.T, method, path string, body any, out any) int {
	t.Helper()

	var r io.Reader

	if body != nil {
		buf, err := json.Marshal(body)
		require.NoError(t, err)

		r = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, b.h.srv.URL+path, r)
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")

	if b.csrf != "" {
		req.Header.Set(adminCSRFHeader, b.csrf)
	}

	resp, err := b.c.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	if out != nil && len(data) > 0 && resp.StatusCode < 300 {
		require.NoError(t, json.Unmarshal(data, out), string(data))
	}

	return resp.StatusCode
}

func TestAdminConsoleLoginAndRoles(t *testing.T) {
	h := newAdminHarness(t)

	// Not signed in.
	anon := h.browser(t)
	assert.Equal(t, http.StatusUnauthorized, anon.do(t, http.MethodGet, "/admin/api/me", nil, nil))
	assert.Equal(t, http.StatusUnauthorized, anon.do(t, http.MethodGet, "/admin/api/users", nil, nil))

	// Someone who is neither admin nor viewer is refused.
	stranger := h.browser(t)
	assert.Equal(t, http.StatusForbidden, stranger.login(t, &mockoidc.MockUser{
		Subject: "stranger", Email: "stranger@example.com", EmailVerified: true,
	}))

	// An admin by email from the configuration file.
	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{
		Subject: "boss", Email: "boss@example.com", EmailVerified: true,
	}))
	require.NotEmpty(t, boss.csrf)

	var me map[string]any
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/me", nil, &me))
	assert.Equal(t, "admin", me["role"])

	// The CSRF header is required.
	csrf := boss.csrf
	boss.csrf = ""
	assert.Equal(t, http.StatusForbidden, boss.do(t, http.MethodGet, "/admin/api/users", nil, nil))
	assert.Equal(t, http.StatusForbidden, boss.do(t, http.MethodGet, "/api/v1/user", nil, nil),
		"a session without the CSRF header is refused")
	boss.csrf = csrf

	// The session drives the v1 API.
	assert.Equal(t, http.StatusOK, boss.do(t, http.MethodPost, "/api/v1/user", map[string]string{"name": "created-by-console"}, nil))

	var users []adminUser
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/users", nil, &users))
	assert.True(t, func() bool {
		for _, u := range users {
			if u.Name == "created-by-console" {
				return true
			}
		}

		return false
	}())

	// Add a viewer from the console.
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/roles", map[string]any{
		"adminUsers": []string{}, "adminGroups": []string{},
		"viewerUsers": []string{"Viewer@Example.com"}, "viewerGroups": []string{},
	}, nil))

	viewer := h.browser(t)
	require.Equal(t, http.StatusOK, viewer.login(t, &mockoidc.MockUser{
		Subject: "viewer", Email: "viewer@example.com", EmailVerified: true,
	}))
	assert.Equal(t, http.StatusOK, viewer.do(t, http.MethodGet, "/admin/api/users", nil, nil))
	assert.Equal(t, http.StatusOK, viewer.do(t, http.MethodGet, "/api/v1/user", nil, nil))
	assert.Equal(t, http.StatusForbidden, viewer.do(t, http.MethodPost, "/api/v1/user", map[string]string{"name": "nope"}, nil))
	assert.Equal(t, http.StatusForbidden, viewer.do(t, http.MethodPut, "/admin/api/settings/device-restriction", map[string]bool{"enabled": true}, nil))

	// Viewers cannot change tags, rules, groups or devices either.
	for _, req := range []struct{ method, path string }{
		{http.MethodPut, "/admin/api/tags/tag:x"},
		{http.MethodPost, "/admin/api/tags/tag:x/enroll"},
		{http.MethodPost, "/admin/api/rules"},
		{http.MethodDelete, "/admin/api/rules/1"},
		{http.MethodPost, "/admin/api/groups"},
		{http.MethodPost, "/admin/api/devices/bulk"},
	} {
		assert.Equal(t, http.StatusForbidden, viewer.do(t, req.method, req.path, map[string]any{}, nil), req.method+" "+req.path)
	}

	assert.Equal(t, http.StatusOK, viewer.do(t, http.MethodGet, "/admin/api/rules", nil, nil), "viewers can read rules")

	// An admin through an IdP group.
	groupAdmin := h.browser(t)
	require.Equal(t, http.StatusOK, groupAdmin.login(t, &mockoidc.MockUser{
		Subject: "ga", Email: "ga@example.com", EmailVerified: true, Groups: []string{"it-admins"},
	}))
	require.Equal(t, http.StatusOK, groupAdmin.do(t, http.MethodGet, "/admin/api/me", nil, &me))
	assert.Equal(t, "admin", me["role"])

	// Changes are audited, with who made them.
	var evs []types.AuditEvent
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/audit", nil, &evs))

	actions := map[string]string{}
	for _, e := range evs {
		actions[e.Action] = e.Actor
	}

	assert.Equal(t, "boss@example.com", actions["api.post"])
	assert.Equal(t, "boss@example.com", actions["settings.roles.update"])
	assert.Equal(t, "stranger@example.com", actions["admin.login.denied"])
	assert.Contains(t, actions, "admin.login")

	// Logout ends the session.
	assert.Equal(t, http.StatusNoContent, boss.do(t, http.MethodPost, "/admin/auth/logout", nil, nil))
	assert.Equal(t, http.StatusUnauthorized, boss.do(t, http.MethodGet, "/admin/api/users", nil, nil))
}

func TestAdminConsoleLoginRulesOverrideConfig(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/login-rules", map[string]any{
		"allowedDomains": []string{" Corp.Example "},
		"allowedUsers":   nil,
		"allowedGroups":  nil,
	}, nil))

	eff := effectiveOIDCConfig(h.app.state, &h.app.cfg.OIDC)
	assert.Equal(t, []string{"corp.example"}, eff.AllowedDomains)
	assert.Empty(t, eff.AllowedUsers)

	// The rule is enforced at device login.
	err := doOIDCAuthorization(eff, &types.OIDCClaims{Email: "x@other.example", EmailVerified: true})
	require.Error(t, err)
	require.NoError(t, doOIDCAuthorization(eff, &types.OIDCClaims{Email: "x@corp.example", EmailVerified: true}))

	// null falls back to the configuration file.
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/login-rules", map[string]any{
		"allowedDomains": nil,
	}, nil))
	assert.Empty(t, effectiveOIDCConfig(h.app.state, &h.app.cfg.OIDC).AllowedDomains)
}

func TestAdminConsoleOffboardsUser(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	leaver := h.app.state.CreateUserForTest("leaver")
	uid := strconv.FormatUint(uint64(leaver.ID), 10)

	assert.Equal(t, http.StatusNoContent, boss.do(t, http.MethodPost, "/admin/api/users/"+uid+"/disable", nil, nil))

	disabled, err := h.app.state.IsUserDisabled(types.UserID(leaver.ID))
	require.NoError(t, err)
	assert.True(t, disabled)

	assert.Equal(t, http.StatusNoContent, boss.do(t, http.MethodDelete, "/admin/api/users/"+uid, nil, nil))

	_, err = h.app.state.GetUserByID(types.UserID(leaver.ID))
	require.Error(t, err)
}

func scimToken(t *testing.T, app *Orgscale) string {
	t.Helper()

	token := "oskey-scim-test"
	sum := sha256.Sum256([]byte(token))
	require.NoError(t, app.state.SetSettingJSON(types.SettingSCIMTokenHash, hex.EncodeToString(sum[:]), "test"))

	return token
}

func scimDo(t *testing.T, srvURL, token, method, path, body string) (int, map[string]any) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, srvURL+"/scim/v2"+path, strings.NewReader(body))
	require.NoError(t, err)

	req.Header.Set("Content-Type", scimContentType)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	var out map[string]any

	data, _ := io.ReadAll(resp.Body)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}

	return resp.StatusCode, out
}

func TestSCIMProvisioning(t *testing.T) {
	h := newAdminHarness(t)
	url := h.srv.URL

	code, _ := scimDo(t, url, "", http.MethodGet, "/Users", "")
	assert.Equal(t, http.StatusUnauthorized, code, "no token configured")

	token := scimToken(t, h.app)

	code, _ = scimDo(t, url, "wrong", http.MethodGet, "/Users", "")
	assert.Equal(t, http.StatusUnauthorized, code)

	// Okta-style create.
	code, alice := scimDo(t, url, token, http.MethodPost, "/Users", `{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName": "alice+vpn@example.com",
		"name": {"givenName": "Alice", "familyName": "Liddell"},
		"emails": [{"primary": true, "value": "Alice+VPN@example.com", "type": "work"}],
		"active": true
	}`)
	require.Equal(t, http.StatusCreated, code)

	aliceID := alice["id"].(string)
	assert.Equal(t, "alice+vpn@example.com", alice["userName"])

	// Lookup by filter, as clients do before creating.
	code, list := scimDo(t, url, token, http.MethodGet, `/Users?filter=userName+eq+"alice%2Bvpn@example.com"`, "")
	require.Equal(t, http.StatusOK, code)
	assert.InDelta(t, 1, list["totalResults"], 0)

	code, bob := scimDo(t, url, token, http.MethodPost, "/Users", `{"userName": "bob@example.com", "displayName": "Bob"}`)
	require.Equal(t, http.StatusCreated, code)

	bobID := bob["id"].(string)

	// A group with both, then Entra-style membership patches.
	code, group := scimDo(t, url, token, http.MethodPost, "/Groups",
		`{"displayName": "VPN Users", "externalId": "g-1", "members": [{"value": "`+aliceID+`"}]}`)
	require.Equal(t, http.StatusCreated, code)

	groupID := group["id"].(string)

	code, _ = scimDo(t, url, token, http.MethodPatch, "/Groups/"+groupID, `{
		"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations": [{"op": "Add", "path": "members", "value": [{"value": "`+bobID+`"}]}]
	}`)
	require.Equal(t, http.StatusOK, code)

	groups, err := h.app.state.ListDirectoryGroups()
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "vpn-users", groups[0].Name)
	assert.Len(t, groups[0].MemberIDs, 2)

	// Okta-style member removal by filter path.
	code, _ = scimDo(t, url, token, http.MethodPatch, "/Groups/"+groupID, `{
		"Operations": [{"op": "remove", "path": "members[value eq \"`+bobID+`\"]"}]
	}`)
	require.Equal(t, http.StatusOK, code)

	groups, err = h.app.state.ListDirectoryGroups()
	require.NoError(t, err)
	assert.Len(t, groups[0].MemberIDs, 1)

	// Entra deactivates with the string "False": the user is offboarded.
	code, res := scimDo(t, url, token, http.MethodPatch, "/Users/"+aliceID, `{
		"Operations": [{"op": "Replace", "path": "active", "value": "False"}]
	}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, res["active"])

	code, _ = scimDo(t, url, token, http.MethodGet, "/Users/"+aliceID, "")
	assert.Equal(t, http.StatusNotFound, code, "alice was removed from orgscale")

	// DELETE offboards too.
	code, _ = scimDo(t, url, token, http.MethodDelete, "/Users/"+bobID, "")
	assert.Equal(t, http.StatusNoContent, code)

	users, err := h.app.state.ListAllUsers()
	require.NoError(t, err)

	for _, u := range users {
		assert.NotContains(t, []string{"alice+vpn@example.com", "bob@example.com"}, u.Email)
	}

	// A first SSO login takes over a SCIM-provisioned user.
	code, carol := scimDo(t, url, token, http.MethodPost, "/Users", `{"userName": "carol@example.com"}`)
	require.Equal(t, http.StatusCreated, code)

	require.Nil(t, provisionedUserForClaim(h.app.state, &types.OIDCClaims{Email: "carol@example.com"}, false),
		"an unverified email never takes over a provisioned user")

	claims := &types.OIDCClaims{Email: "carol@example.com", EmailVerified: true}
	found := provisionedUserForClaim(h.app.state, claims, true)
	require.NotNil(t, found)

	// Malformed emails are refused.
	code, _ = scimDo(t, url, token, http.MethodPost, "/Users", `{"userName": "not-an-email"}`)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, carol["id"], strconv.FormatUint(uint64(found.ID), 10))
}

func TestGoogleSyncApply(t *testing.T) {
	h := newAdminHarness(t)
	app := h.app

	// Someone who logged in earlier and has since been deleted in Workspace.
	gone, _, err := app.state.CreateUser(types.User{Name: "gone", Email: "gone@corp.example", Provider: "oidc"})
	require.NoError(t, err)

	// Signed in with an alias of an active account: kept.
	aliased, _, err := app.state.CreateUser(types.User{Name: "al", Email: "al@corp.example", Provider: "oidc"})
	require.NoError(t, err)

	// Provisioned by SCIM, not in Workspace: not this sync's to remove.
	scimUser, _, err := app.state.CreateUser(types.User{Name: "scimmed", Email: "s@corp.example", Provider: "scim"})
	require.NoError(t, err)

	// A user outside the Workspace domains is never touched.
	outsider, _, err := app.state.CreateUser(types.User{Name: "contractor", Email: "c@elsewhere.example"})
	require.NoError(t, err)

	g := newGoogleSyncer(app)

	users := []googleUser{
		{ID: "1", PrimaryEmail: "alice@corp.example", Aliases: []string{"AL@corp.example"}},
		{ID: "2", PrimaryEmail: "bob@corp.example"},
		{ID: "3", PrimaryEmail: "sus@corp.example", Suspended: true},
	}
	users[0].Name.FullName = "Alice"

	groups := []googleGroup{{ID: "g1", Email: "eng@corp.example", Name: "Engineering"}}
	members := map[string][]googleMember{"g1": {
		{Email: "alice@corp.example", Type: "USER", Status: "ACTIVE"},
		{Email: "sus@corp.example", Type: "USER", Status: "SUSPENDED"},
		{Email: "nested@corp.example", Type: "GROUP"},
	}}

	var status types.GoogleSyncStatus
	require.NoError(t, g.apply(users, groups, members, &status))

	assert.Equal(t, 2, status.Users)
	assert.Equal(t, 2, status.Created)
	assert.Equal(t, 1, status.Disabled)

	_, err = app.state.GetUserByID(types.UserID(gone.ID))
	require.Error(t, err, "deleted in Workspace: offboarded")

	_, err = app.state.GetUserByID(types.UserID(outsider.ID))
	require.NoError(t, err, "other domains are left alone")

	_, err = app.state.GetUserByID(types.UserID(aliased.ID))
	require.NoError(t, err, "an alias of an active account is not a leaver")

	_, err = app.state.GetUserByID(types.UserID(scimUser.ID))
	require.NoError(t, err, "SCIM users are not removed for a missing address")

	dg, err := app.state.ListDirectoryGroups()
	require.NoError(t, err)
	require.Len(t, dg, 1)
	assert.Equal(t, "engineering", dg[0].Name)
	assert.Len(t, dg[0].MemberIDs, 1)

	// A run that would remove most users stops.
	for _, n := range []string{"a", "b", "c"} {
		_, _, err := app.state.CreateUser(types.User{Name: "x" + n, Email: n + "@corp.example", Provider: "google"})
		require.NoError(t, err)
	}

	status = types.GoogleSyncStatus{}
	err = g.apply([]googleUser{{ID: "1", PrimaryEmail: "alice@corp.example"}}, nil, nil, &status)
	require.ErrorIs(t, err, errGoogleMassRemoval)

	// Removing the group in Workspace removes it here.
	status = types.GoogleSyncStatus{}
	require.NoError(t, g.apply(users, nil, nil, &status))

	dg, err = app.state.ListDirectoryGroups()
	require.NoError(t, err)
	assert.Empty(t, dg)
}

func TestAdminConsoleServesIndexForClientRoutes(t *testing.T) {
	h := newAdminHarness(t)

	for _, p := range []string{"/admin/", "/admin/devices", "/admin/users/12"} {
		resp, err := http.Get(h.srv.URL + p) //nolint:noctx
		require.NoError(t, err)

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode, p)
		assert.Contains(t, string(body), "<html", p)
		assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "script-src 'self'")
	}

	resp, err := http.Get(h.srv.URL + "/admin/assets/missing.js") //nolint:noctx
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAdminConsoleRoleRemovalEndsSession(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/roles", map[string]any{
		"adminUsers": []string{"second@example.com"}, "adminGroups": []string{},
		"viewerUsers": []string{}, "viewerGroups": []string{},
	}, nil))

	second := h.browser(t)
	require.Equal(t, http.StatusOK, second.login(t, &mockoidc.MockUser{Subject: "second", Email: "second@example.com", EmailVerified: true}))
	require.Equal(t, http.StatusOK, second.do(t, http.MethodGet, "/admin/api/users", nil, nil))

	// Downgrade to viewer: writes stop at once.
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/roles", map[string]any{
		"adminUsers": []string{}, "adminGroups": []string{},
		"viewerUsers": []string{"second@example.com"}, "viewerGroups": []string{},
	}, nil))

	var me map[string]any
	require.Equal(t, http.StatusOK, second.do(t, http.MethodGet, "/admin/api/me", nil, &me))
	assert.Equal(t, "viewer", me["role"])
	assert.Equal(t, http.StatusForbidden, second.do(t, http.MethodPost, "/api/v1/user", map[string]string{"name": "nope"}, nil))

	// Remove entirely: the session ends.
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/roles", map[string]any{
		"adminUsers": []string{}, "adminGroups": []string{},
		"viewerUsers": []string{}, "viewerGroups": []string{},
	}, nil))
	assert.Equal(t, http.StatusUnauthorized, second.do(t, http.MethodGet, "/admin/api/users", nil, nil))
	assert.Equal(t, http.StatusUnauthorized, second.do(t, http.MethodGet, "/admin/api/me", nil, nil))

	// The config-file admin is unaffected.
	assert.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/users", nil, nil))
}

func TestAdminRoleGroupMatching(t *testing.T) {
	h := newAdminHarness(t)
	aa := h.app.admin

	// The configuration makes members of "it-admins" admins.
	role, ok := aa.roleForIdentity("someone@example.com", nil)
	assert.False(t, ok, "no matching email or group: no access (got %q)", role)

	role, ok = aa.roleForIdentity("someone@example.com", []string{"marketing"})
	assert.False(t, ok, "a non-admin group grants nothing (got %q)", role)

	role, ok = aa.roleForIdentity("", []string{"IT-Admins"})
	assert.True(t, ok)
	assert.Equal(t, AdminRoleAdmin, role)

	role, ok = aa.roleForIdentity("BOSS@example.com", nil)
	assert.True(t, ok)
	assert.Equal(t, AdminRoleAdmin, role)
}

func TestAdminConsoleTagsRulesAndBulk(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{
		Subject: "boss", Email: "boss@example.com", EmailVerified: true,
	}))

	st := h.app.state
	alice, _, err := st.CreateUser(types.User{Name: "alice", Email: "alice@example.com"})
	require.NoError(t, err)

	aliceID := strconv.FormatUint(uint64(alice.ID), 10)

	// Create a tag with access, from the console.
	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPut, "/admin/api/tags/Bad Name!", map[string]any{"access": []any{}}, nil))
	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodPut, "/admin/api/tags/web", map[string]any{
		"description": "Web servers",
		"access":      []map[string]string{{"kind": "user", "principal": aliceID}},
	}, nil))

	var tags []adminTag
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/tags", nil, &tags))
	require.Len(t, tags, 1)
	assert.Equal(t, "tag:web", tags[0].Name)
	assert.True(t, tags[0].Managed)
	assert.Equal(t, "alice@example.com", tags[0].Access[0].Label)

	// An enrollment key for servers.
	var enroll map[string]any
	require.Equal(t, http.StatusCreated, boss.do(t, http.MethodPost, "/admin/api/tags/tag%3Aweb/enroll", map[string]any{
		"reusable": true, "expiresInHours": 24,
	}, &enroll))
	assert.NotEmpty(t, enroll["key"])
	assert.Equal(t, http.StatusNotFound, boss.do(t, http.MethodPost, "/admin/api/tags/tag:nope/enroll", map[string]any{"expiresInHours": 1}, nil))

	// A rule from the rule editor.
	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPost, "/admin/api/rules", map[string]any{
		"name": "bad ports", "ports": "22-x", "enabled": true,
		"sources":      []map[string]string{{"kind": "everyone"}},
		"destinations": []map[string]string{{"kind": "tag", "principal": "tag:web"}},
	}, nil))

	var created map[string]uint64
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPost, "/admin/api/rules", map[string]any{
		"name": "Everyone to the web", "protocol": "tcp", "ports": "80, 443", "enabled": true,
		"sources":      []map[string]string{{"kind": "everyone"}},
		"destinations": []map[string]string{{"kind": "tag", "principal": "tag:web"}},
	}, &created))
	require.NotZero(t, created["id"])

	var rules []adminRule
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/rules", nil, &rules))
	require.Len(t, rules, 1)
	assert.Equal(t, "80,443", rules[0].Ports)
	assert.Equal(t, "Everyone", rules[0].Sources[0].Label)

	// Turn restriction on so the rules are compiled, and check them.
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/device-restriction", map[string]bool{"enabled": true}, nil))

	pol, err := st.EffectivePolicy()
	require.NoError(t, err)
	assert.Contains(t, string(pol), `"tag:web:80,443"`)
	assert.Contains(t, string(pol), `"tag:web":[]`)

	// A tag in use cannot be deleted; an unused one can.
	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodDelete, "/admin/api/rules/"+strconv.FormatUint(created["id"], 10), nil, nil))
	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodDelete, "/admin/api/tags/tag:web", nil, nil))
	assert.Equal(t, http.StatusNotFound, boss.do(t, http.MethodDelete, "/admin/api/tags/tag:web", nil, nil))

	var audit []types.AuditEvent
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/audit", nil, &audit))

	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
	}

	assert.Subset(t, actions, []string{"tag.save", "tag.enroll_key", "rule.create", "rule.delete", "tag.delete"})
}

func TestAdminConsoleGroups(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{
		Subject: "boss", Email: "boss@example.com", EmailVerified: true,
	}))

	alice, _, err := h.app.state.CreateUser(types.User{Name: "alice", Email: "alice@example.com"})
	require.NoError(t, err)

	var created map[string]any
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPost, "/admin/api/groups", map[string]any{
		"name": "Web Team", "memberIds": []uint{alice.ID},
	}, &created))
	assert.Equal(t, "web-team", created["name"])

	id := strconv.FormatUint(uint64(created["id"].(float64)), 10)

	var groups []adminGroup
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/groups", nil, &groups))
	require.Len(t, groups, 1)
	assert.Equal(t, "console", groups[0].Source)
	assert.Equal(t, []string{"alice@example.com"}, groups[0].Members)

	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPut, "/admin/api/groups/"+id, map[string]any{"name": "!!!", "memberIds": []uint{}}, nil))
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/groups/"+id, map[string]any{"name": "Web Team", "memberIds": []uint{}}, nil))

	// Groups from the identity provider cannot be changed here.
	var scimGroupID uint64

	_, err = h.app.state.WriteDirectory(func(tx *gorm.DB) error {
		g, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, "ext", "Engineering")
		if err == nil {
			scimGroupID = g.ID
		}

		return err
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, boss.do(t, http.MethodDelete, "/admin/api/groups/"+strconv.FormatUint(scimGroupID, 10), nil, nil))

	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodDelete, "/admin/api/groups/"+id, nil, nil))
}

func TestAdminConsoleDoubleSlashAndNonAdmins(t *testing.T) {
	h := newAdminHarness(t)

	// The Tailscale client's "Admin console" link, built from a login
	// server URL with a trailing slash.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	res, err := c.Get(h.srv.URL + "//admin")
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusMovedPermanently, res.StatusCode)
	assert.Equal(t, "/admin", res.Header.Get("Location"))

	res, err = c.Get(h.srv.URL + "//evil.example.com/x")
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, "/evil.example.com/x", res.Header.Get("Location"), "never redirects off the server")

	// Someone who signs in but is not an admin or viewer gets a clear
	// refusal and no session.
	b := h.browser(t)
	assert.Equal(t, http.StatusForbidden, b.login(t, &mockoidc.MockUser{
		Subject: "carol", Email: "carol@example.com", EmailVerified: true,
	}))
	assert.Equal(t, http.StatusUnauthorized, b.do(t, http.MethodGet, "/admin/api/me", nil, nil))
}

// TestAdminConsoleRuleEditAndSSH sends rules exactly as the console's rule
// editor does, including editing an existing rule.
func TestAdminConsoleRuleEditAndSSH(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodPut, "/admin/api/tags/tag:prod", map[string]any{"access": []any{}}, nil))

	body := func(kind string, users []string, period string) map[string]any {
		return map[string]any{
			"kind": kind, "name": "rule", "enabled": true, "protocol": "", "ports": "*",
			"sshUsers": users, "sshCheckPeriod": period,
			"sources":      []map[string]string{{"kind": "everyone", "principal": ""}},
			"destinations": []map[string]string{{"kind": "tag", "principal": "tag:prod"}},
		}
	}

	var created map[string]uint64
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPost, "/admin/api/rules", body("network", []string{}, ""), &created))

	// Editing sends the same shape back.
	id := strconv.FormatUint(created["id"], 10)
	b := body("network", []string{}, "")
	b["enabled"] = false
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/rules/"+id, b, nil))

	// SSH rules: accounts and the interval are checked; servers only by tag
	// or "their own devices".
	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPost, "/admin/api/rules", body("ssh", []string{}, ""), nil))
	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPost, "/admin/api/rules", body("ssh", []string{"Bad User"}, ""), nil))
	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPost, "/admin/api/rules", body("ssh", []string{"ubuntu"}, "999h"), nil))

	bad := body("ssh", []string{"ubuntu"}, "")
	bad["destinations"] = []map[string]string{{"kind": "everyone", "principal": ""}}
	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPost, "/admin/api/rules", bad, nil))

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPost, "/admin/api/rules", body("ssh", []string{"ubuntu", "autogroup:nonroot"}, "12h"), nil))

	var rules []adminRule
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/rules", nil, &rules))
	require.Len(t, rules, 2)
	assert.Equal(t, "network", rules[0].Kind)
	assert.False(t, rules[0].Enabled)
	assert.Equal(t, "ssh", rules[1].Kind)
	assert.Equal(t, []string{"ubuntu", "autogroup:nonroot"}, rules[1].SSHUsers)
	assert.Equal(t, "12h", rules[1].SSHCheckPeriod)

	pol, err := h.app.state.EffectivePolicy()
	require.NoError(t, err)
	assert.Contains(t, string(pol), `"ssh":[`)
}

func TestAdminConsoleUserRoles(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	carol, _, err := h.app.state.CreateUser(types.User{Name: "carol", Email: "Carol@Example.com"})
	require.NoError(t, err)

	cid := strconv.FormatUint(uint64(carol.ID), 10)

	role := func(email string) (string, string) {
		var users []adminUser
		require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/users", nil, &users))

		for _, u := range users {
			if strings.EqualFold(u.Email, email) {
				return u.Role, u.RoleFrom
			}
		}

		return "?", "?"
	}

	r, from := role("carol@example.com")
	assert.Empty(t, r)
	assert.Empty(t, from)

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/users/"+cid+"/role", map[string]string{"role": "admin"}, nil))
	r, from = role("carol@example.com")
	assert.Equal(t, "admin", r)
	assert.Equal(t, "console", from)

	// Carol can now sign in to the console as admin.
	c := h.browser(t)
	require.Equal(t, http.StatusOK, c.login(t, &mockoidc.MockUser{Subject: "carol", Email: "carol@example.com", EmailVerified: true}))

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/users/"+cid+"/role", map[string]string{"role": "viewer"}, nil))
	r, _ = role("carol@example.com")
	assert.Equal(t, "viewer", r)
	assert.Equal(t, http.StatusForbidden, c.do(t, http.MethodPost, "/admin/api/groups", map[string]any{"name": "x"}, nil), "viewers cannot change things")

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/users/"+cid+"/role", map[string]string{"role": "none"}, nil))
	assert.Equal(t, http.StatusUnauthorized, c.do(t, http.MethodGet, "/admin/api/users", nil, nil), "taking access away ends the session")

	// The configuration file's admins are changed there; nobody demotes themselves.
	var users []adminUser
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/users", nil, &users))

	for _, u := range users {
		if u.Email == "boss@example.com" {
			assert.Equal(t, "config", u.RoleFrom)
			assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPut, "/admin/api/users/"+strconv.FormatUint(uint64(u.ID), 10)+"/role", map[string]string{"role": "none"}, nil))
		}
	}
}

// TestAdminConsoleTempAccess covers temporary access: only administrators
// sign in; an administrator grants a group or a person access to a tag for a
// set time, the grant appears in the policy, and leaves it when ended or when
// the time is up.
func TestAdminConsoleTempAccess(t *testing.T) {
	h := newAdminHarness(t)

	alice, _, err := h.app.state.CreateUser(types.User{Name: "alice", Email: "alice@example.com", Provider: userProviderEntra})
	require.NoError(t, err)

	aliceID := fmt.Sprint(alice.ID)

	// People who are not administrators cannot sign in, account or not.
	assert.Equal(t, http.StatusForbidden, h.browser(t).login(t, &mockoidc.MockUser{Subject: "alice", Email: "alice@example.com", EmailVerified: true}))

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/device-restriction", map[string]bool{"enabled": true}, nil))

	// A tag made before SSH accounts existed lists none (not null).
	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodPut, "/admin/api/tags/tag:web", map[string]any{"access": []any{}}, nil))
	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodPut, "/admin/api/tags/tag:prod-db", map[string]any{
		"description": "production databases", "access": []any{}, "tempSSHUsers": []string{"ubuntu"},
	}, nil))

	var tags []adminTag
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/tags", nil, &tags))

	for _, tg := range tags {
		require.NotNil(t, tg.TempSSHUsers, tg.Name)
	}

	policy := func() string {
		p, err := h.app.state.EffectivePolicy()
		require.NoError(t, err)

		return string(p)
	}

	var g map[string]any
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPost, "/admin/api/groups", map[string]any{"name": "On call", "memberIds": []uint64{uint64(alice.ID)}}, &g))
	gid := fmt.Sprint(g["id"])

	grant := func(to []map[string]string, tag string, minutes int, ssh bool) (int, []uint64) {
		var out map[string][]uint64
		code := boss.do(t, http.MethodPost, "/admin/api/temp-access", map[string]any{"to": to, "tag": tag, "minutes": minutes, "ssh": ssh, "reason": "incident 42"}, &out)

		return code, out["ids"]
	}
	group := []map[string]string{{"kind": "directory_group", "principal": gid}}
	person := []map[string]string{{"kind": "user", "principal": aliceID}}

	code, _ := grant(group, "tag:web", 60, true)
	assert.Equal(t, http.StatusBadRequest, code, "tag:web has no SSH accounts")
	code, _ = grant(group, "tag:prod-db", 99999, false)
	assert.Equal(t, http.StatusBadRequest, code, "longer than a week")
	code, _ = grant(nil, "tag:prod-db", 60, false)
	assert.Equal(t, http.StatusBadRequest, code, "nobody chosen")

	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPost, "/admin/api/temp-access", map[string]any{"to": person, "server": 999, "minutes": 60}, nil), "no such server")

	assert.NotContains(t, policy(), "tag:prod-db:*")

	code, ids := grant(group, "tag:prod-db", 60, true)
	require.Equal(t, http.StatusCreated, code)
	assert.Contains(t, policy(), "tag:prod-db:*", "the group's members reach the tag")
	assert.Contains(t, policy(), `"users":["ubuntu"]`)

	var list []adminTempAccess
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/temp-access", nil, &list))
	require.Len(t, list, 1)
	assert.True(t, list[0].Active)
	assert.Equal(t, []string{"ubuntu"}, list[0].SSHUsers)
	assert.Equal(t, "directory_group", list[0].Kind)

	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodPost, fmt.Sprintf("/admin/api/temp-access/%d/end", ids[0]), nil, nil))
	assert.NotContains(t, policy(), "tag:prod-db:*")
	assert.Equal(t, http.StatusConflict, boss.do(t, http.MethodPost, fmt.Sprintf("/admin/api/temp-access/%d/end", ids[0]), nil, nil))

	code, _ = grant(person, "tag:prod-db", 30, false)
	require.Equal(t, http.StatusCreated, code)
	assert.Contains(t, policy(), "tag:prod-db:*")

	lapsed, _, err := h.app.state.ExpireAccessRequests(time.Now().Add(31*time.Minute), 24*time.Hour)
	require.NoError(t, err)
	assert.EqualValues(t, 1, lapsed)
	assert.NotContains(t, policy(), "tag:prod-db:*", "access ends on its own")

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/temp-access", nil, &list))
	require.Len(t, list, 2)
	assert.Equal(t, "expired", list[0].Status)
	assert.Equal(t, "ended", list[1].Status)
}

func TestAdminConsolePrivateResources(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	save := func(name, addr string) (int, uint64) {
		var out map[string]uint64
		code := boss.do(t, http.MethodPost, "/admin/api/resources", map[string]string{"name": name, "address": addr}, &out)

		return code, out["id"]
	}

	for _, bad := range []string{"db.example.com", "0.0.0.0/0", "100.64.0.5", "fd7a:115c:a1e0::1"} {
		code, _ := save("x", bad)
		assert.Equal(t, http.StatusBadRequest, code, bad)
	}

	code, _ := save("", "10.0.0.1")
	assert.Equal(t, http.StatusBadRequest, code, "a name is required")

	code, id := save("orders-db", "10.20.1.25")
	require.Equal(t, http.StatusOK, code)

	code, _ = save("vpc", "10.20.7.9/16")
	require.Equal(t, http.StatusOK, code)

	var nets struct {
		Resources []adminResource `json:"resources"`
	}
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/networks", nil, &nets))
	require.Len(t, nets.Resources, 2)
	assert.Equal(t, "10.20.1.25/32", nets.Resources[0].Address, "an address becomes a /32")
	assert.Equal(t, "10.20.0.0/16", nets.Resources[1].Address, "a range is masked")
	assert.Empty(t, nets.Resources[0].CoveredBy, "no router yet")

	// Usable as a rule destination, not as a source.
	rid := fmt.Sprint(id)
	assert.Equal(t, http.StatusBadRequest, boss.do(t, http.MethodPost, "/admin/api/rules", map[string]any{
		"name": "bad", "ports": "*", "enabled": true,
		"sources":      []map[string]string{{"kind": "resource", "principal": rid}},
		"destinations": []map[string]string{{"kind": "everyone"}},
	}, nil))
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPost, "/admin/api/rules", map[string]any{
		"name": "people to orders-db", "ports": "5432", "protocol": "tcp", "enabled": true,
		"sources":      []map[string]string{{"kind": "everyone"}},
		"destinations": []map[string]string{{"kind": "resource", "principal": rid}},
	}, nil))

	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/device-restriction", map[string]bool{"enabled": true}, nil))

	p, err := h.app.state.EffectivePolicy()
	require.NoError(t, err)
	assert.Contains(t, string(p), "10.20.1.25/32:5432")

	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodDelete, "/admin/api/resources/"+rid, nil, nil))

	p, err = h.app.state.EffectivePolicy()
	require.NoError(t, err)
	assert.NotContains(t, string(p), "10.20.1.25")
}
