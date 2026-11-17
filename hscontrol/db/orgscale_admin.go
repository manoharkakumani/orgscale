package db

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Tables for orgscale's admin console: settings editable at runtime,
// directory groups synced from the identity provider, user access state and
// the audit log. The SQLite DDL must match schema.sql.

const orgscaleAdminDDLSQLite = `
CREATE TABLE settings(
  key text PRIMARY KEY,
  value text NOT NULL,
  updated_at datetime,
  updated_by text
);
CREATE TABLE directory_groups(
  id integer PRIMARY KEY AUTOINCREMENT,
  source text NOT NULL,
  external_id text NOT NULL,
  name text NOT NULL,
  display_name text,
  created_at datetime,
  updated_at datetime
);
CREATE UNIQUE INDEX idx_directory_groups_source_external_id ON directory_groups(source, external_id);
CREATE TABLE directory_group_members(
  group_id integer NOT NULL,
  user_id integer NOT NULL,
  PRIMARY KEY(group_id, user_id),
  CONSTRAINT fk_directory_group_members_group FOREIGN KEY(group_id) REFERENCES directory_groups(id) ON DELETE CASCADE,
  CONSTRAINT fk_directory_group_members_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_directory_group_members_user_id ON directory_group_members(user_id);
CREATE TABLE user_access(
  user_id integer PRIMARY KEY,
  disabled_at datetime,
  disabled_by text,
  CONSTRAINT fk_user_access_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE audit_events(
  id integer PRIMARY KEY AUTOINCREMENT,
  created_at datetime NOT NULL,
  actor text NOT NULL,
  actor_kind text NOT NULL,
  action text NOT NULL,
  target text,
  detail text
);
CREATE INDEX idx_audit_events_created_at ON audit_events(created_at);
CREATE TABLE device_access(
  node_id integer NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  created_at datetime,
  PRIMARY KEY(node_id, principal_kind, principal),
  CONSTRAINT fk_device_access_node FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);
`

const orgscaleAdminDDLPostgres = `
CREATE TABLE settings(
  key text PRIMARY KEY,
  value text NOT NULL,
  updated_at timestamptz,
  updated_by text
);
CREATE TABLE directory_groups(
  id bigserial PRIMARY KEY,
  source text NOT NULL,
  external_id text NOT NULL,
  name text NOT NULL,
  display_name text,
  created_at timestamptz,
  updated_at timestamptz
);
CREATE UNIQUE INDEX idx_directory_groups_source_external_id ON directory_groups(source, external_id);
CREATE TABLE directory_group_members(
  group_id bigint NOT NULL,
  user_id bigint NOT NULL,
  PRIMARY KEY(group_id, user_id),
  CONSTRAINT fk_directory_group_members_group FOREIGN KEY(group_id) REFERENCES directory_groups(id) ON DELETE CASCADE,
  CONSTRAINT fk_directory_group_members_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_directory_group_members_user_id ON directory_group_members(user_id);
CREATE TABLE user_access(
  user_id bigint PRIMARY KEY,
  disabled_at timestamptz,
  disabled_by text,
  CONSTRAINT fk_user_access_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE audit_events(
  id bigserial PRIMARY KEY,
  created_at timestamptz NOT NULL,
  actor text NOT NULL,
  actor_kind text NOT NULL,
  action text NOT NULL,
  target text,
  detail text
);
CREATE INDEX idx_audit_events_created_at ON audit_events(created_at);
CREATE TABLE device_access(
  node_id bigint NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  created_at timestamptz,
  PRIMARY KEY(node_id, principal_kind, principal),
  CONSTRAINT fk_device_access_node FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);
`

// ensureOrgscaleAdminTables creates the admin console tables unless they
// exist. It runs both from InitSchema (fresh databases) and as a migration.
func ensureOrgscaleAdminTables(tx *gorm.DB) error {
	if tx.Migrator().HasTable("settings") {
		return nil
	}

	ddl := orgscaleAdminDDLSQLite
	if tx.Name() != "sqlite" {
		ddl = orgscaleAdminDDLPostgres
	}

	for stmt := range strings.SplitSeq(ddl, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}

		err := tx.Exec(stmt).Error
		if err != nil {
			return fmt.Errorf("creating orgscale admin tables: %w", err)
		}
	}

	return nil
}

// ErrSettingNotFound is returned for a setting that is not stored.
var ErrSettingNotFound = errors.New("setting not found")

// GetSetting returns the stored value of key, or [ErrSettingNotFound].
func (hsdb *HSDatabase) GetSetting(key string) (string, error) {
	return Read(hsdb.DB, func(rx *gorm.DB) (string, error) {
		var s types.Setting

		err := rx.Where("key = ?", key).Take(&s).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSettingNotFound
		}

		if err != nil {
			return "", fmt.Errorf("reading setting %q: %w", key, err)
		}

		return s.Value, nil
	})
}

