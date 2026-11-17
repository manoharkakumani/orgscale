package hscontrol

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/policy/managed"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/rs/zerolog/log"
	"github.com/tailscale/hujson"
	"tailscale.com/util/rands"
)

// The admin console's own JSON API, under /admin/api. Users, devices, keys
// and the policy are changed through /api/v1 with the session; this API adds
// what the console needs beyond it: settings, directory groups, device
// access, user offboarding and the audit log.

const maxAdminRequestBody = 1 << 20

func (aa *adminAuth) routes(r chi.Router) {
	r.Get("/me", aa.handleMe)

	r.Group(func(r chi.Router) {
		r.Use(aa.requireSession)

		r.Get("/overview", aa.handleOverview)

		r.Get("/settings", aa.handleGetSettings)
		r.Put("/settings/login-rules", aa.handlePutLoginRules)
		r.Put("/settings/roles", aa.handlePutRoles)
		r.Put("/settings/device-restriction", aa.handlePutDeviceRestriction)
		r.Post("/settings/scim-token", aa.handleCreateSCIMToken)
		r.Delete("/settings/scim-token", aa.handleDeleteSCIMToken)
		r.Put("/settings/google-sync", aa.handlePutGoogleSync)
		r.Post("/settings/google-sync/run", aa.handleRunGoogleSync)

		r.Get("/users", aa.handleListUsers)
		r.Post("/users/{id}/disable", aa.handleDisableUser)
		r.Post("/users/{id}/enable", aa.handleEnableUser)
		r.Delete("/users/{id}", aa.handleRemoveUser)

		r.Get("/groups", aa.handleListGroups)

		r.Get("/devices", aa.handleListDevices)
		r.Put("/devices/{id}/access", aa.handlePutDeviceAccess)

		r.Get("/policy", aa.handleGetPolicy)

		r.Get("/audit", aa.handleListAudit)
	})
}

type adminSessionKey struct{}

func sessionFrom(r *http.Request) *adminSession {
	s, _ := r.Context().Value(adminSessionKey{}).(*adminSession)

	return s
}

func (aa *adminAuth) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, code, err := aa.authenticate(r)
		if err != nil {
			adminJSONError(w, code, err.Error())

			return
		}

		ctx := withAuditActor(r.Context(), auditActor{Name: sess.Email, Kind: types.AuditActorAdmin})
		ctx = contextWithSession(ctx, sess)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func adminJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)

	err := json.NewEncoder(w).Encode(v)
	if err != nil {
		log.Debug().Err(err).Msg("admin api: writing response")
	}
}

func adminJSONError(w http.ResponseWriter, code int, msg string) {
	adminJSON(w, code, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminRequestBody))
	dec.DisallowUnknownFields()

	err := dec.Decode(v)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())

		return false
	}

	return true
}

func (aa *adminAuth) auditReq(r *http.Request, action, target, detail string) {
	a, _ := auditActorFrom(r.Context())
	aa.audit(types.AuditEvent{
		Actor: a.Name, ActorKind: cmp.Or(a.Kind, types.AuditActorAdmin),
		Action: action, Target: target, Detail: detail,
	})
}

// handleMe returns the signed-in administrator and the session's CSRF token.
// It needs only the session cookie: the console calls it first.
func (aa *adminAuth) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, _ := aa.sessionFromRequest(r)
	if sess == nil {
		adminJSON(w, http.StatusUnauthorized, map[string]any{
			"signedIn": false,
			"loginUrl": "/admin/auth/login",
		})

		return
	}

	adminJSON(w, http.StatusOK, map[string]any{
		"signedIn":  true,
		"email":     sess.Email,
		"name":      sess.Name,
		"picture":   sess.Picture,
		"role":      sess.Role,
		"csrfToken": sess.CSRF,
		"expiresAt": sess.ExpiresAt,
		"serverUrl": aa.h.cfg.ServerURL,
		"version":   types.GetVersionInfo().Version,
	})
}

