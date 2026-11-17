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
| **Device visibility** | With **device restriction** on, each person's Tailscale client shows only their own devices and the devices assigned to them. |
| **Device access** | On **Devices**, open a device and pick the users and groups who may reach it. Groups follow your identity provider. |
| **Groups** | IdP groups arrive through SCIM, Google Workspace sync, or the `groups` claim at sign-in. They are usable for device access and in the policy as `group:<name>`. |
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

## Device restriction and assignments

1. **Settings → Device visibility**: turn restriction on. From now on a device
   is visible only to its owner and its grantees.
2. **Devices**: open a shared machine (a server, a NAS, a jump host) and select
   the users and groups who need it, then **Save access**.

orgscale turns this into access rules: everyone can reach their own devices
(`autogroup:member` → `autogroup:self`), and each assigned device gets a rule
letting its grantees reach it. Tailscale clients only list peers they are
allowed to talk to, so users see exactly their own and assigned devices.

The rules are merged into the policy you write on **Access policy** at compile
time; your policy is stored as written. **Show the effective policy** on that
page displays the merged result. Rules you write still apply on top, e.g. to
open specific ports to a group. A rule letting everyone reach everything
(`"src": ["*"]`, `"dst": ["*:*"]`) defeats restriction; the console warns
about it.

A device grant names the device by its addresses. Deleting a device drops its
rule immediately, before its address can be reused.

Visibility is two-way: if a group may reach a server, the server also lists
the group's devices as peers (it can't open connections to them unless a rule
allows it).

Directory group names are unique. Two IdP groups whose names normalize to the
same name (e.g. "Eng Team" from SCIM and "eng-team" from sign-in claims) get
`group:eng-team` and `group:eng-team-oidc`; check the Groups page for the
exact names before writing them into the policy.

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
