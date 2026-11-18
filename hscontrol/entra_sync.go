package hscontrol

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/manoharkakumani/orgscale/hscontrol/util"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2/clientcredentials"
	"gorm.io/gorm"
)

// Microsoft Entra ID directory sync, through Microsoft Graph.
//
// Each run first asks Graph what changed since the last run, with delta
// queries: one small request for users and one for groups. When nothing
// changed the run ends there. When something did, or a day has passed, it
// reads the directory and makes orgscale match it: members are added,
// disabled or deleted accounts are removed with their devices, and group
// memberships follow Entra. Guests are not synced.
//
// It reuses the single sign-on app registration, which needs the Microsoft
// Graph application permissions User.Read.All, Group.Read.All and
// GroupMember.Read.All with admin consent.

const (
	entraGraphBase     = "https://graph.microsoft.com/v1.0"
	entraLoginBase     = "https://login.microsoftonline.com"
	entraSyncTimeout   = 10 * time.Minute
	entraFullEvery     = 24 * time.Hour
	entraMaxRetries    = 4
	entraMaxRetryAfter = 60 * time.Second
)

var errEntraNotConfigured = errors.New("Microsoft Entra sync needs a tenant, client ID and secret: set up single sign-on with Microsoft Entra ID, or enter them here")

type entraSyncer struct {
	h    *Orgscale
	kick chan struct{}

	// graphBase and loginBase are overridden in tests.
	graphBase string
	loginBase string

	runMu sync.Mutex
}

func newEntraSyncer(h *Orgscale) *entraSyncer {
	return &entraSyncer{h: h, kick: make(chan struct{}, 1), graphBase: entraGraphBase, loginBase: entraLoginBase}
}

// Kick asks the background loop to re-read its settings and run soon.
func (e *entraSyncer) Kick() {
	if e == nil {
		return
	}

	select {
	case e.kick <- struct{}{}:
	default:
	}
}

func (e *entraSyncer) settings() types.EntraSyncSettings {
	var s types.EntraSyncSettings

	_, err := e.h.state.GetSettingJSON(types.SettingEntraSync, &s)
	if err != nil {
		log.Warn().Err(err).Msg("entra sync: unreadable settings")
	}

	return s
}

// Run syncs on the configured interval until ctx ends.
func (e *entraSyncer) Run(ctx context.Context) {
	for {
		s := e.settings()
		interval := time.Duration(max(s.IntervalMinutes, 5)) * time.Minute

		if s.Enabled {
			e.RunNow(ctx, false)
		}

		select {
		case <-ctx.Done():
			return
		case <-e.kick:
		case <-time.After(interval):
		}
	}
}

// entraCredentials are the tenant and app the sync signs in as.
type entraCredentials struct {
	tenant, clientID, secret string
}

// credentials uses the sync settings, falling back to the single sign-on
// app registration when it is a Microsoft Entra ID tenant.
func (e *entraSyncer) credentials(s types.EntraSyncSettings) (entraCredentials, error) {
	c := entraCredentials{tenant: s.TenantID, clientID: s.ClientID, secret: s.ClientSecret}

	if c.tenant == "" || c.clientID == "" || c.secret == "" {
		var stored types.StoredOIDC
		_, _ = e.h.state.GetSettingJSON(types.SettingSSOOIDC, &stored)

		issuer, clientID, secret := stored.Issuer, stored.ClientID, stored.ClientSecret

		if e.h.cfg.OIDC.Issuer != "" {
			issuer, clientID = string(e.h.cfg.OIDC.Issuer), e.h.cfg.OIDC.ClientID
			secret, _ = types.ResolveSecret("oidc.client_secret", string(e.h.cfg.OIDC.ClientSecret))
		}

		if tenant, ok := entraTenant(issuer); ok {
			c.tenant = cmp.Or(c.tenant, tenant)
			c.clientID = cmp.Or(c.clientID, clientID)

			if c.clientID == clientID {
				c.secret = cmp.Or(c.secret, secret)
			}
		}
	}

	if c.tenant == "" || c.clientID == "" || c.secret == "" {
		return c, errEntraNotConfigured
	}

	return c, nil
}

