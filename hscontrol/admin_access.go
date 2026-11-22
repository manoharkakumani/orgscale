package hscontrol

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/manoharkakumani/orgscale/hscontrol/types/change"
	"github.com/tailscale/hujson"
	"gorm.io/gorm"
	"tailscale.com/util/rands"
)

// Server tags, the access rule editor and bulk device actions of the admin
// console. Administrators build access visually; the console turns it into
// policy (see hscontrol/policy/managed).

func (aa *adminAuth) accessRoutes(r chi.Router) {
	r.Get("/tags", aa.handleListTags)
	r.Put("/tags/{name}", aa.handlePutTag)
	r.Delete("/tags/{name}", aa.handleDeleteTag)
	r.Post("/tags/{name}/enroll", aa.handleEnrollTag)

	r.Get("/rules", aa.handleListRules)
	r.Post("/rules", aa.handleSaveRule)
	r.Put("/rules/order", aa.handleReorderRules)
	r.Put("/rules/{id}", aa.handleSaveRule)
	r.Delete("/rules/{id}", aa.handleDeleteRule)

	r.Post("/devices/bulk", aa.handleBulkDevices)

	aa.tempAccessRoutes(r)
	aa.networkRoutes(r)
	aa.postureRoutes(r)

	r.Post("/groups", aa.handleSaveGroup)
	r.Put("/groups/{id}", aa.handleSaveGroup)
	r.Delete("/groups/{id}", aa.handleDeleteGroup)
}

// consoleGroup returns the console-made group named by the URL.
func (aa *adminAuth) consoleGroup(w http.ResponseWriter, r *http.Request) (*types.DirectoryGroup, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid group id")

		return nil, false
	}

	var g *types.DirectoryGroup

	err = aa.h.state.ReadDirectory(func(rx *gorm.DB) error {
		var err error

		g, err = hsdb.GetDirectoryGroup(rx, id)

		return err
	})
	if err != nil {
		adminJSONError(w, http.StatusNotFound, "no such group")

		return nil, false
	}

	if g.Source != types.DirectorySourceConsole {
		adminJSONError(w, http.StatusConflict, "this group comes from your identity provider; change it there")

		return nil, false
	}

	return g, true
}

