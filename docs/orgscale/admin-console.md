# orgscale admin console

orgscale adds a web admin console, single sign-on driven user management and
per-device access control to the Headscale control server. Users connect with
the standard Tailscale clients; administrators manage everything from the
browser at `https://<your server>/admin/`.

## What it does

| Area | What you get |
|---|---|
| **Sign-in** | Users and admins sign in with your identity provider (any OpenID Connect provider: Google, Microsoft Entra ID, Okta, Keycloak, Authentik, …). Users are created on first sign-in; nobody creates accounts by hand. |
| **Who can join** | Allowed email domains, people and IdP groups, edited in **Settings → Who can sign in**. Stored in the database; no restart. |
| **Access control** | With **access control** on (**Access** page), each person sees only their own devices and whatever they have been given. People never see each other's devices. |
| **Access rules** | Point-and-click rules: *who* (people, groups, server tags, devices, everyone) *can reach* what, on which ports. No policy JSON. |
| **Servers** | Servers, VMs and containers join with a one-line install command and belong to a **tag** instead of a person. Each tag has an access list; servers can be allowed to reach each other or other tags. |
| **Device access** | On **Devices**, open a device and pick the users and groups who may reach it, or select several devices to share them or move them to a tag at once. |
| **Groups** | Make groups on the **Groups** page, or bring them from your IdP through SCIM, Google Workspace sync, or the `groups` claim at sign-in. |
| **Offboarding** | Deactivating or deleting someone in your IdP (SCIM), or suspending them in Google Workspace, removes the user, their devices, personal keys and access grants. **Disable** in the console does the same but keeps the user so they can be re-enabled. |
| **Roles** | Admins change everything; viewers have read-only access. Set by email or IdP group. |
| **Audit log** | Every change from the console, the API (with the API key prefix), SCIM and directory sync, with who made it. |

## First-time setup

The console is on by default. A new installation has no identity provider,
so orgscale starts in **setup mode** and writes a one-time link to its log:

```
  orgscale is not set up yet. To finish setup, open

      https://vpn.example.com/admin/setup#oskey-setup-…

  within 30 minutes. The link works once.
```

Open it and:

1. pick your identity provider (Google Workspace, Microsoft Entra ID, Okta or
   any OpenID Connect provider) and follow the steps shown to create the app,
   registering the two redirect URLs it lists;
2. paste the client ID and secret;
3. enter your own work email as the first administrator;
4. **Connect and sign in**. orgscale checks the provider, switches to it
   without a restart, and sends you to sign in. Only the email you entered is
   let in. When you are signed in, setup closes: the link and the setup page
   stop working.

The link expired or was lost? On the server run `orgscale admin setup-link`
(with Docker: `docker compose exec orgscale orgscale admin setup-link`). It
only works over the server's local socket, so it needs shell access to the
server. The same command recovers access if nobody can sign in any more,
for example after the identity provider changed.

There is no built-in admin account or password: every administrator signs in
through your identity provider, with its MFA and offboarding.

### Configuring single sign-on in a file instead

Single sign-on can also be set in the configuration file or environment, as
in Headscale. The file then wins and the console shows it read-only:

```yaml
server_url: https://vpn.example.com

oidc:
  issuer: https://accounts.google.com
  client_id: <client id>
  client_secret: file:/run/secrets/oidc_secret
  scope: ["openid", "profile", "email", "groups"]  # "groups" if your IdP supports it

policy:
  mode: database   # lets the console edit access

admin:
  users: ["you@example.com"]   # first admin(s); add more from the console
  # groups: ["it-admins"]      # or: everyone in these IdP groups is an admin
  # session_timeout: 12h
```

Register the redirect URLs `https://vpn.example.com/oidc/callback` (devices)
and `https://vpn.example.com/admin/auth/callback` (console).

Sign-in rules, admins and viewers saved in the console are added to the
file's; sign-in rules saved in the console replace the file's rules.

### HTTPS

The console is only served when `server_url` is `https://`, or on localhost.
On plain HTTP elsewhere it shows how to fix it instead. For a lab only,
`admin.allow_insecure_http: true` (`ORGSCALE_ADMIN_ALLOW_INSECURE_HTTP=true`)
overrides this.