func (aa *adminAuth) handleOverview(w http.ResponseWriter, r *http.Request) {
	st := aa.h.state

	users, err := st.ListAllUsers()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	nodes := st.ListNodes()
	online, expired, tagged := 0, 0, 0

	for _, n := range nodes.All() {
		if o := n.IsOnline(); o.Valid() && o.Get() {
			online++
		}

		if n.IsExpired() {
			expired++
		}

		if n.IsTagged() {
			tagged++
		}
	}

	groups, err := st.ListDirectoryGroups()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	disabled, err := st.ListDisabledUsers()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	raw, err := st.RawPolicy()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	var warnings []string

	restrict := st.DeviceRestrictionEnabled()
	if restrict && managed.AllowsEverything(raw) {
		warnings = append(warnings,
			"Device restriction is on, but the policy has a rule letting everyone reach everything (src \"*\", dst \"*:*\"), so users still see every device. Remove that rule in Access policy.")
	}

	if !restrict {
		warnings = append(warnings,
			"Device restriction is off: every user sees every device. Turn it on in Settings to show users only their own and assigned devices.")
	}

	var scimSince string
	_, _ = st.GetSettingJSON(types.SettingSCIMTokenCreatedAt, &scimSince)

	var google types.GoogleSyncSettings
	_, _ = st.GetSettingJSON(types.SettingGoogleSync, &google)

	var googleStatus types.GoogleSyncStatus
	_, _ = st.GetSettingJSON(types.SettingGoogleSyncStatus, &googleStatus)

	adminJSON(w, http.StatusOK, map[string]any{
		"users":             len(users),
		"disabledUsers":     len(disabled),
		"devices":           nodes.Len(),
		"onlineDevices":     online,
		"expiredDevices":    expired,
		"taggedDevices":     tagged,
		"groups":            len(groups),
		"deviceRestriction": restrict,
		"ssoConfigured":     aa.h.cfg.OIDC.Issuer != "",
		"ssoIssuer":         aa.h.cfg.OIDC.Issuer,
		"scimEnabled":       scimSince != "",
		"googleSync":        google.Enabled,
		"googleSyncStatus":  googleStatus,
		"warnings":          warnings,
	})
}

// listSetting is a console-managed list with its configuration-file fallback.
type listSetting struct {
	// Value is the list in effect.
	Value []string `json:"value"`
	// Console is the list stored in the console, nil when not set there.
	Console []string `json:"console"`
	// Config is the list from the configuration file.
	Config []string `json:"config"`
}

func (aa *adminAuth) loginRule(key string, cfg []string) listSetting {
	var v []string

	ok, _ := aa.h.state.GetSettingJSON(key, &v)
	if !ok {
		return listSetting{Value: nonNil(cfg), Console: nil, Config: nonNil(cfg)}
	}

	return listSetting{Value: nonNil(v), Console: nonNil(v), Config: nonNil(cfg)}
}

func (aa *adminAuth) roleList(key string, cfg []string) listSetting {
	var v []string
	_, _ = aa.h.state.GetSettingJSON(key, &v)

	return listSetting{Value: nonNil(append(slices.Clone(cfg), v...)), Console: nonNil(v), Config: nonNil(cfg)}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

func (aa *adminAuth) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st := aa.h.state
	oidc := aa.h.cfg.OIDC

	var scimSince string
	_, _ = st.GetSettingJSON(types.SettingSCIMTokenCreatedAt, &scimSince)

	var google types.GoogleSyncSettings
	_, _ = st.GetSettingJSON(types.SettingGoogleSync, &google)

	hasSA := google.ServiceAccountJSON != ""
	google.ServiceAccountJSON = ""

	var googleStatus types.GoogleSyncStatus
	_, _ = st.GetSettingJSON(types.SettingGoogleSyncStatus, &googleStatus)

	adminJSON(w, http.StatusOK, map[string]any{
		"sso": map[string]any{
			"issuer":         oidc.Issuer,
			"clientId":       oidc.ClientID,
			"redirectUrls":   []string{oidcCallbackURL(aa.h.cfg.ServerURL), aa.oauth2.RedirectURL},
			"allowedDomains": aa.loginRule(types.SettingSSOAllowedDomains, oidc.AllowedDomains),
			"allowedUsers":   aa.loginRule(types.SettingSSOAllowedUsers, oidc.AllowedUsers),
			"allowedGroups":  aa.loginRule(types.SettingSSOAllowedGroups, oidc.AllowedGroups),
		},
		"roles": map[string]any{
			"adminUsers":   aa.roleList(types.SettingAdminUsers, aa.h.cfg.Admin.Users),
			"adminGroups":  aa.roleList(types.SettingAdminGroups, aa.h.cfg.Admin.Groups),
			"viewerUsers":  aa.roleList(types.SettingViewerUsers, nil),
			"viewerGroups": aa.roleList(types.SettingViewerGroups, nil),
		},
		"deviceRestriction": st.DeviceRestrictionEnabled(),
		"scim": map[string]any{
			"enabled":   scimSince != "",
			"createdAt": scimSince,
			"baseUrl":   strings.TrimSuffix(aa.h.cfg.ServerURL, "/") + "/scim/v2",
		},
		"googleSync": map[string]any{
			"settings":          google,
			"hasServiceAccount": hasSA,
			"status":            googleStatus,
		},
	})
}