// handleSaveGroup creates (POST) or updates (PUT) a group made in the
// console, with its members.
func (aa *adminAuth) handleSaveGroup(w http.ResponseWriter, r *http.Request) {
	externalID := "console-" + rands.HexString(12)

	if chi.URLParam(r, "id") != "" {
		g, ok := aa.consoleGroup(w, r)
		if !ok {
			return
		}

		externalID = g.ExternalID
	}

	var req struct {
		Name      string   `json:"name"`
		MemberIDs []uint64 `json:"memberIds"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if hsdb.DirectoryGroupName(req.Name) == "" || len(req.Name) > 100 {
		adminJSONError(w, http.StatusBadRequest, "give the group a name with letters or digits")

		return
	}

	members := make([]types.UserID, 0, len(req.MemberIDs))
	labels := make([]string, 0, len(req.MemberIDs))

	for _, id := range req.MemberIDs {
		u, err := aa.h.state.GetUserByID(types.UserID(id))
		if err != nil {
			adminJSONError(w, http.StatusBadRequest, fmt.Sprintf("no user %d", id))

			return
		}

		members = append(members, types.UserID(id))
		labels = append(labels, cmp.Or(u.Email, u.Name))
	}

	var saved *types.DirectoryGroup

	changes, err := aa.h.state.WriteDirectory(func(tx *gorm.DB) error {
		g, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceConsole, externalID, req.Name)
		if err != nil {
			return err
		}

		saved = g

		return hsdb.SetDirectoryGroupMembers(tx, g.ID, members)
	})
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	action := "group.update"
	if chi.URLParam(r, "id") == "" {
		action = "group.create"
	}

	aa.auditReq(r, action, "group:"+saved.Name, "members: "+cmp.Or(strings.Join(labels, ", "), "none"))
	adminJSON(w, http.StatusOK, map[string]any{"id": saved.ID, "name": saved.Name})
}

func (aa *adminAuth) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	g, ok := aa.consoleGroup(w, r)
	if !ok {
		return
	}

	changes, err := aa.h.state.WriteDirectory(func(tx *gorm.DB) error {
		err := hsdb.DeleteDeviceAccessForPrincipal(tx, types.PrincipalDirectoryGroup, strconv.FormatUint(g.ID, 10))
		if err != nil {
			return err
		}

		return hsdb.DeleteDirectoryGroup(tx, g.ID)
	})
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.auditReq(r, "group.delete", "group:"+g.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

var tagNameRE = regexp.MustCompile(`^tag:[a-z0-9][a-z0-9-]{0,62}$`)

type adminPrincipal struct {
	Kind      string `json:"kind"`
	Principal string `json:"principal"`
	Label     string `json:"label,omitempty"`
}

// principalLabeler names principals for display.
func (aa *adminAuth) principalLabeler() (func(kind, principal string) string, error) {
	st := aa.h.state

	users, err := st.ListAllUsers()
	if err != nil {
		return nil, err
	}

	userLabel := make(map[string]string, len(users))
	for _, u := range users {
		userLabel[strconv.FormatUint(uint64(u.ID), 10)] = cmp.Or(u.Email, u.Name)
	}

	groups, err := st.ListDirectoryGroups()
	if err != nil {
		return nil, err
	}

	groupLabel := make(map[string]string, len(groups))
	for _, g := range groups {
		groupLabel[strconv.FormatUint(g.ID, 10)] = "group:" + g.Name
	}

	nodeLabel := make(map[string]string)
	for _, n := range st.ListNodes().All() {
		nodeLabel[strconv.FormatUint(uint64(n.ID()), 10)] = n.GivenName()
	}

	resources, err := st.ListNetworkResources()
	if err != nil {
		return nil, err
	}

	resourceLabel := make(map[string]string, len(resources))
	for _, r := range resources {
		resourceLabel[strconv.FormatUint(r.ID, 10)] = r.Name + " (" + r.Address + ")"
	}

	return func(kind, p string) string {
		switch kind {
		case types.PrincipalUser:
			return cmp.Or(userLabel[p], "deleted user "+p)
		case types.PrincipalDirectoryGroup:
			return cmp.Or(groupLabel[p], "deleted group "+p)
		case types.PrincipalPolicyGroup:
			return "group:" + strings.TrimPrefix(p, "group:")
		case types.PrincipalTag:
			return p
		case types.PrincipalDevice:
			return cmp.Or(nodeLabel[p], "deleted device "+p)
		case types.PrincipalEveryone:
			return "Everyone"
		case types.PrincipalSelf:
			return "Their own devices"
		case types.PrincipalResource:
			return cmp.Or(resourceLabel[p], "deleted resource "+p)
		}

		return p
	}, nil
}

// parsePrincipals validates principals from a request. allowed lists the
// kinds the caller accepts.
func (aa *adminAuth) parsePrincipals(in []adminPrincipal, allowed ...string) ([]adminPrincipal, error) {
	st := aa.h.state
	out := make([]adminPrincipal, 0, len(in))

	var tags map[string]bool

	for _, p := range in {
		if !slices.Contains(allowed, p.Kind) {
			return nil, userErrorf("%q cannot be used here", p.Kind)
		}

		switch p.Kind {
		case types.PrincipalUser:
			uid, err := strconv.ParseUint(p.Principal, 10, 64)
			if err != nil {
				return nil, userErrorf("invalid user id %q", p.Principal)
			}

			_, err = st.GetUserByID(types.UserID(uid))
			if err != nil {
				return nil, userErrorf("no user %s", p.Principal)
			}
		case types.PrincipalDirectoryGroup:
			_, err := strconv.ParseUint(p.Principal, 10, 64)
			if err != nil {
				return nil, userErrorf("invalid group id %q", p.Principal)
			}
		case types.PrincipalPolicyGroup:
			p.Principal = strings.TrimSpace(strings.TrimPrefix(p.Principal, "group:"))
			if p.Principal == "" {
				return nil, userError("empty group name")
			}
		case types.PrincipalTag:
			if tags == nil {
				tags = aa.knownTags()
			}

			if !tags[p.Principal] {
				return nil, userErrorf("unknown tag %q", p.Principal)
			}
		case types.PrincipalDevice:
			id, err := strconv.ParseUint(p.Principal, 10, 64)
			if err != nil {
				return nil, userErrorf("invalid device id %q", p.Principal)
			}

			if _, ok := st.GetNodeByID(types.NodeID(id)); !ok {
				return nil, userErrorf("no device %s", p.Principal)
			}
		case types.PrincipalResource:
			id, err := strconv.ParseUint(p.Principal, 10, 64)
			if err != nil {
				return nil, userErrorf("invalid resource id %q", p.Principal)
			}

			resources, err := st.ListNetworkResources()
			if err != nil {
				return nil, err
			}

			if !slices.ContainsFunc(resources, func(r types.NetworkResource) bool { return r.ID == id }) {
				return nil, userErrorf("no private resource %s", p.Principal)
			}
		case types.PrincipalEveryone, types.PrincipalSelf:
			p.Principal = ""
		default:
			return nil, userErrorf("unknown kind %q", p.Kind)
		}

		out = append(out, adminPrincipal{Kind: p.Kind, Principal: p.Principal})
	}

	return out, nil
}

// knownTags returns the tags the console manages and those the hand-written
// policy defines.
func (aa *adminAuth) knownTags() map[string]bool {
	out := map[string]bool{}

	tags, err := aa.h.state.ListServerTags()
	if err == nil {
		for _, t := range tags {
			out[t.Name] = true
		}
	}

	raw, err := aa.h.state.RawPolicy()
	if err == nil {
		for _, t := range policyTags(raw) {
			out[t] = true
		}
	}

	return out
}

// policyTags returns the tags the hand-written policy defines.
func policyTags(raw []byte) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	std, err := hujson.Standardize(bytes.Clone(raw))
	if err != nil {
		return nil
	}

	var doc struct {
		TagOwners map[string]json.RawMessage `json:"tagOwners"`
	}

	if json.Unmarshal(std, &doc) != nil {
		return nil
	}

	out := make([]string, 0, len(doc.TagOwners))
	for t := range doc.TagOwners {
		out = append(out, t)
	}

	slices.Sort(out)

	return out
}

type adminTagDevice struct {
	ID     uint64 `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	OS     string `json:"os"`
}

type adminTag struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Managed     bool             `json:"managed"`
	CreatedAt   *time.Time       `json:"createdAt,omitempty"`
	Access      []adminPrincipal `json:"access"`
	Devices     []adminTagDevice `json:"devices"`

	// TempSSHUsers are the Linux accounts temporary access to the tag may
	// SSH in as.
	TempSSHUsers []string `json:"tempSSHUsers"`
}

