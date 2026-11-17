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
	"golang.org/x/oauth2/jwt"
	"gorm.io/gorm"
)

// Google Workspace does not push SCIM to custom apps, so orgscale pulls the
// directory instead: users, groups and memberships, read with a service
// account that has domain-wide delegation, impersonating a Workspace admin.
//
// Each run:
//   - creates users for active Workspace accounts not in orgscale yet;
//   - offboards users that are suspended or archived in Workspace, and users
//     whose email is in a Workspace domain but no longer in the directory;
//   - mirrors Workspace groups as directory groups with source "google".
//
// A run that would offboard more than half the users stops instead: that is
// far more likely a misconfiguration than a real mass departure.

const (
	googleDirectoryBase = "https://admin.googleapis.com/admin/directory/v1"
	googleTokenURL      = "https://oauth2.googleapis.com/token"
	googleSyncTimeout   = 5 * time.Minute
)

var googleDirectoryScopes = []string{
	"https://www.googleapis.com/auth/admin.directory.user.readonly",
	"https://www.googleapis.com/auth/admin.directory.group.readonly",
	"https://www.googleapis.com/auth/admin.directory.group.member.readonly",
}

var errGoogleMassRemoval = errors.New("refusing to offboard more than half of the users, or more than 25, in one run; check the Google sync settings")

// googleMaxRemovalsPerRun caps offboarding in one run.
const googleMaxRemovalsPerRun = 25

type googleServiceAccount struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func parseServiceAccount(b []byte) (*googleServiceAccount, error) {
	var sa googleServiceAccount

	err := json.Unmarshal(b, &sa)
	if err != nil {
		return nil, fmt.Errorf("the service account key is not valid JSON: %w", err)
	}

	if sa.Type != "service_account" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("the service account key must be a Google service account JSON key (type service_account, with client_email and private_key)")
	}

	return &sa, nil
}

type googleSyncer struct {
	h    *Orgscale
	kick chan struct{}

	runMu sync.Mutex
}

func newGoogleSyncer(h *Orgscale) *googleSyncer {
	return &googleSyncer{h: h, kick: make(chan struct{}, 1)}
}

// Kick asks the background loop to re-read its settings and run soon.
func (g *googleSyncer) Kick() {
	if g == nil {
		return
	}

	select {
	case g.kick <- struct{}{}:
	default:
	}
}

func (g *googleSyncer) settings() (types.GoogleSyncSettings, bool) {
	var s types.GoogleSyncSettings

	ok, err := g.h.state.GetSettingJSON(types.SettingGoogleSync, &s)
	if err != nil {
		log.Warn().Err(err).Msg("google sync: unreadable settings")

		return s, false
	}

	return s, ok
}

// Run syncs on the configured interval until ctx ends.
func (g *googleSyncer) Run(ctx context.Context) {
	for {
		s, _ := g.settings()
		interval := time.Duration(max(s.IntervalMinutes, 5)) * time.Minute

		if s.Enabled {
			g.RunNow(ctx)
		}

		select {
		case <-ctx.Done():
			return
		case <-g.kick:
		case <-time.After(interval):
		}
	}
}

// RunNow runs one sync and records its status.
func (g *googleSyncer) RunNow(ctx context.Context) types.GoogleSyncStatus {
	g.runMu.Lock()
	defer g.runMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, googleSyncTimeout)
	defer cancel()

	status := types.GoogleSyncStatus{LastRun: time.Now().UTC()}

	s, ok := g.settings()

	switch {
	case !ok || s.ServiceAccountJSON == "" || s.AdminEmail == "":
		status.Error = "Google sync is not configured"
	default:
		err := g.sync(ctx, s, &status)
		if err != nil {
			status.Error = err.Error()
		} else {
			status.OK = true
		}
	}

	if status.Error != "" {
		log.Warn().Str("error", status.Error).Msg("google sync failed")
	} else {
		log.Info().Int("users", status.Users).Int("groups", status.Groups).
			Int("created", status.Created).Int("offboarded", status.Disabled).Msg("google sync done")
	}

	err := g.h.state.SetSettingJSON(types.SettingGoogleSyncStatus, status, "google-sync")
	if err != nil {
		log.Error().Err(err).Msg("google sync: storing status")
	}

	return status
}

