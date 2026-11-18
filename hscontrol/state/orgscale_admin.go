package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/policy/managed"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/manoharkakumani/orgscale/hscontrol/types/change"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"tailscale.com/types/views"
)

// policyUserTokens returns, for each user that can be named without
// ambiguity, the token the generated policy uses for them. The policy
// rejects a token that matches more than one user, which would fail every
// policy compile, so a user without a unique token is left out (and so gets
// no generated access) rather than named ambiguously:
//
//   - a provider identifier is unique and matched first, before email or name;
//   - an email is used when no other user has that email;
//   - a username, with a trailing "@", when no user has it as email and no
//     other user has that name.
func policyUserTokens(users []types.User) map[types.UserID]string {
	emails := make(map[string]int, len(users))
	names := make(map[string]int, len(users))

	for _, u := range users {
		if u.Email != "" {
			emails[u.Email]++
		}

		names[u.Name]++
	}

	out := make(map[types.UserID]string, len(users))

	for _, u := range users {
		id := types.UserID(u.ID)

		switch {
		case u.ProviderIdentifier.Valid && u.ProviderIdentifier.String != "":
			out[id] = strings.TrimSuffix(u.ProviderIdentifier.String, "@") + "@"
		case usableToken(u.Email) && strings.Count(u.Email, "@") == 1 && emails[u.Email] == 1:
			out[id] = u.Email
		case usableToken(u.Name) && !strings.Contains(u.Name, "@") && emails[u.Name] == 0 && names[u.Name] == 1:
			out[id] = u.Name + "@"
		default:
			log.Warn().Uint("user", u.ID).Str("name", u.Name).
				Msg("user cannot be named unambiguously in the policy; no managed access generated for them")
		}
	}

	return out
}

// usableToken reports whether s can appear in a policy as a user name:
// non-empty, with no whitespace, control characters, quotes or ':' (which
// would make it parse as something else).
func usableToken(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == 0x7f || r == '"' || r == ':' || r == ',' || r == '*'
	})
}