func (aa *adminAuth) handleListTags(w http.ResponseWriter, r *http.Request) {
	st := aa.h.state

	label, err := aa.principalLabeler()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	managedTags, err := st.ListServerTags()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	grants, err := st.ListServerTagAccess()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	byName := map[string]*adminTag{}
	get := func(name string) *adminTag {
		t, ok := byName[name]
		if !ok {
			t = &adminTag{Name: name, Access: []adminPrincipal{}, Devices: []adminTagDevice{}, TempSSHUsers: []string{}}
			byName[name] = t
		}

		return t
	}

	for _, mt := range managedTags {
		t := get(mt.Name)
		t.Managed, t.Description = true, mt.Description
		t.TempSSHUsers = sshUsersOf(mt)
		created := mt.CreatedAt
		t.CreatedAt = &created
	}

	raw, err := st.RawPolicy()
	if err == nil {
		for _, name := range policyTags(raw) {
			get(name)
		}
	}

	for _, g := range grants {
		t := get(g.Tag)
		t.Access = append(t.Access, adminPrincipal{Kind: g.PrincipalKind, Principal: g.Principal, Label: label(g.PrincipalKind, g.Principal)})
	}

	for _, n := range st.ListNodes().All() {
		for _, tag := range n.Tags().All() {
			d := adminTagDevice{ID: uint64(n.ID()), Name: n.GivenName()}
			if o := n.IsOnline(); o.Valid() {
				d.Online = o.Get()
			}

			if hi := n.Hostinfo(); hi.Valid() {
				d.OS = hi.OS()
			}

			t := get(tag)
			t.Devices = append(t.Devices, d)
		}
	}

	out := make([]*adminTag, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}

	slices.SortFunc(out, func(a, b *adminTag) int { return cmp.Compare(a.Name, b.Name) })

	adminJSON(w, http.StatusOK, out)
}