// graph is a Microsoft Graph client that counts its requests and honours
// throttling.
type graph struct {
	c        *http.Client
	base     string
	requests int
}

func (e *entraSyncer) graphClient(ctx context.Context, c entraCredentials) *graph {
	cc := clientcredentials.Config{
		ClientID:     c.clientID,
		ClientSecret: c.secret,
		TokenURL:     e.loginBase + "/" + url.PathEscape(c.tenant) + "/oauth2/v2.0/token",
		Scopes:       []string{"https://graph.microsoft.com/.default"},
	}

	return &graph{c: cc.Client(ctx), base: e.graphBase}
}

type graphPage[T any] struct {
	Value     []T    `json:"value"`
	NextLink  string `json:"@odata.nextLink"`
	DeltaLink string `json:"@odata.deltaLink"`
}

type graphError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// get fetches one URL, waiting and retrying when Graph throttles.
func (g *graph) get(ctx context.Context, u string, out any) error {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		u = g.base + u
	}

	// Paging and delta links must stay on Graph.
	if !strings.HasPrefix(u, g.base) {
		return fmt.Errorf("refusing a Graph link outside %s", g.base)
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}

		req.Header.Set("ConsistencyLevel", "eventual")

		g.requests++

		resp, err := g.c.Do(req)
		if err != nil {
			return fmt.Errorf("calling Microsoft Graph: %w", err)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()

		if err != nil {
			return err
		}

		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) && attempt < entraMaxRetries {
			wait := 10 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(min(wait, entraMaxRetryAfter)):
			}

			continue
		}

		if resp.StatusCode != http.StatusOK {
			var ge graphError
			_ = json.Unmarshal(body, &ge)

			msg := cmp.Or(ge.Error.Message, resp.Status)
			if resp.StatusCode == http.StatusForbidden || ge.Error.Code == "Authorization_RequestDenied" {
				msg += " (grant the app registration the Microsoft Graph application permissions User.Read.All, Group.Read.All and GroupMember.Read.All, with admin consent)"
			}

			return fmt.Errorf("Microsoft Graph: %s", msg)
		}

		return json.Unmarshal(body, out)
	}
}

// list follows paging and returns every item, and the delta link if the
// query was a delta query.
func graphList[T any](ctx context.Context, g *graph, u string) ([]T, string, error) {
	var out []T

	for u != "" {
		var page graphPage[T]

		err := g.get(ctx, u, &page)
		if err != nil {
			return nil, "", err
		}

		out = append(out, page.Value...)

		if page.DeltaLink != "" {
			return out, page.DeltaLink, nil
		}

		u = page.NextLink
	}

	return out, "", nil
}

type entraUser struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
	AccountEnabled    *bool  `json:"accountEnabled"`
	UserType          string `json:"userType"`
}

type entraGroup struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type entraMember struct {
	ID string `json:"id"`
}

// RunNow runs one sync and records its status. full skips the change check.
func (e *entraSyncer) RunNow(ctx context.Context, full bool) types.EntraSyncStatus {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, entraSyncTimeout)
	defer cancel()

	var prev types.EntraSyncStatus
	_, _ = e.h.state.GetSettingJSON(types.SettingEntraSyncStatus, &prev)

	status := types.EntraSyncStatus{LastFull: prev.LastFull}
	status.LastRun = time.Now().UTC()

	err := e.sync(ctx, full, &status)
	if err != nil {
		status.Error = err.Error()
		log.Warn().Err(err).Msg("entra sync failed")
	} else {
		status.OK = true
		if status.Unchanged {
			// Keep the counts of the last full read.
			status.Users, status.Groups = prev.Users, prev.Groups
		}

		log.Info().Bool("unchanged", status.Unchanged).Int("requests", status.Requests).
			Int("users", status.Users).Int("groups", status.Groups).
			Int("created", status.Created).Int("offboarded", status.Disabled).Msg("entra sync done")
	}

	err = e.h.state.SetSettingJSON(types.SettingEntraSyncStatus, status, "entra-sync")
	if err != nil {
		log.Error().Err(err).Msg("entra sync: storing status")
	}

	return status
}

