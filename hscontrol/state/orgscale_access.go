package state

import (
	"errors"
	"time"

	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/manoharkakumani/orgscale/hscontrol/types/change"
	"gorm.io/gorm"
)

// ListServerTags returns the tags the console manages.
func (s *State) ListServerTags() ([]types.ServerTag, error) {
	return hsdb.Read(s.db.DB, hsdb.ListServerTags)
}

// ListServerTagAccess returns every tag access grant.
func (s *State) ListServerTagAccess() ([]types.ServerTagAccess, error) {
	return hsdb.Read(s.db.DB, hsdb.ListServerTagAccess)
}

// ListAccessRules returns the rule editor's rules in order.
func (s *State) ListAccessRules() ([]types.AccessRule, error) {
	return hsdb.Read(s.db.DB, hsdb.ListAccessRules)
}

// changePolicy runs apply, then recompiles the policy. When the result does
// not compile, undo puts the database back and the error is returned, so the
// stored rules and the live policy always agree.
func (s *State) changePolicy(apply, undo func(tx *gorm.DB) error) ([]change.Change, error) {
	err := s.db.Write(apply)
	if err != nil {
		return nil, err
	}

	changes, err := s.ReloadPolicy()
	if err != nil {
		undoErr := s.db.Write(undo)
		if undoErr != nil {
			return nil, errors.Join(err, undoErr)
		}

		_, _ = s.ReloadPolicy()

		return nil, err
	}

	return changes, nil
}

// SaveServerTag creates or updates a tag with its access grants.
func (s *State) SaveServerTag(tag types.ServerTag, grants []types.ServerTagAccess) ([]change.Change, error) {
	var (
		prev       *types.ServerTag
		prevGrants []types.ServerTagAccess
	)

	err := s.ReadDirectory(func(rx *gorm.DB) error {
		var t types.ServerTag

		err := rx.Where("name = ?", tag.Name).Take(&t).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}

		if err != nil {
			return err
		}

		prev = &t

		return rx.Where("tag = ?", tag.Name).Find(&prevGrants).Error
	})
	if err != nil {
		return nil, err
	}

	return s.changePolicy(
		func(tx *gorm.DB) error { return hsdb.SaveServerTag(tx, tag, grants) },
		func(tx *gorm.DB) error {
			if prev == nil {
				return hsdb.DeleteServerTag(tx, tag.Name)
			}

			return hsdb.SaveServerTag(tx, *prev, prevGrants)
		},
	)
}

// DeleteServerTag removes a tag the console manages.
func (s *State) DeleteServerTag(name string) ([]change.Change, error) {
	err := s.db.Write(func(tx *gorm.DB) error { return hsdb.DeleteServerTag(tx, name) })
	if err != nil {
		return nil, err
	}

	return s.ReloadPolicy()
}

// SaveAccessRule creates (ID 0) or updates an access rule and returns its ID.
func (s *State) SaveAccessRule(rule types.AccessRule) (uint64, []change.Change, error) {
	var prev *types.AccessRule

	if rule.ID != 0 {
		rules, err := s.ListAccessRules()
		if err != nil {
			return 0, nil, err
		}

		for i := range rules {
			if rules[i].ID == rule.ID {
				prev = &rules[i]
			}
		}

		if prev == nil {
			return 0, nil, hsdb.ErrAccessRuleNotFound
		}
	}

	var id uint64

	changes, err := s.changePolicy(
		func(tx *gorm.DB) error {
			var err error

			id, err = hsdb.SaveAccessRule(tx, rule)

			return err
		},
		func(tx *gorm.DB) error {
			if prev == nil {
				return hsdb.DeleteAccessRule(tx, id)
			}

			_, err := hsdb.SaveAccessRule(tx, *prev)

			return err
		},
	)

	return id, changes, err
}

// DeleteAccessRule removes an access rule.
func (s *State) DeleteAccessRule(id uint64) ([]change.Change, error) {
	err := s.db.Write(func(tx *gorm.DB) error { return hsdb.DeleteAccessRule(tx, id) })
	if err != nil {
		return nil, err
	}

	return s.ReloadPolicy()
}

// ReorderAccessRules sets the order of the access rules.
func (s *State) ReorderAccessRules(ids []uint64) ([]change.Change, error) {
	err := s.db.Write(func(tx *gorm.DB) error { return hsdb.ReorderAccessRules(tx, ids) })
	if err != nil {
		return nil, err
	}

	return s.ReloadPolicy()
}

