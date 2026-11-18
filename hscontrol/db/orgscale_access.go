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
  created_by text
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
  created_by text
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