func (e *entraSyncer) sync(ctx context.Context, full bool, status *types.EntraSyncStatus) error {
	creds, err := e.credentials(e.settings())
	if err != nil {
		return err
	}

	g := e.graphClient(ctx, creds)
	defer func() { status.Requests = g.requests }()

	var st types.EntraSyncState
	_, _ = e.h.state.GetSettingJSON(types.SettingEntraSyncState, &st)

	// Cheap path: ask what changed since last time.
	if !full && st.UsersDelta != "" && st.GroupsDelta != "" && time.Since(st.LastFull) < entraFullEvery {
		users, usersDelta, err := graphList[json.RawMessage](ctx, g, st.UsersDelta)
		if err != nil {
			return err
		}

		groups, groupsDelta, err := graphList[json.RawMessage](ctx, g, st.GroupsDelta)
		if err != nil {
			return err
		}

		if len(users) == 0 && len(groups) == 0 {
			st.UsersDelta, st.GroupsDelta = cmp.Or(usersDelta, st.UsersDelta), cmp.Or(groupsDelta, st.GroupsDelta)
			status.Unchanged = true

			return e.h.state.SetSettingJSON(types.SettingEntraSyncState, st, "entra-sync")
		}
	}

	// Full read. Take the delta links first, so changes made during the
	// read are seen by the next run.
	_, usersDelta, err := graphList[json.RawMessage](ctx, g, "/users/delta?$select=id,accountEnabled,userPrincipalName,displayName,userType&$deltatoken=latest")
	if err != nil {
		return err
	}

	_, groupsDelta, err := graphList[json.RawMessage](ctx, g, "/groups/delta?$select=id,displayName,members&$deltatoken=latest")
	if err != nil {
		return err
	}

	users, _, err := graphList[entraUser](ctx, g,
		"/users?$select=id,displayName,userPrincipalName,accountEnabled,userType&$top=999")
	if err != nil {
		return err
	}

	groups, _, err := graphList[entraGroup](ctx, g, "/groups?$select=id,displayName&$top=999")
	if err != nil {
		return err
	}

	members := make(map[string][]string, len(groups))

	for _, gr := range groups {
		ms, _, err := graphList[entraMember](ctx, g,
			"/groups/"+url.PathEscape(gr.ID)+"/transitiveMembers/microsoft.graph.user?$select=id&$top=999")
		if err != nil {
			return err
		}

		for _, m := range ms {
			members[gr.ID] = append(members[gr.ID], m.ID)
		}
	}

	err = e.apply(users, groups, members, &status.GoogleSyncStatus)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	status.LastFull = now

	return e.h.state.SetSettingJSON(types.SettingEntraSyncState,
		types.EntraSyncState{UsersDelta: usersDelta, GroupsDelta: groupsDelta, LastFull: now}, "entra-sync")
}

// entraEmail is the address a member signs in with: their UPN, as
// [applyProviderEmail] uses it.
func entraEmail(u entraUser) string {
	upn := strings.ToLower(strings.TrimSpace(u.UserPrincipalName))
	if strings.Contains(upn, "#ext#") {
		return ""
	}

	return upn
}

