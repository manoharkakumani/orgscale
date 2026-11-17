package hscontrol

import (
	"cmp"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"tailscale.com/util/rands"
)

// A SCIM 2.0 server (RFC 7643/7644) for the provisioning that Okta,
// Microsoft Entra ID, OneLogin and Authentik push. Users map to orgscale
// users (id = user ID), groups to directory groups with source "scim"
// (id = directory group ID). Deactivating or deleting a user offboards
// them: devices, keys and the user are removed.
//
// The bearer token is made in the admin console; only its SHA-256 is kept.

const (
	scimUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
	scimListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	scimContentType = "application/scim+json"
)

type scimServer struct {
	h *Orgscale
}

func (s *scimServer) routes(r chi.Router) {
	r.Use(s.auth)

	r.Get("/ServiceProviderConfig", s.serviceProviderConfig)
	r.Get("/ResourceTypes", s.resourceTypes)
	r.Get("/Schemas", s.schemas)

	r.Get("/Users", s.listUsers)
	r.Post("/Users", s.createUser)
	r.Get("/Users/{id}", s.getUser)
	r.Put("/Users/{id}", s.replaceUser)
	r.Patch("/Users/{id}", s.patchUser)
	r.Delete("/Users/{id}", s.deleteUser)

	r.Get("/Groups", s.listGroups)
	r.Post("/Groups", s.createGroup)
	r.Get("/Groups/{id}", s.getGroup)
	r.Put("/Groups/{id}", s.replaceGroup)
	r.Patch("/Groups/{id}", s.patchGroup)
	r.Delete("/Groups/{id}", s.deleteGroup)
}

func (s *scimServer) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var want string

		ok, err := s.h.state.GetSettingJSON(types.SettingSCIMTokenHash, &want)
		if err != nil || !ok || want == "" {
			scimError(w, http.StatusUnauthorized, "SCIM provisioning is not enabled; create a token in the orgscale admin console")

			return
		}

		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(strings.TrimSpace(token)))

		if !found || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(want)) != 1 {
			scimError(w, http.StatusUnauthorized, "invalid SCIM token")

			return
		}

		next.ServeHTTP(w, r)
	})
}

func scimJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func scimError(w http.ResponseWriter, code int, detail string) {
	scimJSON(w, code, map[string]any{
		"schemas": []string{scimErrorSchema},
		"status":  strconv.Itoa(code),
		"detail":  detail,
	})
}

func (s *scimServer) audit(action, target, detail string) {
	_ = s.h.state.AppendAuditEvent(types.AuditEvent{
		Actor: "scim", ActorKind: types.AuditActorSCIM, Action: action, Target: target, Detail: detail,
	})
}

func (s *scimServer) base() string {
	return strings.TrimSuffix(s.h.cfg.ServerURL, "/") + "/scim/v2"
}

func (s *scimServer) serviceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":        []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":          map[string]bool{"supported": true},
		"bulk":           map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":         map[string]any{"supported": true, "maxResults": 1000},
		"changePassword": map[string]bool{"supported": false},
		"sort":           map[string]bool{"supported": false},
		"etag":           map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type": "oauthbearertoken", "name": "Bearer token", "primary": true,
			"description": "Token created in the orgscale admin console",
		}},
	})
}

func (s *scimServer) resourceTypes(w http.ResponseWriter, _ *http.Request) {
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{scimListSchema},
		"totalResults": 2,
		"Resources": []map[string]any{
			{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "User", "name": "User", "endpoint": "/Users", "schema": scimUserSchema},
			{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "Group", "name": "Group", "endpoint": "/Groups", "schema": scimGroupSchema},
		},
	})
}

func (s *scimServer) schemas(w http.ResponseWriter, _ *http.Request) {
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{scimListSchema},
		"totalResults": 2,
		"Resources": []map[string]any{
			{"id": scimUserSchema, "name": "User"},
			{"id": scimGroupSchema, "name": "Group"},
		},
	})
}

// scimUser is the part of a SCIM User this server reads.
type scimUser struct {
	ExternalID  string `json:"externalId"`
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
	Name        struct {
		Formatted  string `json:"formatted"`
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
	} `json:"name"`
	Emails []struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	Active *scimBool `json:"active"`
}

