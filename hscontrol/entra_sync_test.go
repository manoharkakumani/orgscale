package hscontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGraph serves the parts of Microsoft Graph the Entra sync uses.
type fakeGraph struct {
	mu           sync.Mutex
	users        []map[string]any
	groups       []map[string]any
	members      map[string][]string
	usersChanged bool
	graphCalls   int
	url          string
}

func (f *fakeGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "token_type": "Bearer", "expires_in": 3600})

		return
	}

	f.graphCalls++

	if r.Header.Get("Authorization") != "Bearer t" {
		w.WriteHeader(http.StatusUnauthorized)

		return
	}

	page := func(v any, delta string) {
		out := map[string]any{"value": v}
		if delta != "" {
			out["@odata.deltaLink"] = f.url + delta
		}

		_ = json.NewEncoder(w).Encode(out)
	}

	q := r.URL.Query()

	switch {
	case r.URL.Path == "/users/delta" && q.Get("$deltatoken") == "latest":
		page([]any{}, "/users/delta?$deltatoken=u1")
	case r.URL.Path == "/users/delta":
		var changed []any
		if f.usersChanged {
			changed = []any{map[string]any{"id": "u-bob"}}
		}

		page(changed, "/users/delta?$deltatoken=u2")
	case r.URL.Path == "/groups/delta" && q.Get("$deltatoken") == "latest":
		page([]any{}, "/groups/delta?$deltatoken=g1")
	case r.URL.Path == "/groups/delta":
		page([]any{}, "/groups/delta?$deltatoken=g2")
	case r.URL.Path == "/organization":
		page([]any{map[string]any{"verifiedDomains": []any{map[string]any{"name": "stayx.ai"}, map[string]any{"name": "acquired.example"}}}}, "")
	case r.URL.Path == "/users":
		page(f.users, "")
	case r.URL.Path == "/groups":
		page(f.groups, "")
	case strings.HasPrefix(r.URL.Path, "/groups/") && strings.HasSuffix(r.URL.Path, "/transitiveMembers/microsoft.graph.user"):
		id := strings.Split(r.URL.Path, "/")[2]

		var ms []any
		for _, m := range f.members[id] {
			ms = append(ms, map[string]any{"id": m})
		}

		page(ms, "")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeGraph) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := f.graphCalls
	f.graphCalls = 0

	return n
}

func TestEntraSync(t *testing.T) {
	app := createTestApp(t)

	enabled, disabled := true, false

	fg := &fakeGraph{
		users: []map[string]any{
			{"id": "u-alice", "displayName": "Alice", "userPrincipalName": "Alice@stayx.ai", "accountEnabled": enabled, "userType": "Member"},
			{"id": "u-bob", "displayName": "Bob", "userPrincipalName": "bob@stayx.ai", "accountEnabled": enabled, "userType": "Member"},
			{"id": "u-guest", "displayName": "Guest", "userPrincipalName": "eve_x.com#EXT#@stayx.onmicrosoft.com", "accountEnabled": enabled, "userType": "Guest"},
		},
		groups:  []map[string]any{{"id": "g-eng", "displayName": "Engineering"}},
		members: map[string][]string{"g-eng": {"u-alice", "u-bob", "u-guest"}},
	}

	srv := httptest.NewServer(fg)
	t.Cleanup(srv.Close)

	fg.url = srv.URL
	app.entraSync.graphBase, app.entraSync.loginBase = srv.URL, srv.URL

	require.NoError(t, app.state.SetSettingJSON(types.SettingEntraSync, types.EntraSyncSettings{
		Enabled: true, TenantID: "tenant", ClientID: "client", ClientSecret: "secret",
	}, "test"))

	ctx := context.Background()

	// First run reads everything: delta links, users, groups, members.
	st := app.entraSync.RunNow(ctx, false)
	require.True(t, st.OK, st.Error)
	assert.False(t, st.Unchanged)
	assert.Equal(t, 2, st.Users, "guests are not synced")
	assert.Equal(t, 2, st.Created)
	assert.Equal(t, 1, st.Groups)
	assert.Equal(t, 6, fg.calls())

	users, err := app.state.ListAllUsers()
	require.NoError(t, err)
	require.Len(t, users, 2)

	groups, err := app.state.ListDirectoryGroups()
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "engineering", groups[0].Name)
	assert.Len(t, groups[0].MemberIDs, 2)

	// Nothing changed: the run costs two requests.
	st = app.entraSync.RunNow(ctx, false)
	require.True(t, st.OK, st.Error)
	assert.True(t, st.Unchanged)
	assert.Equal(t, 2, st.Users, "counts of the last full read are kept")
	assert.Equal(t, 2, fg.calls(), "a quiet check is two small requests")

	// A local user whose address a disabled Entra account claims in its
	// mail attribute is never touched.
	_, _, err = app.state.CreateUser(types.User{Name: "carol", Email: "carol@stayx.ai"})
	require.NoError(t, err)

	fg.mu.Lock()
	fg.users = append(fg.users, map[string]any{"id": "u-x", "displayName": "X", "userPrincipalName": "x@stayx.ai", "mail": "carol@stayx.ai", "accountEnabled": disabled, "userType": "Member"})
	fg.mu.Unlock()

	st = app.entraSync.RunNow(ctx, true)
	require.True(t, st.OK, st.Error)
	assert.Zero(t, st.Disabled)

	users, err = app.state.ListAllUsers()
	require.NoError(t, err)
	require.Len(t, users, 3, "alice, bob and the local carol stay")

	// Bob is disabled in Entra: the change check notices and he is removed.
	fg.mu.Lock()
	fg.users[1]["accountEnabled"] = disabled
	fg.usersChanged = true
	fg.mu.Unlock()

	st = app.entraSync.RunNow(ctx, false)
	require.True(t, st.OK, st.Error)
	assert.False(t, st.Unchanged)
	assert.Equal(t, 1, st.Disabled)

	users, err = app.state.ListAllUsers()
	require.NoError(t, err)

	var emails []string
	for _, u := range users {
		emails = append(emails, u.Email)
	}

	assert.ElementsMatch(t, []string{"alice@stayx.ai", "carol@stayx.ai"}, emails)

	// Someone who signed in with SSO on a verified domain nobody else uses
	// is removed once they are gone from Entra.
	_, _, err = app.state.CreateUser(types.User{Name: "dan", Email: "dan@acquired.example", Provider: "oidc"})
	require.NoError(t, err)

	st = app.entraSync.RunNow(ctx, true)
	require.True(t, st.OK, st.Error)
	assert.Equal(t, 1, st.Disabled)
	assert.Empty(t, st.Warning)
}

func TestEntraSyncReusesSSOApp(t *testing.T) {
	app := createTestApp(t)

	require.NoError(t, app.state.SetSettingJSON(types.SettingSSOOIDC, types.StoredOIDC{
		Issuer: "https://login.microsoftonline.com/e644c415-7c35-4777-820d-379e907e7dd1/v2.0", ClientID: "app", ClientSecret: "s",
	}, "test"))

	c, err := app.entraSync.credentials(types.EntraSyncSettings{Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, "e644c415-7c35-4777-820d-379e907e7dd1", c.tenant)
	assert.Equal(t, "app", c.clientID)
	assert.Equal(t, "s", c.secret)

	// A different app needs its own secret.
	_, err = app.entraSync.credentials(types.EntraSyncSettings{Enabled: true, ClientID: "other"})
	require.Error(t, err)
}