// normaliseList trims, drops empty entries and duplicates, and lowercases
// when fold is set (emails and domains).
func normaliseList(in []string, fold bool) []string {
	out := make([]string, 0, len(in))

	for _, s := range in {
		s = strings.TrimSpace(s)
		if fold {
			s = strings.ToLower(s)
		}

		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}

	return out
}

// handlePutLoginRules stores the login rules. A rule sent as null falls
// back to the configuration file; an empty list admits anyone.
func (aa *adminAuth) handlePutLoginRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowedDomains *[]string `json:"allowedDomains"`
		AllowedUsers   *[]string `json:"allowedUsers"`
		AllowedGroups  *[]string `json:"allowedGroups"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	sess := sessionFrom(r)

	set := func(key string, v *[]string, fold bool) error {
		if v == nil {
			return aa.h.state.DeleteSetting(key)
		}

		list := normaliseList(*v, fold)
		for _, item := range list {
			if strings.ContainsAny(item, " \t,;") {
				return fmt.Errorf("%q is not a valid entry", item)
			}
		}

		return aa.h.state.SetSettingJSON(key, list, sess.Email)
	}

	for _, s := range []struct {
		key  string
		v    *[]string
		fold bool
	}{
		{types.SettingSSOAllowedDomains, req.AllowedDomains, true},
		{types.SettingSSOAllowedUsers, req.AllowedUsers, true},
		{types.SettingSSOAllowedGroups, req.AllowedGroups, false},
	} {
		err := set(s.key, s.v, s.fold)
		if err != nil {
			adminJSONError(w, http.StatusBadRequest, err.Error())

			return
		}
	}

	b, _ := json.Marshal(req)
	aa.auditReq(r, "settings.login_rules.update", "", string(b))
	aa.handleGetSettings(w, r)
}

// handlePutRoles stores the console-managed admin and viewer lists. An
// admin cannot remove their own admin role, so the console is never locked.
func (aa *adminAuth) handlePutRoles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AdminUsers   []string `json:"adminUsers"`
		AdminGroups  []string `json:"adminGroups"`
		ViewerUsers  []string `json:"viewerUsers"`
		ViewerGroups []string `json:"viewerGroups"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	sess := sessionFrom(r)
	adminUsers := normaliseList(req.AdminUsers, true)
	adminGroups := normaliseList(req.AdminGroups, false)

	cfgAdmin := slices.ContainsFunc(aa.h.cfg.Admin.Users, func(u string) bool { return strings.EqualFold(u, sess.Email) })
	if !cfgAdmin && !slices.Contains(adminUsers, strings.ToLower(sess.Email)) {
		var prev []string
		_, _ = aa.h.state.GetSettingJSON(types.SettingAdminUsers, &prev)

		if slices.ContainsFunc(prev, func(u string) bool { return strings.EqualFold(u, sess.Email) }) {
			adminJSONError(w, http.StatusBadRequest,
				"you cannot remove yourself from the admins; ask another admin to do it")

			return
		}
	}

	for key, v := range map[string][]string{
		types.SettingAdminUsers:   adminUsers,
		types.SettingAdminGroups:  adminGroups,
		types.SettingViewerUsers:  normaliseList(req.ViewerUsers, true),
		types.SettingViewerGroups: normaliseList(req.ViewerGroups, false),
	} {
		err := aa.h.state.SetSettingJSON(key, v, sess.Email)
		if err != nil {
			adminJSONError(w, http.StatusInternalServerError, err.Error())

			return
		}
	}

	b, _ := json.Marshal(req)
	aa.auditReq(r, "settings.roles.update", "", string(b))
	aa.handleGetSettings(w, r)
}