func tagFromURL(r *http.Request) (string, bool) {
	name := chi.URLParam(r, "name")

	u, err := url.PathUnescape(name)
	if err == nil {
		name = u
	}

	name = strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(name, "tag:") {
		name = "tag:" + name
	}

	return name, tagNameRE.MatchString(name)
}

var tagGranteeKinds = []string{
	types.PrincipalUser, types.PrincipalDirectoryGroup, types.PrincipalPolicyGroup,
	types.PrincipalTag, types.PrincipalEveryone,
}

func (aa *adminAuth) handlePutTag(w http.ResponseWriter, r *http.Request) {
	name, ok := tagFromURL(r)
	if !ok {
		adminJSONError(w, http.StatusBadRequest, "a tag name uses lowercase letters, digits and dashes, like tag:web-servers")

		return
	}

	var req struct {
		Description string           `json:"description"`
		Access      []adminPrincipal `json:"access"`
		// TempSSHUsers, when present, sets the Linux accounts temporary
		// access to the tag may SSH in as.
		TempSSHUsers *[]string `json:"tempSSHUsers"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	var tempSSH string

	if req.TempSSHUsers != nil {
		users := []string{}

		if len(*req.TempSSHUsers) > 0 {
			var err error

			users, err = validSSHUsers(*req.TempSSHUsers)
			if err != nil {
				adminJSONError(w, http.StatusBadRequest, err.Error())

				return
			}
		}

		tempSSH = mustJSON(users)
	}

	// The tag being saved is a valid grantee too (servers reaching each
	// other), though it may not exist yet.
	var self []adminPrincipal

	req.Access = slices.DeleteFunc(req.Access, func(p adminPrincipal) bool {
		if p.Kind == types.PrincipalTag && p.Principal == name {
			self = append(self, p)

			return true
		}

		return false
	})

	ps, err := aa.parsePrincipals(req.Access, tagGranteeKinds...)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	if len(self) > 0 {
		ps = append(ps, adminPrincipal{Kind: types.PrincipalTag, Principal: name})
	}

	grants := make([]types.ServerTagAccess, 0, len(ps))
	labels := make([]string, 0, len(ps))

	label, err := aa.principalLabeler()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	for _, p := range ps {
		grants = append(grants, types.ServerTagAccess{PrincipalKind: p.Kind, Principal: p.Principal})
		labels = append(labels, label(p.Kind, p.Principal))
	}

	sess := sessionFrom(r)

	by := ""
	if sess != nil {
		by = sess.label()
	}

	changes, err := aa.h.state.SaveServerTag(types.ServerTag{
		Name: name, Description: strings.TrimSpace(req.Description), CreatedBy: by,
	}, grants)
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	if req.TempSSHUsers != nil {
		changes, err := aa.h.state.SetServerTagRequests(name, 0, tempSSH, "")
		aa.h.Change(changes...)

		if err != nil {
			adminJSONError(w, http.StatusBadRequest, err.Error())

			return
		}
	}

	aa.auditReq(r, "tag.save", name, "access: "+cmp.Or(strings.Join(labels, ", "), "nobody"))
	w.WriteHeader(http.StatusNoContent)
}

func (aa *adminAuth) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	name, ok := tagFromURL(r)
	if !ok {
		adminJSONError(w, http.StatusBadRequest, "invalid tag name")

		return
	}

	inUse := 0

	for _, n := range aa.h.state.ListNodes().All() {
		if n.HasTag(name) {
			inUse++
		}
	}

	if inUse > 0 {
		adminJSONError(w, http.StatusConflict, fmt.Sprintf("%s is still on %s; retag or remove them first", name, plural(inUse, "device")))

		return
	}

	changes, err := aa.h.state.DeleteServerTag(name)
	aa.h.Change(changes...)

	if errors.Is(err, hsdb.ErrServerTagNotFound) {
		adminJSONError(w, http.StatusNotFound, "this tag is not managed by the console")

		return
	}

	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.auditReq(r, "tag.delete", name, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleEnrollTag creates an auth key that enrolls servers into a tag.
func (aa *adminAuth) handleEnrollTag(w http.ResponseWriter, r *http.Request) {
	name, ok := tagFromURL(r)
	if !ok || !aa.knownTags()[name] {
		adminJSONError(w, http.StatusNotFound, "unknown tag")

		return
	}

	var req struct {
		Reusable  bool `json:"reusable"`
		Ephemeral bool `json:"ephemeral"`
		// ExpiresInHours is how long the key can be used to enroll; devices
		// already enrolled are not affected.
		ExpiresInHours int `json:"expiresInHours"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ExpiresInHours <= 0 || req.ExpiresInHours > 24*365 {
		adminJSONError(w, http.StatusBadRequest, "expiry must be between 1 hour and 1 year")

		return
	}

	exp := time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour)

	key, err := aa.h.state.CreatePreAuthKey(nil, req.Reusable, req.Ephemeral, &exp, []string{name})
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	kind := "single-use"
	if req.Reusable {
		kind = "reusable"
	}

	if req.Ephemeral {
		kind += ", ephemeral"
	}

	aa.auditReq(r, "tag.enroll_key", name, fmt.Sprintf("%s key, expires %s", kind, exp.UTC().Format(time.RFC3339)))
	adminJSON(w, http.StatusCreated, map[string]any{
		"key":        key.Key,
		"expiration": exp,
		"reusable":   req.Reusable,
		"ephemeral":  req.Ephemeral,
		"tag":        name,
		"serverUrl":  aa.h.cfg.ServerURL,
	})
}

