package managed

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()

	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	return m
}

func TestMergeNothingToAddKeepsRaw(t *testing.T) {
	raw := []byte(`// comment
{"acls": [{"action": "accept", "src": ["*"], "dst": ["*:*"]}]}`)

	got, err := Merge(raw, Policy{})
	require.NoError(t, err)
	assert.Equal(t, raw, got)
}

func TestMergeEmptyPolicyStaysAllowAllWithoutRestriction(t *testing.T) {
	got, err := Merge(nil, Policy{Groups: map[string][]string{"eng": {"a@x.com"}}})
	require.NoError(t, err)
	assert.Nil(t, got, "groups alone must not turn allow-all into deny-all")
}

func TestMergeAddsGroupsWithoutOverridingPolicyGroups(t *testing.T) {
	raw := []byte(`{
  // hand written
  "groups": {"group:eng": ["owner@x.com"]},
  "acls": [{"action": "accept", "src": ["group:eng"], "dst": ["*:*"]}],
}`)

	got, err := Merge(raw, Policy{Groups: map[string][]string{
		"eng":   {"idp@x.com"},
		"sales": {"s@x.com"},
		"empty": nil,
	}})
	require.NoError(t, err)

	groups := field[map[string]any](t, got, "groups")
	assert.Equal(t, []any{"owner@x.com"}, groups["group:eng"], "policy definition wins")
	assert.Equal(t, []any{"s@x.com"}, groups["group:sales"])
	assert.Equal(t, []any{}, groups["group:empty"], "empty group still defined so references validate")
}

func TestMergeRestrictDevicesOnEmptyPolicy(t *testing.T) {
	got, err := Merge(nil, Policy{
		RestrictDevices: true,
		Groups:          map[string][]string{"eng": {"a@x.com"}},
		Rules: []Rule{
			{Comment: "db", Src: []string{"group:eng", "b@x.com"}, Dst: []string{"100.64.0.5", "fd7a:115c:a1e0::5"}},
			{Comment: "nobody", Src: nil, Dst: []string{"100.64.0.6"}},
		},
	})
	require.NoError(t, err)

	acls := field[[]any](t, got, "acls")
	require.Len(t, acls, 2, "baseline + one device rule; a rule without grantees is dropped")

	assert.Equal(t, map[string]any{
		"action": "accept",
		"src":    []any{"autogroup:member"},
		"dst":    []any{"autogroup:self:*"},
	}, acls[0])
	assert.Equal(t, map[string]any{
		"action": "accept",
		"src":    []any{"group:eng", "b@x.com"},
		"dst":    []any{"100.64.0.5:*", "fd7a:115c:a1e0::5:*"},
	}, acls[1])
}

func TestMergeRestrictDevicesAppendsToExistingACLs(t *testing.T) {
	raw := []byte(`{"acls": [{"action": "accept", "src": ["group:admins"], "dst": ["*:22"]}], "groups": {"group:admins": ["a@x.com"]}}`)

	got, err := Merge(raw, Policy{RestrictDevices: true})
	require.NoError(t, err)

	acls := field[[]any](t, got, "acls")
	require.Len(t, acls, 2)
	first, ok := acls[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"group:admins"}, first["src"])
}

func TestMergeRejectsBrokenPolicy(t *testing.T) {
	_, err := Merge([]byte(`{"acls": [`), Policy{RestrictDevices: true})
	require.Error(t, err)
}

func TestAllowsEverything(t *testing.T) {
	assert.False(t, AllowsEverything(nil))
	assert.True(t, AllowsEverything([]byte(`{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`)))
	assert.False(t, AllowsEverything([]byte(`{"acls":[{"action":"accept","src":["group:x"],"dst":["*:*"]}]}`)))
	assert.True(t, AllowsEverything([]byte(`{"acls":[{"action":"accept","src":["autogroup:member"],"dst":["*:*"]}]}`)))
	assert.True(t, AllowsEverything([]byte(`{"grants":[{"src":["*"],"dst":["*"],"ip":["*"]}]}`)))
}

func TestMergeDropsUndefinedGroupReferences(t *testing.T) {
	got, err := Merge([]byte(`{"groups": {"group:ops": ["o@x.com"]}}`), Policy{
		RestrictDevices: true,
		Rules: []Rule{
			{Src: []string{"group:gone", "group:ops"}, Dst: []string{"100.64.0.5"}},
			{Src: []string{"group:gone"}, Dst: []string{"100.64.0.6"}},
		},
	})
	require.NoError(t, err)

	acls := field[[]any](t, got, "acls")
	require.Len(t, acls, 2, "baseline + the rule that still has a defined source")
	second, ok := acls[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"group:ops"}, second["src"])
}

func TestMergeTagsAndPortRules(t *testing.T) {
	out, err := Merge(nil, Policy{Tags: []string{"tag:db"}})
	require.NoError(t, err)

	doc := decode(t, out)
	assert.Equal(t, map[string]any{"tag:db": []any{}}, doc["tagOwners"])
	assert.NotContains(t, doc, "acls", "defining tags alone keeps an open network open")

	out, err = Merge([]byte(`{"tagOwners":{"tag:db":["alice@"]}}`), Policy{
		RestrictDevices: true,
		Tags:            []string{"tag:db", "tag:web"},
		Rules: []Rule{
			{Src: []string{"group:ops"}, Dst: []string{"tag:db"}, Ports: "5432, 6432", Proto: "tcp"},
			{Src: []string{"alice@"}, Dst: []string{"tag:gone"}},
		},
	})
	require.NoError(t, err)

	doc = decode(t, out)
	assert.Equal(t, map[string]any{"tag:db": []any{"alice@"}, "tag:web": []any{}}, doc["tagOwners"], "the policy's own owners win")

	acls, ok := doc["acls"].([]any)
	require.True(t, ok)
	require.Len(t, acls, 1, "baseline only: group:ops is undefined and tag:gone too")

	out, err = Merge([]byte(`{"groups":{"group:ops":["bob@"]}}`), Policy{
		RestrictDevices: true,
		Tags:            []string{"tag:db"},
		Rules:           []Rule{{Src: []string{"group:ops"}, Dst: []string{"tag:db"}, Ports: "5432, 6432", Proto: "tcp"}},
	})
	require.NoError(t, err)

	acls, ok = decode(t, out)["acls"].([]any)
	require.True(t, ok)
	require.Len(t, acls, 2)
	assert.Equal(t, map[string]any{
		"action": "accept", "proto": "tcp", "src": []any{"group:ops"}, "dst": []any{"tag:db:5432,6432"},
	}, acls[1])
}

// field returns key of the decoded policy as T, failing the test if it is
// something else.
func field[T any](t *testing.T, got []byte, key string) T {
	t.Helper()

	v, ok := decode(t, got)[key].(T)
	require.True(t, ok, "policy key %q", key)

	return v
}
