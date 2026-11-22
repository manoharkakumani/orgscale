package state

import (
	"maps"
	"net/netip"
	"slices"
	"sync/atomic"

	"github.com/manoharkakumani/orgscale/hscontrol/policy"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/manoharkakumani/orgscale/hscontrol/types/change"
	"tailscale.com/tailcfg"
	"tailscale.com/types/views"
)

// orgscaleGate wraps the policy manager with what orgscale adds after the
// policy compiles:
//
//   - device posture: while it is enforced, a person-owned device that has
//     not passed its latest check is left out of the nodes the policy
//     resolves against and out of every peer list, so no rule names its
//     addresses. It reaches nothing and nothing reaches it, while it stays
//     registered and signed in. A device is blocked until it passes, so a
//     new one gets nothing before its first check.
//   - SSH session recording: accepted SSH sessions are sent to the
//     recorders.
//
// Posture works only while devices are restricted: an allow-all policy
// names every address with "*".
type orgscaleGate struct {
	policy.PolicyManager

	posture   atomic.Pointer[postureState]
	recording atomic.Pointer[types.RecordingSettings]
	// recorders lists where sessions go; it reads the NodeStore.
	recorders func() []netip.AddrPort
}

// postureState is what device posture decided last.
type postureState struct {
	enforce bool
	// passed are the person-owned devices that passed their latest check.
	passed map[types.NodeID]struct{}
}

func newOrgscaleGate(pm policy.PolicyManager) *orgscaleGate {
	return &orgscaleGate{PolicyManager: pm, recorders: func() []netip.AddrPort { return nil }}
}

// blocked reports whether posture cuts n off.
func (g *orgscaleGate) blocked(n types.NodeView) bool {
	st := g.posture.Load()
	if st == nil || !st.enforce || n.IsTagged() {
		return false
	}

	_, ok := st.passed[n.ID()]

	return !ok
}

// blockedIn returns the blocked devices among nodes, nil for none.
func (g *orgscaleGate) blockedIn(nodes views.Slice[types.NodeView]) map[types.NodeID]struct{} {
	st := g.posture.Load()
	if st == nil || !st.enforce {
		return nil
	}

	var out map[types.NodeID]struct{}

	for _, n := range nodes.All() {
		if g.blocked(n) {
			if out == nil {
				out = map[types.NodeID]struct{}{}
			}

			out[n.ID()] = struct{}{}
		}
	}

	return out
}

// SetNodes hands the policy manager every node but the blocked ones.
func (g *orgscaleGate) SetNodes(nodes views.Slice[types.NodeView]) (bool, error) {
	b := g.blockedIn(nodes)
	if len(b) == 0 {
		return g.PolicyManager.SetNodes(nodes)
	}

	kept := make([]types.NodeView, 0, nodes.Len())

	for _, n := range nodes.All() {
		if _, ok := b[n.ID()]; !ok {
			kept = append(kept, n)
		}
	}

	return g.PolicyManager.SetNodes(views.SliceOf(kept))
}

// BuildPeerMap leaves blocked devices out of everyone's peers, and gives
// them none: the filter already drops their traffic both ways.
func (g *orgscaleGate) BuildPeerMap(nodes views.Slice[types.NodeView]) map[types.NodeID][]types.NodeID {
	m := g.PolicyManager.BuildPeerMap(nodes)

	b := g.blockedIn(nodes)
	if len(b) == 0 {
		return m
	}

	for id, peers := range m {
		if _, ok := b[id]; ok {
			m[id] = nil

			continue
		}

		m[id] = slices.DeleteFunc(peers, func(p types.NodeID) bool {
			_, ok := b[p]

			return ok
		})
	}

	return m
}

// SSHPolicy adds the recorders to every accepting rule, and gives a blocked
// device no rules.
func (g *orgscaleGate) SSHPolicy(baseURL string, node types.NodeView) (*tailcfg.SSHPolicy, error) {
	if g.blocked(node) {
		return &tailcfg.SSHPolicy{}, nil
	}

	pol, err := g.PolicyManager.SSHPolicy(baseURL, node)
	if err != nil || pol == nil {
		return pol, err
	}

	rec := g.recording.Load()
	if rec == nil || !rec.Enabled || len(pol.Rules) == 0 {
		return pol, nil
	}

	addrs := g.recorders()

	// The policy manager caches its result; change a copy.
	out := &tailcfg.SSHPolicy{Rules: make([]*tailcfg.SSHRule, 0, len(pol.Rules))}

	for _, r := range pol.Rules {
		if r == nil || r.Action == nil || !r.Action.Accept {
			out.Rules = append(out.Rules, r)

			continue
		}

		rule := *r
		action := withRecording(*rec, addrs, *r.Action)
		rule.Action = &action
		out.Rules = append(out.Rules, &rule)
	}

	return out, nil
}