// managedPolicy builds what orgscale adds to the administrator's policy from
// the settings, directory groups and device access grants in db.
func managedPolicy(db *hsdb.HSDatabase, users []types.User, nodes views.Slice[types.NodeView]) (managed.Policy, error) {
	var m managed.Policy

	restrict, err := db.GetSetting(types.SettingDeviceAccessEnabled)
	if err != nil && !errors.Is(err, hsdb.ErrSettingNotFound) {
		return m, err
	}

	m.RestrictDevices = restrict == "true"

	tokens := policyUserTokens(users)

	groups, err := db.ListDirectoryGroups()
	if err != nil {
		return m, err
	}

	// Directory group names are unique (see [hsdb.UpsertDirectoryGroup]);
	// a device grant to a group expands to that group's own members.
	groupMembers := make(map[uint64][]string, len(groups))
	m.Groups = make(map[string][]string, len(groups))

	for _, g := range groups {
		var members []string

		for _, uid := range g.MemberIDs {
			if t, ok := tokens[uid]; ok {
				members = append(members, t)
			}
		}

		slices.Sort(members)
		members = slices.Compact(members)

		groupMembers[g.ID] = members
		m.Groups[g.Name] = members
	}

	serverTags, err := hsdb.Read(db.DB, hsdb.ListServerTags)
	if err != nil {
		return m, err
	}

	for _, t := range serverTags {
		m.Tags = append(m.Tags, t.Name)
	}

	nodeIPs := make(map[string][]string, nodes.Len())
	for _, n := range nodes.All() {
		nodeIPs[strconv.FormatUint(uint64(n.ID()), 10)] = n.IPsAsString()
	}

	// resolve turns a principal into policy aliases; one that no longer
	// exists resolves to nothing.
	resolve := func(kind, principal string) []string {
		switch kind {
		case types.PrincipalUser:
			id, err := strconv.ParseUint(principal, 10, 64)
			if err != nil {
				return nil
			}

			if t, ok := tokens[types.UserID(id)]; ok {
				return []string{t}
			}
		case types.PrincipalDirectoryGroup:
			id, err := strconv.ParseUint(principal, 10, 64)
			if err != nil {
				return nil
			}

			return groupMembers[id]
		case types.PrincipalPolicyGroup:
			return []string{"group:" + strings.TrimPrefix(principal, "group:")}
		case types.PrincipalTag:
			return []string{principal}
		case types.PrincipalDevice:
			return nodeIPs[principal]
		case types.PrincipalEveryone:
			return []string{"autogroup:member"}
		case types.PrincipalSelf:
			return []string{"autogroup:self"}
		}

		return nil
	}

	resolveAll := func(add func(yield func(kind, principal string))) []string {
		var out []string

		add(func(kind, principal string) {
			out = append(out, resolve(kind, principal)...)
		})

		slices.Sort(out)

		return slices.Compact(out)
	}

	rules, err := hsdb.Read(db.DB, hsdb.ListAccessRules)
	if err != nil {
		return m, err
	}

	// SSH rules apply with or without device restriction.
	for _, r := range rules {
		if !r.Enabled || r.Kind != types.RuleKindSSH {
			continue
		}

		var users []string
		_ = json.Unmarshal([]byte(r.SSHUsers), &users)

		side := func(ps []types.RulePrincipal) []string {
			return resolveAll(func(yield func(string, string)) {
				for _, p := range ps {
					yield(p.Kind, p.Principal)
				}
			})
		}

		src, dst := side(r.Sources), side(r.Destinations)
		m.SSH = append(m.SSH, managed.SSHRule{Comment: r.Name, Src: src, Dst: dst, Users: users, CheckPeriod: r.SSHCheckPeriod})

		// SSH needs port 22 open between them when devices are restricted;
		// a person's own devices are open to them already.
		netDst := slices.DeleteFunc(slices.Clone(dst), func(d string) bool { return d == "autogroup:self" })
		if m.RestrictDevices && len(netDst) > 0 {
			m.Rules = append(m.Rules, managed.Rule{Comment: r.Name + " (SSH)", Src: src, Dst: netDst, Ports: "22", Proto: "tcp"})
		}
	}

	if !m.RestrictDevices {
		return m, nil
	}

	grants, err := db.ListDeviceAccess()
	if err != nil {
		return m, err
	}

	byNode := make(map[types.NodeID][]types.DeviceAccess)
	for _, g := range grants {
		byNode[g.NodeID] = append(byNode[g.NodeID], g)
	}

	for _, n := range nodes.All() {
		gs := byNode[n.ID()]
		if len(gs) == 0 {
			continue
		}

		src := resolveAll(func(yield func(string, string)) {
			for _, g := range gs {
				yield(g.PrincipalKind, g.Principal)
			}
		})

		m.Rules = append(m.Rules, managed.Rule{
			Comment: n.Hostname(),
			Src:     src,
			Dst:     n.IPsAsString(),
		})
	}

	tagGrants, err := hsdb.Read(db.DB, hsdb.ListServerTagAccess)
	if err != nil {
		return m, err
	}

	byTag := make(map[string][]types.ServerTagAccess)
	for _, g := range tagGrants {
		byTag[g.Tag] = append(byTag[g.Tag], g)
	}

	for _, t := range serverTags {
		gs := byTag[t.Name]
		if len(gs) == 0 {
			continue
		}

		m.Rules = append(m.Rules, managed.Rule{
			Comment: t.Name,
			Src: resolveAll(func(yield func(string, string)) {
				for _, g := range gs {
					yield(g.PrincipalKind, g.Principal)
				}
			}),
			Dst: []string{t.Name},
		})
	}

	for _, r := range rules {
		if !r.Enabled || r.Kind == types.RuleKindSSH {
			continue
		}

		side := func(ps []types.RulePrincipal) []string {
			return resolveAll(func(yield func(string, string)) {
				for _, p := range ps {
					yield(p.Kind, p.Principal)
				}
			})
		}

		m.Rules = append(m.Rules, managed.Rule{
			Comment: r.Name,
			Src:     side(r.Sources),
			Dst:     side(r.Destinations),
			Ports:   r.Ports,
			Proto:   r.Protocol,
		})
	}

	return m, nil
}

// withManagedPolicy returns pol with orgscale's managed rules merged in.
func withManagedPolicy(
	db *hsdb.HSDatabase,
	pol []byte,
	users []types.User,
	nodes views.Slice[types.NodeView],
) ([]byte, error) {
	m, err := managedPolicy(db, users, nodes)
	if err != nil {
		return nil, fmt.Errorf("building managed policy: %w", err)
	}

	merged, err := managed.Merge(pol, m)
	if err != nil {
		return nil, fmt.Errorf("merging managed policy: %w", err)
	}

	return merged, nil
}