// ListSettings returns every stored setting.
func (hsdb *HSDatabase) ListSettings() ([]types.Setting, error) {
	return Read(hsdb.DB, func(rx *gorm.DB) ([]types.Setting, error) {
		var s []types.Setting

		err := rx.Order("key").Find(&s).Error
		if err != nil {
			return nil, fmt.Errorf("listing settings: %w", err)
		}

		return s, nil
	})
}

// SetSetting stores value under key, recording who changed it.
func (hsdb *HSDatabase) SetSetting(key, value, by string) error {
	return hsdb.Write(func(tx *gorm.DB) error {
		s := types.Setting{Key: key, Value: value, UpdatedAt: time.Now().UTC(), UpdatedBy: by}

		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at", "updated_by"}),
		}).Create(&s).Error
	})
}

// DeleteSetting removes key, so it falls back to the configuration file.
func (hsdb *HSDatabase) DeleteSetting(key string) error {
	return hsdb.Write(func(tx *gorm.DB) error {
		return tx.Where("key = ?", key).Delete(&types.Setting{}).Error
	})
}

// DirectoryGroupName turns an identity provider group name into the name used
// in policies: lower case, with every run of characters other than letters,
// digits, '.', '_' and '-' replaced by a single '-'. "Engineering Team"
// becomes "engineering-team", usable as `group:engineering-team`.
func DirectoryGroupName(name string) string {
	var b strings.Builder

	dash := false

	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)

			dash = false

			continue
		}

		if !dash && b.Len() > 0 {
			b.WriteByte('-')

			dash = true
		}
	}

	return strings.TrimRight(b.String(), "-")
}

// UpsertDirectoryGroup creates or renames the group identified by source and
// externalID, and returns it.
func UpsertDirectoryGroup(tx *gorm.DB, source, externalID, displayName string) (*types.DirectoryGroup, error) {
	name := DirectoryGroupName(displayName)
	if name == "" {
		name = DirectoryGroupName(externalID)
	}

	if name == "" {
		return nil, fmt.Errorf("directory group %q has no usable name", externalID)
	}

	var g types.DirectoryGroup

	err := tx.Where("source = ? AND external_id = ?", source, externalID).Take(&g).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("reading directory group: %w", err)
	}

	// Names must be unique: two IdP groups that normalise to the same name
	// would otherwise share one policy group and each other's access.
	name, err = uniqueDirectoryGroupName(tx, name, source, g.ID)
	if err != nil {
		return nil, err
	}

	if g.ID == 0 {
		now := time.Now().UTC()
		g = types.DirectoryGroup{
			Source:      source,
			ExternalID:  externalID,
			Name:        name,
			DisplayName: displayName,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		err = tx.Create(&g).Error
		if err != nil {
			return nil, fmt.Errorf("creating directory group: %w", err)
		}

		return &g, nil
	}

	if g.Name != name || g.DisplayName != displayName {
		g.Name, g.DisplayName, g.UpdatedAt = name, displayName, time.Now().UTC()

		err = tx.Model(&g).Updates(map[string]any{
			"name": g.Name, "display_name": g.DisplayName, "updated_at": g.UpdatedAt,
		}).Error
		if err != nil {
			return nil, fmt.Errorf("renaming directory group: %w", err)
		}
	}

	return &g, nil
}

