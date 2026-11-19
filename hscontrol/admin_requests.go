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

// Just-in-time access: an administrator lets people request a tag for up to
// a set time; a person asks (with a reason) from the console's member view;
// another administrator approves or denies; approved access is part of the
// policy until it lapses, and the expiry loop takes it out again.

const (
	// accessRequestMaxMinutes caps what a tag may allow: one week.
	accessRequestMaxMinutes = 7 * 24 * 60
	// accessRequestPendingTTL is how long a request waits for a decision.
	accessRequestPendingTTL = 24 * time.Hour
	// accessRequestOpenLimit is how many undecided or running requests one
	// person may have at once.
	accessRequestOpenLimit = 10
)

func (aa *adminAuth) requestRoutes(r chi.Router) {
	r.Get("/requests", aa.handleListRequests)
	r.Post("/requests/grant", aa.handleGrantAccess)
	r.Post("/requests/{id}/approve", aa.handleDecideRequest(types.AccessRequestApproved))
	r.Post("/requests/{id}/deny", aa.handleDecideRequest(types.AccessRequestDenied))
	r.Post("/requests/{id}/revoke", aa.handleDecideRequest(types.AccessRequestRevoked))

	r.Get("/my/targets", aa.handleMyTargets)
	r.Get("/my/requests", aa.handleMyRequests)
	r.Post("/my/requests", aa.handleCreateRequest)
	r.Post("/my/requests/{id}/cancel", aa.handleCancelRequest)
}