// scimBool accepts true/false and the strings "True"/"False" Entra sends.
type scimBool bool

func (b *scimBool) UnmarshalJSON(data []byte) error {
	var v any

	err := json.Unmarshal(data, &v)
	if err != nil {
		return err
	}

	switch t := v.(type) {
	case bool:
		*b = scimBool(t)
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(t))
		if err != nil {
			return fmt.Errorf("invalid boolean %q", t)
		}

		*b = scimBool(parsed)
	default:
		return fmt.Errorf("invalid boolean %s", data)
	}

	return nil
}

func (u *scimUser) email() string {
	for _, e := range u.Emails {
		if e.Primary && e.Value != "" {
			return e.Value
		}
	}

	for _, e := range u.Emails {
		if e.Value != "" {
			return e.Value
		}
	}

	if strings.Contains(u.UserName, "@") {
		return u.UserName
	}

	return ""
}

func (u *scimUser) displayName() string {
	full := strings.TrimSpace(u.Name.GivenName + " " + u.Name.FamilyName)

	return cmp.Or(u.DisplayName, u.Name.Formatted, full, u.UserName)
}

func (s *scimServer) userResource(u *types.User, active bool) map[string]any {
	id := strconv.FormatUint(uint64(u.ID), 10)
	email := cmp.Or(u.Email, u.Name)

	res := map[string]any{
		"schemas":     []string{scimUserSchema},
		"id":          id,
		"userName":    email,
		"displayName": cmp.Or(u.DisplayName, u.Name),
		"name":        map[string]string{"formatted": cmp.Or(u.DisplayName, u.Name)},
		"active":      active,
		"meta": map[string]any{
			"resourceType": "User",
			"created":      u.CreatedAt.UTC().Format(time.RFC3339),
			"lastModified": u.UpdatedAt.UTC().Format(time.RFC3339),
			"location":     s.base() + "/Users/" + id,
		},
	}

	if u.Email != "" {
		res["emails"] = []map[string]any{{"value": u.Email, "primary": true, "type": "work"}}
	}

	return res
}

func (s *scimServer) userByParam(w http.ResponseWriter, r *http.Request) (*types.User, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		scimError(w, http.StatusNotFound, "user not found")

		return nil, false
	}

	u, err := s.h.state.GetUserByID(types.UserID(id))
	if err != nil {
		scimError(w, http.StatusNotFound, "user not found")

		return nil, false
	}

	return u, true
}

var scimFilterRE = regexp.MustCompile(`^\s*(\w+(?:\.\w+)?)\s+eq\s+"((?:[^"\\]|\\.)*)"\s*$`)

// parseSCIMFilter supports the `attr eq "value"` filters provisioning
// clients send to look a resource up before creating it.
func parseSCIMFilter(f string) (string, string, error) {
	if strings.TrimSpace(f) == "" {
		return "", "", nil
	}

	m := scimFilterRE.FindStringSubmatch(f)
	if m == nil {
		return "", "", fmt.Errorf("unsupported filter %q", f)
	}

	return strings.ToLower(m[1]), strings.ReplaceAll(m[2], `\"`, `"`), nil
}

func paging(r *http.Request) (int, int) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
	count, err := strconv.Atoi(r.URL.Query().Get("count"))

	if err != nil {
		count = 100
	}

	return max(start, 1), min(max(count, 0), 1000)
}

func listResponse(resources []map[string]any, start, count int) map[string]any {
	total := len(resources)

	from := min(start-1, total)
	to := min(from+count, total)

	return map[string]any{
		"schemas":      []string{scimListSchema},
		"totalResults": total,
		"startIndex":   start,
		"itemsPerPage": to - from,
		"Resources":    resources[from:to],
	}
}

func (s *scimServer) listUsers(w http.ResponseWriter, r *http.Request) {
	attr, value, err := parseSCIMFilter(r.URL.Query().Get("filter"))
	if err != nil {
		scimError(w, http.StatusBadRequest, err.Error())

		return
	}

	users, err := s.h.state.ListAllUsers()
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	disabled, _ := s.h.state.ListDisabledUsers()

	var out []map[string]any

	for i := range users {
		u := &users[i]

		switch attr {
		case "":
		case "username", "emails.value", "emails":
			if !strings.EqualFold(u.Email, value) && !strings.EqualFold(u.Name, value) {
				continue
			}
		case "externalid", "id":
			if strconv.FormatUint(uint64(u.ID), 10) != value {
				continue
			}
		default:
			scimError(w, http.StatusBadRequest, "unsupported filter attribute "+attr)

			return
		}

		_, isDisabled := disabled[types.UserID(u.ID)]
		out = append(out, s.userResource(u, !isDisabled))
	}

	start, count := paging(r)
	scimJSON(w, http.StatusOK, listResponse(nonNilMaps(out), start, count))
}

