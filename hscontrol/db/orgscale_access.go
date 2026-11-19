package db

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"gorm.io/gorm"
)

// Tables for server tags and the access rules built in the console. The
// SQLite DDL must match schema.sql.

const orgscaleAccessDDLSQLite = `
CREATE TABLE server_tags(
  name text PRIMARY KEY,
  description text,
  created_at datetime,
  created_by text,
  request_max_minutes integer NOT NULL DEFAULT 0,
  request_ssh_users text
);
CREATE TABLE server_tag_access(
  tag text NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  created_at datetime,
  PRIMARY KEY(tag, principal_kind, principal),
  CONSTRAINT fk_server_tag_access_tag FOREIGN KEY(tag) REFERENCES server_tags(name) ON DELETE CASCADE
);
CREATE TABLE access_rules(
  id integer PRIMARY KEY AUTOINCREMENT,
  name text NOT NULL,
  protocol text NOT NULL DEFAULT '',
  ports text NOT NULL DEFAULT '*',
  position integer NOT NULL DEFAULT 0,
  enabled numeric NOT NULL DEFAULT true,
  kind text NOT NULL DEFAULT 'network',
  ssh_users text,
  ssh_check_period text,
  created_at datetime,
  updated_at datetime,
  updated_by text
);
CREATE TABLE access_rule_principals(
  rule_id integer NOT NULL,
  side text NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  PRIMARY KEY(rule_id, side, principal_kind, principal),
  CONSTRAINT fk_access_rule_principals_rule FOREIGN KEY(rule_id) REFERENCES access_rules(id) ON DELETE CASCADE
);
`

const orgscaleAccessDDLPostgres = `
CREATE TABLE server_tags(
  name text PRIMARY KEY,
  description text,
  created_at timestamptz,
  created_by text,
  request_max_minutes integer NOT NULL DEFAULT 0,
  request_ssh_users text
);
CREATE TABLE server_tag_access(
  tag text NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  created_at timestamptz,
  PRIMARY KEY(tag, principal_kind, principal),
  CONSTRAINT fk_server_tag_access_tag FOREIGN KEY(tag) REFERENCES server_tags(name) ON DELETE CASCADE
);
CREATE TABLE access_rules(
  id bigserial PRIMARY KEY,
  name text NOT NULL,
  protocol text NOT NULL DEFAULT '',
  ports text NOT NULL DEFAULT '*',
  position integer NOT NULL DEFAULT 0,
  enabled boolean NOT NULL DEFAULT true,
  kind text NOT NULL DEFAULT 'network',
  ssh_users text,
  ssh_check_period text,
  created_at timestamptz,
  updated_at timestamptz,
  updated_by text
);
CREATE TABLE access_rule_principals(
  rule_id bigint NOT NULL,
  side text NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  PRIMARY KEY(rule_id, side, principal_kind, principal),
  CONSTRAINT fk_access_rule_principals_rule FOREIGN KEY(rule_id) REFERENCES access_rules(id) ON DELETE CASCADE
);
`

const orgscaleSessionsDDLSQLite = `
CREATE TABLE admin_sessions(
  id text PRIMARY KEY,
  csrf text NOT NULL,
  user_id integer,
  email text,
  name text,
  picture text,
  role_email text,
  role_groups text,
  subject text,
  remote_addr text,
  user_agent text,
  created_at datetime,
  last_seen_at datetime,
  expires_at datetime NOT NULL
);
CREATE INDEX idx_admin_sessions_expires_at ON admin_sessions(expires_at);
`

const orgscaleSessionsDDLPostgres = `
CREATE TABLE admin_sessions(
  id text PRIMARY KEY,
  csrf text NOT NULL,
  user_id bigint,
  email text,
  name text,
  picture text,
  role_email text,
  role_groups text,
  subject text,
  remote_addr text,
  user_agent text,
  created_at timestamptz,
  last_seen_at timestamptz,
  expires_at timestamptz NOT NULL
);
CREATE INDEX idx_admin_sessions_expires_at ON admin_sessions(expires_at);
`

