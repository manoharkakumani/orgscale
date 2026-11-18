// Package managed merges the access rules orgscale manages from the admin
// console into the policy an administrator writes.
//
// The administrator's policy is stored as written. Before it is compiled,
// [Merge] adds:
//
//   - directory groups synced from the identity provider, as `group:<name>`,
//     unless the policy defines a group of that name itself;
//   - when device restriction is on, a rule letting every user reach their own
//     devices, and one rule per assigned device letting its grantees reach it.
//
// Tailscale clients only see the peers the policy lets them talk to, so with
// device restriction on a user sees their own devices and the devices
// assigned to them, directly or through a group, and nothing else.
package managed

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tailscale/hujson"
)

// Rule lets Src reach Dst on Ports over Proto.
type Rule struct {
	// Comment names what the rule is for, e.g. the device.
	Comment string
	// Src are policy aliases: user tokens ("alice@example.com"), groups
	// ("group:engineering"), tags ("tag:db") and "autogroup:member".
	Src []string
	// Dst are policy hosts without ports: device addresses
	// ("100.64.0.5"), user tokens, groups, tags and "autogroup:member".
	Dst []string
	// Ports is "*" (also when empty) or a list like "22,443,8000-8100".
	Ports string
	// Proto is "" (any), "tcp", "udp" or "icmp".
	Proto string
}

// Policy is what orgscale adds to the administrator's policy.
type Policy struct {
	// RestrictDevices turns device restriction on: the baseline own-devices
	// rule and Rules are added.
	RestrictDevices bool
	// Groups maps a group name, without "group:", to its members' user tokens.
	Groups map[string][]string
	// Tags are tags the console defines. Each is added to tagOwners with no
	// owners, unless the policy defines it: only administrators apply them,
	// through auth keys and the console.
	Tags []string
	// Rules are the device, tag and rule-editor access rules.
	Rules []Rule
}

// baselineRule lets every user reach their own devices.
var baselineRule = map[string]any{
	"action": "accept",
	"src":    []string{"autogroup:member"},
	"dst":    []string{"autogroup:self:*"},
}

// Merge returns raw with m added. An empty raw is the allow-all default; it
// stays that way unless device restriction is on, so turning restriction off
// and syncing groups never narrows access.
func Merge(raw []byte, m Policy) ([]byte, error) {
	if !m.RestrictDevices && len(m.Tags) == 0 && (len(m.Groups) == 0 || len(bytes.TrimSpace(raw)) == 0) {
		return raw, nil
	}

	doc := map[string]json.RawMessage{}

	if len(bytes.TrimSpace(raw)) > 0 {
		std, err := hujson.Standardize(bytes.Clone(raw))
		if err != nil {
			return nil, fmt.Errorf("parsing policy: %w", err)
		}

		err = json.Unmarshal(std, &doc)
		if err != nil {
			return nil, fmt.Errorf("parsing policy: %w", err)
		}
	}

	groups := map[string]json.RawMessage{}

	if g, ok := doc["groups"]; ok {
		err := json.Unmarshal(g, &groups)
		if err != nil {
			return nil, fmt.Errorf("parsing policy groups: %w", err)
		}
	}

	if len(m.Groups) > 0 {

		for _, name := range slices.Sorted(maps.Keys(m.Groups)) {
			key := "group:" + name
			if _, defined := groups[key]; defined {
				continue
			}

			members := m.Groups[name]
			if members == nil {
				members = []string{}
			}

			b, err := json.Marshal(members)
			if err != nil {
				return nil, err
			}

			groups[key] = b
		}

		b, err := json.Marshal(groups)
		if err != nil {
			return nil, err
		}

		doc["groups"] = b
	}

	tagOwners := map[string]json.RawMessage{}

	if t, ok := doc["tagOwners"]; ok {
		err := json.Unmarshal(t, &tagOwners)
		if err != nil {
			return nil, fmt.Errorf("parsing policy tagOwners: %w", err)
		}
	}

	if len(m.Tags) > 0 {
		for _, tag := range m.Tags {
			if _, defined := tagOwners[tag]; !defined {
				tagOwners[tag] = json.RawMessage("[]")
			}
		}

		b, err := json.Marshal(tagOwners)
		if err != nil {
			return nil, err
		}

		doc["tagOwners"] = b
	}

	// An alias naming a group or tag the policy does not define would fail
	// the whole compile; a rule naming it grants nothing through it instead.
	undefined := func(alias string) bool {
		switch {
		case strings.HasPrefix(alias, "group:"):
			_, ok := groups[alias]

			return !ok
		case strings.HasPrefix(alias, "tag:"):
			_, ok := tagOwners[alias]

			return !ok
		}

		return false
	}

	if m.RestrictDevices {
		var acls []json.RawMessage

		if a, ok := doc["acls"]; ok {
			err := json.Unmarshal(a, &acls)
			if err != nil {
				return nil, fmt.Errorf("parsing policy acls: %w", err)
			}
		}

		b, err := json.Marshal(baselineRule)
		if err != nil {
			return nil, err
		}

		acls = append(acls, b)

		for _, r := range m.Rules {
			r.Src = slices.DeleteFunc(slices.Clone(r.Src), undefined)
			r.Dst = slices.DeleteFunc(slices.Clone(r.Dst), undefined)

			if len(r.Src) == 0 || len(r.Dst) == 0 {
				continue
			}

			ports := cmp.Or(strings.ReplaceAll(r.Ports, " ", ""), "*")

			dst := make([]string, 0, len(r.Dst))
			for _, d := range r.Dst {
				dst = append(dst, d+":"+ports)
			}

			acl := map[string]any{
				"action": "accept",
				"src":    r.Src,
				"dst":    dst,
			}
			if r.Proto != "" {
				acl["proto"] = r.Proto
			}

			b, err := json.Marshal(acl)
			if err != nil {
				return nil, err
			}

			acls = append(acls, b)
		}

		b, err = json.Marshal(acls)
		if err != nil {
			return nil, err
		}

		doc["acls"] = b
	}

	return json.Marshal(doc)
}

// AllowsEverything reports whether raw lets everyone reach everything: no
// policy at all, or an ACL with source "*" and destination "*:*". Device
// restriction cannot narrow such a policy, and the console warns about it.
func AllowsEverything(raw []byte) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false // Merge replaces the allow-all default when restricting.
	}

	std, err := hujson.Standardize(bytes.Clone(raw))
	if err != nil {
		return false
	}

	var doc struct {
		ACLs []struct {
			Src []string `json:"src"`
			Dst []string `json:"dst"`
		} `json:"acls"`
		Grants []struct {
			Src []string `json:"src"`
			Dst []string `json:"dst"`
		} `json:"grants"`
	}

	err = json.Unmarshal(std, &doc)
	if err != nil {
		return false
	}

	everyone := func(src []string) bool {
		return slices.Contains(src, "*") || slices.Contains(src, "autogroup:member")
	}

	for _, a := range doc.ACLs {
		if everyone(a.Src) && slices.Contains(a.Dst, "*:*") {
			return true
		}
	}

	for _, g := range doc.Grants {
		if everyone(g.Src) && slices.Contains(g.Dst, "*") {
			return true
		}
	}

	return false
}