// effectivePolicy merges the managed rules into pol against the current
// users and nodes.
func (s *State) effectivePolicy(pol []byte) ([]byte, error) {
	users, err := s.db.ListUsers(nil)
	if err != nil {
		return nil, fmt.Errorf("loading users: %w", err)
	}

	merged, err := withManagedPolicy(s.db, pol, users, s.ListNodes())
	if err != nil {
		return nil, err
	}

	// No policy is never installed on reload, so the managed rules of a
	// restriction just turned off would stay live. "{}" is the explicit
	// allow-all; install it once restriction has ever been set.
	if len(merged) == 0 {
		_, err := s.db.GetSetting(types.SettingDeviceAccessEnabled)
		if err == nil {
			return []byte("{}"), nil
		}
	}

	return merged, nil
}

// EffectivePolicy returns the policy that is compiled: the stored policy with
// the managed groups and device rules merged in.
func (s *State) EffectivePolicy() ([]byte, error) {
	pol, err := hsdb.PolicyBytes(s.db.DB, s.cfg)
	if err != nil {
		return nil, err
	}

	return s.effectivePolicy(pol)
}

// RawPolicy returns the stored policy as written, without managed rules.
func (s *State) RawPolicy() ([]byte, error) {
	return hsdb.PolicyBytes(s.db.DB, s.cfg)
}

// GetSetting returns a stored setting, or [hsdb.ErrSettingNotFound].
func (s *State) GetSetting(key string) (string, error) {
	return s.db.GetSetting(key)
}

// GetSettingJSON decodes a stored JSON setting into v. It reports false
// without error when the setting is not stored.
func (s *State) GetSettingJSON(key string, v any) (bool, error) {
	raw, err := s.db.GetSetting(key)
	if errors.Is(err, hsdb.ErrSettingNotFound) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	err = json.Unmarshal([]byte(raw), v)
	if err != nil {
		return false, fmt.Errorf("decoding setting %q: %w", key, err)
	}

	return true, nil
}

// SetSettingJSON stores v as JSON under key.
func (s *State) SetSettingJSON(key string, v any, by string) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	return s.db.SetSetting(key, string(b), by)
}

// SetSetting stores a raw setting value.
func (s *State) SetSetting(key, value, by string) error {
	return s.db.SetSetting(key, value, by)
}

// DeleteSetting removes key, falling back to the configuration file.
func (s *State) DeleteSetting(key string) error {
	return s.db.DeleteSetting(key)
}

// ListSettings returns every stored setting.
func (s *State) ListSettings() ([]types.Setting, error) {
	return s.db.ListSettings()
}

// ListDirectoryGroups returns the directory groups with their members.
func (s *State) ListDirectoryGroups() ([]types.DirectoryGroup, error) {
	return s.db.ListDirectoryGroups()
}

// ListDeviceAccess returns every device access grant.
func (s *State) ListDeviceAccess() ([]types.DeviceAccess, error) {
	return s.db.ListDeviceAccess()
}

// SetDeviceAccess replaces the grants of node and recompiles the policy.
func (s *State) SetDeviceAccess(node types.NodeID, grants []types.DeviceAccess) ([]change.Change, error) {
	if _, ok := s.GetNodeByID(node); !ok {
		return nil, hsdb.ErrNodeNotFound
	}

	err := s.db.SetDeviceAccess(node, grants)
	if err != nil {
		return nil, err
	}

	return s.ReloadPolicy()
}

// SetDeviceRestriction turns device restriction on or off and recompiles the
// policy. Turning it on with a policy that does not compile leaves it off.
func (s *State) SetDeviceRestriction(on bool, by string) ([]change.Change, error) {
	prev, err := s.db.GetSetting(types.SettingDeviceAccessEnabled)
	if err != nil && !errors.Is(err, hsdb.ErrSettingNotFound) {
		return nil, err
	}

	err = s.db.SetSetting(types.SettingDeviceAccessEnabled, strconv.FormatBool(on), by)
	if err != nil {
		return nil, err
	}

	changes, err := s.ReloadPolicy()
	if err != nil {
		// Put the previous value back so the live policy and the setting agree.
		if prev == "" {
			_ = s.db.DeleteSetting(types.SettingDeviceAccessEnabled)
		} else {
			_ = s.db.SetSetting(types.SettingDeviceAccessEnabled, prev, by)
		}

		return nil, err
	}

	return changes, nil
}

// DeviceRestrictionEnabled reports whether device restriction is on.
func (s *State) DeviceRestrictionEnabled() bool {
	v, _ := s.db.GetSetting(types.SettingDeviceAccessEnabled)

	return v == "true"
}

