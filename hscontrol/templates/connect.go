package templates

import (
	"github.com/chasefleming/elem-go"
	"github.com/chasefleming/elem-go/attrs"
	"github.com/chasefleming/elem-go/styles"
)

// fileButton is a download link that saves under the given file name.
func fileButton(href, file, text string) *elem.Element {
	b := downloadButton(href, text)
	b.Attrs[attrs.Download] = file

	return b
}

func step(children ...elem.Node) *elem.Element { return elem.Li(nil, children...) }

func strong(s string) *elem.Element { return elem.Strong(nil, elem.Text(s)) }

// Connect is the one page people are sent to set up a device: per platform,
// the shortest way to point the Tailscale app at this server and sign in.
func Connect(url string) *elem.Element {
	return page(
		"orgscale - Connect a device",
		H1(elem.Text("Connect your device")),
		P(
			elem.Text("This network uses the Tailscale app with your organization's server, "),
			Code(elem.Text(url)),
			elem.Text(". Pick your device, follow the steps once, then sign in with your work account."),
		),
		detailsBox("Mac",
			Ol(
				step(elem.Text("Install "), externalLink("https://tailscale.com/download/mac", "Tailscale for Mac"), elem.Text(" and open it once.")),
				step(elem.Text("Download the profile below and open it. macOS says to review it in System Settings; that is expected.")),
				step(elem.Text("Open "), strong("System Settings"), elem.Text(" and click "), strong("Profile Downloaded"), elem.Text(" near the top of the sidebar (or "), strong("General → Device Management"), elem.Text("). Double-click "), strong("orgscale"), elem.Text(", click "), strong("Install"), elem.Text(" and enter your Mac password. It shows as Unverified; that is fine.")),
				step(elem.Text("Quit Tailscale from its menu bar icon, open it again, then click "), strong("Log in"), elem.Text(".")),
			),
			elem.Div(attrs.Props{attrs.Style: styles.Props{styles.MarginTop: spaceM}.ToInline()},
				fileButton("/apple/macos", "orgscale.mobileconfig", "Download Mac profile"),
			),
			P(elem.Text("Or, in Terminal: "), Code(elem.Text("tailscale login --login-server "+url))),
		),
		detailsBox("Windows",
			Ol(
				step(elem.Text("Install "), externalLink("https://tailscale.com/download/windows", "Tailscale for Windows"), elem.Text(".")),
				step(elem.Text("Download the settings file below, open it and allow the change. It needs an administrator.")),
				step(elem.Text("Click the Tailscale icon in the taskbar and choose "), strong("Log in"), elem.Text(".")),
			),
			elem.Div(attrs.Props{attrs.Style: styles.Props{styles.MarginTop: spaceM}.ToInline()},
				fileButton("/windows/orgscale.reg", "orgscale.reg", "Download Windows settings"),
			),
			P(elem.Text("Or, in PowerShell: "), Code(elem.Text("tailscale login --login-server "+url))),
		),
		detailsBox("iPhone and iPad",
			Ol(
				step(elem.Text("Install "), externalLink("https://apps.apple.com/app/tailscale/id1470499037", "Tailscale"), elem.Text(" from the App Store and open it once.")),
				step(elem.Text("Open the "), strong("Settings"), elem.Text(" app → "), strong("Apps → Tailscale"), elem.Text(" and set "), strong("Alternate Coordination Server URL"), elem.Text(" to "), Code(elem.Text(url)), elem.Text(".")),
				step(elem.Text("Go back to Tailscale and tap "), strong("Log in"), elem.Text(".")),
			),
		),
		detailsBox("Android",
			Ol(
				step(elem.Text("Install "), externalLink("https://play.google.com/store/apps/details?id=com.tailscale.ipn", "Tailscale"), elem.Text(" from Google Play.")),
				step(elem.Text("Open it, tap the account icon, then the "), strong("⋮"), elem.Text(" menu → "), strong("Use an alternate server"), elem.Text(".")),
				step(elem.Text("Enter "), Code(elem.Text(url)), elem.Text(" and sign in.")),
			),
		),
		detailsBox("Linux",
			P(elem.Text("Run this; it installs Tailscale if needed and starts sign-in:")),
			codeBlockText("curl -fsSL "+url+"/connect/linux.sh | sh"),
		),
		P(elem.Text("Your device belongs to the account you sign in with. Signed in already on another network? Log out of that account in Tailscale first, or add this one as a second account.")),
	)
}