type adminRequest struct {
	ID           uint64     `json:"id"`
	UserID       uint       `json:"userId"`
	Requester    string     `json:"requester"`
	Tag          string     `json:"tag"`
	Reason       string     `json:"reason"`
	Minutes      int        `json:"minutes"`
	SSH          bool       `json:"ssh"`
	SSHUsers     []string   `json:"sshUsers,omitempty"`
	Status       string     `json:"status"`
	Active       bool       `json:"active"`
	CreatedAt    time.Time  `json:"createdAt"`
	DecidedBy    string     `json:"decidedBy,omitempty"`
	DecidedAt    *time.Time `json:"decidedAt,omitempty"`
	DecisionNote string     `json:"decisionNote,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	// Granted is set for access an administrator gave directly.
	Granted   bool   `json:"granted"`
	GrantKind string `json:"grantKind,omitempty"`
}

type requestPrincipal struct {
	Kind      string `json:"kind"`
	Principal string `json:"principal"`
}

func requestPrincipalsOf(t types.ServerTag) []requestPrincipal {
	var ps []requestPrincipal
	_ = json.Unmarshal([]byte(t.RequestPrincipals), &ps)

	return ps
}

// mayRequest reports whether user uid may see and request tag t: requests
// are on, and the user is listed, in a listed directory group, or everyone
// is allowed.
func (aa *adminAuth) mayRequest(t types.ServerTag, uid uint) bool {
	if t.RequestMaxMinutes <= 0 || uid == 0 {
		return false
	}

	var groups []types.DirectoryGroup

	for _, p := range requestPrincipalsOf(t) {
		switch p.Kind {
		case types.PrincipalEveryone:
			return true
		case types.PrincipalUser:
			if p.Principal == strconv.FormatUint(uint64(uid), 10) {
				return true
			}
		case types.PrincipalDirectoryGroup:
			if groups == nil {
				var err error

				groups, err = aa.h.state.ListDirectoryGroups()
				if err != nil {
					return false
				}
			}

			for _, g := range groups {
				if strconv.FormatUint(g.ID, 10) == p.Principal && slices.Contains(g.MemberIDs, types.UserID(uid)) {
					return true
				}
			}
		}
	}

	return false
}

func sshUsersOf(t types.ServerTag) []string {
	var users []string
	_ = json.Unmarshal([]byte(t.RequestSSHUsers), &users)

	return users
}

func (aa *adminAuth) requestViews(rows []types.AccessRequest) ([]adminRequest, error) {
	tags, err := aa.h.state.ListServerTags()
	if err != nil {
		return nil, err
	}

	byName := make(map[string]types.ServerTag, len(tags))
	for _, t := range tags {
		byName[t.Name] = t
	}

	now := time.Now()
	out := make([]adminRequest, 0, len(rows))

	for _, r := range rows {
		v := adminRequest{
			ID: r.ID, UserID: r.UserID, Requester: r.Requester, Tag: r.Tag, Reason: r.Reason,
			Minutes: r.Minutes, SSH: r.SSH, Status: r.Status, Active: r.Active(now),
			CreatedAt: r.CreatedAt, DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt,
			DecisionNote: r.DecisionNote, ExpiresAt: r.ExpiresAt,
			Granted: r.GrantKind != "", GrantKind: r.GrantKind,
		}
		if r.SSH {
			v.SSHUsers = sshUsersOf(byName[r.Tag])
		}

		out = append(out, v)
	}

	return out, nil
}

func (aa *adminAuth) handleListRequests(w http.ResponseWriter, _ *http.Request) {
	rows, err := aa.h.state.ListAccessRequests(0, 500)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	out, err := aa.requestViews(rows)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	adminJSON(w, http.StatusOK, out)
}

func requestIDFromURL(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid request id")

		return 0, false
	}

	return id, true
}

// handleDecideRequest approves, denies or revokes a request.
func (aa *adminAuth) handleDecideRequest(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requestIDFromURL(w, r)
		if !ok {
			return
		}

		var body struct {
			Note    string `json:"note"`
			Minutes int    `json:"minutes"` // approve: shorten the time asked for
		}

		if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
			return
		}

		sess := sessionFrom(r)

		req, err := aa.h.state.GetAccessRequest(id)
		if errors.Is(err, hsdb.ErrAccessRequestNotFound) {
			adminJSONError(w, http.StatusNotFound, "no such request")

			return
		} else if err != nil {
			adminJSONError(w, http.StatusInternalServerError, err.Error())

			return
		}

		// Nobody approves their own access.
		if to == types.AccessRequestApproved && (req.UserID == sess.UserID || strings.EqualFold(req.Requester, sess.roleEmail)) {
			adminJSONError(w, http.StatusForbidden, "you cannot approve your own request; another administrator has to")

			return
		}

		now := time.Now().UTC()
		from := []string{types.AccessRequestPending}

		switch to {
		case types.AccessRequestApproved:
			minutes := req.Minutes
			if body.Minutes > 0 && body.Minutes < minutes {
				minutes = body.Minutes
			}

			exp := now.Add(time.Duration(minutes) * time.Minute)
			req.Minutes, req.ExpiresAt = minutes, &exp
		case types.AccessRequestRevoked:
			if !req.Active(now) {
				adminJSONError(w, http.StatusConflict, "this access is not active")

				return
			}

			from = []string{types.AccessRequestApproved}
			req.ExpiresAt = &now
		}

		req.Status, req.DecidedBy, req.DecidedAt = to, sess.label(), &now
		req.DecisionNote = strings.TrimSpace(truncateString(body.Note, 500))

		changes, err := aa.h.state.DecideAccessRequest(req, from...)
		aa.h.Change(changes...)

		if errors.Is(err, hsdb.ErrAccessRequestNotFound) {
			adminJSONError(w, http.StatusConflict, "this request was already decided")

			return
		} else if err != nil {
			adminJSONError(w, http.StatusBadRequest, err.Error())

			return
		}

		detail := fmt.Sprintf("%s → %s", req.Requester, req.Tag)
		if to == types.AccessRequestApproved {
			detail += fmt.Sprintf(" for %s", time.Duration(req.Minutes)*time.Minute)
		}

		if req.DecisionNote != "" {
			detail += ": " + req.DecisionNote
		}

		aa.auditReq(r, "access_request."+map[string]string{
			types.AccessRequestApproved: "approve", types.AccessRequestDenied: "deny", types.AccessRequestRevoked: "revoke",
		}[to], fmt.Sprintf("#%d", id), detail)

		w.WriteHeader(http.StatusNoContent)
	}
}

// handleGrantAccess gives people or directory groups temporary access to a
// tag directly, without a request. The tag need not allow requests.
func (aa *adminAuth) handleGrantAccess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To      []adminPrincipal `json:"to"`
		Tag     string           `json:"tag"`
		Minutes int              `json:"minutes"`
		Reason  string           `json:"reason"`
		SSH     bool             `json:"ssh"`
	}

	if !decodeJSON(w, r, &body) {
		return
	}

	if len(body.To) == 0 || len(body.To) > 50 {
		adminJSONError(w, http.StatusBadRequest, "choose between 1 and 50 people or groups")

		return
	}

	if body.Minutes < 15 || body.Minutes > accessRequestMaxMinutes {
		adminJSONError(w, http.StatusBadRequest, "access can last from 15 minutes to 7 days")

		return
	}

	reason := strings.TrimSpace(truncateString(body.Reason, 500))

	tags, err := aa.h.state.ListServerTags()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	i := slices.IndexFunc(tags, func(t types.ServerTag) bool { return t.Name == body.Tag })
	if i < 0 {
		adminJSONError(w, http.StatusBadRequest, "choose a server tag made on the Servers page")

		return
	}

	if body.SSH && len(sshUsersOf(tags[i])) == 0 {
		adminJSONError(w, http.StatusBadRequest, "set SSH accounts on this tag first (Servers → edit tag)")

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

		id, err := aa.h.state.CreateAccessRequest(types.AccessRequest{
			UserID: uid, Requester: who, Tag: body.Tag, Reason: cmp.Or(reason, "granted by an administrator"),
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

	aa.auditReq(r, "access_request.grant", body.Tag, fmt.Sprintf("%s for %s%s", strings.Join(labels, ", "),
		time.Duration(body.Minutes)*time.Minute, map[bool]string{true: ": " + reason, false: ""}[reason != ""]))

	adminJSON(w, http.StatusCreated, map[string][]uint64{"ids": ids})
}

// adminRequestTarget is a tag as someone who may request it sees it: no
// server names, counts or addresses.
type adminRequestTarget struct {
	Tag        string   `json:"tag"`
	Desc       string   `json:"description"`
	MaxMinutes int      `json:"maxMinutes"`
	SSHUsers   []string `json:"sshUsers"`
}

func (aa *adminAuth) handleMyTargets(w http.ResponseWriter, r *http.Request) {
	tags, err := aa.h.state.ListServerTags()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	uid := sessionFrom(r).UserID
	out := []adminRequestTarget{}

	for _, t := range tags {
		if !aa.mayRequest(t, uid) {
			continue
		}

		out = append(out, adminRequestTarget{
			Tag: t.Name, Desc: t.Description, MaxMinutes: t.RequestMaxMinutes,
			SSHUsers: append([]string{}, sshUsersOf(t)...),
		})
	}

	adminJSON(w, http.StatusOK, out)
}

func (aa *adminAuth) handleMyRequests(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess.UserID == 0 {
		adminJSON(w, http.StatusOK, []adminRequest{})

		return
	}

	rows, err := aa.h.state.ListAccessRequests(sess.UserID, 100)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	out, err := aa.requestViews(rows)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	adminJSON(w, http.StatusOK, out)
}

func (aa *adminAuth) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess.UserID == 0 || sess.roleEmail == "" {
		adminJSONError(w, http.StatusForbidden, "your sign-in is not linked to an orgscale account; connect a device first")

		return
	}

	var body struct {
		Tag     string `json:"tag"`
		Minutes int    `json:"minutes"`
		Reason  string `json:"reason"`
		SSH     bool   `json:"ssh"`
	}

	if !decodeJSON(w, r, &body) {
		return
	}

	reason := strings.TrimSpace(body.Reason)
	if len(reason) < 5 || len(reason) > 500 {
		adminJSONError(w, http.StatusBadRequest, "say why you need access (5 to 500 characters)")

		return
	}

	tags, err := aa.h.state.ListServerTags()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	// The same answer whether the tag does not exist or the person may not
	// ask for it, so requests reveal nothing.
	i := slices.IndexFunc(tags, func(t types.ServerTag) bool { return t.Name == body.Tag })
	if i < 0 || !aa.mayRequest(tags[i], sess.UserID) {
		adminJSONError(w, http.StatusBadRequest, "access to this tag cannot be requested")

		return
	}

	t := tags[i]
	if body.Minutes < 15 || body.Minutes > t.RequestMaxMinutes {
		adminJSONError(w, http.StatusBadRequest, fmt.Sprintf("ask for between 15 minutes and %s", time.Duration(t.RequestMaxMinutes)*time.Minute))

		return
	}

	if body.SSH && len(sshUsersOf(t)) == 0 {
		adminJSONError(w, http.StatusBadRequest, "SSH cannot be requested for this tag")

		return
	}

	mine, err := aa.h.state.ListAccessRequests(sess.UserID, 200)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	now := time.Now()
	open := 0

	for _, m := range mine {
		if m.Status == types.AccessRequestPending || m.Active(now) {
			open++

			if m.Tag == t.Name && m.Status == types.AccessRequestPending {
				adminJSONError(w, http.StatusConflict, "you already have a request waiting for this tag")

				return
			}
		}
	}

	if open >= accessRequestOpenLimit {
		adminJSONError(w, http.StatusTooManyRequests, "you have too many open requests; cancel some first")

		return
	}

	id, err := aa.h.state.CreateAccessRequest(types.AccessRequest{
		UserID: sess.UserID, Requester: sess.roleEmail, Tag: t.Name, Reason: reason,
		Minutes: body.Minutes, SSH: body.SSH, Status: types.AccessRequestPending, CreatedAt: now.UTC(),
	})
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.auditReq(r, "access_request.create", fmt.Sprintf("#%d", id),
		fmt.Sprintf("%s for %s: %s", t.Name, time.Duration(body.Minutes)*time.Minute, reason))

	adminJSON(w, http.StatusCreated, map[string]uint64{"id": id})
}

// handleCancelRequest withdraws a waiting request, or ends running access
// early. People can only cancel their own.
func (aa *adminAuth) handleCancelRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := requestIDFromURL(w, r)
	if !ok {
		return
	}

	sess := sessionFrom(r)

	req, err := aa.h.state.GetAccessRequest(id)
	if err != nil || req.UserID != sess.UserID || sess.UserID == 0 {
		adminJSONError(w, http.StatusNotFound, "no such request")

		return
	}

	now := time.Now().UTC()
	from := []string{types.AccessRequestPending}

	switch {
	case req.Status == types.AccessRequestPending:
		req.Status = types.AccessRequestCancelled
	case req.Active(now):
		req.Status, req.ExpiresAt = types.AccessRequestRevoked, &now
		from = []string{types.AccessRequestApproved}
	default:
		adminJSONError(w, http.StatusConflict, "this request is already closed")

		return
	}

	req.DecidedBy, req.DecidedAt, req.DecisionNote = sess.label(), &now, "ended by the requester"

	changes, err := aa.h.state.DecideAccessRequest(req, from...)
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, http.StatusConflict, "this request changed meanwhile; reload")

		return
	}

	aa.auditReq(r, "access_request.cancel", fmt.Sprintf("#%d", id), req.Tag)
	w.WriteHeader(http.StatusNoContent)
}

// runAccessRequestExpiry ends approved access when its time is up, and
// pending requests nobody decided within a day.
func (h *Orgscale) runAccessRequestExpiry(ctx context.Context) {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		lapsed, changes, err := h.state.ExpireAccessRequests(time.Now(), accessRequestPendingTTL)
		if err != nil {
			log.Warn().Err(err).Msg("access requests: expiring")

			continue
		}

		h.Change(changes...)

		if lapsed > 0 {
			log.Info().Int64("count", lapsed).Msg("access requests: temporary access ended")

			if h.admin != nil {
				h.admin.audit(types.AuditEvent{
					Actor: "orgscale", ActorKind: types.AuditActorSystem,
					Action: "access_request.expire", Detail: fmt.Sprintf("%d ended", lapsed),
				})
			}
		}
	}
}