const accessRequestsDDLSQLite = `
CREATE TABLE access_requests(
  id integer PRIMARY KEY AUTOINCREMENT,
  user_id integer NOT NULL,
  requester text NOT NULL,
  tag text NOT NULL,
  reason text NOT NULL,
  minutes integer NOT NULL,
  ssh numeric NOT NULL DEFAULT false,
  status text NOT NULL,
  created_at datetime NOT NULL,
  decided_by text,
  decided_at datetime,
  decision_note text,
  expires_at datetime,
  grant_kind text,
  grant_principal text
);
CREATE INDEX idx_access_requests_status ON access_requests(status);
`

const accessRequestsDDLPostgres = `
CREATE TABLE access_requests(
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL,
  requester text NOT NULL,
  tag text NOT NULL,
  reason text NOT NULL,
  minutes integer NOT NULL,
  ssh boolean NOT NULL DEFAULT false,
  status text NOT NULL,
  created_at timestamptz NOT NULL,
  decided_by text,
  decided_at timestamptz,
  decision_note text,
  expires_at timestamptz,
  grant_kind text,
  grant_principal text
);
CREATE INDEX idx_access_requests_status ON access_requests(status);
`

// ensureAccessRequests adds the access request table, and the request
// settings to server_tags made before requests existed.
func ensureAccessRequests(tx *gorm.DB) error {
	cols := []struct{ name, ddl string }{
		{"request_max_minutes", "ALTER TABLE server_tags ADD COLUMN request_max_minutes integer NOT NULL DEFAULT 0"},
		{"request_ssh_users", "ALTER TABLE server_tags ADD COLUMN request_ssh_users text"},
	}

	for _, c := range cols {
		if tx.Migrator().HasColumn(&types.ServerTag{}, c.name) {
			continue
		}

		err := tx.Exec(c.ddl).Error
		if err != nil {
			return fmt.Errorf("adding server_tags.%s: %w", c.name, err)
		}
	}

	if tx.Migrator().HasTable("access_requests") {
		for _, c := range []string{"grant_kind", "grant_principal"} {
			if tx.Migrator().HasColumn(&types.AccessRequest{}, c) {
				continue
			}

			err := tx.Exec("ALTER TABLE access_requests ADD COLUMN " + c + " text").Error
			if err != nil {
				return fmt.Errorf("adding access_requests.%s: %w", c, err)
			}
		}

		return nil
	}

	ddl := accessRequestsDDLSQLite
	if tx.Name() != "sqlite" {
		ddl = accessRequestsDDLPostgres
	}

	for stmt := range strings.SplitSeq(ddl, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}

		err := tx.Exec(stmt).Error
		if err != nil {
			return fmt.Errorf("creating access_requests: %w", err)
		}
	}

	return nil
}

// ErrAccessRequestNotFound is returned for an unknown access request.
var ErrAccessRequestNotFound = errors.New("access request not found")

// ListAccessRequests returns access requests, newest first: all of them
// when userID is zero, else that person's.
func ListAccessRequests(rx *gorm.DB, userID uint, limit int) ([]types.AccessRequest, error) {
	q := rx.Order("id DESC")
	if userID != 0 {
		q = q.Where("user_id = ?", userID)
	}

	if limit > 0 {
		q = q.Limit(limit)
	}

	var rows []types.AccessRequest

	err := q.Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("listing access requests: %w", err)
	}

	return rows, nil
}

// ListGrantingAccessRequests returns approved requests that have not
// lapsed by now.
func ListGrantingAccessRequests(rx *gorm.DB, now time.Time) ([]types.AccessRequest, error) {
	var rows []types.AccessRequest

	err := rx.Where("status = ? AND expires_at > ?", types.AccessRequestApproved, now.UTC()).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("listing active access requests: %w", err)
	}

	return rows, nil
}

// GetAccessRequest returns one access request.
func GetAccessRequest(rx *gorm.DB, id uint64) (types.AccessRequest, error) {
	var r types.AccessRequest

	err := rx.Where("id = ?", id).Take(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r, ErrAccessRequestNotFound
	}

	return r, err
}

// CreateAccessRequest stores a new request and returns its ID.
func CreateAccessRequest(tx *gorm.DB, r types.AccessRequest) (uint64, error) {
	r.ID = 0

	err := tx.Create(&r).Error
	if err != nil {
		return 0, fmt.Errorf("creating access request: %w", err)
	}

	return r.ID, nil
}

