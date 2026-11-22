package hscontrol

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/http"
	"strings"
	textTemplate "text/template"
	"uuid"

	"github.com/go-chi/chi/v5"
	"github.com/manoharkakumani/orgscale/hscontrol/templates"
)

// WindowsConfigMessage shows a simple message in the browser for how to configure the Windows Tailscale client.
func (h *Orgscale) WindowsConfigMessage(
	writer http.ResponseWriter,
	req *http.Request,
) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(templates.Windows(h.cfg.ServerURL).Render()))
}

// AppleConfigMessage shows a simple message in the browser to point the user to the iOS/MacOS profile and instructions for how to install it.
func (h *Orgscale) AppleConfigMessage(
	writer http.ResponseWriter,
	req *http.Request,
) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(templates.Apple(h.cfg.ServerURL).Render()))
}

// ConnectPage is the one link to send people: how to connect each kind of
// device to this server.
func (h *Orgscale) ConnectPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(templates.Connect(h.cfg.ServerURL).Render()))
}

// WindowsRegistryFile sets the Tailscale LoginURL policy, so the Windows app
// signs in to this server without a command line.
func (h *Orgscale) WindowsRegistryFile(w http.ResponseWriter, _ *http.Request) {
	url := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(h.cfg.ServerURL)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="orgscale.reg"`)
	_, _ = fmt.Fprintf(w, "Windows Registry Editor Version 5.00\r\n\r\n[HKEY_LOCAL_MACHINE\\SOFTWARE\\Policies\\Tailscale]\r\n\"LoginURL\"=\"%s\"\r\n\"PostureChecking\"=\"always\"\r\n", url)
}

// LinuxConnectScript installs Tailscale when missing and signs in to this
// server.
func (h *Orgscale) LinuxConnectScript(w http.ResponseWriter, _ *http.Request) {
	url := strings.ReplaceAll(h.cfg.ServerURL, "'", "")

	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = fmt.Fprintf(w, `#!/bin/sh
# Connects this machine to %[1]s
set -e
SUDO=""
[ "$(id -u)" -eq 0 ] || SUDO="sudo"
if ! command -v tailscale >/dev/null 2>&1; then
  curl -fsSL https://tailscale.com/install.sh | sh
fi
$SUDO tailscale up --login-server '%[1]s' "$@"
`, url)
}

func (h *Orgscale) ApplePlatformConfig(
	writer http.ResponseWriter,
	req *http.Request,
) {
	platform := chi.URLParam(req, "platform")
	if platform == "" {
		httpError(writer, NewHTTPError(http.StatusBadRequest, "no platform specified", nil))
		return
	}

	id := uuid.NewV4()
	contentID := uuid.NewV4()

	platformConfig := AppleMobilePlatformConfig{
		UUID: contentID,
		URL:  h.cfg.ServerURL,
	}

	// "macos" covers both Mac apps (App Store and standalone) in one profile.
	payloadTypes := []string{applePayloadType[platform]}
	if platform == "macos" {
		payloadTypes = []string{applePayloadType["macos-app-store"], applePayloadType["macos-standalone"]}
	} else if payloadTypes[0] == "" {
		httpError(writer, NewHTTPError(http.StatusBadRequest, "platform must be ios, macos, macos-app-store or macos-standalone", nil))
		return
	}

	var payload bytes.Buffer

	for _, payloadType := range payloadTypes {
		platformConfig.PayloadType = payloadType
		platformConfig.UUID = uuid.NewV4()

		if err := payloadTemplate.Execute(&payload, platformConfig); err != nil { //nolint:noinlineerr
			httpError(writer, err)
			return
		}
	}

	config := AppleMobileConfig{
		UUID:    id,
		URL:     h.cfg.ServerURL,
		Payload: payload.String(),
	}

	var content bytes.Buffer
	if err := commonTemplate.Execute(&content, config); err != nil { //nolint:noinlineerr
		httpError(writer, err)
		return
	}

	writer.Header().
		Set("Content-Type", "application/x-apple-aspen-config; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content.Bytes())
}

type AppleMobileConfig struct {
	UUID    uuid.UUID
	URL     string
	Payload string
}

type AppleMobilePlatformConfig struct {
	UUID        uuid.UUID
	URL         string
	PayloadType string
}

// applePayloadType maps a platform request path to the Tailscale IPN
// PayloadType emitted in the rendered Apple profile.
var applePayloadType = map[string]string{
	"ios":              "io.tailscale.ipn.ios",
	"macos-app-store":  "io.tailscale.ipn.macos",
	"macos-standalone": "io.tailscale.ipn.macsys",
}

var commonTemplate = textTemplate.Must(
	textTemplate.New("mobileconfig").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>PayloadUUID</key>
    <string>{{.UUID}}</string>
    <key>PayloadDisplayName</key>
    <string>Orgscale</string>
    <key>PayloadDescription</key>
    <string>Configure Tailscale login server to: {{.URL}}</string>
    <key>PayloadIdentifier</key>
    <string>com.github.manoharkakumani.orgscale</string>
    <key>PayloadRemovalDisallowed</key>
    <false/>
    <key>PayloadType</key>
    <string>Configuration</string>
    <key>PayloadVersion</key>
    <integer>1</integer>
    <key>PayloadContent</key>
    <array>
    {{.Payload}}
    </array>
  </dict>
</plist>`),
)

var payloadTemplate = textTemplate.Must(textTemplate.New("payloadTemplate").Parse(`
    <dict>
        <key>PayloadType</key>
        <string>{{.PayloadType}}</string>
        <key>PayloadUUID</key>
        <string>{{.UUID}}</string>
        <key>PayloadIdentifier</key>
        <string>com.github.manoharkakumani.orgscale</string>
        <key>PayloadVersion</key>
        <integer>1</integer>
        <key>PayloadEnabled</key>
        <true/>

        <key>ControlURL</key>
        <string>{{.URL}}</string>
        <key>PostureChecking</key>
        <string>always</string>
    </dict>
`))