## Access

Open **Access** and turn on **access control**. From then on:

- everyone reaches their own devices (a person's laptop and phone see each
  other), and nobody sees anyone else's devices;
- servers are reached only as their tag's access list and the rules allow;
- shared devices are reached by the people and groups picked on **Devices**.

Before access control is on, everyone can reach everything (the Tailscale
default).

### Servers and tags

1. **Servers → New tag**: name it (e.g. `databases`) and choose who can reach
   these servers: people, groups, other tags, or the tag itself so its
   servers reach each other.
2. **Add server**: pick the tag, whether the command is for one server,
   several, or short-lived containers, and how long it stays valid (1 hour to
   360 days). Run the command it shows (Linux, Docker, Windows or macOS). The
   server joins as the tag, owned by no person.

A machine already added with someone's account (a shared box, a VM set up
with an admin's login) can be turned into a server: open it on **Devices**
and use **Turn into a server**, or select several and choose **Turn into
servers**. It stops belonging to that person.

Tags can be typed anywhere a tag is asked for (Add server, a device's tags,
auth keys); a new name is created on the spot. A new tag's servers are
reachable by nobody until you choose who on **Servers → Edit access**.

### Rules

**Access → New rule**: choose *who*, *can reach* and *on* (everything, SSH,
web, Remote Desktop, PostgreSQL, MySQL, ping, or custom ports). Examples:

| Need | Who | Can reach | On |
|---|---|---|---|
| Web servers talk to databases | `tag:web` | `tag:databases` | PostgreSQL |
| Engineers SSH into staging | group Engineering | `tag:staging` | SSH |
| Support uses one kiosk | group Support | device `kiosk-1` | Everything |

Rules can be paused and resumed. Every change is in the audit log.

### SSH without keys

orgscale uses Tailscale SSH: nobody creates, copies or rotates SSH keys.

1. Enroll servers with SSH on: **Add server** has *Allow SSH through
   Tailscale* (adds `--ssh`). On a server already enrolled run
   `sudo tailscale set --ssh`.
2. **Access → New SSH rule**: choose *who* (people, groups or everyone),
   *which servers* (tags, or *their own devices*), *as which Linux accounts*
   (`ubuntu`, `root`, … or *any non-root account*) and, optionally, *re-confirm
   with single sign-on* every 1 hour to 7 days.
3. People run plain `ssh ubuntu@db-1`. Tailscale checks who they are against
   the rule; with re-confirmation on, their browser asks them to sign in
   first.

Only people in an SSH rule can SSH in, even if they may reach the server
otherwise. Under access control an SSH rule also opens port 22 to those
servers for those people. Removing someone in your identity provider ends
their SSH access with everything else.

### Temporary access on request

For servers people should reach only now and then (production databases,
customer environments), let them ask instead of giving standing access:

1. **Servers → edit a tag → Temporary access on request**: turn it on, pick
   the longest access allowed (1 hour to 7 days) and, optionally, the Linux
   accounts people may also SSH in as (`ubuntu`).
2. People sign in to `https://vpn.example.com/admin/` with single sign-on.
   Anyone with an orgscale account who is not an administrator sees only a
   **Request access** page: they pick the tag, a duration and write why.
3. Administrators see the request under **Requests** (with a count in the
   sidebar) and approve, approve for less time, or deny, with a note.
   Nobody can approve their own request.
4. Approved access is added to the policy at once and removed when the time
   is up (checked every 20 seconds). The requester or an administrator can
   end it early. Requests nobody decides within a day lapse.

Every step is in the audit log. With access control off everyone reaches
everything anyway, so only the SSH part of a request then makes a
difference.

### Advanced: policy file

**Access → Advanced: policy file** keeps the Tailscale policy editor for
experts. Anything written there applies in addition to the console's rules;
nobody needs it for everyday work. A rule there letting everyone reach
everything (`"src": ["*"]`, `"dst": ["*:*"]`) defeats access control; the
console warns about it.