func nonNilMaps(m []map[string]any) []map[string]any {
	if m == nil {
		return []map[string]any{}
	}

	return m
}

func (s *scimServer) getUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByParam(w, r)
	if !ok {
		return
	}

	disabled, _ := s.h.state.IsUserDisabled(types.UserID(u.ID))
	scimJSON(w, http.StatusOK, s.userResource(u, !disabled))
}

func decodeSCIM(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminRequestBody)).Decode(v)
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalid request body: "+err.Error())

		return false
	}

	return true
}

func (s *scimServer) createUser(w http.ResponseWriter, r *http.Request) {
	var in scimUser
	if !decodeSCIM(w, r, &in) {
		return
	}

	email := in.email()
	if email == "" {
		scimError(w, http.StatusBadRequest, "a user needs an email: send emails or an email userName")

		return
	}

	if in.Active != nil && !bool(*in.Active) {
		// Creating an inactive user grants nothing; report it as such.
		scimError(w, http.StatusBadRequest, "refusing to create an inactive user")

		return
	}

	if existing, err := s.h.userByEmail(email); err == nil && existing != nil {
		scimJSON(w, http.StatusConflict, map[string]any{
			"schemas": []string{scimErrorSchema}, "status": "409", "scimType": "uniqueness",
			"detail": "a user with this email exists; look it up with a userName filter",
		})

		return
	}

	u, _, err := s.h.provisionUser(email, in.displayName(), "", userProviderSCIM)
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	s.audit("scim.user.create", email, "")
	scimJSON(w, http.StatusCreated, s.userResource(u, true))
}

// applyUser updates u from a SCIM user and reports whether it was deactivated.
func (s *scimServer) applyUser(u *types.User, in *scimUser) (*types.User, bool, error) {
	email := in.email()
	name := in.displayName()

	updated, c, err := s.h.state.UpdateUser(types.UserID(u.ID), func(cur *types.User) error {
		if email != "" && !cur.ProviderIdentifier.Valid {
			cur.Email = strings.ToLower(email)
		}

		if name != "" {
			cur.DisplayName = name
		}

		return nil
	})
	if err != nil {
		return nil, false, err
	}

	s.h.Change(c)

	return updated, in.Active != nil && !bool(*in.Active), nil
}

func (s *scimServer) replaceUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByParam(w, r)
	if !ok {
		return
	}

	var in scimUser
	if !decodeSCIM(w, r, &in) {
		return
	}

	s.finishUserUpdate(w, u, &in)
}

func (s *scimServer) finishUserUpdate(w http.ResponseWriter, u *types.User, in *scimUser) {
	updated, deactivated, err := s.applyUser(u, in)
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	if deactivated {
		err = s.h.offboardUser(types.UserID(u.ID), "scim", types.AuditActorSCIM, "deactivated in the identity provider")
		if err != nil {
			scimError(w, http.StatusInternalServerError, err.Error())

			return
		}

		scimJSON(w, http.StatusOK, s.userResource(u, false))

		return
	}

	s.audit("scim.user.update", cmp.Or(updated.Email, updated.Name), "")
	scimJSON(w, http.StatusOK, s.userResource(updated, true))
}