func (aa *adminAuth) handlePutDeviceRestriction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	changes, err := aa.h.state.SetDeviceRestriction(req.Enabled, sessionFrom(r).Email)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "the policy did not compile with device restriction: "+err.Error())

		return
	}

	aa.h.Change(changes...)
	aa.auditReq(r, "settings.device_restriction.update", "", strconv.FormatBool(req.Enabled))
	adminJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
}

// SCIM tokens are shown once; only their SHA-256 is stored.
func (aa *adminAuth) handleCreateSCIMToken(w http.ResponseWriter, r *http.Request) {
	token := "oskey-scim-" + rands.HexString(48)
	sum := sha256.Sum256([]byte(token))
	now := time.Now().UTC().Format(time.RFC3339)
	by := sessionFrom(r).Email

	err := aa.h.state.SetSettingJSON(types.SettingSCIMTokenHash, hex.EncodeToString(sum[:]), by)
	if err == nil {
		err = aa.h.state.SetSettingJSON(types.SettingSCIMTokenCreatedAt, now, by)
	}

	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.auditReq(r, "scim.token.create", "", "")
	adminJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"createdAt": now,
		"baseUrl":   strings.TrimSuffix(aa.h.cfg.ServerURL, "/") + "/scim/v2",
	})
}

func (aa *adminAuth) handleDeleteSCIMToken(w http.ResponseWriter, r *http.Request) {
	_ = aa.h.state.DeleteSetting(types.SettingSCIMTokenHash)
	_ = aa.h.state.DeleteSetting(types.SettingSCIMTokenCreatedAt)

	aa.auditReq(r, "scim.token.delete", "", "")
	w.WriteHeader(http.StatusNoContent)
}

func (aa *adminAuth) handlePutGoogleSync(w http.ResponseWriter, r *http.Request) {
	var req types.GoogleSyncSettings
	if !decodeJSON(w, r, &req) {
		return
	}

	var prev types.GoogleSyncSettings
	_, _ = aa.h.state.GetSettingJSON(types.SettingGoogleSync, &prev)

	// An empty service account keeps the stored one: the console never
	// reads it back.
	if req.ServiceAccountJSON == "" {
		req.ServiceAccountJSON = prev.ServiceAccountJSON
	} else {
		_, err := parseServiceAccount([]byte(req.ServiceAccountJSON))
		if err != nil {
			adminJSONError(w, http.StatusBadRequest, err.Error())

			return
		}
	}

	req.AdminEmail = strings.TrimSpace(req.AdminEmail)
	req.Customer = cmp.Or(strings.TrimSpace(req.Customer), "my_customer")
	req.IntervalMinutes = min(max(req.IntervalMinutes, 5), 24*60)

	if req.Enabled && (req.AdminEmail == "" || req.ServiceAccountJSON == "") {
		adminJSONError(w, http.StatusBadRequest, "Google sync needs a service account key and an admin email to impersonate")

		return
	}

	err := aa.h.state.SetSettingJSON(types.SettingGoogleSync, req, sessionFrom(r).Email)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.h.googleSync.Kick()
	aa.auditReq(r, "google_sync.update", "", fmt.Sprintf("enabled=%t admin=%s customer=%s interval=%dm",
		req.Enabled, req.AdminEmail, req.Customer, req.IntervalMinutes))
	aa.handleGetSettings(w, r)
}

func (aa *adminAuth) handleRunGoogleSync(w http.ResponseWriter, r *http.Request) {
	status := aa.h.googleSync.RunNow(r.Context())

	aa.auditReq(r, "google_sync.run", "", fmt.Sprintf("ok=%t users=%d groups=%d disabled=%d", status.OK, status.Users, status.Groups, status.Disabled))
	adminJSON(w, http.StatusOK, status)
}

type adminUser struct {
	ID          uint       `json:"id"`
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName"`
	Email       string     `json:"email"`
	Provider    string     `json:"provider"`
	Picture     string     `json:"picture"`
	CreatedAt   time.Time  `json:"createdAt"`
	Disabled    bool       `json:"disabled"`
	DisabledAt  *time.Time `json:"disabledAt,omitempty"`
	DisabledBy  string     `json:"disabledBy,omitempty"`
	Groups      []string   `json:"groups"`
	Devices     int        `json:"devices"`
	LastSeen    *time.Time `json:"lastSeen,omitempty"`
}