// uniqueDirectoryGroupName returns base, or base suffixed with the source and
// then a counter, whichever no group other than self holds.
func uniqueDirectoryGroupName(tx *gorm.DB, base, source string, self uint64) (string, error) {
	candidates := []string{base, base + "-" + source}

	for i := 2; i < 1000; i++ {
		var candidate string
		if i-2 < len(candidates) {
			candidate = candidates[i-2]
		} else {
			candidate = fmt.Sprintf("%s-%s-%d", base, source, i-len(candidates))
		}

		var n int64

		err := tx.Model(&types.DirectoryGroup{}).Where("name = ? AND id <> ?", candidate, self).Count(&n).Error
		if err != nil {
			return "", fmt.Errorf("checking directory group name: %w", err)
		}

		if n == 0 {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no free name for directory group %q", base)
}

// GetDirectoryGroup returns a group with its members.
func GetDirectoryGroup(rx *gorm.DB, id uint64) (*types.DirectoryGroup, error) {
	var g types.DirectoryGroup

	err := rx.Where("id = ?", id).Take(&g).Error
	if err != nil {
		return nil, fmt.Errorf("reading directory group %d: %w", id, err)
	}

	err = rx.Model(&types.DirectoryGroupMember{}).Where("group_id = ?", id).
		Order("user_id").Pluck("user_id", &g.MemberIDs).Error
	if err != nil {
		return nil, fmt.Errorf("reading directory group members: %w", err)
	}

	return &g, nil
}

// SetDirectoryGroupMembers replaces the members of group id.
func SetDirectoryGroupMembers(tx *gorm.DB, id uint64, users []types.UserID) error {
	err := tx.Where("group_id = ?", id).Delete(&types.DirectoryGroupMember{}).Error
	if err != nil {
		return fmt.Errorf("clearing directory group members: %w", err)
	}

	slices.Sort(users)
	users = slices.Compact(users)

	if len(users) == 0 {
		return nil
	}

	rows := make([]types.DirectoryGroupMember, 0, len(users))
	for _, u := range users {
		rows = append(rows, types.DirectoryGroupMember{GroupID: id, UserID: u})
	}

	err = tx.Create(&rows).Error
	if err != nil {
		return fmt.Errorf("adding directory group members: %w", err)
	}

	return nil
}

// AddDirectoryGroupMembers adds users to group id, ignoring existing members.
func AddDirectoryGroupMembers(tx *gorm.DB, id uint64, users []types.UserID) error {
	for _, u := range users {
		err := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&types.DirectoryGroupMember{GroupID: id, UserID: u}).Error
		if err != nil {
			return fmt.Errorf("adding directory group member: %w", err)
		}
	}

	return nil
}

// RemoveDirectoryGroupMembers removes users from group id.
func RemoveDirectoryGroupMembers(tx *gorm.DB, id uint64, users []types.UserID) error {
	if len(users) == 0 {
		return nil
	}

	return tx.Where("group_id = ? AND user_id IN ?", id, users).
		Delete(&types.DirectoryGroupMember{}).Error
}

// DeleteDirectoryGroup removes group id and its memberships.
func DeleteDirectoryGroup(tx *gorm.DB, id uint64) error {
	return tx.Where("id = ?", id).Delete(&types.DirectoryGroup{}).Error
}

// DeleteDirectoryGroupsExcept removes the groups of source whose external ID
// is not in keep. A full sync uses it to drop groups deleted at the IdP.
func DeleteDirectoryGroupsExcept(tx *gorm.DB, source string, keep []string) (int64, error) {
	q := tx.Where("source = ?", source)
	if len(keep) > 0 {
		q = q.Where("external_id NOT IN ?", keep)
	}

	res := q.Delete(&types.DirectoryGroup{})

	return res.RowsAffected, res.Error
}

// SetUserOIDCGroups records the groups from a user's OIDC groups claim:
// the user joins each named oidc-sourced group, creating it if needed, and
// leaves the oidc-sourced groups not named. Groups from SCIM or Google are
// left alone.
func SetUserOIDCGroups(tx *gorm.DB, uid types.UserID, groups []string) error {
	keep := make([]uint64, 0, len(groups))

	for _, name := range groups {
		if DirectoryGroupName(name) == "" {
			continue
		}

		g, err := UpsertDirectoryGroup(tx, types.DirectorySourceOIDC, name, name)
		if err != nil {
			return err
		}

		err = AddDirectoryGroupMembers(tx, g.ID, []types.UserID{uid})
		if err != nil {
			return err
		}

		keep = append(keep, g.ID)
	}

	q := tx.Where("user_id = ? AND group_id IN (?)", uid,
		tx.Model(&types.DirectoryGroup{}).Select("id").Where("source = ?", types.DirectorySourceOIDC))
	if len(keep) > 0 {
		q = q.Where("group_id NOT IN ?", keep)
	}

	err := q.Delete(&types.DirectoryGroupMember{}).Error
	if err != nil {
		return fmt.Errorf("leaving stale oidc groups: %w", err)
	}

	return nil
}

// ListDirectoryGroups returns every directory group with its members, ordered
// by name.
func (hsdb *HSDatabase) ListDirectoryGroups() ([]types.DirectoryGroup, error) {
	return Read(hsdb.DB, ListDirectoryGroups)
}

// ListDirectoryGroups returns every directory group with its members.
func ListDirectoryGroups(rx *gorm.DB) ([]types.DirectoryGroup, error) {
	var groups []types.DirectoryGroup

	err := rx.Order("name, source, id").Find(&groups).Error
	if err != nil {
		return nil, fmt.Errorf("listing directory groups: %w", err)
	}

	var members []types.DirectoryGroupMember

	err = rx.Joins("JOIN users ON users.id = directory_group_members.user_id AND users.deleted_at IS NULL").
		Order("directory_group_members.user_id").Find(&members).Error
	if err != nil {
		return nil, fmt.Errorf("listing directory group members: %w", err)
	}

	byGroup := make(map[uint64][]types.UserID, len(groups))
	for _, m := range members {
		byGroup[m.GroupID] = append(byGroup[m.GroupID], m.UserID)
	}

	for i := range groups {
		groups[i].MemberIDs = byGroup[groups[i].ID]
	}

	return groups, nil
}

// GetUserAccess returns the access state of uid; a user never disabled has
// no row and is returned enabled.
func GetUserAccess(rx *gorm.DB, uid types.UserID) (types.UserAccess, error) {
	var a types.UserAccess

	err := rx.Where("user_id = ?", uid).Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return types.UserAccess{UserID: uid}, nil
	}

	return a, err
}

