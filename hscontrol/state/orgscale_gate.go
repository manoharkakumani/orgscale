package state

import (
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
//   - device posture: a blocked device is left out of the nodes the policy
//     resolves against, so no rule names its addresses. It reaches nothing
//     and nothing reaches it, while it stays registered and signed in.
//   - SSH session recording: accepted SSH sessions are sent to the
//     recorders.
//
// It works only while devices are restricted: an allow-all policy names
// every address with "*".
type orgscaleGate struct {
	policy.PolicyManager

	blocked   atomic.Pointer[map[types.NodeID]struct{}]
	recording atomic.Pointer[types.RecordingSettings]
	// recorders lists where sessions go; it reads the NodeStore.
	recorders func() []netip.AddrPort
}

func newOrgscaleGate(pm policy.PolicyManager) *orgscaleGate {
	return &orgscaleGate{PolicyManager: pm, recorders: func() []netip.AddrPort { return nil }}
}

func (g *orgscaleGate) isBlocked(id types.NodeID) bool {
	b := g.blocked.Load()
	if b == nil {
		return false
	}

	_, ok := (*b)[id]

	return ok
}

// SetNodes hands the policy manager every node but the blocked ones.
func (g *orgscaleGate) SetNodes(nodes views.Slice[types.NodeView]) (bool, error) {
	b := g.blocked.Load()
	if b == nil || len(*b) == 0 {
		return g.PolicyManager.SetNodes(nodes)
	}

	kept := make([]types.NodeView, 0, nodes.Len())

	for _, n := range nodes.All() {
		if _, ok := (*b)[n.ID()]; !ok {
			kept = append(kept, n)
		}
	}

	return g.PolicyManager.SetNodes(views.SliceOf(kept))
}

// BuildPeerMap leaves blocked devices out of everyone's peers, and gives
// them none: the filter already drops their traffic both ways.
func (g *orgscaleGate) BuildPeerMap(nodes views.Slice[types.NodeView]) map[types.NodeID][]types.NodeID {
	m := g.PolicyManager.BuildPeerMap(nodes)

	b := g.blocked.Load()
	if b == nil || len(*b) == 0 {
		return m
	}

	for id, peers := range m {
		if _, ok := (*b)[id]; ok {
			m[id] = nil

			continue
		}

		m[id] = slices.DeleteFunc(peers, func(p types.NodeID) bool {
			_, ok := (*b)[p]

			return ok
		})
	}

	return m
}

// SSHPolicy adds the recorders to every accepting rule, and gives a blocked
// device no rules.
func (g *orgscaleGate) SSHPolicy(baseURL string, node types.NodeView) (*tailcfg.SSHPolicy, error) {
	if g.isBlocked(node.ID()) {
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

// PostureBlocked reports whether device posture has cut id off.
func (s *State) PostureBlocked(id types.NodeID) bool {
	return s.gate.isBlocked(id)
}

// SetPostureBlocked replaces the devices cut off by posture and recompiles
// the policy when that changes who is in it.
func (s *State) SetPostureBlocked(ids map[types.NodeID]struct{}) change.Change {
	prev := s.gate.blocked.Load()
	if prev == nil && len(ids) == 0 {
		return change.Change{}
	}

	if prev != nil && len(*prev) == len(ids) {
		same := true

		for id := range ids {
			if _, ok := (*prev)[id]; !ok {
				same = false

				break
			}
		}

		if same {
			return change.Change{}
		}
	}

	s.gate.blocked.Store(&ids)

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
