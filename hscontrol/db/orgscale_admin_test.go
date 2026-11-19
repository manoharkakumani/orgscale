package db

import (
	"testing"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDirectoryGroupName(t *testing.T) {
	assert.Equal(t, "engineering-team", DirectoryGroupName("Engineering Team"))
	assert.Equal(t, "r-d", DirectoryGroupName("  R & D  "))
	assert.Equal(t, "ops.oncall_2", DirectoryGroupName("Ops.Oncall_2"))
	assert.Empty(t, DirectoryGroupName("!!!"))
}

func TestDirectoryGroupNamesStayUnique(t *testing.T) {
	db := dbForTest(t)

	var a, b, c, again *types.DirectoryGroup

	err := db.Write(func(tx *gorm.DB) error {
		var err error

		a, err = UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, "1", "Eng Team")
		if err != nil {
			return err
		}

		b, err = UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, "2", "eng-team")
		if err != nil {
			return err
		}

		c, err = UpsertDirectoryGroup(tx, types.DirectorySourceOIDC, "eng team", "eng team")
		if err != nil {
			return err
		}

		again, err = UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, "1", "Eng Team")

		return err
	})
	require.NoError(t, err)

	assert.Equal(t, "eng-team", a.Name)
	assert.Equal(t, "eng-team-scim", b.Name)
	assert.Equal(t, "eng-team-oidc", c.Name)
	assert.Equal(t, a.ID, again.ID)
	assert.Equal(t, "eng-team", again.Name, "re-syncing a group keeps its name")
}

func TestSettingsRoundTrip(t *testing.T) {
	db := dbForTest(t)

	_, err := db.GetSetting("x")
	require.ErrorIs(t, err, ErrSettingNotFound)

	require.NoError(t, db.SetSetting("x", `["a"]`, "alice"))
	require.NoError(t, db.SetSetting("x", `["b"]`, "bob"))

	v, err := db.GetSetting("x")
	require.NoError(t, err)
	assert.Equal(t, `["b"]`, v)

	require.NoError(t, db.DeleteSetting("x"))

	_, err = db.GetSetting("x")
	require.ErrorIs(t, err, ErrSettingNotFound)
}

func TestUserAccessAndOIDCGroups(t *testing.T) {
	db := dbForTest(t)

	u, err := db.CreateUser(types.User{Name: "alice"})
	require.NoError(t, err)

	uid := types.UserID(u.ID)

	require.NoError(t, db.Write(func(tx *gorm.DB) error {
		return SetUserOIDCGroups(tx, uid, []string{"Admins", "Dev"})
	}))

	groups, err := db.ListDirectoryGroups()
	require.NoError(t, err)
	require.Len(t, groups, 2)

	for _, g := range groups {
		assert.Equal(t, []types.UserID{uid}, g.MemberIDs)
	}

	// The next login drops Dev.
	require.NoError(t, db.Write(func(tx *gorm.DB) error {
		return SetUserOIDCGroups(tx, uid, []string{"Admins"})
	}))

	groups, err = db.ListDirectoryGroups()
	require.NoError(t, err)

	for _, g := range groups {
		if g.Name == "dev" {
			assert.Empty(t, g.MemberIDs)
		} else {
			assert.Equal(t, []types.UserID{uid}, g.MemberIDs)
		}
	}

	disabled, err := db.IsUserDisabled(uid)
	require.NoError(t, err)
	assert.False(t, disabled)

	require.NoError(t, db.Write(func(tx *gorm.DB) error { return SetUserDisabled(tx, uid, true, "admin") }))

	disabled, err = db.IsUserDisabled(uid)
	require.NoError(t, err)
	assert.True(t, disabled)

	// Deleting the user cascades its access row and memberships.
	require.NoError(t, db.DestroyUser(uid))

	all, err := db.ListDisabledUsers()
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestAuditEvents(t *testing.T) {
	db := dbForTest(t)

	for _, a := range []string{"one", "two", "three"} {
		require.NoError(t, db.AppendAuditEvent(types.AuditEvent{Actor: "x", ActorKind: types.AuditActorAdmin, Action: a}))
	}

	evs, err := db.ListAuditEvents(2, 0)
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.Equal(t, "three", evs[0].Action)

	older, err := db.ListAuditEvents(10, evs[1].ID)
	require.NoError(t, err)
	require.Len(t, older, 1)
	assert.Equal(t, "one", older[0].Action)
}
