package types

import (
	"fmt"
	"strconv"
	"strings"
)

// SettingPosture holds the device requirements set in the admin console, as
// [PostureRules].
const SettingPosture = "posture.rules"

// SettingRecording holds the SSH session recording settings, as
// [RecordingSettings].
const SettingRecording = "ssh.recording"

// SettingTailnets holds links to the consoles of other orgscale tailnets, as
// []TailnetLink.
const SettingTailnets = "tailnets.links"

// PostureMode says what happens to a device that fails the requirements.
type PostureMode string

const (
	// PostureOff checks nothing.
	PostureOff PostureMode = "off"
	// PostureMonitor reports failing devices but leaves their access alone.
	PostureMonitor PostureMode = "monitor"
	// PostureEnforce cuts failing devices off: they stay signed in but reach
	// nothing and nothing reaches them.
	PostureEnforce PostureMode = "enforce"
)

// PostureOSes are the operating systems requirements can name, as
// Tailscale clients report them in Hostinfo.OS.
var PostureOSes = []string{"macOS", "windows", "linux", "iOS", "android"}

// OSRequirement is what one operating system must meet.
type OSRequirement struct {
	// Blocked refuses every device running the system.
	Blocked bool `json:"blocked"`
	// MinVersion is the oldest version allowed, e.g. "14" for macOS 14 or
	// "10.0.22000" for Windows 11. Empty allows any. Linux reports no
	// comparable version, so it is ignored there.
	MinVersion string `json:"minVersion"`
}

// PostureRules are the requirements person-owned devices must meet. Tagged
// devices (servers) are exempt: they are not anyone's laptop or phone.
type PostureRules struct {
	Mode PostureMode `json:"mode"`
	// MinTailscaleVersion is the oldest Tailscale client allowed, e.g. "1.70".
	MinTailscaleVersion string                   `json:"minTailscaleVersion"`
	OS                  map[string]OSRequirement `json:"os"`
	// RequireIntune lets only devices Microsoft Intune reports compliant in.
	// It needs Entra directory sync with the Graph application permission
	// DeviceManagementManagedDevices.Read.All.
	RequireIntune bool `json:"requireIntune"`
}

// Active reports whether the rules check anything.
func (r PostureRules) Active() bool {
	return r.Mode == PostureMonitor || r.Mode == PostureEnforce
}

// Intune compliance as the posture check sees it.
const (
	IntuneCompliant    = "compliant"
	IntuneNoncompliant = "noncompliant"
	// IntuneNotManaged is a device Intune does not know.
	IntuneNotManaged = "notManaged"
	// IntuneUnavailable is Intune not answering, e.g. a missing permission.
	IntuneUnavailable = "unavailable"
)

// PostureFacts is what a device reports, and what Intune says about it.
type PostureFacts struct {
	OS               string
	OSVersion        string
	TailscaleVersion string
	// Intune is one of the Intune* constants, or empty when not checked.
	Intune string
}

// Check returns why a device with facts f fails r; none means it passes.
func (r PostureRules) Check(f PostureFacts) []string {
	var reasons []string

	if r.MinTailscaleVersion != "" && !versionAtLeast(f.TailscaleVersion, r.MinTailscaleVersion) {
		reasons = append(reasons, fmt.Sprintf("Tailscale %s is older than %s", cmpOrUnknown(shortVersion(f.TailscaleVersion)), r.MinTailscaleVersion))
	}

	if req, ok := r.OS[f.OS]; ok {
		switch {
		case req.Blocked:
			reasons = append(reasons, PostureOSLabel(f.OS)+" devices are not allowed")
		case req.MinVersion != "" && f.OS != "linux" && !versionAtLeast(f.OSVersion, req.MinVersion):
			reasons = append(reasons, fmt.Sprintf("%s %s is older than %s", PostureOSLabel(f.OS), cmpOrUnknown(f.OSVersion), req.MinVersion))
		}
	}

	if r.RequireIntune {
		switch f.Intune {
		case IntuneCompliant:
		case IntuneNoncompliant:
			reasons = append(reasons, "Intune reports the device non-compliant")
		case IntuneNotManaged:
			reasons = append(reasons, "the device is not enrolled in Intune")
		default:
			reasons = append(reasons, "Intune compliance is unknown")
		}
	}

	return reasons
}

// PostureOSLabel is how the console names an operating system.
func PostureOSLabel(os string) string {
	switch os {
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "android":
		return "Android"
	case "":
		return "Unknown OS"
	}

	return os
}

func cmpOrUnknown(s string) string {
	if s == "" {
		return "(unknown version)"
	}

	return s
}

// shortVersion drops the build suffix of a Tailscale version such as
// "1.76.1-t1234abcd-g5678".
func shortVersion(v string) string {
	v, _, _ = strings.Cut(v, "-")

	return v
}

// versionParts reads the leading dotted numbers of v: "10.0.22631.4460
// Build 22631" gives [10 0 22631 4460], "1.76.1-tabc" gives [1 76 1].
func versionParts(v string) []int {
	v = strings.TrimSpace(v)
	end := strings.IndexFunc(v, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })

	if end >= 0 {
		v = v[:end]
	}

	var out []int

	for p := range strings.SplitSeq(strings.Trim(v, "."), ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}

		out = append(out, n)
	}

	return out
}

// ValidVersion reports whether v is a dotted version such as "14" or "1.70.0".
func ValidVersion(v string) bool {
	p := versionParts(v)

	return len(p) > 0 && len(p) == strings.Count(strings.Trim(v, "."), ".")+1
}

// versionAtLeast reports whether have is min or newer; an unreadable have is
// not.
func versionAtLeast(have, minimum string) bool {
	h, m := versionParts(have), versionParts(minimum)
	if len(h) == 0 {
		return false
	}

	for i := range max(len(h), len(m)) {
		var a, b int
		if i < len(h) {
			a = h[i]
		}

		if i < len(m) {
			b = m[i]
		}

		if a != b {
			return a > b
		}
	}

	return true
}

// RecordingSettings configure Tailscale SSH session recording. Recorders are
// the devices tagged [RecorderTag]: each runs tailscale's tsrecorder, which
// stores sessions on its own disk or in S3.
type RecordingSettings struct {
	Enabled bool `json:"enabled"`
	// Enforce refuses SSH sessions when no recorder takes them; otherwise
	// they go ahead unrecorded.
	Enforce bool `json:"enforce"`
}

// RecorderTag marks the devices that record SSH sessions.
const RecorderTag = "tag:recorder"

// TailnetLink names the admin console of another orgscale tailnet.
type TailnetLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