// IsUserDisabled reports whether uid is disabled.
func (hsdb *HSDatabase) IsUserDisabled(uid types.UserID) (bool, error) {
	a, err := Read(hsdb.DB, func(rx *gorm.DB) (types.UserAccess, error) {
		return GetUserAccess(rx, uid)
	})

	return a.DisabledAt != nil, err
}

// SetUserDisabled records uid as disabled or enabled.
func SetUserDisabled(tx *gorm.DB, uid types.UserID, disabled bool, by string) error {
	if !disabled {
		return tx.Where("user_id = ?", uid).Delete(&types.UserAccess{}).Error
	}

	now := time.Now().UTC()

	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"disabled_at", "disabled_by"}),
	}).Create(&types.UserAccess{UserID: uid, DisabledAt: &now, DisabledBy: by}).Error
}

// ListDisabledUsers returns the access rows of every disabled user.
func (hsdb *HSDatabase) ListDisabledUsers() (map[types.UserID]types.UserAccess, error) {
	return Read(hsdb.DB, func(rx *gorm.DB) (map[types.UserID]types.UserAccess, error) {
		var rows []types.UserAccess

		err := rx.Where("disabled_at IS NOT NULL").Find(&rows).Error
		if err != nil {
			return nil, fmt.Errorf("listing disabled users: %w", err)
		}

		out := make(map[types.UserID]types.UserAccess, len(rows))
		for _, r := range rows {
			out[r.UserID] = r
		}

		return out, nil
	})
}

// AppendAuditEvent adds ev to the audit log; CreatedAt defaults to now.
func (hsdb *HSDatabase) AppendAuditEvent(ev types.AuditEvent) error {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}

	return hsdb.Write(func(tx *gorm.DB) error {
		return tx.Create(&ev).Error
	})
}

// ListAuditEvents returns up to limit events, newest first, older than
// beforeID when it is not zero.
func (hsdb *HSDatabase) ListAuditEvents(limit int, beforeID uint64) ([]types.AuditEvent, error) {
	return Read(hsdb.DB, func(rx *gorm.DB) ([]types.AuditEvent, error) {
		q := rx.Order("id DESC").Limit(limit)
		if beforeID > 0 {
			q = q.Where("id < ?", beforeID)
		}

		var evs []types.AuditEvent

		err := q.Find(&evs).Error
		if err != nil {
			return nil, fmt.Errorf("listing audit events: %w", err)
		}

		return evs, nil
	})
}

// ListDeviceAccess returns every device access grant.
func (hsdb *HSDatabase) ListDeviceAccess() ([]types.DeviceAccess, error) {
	return Read(hsdb.DB, ListDeviceAccess)
}

// ListDeviceAccess returns every device access grant, ordered by node.
func ListDeviceAccess(rx *gorm.DB) ([]types.DeviceAccess, error) {
	var rows []types.DeviceAccess

	err := rx.Order("node_id, principal_kind, principal").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("listing device access: %w", err)
	}

	return rows, nil
}

// SetDeviceAccess replaces the grants of node with grants.
func (hsdb *HSDatabase) SetDeviceAccess(node types.NodeID, grants []types.DeviceAccess) error {
	return hsdb.Write(func(tx *gorm.DB) error {
		err := tx.Where("node_id = ?", node).Delete(&types.DeviceAccess{}).Error
		if err != nil {
			return fmt.Errorf("clearing device access: %w", err)
		}

		now := time.Now().UTC()
		seen := make(map[[2]string]bool, len(grants))

		for _, g := range grants {
			k := [2]string{g.PrincipalKind, g.Principal}
			if seen[k] {
				continue
			}

			seen[k] = true

			g.NodeID, g.CreatedAt = node, now

			err = tx.Create(&g).Error
			if err != nil {
				return fmt.Errorf("adding device access: %w", err)
			}
		}

		return nil
	})
}

// DeleteDeviceAccessForPrincipal removes every grant naming the principal,
// as when a user or group is deleted.
func DeleteDeviceAccessForPrincipal(tx *gorm.DB, kind, principal string) error {
	return tx.Where("principal_kind = ? AND principal = ?", kind, principal).
		Delete(&types.DeviceAccess{}).Error
}