func (aa *adminAuth) handleListUsers(w http.ResponseWriter, r *http.Request) {
	st := aa.h.state

	users, err := st.ListAllUsers()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	disabled, err := st.ListDisabledUsers()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	groups, err := st.ListDirectoryGroups()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	userGroups := make(map[types.UserID][]string)

	for _, g := range groups {
		for _, uid := range g.MemberIDs {
			userGroups[uid] = append(userGroups[uid], g.Name)
		}
	}

	out := make([]adminUser, 0, len(users))

	for _, u := range users {
		uid := types.UserID(u.ID)
		au := adminUser{
			ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Email: u.Email,
			Provider: cmp.Or(u.Provider, "local"), Picture: u.ProfilePicURL, CreatedAt: u.CreatedAt,
			Groups: nonNil(userGroups[uid]),
		}

		if a, ok := disabled[uid]; ok {
			au.Disabled, au.DisabledAt, au.DisabledBy = true, a.DisabledAt, a.DisabledBy
		}

		for _, n := range st.ListNodesByUser(uid).All() {
			au.Devices++

			if ls := n.LastSeen(); ls.Valid() {
				t := ls.Get()
				if au.LastSeen == nil || t.After(*au.LastSeen) {
					au.LastSeen = &t
				}
			}
		}

		out = append(out, au)
	}

	adminJSON(w, http.StatusOK, out)
}

func userIDParam(r *http.Request) (types.UserID, error) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)

	return types.UserID(id), err
}

func (aa *adminAuth) userLabel(uid types.UserID) string {
	u, err := aa.h.state.GetUserByID(uid)
	if err != nil {
		return strconv.FormatUint(uint64(uid), 10)
	}

	return cmp.Or(u.Email, u.Name)
}

func (aa *adminAuth) handleDisableUser(w http.ResponseWriter, r *http.Request) {
	aa.setUserDisabled(w, r, true)
}

func (aa *adminAuth) handleEnableUser(w http.ResponseWriter, r *http.Request) {
	aa.setUserDisabled(w, r, false)
}

func (aa *adminAuth) setUserDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	uid, err := userIDParam(r)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid user id")

		return
	}

	label := aa.userLabel(uid)
	sess := sessionFrom(r)

	if disabled && sess.UserID != 0 && types.UserID(sess.UserID) == uid {
		adminJSONError(w, http.StatusBadRequest, "you cannot disable yourself")

		return
	}

	changes, err := aa.h.state.SetUserDisabled(uid, disabled, sess.Email)
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, statusForError(err), err.Error())

		return
	}

	if disabled {
		aa.endSessionsOf(label)
		aa.auditReq(r, "user.disable", label, "devices deleted, keys expired")
	} else {
		aa.auditReq(r, "user.enable", label, "")
	}

	w.WriteHeader(http.StatusNoContent)
}

func (aa *adminAuth) handleRemoveUser(w http.ResponseWriter, r *http.Request) {
	uid, err := userIDParam(r)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid user id")

		return
	}

	sess := sessionFrom(r)
	if sess.UserID != 0 && types.UserID(sess.UserID) == uid {
		adminJSONError(w, http.StatusBadRequest, "you cannot remove yourself")

		return
	}

	label := aa.userLabel(uid)

	changes, err := aa.h.state.RemoveUser(uid)
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, statusForError(err), err.Error())

		return
	}

	aa.endSessionsOf(label)
	aa.auditReq(r, "user.remove", label, "user, devices and keys deleted")
	w.WriteHeader(http.StatusNoContent)
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, hsdb.ErrUserNotFound), errors.Is(err, hsdb.ErrNodeNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

type adminGroup struct {
	ID          uint64   `json:"id,omitempty"`
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Source      string   `json:"source"`
	Members     []string `json:"members"`
	MemberIDs   []uint64 `json:"memberIds"`
}

// handleListGroups lists directory groups and the groups the policy defines.
func (aa *adminAuth) handleListGroups(w http.ResponseWriter, r *http.Request) {
	st := aa.h.state

	users, err := st.ListAllUsers()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	label := make(map[types.UserID]string, len(users))
	for _, u := range users {
		label[types.UserID(u.ID)] = cmp.Or(u.Email, u.Name)
	}

	groups, err := st.ListDirectoryGroups()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	out := make([]adminGroup, 0, len(groups))

	for _, g := range groups {
		ag := adminGroup{
			ID: g.ID, Name: g.Name, DisplayName: g.DisplayName, Source: g.Source,
			Members: []string{}, MemberIDs: []uint64{},
		}

		for _, uid := range g.MemberIDs {
			ag.Members = append(ag.Members, label[uid])
			ag.MemberIDs = append(ag.MemberIDs, uint64(uid))
		}

		out = append(out, ag)
	}

	raw, err := st.RawPolicy()
	if err == nil {
		for name, members := range policyGroups(raw) {
			out = append(out, adminGroup{
				Name: strings.TrimPrefix(name, "group:"), DisplayName: name, Source: "policy",
				Members: nonNil(members), MemberIDs: []uint64{},
			})
		}
	}

	slices.SortFunc(out, func(a, b adminGroup) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Source, b.Source))
	})

	adminJSON(w, http.StatusOK, out)
}