// UpdateAccessRequest moves a request from one of the states in from to the
// state in r, so two admins deciding at once cannot both win.
func UpdateAccessRequest(tx *gorm.DB, r types.AccessRequest, from ...string) error {
	res := tx.Model(&types.AccessRequest{}).Where("id = ? AND status IN ?", r.ID, from).Updates(map[string]any{
		"status":        r.Status,
		"minutes":       r.Minutes,
		"decided_by":    r.DecidedBy,
		"decided_at":    r.DecidedAt,
		"decision_note": r.DecisionNote,
		"expires_at":    r.ExpiresAt,
	})
	if res.Error != nil {
		return fmt.Errorf("updating access request: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		return ErrAccessRequestNotFound
	}

	return nil
}

// ExpireAccessRequests marks approved requests whose time is up as expired
// and pending ones older than maxPending as expired too. It returns how
// many approved requests lapsed.
func ExpireAccessRequests(tx *gorm.DB, now time.Time, maxPending time.Duration) (int64, error) {
	res := tx.Model(&types.AccessRequest{}).
		Where("status = ? AND expires_at <= ?", types.AccessRequestApproved, now.UTC()).
		Update("status", types.AccessRequestExpired)
	if res.Error != nil {
		return 0, res.Error
	}

	err := tx.Model(&types.AccessRequest{}).
		Where("status = ? AND created_at <= ?", types.AccessRequestPending, now.Add(-maxPending).UTC()).
		Update("status", types.AccessRequestExpired).Error

	return res.RowsAffected, err
}

// ensureSSHRuleColumns adds the SSH columns to access_rules made before SSH
// rules existed.
func ensureSSHRuleColumns(tx *gorm.DB) error {
	cols := []struct{ name, ddl string }{
		{"kind", "ALTER TABLE access_rules ADD COLUMN kind text NOT NULL DEFAULT 'network'"},
		{"ssh_users", "ALTER TABLE access_rules ADD COLUMN ssh_users text"},
		{"ssh_check_period", "ALTER TABLE access_rules ADD COLUMN ssh_check_period text"},
	}

	for _, c := range cols {
		if tx.Migrator().HasColumn(&types.AccessRule{}, c.name) {
			continue
		}

		err := tx.Exec(c.ddl).Error
		if err != nil {
			return fmt.Errorf("adding access_rules.%s: %w", c.name, err)
		}
	}

	return nil
}

// ensureOrgscaleSessionsTable creates the console session table unless it
// exists. It runs from InitSchema and as a migration.
func ensureOrgscaleSessionsTable(tx *gorm.DB) error {
	if tx.Migrator().HasTable("admin_sessions") {
		return nil
	}

	ddl := orgscaleSessionsDDLSQLite
	if tx.Name() != "sqlite" {
		ddl = orgscaleSessionsDDLPostgres
	}

	for stmt := range strings.SplitSeq(ddl, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}

		err := tx.Exec(stmt).Error
		if err != nil {
			return fmt.Errorf("creating admin_sessions: %w", err)
		}
	}

	return nil
}

// ListAdminSessions returns the console sessions that have not expired.
func ListAdminSessions(rx *gorm.DB) ([]types.AdminSessionRecord, error) {
	var rows []types.AdminSessionRecord

	err := rx.Where("expires_at > ?", time.Now().UTC()).Order("created_at").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("listing admin sessions: %w", err)
	}

	return rows, nil
}

// SaveAdminSession stores a console session, replacing one with the same ID.
func SaveAdminSession(tx *gorm.DB, s types.AdminSessionRecord) error {
	err := tx.Where("id = ?", s.ID).Delete(&types.AdminSessionRecord{}).Error
	if err != nil {
		return err
	}

	return tx.Create(&s).Error
}

// TouchAdminSession records that a session was used.
func TouchAdminSession(tx *gorm.DB, id string, at time.Time) error {
	return tx.Model(&types.AdminSessionRecord{}).Where("id = ?", id).Update("last_seen_at", at).Error
}

// DeleteAdminSessions removes sessions by ID, and every expired session.
func DeleteAdminSessions(tx *gorm.DB, ids ...string) error {
	if len(ids) > 0 {
		err := tx.Where("id IN ?", ids).Delete(&types.AdminSessionRecord{}).Error
		if err != nil {
			return err
		}
	}

	return tx.Where("expires_at <= ?", time.Now().UTC()).Delete(&types.AdminSessionRecord{}).Error
}

