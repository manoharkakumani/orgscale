package servertest_test

import (
	"context"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"

	hsdb "github.com/manoharkakumani/orgscale/hscontrol/db"
	"github.com/manoharkakumani/orgscale/hscontrol/servertest"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"tailscale.com/tailcfg"
	"tailscale.com/types/netmap"
)

func nodeIDOf(t *testing.T, c *servertest.TestClient) types.NodeID {
	t.Helper()

	nm := c.Netmap()
	require.NotNil(t, nm)

	return types.NodeID(nm.SelfNode.ID()) //nolint:gosec // G115: node IDs are positive
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

// TestOrgscaleServerTagsAndRules checks servers enrolled into a console tag
// and access granted per tag and through the rule editor.
func TestOrgscaleServerTagsAndRules(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	st := srv.State()

	alice := srv.CreateUser(t, "alice")
	bob := srv.CreateUser(t, "bob")

	// A tag made in the console: no policy JSON involved.
	changes, err := st.SaveServerTag(types.ServerTag{Name: "tag:db", Description: "Databases"}, nil)
	require.NoError(t, err)
	srv.App.Change(changes...)

	exp := time.Now().Add(time.Hour)
	key, err := st.CreatePreAuthKey(nil, true, false, &exp, []string{"tag:db"})
	require.NoError(t, err)

	aliceLaptop := servertest.NewClient(t, srv, "alice-laptop", servertest.WithUser(alice))
	bobLaptop := servertest.NewClient(t, srv, "bob-laptop", servertest.WithUser(bob))
	db1 := servertest.NewClient(t, srv, "db-1", servertest.WithAuthKey(key.Key))
	db2 := servertest.NewClient(t, srv, "db-2", servertest.WithAuthKey(key.Key))

	db1Node, ok := st.GetNodeByID(nodeIDOf(t, db1))
	require.True(t, ok)
	require.True(t, db1Node.IsTagged(), "enrolled with a tag key, the server belongs to the tag")

	changes, err = st.SetDeviceRestriction(true, "test")
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, aliceLaptop)
	waitPeers(t, bobLaptop)
	waitPeers(t, db1)

	// Bob gets every server carrying the tag at once.
	changes, err = st.SaveServerTag(types.ServerTag{Name: "tag:db", Description: "Databases"}, []types.ServerTagAccess{
		{PrincipalKind: types.PrincipalUser, Principal: strconv.FormatUint(uint64(bob.ID), 10)},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, bobLaptop, "db-1", "db-2")
	waitPeers(t, aliceLaptop)
	waitPeers(t, db2, "bob-laptop")

	// A rule from the rule editor: alice may reach the databases on 5432.
	id, changes, err := st.SaveAccessRule(types.AccessRule{
		Name: "Alice to Postgres", Protocol: "tcp", Ports: "5432", Enabled: true,
		Sources:      []types.RulePrincipal{{Kind: types.PrincipalUser, Principal: strconv.FormatUint(uint64(alice.ID), 10)}},
		Destinations: []types.RulePrincipal{{Kind: types.PrincipalTag, Principal: "tag:db"}},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, aliceLaptop, "db-1", "db-2")

	pol, err := st.EffectivePolicy()
	require.NoError(t, err)
	require.Contains(t, string(pol), `"tag:db:5432"`)
	require.Contains(t, string(pol), `"proto":"tcp"`)

	// Pausing the rule takes the access away again.
	_, changes, err = st.SaveAccessRule(types.AccessRule{
		ID: id, Name: "Alice to Postgres", Protocol: "tcp", Ports: "5432", Enabled: false,
		Sources:      []types.RulePrincipal{{Kind: types.PrincipalUser, Principal: strconv.FormatUint(uint64(alice.ID), 10)}},
		Destinations: []types.RulePrincipal{{Kind: types.PrincipalTag, Principal: "tag:db"}},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, aliceLaptop)

	// Removing bob drops him from the tag's access list.
	changes, err = st.RemoveUser(types.UserID(bob.ID))
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, db1)

	grants, err := st.ListServerTagAccess()
	require.NoError(t, err)
	require.Empty(t, grants)
}

// TestOrgscaleServersTalkPeopleIsolated checks the setup of an organisation
// where people only see their own devices, never each other's, servers talk to servers
// as their tags allow, and a console-made group reaches some servers.
func TestOrgscaleServersTalkPeopleIsolated(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	st := srv.State()

	alice := srv.CreateUser(t, "alice")
	bob := srv.CreateUser(t, "bob")

	for _, tag := range []string{"tag:web", "tag:db"} {
		changes, err := st.SaveServerTag(types.ServerTag{Name: tag}, nil)
		require.NoError(t, err)
		srv.App.Change(changes...)
	}

	key := func(tag string) string {
		exp := time.Now().Add(time.Hour)
		k, err := st.CreatePreAuthKey(nil, true, false, &exp, []string{tag})
		require.NoError(t, err)

		return k.Key
	}

	webKey, dbKey := key("tag:web"), key("tag:db")

	aliceLaptop := servertest.NewClient(t, srv, "alice-laptop", servertest.WithUser(alice))
	alicePhone := servertest.NewClient(t, srv, "alice-phone", servertest.WithUser(alice))
	bobLaptop := servertest.NewClient(t, srv, "bob-laptop", servertest.WithUser(bob))
	web1 := servertest.NewClient(t, srv, "web-1", servertest.WithAuthKey(webKey))
	db1 := servertest.NewClient(t, srv, "db-1", servertest.WithAuthKey(dbKey))
	db2 := servertest.NewClient(t, srv, "db-2", servertest.WithAuthKey(dbKey))

	changes, err := st.SetDeviceRestriction(true, "test")
	require.NoError(t, err)
	srv.App.Change(changes...)

	// Databases reach each other (the tag grants itself); web reaches the
	// databases through a rule.
	changes, err = st.SaveServerTag(types.ServerTag{Name: "tag:db"}, []types.ServerTagAccess{
		{PrincipalKind: types.PrincipalTag, Principal: "tag:db"},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	_, changes, err = st.SaveAccessRule(types.AccessRule{
		Name: "web to db", Protocol: "tcp", Ports: "5432", Enabled: true,
		Sources:      []types.RulePrincipal{{Kind: types.PrincipalTag, Principal: "tag:web"}},
		Destinations: []types.RulePrincipal{{Kind: types.PrincipalTag, Principal: "tag:db"}},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	// A group made in the console: bob may reach the web servers.
	var groupID uint64

	changes, err = st.WriteDirectory(func(tx *gorm.DB) error {
		g, err := hsdb.UpsertDirectoryGroup(tx, types.DirectorySourceConsole, "console-1", "Web Team")
		if err != nil {
			return err
		}

		groupID = g.ID

		return hsdb.SetDirectoryGroupMembers(tx, g.ID, []types.UserID{types.UserID(bob.ID)})
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	changes, err = st.SaveServerTag(types.ServerTag{Name: "tag:web"}, []types.ServerTagAccess{
		{PrincipalKind: types.PrincipalDirectoryGroup, Principal: strconv.FormatUint(groupID, 10)},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, db1, "db-2", "web-1")
	waitPeers(t, web1, "db-1", "db-2", "bob-laptop")
	waitPeers(t, bobLaptop, "web-1")
	waitPeers(t, aliceLaptop, "alice-phone")
	waitPeers(t, alicePhone, "alice-laptop")
	waitPeers(t, db2, "db-1", "web-1")
}

// TestOrgscaleSSHRules checks that an SSH rule from the console reaches the
// server as Tailscale SSH policy, and opens port 22 under access control.
func TestOrgscaleSSHRules(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	st := srv.State()

	alice := srv.CreateUser(t, "alice")
	bob := srv.CreateUser(t, "bob")

	changes, err := st.SaveServerTag(types.ServerTag{Name: "tag:prod"}, nil)
	require.NoError(t, err)
	srv.App.Change(changes...)

	exp := time.Now().Add(time.Hour)
	key, err := st.CreatePreAuthKey(nil, true, false, &exp, []string{"tag:prod"})
	require.NoError(t, err)

	aliceLaptop := servertest.NewClient(t, srv, "alice-laptop", servertest.WithUser(alice))
	bobLaptop := servertest.NewClient(t, srv, "bob-laptop", servertest.WithUser(bob))
	prod := servertest.NewClient(t, srv, "prod-1", servertest.WithAuthKey(key.Key))

	changes, err = st.SetDeviceRestriction(true, "test")
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, aliceLaptop)

	_, changes, err = st.SaveAccessRule(types.AccessRule{
		Kind: types.RuleKindSSH, Name: "Alice SSH to prod", Enabled: true,
		SSHUsers: `["ubuntu","root"]`, SSHCheckPeriod: "12h",
		Sources:      []types.RulePrincipal{{Kind: types.PrincipalUser, Principal: strconv.FormatUint(uint64(alice.ID), 10)}},
		Destinations: []types.RulePrincipal{{Kind: types.PrincipalTag, Principal: "tag:prod"}},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	// Port 22 is opened for alice only, so only she sees the server.
	waitPeers(t, aliceLaptop, "prod-1")
	waitPeers(t, bobLaptop)

	// The server receives an SSH policy letting alice in as ubuntu or root,
	// after signing in again (check mode).
	prod.WaitForCondition(t, "ssh policy", 15*time.Second, func(nm *netmap.NetworkMap) bool {
		if nm.SSHPolicy == nil {
			return false
		}

		for _, r := range nm.SSHPolicy.Rules {
			if r.SSHUsers["ubuntu"] != "" && r.SSHUsers["root"] != "" && len(r.Principals) > 0 {
				return true
			}
		}

		return false
	})

	pol, err := st.EffectivePolicy()
	require.NoError(t, err)
	require.Contains(t, string(pol), `"action":"check"`)
	require.Contains(t, string(pol), `"checkPeriod":"12h"`)
	require.Contains(t, string(pol), `"tag:prod:22"`)
}

// TestOrgscaleConsoleDNS checks that DNS settings changed in the admin
// console reach connected clients without a restart, and that resetting
// them goes back to the config file's.
func TestOrgscaleConsoleDNS(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t, servertest.WithMagicDNS("tailnet.example"))
	alice := srv.CreateUser(t, "alice")
	c := servertest.NewClient(t, srv, "alice-laptop", servertest.WithUser(alice))
	c.WaitForCondition(t, "netmap", 15*time.Second, func(nm *netmap.NetworkMap) bool { return nm.SelfNode.Valid() })

	srv.ApplyConsoleDNS(&types.ConsoleDNS{
		MagicDNS:         true,
		OverrideLocalDNS: true,
		Nameservers:      []string{"10.0.0.53"},
		SplitDNS:         map[string][]string{"corp.example.com": {"10.1.0.53"}},
		SearchDomains:    []string{"corp.example.com"},
		Records:          []types.ConsoleDNSRecord{{Name: "grafana.tailnet.example", Type: "A", Value: "100.64.0.9"}},
	})

	c.WaitForCondition(t, "console DNS", 15*time.Second, func(nm *netmap.NetworkMap) bool {
		d := nm.DNS

		return len(d.Resolvers) == 1 && d.Resolvers[0].Addr == "10.0.0.53" &&
			len(d.Routes["corp.example.com"]) == 1 &&
			slices.Contains(d.Domains, "corp.example.com") &&
			len(d.ExtraRecords) == 1 && d.Proxied
	})

	srv.ApplyConsoleDNS(nil)

	c.WaitForCondition(t, "file DNS again", 15*time.Second, func(nm *netmap.NetworkMap) bool {
		d := nm.DNS
		return len(d.Resolvers) == 0 && len(d.Routes["corp.example.com"]) == 0 && len(d.ExtraRecords) == 0 && d.Proxied
	})
}

// TestOrgscaleTempAccessOneServer checks temporary access to a single
// server: of two servers sharing a tag, the person reaches only the one
// granted, and loses it when the grant ends.
func TestOrgscaleTempAccessOneServer(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	st := srv.State()

	alice := srv.CreateUser(t, "alice")

	changes, err := st.SaveServerTag(types.ServerTag{Name: "tag:db"}, nil)
	require.NoError(t, err)
	srv.App.Change(changes...)

	exp := time.Now().Add(time.Hour)
	k, err := st.CreatePreAuthKey(nil, true, false, &exp, []string{"tag:db"})
	require.NoError(t, err)

	laptop := servertest.NewClient(t, srv, "alice-laptop", servertest.WithUser(alice))
	db1 := servertest.NewClient(t, srv, "db-1", servertest.WithAuthKey(k.Key))
	servertest.NewClient(t, srv, "db-2", servertest.WithAuthKey(k.Key))

	changes, err = st.SetDeviceRestriction(true, "test")
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, laptop)

	now := time.Now().UTC()
	until := now.Add(time.Hour)

	id, err := st.CreateAccessRequest(types.AccessRequest{
		UserID: alice.ID, Requester: "alice", Tag: types.AccessNodePrefix + strconv.FormatUint(uint64(nodeIDOf(t, db1)), 10),
		Minutes: 60, Status: types.AccessRequestPending, CreatedAt: now,
		GrantKind: types.PrincipalUser, GrantPrincipal: strconv.FormatUint(uint64(alice.ID), 10),
	})
	require.NoError(t, err)

	changes, err = st.DecideAccessRequest(types.AccessRequest{
		ID: id, Minutes: 60, Status: types.AccessRequestApproved, DecidedBy: "test", DecidedAt: &now, ExpiresAt: &until,
	}, types.AccessRequestPending)
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, laptop, "db-1")

	_, changes, err = st.ExpireAccessRequests(until.Add(time.Second), 24*time.Hour)
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, laptop)
}

// TestOrgscalePrivateResource checks a private resource behind a subnet
// router: with access control on, a rule naming the resource lets the
// person reach it through the router (the router's packet filter accepts
// them for that address only), and deleting the resource takes it away.
func TestOrgscalePrivateResource(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	st := srv.State()

	alice := srv.CreateUser(t, "alice")

	changes, err := st.SaveServerTag(types.ServerTag{Name: "tag:router"}, nil)
	require.NoError(t, err)
	srv.App.Change(changes...)

	exp := time.Now().Add(time.Hour)
	k, err := st.CreatePreAuthKey(nil, true, false, &exp, []string{"tag:router"})
	require.NoError(t, err)

	laptop := servertest.NewClient(t, srv, "alice-laptop", servertest.WithUser(alice))
	router := servertest.NewClient(t, srv, "vpc-router", servertest.WithAuthKey(k.Key))

	vpc := netip.MustParsePrefix("10.20.0.0/16")
	router.Direct().SetHostinfo(&tailcfg.Hostinfo{BackendLogID: "servertest-vpc-router", Hostname: "vpc-router", RoutableIPs: []netip.Prefix{vpc}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = router.Direct().SendUpdate(ctx)

	_, routeChange, err := st.SetApprovedRoutes(nodeIDOf(t, router), []netip.Prefix{vpc})
	require.NoError(t, err)
	srv.App.Change(routeChange)

	changes, err = st.SetDeviceRestriction(true, "test")
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, laptop)

	db := "10.20.1.25/32"
	id, changes, err := st.SaveNetworkResource(types.NetworkResource{Name: "orders-db", Address: db})
	require.NoError(t, err)
	srv.App.Change(changes...)

	_, changes, err = st.SaveAccessRule(types.AccessRule{
		Name: "alice to orders-db", Protocol: "tcp", Ports: "5432", Enabled: true,
		Sources:      []types.RulePrincipal{{Kind: types.PrincipalUser, Principal: strconv.FormatUint(uint64(alice.ID), 10)}},
		Destinations: []types.RulePrincipal{{Kind: types.PrincipalResource, Principal: strconv.FormatUint(id, 10)}},
	})
	require.NoError(t, err)
	srv.App.Change(changes...)

	// Alice now has the router as a peer, carrying the VPC route.
	laptop.WaitForCondition(t, "alice reaches the VPC through the router", 15*time.Second, func(nm *netmap.NetworkMap) bool {
		for _, p := range nm.Peers {
			for i := range p.AllowedIPs().Len() {
				if p.AllowedIPs().At(i) == vpc {
					return true
				}
			}
		}

		return false
	})

	// The router lets her in to the database's address only.
	routerAllows := func(nm *netmap.NetworkMap) (bool, bool) {
		var dbAllowed, otherAllowed bool

		for _, m := range nm.PacketFilter {
			for _, d := range m.Dsts {
				if d.Net.String() == db && d.Ports.First <= 5432 && 5432 <= d.Ports.Last {
					dbAllowed = true
				} else if vpc.Overlaps(d.Net) {
					otherAllowed = true
				}
			}
		}

		return dbAllowed, otherAllowed
	}

	router.WaitForCondition(t, "router accepts alice for the database", 15*time.Second, func(nm *netmap.NetworkMap) bool {
		ok, _ := routerAllows(nm)
		return ok
	})

	_, other := routerAllows(router.Netmap())
	require.False(t, other, "only the database's address is open, not the whole VPC")

	changes, err = st.DeleteNetworkResource(id)
	require.NoError(t, err)
	srv.App.Change(changes...)

	waitPeers(t, laptop)
}

// TestPeerCapabilityVersionIsThePeers checks that peers are described with
// their own capability version, not the viewer's. Tailscale 1.105 and later
// start hybrid post-quantum WireGuard handshakes with peers reported at 152
// or above, so describing an older peer with a newer viewer's version left
// the two unable to connect.
func TestPeerCapabilityVersionIsThePeers(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	st := srv.State()

	alice := srv.CreateUser(t, "alice")
	a := servertest.NewClient(t, srv, "a", servertest.WithUser(alice))
	servertest.NewClient(t, srv, "b", servertest.WithUser(alice))

	a.WaitForPeers(t, 1, 15*time.Second)

	peer, ok := st.GetNodeByID(nodeIDOf(t, a))
	require.True(t, ok)

	// A viewer much newer than the peer still sees the peer's own version.
	require.Equal(t, tailcfg.CurrentCapabilityVersion, st.PeerCapabilityVersion(peer, 999))

	// And the map a client receives carries each peer's own version, once
	// the peer has told the server (a newly joined peer is announced first
	// with a cautious version, then updated).
	a.WaitForCondition(t, "peer b at its own capability version", 15*time.Second, func(nm *netmap.NetworkMap) bool {
		return len(nm.Peers) == 1 && nm.Peers[0].Cap() == tailcfg.CurrentCapabilityVersion
	})
}