// policyGroups returns the groups the hand-written policy defines.
func policyGroups(raw []byte) map[string][]string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	std, err := hujson.Standardize(bytes.Clone(raw))
	if err != nil {
		return nil
	}

	var doc struct {
		Groups map[string][]string `json:"groups"`
	}

	if json.Unmarshal(std, &doc) != nil {
		return nil
	}

	return doc.Groups
}

type adminDeviceGrant struct {
	Kind      string `json:"kind"`
	Principal string `json:"principal"`
	Label     string `json:"label"`
}

type adminDevice struct {
	ID              uint64             `json:"id"`
	Name            string             `json:"name"`
	Hostname        string             `json:"hostname"`
	OS              string             `json:"os"`
	ClientVersion   string             `json:"clientVersion"`
	Owner           string             `json:"owner"`
	OwnerID         uint               `json:"ownerId,omitempty"`
	Tags            []string           `json:"tags"`
	IPs             []string           `json:"ips"`
	Online          bool               `json:"online"`
	LastSeen        *time.Time         `json:"lastSeen,omitempty"`
	Expiry          *time.Time         `json:"expiry,omitempty"`
	Expired         bool               `json:"expired"`
	CreatedAt       time.Time          `json:"createdAt"`
	AnnouncedRoutes []string           `json:"announcedRoutes"`
	ApprovedRoutes  []string           `json:"approvedRoutes"`
	Access          []adminDeviceGrant `json:"access"`
}

func (aa *adminAuth) handleListDevices(w http.ResponseWriter, r *http.Request) {
	st := aa.h.state

	grants, err := st.ListDeviceAccess()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	users, err := st.ListAllUsers()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	userLabel := make(map[string]string, len(users))
	for _, u := range users {
		userLabel[strconv.FormatUint(uint64(u.ID), 10)] = cmp.Or(u.Email, u.Name)
	}

	groups, err := st.ListDirectoryGroups()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	groupLabel := make(map[string]string, len(groups))
	for _, g := range groups {
		groupLabel[strconv.FormatUint(g.ID, 10)] = "group:" + g.Name
	}

	byNode := make(map[types.NodeID][]adminDeviceGrant)

	for _, g := range grants {
		label := g.Principal

		switch g.PrincipalKind {
		case types.PrincipalUser:
			label = cmp.Or(userLabel[g.Principal], "deleted user "+g.Principal)
		case types.PrincipalDirectoryGroup:
			label = cmp.Or(groupLabel[g.Principal], "deleted group "+g.Principal)
		case types.PrincipalPolicyGroup:
			label = "group:" + strings.TrimPrefix(g.Principal, "group:")
		}

		byNode[g.NodeID] = append(byNode[g.NodeID], adminDeviceGrant{Kind: g.PrincipalKind, Principal: g.Principal, Label: label})
	}

	nodes := st.ListNodes()
	out := make([]adminDevice, 0, nodes.Len())

	for _, n := range nodes.All() {
		d := adminDevice{
			ID: uint64(n.ID()), Name: n.GivenName(), Hostname: n.Hostname(),
			Tags: nonNil(n.Tags().AsSlice()), IPs: nonNil(n.IPsAsString()),
			Expired: n.IsExpired(), CreatedAt: n.CreatedAt(),
			AnnouncedRoutes: []string{}, ApprovedRoutes: []string{},
			Access: byNode[n.ID()],
		}

		if d.Access == nil {
			d.Access = []adminDeviceGrant{}
		}

		if hi := n.Hostinfo(); hi.Valid() {
			d.OS, d.ClientVersion = hi.OS(), hi.IPNVersion()
		}

		if n.IsTagged() {
			d.Owner = "tagged"
		} else if u := n.User(); u.Valid() {
			d.Owner, d.OwnerID = cmp.Or(u.Email(), u.Name()), u.ID()
		}

		if o := n.IsOnline(); o.Valid() {
			d.Online = o.Get()
		}

		if ls := n.LastSeen(); ls.Valid() {
			t := ls.Get()
			d.LastSeen = &t
		}

		if e := n.Expiry(); e.Valid() {
			t := e.Get()
			d.Expiry = &t
		}

		for _, p := range n.AnnouncedRoutes() {
			d.AnnouncedRoutes = append(d.AnnouncedRoutes, p.String())
		}

		for _, p := range n.ApprovedRoutes().All() {
			d.ApprovedRoutes = append(d.ApprovedRoutes, p.String())
		}

		out = append(out, d)
	}

	adminJSON(w, http.StatusOK, out)
}