// ensureOrgscaleAccessTables creates the server tag and access rule tables
// unless they exist. It runs from InitSchema and as a migration.
func ensureOrgscaleAccessTables(tx *gorm.DB) error {
	if tx.Migrator().HasTable("server_tags") {
		return nil
	}

	ddl := orgscaleAccessDDLSQLite
	if tx.Name() != "sqlite" {
		ddl = orgscaleAccessDDLPostgres
	}

	for stmt := range strings.SplitSeq(ddl, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}

		err := tx.Exec(stmt).Error
		if err != nil {
			return fmt.Errorf("creating orgscale access tables: %w", err)
		}
	}

	return nil
}

// ErrServerTagNotFound is returned for a tag the console does not manage.
var ErrServerTagNotFound = errors.New("server tag not found")

// ErrAccessRuleNotFound is returned for an unknown access rule.
var ErrAccessRuleNotFound = errors.New("access rule not found")

// ListServerTags returns the console's tags, by name.
func ListServerTags(rx *gorm.DB) ([]types.ServerTag, error) {
	var tags []types.ServerTag

	err := rx.Order("name").Find(&tags).Error
	if err != nil {
		return nil, fmt.Errorf("listing server tags: %w", err)
	}

	return tags, nil
}

// ListServerTagAccess returns every tag access grant.
func ListServerTagAccess(rx *gorm.DB) ([]types.ServerTagAccess, error) {
	var rows []types.ServerTagAccess

	err := rx.Order("tag, principal_kind, principal").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("listing server tag access: %w", err)
	}

	return rows, nil
}

// SaveServerTag creates or updates tag and replaces its access grants.
func SaveServerTag(tx *gorm.DB, tag types.ServerTag, grants []types.ServerTagAccess) error {
	var existing types.ServerTag

	err := tx.Where("name = ?", tag.Name).Take(&existing).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		tag.CreatedAt = time.Now().UTC()

		err = tx.Create(&tag).Error
	case err == nil:
		// Request settings change only through SetServerTagRequests.
		err = tx.Model(&types.ServerTag{}).Where("name = ?", tag.Name).
			Update("description", tag.Description).Error
	}

	if err != nil {
		return fmt.Errorf("saving server tag: %w", err)
	}

	err = tx.Where("tag = ?", tag.Name).Delete(&types.ServerTagAccess{}).Error
	if err != nil {
		return fmt.Errorf("clearing server tag access: %w", err)
	}

	now := time.Now().UTC()
	seen := make(map[[2]string]bool, len(grants))

	for _, g := range grants {
		k := [2]string{g.PrincipalKind, g.Principal}
		if seen[k] {
			continue
		}

		seen[k] = true
		g.Tag, g.CreatedAt = tag.Name, now

		err = tx.Create(&g).Error
		if err != nil {
			return fmt.Errorf("adding server tag access: %w", err)
		}
	}

	return nil
}

// SetServerTagRequests sets whether people may request temporary access to
// a tag, for how long at most (0 turns requests off), and which Linux
// accounts an approved request may SSH in as (a JSON array).
func SetServerTagRequests(tx *gorm.DB, name string, maxMinutes int, sshUsers string) error {
	res := tx.Model(&types.ServerTag{}).Where("name = ?", name).Updates(map[string]any{
		"request_max_minutes": maxMinutes,
		"request_ssh_users":   sshUsers,
	})
	if res.Error != nil {
		return res.Error
	}

	if res.RowsAffected == 0 {
		return ErrServerTagNotFound
	}

	return nil
}

// DeleteServerTag removes a tag and its access grants.
func DeleteServerTag(tx *gorm.DB, name string) error {
	err := tx.Where("tag = ?", name).Delete(&types.ServerTagAccess{}).Error
	if err != nil {
		return err
	}

	res := tx.Where("name = ?", name).Delete(&types.ServerTag{})
	if res.Error != nil {
		return res.Error
	}

	if res.RowsAffected == 0 {
		return ErrServerTagNotFound
	}

	return nil
}