// SyncUserOIDCGroups records the groups claim of a user's latest login and
// recompiles the policy when directory groups are in use.
func (s *State) SyncUserOIDCGroups(uid types.UserID, groups []string) ([]change.Change, error) {
	err := s.db.Write(func(tx *gorm.DB) error {
		return hsdb.SetUserOIDCGroups(tx, uid, groups)
	})
	if err != nil {
		return nil, fmt.Errorf("recording oidc groups: %w", err)
	}

	return s.ReloadPolicy()
}

// WriteDirectory runs fn in one database transaction, for directory syncs
// that change several groups and members at once, then recompiles the
// policy.
func (s *State) WriteDirectory(fn func(tx *gorm.DB) error) ([]change.Change, error) {
	err := s.db.Write(fn)
	if err != nil {
		return nil, err
	}

	return s.ReloadPolicy()
}

// ReadDirectory runs fn in a read transaction.
func (s *State) ReadDirectory(fn func(rx *gorm.DB) error) error {
	return s.db.Read(fn)
}

// IsUserDisabled reports whether uid is disabled.
func (s *State) IsUserDisabled(uid types.UserID) (bool, error) {
	return s.db.IsUserDisabled(uid)
}

// ListDisabledUsers returns every disabled user's access row.
func (s *State) ListDisabledUsers() (map[types.UserID]types.UserAccess, error) {
	return s.db.ListDisabledUsers()
}

// SetUserDisabled disables or re-enables uid. Disabling deletes the user's
// devices and blocks new logins; the user and their group memberships stay.
func (s *State) SetUserDisabled(uid types.UserID, disabled bool, by string) ([]change.Change, error) {
	_, err := s.db.GetUserByID(uid)
	if err != nil {
		return nil, err
	}

	err = s.db.Write(func(tx *gorm.DB) error {
		return hsdb.SetUserDisabled(tx, uid, disabled, by)
	})
	if err != nil {
		return nil, fmt.Errorf("setting user access: %w", err)
	}

	if !disabled {
		return nil, nil
	}

	// Expire the user's own pre-auth keys so they cannot register new
	// devices. Tagged keys belong to their tags and stay.
	keys, err := s.ListPreAuthKeys()
	if err != nil {
		return nil, fmt.Errorf("listing pre-auth keys: %w", err)
	}

	for _, k := range keys {
		if k.UserID == nil || *k.UserID != uint(uid) || k.IsTagged() {
			continue
		}

		err = s.ExpirePreAuthKey(k.ID)
		if err != nil {
			return nil, fmt.Errorf("expiring pre-auth key %d: %w", k.ID, err)
		}
	}

	return s.deleteUserNodes(uid)
}

// deleteUserNodes deletes every node uid owns.
func (s *State) deleteUserNodes(uid types.UserID) ([]change.Change, error) {
	var changes []change.Change

	for _, n := range s.ListNodesByUser(uid).All() {
		cs, err := s.DeleteNode(n)
		changes = append(changes, cs...)

		if err != nil {
			return changes, fmt.Errorf("deleting node %d: %w", n.ID(), err)
		}
	}

	return changes, nil
}

// RemoveUser offboards uid: deletes their devices, their pre-auth keys,
// device grants naming them, and the user. Tagged devices created with the
// user's keys stay; they belong to their tags.
func (s *State) RemoveUser(uid types.UserID) ([]change.Change, error) {
	_, err := s.db.GetUserByID(uid)
	if err != nil {
		return nil, err
	}

	changes, err := s.deleteUserNodes(uid)
	if err != nil {
		return changes, err
	}

	err = s.db.Write(func(tx *gorm.DB) error {
		return hsdb.DeleteDeviceAccessForPrincipal(tx, types.PrincipalUser, strconv.FormatUint(uint64(uid), 10))
	})
	if err != nil {
		return changes, fmt.Errorf("removing device grants: %w", err)
	}

	c, err := s.DeleteUser(uid)
	if err != nil {
		return changes, fmt.Errorf("deleting user: %w", err)
	}

	changes = append(changes, c)

	reload, err := s.ReloadPolicy()

	return append(changes, reload...), err
}

// AppendAuditEvent adds an event to the audit log.
func (s *State) AppendAuditEvent(ev types.AuditEvent) error {
	return s.db.AppendAuditEvent(ev)
}

// ListAuditEvents returns audit events, newest first.
func (s *State) ListAuditEvents(limit int, beforeID uint64) ([]types.AuditEvent, error) {
	return s.db.ListAuditEvents(limit, beforeID)
}