type googleUser struct {
	ID           string `json:"id"`
	PrimaryEmail string `json:"primaryEmail"`
	Name         struct {
		FullName string `json:"fullName"`
	} `json:"name"`
	Aliases            []string `json:"aliases"`
	NonEditableAliases []string `json:"nonEditableAliases"`
	Suspended          bool     `json:"suspended"`
	Archived           bool     `json:"archived"`
	ThumbnailPhotoURL  string   `json:"thumbnailPhotoUrl"`
}

type googleGroup struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type googleMember struct {
	Email  string `json:"email"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

func (g *googleSyncer) client(ctx context.Context, s types.GoogleSyncSettings) (*http.Client, error) {
	sa, err := parseServiceAccount([]byte(s.ServiceAccountJSON))
	if err != nil {
		return nil, err
	}

	cfg := &jwt.Config{
		Email:      sa.ClientEmail,
		PrivateKey: []byte(sa.PrivateKey),
		Scopes:     googleDirectoryScopes,
		// Pinned: the key file's token_uri is not trusted, the signed
		// assertion must only ever go to Google.
		TokenURL: googleTokenURL,
		Subject:  s.AdminEmail,
	}

	return cfg.Client(ctx), nil
}

// list pages through a Directory API collection.
func googleList[T any](ctx context.Context, c *http.Client, endpoint, field string, q url.Values) ([]T, error) {
	var out []T

	for {
		u := endpoint + "?" + q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}

		resp, err := c.Do(req)
		if err != nil {
			return nil, fmt.Errorf("calling the Google Directory API: %w", err)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()

		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("google directory API %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
		}

		var page map[string]json.RawMessage

		err = json.Unmarshal(body, &page)
		if err != nil {
			return nil, fmt.Errorf("decoding Google response: %w", err)
		}

		if items, ok := page[field]; ok {
			var batch []T

			err = json.Unmarshal(items, &batch)
			if err != nil {
				return nil, fmt.Errorf("decoding Google %s: %w", field, err)
			}

			out = append(out, batch...)
		}

		var next string
		if raw, ok := page["nextPageToken"]; ok {
			_ = json.Unmarshal(raw, &next)
		}

		if next == "" {
			return out, nil
		}

		q.Set("pageToken", next)
	}
}

func (g *googleSyncer) sync(ctx context.Context, s types.GoogleSyncSettings, status *types.GoogleSyncStatus) error {
	c, err := g.client(ctx, s)
	if err != nil {
		return err
	}

	customer := cmp.Or(s.Customer, "my_customer")

	gusers, err := googleList[googleUser](ctx, c, googleDirectoryBase+"/users", "users",
		url.Values{"customer": {customer}, "maxResults": {"500"}, "projection": {"basic"}})
	if err != nil {
		return err
	}

	if len(gusers) == 0 {
		return errors.New("the Google directory returned no users; check the customer ID and the admin email")
	}

	ggroups, err := googleList[googleGroup](ctx, c, googleDirectoryBase+"/groups", "groups",
		url.Values{"customer": {customer}, "maxResults": {"200"}})
	if err != nil {
		return err
	}

	members := make(map[string][]googleMember, len(ggroups))

	for _, gg := range ggroups {
		ms, err := googleList[googleMember](ctx, c, googleDirectoryBase+"/groups/"+url.PathEscape(gg.ID)+"/members", "members",
			url.Values{"maxResults": {"200"}, "includeDerivedMembership": {"true"}})
		if err != nil {
			return err
		}

		members[gg.ID] = ms
	}

	return g.apply(gusers, ggroups, members, status)
}

// apply makes orgscale match the fetched directory. Split from sync so tests
// can drive it without Google.
func (g *googleSyncer) apply(
	gusers []googleUser,
	ggroups []googleGroup,
	members map[string][]googleMember,
	status *types.GoogleSyncStatus,
) error {
	h := g.h

	active := make(map[string]googleUser)
	inactive := make(map[string]bool)
	// known holds every address of an active account, aliases included, so
	// someone signed in with an alias is not mistaken for a leaver.
	known := make(map[string]bool)

	for _, u := range gusers {
		email := strings.ToLower(u.PrimaryEmail)
		addrs := append([]string{email}, append(u.Aliases, u.NonEditableAliases...)...)

		if u.Suspended || u.Archived {
			for _, a := range addrs {
				inactive[strings.ToLower(a)] = true
			}
		} else {
			active[email] = u
			for _, a := range addrs {
				known[strings.ToLower(a)] = true
			}
		}
	}

	users, err := h.state.ListAllUsers()
	if err != nil {
		return err
	}

	domains := make(map[string]bool)

	for _, u := range gusers {
		if _, d, ok := strings.Cut(strings.ToLower(u.PrimaryEmail), "@"); ok {
			domains[d] = true
		}
	}

	// Who to offboard:
	//   - any user whose address Workspace marks suspended or archived;
	//   - users from this sync or from SSO sign-in whose address is in a
	//     Workspace domain but belongs to no active account (aliases count,
	//     and Google keeps a renamed account's old address as an alias).
	// SCIM and local users are never removed for a missing address.
	var leavers []types.User

	for _, u := range users {
		email := strings.ToLower(u.Email)
		if email == "" || known[email] {
			continue
		}

		_, d, _ := strings.Cut(email, "@")
		syncable := u.Provider == userProviderGoogle || u.Provider == util.RegisterMethodOIDC

		if inactive[email] || (syncable && domains[d]) {
			leavers = append(leavers, u)
		}
	}

	if len(leavers) > googleMaxRemovalsPerRun || (len(users) >= 4 && len(leavers)*2 > len(users)) {
		return errGoogleMassRemoval
	}

	for _, u := range leavers {
		err := h.offboardUser(types.UserID(u.ID), "google-sync", types.AuditActorSystem,
			"suspended, archived or deleted in Google Workspace")
		if err != nil {
			return fmt.Errorf("offboarding %s: %w", u.Email, err)
		}

		status.Disabled++
	}

	byEmail := make(map[string]types.UserID, len(active))

	for email, gu := range active {
		u, created, err := h.provisionUser(email, gu.Name.FullName, gu.ThumbnailPhotoURL, userProviderGoogle)
		if err != nil {
			log.Warn().Err(err).Str("email", email).Msg("google sync: skipping user")

			continue
		}

		if created {
			status.Created++
		}

		byEmail[email] = types.UserID(u.ID)
	}

	status.Users = len(active)

	keep := make([]string, 0, len(ggroups))

	changes, err := h.state.WriteDirectory(func(tx *gorm.DB) error {
		for _, gg := range ggroups {
			group, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceGoogle, gg.ID, cmp.Or(gg.Name, gg.Email))
			if err != nil {
				return err
			}

			keep = append(keep, gg.ID)

			var ids []types.UserID

			for _, m := range members[gg.ID] {
				if m.Type != "USER" || (m.Status != "" && m.Status != "ACTIVE") {
					continue
				}

				if id, ok := byEmail[strings.ToLower(m.Email)]; ok {
					ids = append(ids, id)
				}
			}

			err = hsdb.SetDirectoryGroupMembers(tx, group.ID, ids)
			if err != nil {
				return err
			}
		}

		removed, err := hsdb.DeleteDirectoryGroupsExcept(tx, types.DirectorySourceGoogle, keep)
		if err != nil {
			return err
		}

		if removed > 0 {
			log.Info().Int64("groups", removed).Msg("google sync: removed groups deleted in Workspace")
		}

		return nil
	})
	h.Change(changes...)

	if err != nil {
		return fmt.Errorf("writing groups: %w", err)
	}

	status.Groups = len(ggroups)

	_ = h.state.AppendAuditEvent(types.AuditEvent{
		Actor: "google-sync", ActorKind: types.AuditActorSystem, Action: "google_sync.run",
		Detail: "users=" + strconv.Itoa(status.Users) + " groups=" + strconv.Itoa(status.Groups) +
			" created=" + strconv.Itoa(status.Created) + " offboarded=" + strconv.Itoa(status.Disabled),
	})

	return nil
}
