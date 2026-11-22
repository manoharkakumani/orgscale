package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		have, minimum string
		want          bool
	}{
		{"15.1.1", "14", true},
		{"14.0", "14", true},
		{"13.6.9", "14", false},
		{"10.0.22631.4460 Build 22631.4460", "10.0.22000", true},
		{"10.0.19045.5011", "10.0.22000", false},
		{"1.76.1-t1234abcd-g5678", "1.70", true},
		{"1.68.2", "1.70", false},
		{"", "1", false},
		{"Ubuntu 24.04", "20", false},
	} {
		assert.Equal(t, tc.want, versionAtLeast(tc.have, tc.minimum), "%q >= %q", tc.have, tc.minimum)
	}
}

func TestValidVersion(t *testing.T) {
	assert.True(t, ValidVersion("14"))
	assert.True(t, ValidVersion("10.0.22000"))
	assert.False(t, ValidVersion("14.x"))
	assert.False(t, ValidVersion("latest"))
	assert.False(t, ValidVersion(""))
}

func TestPostureCheck(t *testing.T) {
	rules := PostureRules{
		Mode:                PostureEnforce,
		MinTailscaleVersion: "1.70",
		OS: map[string]OSRequirement{
			"macOS":   {MinVersion: "14"},
			"android": {Blocked: true},
			"linux":   {MinVersion: "99"},
		},
	}

	assert.Empty(t, rules.Check(PostureFacts{OS: "macOS", OSVersion: "15.1", TailscaleVersion: "1.76.0"}))
	assert.Equal(t, []string{"macOS 13.6 is older than 14"}, rules.Check(PostureFacts{OS: "macOS", OSVersion: "13.6", TailscaleVersion: "1.76.0"}))
	assert.Equal(t, []string{"Tailscale 1.60.0 is older than 1.70", "Android devices are not allowed"},
		rules.Check(PostureFacts{OS: "android", OSVersion: "14", TailscaleVersion: "1.60.0-t1"}))
	assert.Empty(t, rules.Check(PostureFacts{OS: "linux", OSVersion: "Debian 12", TailscaleVersion: "1.80.0"}), "no version check on Linux")
	assert.Empty(t, rules.Check(PostureFacts{OS: "windows", OSVersion: "10.0.1", TailscaleVersion: "1.80.0"}), "no rule for Windows")

	rules.RequireIntune = true
	ok := PostureFacts{OS: "windows", TailscaleVersion: "1.80.0"}

	for state, want := range map[string]int{IntuneCompliant: 0, IntuneNoncompliant: 1, IntuneNotManaged: 1, IntuneUnavailable: 1, "": 1} {
		ok.Intune = state
		assert.Len(t, rules.Check(ok), want, "intune %q", state)
	}
}
