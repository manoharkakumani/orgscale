package servertest_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/servertest"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"tailscale.com/types/netmap"
)

func nodeIDOf(t *testing.T, c *servertest.TestClient) types.NodeID {
	t.Helper()

	nm := c.Netmap()
	require.NotNil(t, nm)

	return types.NodeID(nm.SelfNode.ID())
}

// waitPeers waits until c sees exactly the peers named want.
func waitPeers(t *testing.T, c *servertest.TestClient, want ...string) {
	t.Helper()

	slices.Sort(want)

	c.WaitForCondition(t, "peers "+strconv.Quote(c.Name), 15*time.Second, func(nm *netmap.NetworkMap) bool {
		got := make([]string, 0, len(nm.Peers))
		for _, p := range nm.Peers {
			got = append(got, p.Hostinfo().Hostname())
		}

		slices.Sort(got)

		return slices.Equal(got, want)
	})
}

// TestOrgscaleDeviceAccess checks the device access model of the admin
// console: with restriction on, a user sees their own devices and the
// devices assigned to them, directly or through a directory group, and
// removing a user from the organisation removes their devices.
func TestOrgscaleDeviceAccess(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	st := srv.State()

	alice := srv.CreateUser(t, "alice")
	bob := srv.CreateUser(t, "bob")
	ops := srv.CreateUser(t, "ops")

	aliceLaptop := servertest.NewClient(t, srv, "alice-laptop", servertest.WithUser(alice))
	alicePhone := servertest.NewClient(t, srv, "alice-phone", servertest.WithUser(alice))
	bobLaptop := servertest.NewClient(t, srv, "bob-laptop", servertest.WithUser(bob))
	db := servertest.NewClient(t, srv, "db-server", servertest.WithUser(ops))

	// No policy: everyone sees everyone.
	waitPeers(t, bobLaptop, "alice-laptop", "alice-phone", "db-server")

	// Restriction on: only your own devices.
	changes, err := st.SetDeviceRestriction(true, "test")
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, aliceLaptop, "alice-phone")
	waitPeers(t, alicePhone, "alice-laptop")
	waitPeers(t, bobLaptop)
	waitPeers(t, db)

	// Assign the db server to alice directly.
	dbID := nodeIDOf(t, db)
	changes, err = st.SetDeviceAccess(dbID, []types.DeviceAccess{
		{PrincipalKind: types.PrincipalUser, Principal: strconv.FormatUint(uint64(alice.ID), 10)},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, aliceLaptop, "alice-phone", "db-server")
	waitPeers(t, db, "alice-laptop", "alice-phone")
	waitPeers(t, bobLaptop)

	// A directory group from the IdP, with bob in it, gets the db server too.
	var groupID uint64

	changes, err = st.WriteDirectory(func(tx *gorm.DB) error {
		g, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceSCIM, "ext-1", "Engineering Team")
		if err != nil {
			return err
		}

		groupID = g.ID

		return hsdb.SetDirectoryGroupMembers(tx, g.ID, []types.UserID{types.UserID(bob.ID)})
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	changes, err = st.SetDeviceAccess(dbID, []types.DeviceAccess{
		{PrincipalKind: types.PrincipalUser, Principal: strconv.FormatUint(uint64(alice.ID), 10)},
		{PrincipalKind: types.PrincipalDirectoryGroup, Principal: strconv.FormatUint(groupID, 10)},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, bobLaptop, "db-server")
	waitPeers(t, db, "alice-laptop", "alice-phone", "bob-laptop")

	// The group is usable by name in a hand-written policy as well.
	pol, err := st.EffectivePolicy()
	require.NoError(t, err)
	require.Contains(t, string(pol), `"group:engineering-team"`)

	// Leaving the group at the IdP takes the access away.
	changes, err = st.WriteDirectory(func(tx *gorm.DB) error {
		return hsdb.SetDirectoryGroupMembers(tx, groupID, nil)
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, bobLaptop)
	waitPeers(t, db, "alice-laptop", "alice-phone")

	// Removing alice from the organisation deletes her devices and the user.
	changes, err = st.RemoveUser(types.UserID(alice.ID))
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, db)

	_, err = st.GetUserByID(types.UserID(alice.ID))
	require.Error(t, err, "alice is gone")
	require.Zero(t, st.ListNodesByUser(types.UserID(alice.ID)).Len(), "alice's devices are gone")

	grants, err := st.ListDeviceAccess()
	require.NoError(t, err)

	for _, g := range grants {
		if g.PrincipalKind == types.PrincipalUser {
			require.NotEqual(t, strconv.FormatUint(uint64(alice.ID), 10), g.Principal, "grants naming alice are gone")
		}
	}

	// Turning restriction off restores the open default.
	changes, err = st.SetDeviceRestriction(false, "test")
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, bobLaptop, "db-server")
}

// TestOrgscaleDisabledUserLosesDevices checks that disabling a user deletes
// their devices.
func TestOrgscaleDisabledUserLosesDevices(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	st := srv.State()

	alice := srv.CreateUser(t, "alice")
	bob := srv.CreateUser(t, "bob")

	servertest.NewClient(t, srv, "alice-laptop", servertest.WithUser(alice))
	bobLaptop := servertest.NewClient(t, srv, "bob-laptop", servertest.WithUser(bob))

	waitPeers(t, bobLaptop, "alice-laptop")

	changes, err := st.SetUserDisabled(types.UserID(alice.ID), true, "test")
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, bobLaptop)

	disabled, err := st.IsUserDisabled(types.UserID(alice.ID))
	require.NoError(t, err)
	require.True(t, disabled)

	_, err = st.SetUserDisabled(types.UserID(alice.ID), false, "test")
	require.NoError(t, err)

	disabled, err = st.IsUserDisabled(types.UserID(alice.ID))
	require.NoError(t, err)
	require.False(t, disabled)
}