// ListAccessRules returns the access rules in order, with their sources and
// destinations.
func ListAccessRules(rx *gorm.DB) ([]types.AccessRule, error) {
	var rules []types.AccessRule

	err := rx.Order("position, id").Find(&rules).Error
	if err != nil {
		return nil, fmt.Errorf("listing access rules: %w", err)
	}

	var ps []types.RulePrincipal

	err = rx.Order("rule_id, side, principal_kind, principal").Find(&ps).Error
	if err != nil {
		return nil, fmt.Errorf("listing access rule principals: %w", err)
	}

	byRule := make(map[uint64]int, len(rules))
	for i := range rules {
		byRule[rules[i].ID] = i
	}

	for _, p := range ps {
		i, ok := byRule[p.RuleID]
		if !ok {
			continue
		}

		if p.Side == types.RuleSideSource {
			rules[i].Sources = append(rules[i].Sources, p)
		} else {
			rules[i].Destinations = append(rules[i].Destinations, p)
		}
	}

	return rules, nil
}

// SaveAccessRule creates rule (ID 0, appended last) or updates it, and
// replaces its sources and destinations. It returns the rule's ID.
func SaveAccessRule(tx *gorm.DB, rule types.AccessRule) (uint64, error) {
	now := time.Now().UTC()
	rule.UpdatedAt = now

	if rule.ID == 0 {
		var last struct{ Max *int }

		err := tx.Model(&types.AccessRule{}).Select("MAX(position) AS max").Scan(&last).Error
		if err != nil {
			return 0, fmt.Errorf("ordering access rule: %w", err)
		}

		if last.Max != nil {
			rule.Position = *last.Max + 1
		}

		rule.CreatedAt = now

		err = tx.Create(&rule).Error
		if err != nil {
			return 0, fmt.Errorf("creating access rule: %w", err)
		}
	} else {
		res := tx.Model(&types.AccessRule{}).Where("id = ?", rule.ID).Updates(map[string]any{
			"name": rule.Name, "protocol": rule.Protocol, "ports": rule.Ports,
			"enabled": rule.Enabled, "updated_at": now, "updated_by": rule.UpdatedBy,
			"ssh_users": rule.SSHUsers, "ssh_check_period": rule.SSHCheckPeriod,
		})
		if res.Error != nil {
			return 0, fmt.Errorf("updating access rule: %w", res.Error)
		}

		if res.RowsAffected == 0 {
			return 0, ErrAccessRuleNotFound
		}

		err := tx.Where("rule_id = ?", rule.ID).Delete(&types.RulePrincipal{}).Error
		if err != nil {
			return 0, fmt.Errorf("clearing access rule principals: %w", err)
		}
	}

	seen := make(map[types.RulePrincipal]bool)

	add := func(side string, ps []types.RulePrincipal) error {
		for _, p := range ps {
			p.RuleID, p.Side = rule.ID, side
			if seen[p] {
				continue
			}

			seen[p] = true

			err := tx.Create(&p).Error
			if err != nil {
				return fmt.Errorf("adding access rule principal: %w", err)
			}
		}

		return nil
	}

	err := add(types.RuleSideSource, rule.Sources)
	if err != nil {
		return 0, err
	}

	err = add(types.RuleSideDestination, rule.Destinations)
	if err != nil {
		return 0, err
	}

	return rule.ID, nil
}

// DeleteAccessRule removes a rule.
func DeleteAccessRule(tx *gorm.DB, id uint64) error {
	err := tx.Where("rule_id = ?", id).Delete(&types.RulePrincipal{}).Error
	if err != nil {
		return err
	}

	res := tx.Where("id = ?", id).Delete(&types.AccessRule{})
	if res.Error != nil {
		return res.Error
	}

	if res.RowsAffected == 0 {
		return ErrAccessRuleNotFound
	}

	return nil
}

// ReorderAccessRules sets the rules' positions to the order of ids.
func ReorderAccessRules(tx *gorm.DB, ids []uint64) error {
	for i, id := range ids {
		err := tx.Model(&types.AccessRule{}).Where("id = ?", id).Update("position", i).Error
		if err != nil {
			return fmt.Errorf("reordering access rules: %w", err)
		}
	}

	return nil
}

// deletePrincipalEverywhere removes a principal from tag grants and access
// rules. A rule left without sources or destinations stays, and grants
// nothing until it is edited.
func deletePrincipalEverywhere(tx *gorm.DB, kind, principal string) error {
	err := tx.Where("principal_kind = ? AND principal = ?", kind, principal).
		Delete(&types.ServerTagAccess{}).Error
	if err != nil {
		return err
	}

	return tx.Where("principal_kind = ? AND principal = ?", kind, principal).
		Delete(&types.RulePrincipal{}).Error
}