type adminRule struct {
	ID             uint64           `json:"id"`
	Kind           string           `json:"kind"`
	SSHUsers       []string         `json:"sshUsers"`
	SSHCheckPeriod string           `json:"sshCheckPeriod"`
	Name           string           `json:"name"`
	Protocol       string           `json:"protocol"`
	Ports          string           `json:"ports"`
	Enabled        bool             `json:"enabled"`
	Sources        []adminPrincipal `json:"sources"`
	Destinations   []adminPrincipal `json:"destinations"`
	UpdatedAt      time.Time        `json:"updatedAt"`
	UpdatedBy      string           `json:"updatedBy"`
}

func (aa *adminAuth) handleListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := aa.h.state.ListAccessRules()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	label, err := aa.principalLabeler()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	conv := func(ps []types.RulePrincipal) []adminPrincipal {
		out := make([]adminPrincipal, 0, len(ps))
		for _, p := range ps {
			out = append(out, adminPrincipal{Kind: p.Kind, Principal: p.Principal, Label: label(p.Kind, p.Principal)})
		}

		return out
	}

	out := make([]adminRule, 0, len(rules))
	for _, ru := range rules {
		users := []string{}
		_ = json.Unmarshal([]byte(ru.SSHUsers), &users)

		out = append(out, adminRule{
			ID: ru.ID, Kind: cmp.Or(ru.Kind, types.RuleKindNetwork), SSHUsers: nonNil(users), SSHCheckPeriod: ru.SSHCheckPeriod,
			Name: ru.Name, Protocol: ru.Protocol, Ports: ru.Ports, Enabled: ru.Enabled,
			Sources: conv(ru.Sources), Destinations: conv(ru.Destinations),
			UpdatedAt: ru.UpdatedAt, UpdatedBy: ru.UpdatedBy,
		})
	}

	adminJSON(w, http.StatusOK, out)
}

