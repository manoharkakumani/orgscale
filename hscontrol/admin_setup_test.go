package hscontrol

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSetupHarness is a server with the console on and no single sign-on.
func newSetupHarness(t *testing.T) *adminHarness {
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
	app.cfg.OIDC = types.OIDCConfig{
		Scope: []string{"openid", "profile", "email"},
		PKCE:  types.PKCEConfig{Enabled: true, Method: types.PKCEMethodS256},
	}
	app.cfg.Admin = types.AdminConfig{Enabled: true, SessionTimeout: time.Hour}
	app.authProvider = &authSwitch{web: NewAuthProviderWeb(srv.URL)}
	app.admin = newAdminAuth(app)
	router = app.HTTPHandler()

	t.Cleanup(func() {
		if p := app.oidcProvider(); p != nil {
			p.authCache.Close()
		}
	})

	return &adminHarness{app: app, idp: idp, srv: srv}
}

func TestAdminSetupFlow(t *testing.T) {
	h := newSetupHarness(t)

	// Nobody can sign in yet; the console says setup is needed.
	b := h.browser(t)

	assert.Equal(t, http.StatusUnauthorized, b.do(t, http.MethodGet, "/admin/api/me", nil, nil))

	// The link printed at start.
	link, _, err := h.app.admin.newSetupLink()
	require.NoError(t, err)

	token := link[strings.Index(link, "#")+1:]
	require.True(t, strings.HasPrefix(token, "oskey-setup-"))

	// A wrong token is refused; the right one opens a setup session once.
	assert.Equal(t, http.StatusForbidden, b.do(t, http.MethodPost, "/admin/setup/api/session", map[string]string{"token": "oskey-setup-nope"}, nil))

	var sess map[string]string
	require.Equal(t, http.StatusOK, b.do(t, http.MethodPost, "/admin/setup/api/session", map[string]string{"token": token}, &sess))
	assert.Equal(t, http.StatusGone, b.do(t, http.MethodPost, "/admin/setup/api/session", map[string]string{"token": token}, nil), "single use")

	// Setup endpoints need the setup session's CSRF token.
	assert.Equal(t, http.StatusForbidden, b.do(t, http.MethodGet, "/admin/setup/api/state", nil, nil))
	b.csrf = sess["csrfToken"]

	var state ssoState
	require.Equal(t, http.StatusOK, b.do(t, http.MethodGet, "/admin/setup/api/state", nil, &state))
	assert.False(t, state.Configured)
	assert.Equal(t, []string{h.srv.URL + "/oidc/callback", h.srv.URL + "/admin/auth/callback"}, state.RedirectURLs)

	// The setup session cannot use the console API.
	assert.Equal(t, http.StatusUnauthorized, b.do(t, http.MethodGet, "/admin/api/users", nil, nil))

	// Bad settings are rejected before anything is stored.
	assert.Equal(t, http.StatusBadRequest, b.do(t, http.MethodPut, "/admin/setup/api/sso", map[string]any{
		"issuer": "http://idp.example.com", "clientId": "x", "clientSecret": "y", "adminEmail": "boss@example.com",
	}, nil), "plain http to a remote issuer")
	assert.Equal(t, http.StatusBadRequest, b.do(t, http.MethodPut, "/admin/setup/api/sso", map[string]any{
		"issuer": h.idp.Issuer(), "clientId": h.idp.ClientID, "clientSecret": h.idp.ClientSecret, "adminEmail": "not-an-email",
	}, nil))
	assert.Nil(t, h.app.oidcProvider())

	require.Equal(t, http.StatusOK, b.do(t, http.MethodPut, "/admin/setup/api/sso", map[string]any{
		"issuer": h.idp.Issuer(), "clientId": h.idp.ClientID, "clientSecret": h.idp.ClientSecret, "adminEmail": "Boss@Example.com",
	}, nil))
	require.NotNil(t, h.app.oidcProvider(), "single sign-on is live without a restart")

	// Only the named admin gets in.
	other := h.browser(t)
	assert.Equal(t, http.StatusForbidden, other.login(t, &mockoidc.MockUser{Subject: "eve", Email: "eve@example.com", EmailVerified: true}))

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	// Setup is closed: the setup session ended with the admin's sign-in.
	assert.Equal(t, http.StatusUnauthorized, b.do(t, http.MethodGet, "/admin/setup/api/state", nil, nil))

	var tok types.SetupToken
	found, err := h.app.state.GetSettingJSON(types.SettingSetupToken, &tok)
	require.NoError(t, err)
	assert.False(t, found)

	// The admin can see the settings and keep the secret when editing.
	var sso ssoState
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/settings/sso", nil, &sso))
	assert.True(t, sso.HasSecret)
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodPut, "/admin/api/settings/sso", map[string]any{
		"issuer": h.idp.Issuer(), "clientId": h.idp.ClientID,
	}, nil))

	var sessions []adminSessionInfo
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/sessions", nil, &sessions))
	require.Len(t, sessions, 1)
	assert.True(t, sessions[0].Current)
}

func TestAdminSessionsSurviveRestart(t *testing.T) {
	h := newAdminHarness(t)

	boss := h.browser(t)
	require.Equal(t, http.StatusOK, boss.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	second := h.browser(t)
	require.Equal(t, http.StatusOK, second.login(t, &mockoidc.MockUser{Subject: "boss", Email: "boss@example.com", EmailVerified: true}))

	// A fresh console (as after a restart) loads the sessions back.
	restarted := newAdminAuth(h.app)
	assert.Len(t, restarted.sessions, 2)

	var sessions []adminSessionInfo
	require.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/sessions", nil, &sessions))
	require.Len(t, sessions, 2)

	// Sign out everywhere else.
	require.Equal(t, http.StatusNoContent, boss.do(t, http.MethodDelete, "/admin/api/sessions/others", nil, nil))
	assert.Equal(t, http.StatusUnauthorized, second.do(t, http.MethodGet, "/admin/api/users", nil, nil))
	assert.Equal(t, http.StatusOK, boss.do(t, http.MethodGet, "/admin/api/users", nil, nil))

	rows, err := h.app.state.ListAdminSessions()
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestAdminSetupLinkRateLimitAndHTTPS(t *testing.T) {
	h := newSetupHarness(t)
	b := h.browser(t)

	for range 10 {
		b.do(t, http.MethodPost, "/admin/setup/api/session", map[string]string{"token": "oskey-setup-guess"}, nil)
	}

	assert.Equal(t, http.StatusTooManyRequests, b.do(t, http.MethodPost, "/admin/setup/api/session", map[string]string{"token": "oskey-setup-guess"}, nil))

	for _, tc := range []struct {
		url  string
		ok   bool
		lab  bool
		name string
	}{
		{"https://vpn.example.com", true, false, "https"},
		{"http://127.0.0.1:8080", true, false, "loopback"},
		{"http://localhost:8080", true, false, "localhost"},
		{"http://192.168.1.10:8080", false, false, "plain http"},
		{"http://192.168.1.10:8080", true, true, "plain http, lab override"},
	} {
		cfg := &types.Config{ServerURL: tc.url, Admin: types.AdminConfig{AllowInsecureHTTP: tc.lab}}
		assert.Equal(t, tc.ok, consoleTransportOK(cfg), tc.name)
	}
}