type scimPatch struct {
	Operations []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"Operations"`
}

func (s *scimServer) patchUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByParam(w, r)
	if !ok {
		return
	}

	var p scimPatch
	if !decodeSCIM(w, r, &p) {
		return
	}

	var in scimUser

	for _, op := range p.Operations {
		if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
			continue // removing user attributes is not meaningful here
		}

		switch strings.ToLower(op.Path) {
		case "":
			// value is a partial user.
			err := json.Unmarshal(op.Value, &in)
			if err != nil {
				scimError(w, http.StatusBadRequest, "invalid patch value: "+err.Error())

				return
			}
		case "active":
			var b scimBool

			err := json.Unmarshal(op.Value, &b)
			if err != nil {
				scimError(w, http.StatusBadRequest, "invalid active value: "+err.Error())

				return
			}

			in.Active = &b
		case "displayname":
			_ = json.Unmarshal(op.Value, &in.DisplayName)
		case "name.formatted":
			_ = json.Unmarshal(op.Value, &in.Name.Formatted)
		case "username":
			_ = json.Unmarshal(op.Value, &in.UserName)
		case `emails[type eq "work"].value`, "emails.value":
			var v string
			if json.Unmarshal(op.Value, &v) == nil {
				in.Emails = append(in.Emails, struct {
					Value   string `json:"value"`
					Primary bool   `json:"primary"`
				}{Value: v, Primary: true})
			}
		}
	}

	s.finishUserUpdate(w, u, &in)
}

func (s *scimServer) deleteUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByParam(w, r)
	if !ok {
		return
	}

	err := s.h.offboardUser(types.UserID(u.ID), "scim", types.AuditActorSCIM, "deleted in the identity provider")
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Groups.

type scimMember struct {
	Value string `json:"value"`
}

type scimGroup struct {
	ExternalID  string       `json:"externalId"`
	DisplayName string       `json:"displayName"`
	Members     []scimMember `json:"members"`
}

func (s *scimServer) groupResource(g *types.DirectoryGroup) map[string]any {
	id := strconv.FormatUint(g.ID, 10)

	members := make([]map[string]string, 0, len(g.MemberIDs))
	for _, m := range g.MemberIDs {
		members = append(members, map[string]string{"value": strconv.FormatUint(uint64(m), 10)})
	}

	return map[string]any{
		"schemas":     []string{scimGroupSchema},
		"id":          id,
		"externalId":  g.ExternalID,
		"displayName": g.DisplayName,
		"members":     members,
		"meta": map[string]any{
			"resourceType": "Group",
			"created":      g.CreatedAt.UTC().Format(time.RFC3339),
			"lastModified": g.UpdatedAt.UTC().Format(time.RFC3339),
			"location":     s.base() + "/Groups/" + id,
		},
	}
}

func (s *scimServer) scimGroups() ([]types.DirectoryGroup, error) {
	all, err := s.h.state.ListDirectoryGroups()
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(all, func(g types.DirectoryGroup) bool { return g.Source != types.DirectorySourceSCIM }), nil
}

func (s *scimServer) groupByParam(w http.ResponseWriter, r *http.Request) (*types.DirectoryGroup, bool) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err == nil {
		var g *types.DirectoryGroup

		err = s.h.state.ReadDirectory(func(rx *gorm.DB) error {
			var err error

			g, err = hsdb.GetDirectoryGroup(rx, id)

			return err
		})
		if err == nil && g.Source == types.DirectorySourceSCIM {
			return g, true
		}
	}

	scimError(w, http.StatusNotFound, "group not found")

	return nil, false
}

func (s *scimServer) listGroups(w http.ResponseWriter, r *http.Request) {
	attr, value, err := parseSCIMFilter(r.URL.Query().Get("filter"))
	if err != nil {
		scimError(w, http.StatusBadRequest, err.Error())

		return
	}

	groups, err := s.scimGroups()
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	var out []map[string]any

	for i := range groups {
		g := &groups[i]

		switch attr {
		case "":
		case "displayname":
			if !strings.EqualFold(g.DisplayName, value) {
				continue
			}
		case "externalid":
			if g.ExternalID != value {
				continue
			}
		case "id":
			if strconv.FormatUint(g.ID, 10) != value {
				continue
			}
		default:
			scimError(w, http.StatusBadRequest, "unsupported filter attribute "+attr)

			return
		}

		res := s.groupResource(g)
		if r.URL.Query().Get("excludedAttributes") == "members" {
			delete(res, "members")
		}

		out = append(out, res)
	}

	start, count := paging(r)
	scimJSON(w, http.StatusOK, listResponse(nonNilMaps(out), start, count))
}

func (s *scimServer) getGroup(w http.ResponseWriter, r *http.Request) {
	g, ok := s.groupByParam(w, r)
	if !ok {
		return
	}

	scimJSON(w, http.StatusOK, s.groupResource(g))
}

// knownUsers returns the IDs of every user. It must be read before a
// directory transaction: SQLite has one connection, which the transaction
// holds, so a lookup inside it would wait forever.
func (s *scimServer) knownUsers() (map[types.UserID]bool, error) {
	users, err := s.h.state.ListAllUsers()
	if err != nil {
		return nil, err
	}

	known := make(map[types.UserID]bool, len(users))
	for _, u := range users {
		known[types.UserID(u.ID)] = true
	}

	return known, nil
}

// memberIDs turns SCIM member references into user IDs, skipping unknown ones
// (a nested group, or a user not provisioned here).
func memberIDs(known map[types.UserID]bool, members []scimMember) []types.UserID {
	out := make([]types.UserID, 0, len(members))

	for _, m := range members {
		id, err := strconv.ParseUint(m.Value, 10, 64)
		if err != nil {
			continue
		}

		if known[types.UserID(id)] {
			out = append(out, types.UserID(id))
		}
	}

	return out
}

func (s *scimServer) writeGroup(fn func(tx *gorm.DB, known map[types.UserID]bool) error) error {
	known, err := s.knownUsers()
	if err != nil {
		return err
	}

	changes, err := s.h.state.WriteDirectory(func(tx *gorm.DB) error { return fn(tx, known) })
	s.h.Change(changes...)

	return err
}

func (s *scimServer) reloadGroup(w http.ResponseWriter, code int, id uint64) {
	var g *types.DirectoryGroup

	err := s.h.state.ReadDirectory(func(rx *gorm.DB) error {
		var err error

		g, err = hsdb.GetDirectoryGroup(rx, id)

		return err
	})
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	scimJSON(w, code, s.groupResource(g))
}

func (s *scimServer) createGroup(w http.ResponseWriter, r *http.Request) {
	var in scimGroup
	if !decodeSCIM(w, r, &in) {
		return
	}

	if strings.TrimSpace(in.DisplayName) == "" {
		scimError(w, http.StatusBadRequest, "a group needs a displayName")

		return
	}

	external := cmp.Or(in.ExternalID, "orgscale-"+rands.HexString(16))

	groups, err := s.scimGroups()
	if err == nil {
		for _, g := range groups {
			if g.ExternalID == external || strings.EqualFold(g.DisplayName, in.DisplayName) {
				scimJSON(w, http.StatusConflict, map[string]any{
					"schemas": []string{scimErrorSchema}, "status": "409", "scimType": "uniqueness",
					"detail": "a group with this name exists",
				})

				return
			}
		}
	}

	var id uint64

	err = s.writeGroup(func(tx *gorm.DB, known map[types.UserID]bool) error {
		g, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, external, in.DisplayName)
		if err != nil {
			return err
		}

		id = g.ID

		return hsdb.SetDirectoryGroupMembers(tx, g.ID, memberIDs(known, in.Members))
	})
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	s.audit("scim.group.create", in.DisplayName, plural(len(in.Members), "member"))
	s.reloadGroup(w, http.StatusCreated, id)
}

func (s *scimServer) replaceGroup(w http.ResponseWriter, r *http.Request) {
	g, ok := s.groupByParam(w, r)
	if !ok {
		return
	}

	var in scimGroup
	if !decodeSCIM(w, r, &in) {
		return
	}

	err := s.writeGroup(func(tx *gorm.DB, known map[types.UserID]bool) error {
		_, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, g.ExternalID, cmp.Or(in.DisplayName, g.DisplayName))
		if err != nil {
			return err
		}

		return hsdb.SetDirectoryGroupMembers(tx, g.ID, memberIDs(known, in.Members))
	})
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	s.audit("scim.group.replace", cmp.Or(in.DisplayName, g.DisplayName), plural(len(in.Members), "member"))
	s.reloadGroup(w, http.StatusOK, g.ID)
}

var scimMemberPathRE = regexp.MustCompile(`(?i)^members\[\s*value\s+eq\s+"([^"]+)"\s*\]$`)