var portsRE = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?(,\d{1,5}(-\d{1,5})?)*$`)

// normalizePorts validates a port list like "22, 443, 8000-8100".
func normalizePorts(s string) (string, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if s == "" || s == "*" {
		return "*", nil
	}

	if !portsRE.MatchString(s) {
		return "", userError("ports are a list like 22, 443, 8000-8100")
	}

	for part := range strings.SplitSeq(s, ",") {
		lo, hi, _ := strings.Cut(part, "-")
		a, _ := strconv.Atoi(lo)
		b := a

		if hi != "" {
			b, _ = strconv.Atoi(hi)
		}

		if a < 1 || b > 65535 || a > b {
			return "", userErrorf("invalid port range %q", part)
		}
	}

	return s, nil
}

var (
	sshSourceKinds      = []string{types.PrincipalUser, types.PrincipalDirectoryGroup, types.PrincipalPolicyGroup, types.PrincipalEveryone}
	sshDestinationKinds = []string{types.PrincipalTag, types.PrincipalSelf}
	sshUserRE           = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
)

// validSSHUsers checks the Linux accounts of an SSH rule.
func validSSHUsers(in []string) ([]string, error) {
	var out []string

	for _, u := range in {
		u = strings.TrimSpace(u)
		if u != "autogroup:nonroot" && !sshUserRE.MatchString(u) {
			return nil, userErrorf("%q is not a Linux account name", u)
		}

		if !slices.Contains(out, u) {
			out = append(out, u)
		}
	}

	if len(out) == 0 {
		return nil, userError("choose at least one account people may log in as")
	}

	return out, nil
}

var ruleKinds = []string{
	types.PrincipalUser, types.PrincipalDirectoryGroup, types.PrincipalPolicyGroup,
	types.PrincipalTag, types.PrincipalDevice, types.PrincipalEveryone,
}

func (aa *adminAuth) handleSaveRule(w http.ResponseWriter, r *http.Request) {
	var id uint64

	if s := chi.URLParam(r, "id"); s != "" {
		var err error

		id, err = strconv.ParseUint(s, 10, 64)
		if err != nil {
			adminJSONError(w, http.StatusBadRequest, "invalid rule id")

			return
		}
	}

	var req struct {
		Kind           string           `json:"kind"`
		SSHUsers       []string         `json:"sshUsers"`
		SSHCheckPeriod string           `json:"sshCheckPeriod"`
		Name           string           `json:"name"`
		Protocol       string           `json:"protocol"`
		Ports          string           `json:"ports"`
		Enabled        bool             `json:"enabled"`
		Sources        []adminPrincipal `json:"sources"`
		Destinations   []adminPrincipal `json:"destinations"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 120 {
		adminJSONError(w, http.StatusBadRequest, "give the rule a name (up to 120 characters)")

		return
	}

	if !slices.Contains([]string{"", "tcp", "udp", "icmp"}, req.Protocol) {
		adminJSONError(w, http.StatusBadRequest, "protocol is any, tcp, udp or icmp")

		return
	}

	ports, err := normalizePorts(req.Ports)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	if req.Protocol == "icmp" {
		ports = "*"
	}

	if len(req.Sources) == 0 || len(req.Destinations) == 0 {
		adminJSONError(w, http.StatusBadRequest, "choose who the rule is for and what they can reach")

		return
	}

	req.Kind = cmp.Or(req.Kind, types.RuleKindNetwork)

	srcKinds, dstKinds := ruleKinds, append(slices.Clone(ruleKinds), types.PrincipalResource)
	sshUsers := ""

	switch req.Kind {
	case types.RuleKindNetwork:
		req.SSHUsers, req.SSHCheckPeriod = nil, ""
	case types.RuleKindSSH:
		srcKinds = sshSourceKinds
		dstKinds = sshDestinationKinds
		ports, req.Protocol = "22", "tcp"

		users, err := validSSHUsers(req.SSHUsers)
		if err != nil {
			adminJSONError(w, http.StatusBadRequest, err.Error())

			return
		}

		sshUsers = mustJSON(users)

		if req.SSHCheckPeriod != "" {
			d, err := time.ParseDuration(req.SSHCheckPeriod)
			if err != nil || d < time.Minute || d > 168*time.Hour {
				adminJSONError(w, http.StatusBadRequest, "the sign-in interval must be between 1 minute and 7 days")

				return
			}
		}
	default:
		adminJSONError(w, http.StatusBadRequest, "unknown rule type")

		return
	}

	src, err := aa.parsePrincipals(req.Sources, srcKinds...)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "who: "+err.Error())

		return
	}

	dst, err := aa.parsePrincipals(req.Destinations, dstKinds...)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "can reach: "+err.Error())

		return
	}

	conv := func(ps []adminPrincipal) []types.RulePrincipal {
		out := make([]types.RulePrincipal, 0, len(ps))
		for _, p := range ps {
			out = append(out, types.RulePrincipal{Kind: p.Kind, Principal: p.Principal})
		}

		return out
	}

	by := ""
	if sess := sessionFrom(r); sess != nil {
		by = sess.label()
	}

	newID, changes, err := aa.h.state.SaveAccessRule(types.AccessRule{
		ID: id, Kind: req.Kind, SSHUsers: sshUsers, SSHCheckPeriod: req.SSHCheckPeriod, Name: req.Name, Protocol: req.Protocol, Ports: ports, Enabled: req.Enabled,
		UpdatedBy: by, Sources: conv(src), Destinations: conv(dst),
	})
	aa.h.Change(changes...)

	if errors.Is(err, hsdb.ErrAccessRuleNotFound) {
		adminJSONError(w, http.StatusNotFound, "no such rule")

		return
	}

	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	action := "rule.update"
	if id == 0 {
		action = "rule.create"
	}

	detail := aa.describeRule(src, dst, req.Protocol, ports, req.Enabled)
	if req.Kind == types.RuleKindSSH {
		detail = "SSH as " + strings.Join(req.SSHUsers, ", ") + ": " + detail
		if req.SSHCheckPeriod != "" {
			detail += ", sign in again every " + req.SSHCheckPeriod
		}
	}

	aa.auditReq(r, action, req.Name, detail)
	adminJSON(w, http.StatusOK, map[string]uint64{"id": newID})
}