// withRecording sends the sessions action accepts to addrs, refusing them
// when recording is enforced and there is nowhere to send them.
func withRecording(rec types.RecordingSettings, addrs []netip.AddrPort, action tailcfg.SSHAction) tailcfg.SSHAction {
	if !rec.Enabled || !action.Accept {
		return action
	}

	action.Recorders = slices.Clone(addrs)

	if rec.Enforce {
		action.OnRecordingFailure = &tailcfg.SSHRecorderFailureAction{
			RejectSessionWithMessage:    "# orgscale: SSH sessions to this server must be recorded, and no session recorder is reachable.\n",
			TerminateSessionWithMessage: "# orgscale: the session recorder stopped answering; the session ends.\n",
		}

		// With no recorder at all there is nothing to try.
		if len(addrs) == 0 {
			return tailcfg.SSHAction{Reject: true, Message: action.OnRecordingFailure.RejectSessionWithMessage}
		}
	}

	return action
}

// RecordSSHAction adds session recording to an SSH action the server
// decided, e.g. after a check-mode sign-in.
func (s *State) RecordSSHAction(action *tailcfg.SSHAction) *tailcfg.SSHAction {
	if action == nil {
		return nil
	}

	rec := s.Recording()
	if !rec.Enabled {
		return action
	}

	out := withRecording(rec, s.gate.recorders(), *action)

	return &out
}

// recorderAddrs lists the session recorders: the tailnet IPv4 address of
// every device tagged [types.RecorderTag], online ones first.
func recorderAddrs(nodes views.Slice[types.NodeView]) []netip.AddrPort {
	var online, offline []netip.AddrPort

	for _, n := range nodes.All() {
		if !n.IsTagged() || !slices.Contains(n.Tags().AsSlice(), types.RecorderTag) {
			continue
		}

		for _, ip := range n.IPs() {
			if !ip.Is4() {
				continue
			}

			ap := netip.AddrPortFrom(ip, 80)
			if n.IsOnline().Valid() && n.IsOnline().Get() {
				online = append(online, ap)
			} else {
				offline = append(offline, ap)
			}
		}
	}

	return append(online, offline...)
}

// PostureBlocked reports whether device posture cuts id off.
func (s *State) PostureBlocked(id types.NodeID) bool {
	n, ok := s.GetNodeByID(id)

	return ok && s.gate.blocked(n)
}

// SetPosture applies a posture check: with enforce, every person-owned
// device not in passed is cut off; without, none is. The policy is
// recompiled when that changes who is in it.
func (s *State) SetPosture(enforce bool, passed map[types.NodeID]struct{}) change.Change {
	next := &postureState{enforce: enforce, passed: passed}
	if !enforce {
		next.passed = nil
	}

	before := s.gate.blockedIn(s.ListNodes())

	s.gate.posture.Store(next)

	after := s.gate.blockedIn(s.ListNodes())
	if maps.Equal(before, after) {
		return change.Change{}
	}

	changed, err := s.polMan.SetNodes(s.ListNodes())
	if err != nil {
		return change.PolicyChange()
	}

	if changed {
		s.nodeStore.RebuildPeerMaps()
	}

	// SSH policies are not part of the filter hash; send everything.
	return change.PolicyChange()
}

// SetRecording applies the SSH session recording settings.
func (s *State) SetRecording(r types.RecordingSettings) {
	s.gate.recording.Store(&r)
}

// Recording returns the SSH session recording settings in effect.
func (s *State) Recording() types.RecordingSettings {
	if r := s.gate.recording.Load(); r != nil {
		return *r
	}

	return types.RecordingSettings{}
}

// RecorderAddrs lists where SSH sessions are recorded.
func (s *State) RecorderAddrs() []netip.AddrPort {
	return s.gate.recorders()
}