// ListAdminSessions returns the console sessions that have not expired.
func (s *State) ListAdminSessions() ([]types.AdminSessionRecord, error) {
	return hsdb.Read(s.db.DB, hsdb.ListAdminSessions)
}

// SaveAdminSession stores a console session.
func (s *State) SaveAdminSession(rec types.AdminSessionRecord) error {
	return s.db.Write(func(tx *gorm.DB) error { return hsdb.SaveAdminSession(tx, rec) })
}

// TouchAdminSession records that a console session was used.
func (s *State) TouchAdminSession(id string, at time.Time) error {
	return s.db.Write(func(tx *gorm.DB) error { return hsdb.TouchAdminSession(tx, id, at) })
}

// DeleteAdminSessions ends console sessions, and removes expired ones.
func (s *State) DeleteAdminSessions(ids ...string) error {
	return s.db.Write(func(tx *gorm.DB) error { return hsdb.DeleteAdminSessions(tx, ids...) })
}

// SetServerTagRequests changes a tag's access request settings. The SSH
// accounts apply to requests already approved, so the policy is rebuilt.
func (s *State) SetServerTagRequests(name string, maxMinutes int, sshUsers string) ([]change.Change, error) {
	var prev types.ServerTag

	err := s.ReadDirectory(func(rx *gorm.DB) error { return rx.Where("name = ?", name).Take(&prev).Error })
	if err != nil {
		return nil, err
	}

	return s.changePolicy(
		func(tx *gorm.DB) error { return hsdb.SetServerTagRequests(tx, name, maxMinutes, sshUsers) },
		func(tx *gorm.DB) error {
			return hsdb.SetServerTagRequests(tx, name, prev.RequestMaxMinutes, prev.RequestSSHUsers)
		},
	)
}

// ListAccessRequests returns access requests, newest first: everyone's when
// userID is zero.
func (s *State) ListAccessRequests(userID uint, limit int) ([]types.AccessRequest, error) {
	return hsdb.Read(s.db.DB, func(rx *gorm.DB) ([]types.AccessRequest, error) {
		return hsdb.ListAccessRequests(rx, userID, limit)
	})
}

// GetAccessRequest returns one access request.
func (s *State) GetAccessRequest(id uint64) (types.AccessRequest, error) {
	return hsdb.Read(s.db.DB, func(rx *gorm.DB) (types.AccessRequest, error) {
		return hsdb.GetAccessRequest(rx, id)
	})
}

// CreateAccessRequest stores a pending request. It grants nothing until
// approved.
func (s *State) CreateAccessRequest(r types.AccessRequest) (uint64, error) {
	var id uint64

	err := s.db.Write(func(tx *gorm.DB) error {
		var err error

		id, err = hsdb.CreateAccessRequest(tx, r)

		return err
	})

	return id, err
}

// DecideAccessRequest moves request r (already set to its new state) from
// one of the states in from. Moving into or out of approved recompiles the
// policy, so access starts or ends at once.
func (s *State) DecideAccessRequest(r types.AccessRequest, from ...string) ([]change.Change, error) {
	prev, err := s.GetAccessRequest(r.ID)
	if err != nil {
		return nil, err
	}

	if r.Status != types.AccessRequestApproved && prev.Status != types.AccessRequestApproved {
		return nil, s.db.Write(func(tx *gorm.DB) error { return hsdb.UpdateAccessRequest(tx, r, from...) })
	}

	return s.changePolicy(
		func(tx *gorm.DB) error { return hsdb.UpdateAccessRequest(tx, r, from...) },
		func(tx *gorm.DB) error { return hsdb.UpdateAccessRequest(tx, prev, r.Status) },
	)
}

// ExpireAccessRequests ends approved requests whose time is up, and pending
// ones nobody decided within maxPending. It recompiles the policy when any
// access lapsed and returns how many did.
func (s *State) ExpireAccessRequests(now time.Time, maxPending time.Duration) (int64, []change.Change, error) {
	var lapsed int64

	err := s.db.Write(func(tx *gorm.DB) error {
		var err error

		lapsed, err = hsdb.ExpireAccessRequests(tx, now, maxPending)

		return err
	})
	if err != nil || lapsed == 0 {
		return lapsed, nil, err
	}

	changes, err := s.ReloadPolicy()

	return lapsed, changes, err
}