func (aa *adminAuth) describeRule(src, dst []adminPrincipal, proto, ports string, enabled bool) string {
	label, err := aa.principalLabeler()
	if err != nil {
		return ""
	}

	names := func(ps []adminPrincipal) string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, label(p.Kind, p.Principal))
		}

		return strings.Join(out, ", ")
	}

	what := "all ports"
	if ports != "*" {
		what = "ports " + ports
	}

	if proto != "" {
		what = strings.ToUpper(proto) + " " + what
	}

	s := fmt.Sprintf("%s → %s (%s)", names(src), names(dst), what)
	if !enabled {
		s += ", paused"
	}

	return s
}

func (aa *adminAuth) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, "invalid rule id")

		return
	}

	name := strconv.FormatUint(id, 10)

	rules, _ := aa.h.state.ListAccessRules()
	for _, ru := range rules {
		if ru.ID == id {
			name = ru.Name
		}
	}

	changes, err := aa.h.state.DeleteAccessRule(id)
	aa.h.Change(changes...)

	if errors.Is(err, hsdb.ErrAccessRuleNotFound) {
		adminJSONError(w, http.StatusNotFound, "no such rule")

		return
	}

	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.auditReq(r, "rule.delete", name, "")
	w.WriteHeader(http.StatusNoContent)
}