func (aa *adminAuth) handlePutDeviceAccess(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid device id")

		return
	}

	var req struct {
		Access []struct {
			Kind      string `json:"kind"`
			Principal string `json:"principal"`
		} `json:"access"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	grants := make([]types.DeviceAccess, 0, len(req.Access))
	labels := make([]string, 0, len(req.Access))

	for _, a := range req.Access {
		switch a.Kind {
		case types.PrincipalUser:
			uid, err := strconv.ParseUint(a.Principal, 10, 64)
			if err != nil {
				adminJSONError(w, http.StatusBadRequest, "invalid user id "+a.Principal)

				return
			}

			if _, err := aa.h.state.GetUserByID(types.UserID(uid)); err != nil {
				adminJSONError(w, http.StatusBadRequest, "no user "+a.Principal)

				return
			}

			labels = append(labels, aa.userLabel(types.UserID(uid)))
		case types.PrincipalDirectoryGroup:
			if _, err := strconv.ParseUint(a.Principal, 10, 64); err != nil {
				adminJSONError(w, http.StatusBadRequest, "invalid group id "+a.Principal)

				return
			}

			labels = append(labels, "directory group "+a.Principal)
		case types.PrincipalPolicyGroup:
			if strings.TrimSpace(strings.TrimPrefix(a.Principal, "group:")) == "" {
				adminJSONError(w, http.StatusBadRequest, "empty group name")

				return
			}

			labels = append(labels, "group:"+strings.TrimPrefix(a.Principal, "group:"))
		default:
			adminJSONError(w, http.StatusBadRequest, "unknown principal kind "+a.Kind)

			return
		}

		grants = append(grants, types.DeviceAccess{PrincipalKind: a.Kind, Principal: strings.TrimPrefix(a.Principal, "group:")})
	}

	changes, err := aa.h.state.SetDeviceAccess(types.NodeID(id), grants)
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, statusForError(err), err.Error())

		return
	}

	name := strconv.FormatUint(id, 10)
	if n, ok := aa.h.state.GetNodeByID(types.NodeID(id)); ok {
		name = n.GivenName()
	}

	aa.auditReq(r, "device.access.update", name, strings.Join(labels, ", "))
	w.WriteHeader(http.StatusNoContent)
}

func (aa *adminAuth) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	raw, err := aa.h.state.RawPolicy()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	effective, err := aa.h.state.EffectivePolicy()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	var pretty bytes.Buffer
	if len(effective) > 0 && json.Indent(&pretty, effective, "", "  ") == nil {
		effective = pretty.Bytes()
	}

	adminJSON(w, http.StatusOK, map[string]any{
		"policy":           string(raw),
		"effective":        string(effective),
		"mode":             aa.h.cfg.Policy.Mode,
		"editable":         aa.h.cfg.Policy.Mode == types.PolicyModeDB,
		"allowsEverything": managed.AllowsEverything(raw),
	})
}

func (aa *adminAuth) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	limit = min(max(limit, 1), 500)

	if r.URL.Query().Get("limit") == "" {
		limit = 100
	}

	before, _ := strconv.ParseUint(r.URL.Query().Get("before"), 10, 64)

	evs, err := aa.h.state.ListAuditEvents(limit, before)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	if evs == nil {
		evs = []types.AuditEvent{}
	}

	adminJSON(w, http.StatusOK, evs)
}
