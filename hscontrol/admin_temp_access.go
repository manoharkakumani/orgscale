package hscontrol

import (
	"cmp"
	"context"
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
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/rs/zerolog/log"
)

// Temporary access: an administrator gives people or directory groups access
// to a server tag for a set time. The grant is part of the policy until it
// lapses, and the expiry loop takes it out again. Only administrators use
// this; nobody else can sign in to the console.

// tempAccessMaxMinutes caps a grant: one week.
const tempAccessMaxMinutes = 7 * 24 * 60

func (aa *adminAuth) tempAccessRoutes(r chi.Router) {
	r.Get("/temp-access", aa.handleListTempAccess)
	r.Post("/temp-access", aa.handleGrantTempAccess)
	r.Post("/temp-access/{id}/end", aa.handleEndTempAccess)
}

type adminTempAccess struct {
	ID        uint64     `json:"id"`
	Grantee   string     `json:"grantee"`
	Kind      string     `json:"kind"` // "user" or "directory_group"
	Tag       string     `json:"tag"`  // the tag, or the server's name
	Server    bool       `json:"server"`
	Resource  bool       `json:"resource"`
	Reason    string     `json:"reason"`
	Minutes   int        `json:"minutes"`
	SSH       bool       `json:"ssh"`
	SSHUsers  []string   `json:"sshUsers"`
	Status    string     `json:"status"` // "active", "ended" or "expired"
	Active    bool       `json:"active"`
	GrantedBy string     `json:"grantedBy"`
	GrantedAt time.Time  `json:"grantedAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	EndedBy   string     `json:"endedBy,omitempty"`
}

// sshUsersOf returns the Linux accounts temporary access to t may SSH in as,
// never nil.
func sshUsersOf(t types.ServerTag) []string {
	var users []string

	_ = json.Unmarshal([]byte(t.RequestSSHUsers), &users)

	if users == nil {
		users = []string{}
	}

	return users
}

func (aa *adminAuth) handleListTempAccess(w http.ResponseWriter, _ *http.Request) {
	rows, err := aa.h.state.ListAccessRequests(0, 500)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	tags, err := aa.h.state.ListServerTags()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	byName := make(map[string]types.ServerTag, len(tags))
	for _, t := range tags {
		byName[t.Name] = t
	}

	resourceNames := map[string]string{}

	rs, err := aa.h.state.ListNetworkResources()
	if err == nil {
		for _, r := range rs {
			resourceNames[strconv.FormatUint(r.ID, 10)] = r.Name
		}
	}

	now := time.Now()
	out := []adminTempAccess{}

	for _, r := range rows {
		// Only grants; anything else is from the earlier request feature.
		if r.GrantKind == "" || r.Status == types.AccessRequestPending {
			continue
		}

		v := adminTempAccess{
			ID: r.ID, Grantee: r.Requester, Kind: cmp.Or(r.GrantKind, types.PrincipalUser), Tag: r.Tag,
			Reason: r.Reason, Minutes: r.Minutes, SSH: r.SSH, SSHUsers: []string{},
			Active: r.Active(now), GrantedBy: r.DecidedBy, GrantedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		}

		switch {
		case v.Active:
			v.Status = "active"
		case r.Status == types.AccessRequestRevoked:
			v.Status, v.EndedBy = "ended", r.DecisionNote
		default:
			v.Status = "expired"
		}

		if id, ok := r.AccessResourceTarget(); ok {
			v.Resource, v.Tag = true, cmp.Or(resourceNames[id], "a removed resource")
		}

		if id, ok := r.AccessNodeTarget(); ok {
			v.Server, v.Tag = true, "a removed server"

			n, err := strconv.ParseUint(id, 10, 64)
			if err == nil {
				if nv, ok := aa.h.state.GetNodeByID(types.NodeID(n)); ok {
					v.Tag = nv.GivenName()
				}
			}
		}

		if r.SSH {
			v.SSHUsers = sshUsersOf(byName[r.Tag])
		}

		out = append(out, v)
	}

	adminJSON(w, http.StatusOK, out)
}

// handleGrantTempAccess gives people or directory groups access to a tag for
// a set time. A group's grant covers whoever is in it while it lasts.
func (aa *adminAuth) handleGrantTempAccess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To       []adminPrincipal `json:"to"`
		Tag      string           `json:"tag"`
		Server   uint64           `json:"server"`   // one server instead of a tag
		Resource uint64           `json:"resource"` // or one private resource
		Minutes  int              `json:"minutes"`
		Reason   string           `json:"reason"`
		SSH      bool             `json:"ssh"`
	}

	if !decodeJSON(w, r, &body) {
		return
	}

	if len(body.To) == 0 || len(body.To) > 50 {
		adminJSONError(w, http.StatusBadRequest, "choose between 1 and 50 people or groups")

		return
	}

	if body.Minutes < 15 || body.Minutes > tempAccessMaxMinutes {
		adminJSONError(w, http.StatusBadRequest, "access can last from 15 minutes to 7 days")

		return
	}

	reason := strings.TrimSpace(truncateString(body.Reason, 500))

	target, targetLabel, err := aa.grantTarget(body.Tag, body.Server, body.Resource, body.SSH)
	if err != nil {
		code := http.StatusBadRequest
		if _, ok := errors.AsType[userError](err); !ok {
			code = http.StatusInternalServerError
		}

		adminJSONError(w, code, err.Error())

		return
	}

	// Network access only matters while devices are restricted.
	if !body.SSH && !aa.h.state.DeviceRestrictionEnabled() {
		adminJSONError(w, http.StatusBadRequest, "access control is off, so everyone already reaches every server; turn it on under Access first")

		return
	}

	ps, err := aa.parsePrincipals(body.To, types.PrincipalUser, types.PrincipalDirectoryGroup)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	label, err := aa.principalLabeler()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	// Groups are shown by their display name, e.g. "Development".
	groupNames := map[string]string{}

	groups, err := aa.h.state.ListDirectoryGroups()
	if err == nil {
		for _, g := range groups {
			groupNames[strconv.FormatUint(g.ID, 10)] = cmp.Or(g.DisplayName, g.Name)
		}
	}

	sess := sessionFrom(r)
	now := time.Now().UTC()
	exp := now.Add(time.Duration(body.Minutes) * time.Minute)

	var (
		ids    []uint64
		labels []string
	)

	for _, p := range ps {
		var uid uint

		if p.Kind == types.PrincipalUser {
			n, _ := strconv.ParseUint(p.Principal, 10, 64)
			uid = uint(n)
		}

		who := label(p.Kind, p.Principal)
		if n, ok := groupNames[p.Principal]; ok && p.Kind == types.PrincipalDirectoryGroup {
			who = n
		}

		id, err := aa.h.state.CreateAccessRequest(types.AccessRequest{
			UserID: uid, Requester: who, Tag: target, Reason: reason,
			Minutes: body.Minutes, SSH: body.SSH, Status: types.AccessRequestPending, CreatedAt: now,
			GrantKind: p.Kind, GrantPrincipal: p.Principal,
		})
		if err != nil {
			adminJSONError(w, http.StatusInternalServerError, err.Error())

			return
		}

		changes, err := aa.h.state.DecideAccessRequest(types.AccessRequest{
			ID: id, Minutes: body.Minutes, Status: types.AccessRequestApproved,
			DecidedBy: sess.label(), DecidedAt: &now, ExpiresAt: &exp,
		}, types.AccessRequestPending)
		aa.h.Change(changes...)

		if err != nil {
			adminJSONError(w, http.StatusBadRequest, err.Error())

			return
		}

		ids = append(ids, id)
		labels = append(labels, who)
	}

	detail := fmt.Sprintf("%s → %s for %s", strings.Join(labels, ", "), targetLabel, time.Duration(body.Minutes)*time.Minute)
	if body.SSH {
		detail += ", with SSH"
	}

	if reason != "" {
		detail += ": " + reason
	}

	aa.auditReq(r, "temp_access.grant", targetLabel, detail)
	adminJSON(w, http.StatusCreated, map[string][]uint64{"ids": ids})
}

// grantTarget checks what a grant names, one server, one private resource
// or a tag, and returns how it is stored (see [types.AccessRequest.Tag]) and
// how it is shown.
func (aa *adminAuth) grantTarget(tag string, server, resource uint64, ssh bool) (string, string, error) {
	switch {
	case server != 0:
		node, ok := aa.h.state.GetNodeByID(types.NodeID(server))
		if !ok || !node.IsTagged() {
			return "", "", userError("choose a server (a device that joined with a tag)")
		}

		if ssh {
			return "", "", userError("keyless SSH is given per tag, not per server; give this server its own tag to SSH into it alone")
		}

		return types.AccessNodePrefix + strconv.FormatUint(server, 10), node.GivenName(), nil
	case resource != 0:
		resources, err := aa.h.state.ListNetworkResources()
		if err != nil {
			return "", "", err
		}

		j := slices.IndexFunc(resources, func(r types.NetworkResource) bool { return r.ID == resource })
		if j < 0 {
			return "", "", userError("choose a private resource from the Networks page")
		}

		if ssh {
			return "", "", userError("keyless SSH works on devices running Tailscale, not on private resources")
		}

		return types.AccessResourcePrefix + strconv.FormatUint(resource, 10), resources[j].Name, nil
	}

	tags, err := aa.h.state.ListServerTags()
	if err != nil {
		return "", "", err
	}

	i := slices.IndexFunc(tags, func(t types.ServerTag) bool { return t.Name == tag })
	if i < 0 {
		return "", "", userError("choose a server tag made on the Servers page")
	}

	if ssh && len(sshUsersOf(tags[i])) == 0 {
		return "", "", userError("set SSH accounts on this tag first (Servers → Edit access)")
	}

	return tag, tag, nil
}

// handleEndTempAccess ends a grant before its time is up.
func (aa *adminAuth) handleEndTempAccess(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid id")

		return
	}

	g, err := aa.h.state.GetAccessRequest(id)
	if errors.Is(err, hsdb.ErrAccessRequestNotFound) {
		adminJSONError(w, http.StatusNotFound, "no such grant")

		return
	} else if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	now := time.Now().UTC()
	if !g.Active(now) {
		adminJSONError(w, http.StatusConflict, "this access has already ended")

		return
	}

	sess := sessionFrom(r)
	g.Status, g.ExpiresAt, g.DecisionNote = types.AccessRequestRevoked, &now, sess.label()

	changes, err := aa.h.state.DecideAccessRequest(g, types.AccessRequestApproved)
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, http.StatusConflict, "this access changed meanwhile; reload")

		return
	}

	target := g.Tag
	if id, ok := g.AccessNodeTarget(); ok {
		n, err := strconv.ParseUint(id, 10, 64)
		if err == nil {
			if nv, ok := aa.h.state.GetNodeByID(types.NodeID(n)); ok {
				target = nv.GivenName()
			}
		}
	}

	aa.auditReq(r, "temp_access.end", target, g.Requester+" → "+target)
	w.WriteHeader(http.StatusNoContent)
}

// runAccessRequestExpiry ends temporary access when its time is up.
func (h *Orgscale) runAccessRequestExpiry(ctx context.Context) {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		lapsed, changes, err := h.state.ExpireAccessRequests(time.Now(), 24*time.Hour)
		if err != nil {
			log.Warn().Err(err).Msg("temporary access: expiring")

			continue
		}

		h.Change(changes...)

		if lapsed > 0 {
			log.Info().Int64("count", lapsed).Msg("temporary access ended")

			if h.admin != nil {
				h.admin.audit(types.AuditEvent{
					Actor: "orgscale", ActorKind: types.AuditActorSystem,
					Action: "temp_access.expire", Detail: fmt.Sprintf("%d ended", lapsed),
				})
			}
		}
	}
}