func (aa *adminAuth) handleReorderRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []uint64 `json:"ids"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	changes, err := aa.h.state.ReorderAccessRules(req.IDs)
	aa.h.Change(changes...)

	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBulkDevices applies tags or access to several devices at once.
//
// Tagging a person's device turns it into a server owned by its tags, as on
// Tailscale: it no longer belongs to the person.
func (aa *adminAuth) handleBulkDevices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []uint64 `json:"ids"`
		// Action is "set_tags", "add_access" or "set_access".
		Action string           `json:"action"`
		Tags   []string         `json:"tags"`
		Access []adminPrincipal `json:"access"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		adminJSONError(w, http.StatusBadRequest, "select between 1 and 1000 devices")

		return
	}

	st := aa.h.state

	var names []string

	for _, id := range req.IDs {
		n, ok := st.GetNodeByID(types.NodeID(id))
		if !ok {
			adminJSONError(w, http.StatusNotFound, fmt.Sprintf("no device %d", id))

			return
		}

		names = append(names, n.GivenName())
	}

	var all []change.Change

	defer func() { aa.h.Change(all...) }()

	switch req.Action {
	case "set_tags":
		known := aa.knownTags()

		if len(req.Tags) == 0 {
			adminJSONError(w, http.StatusBadRequest, "choose at least one tag")

			return
		}

		for _, t := range req.Tags {
			if !known[t] {
				adminJSONError(w, http.StatusBadRequest, "unknown tag "+t)

				return
			}
		}

		for _, id := range req.IDs {
			_, c, err := st.SetNodeTags(types.NodeID(id), req.Tags)
			if err != nil {
				adminJSONError(w, http.StatusBadRequest, err.Error())

				return
			}

			all = append(all, c)
		}

		aa.auditReq(r, "device.bulk_tags", strings.Join(names, ", "), strings.Join(req.Tags, ", "))
	case "add_access", "set_access":
		ps, err := aa.parsePrincipals(req.Access, types.PrincipalUser, types.PrincipalDirectoryGroup, types.PrincipalPolicyGroup)
		if err != nil {
			adminJSONError(w, http.StatusBadRequest, err.Error())

			return
		}

		existing, err := st.ListDeviceAccess()
		if err != nil {
			adminJSONError(w, http.StatusInternalServerError, err.Error())

			return
		}

		for _, id := range req.IDs {
			var grants []types.DeviceAccess

			if req.Action == "add_access" {
				for _, g := range existing {
					if g.NodeID == types.NodeID(id) {
						grants = append(grants, types.DeviceAccess{PrincipalKind: g.PrincipalKind, Principal: g.Principal})
					}
				}
			}

			for _, p := range ps {
				grants = append(grants, types.DeviceAccess{PrincipalKind: p.Kind, Principal: p.Principal})
			}

			c, err := st.SetDeviceAccess(types.NodeID(id), grants)
			all = append(all, c...)

			if err != nil {
				adminJSONError(w, http.StatusBadRequest, err.Error())

				return
			}
		}

		label, _ := aa.principalLabeler()

		var labels []string

		for _, p := range ps {
			if label != nil {
				labels = append(labels, label(p.Kind, p.Principal))
			}
		}

		aa.auditReq(r, "device.bulk_access", strings.Join(names, ", "), req.Action+": "+strings.Join(labels, ", "))
	default:
		adminJSONError(w, http.StatusBadRequest, "unknown action")

		return
	}

	w.WriteHeader(http.StatusNoContent)
}
