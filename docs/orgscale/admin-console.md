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

## Configuration

The console needs OIDC. Minimal configuration:

```yaml
server_url: https://vpn.example.com

oidc:
  issuer: https://accounts.google.com          # your IdP's issuer URL
  client_id: <client id>
  client_secret: file:/run/secrets/oidc_secret
  scope: ["openid", "profile", "email", "groups"]  # "groups" if your IdP supports it

policy:
  mode: database   # lets the console edit the access policy

admin:
  enabled: true
  users: ["you@example.com"]   # first admin(s); add more from the console
  # groups: ["it-admins"]      # or: everyone in these IdP groups is an admin
  # session_timeout: 12h
```

Register **two redirect URLs** with your identity provider (also shown in
**Settings → Single sign-on**):

- `https://vpn.example.com/oidc/callback` for devices,
- `https://vpn.example.com/admin/auth/callback` for the console.

Then open `https://vpn.example.com/admin/` and sign in.

Anything set in the configuration file (`oidc.allowed_*`, `admin.users`,
`admin.groups`) still applies. Sign-in rules saved in the console replace the
file's rules; admin and viewer lists from the console are added to the file's.

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
with an admin's login) can be turned into a server: select it on **Devices**
and choose **Move to tag**. It stops belonging to that person.

### Rules

**Access → New rule**: choose *who*, *can reach* and *on* (everything, SSH,
web, Remote Desktop, PostgreSQL, MySQL, ping, or custom ports). Examples:

| Need | Who | Can reach | On |
|---|---|---|---|
| Web servers talk to databases | `tag:web` | `tag:databases` | PostgreSQL |
| Engineers SSH into staging | group Engineering | `tag:staging` | SSH |
| Support uses one kiosk | group Support | device `kiosk-1` | Everything |

Rules can be paused and resumed. Every change is in the audit log.

### Advanced: policy file

**Access → Advanced: policy file** keeps the Tailscale policy editor for
experts. Anything written there applies in addition to the console's rules;
nobody needs it for everyday work. A rule there letting everyone reach
everything (`"src": ["*"]`, `"dst": ["*:*"]`) defeats access control; the
console warns about it.

Visibility is two-way: if a group may reach a server, the server also lists
the group's devices as peers (it can't open connections to them unless a rule
allows it).

## Directory sync

### SCIM (Okta, Microsoft Entra ID, OneLogin, JumpCloud, Authentik)

1. **Settings → Directory sync → SCIM provisioning → Turn on and create token**.
   Copy the token; it is shown once.
2. In your IdP's provisioning settings use the SCIM base URL
   `https://vpn.example.com/scim/v2` and the token as a bearer token.
3. Assign the people and groups who should have VPN access to the app.

- **Okta**: enable SCIM provisioning on the app, unique identifier `userName`,
  auth mode *HTTP Header*, then enable *Create*, *Update* and *Deactivate
  Users* and push groups.
- **Microsoft Entra ID**: Enterprise application → Provisioning → Automatic,
  Tenant URL and Secret Token, *Test connection*, *Start provisioning*.
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
- Console sessions live in memory: restarting orgscale signs admins out.
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
top bar overrides it per browser.