Visibility is two-way: if a group may reach a server, the server also lists
the group's devices as peers (it can't open connections to them unless a rule
allows it).

## DNS

**Settings → DNS** controls what devices get, live, with no restart:

- **MagicDNS**: devices reach each other by name (`db-server`, or
  `db-server.<base domain>`). The name domain itself (`dns.base_domain`)
  stays in the config file, since changing it renames every device.
- **DNS servers**: IP addresses or `https://` DNS-over-HTTPS addresses.
  Devices use them as a fallback, or instead of their own DNS when
  **Use instead of devices' own DNS** is on (for central filtering or
  logging).
- **Split DNS**: send lookups for internal domains (`corp.example.com`) to
  your own DNS servers, e.g. the domain controller reachable through a
  subnet router.
- **Search domains**: short names such as `intranet`.
- **Custom records**: names that resolve to an address, e.g.
  `grafana.<base domain>` → `100.64.0.9`. While `dns.extra_records_path` is
  set, that file manages the records instead.

Saving overrides the config file's `dns.*` settings; **Use the config
file's settings** goes back to them.

## Connecting devices

People use the official Tailscale app, pointed at your server. Send everyone
**`https://vpn.example.com/connect`** (also on the console's Overview): it has
each platform's steps and downloads the settings, so nobody types a command.

| Platform | How |
|---|---|
| Managed Mac (Intune, Jamf, Kandji) | Upload `https://vpn.example.com/apple/macos` as a custom configuration profile. |
| Managed Windows | Set `HKLM\SOFTWARE\Policies\Tailscale\LoginURL` to your server URL (Settings catalog, OMA-URI or a script), or deploy `https://vpn.example.com/windows/orgscale.reg`. |
| Managed iPhone / iPad | App configuration policy for Tailscale: `ControlURL` = your server URL. |
| Managed Android | Managed configuration for Tailscale: `LoginURL` = your server URL. |
| Mac (own) | Install the profile from `/connect`, then **Log in** in the Tailscale menu. |
| Windows (own) | Open the settings file from `/connect`, then **Log in**. |
| iPhone / iPad (own) | Settings → Apps → Tailscale → Alternate Coordination Server URL. |
| Android (own) | Account → ⋮ → **Use an alternate server**. |
| Linux | `curl -fsSL https://vpn.example.com/connect/linux.sh \| sh` |

Devices remember the server address they joined with. Use a permanent name
(such as `vpn.example.com`) before connecting people: with a temporary tunnel
address (ngrok, trycloudflare) every device must be set up again when it
changes, and the Overview warns about it.

Signing in opens your identity provider; the device is registered to the
person who signs in. Servers use **Servers → Add server** instead.

## Directory sync

### Microsoft Entra ID

orgscale reads users and groups from Entra through Microsoft Graph, using the
single sign-on app registration (or another one you enter):

1. In entra.microsoft.com → **App registrations** → your orgscale app →
   **API permissions** → **Add a permission → Microsoft Graph → Application
   permissions**: `User.Read.All`, `Group.Read.All`, `GroupMember.Read.All`
   and `Organization.Read.All`. Then **Grant admin consent**. The **Type**
   column must say **Application**; **Delegated** permissions with the same
   names (the kind sign-in uses) do not work for sync.
   (`Organization.Read.All` lets orgscale see the tenant's verified domains,
   so people deleted from Entra are removed even when nobody else uses their
   domain. Without it the sync works but warns.)
2. **Settings → Microsoft Entra ID**: turn sync on, **Save**, **Sync now**.

Each check asks Graph only what changed since the last one (delta queries):
two small requests when nothing did. The directory is read in full when
something changed, and once a day. Checks run every 15 minutes by default (5
minutes to a day). Members are added; accounts disabled or deleted in Entra
are removed with their devices; groups (with nested members) follow Entra.
Guests are not synced. The same 25-user / half-of-all safety limit as Google
sync applies. No public URL is needed.

SCIM (below) also works with Entra, through a separate non-gallery
enterprise app; it pushes changes instantly but needs orgscale reachable from
the internet.

### SCIM (Okta, Microsoft Entra ID, OneLogin, JumpCloud, Authentik)

1. **Settings → Directory sync → SCIM provisioning → Turn on and create token**.
   Copy the token; it is shown once.
2. In your IdP's provisioning settings use the SCIM base URL
   `https://vpn.example.com/scim/v2` and the token as a bearer token.
3. Assign the people and groups who should have VPN access to the app.

- **Okta**: enable SCIM provisioning on the app, unique identifier `userName`,
  auth mode *HTTP Header*, then enable *Create*, *Update* and *Deactivate
  Users* and push groups.
- **Microsoft Entra ID**: Enterprise applications → New application →
  Create your own application → *Non-gallery*. (Apps made under App
  registrations cannot provision.) Then Provisioning → Automatic, Tenant URL
  and Secret Token, *Test connection*, *Start provisioning*.
- **Authentik**: create a SCIM provider and attach it to the application as a
  backchannel provider.

Users are matched by email. A user provisioned before their first sign-in is
taken over by that sign-in, so they keep their group memberships and grants.
The takeover requires the identity provider to mark the email **verified**.

Deactivating or deleting someone over SCIM removes them from orgscale. Turn on
"assignment required" (Entra ID) or app assignment (Okta) in your IdP, so a
person removed from the app cannot simply sign in again and be recreated.

### Google Workspace

Google does not push SCIM to custom apps, so orgscale reads the directory on a
schedule (every 15 minutes by default):

1. In Google Cloud, enable the **Admin SDK API**, create a service account and
   a JSON key.
2. In the Workspace Admin console → Security → API controls → Domain-wide
   delegation, add the service account's client ID with the scopes
   `admin.directory.user.readonly`, `admin.directory.group.readonly` and
   `admin.directory.group.member.readonly` (full URLs are shown in the console).
3. **Settings → Google Workspace sync**: paste the key, enter a Workspace admin
   email to impersonate, turn sync on, **Save**, then **Sync now**.

Removal rules for each run:

- any user whose address Workspace marks **suspended or archived** is removed;
- users created by this sync, or who signed in with SSO, are removed when
  their address is in one of your Workspace domains but belongs to no active
  account. Aliases count as belonging to the account, and Google keeps a
  renamed account's old address as an alias;
- SCIM, local and other-domain users are never removed for a missing address;
- a run that would remove more than 25 users, or more than half of all users,
  stops with an error instead.

Impersonate a super admin, so the sync sees the whole directory.

## Security notes

- Console sessions are server-side; the browser holds an `HttpOnly`,
  `SameSite=Strict` cookie, and every API call also needs the session's CSRF
  token. The console is served with a strict Content-Security-Policy.
- Console sessions are stored in the database (only a hash of the cookie),
  so they survive restarts. **Settings → Console access** lists them; sign
  out any session, or all your other ones.
- Sign-in and setup are rate-limited per client address; a client that keeps
  sending wrong SCIM tokens is blocked for a minute.
- Over HTTPS the server sends `Strict-Transport-Security`.
- A console role is checked again on every request: removing someone from
  the admin or viewer lists takes effect immediately.
- Console roles by email, and the takeover of provisioned users, only trust
  emails the identity provider marks verified.
- A console session can call `/api/v1` and `/api/v2` with full rights
  (viewers: read-only). API keys keep working for scripts and Terraform.
- The SCIM token and API keys are stored only as hashes. The Google service
  account key is stored in the database (never returned by the API); protect
  the database file. Its token is only ever sent to Google's token endpoint.
- Removing a person from **Who can sign in** stops new sign-ins only; use
  **Disable** or **Remove** to cut off someone who is already connected.

## Developing the console

```bash
cd web/admin
npm ci
npm run dev        # Vite dev server, proxies the API to 127.0.0.1:8080
make admin-ui      # from the repository root: build into hscontrol/adminui/dist
```

The built files are committed so `go build` works without Node.js.

The console follows the system's light or dark appearance; the switch in the
top bar overrides it per browser. It works on phones and tablets: the menu
slides in, tables become cards and dialogs open as sheets.
