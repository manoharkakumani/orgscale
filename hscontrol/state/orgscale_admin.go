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
	"gorm.io/gorm"
	"tailscale.com/types/views"
)

// PolicyUserToken is how orgscale names u in the policies it generates: the
// email when there is one, the username with a trailing "@" otherwise.
func PolicyUserToken(u types.User) string {
	if u.Email != "" {
		return u.Email
	}

	return u.Name + "@"
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

	byID := make(map[types.UserID]types.User, len(users))
	for _, u := range users {
		byID[types.UserID(u.ID)] = u
	}

	groups, err := db.ListDirectoryGroups()
	if err != nil {
		return m, err
	}

	groupName := make(map[uint64]string, len(groups))
	m.Groups = make(map[string][]string, len(groups))

	for _, g := range groups {
		groupName[g.ID] = g.Name

		members := m.Groups[g.Name]
		for _, uid := range g.MemberIDs {
			if u, ok := byID[uid]; ok {
				members = append(members, PolicyUserToken(u))
			}
		}

		slices.Sort(members)
		m.Groups[g.Name] = slices.Compact(members)
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

		var src []string

		for _, g := range gs {
			switch g.PrincipalKind {
			case types.PrincipalUser:
				id, err := strconv.ParseUint(g.Principal, 10, 64)
				if err != nil {
					continue
				}

				if u, ok := byID[types.UserID(id)]; ok {
					src = append(src, PolicyUserToken(u))
				}
			case types.PrincipalDirectoryGroup:
				id, err := strconv.ParseUint(g.Principal, 10, 64)
				if err != nil {
					continue
				}

				if name, ok := groupName[id]; ok {
					src = append(src, "group:"+name)
				}
			case types.PrincipalPolicyGroup:
				src = append(src, "group:"+strings.TrimPrefix(g.Principal, "group:"))
			}
		}

		slices.Sort(src)
		src = slices.Compact(src)

		m.Rules = append(m.Rules, managed.Rule{
			Comment: n.Hostname(),
			Src:     src,
			Dst:     n.IPsAsString(),
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