// apply makes orgscale match the Entra directory. Split from sync so tests
// can drive it without Graph.
func (e *entraSyncer) apply(users []entraUser, groups []entraGroup, members map[string][]string, status *types.GoogleSyncStatus) error {
	h := e.h

	active := make(map[string]entraUser)
	inactive := make(map[string]bool)
	known := make(map[string]bool)
	domains := make(map[string]bool)
	byEntraID := make(map[string]string)

	for _, u := range users {
		if !strings.EqualFold(u.UserType, "Member") && u.UserType != "" {
			continue // guests are not synced
		}

		// People are identified only by their UPN, the name they sign in
		// with. The mail and proxy addresses are not used: they can carry
		// addresses that are not the account's own.
		email := entraEmail(u)
		if email == "" {
			continue
		}

		if _, d, ok := strings.Cut(email, "@"); ok {
			domains[d] = true
		}

		if u.AccountEnabled != nil && !*u.AccountEnabled {
			inactive[email] = true

			continue
		}

		active[email] = u
		byEntraID[u.ID] = email
		known[email] = true
	}

	if len(active) == 0 {
		return errors.New("Microsoft Entra ID returned no member accounts; check the tenant and the app's permissions")
	}

	existing, err := h.state.ListAllUsers()
	if err != nil {
		return err
	}

	// Who to offboard: users from this sync or from single sign-on whose
	// UPN is disabled in Entra, or is in a tenant domain but belongs to no
	// active member. SCIM, Google and local users are never removed because
	// of Entra data.
	var leavers []types.User

	for _, u := range existing {
		email := strings.ToLower(u.Email)
		if email == "" || known[email] {
			continue
		}

		_, d, _ := strings.Cut(email, "@")
		syncable := u.Provider == userProviderEntra || u.Provider == util.RegisterMethodOIDC

		if syncable && (inactive[email] || domains[d]) {
			leavers = append(leavers, u)
		}
	}

	if len(leavers) > googleMaxRemovalsPerRun || (len(existing) >= 4 && len(leavers)*2 > len(existing)) {
		return errors.New("refusing to offboard more than half of the users, or more than 25, in one run; check the Microsoft Entra sync settings")
	}

	for _, u := range leavers {
		err := h.offboardUser(types.UserID(u.ID), "entra-sync", types.AuditActorSystem,
			"disabled or deleted in Microsoft Entra ID")
		if err != nil {
			return fmt.Errorf("offboarding %s: %w", u.Email, err)
		}

		status.Disabled++
	}

	byEmail := make(map[string]types.UserID, len(active))

	for email, eu := range active {
		u, created, err := h.provisionUser(email, eu.DisplayName, "", userProviderEntra)
		if err != nil {
			log.Warn().Err(err).Str("email", email).Msg("entra sync: skipping user")

			continue
		}

		if created {
			status.Created++
		}

		byEmail[email] = types.UserID(u.ID)
	}

	status.Users = len(active)

	keep := make([]string, 0, len(groups))

	changes, err := h.state.WriteDirectory(func(tx *gorm.DB) error {
		for _, gr := range groups {
			group, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceEntra, gr.ID, cmp.Or(gr.DisplayName, gr.ID))
			if err != nil {
				return err
			}

			keep = append(keep, gr.ID)

			var ids []types.UserID

			for _, m := range members[gr.ID] {
				if id, ok := byEmail[byEntraID[m]]; ok {
					ids = append(ids, id)
				}
			}

			err = hsdb.SetDirectoryGroupMembers(tx, group.ID, ids)
			if err != nil {
				return err
			}
		}

		_, err := hsdb.DeleteDirectoryGroupsExcept(tx, types.DirectorySourceEntra, keep)

		return err
	})
	h.Change(changes...)

	if err != nil {
		return fmt.Errorf("writing groups: %w", err)
	}

	status.Groups = len(groups)

	_ = h.state.AppendAuditEvent(types.AuditEvent{
		Actor: "entra-sync", ActorKind: types.AuditActorSystem, Action: "entra_sync.run",
		Detail: "users=" + strconv.Itoa(status.Users) + " groups=" + strconv.Itoa(status.Groups) +
			" created=" + strconv.Itoa(status.Created) + " offboarded=" + strconv.Itoa(status.Disabled),
	})

	return nil
}