func (s *scimServer) patchGroup(w http.ResponseWriter, r *http.Request) {
	g, ok := s.groupByParam(w, r)
	if !ok {
		return
	}

	var p scimPatch
	if !decodeSCIM(w, r, &p) {
		return
	}

	var summary []string

	err := s.writeGroup(func(tx *gorm.DB, known map[types.UserID]bool) error {
		for _, op := range p.Operations {
			kind := strings.ToLower(op.Op)
			path := strings.TrimSpace(op.Path)

			var members []scimMember
			if len(op.Value) > 0 {
				_ = json.Unmarshal(op.Value, &members)
			}

			switch {
			case strings.EqualFold(path, "members") && kind == "add":
				err := hsdb.AddDirectoryGroupMembers(tx, g.ID, memberIDs(known, members))
				if err != nil {
					return err
				}

				summary = append(summary, "+"+plural(len(members), "member"))
			case strings.EqualFold(path, "members") && kind == "replace":
				err := hsdb.SetDirectoryGroupMembers(tx, g.ID, memberIDs(known, members))
				if err != nil {
					return err
				}

				summary = append(summary, "="+plural(len(members), "member"))
			case strings.EqualFold(path, "members") && kind == "remove":
				if len(members) == 0 {
					err := hsdb.SetDirectoryGroupMembers(tx, g.ID, nil)
					if err != nil {
						return err
					}

					summary = append(summary, "removed all members")

					continue
				}

				err := hsdb.RemoveDirectoryGroupMembers(tx, g.ID, memberIDs(known, members))
				if err != nil {
					return err
				}

				summary = append(summary, "-"+plural(len(members), "member"))
			case kind == "remove" && scimMemberPathRE.MatchString(path):
				m := scimMemberPathRE.FindStringSubmatch(path)

				err := hsdb.RemoveDirectoryGroupMembers(tx, g.ID, memberIDs(known, []scimMember{{Value: m[1]}}))
				if err != nil {
					return err
				}

				summary = append(summary, "-1 member")
			case strings.EqualFold(path, "displayName") && (kind == "replace" || kind == "add"):
				var name string
				if json.Unmarshal(op.Value, &name) != nil || strings.TrimSpace(name) == "" {
					return errors.New("invalid displayName")
				}

				_, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, g.ExternalID, name)
				if err != nil {
					return err
				}

				summary = append(summary, "renamed to "+name)
			case path == "" && (kind == "replace" || kind == "add"):
				var v scimGroup
				if json.Unmarshal(op.Value, &v) != nil {
					return errors.New("invalid patch value")
				}

				if v.DisplayName != "" {
					_, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, g.ExternalID, v.DisplayName)
					if err != nil {
						return err
					}

					summary = append(summary, "renamed to "+v.DisplayName)
				}

				if v.Members != nil {
					add := hsdb.AddDirectoryGroupMembers
					if kind == "replace" {
						add = hsdb.SetDirectoryGroupMembers
					}

					err := add(tx, g.ID, memberIDs(known, v.Members))
					if err != nil {
						return err
					}

					summary = append(summary, kind+" "+plural(len(v.Members), "member"))
				}
			default:
				log.Debug().Str("op", op.Op).Str("path", op.Path).Msg("scim: ignoring unsupported group patch")
			}
		}

		return nil
	})
	if err != nil {
		scimError(w, http.StatusBadRequest, err.Error())

		return
	}

	s.audit("scim.group.update", g.DisplayName, strings.Join(summary, ", "))

	// Entra and Okta accept 204 or the resource; return the resource.
	s.reloadGroup(w, http.StatusOK, g.ID)
}

func (s *scimServer) deleteGroup(w http.ResponseWriter, r *http.Request) {
	g, ok := s.groupByParam(w, r)
	if !ok {
		return
	}

	err := s.writeGroup(func(tx *gorm.DB, known map[types.UserID]bool) error {
		err := hsdb.DeleteDeviceAccessForPrincipal(tx, types.PrincipalDirectoryGroup, strconv.FormatUint(g.ID, 10))
		if err != nil {
			return err
		}

		return hsdb.DeleteDirectoryGroup(tx, g.ID)
	})
	if err != nil {
		scimError(w, http.StatusInternalServerError, err.Error())

		return
	}

	s.audit("scim.group.delete", g.DisplayName, "")
	w.WriteHeader(http.StatusNoContent)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}

	return strconv.Itoa(n) + " " + word + "s"
}
